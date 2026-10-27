package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Maskrom flashing writes the image to the board's onboard eMMC over USB.
// rkdeveloptool loads Rockchip's SPL loader into the SoC's RAM ("boot"),
// which brings up DRAM and exposes the eMMC through the rockusb protocol,
// then "write 0 <raw image>" puts the whole image (idbloader, U-Boot, GPT,
// rootfs) at sector 0.  It cannot read the old partition table cheaply, so a
// maskrom flash is always a clean install: on first boot DATA is recreated.

func cacheDir() string {
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "bdx-flasher")
	}
	return filepath.Join(os.TempDir(), "bdx-flasher")
}

func rkdeveloptoolPath() string {
	p, _ := exec.LookPath("rkdeveloptool")
	return p
}

// ensureLoader returns the cached SPL loader, downloading and verifying it on
// first use.  An explicit override path is used as is.
func ensureLoader(b Board, override string) (string, error) {
	if override != "" {
		return filepath.Abs(override)
	}
	dir := cacheDir()
	path := filepath.Join(dir, b.Loader.Name)
	if sum, err := fileSHA256(path); err == nil && sum == b.Loader.SHA256 {
		return path, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(b.Loader.URL)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", b.Loader.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: %s", b.Loader.URL, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return "", fmt.Errorf("download %s: %w", b.Loader.Name, err)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != b.Loader.SHA256 {
		return "", fmt.Errorf("%s from %s has SHA-256 %s, expected %s", b.Loader.Name, b.Loader.URL, got, b.Loader.SHA256)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// prepareRawImage gives rkdeveloptool an uncompressed image, decompressing an
// .img.xz into the cache.  temp reports whether the caller should delete it.
func prepareRawImage(info ImageInfo, progress func(done, total int64)) (path string, temp bool, err error) {
	if !info.Compressed {
		return info.Path, false, nil
	}
	dir := cacheDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false, err
	}
	raw := filepath.Join(dir, strings.TrimSuffix(filepath.Base(info.Path), ".xz"))
	stamp := raw + ".source"
	st, _ := os.Stat(info.Path)
	want := fmt.Sprintf("%s %d %d\n", info.Path, st.Size(), st.ModTime().UnixNano())
	if got, err := os.ReadFile(stamp); err == nil && string(got) == want {
		if rs, err := os.Stat(raw); err == nil && rs.Size() == info.RawSize {
			return raw, true, nil
		}
	}
	os.Remove(stamp)

	var fs unix.Statfs_t
	if err := unix.Statfs(dir, &fs); err == nil {
		if free := int64(fs.Bavail) * int64(fs.Bsize); free < info.RawSize+gib {
			return "", false, fmt.Errorf("decompressing the image needs %s free in %s; only %s is free",
				humanBytes(info.RawSize+gib), dir, humanBytes(free))
		}
	}
	out, err := os.Create(raw)
	if err != nil {
		return "", false, err
	}
	defer out.Close()
	xz := exec.Command("xz", "-dc", "-T0", info.Path)
	var xzErr bytes.Buffer
	xz.Stderr = &xzErr
	pipe, err := xz.StdoutPipe()
	if err != nil {
		return "", false, err
	}
	if err := xz.Start(); err != nil {
		return "", false, err
	}
	n, cerr := io.CopyBuffer(out, &progressReader{r: pipe, total: info.RawSize, fn: progress}, make([]byte, chunk))
	werr := xz.Wait()
	switch {
	case cerr != nil:
		err = cerr
	case werr != nil:
		err = fmt.Errorf("xz: %v %s", werr, strings.TrimSpace(xzErr.String()))
	case n != info.RawSize:
		err = fmt.Errorf("decompressed %d bytes, expected %d", n, info.RawSize)
	}
	if err == nil {
		err = out.Close()
	}
	if err != nil {
		os.Remove(raw)
		return "", false, err
	}
	return raw, true, os.WriteFile(stamp, []byte(want), 0o644)
}

// ---- privileged half ----

type maskromOpts struct {
	Image  string // raw .img
	Loader string
}

type rockusbDev struct {
	USBID string // vid:pid
	Mode  string // Maskrom or Loader
}

var listRE = regexp.MustCompile(`Vid=0x([0-9a-fA-F]+),Pid=0x([0-9a-fA-F]+).*\t(\w+)\s*$`)

func rockusbList() ([]rockusbDev, error) {
	out, err := exec.Command("rkdeveloptool", "list").CombinedOutput()
	var devs []rockusbDev
	for _, line := range strings.Split(string(out), "\n") {
		if m := listRE.FindStringSubmatch(line); m != nil {
			devs = append(devs, rockusbDev{
				USBID: fmt.Sprintf("%04s:%04s", strings.ToLower(m[1]), strings.ToLower(m[2])),
				Mode:  m[3],
			})
		}
	}
	if len(devs) == 0 && err != nil && !strings.Contains(string(out), "No devices") {
		return nil, fmt.Errorf("rkdeveloptool list: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return devs, nil
}

func waitForMode(mode string, timeout time.Duration) (rockusbDev, error) {
	deadline := time.Now().Add(timeout)
	for {
		devs, err := rockusbList()
		if err == nil && len(devs) == 1 && devs[0].Mode == mode {
			return devs[0], nil
		}
		if time.Now().After(deadline) {
			return rockusbDev{}, fmt.Errorf("the board did not come up in %s mode", mode)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

var flashSizeRE = regexp.MustCompile(`Flash Size:\s*(\d+)\s*MB`)

func runMaskrom(b Board, o maskromOpts, ev emitter) error {
	if os.Geteuid() != 0 {
		return errors.New("maskrom flashing must run as root")
	}
	if rkdeveloptoolPath() == "" {
		return errors.New("rkdeveloptool is not installed (sudo apt install rkdeveloptool)")
	}
	st, err := os.Stat(o.Image)
	if err != nil {
		return err
	}
	size := st.Size()
	if size%sector != 0 {
		return fmt.Errorf("%s is not a whole number of 512-byte sectors", o.Image)
	}

	ev.Phase("Finding the board")
	devs, err := rockusbList()
	if err != nil {
		return err
	}
	switch {
	case len(devs) == 0:
		return errors.New("no Rockchip board in Maskrom mode found on USB")
	case len(devs) > 1:
		return fmt.Errorf("%d Rockchip boards are connected; connect only the one to flash", len(devs))
	case devs[0].USBID != b.MaskromUSBID:
		return fmt.Errorf("the connected board (USB %s) is not a %s (USB %s)", devs[0].USBID, b.Name, b.MaskromUSBID)
	}
	ev.Log(fmt.Sprintf("Found %s in %s mode (USB %s).", b.Name, devs[0].Mode, devs[0].USBID))

	if devs[0].Mode == "Maskrom" {
		ev.Phase("Loading the USB loader into the board's RAM")
		if err := run(ev, "rkdeveloptool", "boot", o.Loader); err != nil {
			return err
		}
		if _, err := waitForMode("Loader", 15*time.Second); err != nil {
			return err
		}
	}

	ev.Phase("Checking the eMMC")
	out, err := exec.Command("rkdeveloptool", "read-flash-info").CombinedOutput()
	ev.Log(strings.TrimSpace(string(out)))
	if err != nil {
		return fmt.Errorf("rkdeveloptool read-flash-info: %w", err)
	}
	m := flashSizeRE.FindStringSubmatch(string(out))
	if m == nil {
		return errors.New("cannot read the storage size from the board")
	}
	mb, _ := strconv.ParseInt(m[1], 10, 64)
	if flash := mb << 20; flash < size+minSpare {
		return fmt.Errorf("the board's storage is %s; the image needs at least %s. Is an eMMC module fitted?",
			humanBytes(flash), humanBytes(size+minSpare))
	}

	ev.Phase("Writing the eMMC over USB")
	if err := rkWrite(o.Image, size, ev); err != nil {
		return err
	}

	ev.Phase("Spot-checking the eMMC")
	if err := rkSpotCheck(o.Image, size, ev); err != nil {
		return err
	}

	ev.Phase("Restarting the board")
	if err := run(ev, "rkdeveloptool", "reset"); err != nil {
		ev.Log("The board did not restart by itself; power-cycle it. (" + err.Error() + ")")
	}
	return nil
}

var pctRE = regexp.MustCompile(`\((\d+)%\)`)

// rkWrite runs `rkdeveloptool write 0 image`, turning its \r-separated
// percentage updates into progress events.
func rkWrite(image string, size int64, ev emitter) error {
	cmd := exec.Command("rkdeveloptool", "write", "0", image)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(pipe)
	sc.Split(scanCRLF)
	var tail []string
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if m := pctRE.FindStringSubmatch(line); m != nil {
			pct, _ := strconv.ParseInt(m[1], 10, 64)
			ev.Progress(size*pct/100, size)
			continue
		}
		if line != "" {
			ev.Log(line)
			tail = append(tail, line)
		}
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("rkdeveloptool write: %v %s", err, strings.Join(tail, " "))
	}
	for _, l := range tail {
		if strings.Contains(l, "failed") {
			return fmt.Errorf("rkdeveloptool write: %s", l)
		}
	}
	return nil
}

func scanCRLF(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// rkSpotCheck reads back the boot area plus regions spread over the image and
// compares them with the file.  A full read-back over the board's USB 2.0
// rockusb link would take as long as the write itself.
func rkSpotCheck(image string, size int64, ev emitter) error {
	const region = 4 << 20
	offsets := []int64{0, 4 << 20, 8 << 20, 12 << 20} // idbloader, U-Boot, GPT
	for i := int64(1); i <= 12; i++ {
		offsets = append(offsets, (size*i/13)/sector*sector)
	}
	offsets = append(offsets, size-region)

	f, err := os.Open(image)
	if err != nil {
		return err
	}
	defer f.Close()
	tmp, err := os.MkdirTemp("", "bdx-rkcheck")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	want := make([]byte, region)
	for i, off := range offsets {
		n := int64(region)
		if off+n > size {
			n = size - off
		}
		if _, err := f.ReadAt(want[:n], off); err != nil {
			return err
		}
		got := filepath.Join(tmp, "region")
		if out, err := exec.Command("rkdeveloptool", "read", strconv.FormatInt(off/sector, 10),
			strconv.FormatInt(n, 10), got).CombinedOutput(); err != nil {
			return fmt.Errorf("rkdeveloptool read at %s: %v %s", humanBytes(off), err, strings.TrimSpace(string(out)))
		}
		data, err := os.ReadFile(got)
		if err != nil {
			return err
		}
		if !bytes.Equal(data, want[:n]) {
			return fmt.Errorf("verification failed: the eMMC differs from the image at %s. Retry the flash; if it fails again the eMMC module may be faulty", humanBytes(off))
		}
		ev.Progress(int64(i+1), int64(len(offsets)))
	}
	ev.Log(fmt.Sprintf("Spot check passed: %d regions (%s) read back identical.", len(offsets), humanBytes(int64(len(offsets))*region)))
	return nil
}
