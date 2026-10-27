package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Disk is a whole block device that could receive an image.
type Disk struct {
	Name      string
	Path      string
	Size      int64
	Model     string
	Vendor    string
	Tran      string
	Removable bool
	ReadOnly  bool
	System    bool     // holds a mount the host needs (/, /boot, swap, ...)
	Mounts    []string // every mountpoint on the disk or its partitions
	HasData   bool     // has a GPT partition labelled DATA (an existing BDX install)
}

type lsblkDev struct {
	Name        string     `json:"name"`
	Path        string     `json:"path"`
	Size        int64      `json:"size"`
	Model       *string    `json:"model"`
	Vendor      *string    `json:"vendor"`
	Tran        *string    `json:"tran"`
	RM          bool       `json:"rm"`
	Hotplug     bool       `json:"hotplug"`
	RO          bool       `json:"ro"`
	Type        string     `json:"type"`
	Mountpoints []*string  `json:"mountpoints"`
	PartLabel   *string    `json:"partlabel"`
	Children    []lsblkDev `json:"children"`
}

var systemMounts = map[string]bool{
	"/": true, "/boot": true, "/boot/efi": true, "/efi": true, "/usr": true,
	"/var": true, "/home": true, "/opt": true, "/srv": true, "[SWAP]": true,
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

// listDisks returns every writable whole disk, including the host's own ones
// (flagged System) so callers can explain why they are not offered.
func listDisks() ([]Disk, error) {
	out, err := exec.Command("lsblk", "-J", "-b", "-o",
		"NAME,PATH,SIZE,MODEL,VENDOR,TRAN,RM,HOTPLUG,RO,TYPE,MOUNTPOINTS,PARTLABEL").Output()
	if err != nil {
		return nil, fmt.Errorf("lsblk: %w", err)
	}
	var doc struct {
		BlockDevices []lsblkDev `json:"blockdevices"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("lsblk output: %w", err)
	}
	// tests/loop-test.sh flashes a loop device instead of real media.
	allowLoop := os.Getenv("BDX_FLASHER_ALLOW_LOOP") == "1"
	var disks []Disk
	for _, d := range doc.BlockDevices {
		if !(d.Type == "disk" || d.Type == "loop" && allowLoop) || d.Size == 0 {
			continue
		}
		disk := Disk{
			Name:      d.Name,
			Path:      d.Path,
			Size:      d.Size,
			Model:     str(d.Model),
			Vendor:    str(d.Vendor),
			Tran:      str(d.Tran),
			Removable: d.RM || d.Hotplug || str(d.Tran) == "usb" || str(d.Tran) == "mmc" || strings.HasPrefix(d.Name, "mmcblk"),
			ReadOnly:  d.RO,
		}
		var walk func(lsblkDev)
		walk = func(n lsblkDev) {
			for _, m := range n.Mountpoints {
				if mp := str(m); mp != "" {
					disk.Mounts = append(disk.Mounts, mp)
					if systemMounts[mp] {
						disk.System = true
					}
				}
			}
			if n.Type == "part" && str(n.PartLabel) == "DATA" {
				disk.HasData = true
			}
			for _, c := range n.Children {
				walk(c)
			}
		}
		walk(d)
		disks = append(disks, disk)
	}
	return disks, nil
}

// candidateDisks filters listDisks down to what a user may flash.
func candidateDisks(showInternal bool) ([]Disk, error) {
	all, err := listDisks()
	if err != nil {
		return nil, err
	}
	var out []Disk
	for _, d := range all {
		if d.System || d.ReadOnly {
			continue
		}
		if !d.Removable && !showInternal {
			continue
		}
		out = append(out, d)
	}
	return out, nil
}

func findDisk(path string) (Disk, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return Disk{}, err
	}
	all, err := listDisks()
	if err != nil {
		return Disk{}, err
	}
	for _, d := range all {
		if d.Path == resolved {
			return d, nil
		}
	}
	if _, err := os.Stat("/sys/class/block/" + filepath.Base(resolved) + "/partition"); err == nil {
		return Disk{}, fmt.Errorf("%s is a partition; choose the whole disk", resolved)
	}
	return Disk{}, fmt.Errorf("%s is not a disk", resolved)
}

func (d Disk) Label() string {
	desc := strings.TrimSpace(d.Vendor + " " + d.Model)
	if desc == "" {
		desc = "Unknown device"
	}
	kind := strings.ToUpper(d.Tran)
	if kind == "" && strings.HasPrefix(d.Name, "mmcblk") {
		kind = "SD/MMC"
	}
	if kind != "" {
		kind = " · " + kind
	}
	return fmt.Sprintf("%s — %s (%s%s)", d.Path, desc, humanBytes(d.Size), kind)
}

// partPath builds the device node of partition n on disk.
func partPath(disk string, n int) string {
	last := disk[len(disk)-1]
	if last >= '0' && last <= '9' {
		return fmt.Sprintf("%sp%d", disk, n)
	}
	return fmt.Sprintf("%s%d", disk, n)
}

// usbPresent reports whether a USB device vendor:product is connected.
func usbPresent(id string) bool {
	vendor, product, ok := strings.Cut(strings.ToLower(id), ":")
	if !ok {
		return false
	}
	matches, _ := filepath.Glob("/sys/bus/usb/devices/*/idVendor")
	for _, vf := range matches {
		v, err := os.ReadFile(vf)
		if err != nil || strings.TrimSpace(string(v)) != vendor {
			continue
		}
		p, err := os.ReadFile(filepath.Join(filepath.Dir(vf), "idProduct"))
		if err == nil && strings.TrimSpace(string(p)) == product {
			return true
		}
	}
	return false
}

func humanBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}
