package qmp

// A TRUNCATED PNG IS AN ERROR, AND THE DECODER IS WHAT KNOWS.
//
// THIS TEST REPLACED THE PPM VERSION OF ITSELF, which was written when a frame was P6 and the size
// was arithmetic from the header: `header + width*height*3` is decided before the first pixel, so a
// file of that exact length was complete by the format's own rule.
//
// PNG HAS NO SUCH RULE. Its length is not predictable from the header - the whole point is that the
// data is compressed - so `waitForStable` decides by DECODING: `image.Decode` reads to the IEND chunk,
// and "it decoded" therefore means "the last chunk arrived". A truncated file fails that decode, which
// is the verdict the caller needs.
//
// AND IT IS A REAL RACE, NOT A HYPOTHETICAL: `screendump` replies before the write completes.

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{0x40, 0x60, 0x80, 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A COMPLETE PNG IS READ: its size comes from the decoder, and the caller never trusts a constant.
func TestAPNGIsReadAndItsSizeComesFromTheDecoder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "frame.png")
	if err := os.WriteFile(path, pngBytes(t, 1920, 1080), 0o644); err != nil {
		t.Fatal(err)
	}
	w, h, err := frameSize(path)
	if err != nil {
		t.Fatal(err)
	}
	if w != 1920 || h != 1080 {
		t.Fatalf("read %dx%d, want 1920x1080", w, h)
	}
	if err := waitForStable(path, 2*time.Second); err != nil {
		t.Fatalf("a complete PNG was not stable: %v", err)
	}
}

// A HALF-WRITTEN PNG MUST BE AN ERROR, not a frame with the wrong shape. This is the race the wait
// exists for: qemu replies to `screendump` before the IEND chunk is on disk.
func TestATruncatedPNGIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "frame.png")
	full := pngBytes(t, 400, 300)
	half := full[:len(full)/2]
	if err := os.WriteFile(path, half, 0o644); err != nil {
		t.Fatal(err)
	}
	// The size has stopped growing - the writer is gone - so no amount of waiting makes this decode.
	if err := waitForStable(path, 300*time.Millisecond); err == nil {
		t.Fatal("a truncated PNG was reported stable. The caller hands that path to `screen.Load`, " +
			"which fails with \"not enough pixel data\" - a real error at a moment when another " +
			"chunk may still be on its way")
	}
}

// AND SOMETHING THAT IS NOT AN IMAGE AT ALL IS REFUSED BY NAME.
//
// The PPM version of this test caught the mistake that produced
// `frames/frame-000007.png: not a binary PPM (starts "\x89PNG\r")` - the reader was still checking
// for a P6 header after the writer had moved to PNG. That class of bug is gone with the header check,
// so what is left to assert is that a bogus file does not decode into a frame.
func TestSomethingThatIsNotAPictureIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "frame.png")
	if err := os.WriteFile(path, []byte("this is not a picture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := frameSize(path); err == nil {
		t.Fatal("a text file was read as a frame")
	}
	if err := waitForStable(path, 300*time.Millisecond); err == nil {
		t.Fatal("a text file was reported stable")
	}
}
