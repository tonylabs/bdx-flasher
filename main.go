package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	appID      = "org.openbdx.bdx-flasher"
	appName    = "BDX Flasher"
	appVersion = "1.0.0"
)

type options struct {
	cli          bool
	board        string
	image        string
	device       string
	eraseAll     bool
	yes          bool
	listDevices  bool
	showInternal bool
	target       string
	loader       string
	uiScale      float64
	version      bool
	jetson       jetsonOpts
}

func main() {
	if len(os.Args) > 2 && os.Args[1] == "--helper" {
		os.Exit(helperMain(os.Args[2], os.Args[3:]))
	}

	var o options
	var forceDownload, downloadOnly bool
	var cacheRoot, workRoot string
	flag.BoolVar(&o.cli, "cli", false, "run terminal mode instead of GUI")
	flag.StringVar(&o.board, "board", "", "main board: "+strings.Join(boardSlugs(), ", "))
	flag.StringVar(&o.image, "image", "", "image boards: BDX disk image (.img or .img.xz); default is the newest one found")
	flag.StringVar(&o.device, "device", "", "image boards: target disk, e.g. /dev/sdb or /dev/mmcblk0")
	flag.StringVar(&o.target, "target", "drive", "image boards: drive (microSD / USB-attached disk) or maskrom (onboard eMMC over USB)")
	flag.StringVar(&o.loader, "loader", "", "maskrom: Rockchip SPL loader to use instead of the downloaded one")
	flag.BoolVar(&o.eraseAll, "erase-all", false, "clean install: reinstall system and reset all /data (not secure erase)")
	flag.BoolVar(&o.yes, "yes", false, "CLI mode only: skip typed confirmation")
	flag.BoolVar(&o.listDevices, "list-devices", false, "list disks that can be flashed and exit")
	flag.BoolVar(&o.showInternal, "show-internal", false, "also offer non-removable disks (never the disk this computer runs from)")
	flag.Float64Var(&o.uiScale, "ui-scale", 1.15, "GUI scale multiplier for readability")
	flag.BoolVar(&o.version, "version", false, "print version")
	flag.StringVar(&o.jetson.ManifestURL, "manifest-url", "", "Jetson boards: firmware.ini URL or local file (passed to the BDX Orin Flasher)")
	flag.StringVar(&o.jetson.LocalBundle, "local-bundle", "", "Jetson boards: local NVIDIA MFI .tar.gz bundle")
	flag.StringVar(&o.jetson.LocalSHA256, "local-sha256", "", "Jetson boards: SHA256 sidecar for --local-bundle")
	flag.StringVar(&o.jetson.LocalEnv, "local-env", "", "Jetson boards: BDX-FLASH-BUNDLE.env sidecar for --local-bundle")
	flag.BoolVar(&forceDownload, "force-download", false, "Jetson boards: re-download firmware even if cached")
	flag.BoolVar(&downloadOnly, "download-only", false, "Jetson boards: download and verify, but do not flash")
	flag.StringVar(&cacheRoot, "cache-root", "", "Jetson boards: firmware cache root")
	flag.StringVar(&workRoot, "work-root", "", "Jetson boards: directory for extracting flash bundles")
	flag.Parse()

	if o.version {
		fmt.Printf("%s %s\n", appName, appVersion)
		return
	}
	if forceDownload {
		o.jetson.Extra = append(o.jetson.Extra, "--force-download")
	}
	if downloadOnly {
		o.jetson.Extra = append(o.jetson.Extra, "--download-only")
	}
	if cacheRoot != "" {
		o.jetson.Extra = append(o.jetson.Extra, "--cache-root", cacheRoot)
	}
	if workRoot != "" {
		o.jetson.Extra = append(o.jetson.Extra, "--work-root", workRoot)
	}
	o.jetson.EraseAll = o.eraseAll
	if o.board != "" {
		if _, ok := boardBySlug(o.board); !ok {
			fmt.Fprintf(os.Stderr, "unknown board %q (choose one of: %s)\n", o.board, strings.Join(boardSlugs(), ", "))
			os.Exit(2)
		}
	}

	var err error
	switch {
	case o.listDevices:
		err = printDevices(o.showInternal)
	case o.cli:
		err = runCLI(o)
	default:
		err = runGUI(o)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func helperMain(cmd string, args []string) int {
	ev := &stdoutEmitter{}
	fs := flag.NewFlagSet("helper", flag.ContinueOnError)
	var err error
	switch cmd {
	case "write":
		var o writeOpts
		fs.StringVar(&o.Image, "image", "", "")
		fs.StringVar(&o.Device, "device", "", "")
		fs.BoolVar(&o.Clean, "erase-all", false, "")
		if fs.Parse(args) != nil || o.Image == "" || o.Device == "" {
			fmt.Println("@@ERROR helper needs --image and --device")
			return 2
		}
		err = runWrite(o, ev)
	case "maskrom":
		var o maskromOpts
		var slug string
		fs.StringVar(&slug, "board", "", "")
		fs.StringVar(&o.Image, "image", "", "")
		fs.StringVar(&o.Loader, "loader", "", "")
		var b Board
		var ok bool
		if fs.Parse(args) != nil || o.Image == "" || o.Loader == "" {
			fmt.Println("@@ERROR helper needs --board, --image and --loader")
			return 2
		}
		if b, ok = boardBySlug(slug); !ok || b.MaskromUSBID == "" {
			fmt.Printf("@@ERROR board %q has no maskrom support\n", slug)
			return 2
		}
		err = runMaskrom(b, o, ev)
	default:
		fmt.Printf("@@ERROR unknown helper command %q\n", cmd)
		return 2
	}
	if err != nil {
		fmt.Printf("@@ERROR %s\n", strings.ReplaceAll(err.Error(), "\n", " "))
		return 1
	}
	ev.Phase("Done")
	return 0
}

func printDevices(showInternal bool) error {
	disks, err := listDisks()
	if err != nil {
		return err
	}
	for _, d := range disks {
		note := ""
		switch {
		case d.System:
			note = "  [this computer's system disk — never offered]"
		case d.ReadOnly:
			note = "  [read-only]"
		case !d.Removable && !showInternal:
			note = "  [internal — use --show-internal]"
		case d.HasData:
			note = "  [existing BDX DATA partition]"
		}
		fmt.Println(d.Label() + note)
	}
	return nil
}

// ---- terminal mode ----

var stdin = bufio.NewReader(os.Stdin)

func ask(prompt string) string {
	fmt.Print(prompt)
	line, _ := stdin.ReadString('\n')
	return strings.TrimSpace(line)
}

func choose(title string, items []string) (int, error) {
	fmt.Println(title)
	for i, it := range items {
		fmt.Printf("  %d) %s\n", i+1, it)
	}
	n, err := strconv.Atoi(ask("Choice: "))
	if err != nil || n < 1 || n > len(items) {
		return 0, errors.New("no valid choice made")
	}
	return n - 1, nil
}

func runCLI(o options) error {
	board, ok := boardBySlug(o.board)
	if !ok {
		i, err := choose("Select the main board:", boardNames())
		if err != nil {
			return err
		}
		board = boards[i]
	}
	fmt.Printf("Main board: %s (%s)\n", board.Name, board.SoC)
	if o.target != "drive" && o.target != "maskrom" {
		return fmt.Errorf("unknown --target %q (drive or maskrom)", o.target)
	}
	if o.target == "maskrom" && board.MaskromUSBID == "" {
		return fmt.Errorf("%s cannot be flashed in maskrom mode", board.Name)
	}

	if board.Method == MethodJetson {
		if orinFlasherPath() == "" {
			return errors.New("Jetson boards are flashed by the BDX Orin Flasher, which is not installed (sudo apt install bdx-orin-flasher)")
		}
		fmt.Println("Handing over to the BDX Orin Flasher (it detects the board from its recovery-mode USB ID).")
		return execOrinFlasherCLI(o.jetson)
	}
	if o.target == "maskrom" {
		return runMaskromCLI(board, o)
	}
	return runImageCLI(board, o)
}

func cliImage(board Board, o options) (ImageInfo, error) {
	path := o.image
	if path == "" {
		if path = findDefaultImage(board); path == "" {
			return ImageInfo{}, fmt.Errorf("no %s image found; pass --image", board.ImageGlob)
		}
	}
	info, err := inspectImage(path)
	if err != nil {
		return info, err
	}
	fmt.Printf("Image:      %s\n            release %s, %s on disk\n", info.Path, orUnknown(info.Release), humanBytes(info.RawSize))
	if info.SidecarSHA != "" {
		fmt.Println("Verifying image checksum...")
		if err := verifySidecar(info, cliProgress("checksum")); err != nil {
			fmt.Println()
			return info, err
		}
		fmt.Println("\nChecksum OK.")
	} else {
		fmt.Println("Warning: no .sha256 file next to the image; skipping the download check.")
	}
	return info, nil
}

func runMaskromCLI(board Board, o options) error {
	if rkdeveloptoolPath() == "" {
		return errors.New("rkdeveloptool is not installed (sudo apt install rkdeveloptool)")
	}
	if !usbPresent(board.MaskromUSBID) {
		return fmt.Errorf("no %s in Maskrom mode found (USB %s). Hold the Maskrom button while powering the board, with its OTG port connected to this computer", board.Name, board.MaskromUSBID)
	}
	fmt.Printf("Target:     onboard eMMC over USB (Maskrom, USB %s)\n", board.MaskromUSBID)
	info, err := cliImage(board, o)
	if err != nil {
		return err
	}
	loader, err := ensureLoader(board, o.loader)
	if err != nil {
		return err
	}
	fmt.Printf("Loader:     %s\n", loader)
	fmt.Println()
	fmt.Println("WARNING: clean install. Everything on the board's eMMC, including /data, will be erased.")
	phrase := "erase and flash emmc"
	if o.yes {
		fmt.Println("--yes given; not asking for confirmation.")
	} else if ask(fmt.Sprintf("Type '%s' to continue: ", phrase)) != phrase {
		return errors.New("not confirmed; nothing was written")
	}
	raw, temp, err := prepareRawImage(info, cliProgress("decompress"))
	if err != nil {
		return err
	}
	if temp {
		fmt.Println()
	}
	var launcher []string
	if os.Geteuid() != 0 {
		launcher = []string{"sudo"}
	}
	err = runHelper(launcher, maskromOpts{Image: raw, Loader: loader}.helperArgs(board), cliEvents(cliProgress("")))
	if err != nil {
		return err
	}
	if temp {
		os.Remove(raw)
		os.Remove(raw + ".source")
	}
	fmt.Printf("\nFlash complete. The %s restarts from its eMMC (remove any microSD card first).\n", board.Name)
	return nil
}

func cliEvents(progress func(done, total int64)) func(helperEvent) {
	return func(e helperEvent) {
		switch e.Kind {
		case "phase":
			fmt.Printf("\n==> %s\n", e.Text)
		case "log":
			fmt.Printf("    %s\n", e.Text)
		case "error":
			fmt.Printf("\nERROR: %s\n", e.Text)
		case "progress":
			progress(e.Done, e.Total)
		}
	}
}

func runImageCLI(board Board, o options) error {
	dev := o.device
	if dev == "" {
		disks, err := candidateDisks(o.showInternal)
		if err != nil {
			return err
		}
		if len(disks) == 0 {
			return errors.New("no removable disk found; insert the microSD card or USB adapter (or use --show-internal)")
		}
		labels := make([]string, len(disks))
		for i, d := range disks {
			labels[i] = d.Label()
		}
		i, err := choose("Select the target disk:", labels)
		if err != nil {
			return err
		}
		dev = disks[i].Path
	}
	disk, err := findDisk(dev)
	if err != nil {
		return err
	}
	if disk.System {
		return fmt.Errorf("%s holds this computer's own system; refusing", disk.Path)
	}
	if !disk.Removable && !o.showInternal {
		return fmt.Errorf("%s is an internal disk; pass --show-internal if you really mean it", disk.Path)
	}
	fmt.Printf("Target:     %s\n", disk.Label())
	info, err := cliImage(board, o)
	if err != nil {
		return err
	}
	if min := info.RawSize + minSpare; disk.Size < min {
		return fmt.Errorf("%s is %s; this image needs at least %s (%d GB recommended)", disk.Path, humanBytes(disk.Size), humanBytes(min), board.MinMediaGiB)
	}

	clean := o.eraseAll
	if !clean && disk.HasData {
		if _, err := planKeepData(disk.Path, info.RawSize); err != nil && os.Geteuid() == 0 {
			return err
		}
		fmt.Println("Existing BDX /data on the target will be kept (use --erase-all to reset it).")
	}
	fmt.Println()
	if clean {
		fmt.Printf("WARNING: clean install. Everything on %s, including /data (user files, settings, recordings, keys, HTTPS certificates), will be erased.\n", disk.Path)
	} else {
		fmt.Printf("WARNING: the system on %s will be replaced. Other partitions on it are erased.\n", disk.Path)
	}
	phrase := "flash " + disk.Name
	if clean {
		phrase = "erase and flash " + disk.Name
	}
	if o.yes {
		fmt.Println("--yes given; not asking for confirmation.")
	} else if ask(fmt.Sprintf("Type '%s' to continue: ", phrase)) != phrase {
		return errors.New("not confirmed; nothing was written")
	}

	var launcher []string
	if os.Geteuid() != 0 {
		launcher = []string{"sudo"}
	}
	progress := cliProgress("")
	err = runHelper(launcher, writeOpts{Image: info.Path, Device: disk.Path, Clean: clean}.helperArgs(), cliEvents(progress))
	if err != nil {
		return err
	}
	fmt.Printf("\nFlash complete. Move the media to the %s and power it on.\n", board.Name)
	return nil
}

func cliProgress(label string) func(done, total int64) {
	last := -1
	return func(done, total int64) {
		if total <= 0 {
			return
		}
		pct := int(done * 100 / total)
		if pct == last {
			return
		}
		last = pct
		fmt.Printf("\r    %s%3d%%  %s / %s   ", label+" ", pct, humanBytes(done), humanBytes(total))
	}
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
