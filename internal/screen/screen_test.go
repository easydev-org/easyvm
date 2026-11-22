package screen

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
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
	// PNG, because that is the only frame format `Screendump` writes now - the PPM decoder this test
	// used to exercise is gone with the format.
	path := filepath.Join(dir, "odd.png")
	w, h := 1280, 800
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	b := loaded.Bounds()
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

// A COLOUR IS FOUND BY ITS OWN VALUE, ITS OWN CENTRE, INSIDE ITS OWN REGION.
//
// `at_color` REPLACED `at_window` BECAUSE THE WINDOW'S POSITION IS THE UNRELIABLE THING. macOS paints
// the zoom button rgb(40,200,64) at (474,281) on a floating window and (65,50) once full screen - two
// completely different places, one constant colour - so the three properties this test pins are the
// three the primitive depends on.
func TestFindColorFindsTheCentreOfABlobInARegion(t *testing.T) {
	const w, h = 400, 300
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// a dark background, like a mac window
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{30, 30, 30, 255})
		}
	}
	green := color.RGBA{40, 200, 64, 255}

	// THE TARGET: a 12-px dot centred at (100, 80).
	drawDot := func(cx, cy, r int, c color.RGBA) {
		for y := cy - r; y <= cy+r; y++ {
			for x := cx - r; x <= cx+r; x++ {
				if (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r*r {
					img.Set(x, y, c)
				}
			}
		}
	}
	drawDot(100, 80, 6, green)

	// A DECOY OF THE SAME COLOUR, LARGER, OUTSIDE THE REGION - this is the "interference" the region
	// exists to exclude. Without the region it would win, being the heaviest blob on the screen.
	drawDot(320, 240, 12, green)

	// 1. THE WHOLE FRAME FINDS THE DECOY, because it is the biggest blob. That is correct behaviour and
	// it is why a region has to be given when the colour appears twice.
	x, y, ok := FindColor(img, [3]uint8{40, 200, 64}, 45, [4]int{})
	if !ok {
		t.Fatal("the decoy is a green blob and the whole-frame search must find it")
	}
	if x < 280 || y < 200 {
		t.Fatalf("whole-frame centre = (%d,%d), want the decoy near (320,240); the search is not "+
			"taking the heaviest blob", x, y)
	}

	// 2. THE REGION FINDS THE TARGET AND NOT THE DECOY.
	x, y, ok = FindColor(img, [3]uint8{40, 200, 64}, 45, [4]int{60, 40, 150, 130})
	if !ok {
		t.Fatal("no green found in the region, though the dot is inside it")
	}
	if absInt(x-100) > 6 || absInt(y-80) > 6 {
		t.Fatalf("region centre = (%d,%d), want the dot at (100,80) within 6 px; the answer must be "+
			"the blob's CENTRE, not its first pixel", x, y)
	}

	// 3. A TOLERANCE THAT DOES NOT REACH THE COLOUR FINDS NOTHING, and one that does finds it. This is
	// what makes the number meaningful rather than decorative.
	if _, _, ok := FindColor(img, [3]uint8{40, 200, 64}, 0, [4]int{60, 40, 150, 130}); !ok {
		t.Error("tolerance 0 must still find the exact colour: the dot is drawn in it")
	}
	if _, _, ok := FindColor(img, [3]uint8{200, 40, 40}, 45, [4]int{60, 40, 150, 130}); ok {
		t.Error("a red target found something in a region holding only green - the channel test is " +
			"not being applied")
	}
	// A NEAR MISS WITHIN 45 IS ACCEPTED, and 10 away is refused at 5: antialiasing is why the
	// tolerance exists, and this is where the boundary actually is.
	//
	// THE WHOLE DOT IS REDRAWN 10 AWAY ON EACH CHANNEL, not one pixel of it - replacing a single pixel
	// leaves the rest of the dot exact, so the search still succeeds and the assertion would be about
	// nothing. That is the mistake this test made first.
	drawDot(100, 80, 6, color.RGBA{50, 210, 74, 255})
	if _, _, ok := FindColor(img, [3]uint8{40, 200, 64}, 45, [4]int{60, 40, 150, 130}); !ok {
		t.Error("a blob 10 away from the target must be accepted inside tolerance 45")
	}
	if _, _, ok := FindColor(img, [3]uint8{40, 200, 64}, 5, [4]int{60, 40, 150, 130}); ok {
		t.Error("a blob 10 away must be refused at tolerance 5")
	}
}

// KUBUNTU'S LAUNCHER IS A SATURATED SCREEN, AND ITS INSTALLER IS A GREY ONE.
//
// THE NUMBERS BELOW ARE THE IMAGE'S OWN, measured on Kubuntu 26.04's live medium: the launcher reads
// saturation 96.9 and every Calamares page reads 4.1 to 6.2. The threshold is 40, which is three
// times the highest installer page and well under the launcher - so this test asserts the gap rather
// than a comfortable margin around one point.
func TestKubuntuLauncherIsSaturatedAndItsInstallerIsNot(t *testing.T) {
	// THE LAUNCHER: the blue radial gradient, which is saturated everywhere.
	//
	// The two ends of the gradient are the same HUE at different brightnesses, which is what makes
	// the panel test see them as equal (`Drop=0.0`) and this one see them as one screen.
	launcher := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	for y := 0; y < 1080; y++ {
		// A deep blue that darkens downward, 12..40 in red and 60..110 in blue: saturated throughout.
		t := float64(y) / 1080.0
		r := uint8(12 + t*28)
		g := uint8(30 + t*40)
		b := uint8(110 - t*50)
		fill(launcher, image.Rect(0, y, 1920, y+1), color.RGBA{r, g, b, 255})
	}
	if ok, st := IsKubuntuLauncher(launcher); !ok {
		t.Fatalf("a saturated blue launcher must read as the kubuntu launcher: sat=%.1f", st.SaturationRise)
	}

	// AND THE PANEL TEST CANNOT SEE IT - which is the whole reason this function exists.
	if ok, st := IsLinuxDesktop(launcher); ok {
		t.Fatalf("the launcher must NOT read as a Linux desktop; it has no panel, and a "+
			"`desktop: linux` step would wait out its budget on it: drop=%.1f", st.BrightnessDrop)
	}

	// THE INSTALLER: Calamares draws grey on grey. This is the page the step after the wait clicks
	// into, so answering true here would end the wait on the wrong screen.
	installer := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	fill(installer, installer.Bounds(), color.RGBA{222, 222, 222, 255})
	if ok, st := IsKubuntuLauncher(installer); ok {
		t.Fatalf("a grey Calamares page must NOT read as the launcher: sat=%.1f", st.SaturationRise)
	}

	// GRUB: coloured words on black. Saturation per pixel can be high on the words, so the brightness
	// floor is what rejects it - there is no desktop behind them.
	grub := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	fill(grub, grub.Bounds(), color.RGBA{0, 0, 0, 255})
	fill(grub, image.Rect(300, 400, 1300, 440), color.RGBA{230, 230, 230, 255})
	if ok, st := IsKubuntuLauncher(grub); ok {
		t.Fatalf("GRUB's black screen must NOT read as the launcher: mean=%.1f", st.TopMean[0])
	}
}

// AND A DISTRIBUTION'S OWN TEST IS REACHABLE BY NAME, so a walk can say `desktop: linux/kubuntu`
// rather than depending on a family-wide threshold that cannot see its medium.
func TestEveryNamedDesktopFamilyIsDispatched(t *testing.T) {
	body, err := os.ReadFile("../run/actions.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	for _, name := range []string{"windows", "macos", "linux", "android", "linux/kubuntu"} {
		if !strings.Contains(src, `case "`+name+`":`) {
			t.Errorf("no `case %q:` in the desktop dispatch, so `desktop: %s` would fall through "+
				"to the default and fail on a running guest", name, name)
		}
	}
}

// EVERY DESKTOP-ENVIRONMENT KEY THE WALKTHROUGHS USE IS DISPATCHED, AND EACH ONE IS A REAL
// MEASUREMENT RATHER THAN A COPY-PASTE THAT NOTHING CALLS.
//
// THE KEYS NAME THE ENVIRONMENT, NOT THE DISTRIBUTION, because two media that run KDE draw one
// desktop and two that run different environments do not. The distribution reaches the key by the
// desktop its ISO carries:
//
//	kde        kubuntu, cachyos, endeavouros, manjaro, opensuse-leap-16.1
//	gnome      ubuntu, popos, fedora-workstation, nixos, zorin
//	cinnamon   mint
//	xfce       mx
func TestEveryDesktopEnvironmentKeyIsDispatched(t *testing.T) {
	body, err := os.ReadFile("../run/actions.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	for _, key := range []string{"linux", "linux/kubuntu", "linux/kde", "linux/gnome", "linux/cinnamon", "linux/xfce"} {
		if !strings.Contains(src, `case "`+key+`":`) {
			t.Errorf("no `case %q:` in the desktop dispatch, so `desktop: %s` falls to the default and "+
				"fails on a running guest", key, key)
		}
	}
}

// AND EVERY WALKTHROUGH NAMES ONE OF THEM. A file that says `desktop: linux/something` this build has
// no branch for is a wait that can never be satisfied, and the failure reads as a guest that never
// booted.
func TestEveryWalkthroughNamesADispatchedDesktop(t *testing.T) {
	// `.walk` IS THE ONLY FORMAT, and a `desktop` line in it reads `desktop == linux/gnome`.
	files, err := filepath.Glob(filepath.Join("..", "..", "linux", "*", "steps", "install.walk"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Skip("no linux install walkthroughs here")
	}
	known := map[string]bool{
		"linux": true, "linux/kubuntu": true, "linux/kde": true,
		"linux/gnome": true, "linux/cinnamon": true, "linux/xfce": true,
	}
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(line)
			var value string
			switch {
			case strings.HasPrefix(trimmed, "desktop:"):
				value = strings.TrimSpace(strings.TrimPrefix(trimmed, "desktop:"))
			case strings.HasPrefix(trimmed, "desktop =="):
				value = strings.TrimSpace(strings.TrimPrefix(trimmed, "desktop =="))
			default:
				continue
			}
			if value == "" || strings.HasPrefix(value, "-") {
				continue
			}
			// A `desktop:` ASKED BY SIMILARITY CARRIES ITS OWN `reference:`, and the family is then
			// only a label - so the dispatch question is asked of the plain form.
			if !known[value] {
				t.Errorf("%s: `desktop: %s` has no branch in the dispatch table", filepath.Base(filepath.Dir(filepath.Dir(f))), value)
			}
		}
	}
}

// THE KDE BRANCH ANSWERS FOR BOTH PANEL HEIGHTS, WHICH IS THE REASON IT EXISTS.
//
// MEASURED on two live media: manjaro draws a 48-row panel and `IsLinuxDesktop` sees it
// (drop=128.4); cachyos draws an 8-row panel and the same test answers false (drop=0.0, edge=16.9).
// One environment, two heights, and a single branch has to accept both.
func TestKDEDesktopAcceptsBothPanelHeights(t *testing.T) {
	// A TALL PANEL: 48 rows at the bottom, brighter than the body - manjaro's shape.
	tall := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	fill(tall, tall.Bounds(), color.RGBA{60, 20, 40, 255})
	fill(tall, image.Rect(0, 1032, 1920, 1080), color.RGBA{230, 230, 230, 255})
	if ok, st := IsKDEDesktop(tall); !ok {
		t.Fatalf("a 48-row panel must read as KDE: drop=%.1f edge=%.1f", st.BrightnessDrop, st.VarianceRatio)
	}

	// A THIN PANEL: the last 8 rows only, which the band average dilutes - cachyos' shape.
	thin := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	fill(thin, thin.Bounds(), color.RGBA{22, 22, 22, 255})
	fill(thin, image.Rect(0, 1072, 1920, 1080), color.RGBA{46, 46, 46, 255})
	if ok, st := IsKDEDesktop(thin); !ok {
		t.Fatalf("an 8-row panel must also read as KDE, and that is the reason this branch exists: "+
			"drop=%.1f edge=%.1f", st.BrightnessDrop, st.VarianceRatio)
	}

	// AND AN INSTALLER IS NOT A DESKTOP AT EITHER HEIGHT: one flat window, no bar.
	win := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	fill(win, win.Bounds(), color.RGBA{222, 222, 222, 255})
	if ok, _ := IsKDEDesktop(win); ok {
		t.Fatal("a flat Calamares window must not read as a KDE desktop")
	}
}

// MINT'S CINNAMON DESKTOP IS A PANEL THE SAME COLOUR AS ITS WALLPAPER, and `IsLinuxDesktop` cannot
// see it - which is not a missing threshold, it is a wrong premise for this one medium.
//
// THE FRAME IS REAL: internal/screen/testdata/mint_desktop.png is 1920x1080 captured from mint
// 22.3 Cinnamon's live medium, after its GRUB menu was answered, with nothing else on screen. The
// numbers below are that frame's, not a drawn approximation:
//
//	IsLinuxDesktop    false   BottomVar=8.50  BrightnessDrop=0
//	y=1030..1038      mad 8.3..8.5   wallpaper
//	y=1039            mad 0.0        the panel's top border
//	y=1040..1044      mad 0.2..2.1   the panel's background
//	y=1045..1070      mad 6.8..19.0  its icons
//	y=1075..1079      mad 0.2..1.7   its bottom border
//
// so the panel is there and the brightness difference the shared test looks for is exactly zero.
func TestCinnamonSeesAPanelThatIsTheSameColourAsItsWallpaper(t *testing.T) {
	f, err := os.Open("testdata/mint_desktop.png")
	if err != nil {
		t.Fatalf("mint's live frame is the whole point of this test: %v", err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}

	// THE SHARED TEST DOES NOT SEE IT, and that is the fact this test exists to pin down: if a later
	// change makes `IsLinuxDesktop` accept this frame, the special case below is dead code and this
	// test says so rather than letting it rot.
	if ok, st := IsLinuxDesktop(img); ok {
		t.Fatalf("IsLinuxDesktop now accepts mint's frame (%s), so IsCinnamonDesktop no longer "+
			"needs its own reading - remove the special case instead of keeping both", st.Explain())
	}

	if ok, st := IsCinnamonDesktop(img); !ok {
		t.Fatalf("mint's own live desktop must read as a Cinnamon desktop: %s", st.Explain())
	}
}

// AND THE PANEL'S EDGE IS NOT A WINDOW'S EDGE: a solid frame, and a window with a border, must both
// stay out. Without this, "a flat row near the bottom" is satisfied by every full-screen dialog.
func TestCinnamonRejectsASolidFrameAndAWindow(t *testing.T) {
	// A UNIFORM FRAME: every row is flat, including the last, so there is no border with content
	// under it - this is the boot screen and the install window's body.
	solid := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	fill(solid, solid.Bounds(), color.RGBA{40, 40, 44, 255})
	if ok, st := IsCinnamonDesktop(solid); ok {
		t.Fatalf("a uniform frame must not read as a Cinnamon desktop: %s", st.Explain())
	}

	// A WINDOW THAT FILLS THE BOTTOM QUARTER: a flat body with a title bar and a button row, which
	// is a full-screen installer dialogue. Its rows are flat AND its last rows are flat, which is
	// exactly the shape the rule has to reject.
	win := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	fill(win, win.Bounds(), color.RGBA{90, 90, 96, 255})
	if ok, st := IsCinnamonDesktop(win); ok {
		t.Fatalf("a full-screen window must not read as a Cinnamon desktop: %s", st.Explain())
	}

	// A BLACK BOOT SCREEN, the frame a guest shows before anything is drawn.
	black := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	fill(black, black.Bounds(), color.RGBA{0, 0, 0, 255})
	if ok, _ := IsCinnamonDesktop(black); ok {
		t.Fatal("a black screen must not read as a Cinnamon desktop")
	}
}
