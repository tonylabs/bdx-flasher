package main

import (
	"image/png"
	"os"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
)

func TestRender(t *testing.T) {
	out := os.Getenv("RENDER_DIR")
	if out == "" {
		t.Skip("RENDER_DIR not set")
	}
	a := test.NewApp()
	defer a.Quit()
	g := &gui{win: a.NewWindow(appName)}
	g.build(fyne.NewStaticResource("icon.png", iconPNG))
	g.win.Resize(fyne.NewSize(980, 720))
	for _, slug := range []string{"rock5bplus", "nano"} {
		b, _ := boardBySlug(slug)
		g.boardRadio.SetSelected(b.Name)
		f, _ := os.Create(out + "/" + slug + ".png")
		png.Encode(f, g.win.Canvas().Capture())
		f.Close()
	}
	b, _ := boardBySlug("rock5bplus")
	g.boardRadio.SetSelected(b.Name)
	g.targetMode.SetSelected(targetMaskrom)
	f, _ := os.Create(out + "/rock5bplus-maskrom.png")
	png.Encode(f, g.win.Canvas().Capture())
	f.Close()
}
