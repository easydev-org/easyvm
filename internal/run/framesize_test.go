package run

// A GUEST AT THE WRONG SIZE IS "NOT YET", NOT "NEVER".
//
// THIS IS THE ONE BEHAVIOUR `desktop:` HAD THAT ITS REPLACEMENT LOOPS DID NOT, and it is worth a test
// because losing it is expensive rather than noisy.
//
// MEASURED ON zorin-install, WHICH PASSED ITS GATE AND THEN PUBLISHED A 0 MiB DISK: the guest drew at
// 800x600 while the file clicks (1223, 586) and (1361, 764) - coordinates measured on 1920x1080 - so
// the whole installer walk pressed the wallpaper, nothing was written, and the stage "passed" its
// steps. The old gate waited two minutes for the switch and NAMED the size; a plain comparison against
// a 1920x1080 reference just fails on the dimensions.
//
// SO THE COMPARISON ERROR IS ITS OWN VALUE. `errFrameSize` means "the picture may yet become the right
// shape", and `doRepeat` re-asks; `check:` alone still reports it, because a single question has no
// next attempt.

import (
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/easydev-org/easyvm/internal/screen"
	"github.com/easydev-org/easyvm/internal/walk"
)

// writePNG puts a picture on disk, which every test that compares one needs.
func writePNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

// frameAt draws a two-band picture at the given size: a distinct top band, then a body.
func frameAt(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if y*1080/h < 120 { // the same fraction of the height as the reference's band
				img.Set(x, y, color.RGBA{0x20, 0x60, 0xa0, 0xff})
			} else {
				img.Set(x, y, color.RGBA{0x90, 0x30, 0x50, 0xff})
			}
		}
	}
	return img
}

func TestAFrameAtAnotherSizeIsASizeErrorNotAMismatch(t *testing.T) {
	dir := t.TempDir()
	refPath := filepath.Join(dir, "desktop.png")
	writePNG(t, refPath, frameAt(1920, 1080))

	d := &fakeDriver{}
	ex, _ := newExec(d)
	ex.Work = dir
	ex.File = &walk.File{Path: filepath.Join(dir, "install.walk")}

	_, err := ex.similarityInRect("wait for the desktop", refPath, frameAt(800, 600),
		screen.Rect{From: 0, To: 120, Pixels: true, AtW: 1920, AtH: 1080})
	if err == nil {
		t.Fatal("an 800x600 frame compared against a 1920x1080 reference returned no error. The " +
			"old `desktop:` gate waited for the mode switch; a comparison that silently scored it " +
			"would answer about a corner neither picture has")
	}
	if !errors.Is(err, errFrameSize) {
		t.Fatalf("the error is not errFrameSize, so `doRepeat` treats it as a verdict rather than "+
			"as \"ask again\": %v", err)
	}
	// AND IT SAYS THE SIZES IT COMPARED, which is the sentence that identified the zorin fault. It
	// reports the CROPPED sizes rather than the frame's, because the crop is what `Compare` saw -
	// "1x66 and 1x120" for a 120-row band, which is still enough to see that the guest is not at the
	// size the coordinates were measured at.
	if !strings.Contains(err.Error(), "different sizes") {
		t.Errorf("the error does not say the sizes differ: %v", err)
	}
}

// AND THE SAME PICTURE AT THE SAME SIZE IS A MEASUREMENT, not an error.
func TestAMatchingFrameIsScored(t *testing.T) {
	dir := t.TempDir()
	refPath := filepath.Join(dir, "desktop.png")
	writePNG(t, refPath, frameAt(1920, 1080))

	d := &fakeDriver{}
	ex, _ := newExec(d)
	ex.Work = dir
	ex.File = &walk.File{Path: filepath.Join(dir, "install.walk")}

	sim, err := ex.similarityInRect("wait for the desktop", refPath, frameAt(1920, 1080),
		screen.Rect{From: 0, To: 120, Pixels: true, AtW: 1920, AtH: 1080})
	if err != nil {
		t.Fatalf("an identical frame failed: %v", err)
	}
	if sim < 0.99 {
		t.Errorf("an identical frame scored %.4f, want ~1.0", sim)
	}
}

// A REFERENCE THAT DOES NOT EXIST IS A FAILURE, NOT A WAIT. Otherwise a typo in `reference:` would
// make a loop run to its ceiling and report "never saw a desktop".
func TestAMissingReferenceFailsImmediately(t *testing.T) {
	dir := t.TempDir()
	d := &fakeDriver{}
	ex, _ := newExec(d)
	ex.Work = dir
	ex.File = &walk.File{Path: filepath.Join(dir, "install.walk")}

	_, err := ex.similarityInRect("wait", filepath.Join(dir, "nope.png"), frameAt(1920, 1080),
		screen.Rect{From: 0, To: 120, Pixels: true, AtW: 1920, AtH: 1080})
	if err == nil {
		t.Fatal("a missing reference returned no error")
	}
	if errors.Is(err, errFrameSize) {
		t.Fatalf("a missing reference was classified as a size mismatch, so a loop would retry "+
			"until its ceiling instead of naming the file: %v", err)
	}
}
