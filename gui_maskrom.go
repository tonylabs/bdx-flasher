package main

import (
	"fmt"
	"os"
	"path/filepath"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

const (
	targetDrive   = "Drive on this computer (microSD card / USB adapter)"
	targetMaskrom = "Board in Maskrom mode (onboard eMMC, over USB)"
)

func (g *gui) buildMaskromCard(b Board) {
	steps := widget.NewLabel("1. Remove the microSD card from the board.\n" +
		"2. Connect the board's USB OTG port to this computer (see Radxa's documentation for which port it is).\n" +
		"3. Hold the MASKROM button, apply power, and release the button after 2–3 seconds.\n" +
		"The board then appears below. The whole eMMC is overwritten: a maskrom flash is always a clean install, and /data is recreated on first boot.")
	steps.Wrapping = fyne.TextWrapWord
	g.maskStatus = widget.NewLabel("")
	g.maskStatus.Wrapping = fyne.TextWrapWord
	g.maskCard = widget.NewCard("3 · Maskrom mode", "", container.NewVBox(steps, widget.NewSeparator(), g.maskStatus))
	g.targetMode = widget.NewRadioGroup([]string{targetDrive, targetMaskrom}, func(string) {
		if g.maskromMode() {
			g.diskCard.Hide()
			g.maskCard.Show()
		} else {
			g.maskCard.Hide()
			g.diskCard.Show()
		}
		g.updateMaskrom()
		g.steps.Refresh()
	})
	g.targetMode.Required = true
}

func (g *gui) maskromMode() bool {
	return g.board.Method == MethodImage && g.targetMode != nil && g.targetMode.Selected == targetMaskrom
}

func (g *gui) updateMaskrom() {
	if g.maskStatus == nil || g.board.MaskromUSBID == "" {
		return
	}
	g.maskPresent = usbPresent(g.board.MaskromUSBID)
	switch {
	case rkdeveloptoolPath() == "":
		g.maskStatus.SetText("⚠ rkdeveloptool is not installed: sudo apt install rkdeveloptool")
	case g.maskPresent:
		g.maskStatus.SetText(fmt.Sprintf("✔ Rockchip RK3588 board detected in Maskrom/Loader mode (USB %s).", g.board.MaskromUSBID))
	default:
		g.maskStatus.SetText(fmt.Sprintf("Waiting for the board in Maskrom mode (USB %s)…", g.board.MaskromUSBID))
	}
	g.updateFlashButton()
}

func (g *gui) confirmMaskrom() {
	img := g.image
	if img == nil {
		return
	}
	body := widget.NewLabel(fmt.Sprintf("Board:   %s\nImage:   %s (release %s)\nTarget:  onboard eMMC over USB (Maskrom)\n\n"+
		"Clean install: ALL data on the eMMC is erased, including /data (user files, settings, recordings, keys and local HTTPS certificates). This is not a secure erase.\n\n"+
		"The first run downloads Radxa's RK3588 USB loader (%s, checked against a pinned SHA-256).",
		g.board.Name, filepath.Base(img.Path), orUnknown(img.Release), g.board.Loader.Name))
	body.Wrapping = fyne.TextWrapWord
	dlg := dialog.NewCustomConfirm("Confirm flash", "Erase and flash", "Cancel", body, func(ok bool) {
		if ok {
			g.startMaskrom(*img)
		}
	}, g.win)
	dlg.Resize(fyne.NewSize(640, 0))
	dlg.Show()
}

func (g *gui) startMaskrom(img ImageInfo) {
	b := g.board
	g.setBusy(true)
	g.logText.Reset()
	g.logView.SetText("")
	go func() {
		err := g.flashMaskrom(b, img)
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
				fmt.Sprintf("The %s's eMMC is flashed and the board is restarting.\n\nWith no microSD card inserted it boots from the eMMC. The first boot takes a few extra minutes while it prepares /data and updates the Teensy firmware.\n\nLog in as duck.", b.Name), g.win)
			g.updateMaskrom()
		})
	}()
}

func (g *gui) flashMaskrom(b Board, img ImageInfo) error {
	if img.SidecarSHA != "" {
		fyne.Do(func() { g.setPhase("Verifying image checksum…") })
		if err := verifySidecar(img, g.progressFn()); err != nil {
			return err
		}
		fyne.Do(func() { g.appendLog("Image checksum OK: " + img.SidecarSHA) })
	}
	fyne.Do(func() { g.setPhase("Getting the RK3588 USB loader…") })
	loader, err := ensureLoader(b, g.o.loader)
	if err != nil {
		return err
	}
	fyne.Do(func() { g.appendLog("Loader: " + loader) })

	if img.Compressed {
		fyne.Do(func() {
			g.setPhase("Decompressing the image…")
			g.progress.SetValue(0)
		})
	}
	raw, temp, err := prepareRawImage(img, g.progressFn())
	if err != nil {
		return err
	}
	if temp {
		defer func() {
			os.Remove(raw)
			os.Remove(raw + ".source")
		}()
	}

	fyne.Do(func() {
		g.setPhase("Waiting for administrator authorisation…")
		g.progress.SetValue(0)
	})
	var launcher []string
	if os.Geteuid() != 0 {
		launcher = []string{"pkexec"}
	}
	return runHelper(launcher, maskromOpts{Image: raw, Loader: loader}.helperArgs(b), g.helperEvents())
}
