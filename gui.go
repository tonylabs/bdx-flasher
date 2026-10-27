package main

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

//go:embed assets/icon.png
var iconPNG []byte

type gui struct {
	o   options
	win fyne.Window

	board Board
	busy  bool

	boardRadio *widget.RadioGroup
	boardInfo  *widget.Label
	steps      *fyne.Container

	// image boards
	imageEntry *widget.Entry
	imageInfo  *widget.Label
	image      *ImageInfo
	diskSelect *widget.Select
	diskInfo   *widget.Label
	showAll    *widget.Check
	disks      []Disk
	flashBtn   *widget.Button

	// maskrom (onboard eMMC over USB)
	targetMode  *widget.RadioGroup
	diskCard    *widget.Card
	maskCard    *widget.Card
	maskStatus  *widget.Label
	maskPresent bool

	// Jetson boards
	usbStatus   *widget.Label
	bundleEntry *widget.Entry
	orinBtn     *widget.Button

	phase    *widget.Label
	progress *widget.ProgressBar
	logView  *widget.Entry
	logText  strings.Builder
}

func runGUI(o options) error {
	if os.Getenv("FYNE_SCALE") == "" && o.uiScale > 0 {
		os.Setenv("FYNE_SCALE", strconv.FormatFloat(o.uiScale, 'f', 2, 64))
	}
	a := app.NewWithID(appID)
	icon := fyne.NewStaticResource("icon.png", iconPNG)
	a.SetIcon(icon)
	g := &gui{o: o, win: a.NewWindow(appName)}
	g.win.SetIcon(icon)
	g.build(icon)
	g.win.Resize(fyne.NewSize(980, 720))
	g.win.SetCloseIntercept(func() {
		if g.busy {
			dialog.ShowInformation("Flashing in progress",
				"Wait for the flash to finish. Closing now would leave the media unbootable.", g.win)
			return
		}
		g.win.Close()
	})

	start := boards[0]
	if b, ok := boardBySlug(o.board); ok {
		start = b
	}
	g.boardRadio.SetSelected(start.Name)

	stop := make(chan struct{})
	go g.watchHardware(stop)
	g.win.ShowAndRun()
	close(stop)
	return nil
}

func (g *gui) build(icon fyne.Resource) {
	logo := widget.NewIcon(icon)
	title := widget.NewLabelWithStyle(appName, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	title.SizeName = theme.SizeNameHeadingText
	subtitle := widget.NewLabel("Flash OpenBDX firmware onto your robot's main board")
	header := container.NewBorder(nil, nil,
		container.NewGridWrap(fyne.NewSize(64, 64), logo), nil,
		container.NewVBox(title, subtitle))

	g.boardInfo = widget.NewLabel("")
	g.boardInfo.Wrapping = fyne.TextWrapWord
	g.boardRadio = widget.NewRadioGroup(boardNames(), func(name string) {
		if b, ok := boardByName(name); ok {
			g.selectBoard(b)
		}
	})
	g.boardRadio.Required = true
	boardCard := widget.NewCard("1 · Main board", "", container.NewVBox(g.boardRadio, widget.NewSeparator(), g.boardInfo))

	g.steps = container.NewVBox()

	g.phase = widget.NewLabel("Ready.")
	g.phase.Wrapping = fyne.TextWrapWord
	g.progress = widget.NewProgressBar()
	g.logView = widget.NewMultiLineEntry()
	g.logView.Wrapping = fyne.TextWrapWord
	g.logView.TextStyle = fyne.TextStyle{Monospace: true}
	g.logView.SetMinRowsVisible(7)
	details := widget.NewAccordion(widget.NewAccordionItem("Details", g.logView))
	status := container.NewVBox(widget.NewSeparator(), g.phase, g.progress, details)

	left := container.NewVBox(boardCard)
	right := container.NewVScroll(g.steps)
	split := container.NewHSplit(container.NewPadded(left), container.NewPadded(right))
	split.Offset = 0.38
	g.win.SetContent(container.NewBorder(container.NewPadded(header), container.NewPadded(status), nil, nil, split))
}

func (g *gui) selectBoard(b Board) {
	g.board = b
	g.boardInfo.SetText(b.SoC + "\n\n" + b.Summary)
	g.steps.RemoveAll()
	if b.Method == MethodImage {
		g.buildImageSteps(b)
	} else {
		g.buildJetsonSteps(b)
	}
	g.steps.Refresh()
}

// ---- image boards (ROCK 5B+) ----

func (g *gui) buildImageSteps(b Board) {
	g.imageEntry = widget.NewEntry()
	g.imageEntry.SetPlaceHolder("Path to " + b.ImageGlob)
	g.imageInfo = widget.NewLabel("")
	g.imageInfo.Wrapping = fyne.TextWrapWord
	g.imageEntry.OnSubmitted = func(string) { g.loadImage() }
	browse := widget.NewButtonWithIcon("Browse…", theme.FolderOpenIcon(), g.browseImage)
	imageCard := widget.NewCard("2 · Firmware image", "",
		container.NewVBox(container.NewBorder(nil, nil, nil, browse, g.imageEntry), g.imageInfo))

	g.diskSelect = widget.NewSelect(nil, func(string) { g.updateDiskInfo() })
	g.diskSelect.PlaceHolder = "Insert a microSD card or USB drive adapter"
	g.diskInfo = widget.NewLabel("")
	g.diskInfo.Wrapping = fyne.TextWrapWord
	g.showAll = widget.NewCheck("Show internal drives", func(bool) { g.refreshDisks() })
	refresh := widget.NewButtonWithIcon("", theme.ViewRefreshIcon(), g.refreshDisks)
	g.diskCard = widget.NewCard("3 · Target drive", "",
		container.NewVBox(container.NewBorder(nil, nil, nil, refresh, g.diskSelect), g.showAll, g.diskInfo))

	g.flashBtn = widget.NewButtonWithIcon("Flash "+b.Name, theme.UploadIcon(), g.confirmFlash)
	g.flashBtn.Importance = widget.HighImportance

	g.steps.Add(imageCard)
	g.targetMode, g.maskCard = nil, nil
	if b.MaskromUSBID != "" {
		g.buildMaskromCard(b)
		g.steps.Add(widget.NewCard("Where to flash", "", g.targetMode))
		g.steps.Add(g.maskCard)
	}
	g.steps.Add(g.diskCard)
	g.steps.Add(container.NewHBox(layout.NewSpacer(), g.flashBtn))
	if g.targetMode != nil {
		mode := targetDrive
		if g.o.target == "maskrom" {
			mode = targetMaskrom
		}
		g.targetMode.SetSelected(mode)
	}

	path := g.o.image
	if path == "" {
		path = findDefaultImage(b)
	}
	g.imageEntry.SetText(path)
	g.loadImage()
	g.refreshDisks()
}

func (g *gui) browseImage() {
	d := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
		if err != nil || r == nil {
			return
		}
		r.Close()
		g.imageEntry.SetText(r.URI().Path())
		g.loadImage()
	}, g.win)
	d.SetFilter(storage.NewExtensionFileFilter([]string{".img", ".xz"}))
	if p := g.imageEntry.Text; p != "" {
		if l, err := storage.ListerForURI(storage.NewFileURI(filepath.Dir(p))); err == nil {
			d.SetLocation(l)
		}
	}
	d.Resize(fyne.NewSize(800, 560))
	d.Show()
}

func (g *gui) loadImage() {
	g.image = nil
	path := strings.TrimSpace(g.imageEntry.Text)
	if path == "" {
		g.imageInfo.SetText("No image found. Choose a bdx-system-rock5bplus .img.xz file.")
		g.updateFlashButton()
		return
	}
	info, err := inspectImage(path)
	if err != nil {
		g.imageInfo.SetText("⚠ " + err.Error())
		g.updateFlashButton()
		return
	}
	g.image = &info
	check := "no .sha256 file next to it — its integrity cannot be checked"
	if info.SidecarSHA != "" {
		check = "SHA-256 sidecar found; it is checked before flashing"
	}
	g.imageInfo.SetText(fmt.Sprintf("Release %s · %s to write (%s file)\n%s",
		orUnknown(info.Release), humanBytes(info.RawSize), humanBytes(info.FileSize), check))
	g.updateDiskInfo()
}

func (g *gui) refreshDisks() {
	if g.diskSelect == nil {
		return
	}
	disks, err := candidateDisks(g.showAll.Checked)
	if err != nil {
		g.diskInfo.SetText("⚠ " + err.Error())
		return
	}
	prev := g.selectedDisk()
	g.disks = disks
	labels := make([]string, len(disks))
	for i, d := range disks {
		labels[i] = d.Label()
	}
	g.diskSelect.SetOptions(labels)
	g.diskSelect.ClearSelected()
	for i, d := range disks {
		if prev != nil && d.Path == prev.Path {
			g.diskSelect.SetSelectedIndex(i)
		}
	}
	if len(disks) == 1 && g.diskSelect.SelectedIndex() < 0 {
		g.diskSelect.SetSelectedIndex(0)
	}
	g.updateDiskInfo()
}

func (g *gui) selectedDisk() *Disk {
	if g.diskSelect == nil {
		return nil
	}
	i := g.diskSelect.SelectedIndex()
	if i < 0 || i >= len(g.disks) {
		return nil
	}
	return &g.disks[i]
}

func (g *gui) updateDiskInfo() {
	if g.diskInfo == nil {
		return
	}
	d := g.selectedDisk()
	switch {
	case d == nil && len(g.disks) == 0:
		g.diskInfo.SetText("No removable drive found. Insert the microSD card (or an NVMe/eMMC USB adapter); the list updates automatically.")
	case d == nil:
		g.diskInfo.SetText("Choose the drive to flash.")
	default:
		var lines []string
		if g.image != nil && d.Size < g.image.RawSize+minSpare {
			lines = append(lines, fmt.Sprintf("⚠ Too small: this image needs at least %s (%d GB or more recommended).",
				humanBytes(g.image.RawSize+minSpare), g.board.MinMediaGiB))
		}
		if d.HasData {
			lines = append(lines, "An existing BDX install was found. Its /data (DATA partition) is kept unless you choose Clean install.")
		} else {
			lines = append(lines, "Everything on this drive will be erased.")
		}
		if len(d.Mounts) > 0 {
			lines = append(lines, "Mounted at "+strings.Join(d.Mounts, ", ")+"; it is unmounted before writing.")
		}
		if !d.Removable {
			lines = append(lines, "⚠ This is an internal drive.")
		}
		g.diskInfo.SetText(strings.Join(lines, "\n"))
	}
	g.updateFlashButton()
}

func (g *gui) updateFlashButton() {
	if g.flashBtn == nil {
		return
	}
	if g.maskromMode() {
		if g.busy || g.image == nil || !g.maskPresent || rkdeveloptoolPath() == "" {
			g.flashBtn.Disable()
		} else {
			g.flashBtn.Enable()
		}
		return
	}
	d := g.selectedDisk()
	if g.busy || g.image == nil || d == nil || d.Size < g.image.RawSize+minSpare {
		g.flashBtn.Disable()
	} else {
		g.flashBtn.Enable()
	}
}

func (g *gui) confirmFlash() {
	if g.maskromMode() {
		g.confirmMaskrom()
		return
	}
	d, img := g.selectedDisk(), g.image
	if d == nil || img == nil {
		return
	}
	clean := widget.NewCheck("Clean install (reset system and /data)", nil)
	clean.SetChecked(g.o.eraseAll)
	if !d.HasData {
		clean.SetChecked(true)
		clean.Disable()
	}
	warn := widget.NewLabel("")
	warn.Wrapping = fyne.TextWrapWord
	update := func(on bool) {
		if on {
			warn.SetText("Clean install erases ALL data on the drive, including /data: user files, settings, recordings, keys and local HTTPS certificates. This is not a secure erase.")
		} else {
			warn.SetText("The system is replaced; the existing /data partition and its contents are kept.")
		}
	}
	clean.OnChanged = update
	update(clean.Checked)
	body := container.NewVBox(
		widget.NewLabel(fmt.Sprintf("Board:   %s\nImage:   %s (release %s)\nTarget:  %s",
			g.board.Name, filepath.Base(img.Path), orUnknown(img.Release), d.Label())),
		widget.NewSeparator(), clean, warn)
	dlg := dialog.NewCustomConfirm("Confirm flash", "Flash", "Cancel", body, func(ok bool) {
		if ok {
			g.startFlash(*img, *d, clean.Checked)
		}
	}, g.win)
	dlg.Resize(fyne.NewSize(640, 0))
	dlg.Show()
}

func (g *gui) startFlash(img ImageInfo, d Disk, clean bool) {
	g.setBusy(true)
	g.logText.Reset()
	g.logView.SetText("")
	go func() {
		err := g.flash(img, d, clean)
		fyne.Do(func() {
			g.setBusy(false)
			if err != nil {
				g.setPhase("Flash failed.")
				g.appendLog("ERROR: " + err.Error())
				dialog.ShowError(err, g.win)
				return
			}
			g.progress.SetValue(1)
			g.setPhase("Flash complete.")
			dialog.ShowInformation("Flash complete",
				fmt.Sprintf("%s is ready.\n\nMove it to the %s and power on. The first boot takes a few extra minutes while it prepares /data and updates the Teensy firmware.\n\nLog in as duck.", d.Path, g.board.Name), g.win)
			g.refreshDisks()
		})
	}()
}

func (g *gui) flash(img ImageInfo, d Disk, clean bool) error {
	if img.SidecarSHA != "" {
		fyne.Do(func() { g.setPhase("Verifying image checksum…") })
		if err := verifySidecar(img, g.progressFn()); err != nil {
			return err
		}
		fyne.Do(func() { g.appendLog("Image checksum OK: " + img.SidecarSHA) })
	}
	fyne.Do(func() {
		g.setPhase("Waiting for administrator authorisation…")
		g.progress.SetValue(0)
	})
	var launcher []string
	if os.Geteuid() != 0 {
		launcher = []string{"pkexec"}
	}
	return runHelper(launcher, writeOpts{Image: img.Path, Device: d.Path, Clean: clean}.helperArgs(), g.helperEvents())
}

func (g *gui) helperEvents() func(helperEvent) {
	return func(e helperEvent) {
		fyne.Do(func() {
			switch e.Kind {
			case "phase":
				g.setPhase(e.Text + "…")
				g.appendLog("==> " + e.Text)
				g.progress.SetValue(0)
			case "log", "error":
				g.appendLog(e.Text)
			case "progress":
				if e.Total > 0 {
					g.progress.SetValue(float64(e.Done) / float64(e.Total))
				}
			}
		})
	}
}

func (g *gui) progressFn() func(done, total int64) {
	var last time.Time
	return func(done, total int64) {
		if total <= 0 || (done < total && time.Since(last) < 200*time.Millisecond) {
			return
		}
		last = time.Now()
		fyne.Do(func() { g.progress.SetValue(float64(done) / float64(total)) })
	}
}

// ---- Jetson boards ----

func (g *gui) buildJetsonSteps(b Board) {
	g.flashBtn, g.diskSelect, g.diskInfo = nil, nil, nil
	g.targetMode, g.maskStatus = nil, nil
	steps := widget.NewLabel("1. Power off the board and connect its USB-C port to this computer.\n" +
		"2. Hold the FORCE RECOVERY button (or bridge FC REC to GND on the button header) while applying power.\n" +
		"3. Release the button after a few seconds. The board appears below as a recovery-mode USB device.")
	steps.Wrapping = fyne.TextWrapWord
	g.usbStatus = widget.NewLabel("")
	g.usbStatus.Wrapping = fyne.TextWrapWord
	recoveryCard := widget.NewCard("2 · Recovery mode", "", container.NewVBox(steps, widget.NewSeparator(), g.usbStatus))

	g.bundleEntry = widget.NewEntry()
	g.bundleEntry.SetPlaceHolder("Optional: local bdx-system-" + b.Slug + "-*.tar.gz (default: download the latest release)")
	g.bundleEntry.SetText(g.o.jetson.LocalBundle)
	browse := widget.NewButtonWithIcon("Browse…", theme.FolderOpenIcon(), func() {
		d := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
			if err == nil && r != nil {
				r.Close()
				g.bundleEntry.SetText(r.URI().Path())
			}
		}, g.win)
		d.SetFilter(storage.NewExtensionFileFilter([]string{".gz"}))
		d.Resize(fyne.NewSize(800, 560))
		d.Show()
	})
	fwCard := widget.NewCard("3 · Firmware", "", container.NewBorder(nil, nil, nil, browse, g.bundleEntry))

	g.orinBtn = widget.NewButtonWithIcon("Open BDX Orin Flasher", theme.UploadIcon(), func() {
		o := g.o.jetson
		o.LocalBundle = strings.TrimSpace(g.bundleEntry.Text)
		if o.LocalBundle != "" {
			if o.LocalSHA256 == "" {
				if _, err := os.Stat(o.LocalBundle + ".sha256"); err == nil {
					o.LocalSHA256 = o.LocalBundle + ".sha256"
				}
			}
			if o.LocalEnv == "" {
				env := strings.TrimSuffix(o.LocalBundle, ".tar.gz") + ".env"
				if _, err := os.Stat(env); err == nil {
					o.LocalEnv = env
				}
			}
		}
		if err := launchOrinFlasherGUI(o); err != nil {
			dialog.ShowError(err, g.win)
			return
		}
		g.setPhase("BDX Orin Flasher opened for the " + b.Name + ".")
		g.appendLog("Started " + orinFlasherPath() + " " + strings.Join(o.args(), " "))
	})
	g.orinBtn.Importance = widget.HighImportance
	note := widget.NewLabel("")
	note.Wrapping = fyne.TextWrapWord
	if orinFlasherPath() == "" {
		g.orinBtn.Disable()
		note.SetText("⚠ The BDX Orin Flasher is not installed. Install it to flash Jetson boards: sudo apt install bdx-orin-flasher")
	} else {
		note.SetText("Jetson boards are flashed by the BDX Orin Flasher, which downloads, verifies and flashes the firmware over USB. Its Confirm flash dialog offers Clean install.")
	}

	g.steps.Add(recoveryCard)
	g.steps.Add(fwCard)
	g.steps.Add(note)
	g.steps.Add(container.NewHBox(layout.NewSpacer(), g.orinBtn))
	g.updateUSB()
}

func (g *gui) updateUSB() {
	if g.board.Method != MethodJetson || g.usbStatus == nil {
		return
	}
	if usbPresent(g.board.USBID) {
		g.usbStatus.SetText(fmt.Sprintf("✔ %s detected in recovery mode (USB %s).", g.board.Name, g.board.USBID))
		return
	}
	for _, other := range boards {
		if other.Method == MethodJetson && other.Slug != g.board.Slug && usbPresent(other.USBID) {
			g.usbStatus.SetText(fmt.Sprintf("⚠ A %s is in recovery mode (USB %s), not the selected %s. Select the matching board.",
				other.Name, other.USBID, g.board.Name))
			return
		}
	}
	g.usbStatus.SetText(fmt.Sprintf("Waiting for the board in recovery mode (USB %s)…", g.board.USBID))
}

// watchHardware refreshes the drive list and recovery-mode status as media
// and boards are plugged in.
func (g *gui) watchHardware(stop <-chan struct{}) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	var lastDisks string
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		disks, _ := listDisks()
		var sig strings.Builder
		for _, d := range disks {
			fmt.Fprintf(&sig, "%s:%d:%v;", d.Path, d.Size, d.Mounts)
		}
		changed := sig.String() != lastDisks
		lastDisks = sig.String()
		fyne.Do(func() {
			if g.busy {
				return
			}
			if changed {
				g.refreshDisks()
			}
			g.updateUSB()
			g.updateMaskrom()
		})
	}
}

// ---- status helpers ----

func (g *gui) setBusy(b bool) {
	g.busy = b
	if b {
		g.boardRadio.Disable()
	} else {
		g.boardRadio.Enable()
	}
	g.updateFlashButton()
}

func (g *gui) setPhase(s string) { g.phase.SetText(s) }

func (g *gui) appendLog(s string) {
	g.logText.WriteString(s)
	g.logText.WriteString("\n")
	g.logView.SetText(g.logText.String())
	g.logView.CursorRow = strings.Count(g.logText.String(), "\n")
	g.logView.Refresh()
}
