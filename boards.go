package main

// Method says how a board is flashed.
type Method int

const (
	// MethodJetson boards are flashed over USB in recovery mode by NVIDIA's
	// initrd flash, which the BDX Orin Flasher already implements.
	MethodJetson Method = iota
	// MethodImage boards boot a raw disk image written to removable media.
	MethodImage
)

// Board is one supported main board.
type Board struct {
	Slug    string
	Name    string
	SoC     string
	Method  Method
	Summary string

	// MethodJetson
	Profile string
	USBID   string // recovery-mode vendor:product

	// MethodImage
	ImageGlob   string // file name pattern of BDX images for this board
	MinMediaGiB int64

	// Rockchip maskrom flashing of the onboard eMMC (empty: unsupported)
	MaskromUSBID string
	Loader       loaderBlob
}

// loaderBlob is the Rockchip SPL loader rkdeveloptool runs in RAM to reach
// the board's storage from maskrom.
type loaderBlob struct {
	Name   string
	URL    string
	SHA256 string
}

var boards = []Board{
	{
		Slug:        "rock5bplus",
		Name:        "Radxa ROCK 5B+",
		SoC:         "Rockchip RK3588 · Armbian (Debian trixie)",
		Method:      MethodImage,
		Summary:     "Writes the BDX image to a microSD card, to an NVMe SSD / eMMC module in a USB adapter, or over USB to the onboard eMMC of a board in Maskrom mode. On first boot the board grows the system partition and creates DATA (/data).",
		ImageGlob:   "bdx-system-rock5bplus-*.img*",
		MinMediaGiB: 16,

		MaskromUSBID: "2207:350b",
		Loader: loaderBlob{
			Name:   "rk3588_spl_loader_v1.15.113.bin",
			URL:    "https://dl.radxa.com/rock5/sw/images/loader/rk3588_spl_loader_v1.15.113.bin",
			SHA256: "26baab70e6b915364f7d73d88298366db1bfc346e34683e95d3d11b52492047f",
		},
	},
	{
		Slug:    "nano",
		Name:    "NVIDIA Jetson Orin Nano Super Dev Kit",
		SoC:     "Jetson Orin Nano 8 GB · JetPack 6.2.2",
		Method:  MethodJetson,
		Summary: "Flashed over USB-C in recovery mode by the BDX Orin Flasher.",
		Profile: "orin-nano-8gb-devkit-super",
		USBID:   "0955:7523",
	},
	{
		Slug:    "nx8",
		Name:    "NVIDIA Jetson Orin NX 8 GB",
		SoC:     "Jetson Orin NX 8 GB (Super) · JetPack 6.2.2",
		Method:  MethodJetson,
		Summary: "Flashed over USB-C in recovery mode by the BDX Orin Flasher.",
		Profile: "orin-nx-8gb-super",
		USBID:   "0955:7423",
	},
	{
		Slug:    "nx16",
		Name:    "NVIDIA Jetson Orin NX 16 GB",
		SoC:     "Jetson Orin NX 16 GB (Super) · JetPack 6.2.2",
		Method:  MethodJetson,
		Summary: "Flashed over USB-C in recovery mode by the BDX Orin Flasher.",
		Profile: "orin-nx-16gb-super",
		USBID:   "0955:7323",
	},
}

func boardBySlug(slug string) (Board, bool) {
	for _, b := range boards {
		if b.Slug == slug {
			return b, true
		}
	}
	return Board{}, false
}

func boardByName(name string) (Board, bool) {
	for _, b := range boards {
		if b.Name == name {
			return b, true
		}
	}
	return Board{}, false
}

func boardNames() []string {
	names := make([]string, len(boards))
	for i, b := range boards {
		names[i] = b.Name
	}
	return names
}

func boardSlugs() []string {
	slugs := make([]string, len(boards))
	for i, b := range boards {
		slugs[i] = b.Slug
	}
	return slugs
}
