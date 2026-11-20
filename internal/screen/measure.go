package screen

import (
	"fmt"
	"image"
	"sort"
)

// MEASUREMENT, NOT JUDGEMENT.
//
// THIS FILE EXISTS BECAUSE THE THRESHOLDS LIVED IN THE WRONG PLACE. `IsLinuxDesktop` computes
// `BrightnessDrop`, `BottomVar` and `BottomSat` - five numbers - and then IMMEDIATELY compares them
// against `MinBrightnessDrop`, `MinVarianceRatio` and `MinSaturationRise`. The comparison is the
// part that changes when a new medium appears, and it is the part that could not be moved without
// recompiling: mint's panel is the same colour as its wallpaper, so `margin > 40` was not merely
// wrong for it, it was UNSATISFIABLE, and the only way to say so was 80 new lines of Go and a new
// image.
//
// SO THE NUMBERS COME OUT, AND THE COMPARISON GOES INTO THE FILE. A `see:` step binds what this
// returns; a `want:` step decides. Neither needs a rebuild.
//
// THE ANSWER TO "SHOULD THIS BE A VERDICT" IS IN `VOCABULARY.md`, and the rule is: a measurement is
// code, a verdict is data. Everything here returns a number or a word list and decides nothing.
type Reading struct {
	// Mean is the mean brightness 0..255 over the rect.
	Mean float64
	// Spread is the mean absolute deviation of brightness inside the rect - what "flat" means. A
	// panel's border measures 0.0 to 2.1; a wallpaper measures 8 or more.
	Spread float64
	// Sat is the mean HSV saturation, 0..1. A launcher on a full-bleed gradient measures 96.9/100;
	// an installer window measures 4 to 6.
	Sat float64
	// FlatRows is how many rows inside the rect are one colour across its full width, within
	// tolerance. This is the number that sees a panel whose colour matches its wallpaper.
	FlatRows int
	// Rows is the rect's height in pixels, so a caller can say "3 of 41".
	Rows int
	// Min and Max are the darkest and brightest pixel in the rect, which a `want:` may compare when
	// a mean hides the thing it is looking for - a single white icon on a dark bar.
	Min, Max float64
}

// Rect is a region of a frame, expressed so that it CANNOT be measured at the wrong size.
//
// THE BUG THIS PREVENTS IS WRITTEN DOWN IN THE FILES: `click (936, 628) is a coordinate measured at
// 1920x1080 but the framebuffer is 640x480`. That happened because a coordinate carried no
// statement of the size it was measured at, so it was re-interpreted against whatever frame arrived.
//
// A Rect carries no such ambiguity: `Bands` are FRACTIONS of the frame, so the same Rect means the
// same place at 1280x720 and 1920x1080. `Pixels` is the escape hatch for a rect that must be exact -
// and it names its size, so a mismatch is an error rather than a silent reinterpretation.
type Rect struct {
	// Bands is [from, to] as fractions of the frame height, for a full-width horizontal band. The
	// form every panel test needs, and the reason `top 4.5%` and `bottom 4.5%` can be written.
	From, To float64
	// X0, X1 are fractions of the width. Zero values mean the full width.
	XFrom, XTo float64
	// Pixels says From/To/XFrom/XTo are PIXEL coordinates rather than fractions, and must be scaled
	// against the frame they are measured on. The older files carry pixel rects because that is what
	// a screen ruler gives; the newer form prefers fractions because they cannot be wrong at another
	// size. Keeping both in one struct means one place converts, rather than every reader.
	Pixels bool
	// AtW, AtH is THE SCREEN THE PIXELS WERE WRITTEN FOR - the file's `rect_at: [w, h]`.
	//
	// IT IS NOT THE FRAME'S SIZE, AND CONFLATING THE TWO WAS A REAL FAILURE: a macos recovery frame is
	// 640x480 and its rects were measured at 1920x1080, so scaling them by the FRAME asked for a
	// 1400-wide crop of a 640-wide image -
	//
	//	crop [760, 200, 1400, 560] is not inside a 640x480 frame
	//
	// Zero means the file named no size, and the frame is the only answer available.
	AtW, AtH int
}

// FullWidth is the whole frame's width, which is what a panel test measures.
func bandRect(from, to float64) Rect { return Rect{From: from, To: to, XTo: 1} }

// Measure returns the numbers a decision could be made from, and decides nothing itself.
func Measure(img image.Image, r Rect) Reading {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	read := Reading{}
	if w == 0 || h == 0 {
		return read
	}
	// A PIXEL RECT IS SCALED AGAINST THIS FRAME, which is the conversion `Pixels` exists for: the
	// same struct carries both, and this is the one place that knows the frame's size.
	from, to, xf, xt := r.From, r.To, r.XFrom, r.XTo
	if r.Pixels {
		aw, ah := r.AtW, r.AtH
		if aw <= 0 || ah <= 0 {
			aw, ah = w, h
		}
		from, to = r.From/float64(ah), r.To/float64(ah)
		xf, xt = r.XFrom/float64(aw), r.XTo/float64(aw)
	}
	y0 := b.Min.Y + int(float64(h)*from)
	y1 := b.Min.Y + int(float64(h)*to)
	if y1 <= y0 {
		y1 = y0 + 1
	}
	if y1 > b.Max.Y {
		y1 = b.Max.Y
	}
	xFrom, xTo := xf, xt
	if xTo <= xFrom {
		xFrom, xTo = 0, 1
	}
	x0 := b.Min.X + int(float64(w)*xFrom)
	x1 := b.Min.X + int(float64(w)*xTo)
	if x1 <= x0 {
		x1 = x0 + 1
	}
	if x1 > b.Max.X {
		x1 = b.Max.X
	}
	read.Rows = y1 - y0

	var sumB, sumSat, count float64
	minV, maxV := 255.0, 0.0
	values := make([]float64, 0, (y1-y0)*((x1-x0)/4+1))
	for y := y0; y < y1; y++ {
		row := make([]float64, 0, (x1-x0)/4+1)
		for x := x0; x < x1; x += 4 {
			rr, gg, bb := rgb8(img.At(x, y))
			c := [3]float64{float64(rr), float64(gg), float64(bb)}
			v := sum(c) / 3
			row = append(row, v)
			values = append(values, v)
			if v < minV {
				minV = v
			}
			if v > maxV {
				maxV = v
			}
			sumB += v
			sumSat += saturation(c)
			count++
		}
		// A FLAT ROW IS ONE COLOUR ALONG ITS WHOLE WIDTH, which is what a panel's border is and what
		// a wallpaper never is.
		if len(row) > 0 {
			rm := meanOf(row)
			d := 0.0
			for _, v := range row {
				if v > rm {
					d += v - rm
				} else {
					d += rm - v
				}
			}
			if d/float64(len(row)) < 3 {
				read.FlatRows++
			}
		}
	}
	if count == 0 {
		return read
	}
	read.Mean = sumB / count
	read.Sat = sumSat / count
	read.Spread = meanDeviation(values, read.Mean)
	read.Min, read.Max = minV, maxV
	return read
}

// meanOf is a mean, kept here so `Measure` reads top to bottom.
func meanOf(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	var t float64
	for _, x := range v {
		t += x
	}
	return t / float64(len(v))
}

// meanDeviation is the mean absolute deviation - the "flatness" number.
func meanDeviation(v []float64, mean float64) float64 {
	if len(v) == 0 {
		return 0
	}
	var d float64
	for _, x := range v {
		if x > mean {
			d += x - mean
		} else {
			d += mean - x
		}
	}
	return d / float64(len(v))
}

// Median returns the middle brightness, which a caller may prefer to the mean when a small icon
// drags an average. Kept beside the other measurements rather than in a caller.
func Median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	c := append([]float64(nil), v...)
	sort.Float64s(c)
	return c[len(c)/2]
}

// Explain renders a reading the way a log line should, so a FAILURE says which number was wrong.
func (r Reading) Explain() string {
	return fmt.Sprintf("mean=%.1f spread=%.1f sat=%.3f flat_rows=%d/%d",
		r.Mean, r.Spread, r.Sat, r.FlatRows, r.Rows)
}
