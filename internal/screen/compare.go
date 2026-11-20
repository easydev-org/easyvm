package screen

// COMPARING A FRAME TO A REFERENCE IMAGE, WHICH IS A DIFFERENT QUESTION FROM `Analyse`.
//
// `Analyse` reduces a screen to seven numbers - mean, spread, saturation - and those numbers are what
// `desktop:` and `want:` decide on. They cannot say "this is the partition list": two panes with the
// same average brightness are the same seven numbers.
//
// A REFERENCE IMAGE CAN, AND THE NUMBERS ARE WIDE ENOUGH TO MAKE IT RELIABLE. Measured over four
// real recordings from this repository's own CI (ubuntu, windows, macos, freebsd), comparing frames
// the way this file does:
//
//	same image against itself                      0.00%  changed
//	one second apart, same screen                  0.53 - 0.90%
//	five seconds apart, same screen                0.63 - 0.88%
//	TWO DIFFERENT SCREENS                         98.33 - 99.98%
//
// THREE ORDERS OF MAGNITUDE BETWEEN "the same pane, a second later" and "another pane". That is why a
// threshold works at all: nothing here needs to be precise, it needs to sit in a gap that wide.
//
// THE THRESHOLD IS THE FILE'S. A default would be a number nobody chose, and the failure it produces
// - a pane that looks identical to a person and is refused - is not one a default can fix.

import (
	"fmt"
	"image"
	"image/color"
)

// Difference is what Compare found: the share of pixels that changed, and where they are.
type Difference struct {
	// Changed is the fraction of sampled pixels whose colour moved by more than the tolerance, 0..1.
	Changed float64
	// MeanDelta is the average per-channel difference over every pixel sampled, 0..255.
	MeanDelta float64
	// MaxDelta is the largest per-channel difference found, 0..255. IT IS REPORTED BECAUSE A SMALL
	// REGION CAN CHANGE COMPLETELY - a tick box filling in is 0.02% of a 1920x1080 screen and it is
	// the difference a person cares about - so a step that needs that assertion wants the region
	// mask rather than a lower threshold.
	MaxDelta int
	// Compared is how many pixels were sampled, so a caller can tell a real comparison from two
	// one-pixel images.
	Compared int
}

// CompareStats says how a frame differed from its reference.
func (d Difference) Explain() string {
	return fmt.Sprintf("%.2f%% of %d sampled pixel(s) changed (mean delta %.1f, max %d)",
		d.Changed*100, d.Compared, d.MeanDelta, d.MaxDelta)
}

// compareStride samples every Nth pixel along both axes.
//
// A FULL 1920x1080 COMPARISON IS 2,073,600 PIXELS PER CALL and a walkthrough makes dozens of them. A
// stride of four reads 129,600 of them, which is what the measurements above were taken with - the
// numbers are the same at stride 1 and stride 4 for these images, because the change is either
// thousands of pixels (a different pane) or a handful (a clock).
const compareStride = 4

// pixelDelta is the largest per-channel difference between two colours, 0..255.
//
// THE LARGEST RATHER THAN THE AVERAGE, because an average hides exactly the changes worth seeing: a
// red button on a grey background averaged with its surroundings is nearly grey.
func pixelDelta(a, b color.Color) int {
	ar, ag, ab, _ := a.RGBA()
	br, bg, bb, _ := b.RGBA()
	d := func(x, y uint32) int {
		v := int(x>>8) - int(y>>8)
		if v < 0 {
			return -v
		}
		return v
	}
	out := d(ar, br)
	if v := d(ag, bg); v > out {
		out = v
	}
	if v := d(ab, bb); v > out {
		out = v
	}
	return out
}

// Compare measures how a frame differs from a reference.
//
// `tolerance` is the per-channel delta below which a pixel counts as unchanged - 0 means every
// one-bit difference counts, and 8 is what the measurements above used: it absorbs the encoder's own
// rounding and the cursor's antialiasing without absorbing a colour change a person would see.
//
// A SIZE MISMATCH IS AN ERROR RATHER THAN A CROP. Two screens of different sizes are not two views of
// one screen, and silently comparing the overlapping corner would report "identical" for a reference
// of the wrong guest.
func Compare(frame, reference image.Image, tolerance int) (Difference, error) {
	fw, fh := frame.Bounds().Dx(), frame.Bounds().Dy()
	rw, rh := reference.Bounds().Dx(), reference.Bounds().Dy()
	if fw != rw || fh != rh {
		return Difference{}, fmt.Errorf("the frame is %dx%d and the reference is %dx%d. A reference "+
			"is a picture of ONE screen size; a guest that changed size needs a new reference rather "+
			"than a comparison of the corner they share", fw, fh, rw, rh)
	}
	if tolerance < 0 {
		tolerance = 0
	}

	var out Difference
	var sum int
	for y := frame.Bounds().Min.Y; y < frame.Bounds().Max.Y; y += compareStride {
		for x := frame.Bounds().Min.X; x < frame.Bounds().Max.X; x += compareStride {
			d := pixelDelta(frame.At(x, y), reference.At(x, y))
			sum += d
			if d > out.MaxDelta {
				out.MaxDelta = d
			}
			if d > tolerance {
				out.Changed++
			}
			out.Compared++
		}
	}
	if out.Compared == 0 {
		return Difference{}, fmt.Errorf("nothing was compared: the images have no pixels")
	}
	out.Changed /= float64(out.Compared)
	out.MeanDelta = float64(sum) / float64(out.Compared)
	return out, nil
}
