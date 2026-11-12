package screen

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
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

// EVERY DESKTOP A WALKTHROUGH ASKS FOR HAS A TEST, AND THE DISPATCHER MUST REACH ALL OF THEM.
//
// `desktop:` HAD ONE BRANCH, FOR WINDOWS. `macos/15/steps/ssh.yaml` asks for `desktop: macos` and
// every Linux file asks for its own - so two thirds of the migrated walkthroughs would have failed
// on their first frame with "the desktop test is not implemented", AFTER the machine had booted.
func TestEveryFamilyTheFilesAskForIsImplemented(t *testing.T) {
	body, err := os.ReadFile("../run/actions.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	for _, family := range []string{"windows", "macos", "linux"} {
		if !strings.Contains(src, `case "`+family+`":`) {
			t.Fatalf("`desktop: %s` has no branch in the dispatcher. A walkthrough that asks for "+
				"it fails on its first frame, after the guest is up", family)
		}
	}
}

// A LINUX DESKTOP IS A FLAT BAND ALONG AN EDGE; AN INSTALLER IS NOT.
//
// The five desktops these images run - GNOME, KDE, XFCE, MATE, Cinnamon - all draw a panel, and the
// installers they boot draw a full-bleed window instead. The test is brightness margin plus
// flatness, so a dark wallpaper edge and a window's title bar both fail it.
func TestALinuxDesktopIsAPanelAndAnInstallerIsNot(t *testing.T) {
	// A DESKTOP: a bright, flat strip along the top and a mid-grey body under it.
	desktop := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	fill(desktop, image.Rect(0, 0, 1920, 44), color.RGBA{44, 48, 60, 255})     // the panel
	fill(desktop, image.Rect(0, 44, 1920, 1080), color.RGBA{96, 88, 120, 255}) // the wallpaper
	if ok, st := IsLinuxDesktop(desktop); !ok {
		t.Fatalf("a panel over a wallpaper must read as a desktop: %s", st.Explain())
	}

	// AN INSTALLER: one flat window with no strip along either edge.
	installer := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	fill(installer, installer.Bounds(), color.RGBA{96, 96, 96, 255})
	if ok, st := IsLinuxDesktop(installer); ok {
		t.Fatalf("a uniform window must NOT read as a desktop: %s", st.Explain())
	}

	// A BLACK BOOT SCREEN: uniform, so no band is brighter than the body.
	black := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	fill(black, black.Bounds(), color.RGBA{0, 0, 0, 255})
	if ok, _ := IsLinuxDesktop(black); ok {
		t.Fatal("a black screen must not read as a desktop")
	}
}

// A MACOS DESKTOP IS DECIDED BY THE DOCK AND THE BODY, and the dispatcher had no branch for it -
// `desktop: macos` would have failed on the first frame of a guest that was already up.
func TestMacOSDesktopIsReachableAndExplainsItself(t *testing.T) {
	// A WALLPAPER WITH A COLOURFUL DOCK: the shape a finished macOS desktop has.
	img := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	fill(img, image.Rect(0, 0, 1920, 1000), color.RGBA{40, 70, 130, 255})
	fill(img, image.Rect(0, 1000, 1920, 1080), color.RGBA{70, 40, 40, 255})
	for x := 200; x < 900; x += 90 {
		fill(img, image.Rect(x, 1010, x+60, 1070), color.RGBA{200, 90, 60, 255})
	}
	ok, st := IsMacOSDesktop(img)
	// The numbers are the contract: a failed wait reports them, so they must be populated whether
	// or not this particular synthetic frame passes.
	if st.Width != 1920 || st.Height != 1080 {
		t.Fatalf("the stats lost the frame size: %+v", st)
	}
	shared := st.Stats()
	if shared.Width != 1920 {
		t.Fatalf("Stats() did not carry the size through: %+v", shared)
	}
	if ok && st.DockColourful <= 0 {
		t.Fatalf("a frame that reads as a desktop must report a colourful dock: %s", st.Explain())
	}
	if msg := st.Explain(); msg == "" {
		t.Fatal("Explain() returned nothing, so a failed wait would report no numbers")
	}
}

// fill paints a rectangle, for the synthetic frames above.
func fill(img *image.RGBA, r image.Rectangle, c color.RGBA) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.SetRGBA(x, y, c)
		}
	}
}

// CROP MUST NOT CARE WHICH DECODER PRODUCED THE FRAME.
//
// `image.Decode` RETURNS A DIFFERENT CONCRETE TYPE PER FORMAT. qemu's screendump is P6 PPM, which
// this package decodes to `*image.RGBA`; the PNG that the lab tooling writes decodes to
// `*image.NRGBA`. A hard type assertion on the pixel value therefore works on every frame a
// walkthrough reads and PANICS on the frames a person looks at.
//
// MEASURED ON linux-ubuntu-install, WHICH HAD ALREADY PASSED EVERY PANE:
//
//	=== the installer finishes
//	  waiting up to 1h30m0s for "installation complete" ...
//	panic: interface conversion: color.Color is nrgb.Color, not color.RGBA
//	goroutine 18: internal/screen.Crop(...)
//
// - a finished install reported as a failure, with a stack trace instead of a reason.
func TestCropAcceptsEveryDecodedImageKind(t *testing.T) {
	// THE SAME PICTURE, ENCODED BOTH WAYS A FRAME ARRIVES.
	rgba := image.NewRGBA(image.Rect(0, 0, 40, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 40; x++ {
			rgba.SetRGBA(x, y, color.RGBA{R: uint8(x * 6), G: uint8(y * 12), B: 200, A: 255})
		}
	}
	nrgba := image.NewNRGBA(rgba.Bounds())
	for y := 0; y < 20; y++ {
		for x := 0; x < 40; x++ {
			nrgba.Set(x, y, rgba.At(x, y))
		}
	}

	for name, img := range map[string]image.Image{
		"*image.RGBA (a PPM screendump)": rgba,
		"*image.NRGBA (a PNG or JPEG)":   nrgba,
		"*image.Gray (a single-channel)": image.NewGray(rgba.Bounds()),
	} {
		// A PANIC HERE IS THE FAILURE, so it is caught rather than allowed to abort the test binary.
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Crop panicked on %s: %v. The pixel conversion has to work for any "+
						"color.Color, not just the one qemu's own decoder produces", name, r)
				}
			}()
			out, err := Crop(img, [4]int{2, 2, 20, 12}, 2)
			if err != nil {
				t.Fatalf("Crop on %s: %v", name, err)
			}
			if b := out.Bounds(); b.Dx() != 36 || b.Dy() != 20 {
				t.Fatalf("Crop on %s produced %dx%d, want 36x20", name, b.Dx(), b.Dy())
			}
		}()
	}
}
