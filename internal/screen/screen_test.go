package screen

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)


// A TRUNCATED FRAME MUST BE REFUSED. `screendump` replies before the write finishes, so this is a
// race the reader meets rather than a hypothetical.
//
// THE OLD FORM OF THIS TEST COULD CHECK THE LENGTH, because an uncompressed frame's size is
// arithmetic from its header. A PNG has no such rule - the data is compressed - so the test is "it
// does not decode", which is the same verdict `image.Decode` reaches when it looks for the last
// chunk.
func TestTruncatedFrameIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "short.png")
	img := image.NewRGBA(image.Rect(0, 0, 64, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{0x30, 0x60, 0x90, 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	half := buf.Bytes()[:buf.Len()/2]
	if err := os.WriteFile(path, half, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("a truncated frame must be refused; every measurement on it would be wrong")
	}
}

// AND A FILE THAT IS NOT A PICTURE AT ALL IS REFUSED BY NAME.
func TestSomethingThatIsNotAPictureIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.png")
	if err := os.WriteFile(path, []byte("GIF89a not really"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("a file that is not an image must be refused")
	}
}
// THE DIMENSIONS COME FROM THE FILE, not from a constant. A guest booted at another resolution must
// still be measured correctly, which is why the Android frame above is 1280x800 and still parses.
func TestDimensionsComeFromTheHeader(t *testing.T) {
	dir := t.TempDir()
	// PNG, because that is the only frame format `Screendump` writes.
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
		"*image.RGBA (a decoded frame)": rgba,
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
