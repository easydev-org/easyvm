package run

import (
	"image"
	"image/color"
	"testing"

	"github.com/easydev-org/easyvm/internal/screen"
)

// The property, asserted on the REAL function: a 1920x1080 rect must crop inside a 640x480 frame.
func TestReadWordsCropsInsideTheFrame(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 640, 480))
	for y := 0; y < 480; y++ {
		for x := 0; x < 640; x++ {
			img.Set(x, y, color.RGBA{20, 20, 20, 255})
		}
	}
	r := screen.Rect{
		XFrom: 760, XTo: 1400, From: 200, To: 560,
		Pixels: true, AtW: 1920, AtH: 1080,
	}
	// readWords shells out to tesseract for the crop; with no words it still returns, and the point
	// here is that Crop does not refuse the region.
	ex, _ := newExec(&fakeDriver{})
	if _, err := ex.readWords(img, r, 6); err != nil {
		t.Fatalf("readWords refused a rect that fits the frame: %v", err)
	}
}
