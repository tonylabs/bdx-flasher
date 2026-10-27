package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ImageInfo describes a local BDX disk image (.img or .img.xz).
type ImageInfo struct {
	Path       string
	Compressed bool
	FileSize   int64
	RawSize    int64  // bytes written to the disk
	Release    string // e.g. 1.3.1-10, parsed from the file name
	SidecarSHA string // expected SHA-256 of the file from <file>.sha256, if any
}

var releaseRE = regexp.MustCompile(`-(\d+\.\d+\.\d+-\d+)`)

func inspectImage(path string) (ImageInfo, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return ImageInfo{}, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return ImageInfo{}, err
	}
	if !st.Mode().IsRegular() {
		return ImageInfo{}, fmt.Errorf("%s is not a regular file", abs)
	}
	info := ImageInfo{Path: abs, FileSize: st.Size(), RawSize: st.Size()}
	switch {
	case strings.HasSuffix(abs, ".img.xz"):
		info.Compressed = true
		if info.RawSize, err = xzRawSize(abs); err != nil {
			return ImageInfo{}, err
		}
	case strings.HasSuffix(abs, ".img"):
	default:
		return ImageInfo{}, fmt.Errorf("%s: expected a .img or .img.xz disk image", filepath.Base(abs))
	}
	if m := releaseRE.FindStringSubmatch(filepath.Base(abs)); m != nil {
		info.Release = m[1]
	}
	info.SidecarSHA, _ = readSidecar(abs + ".sha256")
	return info, nil
}

// xzRawSize reads the uncompressed size from the xz index (xz --robot --list).
func xzRawSize(path string) (int64, error) {
	out, err := exec.Command("xz", "--robot", "--list", path).Output()
	if err != nil {
		return 0, fmt.Errorf("xz --list %s: %w", filepath.Base(path), err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Split(line, "\t")
		if len(f) >= 5 && f[0] == "totals" {
			return strconv.ParseInt(f[4], 10, 64)
		}
	}
	return 0, fmt.Errorf("xz --list %s: no totals line", filepath.Base(path))
}

func readSidecar(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) > 0 && len(fields[0]) == 64 {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("%s: no SHA-256 found", filepath.Base(path))
}

// verifySidecar hashes the image file and compares it with its .sha256 file.
func verifySidecar(info ImageInfo, progress func(done, total int64)) error {
	if info.SidecarSHA == "" {
		return nil
	}
	f, err := os.Open(info.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, &progressReader{r: f, total: info.FileSize, fn: progress}); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != info.SidecarSHA {
		return fmt.Errorf("checksum mismatch for %s:\n  expected %s\n  got      %s\nThe file is damaged or incomplete; download or copy it again",
			filepath.Base(info.Path), info.SidecarSHA, got)
	}
	return nil
}

// findDefaultImage returns the newest image matching the board's pattern in
// the usual places (the port's out/ folder, Downloads, the current folder).
func findDefaultImage(b Board) string {
	home, _ := os.UserHomeDir()
	dirs := []string{
		filepath.Join(home, "Desktop", "OpenBDX", "out"),
		filepath.Join(home, "Downloads"),
		filepath.Join(home, ".cache", "bdx-flasher"),
	}
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, wd)
	}
	type cand struct {
		path string
		mod  int64
		xz   bool
	}
	var cands []cand
	for _, dir := range dirs {
		matches, _ := filepath.Glob(filepath.Join(dir, b.ImageGlob))
		for _, m := range matches {
			if !strings.HasSuffix(m, ".img") && !strings.HasSuffix(m, ".img.xz") {
				continue
			}
			if st, err := os.Stat(m); err == nil && st.Mode().IsRegular() {
				cands = append(cands, cand{m, st.ModTime().Unix(), strings.HasSuffix(m, ".xz")})
			}
		}
	}
	// Newest first; for the same build prefer the .xz, which has a sidecar
	// for the file that was actually distributed.
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].mod/60 != cands[j].mod/60 {
			return cands[i].mod > cands[j].mod
		}
		return cands[i].xz && !cands[j].xz
	})
	if len(cands) == 0 {
		return ""
	}
	return cands[0].path
}

type progressReader struct {
	r     io.Reader
	done  int64
	total int64
	fn    func(done, total int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	if p.fn != nil {
		p.fn(p.done, p.total)
	}
	return n, err
}
