package screen

import (
	"image"
	"image/color"
	"testing"
)

// THE THRESHOLD IN A `check: {mode: like}` STEP IS ONLY USABLE BECAUSE OF THE GAP THESE TESTS PIN.
//
// MEASURED over this repository's own CI recordings (ubuntu, windows, macos, freebsd), comparing at
// stride 4 with a per-channel tolerance of 8:
//
//	same image against itself                    0.00%
//	one second apart, same screen            0.53 - 0.90%
//	five seconds apart, same screen          0.63 - 0.88%
//	TWO DIFFERENT SCREENS                   98.33 - 99.98%
//
// A threshold anywhere between about 2% and 95% works, which is why the number can be the file's own
// rather than something tuned per guest.

func solid(w, h int, c color.Color) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

// TestIdenticalImagesCompareEqual is the floor: nothing changed.
func TestIdenticalImagesCompareEqual(t *testing.T) {
	a := solid(200, 100, color.RGBA{40, 80, 120, 255})
	b := solid(200, 100, color.RGBA{40, 80, 120, 255})
	d, err := Compare(a, b, 8)
	if err != nil {
		t.Fatal(err)
	}
	if d.Changed != 0 {
		t.Fatalf("%.4f%% of pixels differ between two identical images", d.Changed*100)
	}
	if d.MaxDelta != 0 {
		t.Fatalf("max delta is %d between identical images", d.MaxDelta)
	}
}

// TestASmallLocalChangeStaysBelowADefaultThreshold is the "a clock ticked" case.
//
// ONE PERCENT OF THE SCREEN IS MORE THAN A CLOCK OCCUPIES, and a threshold above it accepts the
// screen. This is the number a file should write.
func TestASmallLocalChangeStaysBelowADefaultThreshold(t *testing.T) {
	a := solid(400, 400, color.RGBA{200, 200, 200, 255})
	img := image.NewRGBA(image.Rect(0, 0, 400, 400))
	for y := 0; y < 400; y++ {
		for x := 0; x < 400; x++ {
			img.Set(x, y, color.RGBA{200, 200, 200, 255})
		}
	}
	// A 40x40 block in the corner is 1% of a 400x400 screen.
	for y := 0; y < 40; y++ {
		for x := 0; x < 40; x++ {
			img.Set(x, y, color.RGBA{0, 0, 0, 255})
		}
	}
	d, err := Compare(img, a, 8)
	if err != nil {
		t.Fatal(err)
	}
	if d.Changed > 0.02 {
		t.Fatalf("%.2f%% changed for a 1%% block; the sampling is wrong", d.Changed*100)
	}
	if d.MaxDelta != 200 {
		t.Fatalf("max delta is %d and the block is pure black on light grey, so it should be 200",
			d.MaxDelta)
	}
	// THE MAX DELTA IS THE POINT: a tiny region that changed COMPLETELY is invisible to the share
	// but obvious to MaxDelta, which is why both are reported.
	if d.Changed == 0 {
		t.Fatal("a 40x40 black block on light grey registered as no change at all")
	}
}

// TestADifferentScreenIsAlmostEntirelyDifferent is the ceiling, and it is what makes the threshold
// safe: another pane is not 5% different, it is 98% different.
func TestADifferentScreenIsAlmostEntirelyDifferent(t *testing.T) {
	a := solid(400, 300, color.RGBA{20, 20, 30, 255})    // a dark pane
	b := solid(400, 300, color.RGBA{230, 230, 235, 255}) // a light one
	d, err := Compare(a, b, 8)
	if err != nil {
		t.Fatal(err)
	}
	if d.Changed < 0.95 {
		t.Fatalf("%.2f%% changed between a dark and a light screen; the threshold has no gap to "+
			"sit in", d.Changed*100)
	}
}

// TestASizeMismatchIsAnError covers the mistake a reference invites: the guest switched resolution.
func TestASizeMismatchIsAnError(t *testing.T) {
	a := solid(200, 100, color.RGBA{0, 0, 0, 255})
	b := solid(100, 200, color.RGBA{0, 0, 0, 255})
	if _, err := Compare(a, b, 8); err == nil {
		t.Fatal("comparing a 200x100 frame to a 100x200 reference was accepted. A reference is a " +
			"picture of ONE screen size, and comparing the shared corner would report them identical")
	}
}

// TestToleranceAbsorbsEncoderNoise is what the tolerance argument is for.
func TestToleranceAbsorbsEncoderNoise(t *testing.T) {
	a := solid(100, 100, color.RGBA{100, 100, 100, 255})
	b := solid(100, 100, color.RGBA{104, 104, 104, 255}) // four levels off
	// A tolerance of 8 must accept it and a tolerance of 0 must not.
	loose, err := Compare(b, a, 8)
	if err != nil {
		t.Fatal(err)
	}
	if loose.Changed != 0 {
		t.Fatalf("a 4-level difference counted as a change at tolerance 8: %.2f%%", loose.Changed*100)
	}
	strict, err := Compare(b, a, 0)
	if err != nil {
		t.Fatal(err)
	}
	if strict.Changed != 1 {
		t.Fatalf("a 4-level difference did not count at tolerance 0: %.2f%%", strict.Changed*100)
	}
}
