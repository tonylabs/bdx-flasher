package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	gib      = int64(1) << 30
	sector   = int64(512)
	chunk    = 4 << 20
	minSpare = 2 * gib // room the board needs past the image for DATA
)

// The privileged half of the image flasher.  It runs as root (pkexec/sudo
// re-executes this binary with --helper write) and reports to the unprivileged
// GUI/CLI through tagged stdout lines.

type emitter interface {
	Phase(string)
	Progress(done, total int64)
	Log(string)
}

type stdoutEmitter struct{ last time.Time }

func (e *stdoutEmitter) Phase(s string) { fmt.Printf("@@PHASE %s\n", s) }
func (e *stdoutEmitter) Log(s string) {
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		fmt.Printf("@@LOG %s\n", line)
	}
}
func (e *stdoutEmitter) Progress(done, total int64) {
	if done < total && time.Since(e.last) < 250*time.Millisecond {
		return
	}
	e.last = time.Now()
	fmt.Printf("@@PROGRESS %d %d\n", done, total)
}

type writeOpts struct {
	Image  string
	Device string
	Clean  bool // reset /data instead of carrying the DATA partition over
}

// gptPart / gptTable mirror `sfdisk --json`.
type gptPart struct {
	Node  string `json:"node"`
	Start int64  `json:"start"`
	Size  int64  `json:"size"`
	Type  string `json:"type"`
	UUID  string `json:"uuid"`
	Name  string `json:"name"`
}

type gptTable struct {
	Label      string    `json:"label"`
	SectorSize int64     `json:"sectorsize"`
	Partitions []gptPart `json:"partitions"`
}

func readTable(dev string) (*gptTable, error) {
	out, err := exec.Command("sfdisk", "--json", dev).Output()
	if err != nil {
		return nil, err
	}
	var doc struct {
		PartitionTable gptTable `json:"partitiontable"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, err
	}
	if doc.PartitionTable.SectorSize == 0 {
		doc.PartitionTable.SectorSize = sector
	}
	return &doc.PartitionTable, nil
}

func partNumber(disk, node string) (int, error) {
	return strconv.Atoi(strings.TrimPrefix(strings.TrimPrefix(node, disk), "p"))
}

// keepPlan records an existing BDX DATA partition so it can be put back after
// the image overwrites the partition table.
type keepPlan struct {
	RootNum   int
	RootStart int64
	RootSize  int64
	Data      gptPart
	DataNum   int
}

// planKeepData returns nil when the disk has no DATA partition.  It fails when
// DATA exists but the layout is not the one bdx-rock-prepare-data creates, so
// the user has to choose a clean install knowingly.
func planKeepData(disk string, rawSize int64) (*keepPlan, error) {
	t, err := readTable(disk)
	if err != nil {
		return nil, nil // no readable partition table: nothing to keep
	}
	var data *gptPart
	for i := range t.Partitions {
		if t.Partitions[i].Name == "DATA" {
			data = &t.Partitions[i]
		}
	}
	if data == nil {
		return nil, nil
	}
	unsupported := func(why string) (*keepPlan, error) {
		return nil, fmt.Errorf("%s has a DATA partition, but %s; /data cannot be kept. Choose a clean install to reset it", disk, why)
	}
	if t.Label != "gpt" || t.SectorSize != sector {
		return unsupported("the disk is not a 512-byte-sector GPT disk")
	}
	if len(t.Partitions) != 2 {
		return unsupported(fmt.Sprintf("it has %d partitions instead of rootfs + DATA", len(t.Partitions)))
	}
	root := t.Partitions[0]
	if root.Name == "DATA" {
		root = t.Partitions[1]
	}
	if root.Start+root.Size > data.Start {
		return unsupported("DATA does not follow the system partition")
	}
	if data.Start*sector < rawSize {
		return unsupported("the new image would overwrite the start of DATA")
	}
	p := &keepPlan{RootStart: root.Start, RootSize: root.Size, Data: *data}
	if p.RootNum, err = partNumber(disk, root.Node); err != nil {
		return unsupported("its partition numbers cannot be read")
	}
	if p.DataNum, err = partNumber(disk, data.Node); err != nil {
		return unsupported("its partition numbers cannot be read")
	}
	return p, nil
}

func run(ev emitter, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if s := strings.TrimSpace(string(out)); s != "" {
		ev.Log(s)
	}
	if err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func runWrite(o writeOpts, ev emitter) error {
	if os.Geteuid() != 0 {
		return errors.New("the image writer must run as root")
	}
	info, err := inspectImage(o.Image)
	if err != nil {
		return err
	}
	disk, err := findDisk(o.Device)
	if err != nil {
		return err
	}
	switch {
	case disk.System:
		return fmt.Errorf("%s holds this computer's own system (%s); refusing to overwrite it", disk.Path, strings.Join(disk.Mounts, ", "))
	case disk.ReadOnly:
		return fmt.Errorf("%s is read-only (check the SD card's lock switch)", disk.Path)
	case disk.Size < info.RawSize+minSpare:
		return fmt.Errorf("%s is %s; this image needs at least %s", disk.Path, humanBytes(disk.Size), humanBytes(info.RawSize+minSpare))
	}

	var keep *keepPlan
	if !o.Clean {
		if keep, err = planKeepData(disk.Path, info.RawSize); err != nil {
			return err
		}
	}
	switch {
	case keep != nil:
		ev.Log(fmt.Sprintf("Existing DATA partition %d (%s) will be kept.", keep.DataNum, humanBytes(keep.Data.Size*sector)))
	case disk.HasData:
		ev.Log("Clean install: the existing DATA partition will be reset on first boot.")
	default:
		ev.Log("No existing DATA partition; the board creates it on first boot.")
	}

	ev.Phase("Unmounting " + disk.Path)
	mounts := append([]string(nil), disk.Mounts...)
	sort.Slice(mounts, func(i, j int) bool { return len(mounts[i]) > len(mounts[j]) })
	for _, mp := range mounts {
		if err := run(ev, "umount", mp); err != nil {
			return fmt.Errorf("cannot unmount %s: %w", mp, err)
		}
	}

	ev.Phase("Writing " + disk.Path)
	sum, err := writeImage(info, disk.Path, ev)
	if err != nil {
		return err
	}

	ev.Phase("Verifying " + disk.Path)
	if err := verifyDevice(disk.Path, info.RawSize, sum, ev); err != nil {
		return err
	}

	ev.Phase("Finishing partition table")
	// The image is smaller than the media: move the backup GPT to the real end.
	if err := run(ev, "sgdisk", "-e", disk.Path); err != nil {
		return err
	}
	if keep != nil {
		if err := restoreData(disk.Path, keep, ev); err != nil {
			return fmt.Errorf("the image was written but DATA could not be restored: %w", err)
		}
	}
	rereadPartitions(disk.Path, ev)
	syscall.Sync()
	return nil
}

func writeImage(info ImageInfo, dev string, ev emitter) (string, error) {
	var src io.Reader
	var xz *exec.Cmd
	var xzErr bytes.Buffer
	if info.Compressed {
		xz = exec.Command("xz", "-dc", "-T0", info.Path)
		xz.Stderr = &xzErr
		pipe, err := xz.StdoutPipe()
		if err != nil {
			return "", err
		}
		if err := xz.Start(); err != nil {
			return "", fmt.Errorf("xz: %w", err)
		}
		defer xz.Process.Kill()
		src = pipe
	} else {
		f, err := os.Open(info.Path)
		if err != nil {
			return "", err
		}
		defer f.Close()
		src = f
	}

	// O_EXCL on a block device fails while anything has it mounted.
	out, err := os.OpenFile(dev, os.O_WRONLY|syscall.O_EXCL, 0)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", dev, err)
	}
	defer out.Close()

	h := sha256.New()
	buf := make([]byte, chunk)
	var written int64
	for {
		n, rerr := io.ReadFull(src, buf)
		if n > 0 {
			h.Write(buf[:n])
			if _, err := out.Write(buf[:n]); err != nil {
				return "", fmt.Errorf("write %s at %s: %w", dev, humanBytes(written), err)
			}
			written += int64(n)
			// Flush regularly so progress tracks the media rather than the
			// page cache, and the final flush does not stall for minutes.
			if written%(128<<20) == 0 {
				if err := out.Sync(); err != nil {
					return "", fmt.Errorf("sync %s: %w", dev, err)
				}
			}
			ev.Progress(written, info.RawSize)
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			return "", fmt.Errorf("read %s: %w", info.Path, rerr)
		}
	}
	if xz != nil {
		if err := xz.Wait(); err != nil {
			return "", fmt.Errorf("decompressing %s: %v %s", info.Path, err, strings.TrimSpace(xzErr.String()))
		}
	}
	if written != info.RawSize {
		return "", fmt.Errorf("wrote %d bytes, expected %d", written, info.RawSize)
	}
	ev.Phase("Flushing to " + dev + " (this can take a while on SD cards)")
	if err := out.Sync(); err != nil {
		return "", fmt.Errorf("sync %s: %w", dev, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// verifyDevice reads the written range back from the media, bypassing the
// page cache, and compares it with the hash of what was written.
func verifyDevice(dev string, size int64, want string, ev emitter) error {
	f, err := os.Open(dev)
	if err != nil {
		return err
	}
	defer f.Close()
	fd := int(f.Fd())
	_ = unix.IoctlSetInt(fd, unix.BLKFLSBUF, 0)
	_ = unix.Fadvise(fd, 0, 0, unix.FADV_DONTNEED)
	h := sha256.New()
	pr := &progressReader{r: io.LimitReader(f, size), total: size, fn: ev.Progress}
	n, err := io.CopyBuffer(h, pr, make([]byte, chunk))
	if err != nil {
		return fmt.Errorf("read back %s: %w", dev, err)
	}
	if n != size {
		return fmt.Errorf("read back %d bytes from %s, expected %d", n, dev, size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("verification failed: %s returned different data than was written. The card or drive is probably faulty or counterfeit", dev)
	}
	ev.Log("Read-back verification passed (SHA-256 " + want[:16] + "…).")
	return nil
}

// restoreData puts the old rootfs size and DATA partition back into the
// image's partition table and grows the new rootfs filesystem to match, so
// bdx-rock-prepare-data finds DATA on first boot and leaves it alone.
func restoreData(disk string, k *keepPlan, ev emitter) error {
	t, err := readTable(disk)
	if err != nil {
		return fmt.Errorf("read new partition table: %w", err)
	}
	if len(t.Partitions) != 1 {
		return fmt.Errorf("the image has %d partitions; expected one", len(t.Partitions))
	}
	img := t.Partitions[0]
	num, err := partNumber(disk, img.Node)
	if err != nil || num != k.RootNum || img.Start != k.RootStart {
		return fmt.Errorf("the image's system partition (%s at sector %d) does not line up with the old one (#%d at sector %d)",
			img.Node, img.Start, k.RootNum, k.RootStart)
	}
	if img.Size > k.RootSize {
		return fmt.Errorf("the image's system partition is larger than the old one")
	}

	ev.Log(fmt.Sprintf("Restoring system partition size %s and DATA partition.", humanBytes(k.RootSize*sector)))
	sf := exec.Command("sfdisk", "--no-reread", "--no-tell-kernel", "-N", strconv.Itoa(k.RootNum), disk)
	sf.Stdin = strings.NewReader(fmt.Sprintf("%d,%d\n", k.RootStart, k.RootSize))
	if out, err := sf.CombinedOutput(); err != nil {
		return fmt.Errorf("sfdisk: %v: %s", err, strings.TrimSpace(string(out)))
	}
	n := strconv.Itoa(k.DataNum)
	args := []string{
		"-n", fmt.Sprintf("%s:%d:%d", n, k.Data.Start, k.Data.Start+k.Data.Size-1),
		"-c", n + ":DATA",
		"-t", n + ":" + k.Data.Type,
	}
	if k.Data.UUID != "" {
		args = append(args, "-u", n+":"+k.Data.UUID)
	}
	if err := run(ev, "sgdisk", append(args, disk)...); err != nil {
		return err
	}
	rereadPartitions(disk, ev)

	root := partPath(disk, k.RootNum)
	if err := waitForNode(root); err != nil {
		return err
	}
	ev.Phase("Growing the system filesystem")
	fsck := exec.Command("e2fsck", "-f", "-y", root)
	out, err := fsck.CombinedOutput()
	ev.Log(strings.TrimSpace(string(out)))
	var exit *exec.ExitError
	if err != nil && !(errors.As(err, &exit) && exit.ExitCode() < 4) {
		return fmt.Errorf("e2fsck %s: %w", root, err)
	}
	return run(ev, "resize2fs", root)
}

func rereadPartitions(disk string, ev emitter) {
	for i := 0; i < 5; i++ {
		if exec.Command("blockdev", "--rereadpt", disk).Run() == nil {
			break
		}
		time.Sleep(time.Second)
	}
	_ = exec.Command("udevadm", "settle", "--timeout=15").Run()
}

func waitForNode(path string) error {
	for i := 0; i < 20; i++ {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("%s did not appear", path)
}

// helperEvent is one parsed line of helper output.
type helperEvent struct {
	Kind  string // phase, progress, log, error
	Text  string
	Done  int64
	Total int64
}

func (o writeOpts) helperArgs() []string {
	a := []string{"write", "--image", o.Image, "--device", o.Device}
	if o.Clean {
		a = append(a, "--erase-all")
	}
	return a
}

func (o maskromOpts) helperArgs(b Board) []string {
	return []string{"maskrom", "--board", b.Slug, "--image", o.Image, "--loader", o.Loader}
}

// runHelper runs `<launcher> <this binary> --helper <helperArgs>` and streams
// its events.  launcher is pkexec in the GUI, sudo in the terminal, or empty
// when already root.
func runHelper(launcher []string, helperArgs []string, on func(helperEvent)) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := append(append(append([]string(nil), launcher...), exe, "--helper"), helperArgs...)
	cmd := exec.Command(args[0], args[1:]...)
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Start(); err != nil {
		w.Close()
		r.Close()
		return err
	}
	w.Close()
	var lastErr string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		tag, rest, _ := strings.Cut(line, " ")
		switch tag {
		case "@@PHASE":
			on(helperEvent{Kind: "phase", Text: rest})
		case "@@LOG":
			on(helperEvent{Kind: "log", Text: rest})
		case "@@ERROR":
			lastErr = rest
			on(helperEvent{Kind: "error", Text: rest})
		case "@@PROGRESS":
			var e helperEvent
			if _, err := fmt.Sscanf(rest, "%d %d", &e.Done, &e.Total); err == nil {
				e.Kind = "progress"
				on(e)
			}
		default:
			on(helperEvent{Kind: "log", Text: line})
		}
	}
	r.Close()
	err = cmd.Wait()
	if err == nil {
		return nil
	}
	if lastErr != "" {
		return errors.New(lastErr)
	}
	var exit *exec.ExitError
	if len(launcher) > 0 && launcher[0] == "pkexec" && errors.As(err, &exit) && (exit.ExitCode() == 126 || exit.ExitCode() == 127) {
		return errors.New("administrator authorisation was cancelled or denied")
	}
	return err
}
