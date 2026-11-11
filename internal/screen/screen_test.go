package screen

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"
)

// A REAL FRAME IS THE ONLY HONEST FIXTURE, and these five are on this machine from earlier runs of
// the Python implementation. None of them is a Windows desktop, so they test the half of the
// question that matters most: a test that says "desktop" over a black screen or an installer pane
// fails a healthy guest, and the reader sees "NO DESKTOP" above a screenshot of a desktop.
var realFrames = []string{
	"/tmp/_matrix.ppm",       // an installer pane
	"/tmp/_u.ppm",            // a wizard page at a different resolution
	"/tmp/d3.ppm",            // a dark screen
	"/tmp/black.ppm",         // nothing lit at all
	"/tmp/blissshot/hw1.ppm", // an Android live image at 1280x800
}

func TestRealNonDesktopFramesAreNotDesktops(t *testing.T) {
	checked := 0
	for _, path := range realFrames {
		img, err := Load(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Logf("%s: %v (skipped)", path, err)
			continue
		}
		ok, stats := IsWindowsDesktop(img)
		checked++
		if ok {
			t.Fatalf("%s was reported a Windows desktop: %s\n"+
				"a false positive fails a healthy guest and reads as NO DESKTOP over a picture "+
				"of one", filepath.Base(path), stats.Explain())
		}
		t.Logf("%-14s not a desktop: %s", filepath.Base(path), stats.Explain())
	}
	if checked == 0 {
		t.Skip("no real frames on this machine; the synthetic tests below still run")
	}
}

// buildPPM writes a P6 frame.
func buildPPM(t *testing.T, w, h int, pixel func(x, y int) color.RGBA) image.Image {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, pixel(x, y))
		}
	}
	return img
}

// A SYNTHETIC DESKTOP IS BUILT FROM THE MEASUREMENTS, so the test says what the numbers mean rather
// than freezing whatever the code happened to produce: a bright flat taskbar on top, a dark busy
// wallpaper below, and the three ratios that separate them from a wizard pane.
func TestASyntheticDesktopPasses(t *testing.T) {
	img := buildPPM(t, 1920, 1080, func(x, y int) color.RGBA {
		switch {
		case y < 216: // top band: a flat, bright taskbar
			return color.RGBA{R: 240, G: 240, B: 240, A: 0xff}
		case y > 864: // bottom band: a dark, varied wallpaper
			// A vertical gradient plus a horizontal ripple gives both the luminance variance and
			// the colour spread the real test looks for.
			base := uint8(20 + (y*7)%60)
			return color.RGBA{
				R: base,
				G: uint8(20 + (x*3)%90),
				B: uint8(60 + (x*5)%120),
				A: 0xff,
			}
		}
		return color.RGBA{R: 128, G: 128, B: 128, A: 0xff}
	})

	ok, stats := IsWindowsDesktop(img)
	if !ok {
		t.Fatalf("a frame built to the measured shape must pass; got %s", stats.Explain())
	}
	t.Logf("synthetic desktop: %s", stats.Explain())
}

// AND A LIT FLAT SCREEN MUST FAIL, because that is what a lock screen is and the Windows stage has
// a separate check for it. Treating a lock screen as a desktop is how a stage types a password into
// a wallpaper.
func TestABrightFlatScreenIsNotADesktop(t *testing.T) {
	img := buildPPM(t, 1920, 1080, func(x, y int) color.RGBA {
		return color.RGBA{R: 210, G: 210, B: 210, A: 0xff}
	})
	ok, stats := IsWindowsDesktop(img)
	if ok {
		t.Fatalf("a flat bright screen is a lock screen, not a desktop; got %s", stats.Explain())
	}
}

// A BLACK SCREEN MUST FAIL, and this is the case the first version of the Python test got wrong:
// it passed 274 of 349 frames, including the boot, because a dark menu bar and an unlit framebuffer
// look the same to a "is the top dark" test.
func TestABlackScreenIsNotADesktop(t *testing.T) {
	img := buildPPM(t, 1920, 1080, func(x, y int) color.RGBA {
		return color.RGBA{A: 0xff}
	})
	if ok, stats := IsWindowsDesktop(img); ok {
		t.Fatalf("a black screen must not be a desktop; got %s", stats.Explain())
	}
}

// --- the parser -----------------------------------------------------------------------

func TestTruncatedFrameIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "short.ppm")
	// A correct header and half the pixels.
	w, h := 64, 48
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "P6\n%d %d\n255\n", w, h)
	buf.Write(make([]byte, w*h*3/2))
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("a truncated frame must be refused; every measurement on it would be wrong")
	}
}

func TestNonPPMIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.ppm")
	if err := os.WriteFile(path, []byte("GIF89a not really"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("a file that is not a binary PPM must be refused")
	}
}

// THE DIMENSIONS COME FROM THE FILE, not from a constant. A guest booted at another resolution must
// still be measured correctly, which is why the Android frame above is 1280x800 and still parses.
func TestDimensionsComeFromTheHeader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "odd.ppm")
	w, h := 1280, 800
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "P6\n%d %d\n255\n", w, h)
	buf.Write(make([]byte, w*h*3))
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	img, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	if b.Dx() != w || b.Dy() != h {
		t.Fatalf("read %dx%d, want %dx%d", b.Dx(), b.Dy(), w, h)
	}
}
