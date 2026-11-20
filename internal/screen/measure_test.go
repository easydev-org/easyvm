package screen

import (
	"image"
	"image/png"
	"os"
	"testing"
)

// THE MEASUREMENTS MUST REPRODUCE THE VERDICTS THE GO REACHES, or the whole design is wrong.
//
// This is the test that decides whether a picture can be judged from data rather than from a
// function. It reads the frames ALREADY IN testdata - captured from real media - measures them, and
// checks the three verdicts this repository currently reaches in Go:
//
//	mint_desktop.png    IsLinuxDesktop=false   IsCinnamonDesktop=true
//	zorin_desktop.png   IsLinuxDesktop=true
//	mx_grub.png         IsLinuxDesktop=false   (a BIOS menu is not a desktop)
//
// IF THIS TEST CAN PASS, `IsCinnamonDesktop` DOES NOT NEED TO EXIST. If it cannot, the design is
// wrong and the special case stays in Go.
func TestTheMeasurementsReproduceTheDesktopVerdicts(t *testing.T) {
	cases := []struct {
		frame   string
		desktop bool
		andWhy  string
	}{
		{"testdata/mint_desktop.png", true, "its panel is its wallpaper's colour, so only flat_rows sees it"},
		{"testdata/zorin_desktop.png", true, "a panel that differs in brightness from the body"},
		{"testdata/mx_grub.png", false, "a BIOS boot menu has neither a brightness difference nor a flat border"},
	}

	for _, c := range cases {
		img := loadFrame(t, c.frame)
		body := Measure(img, bandRect(0.35, 0.65))
		top := Measure(img, bandRect(0.00, 0.045))
		bottom := Measure(img, bandRect(0.955, 1.00))

		t.Logf("%s\n  body   %s\n  top    %s\n  bottom %s",
			c.frame, body.Explain(), top.Explain(), bottom.Explain())

		got := desktopVerdict(body, top, bottom)
		if got != c.desktop {
			t.Fatalf("%s: the arithmetic says %v and Go says %v - %s\n  body   %s\n  top    %s\n"+
				"  bottom %s", c.frame, got, c.desktop, c.andWhy, body.Explain(), top.Explain(),
				bottom.Explain())
		}
	}
}

// desktopVerdict is the RULE FROM THE FILE, written once here so the test can evaluate it. When
// `want:` exists this function is deleted and the rule lives in the walkthrough.
//
// IT IS DELIBERATELY THE SAME SHAPE THE YAML WILL HAVE:
//
//	any:
//	  - all: [top.spread < 42, top.mean - body.mean > 40, top.sat > 0.05]
//	  - all: [bottom.spread < 42, bottom.mean - body.mean > 40, bottom.sat > 0.05]
//	  - top.flat_rows >= 3
//	  - bottom.flat_rows >= 3
func desktopVerdict(body, top, bottom Reading) bool {
	diff := func(e Reading) float64 {
		d := e.Mean - body.Mean
		if d < 0 {
			d = -d
		}
		return d
	}
	brightBand := func(e Reading) bool {
		return e.Spread < 42 && diff(e) > 40 && e.Sat > 0.05
	}
	return brightBand(top) || brightBand(bottom) || top.FlatRows >= 3 || bottom.FlatRows >= 3
}

func loadFrame(t *testing.T, path string) image.Image {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("%s: this frame is the whole point of the test: %v", path, err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return img
}
