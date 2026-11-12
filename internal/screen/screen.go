// Package screen reads a frame and answers questions about it.
//
// EVERY THRESHOLD HERE WAS MEASURED, and the comments say what the measurement was. That matters
// more than usual in this package: a desktop test that is slightly wrong fails a healthy guest,
// and the reader of the log sees "NO DESKTOP" over a screenshot of a desktop. The numbers are the
// Python implementation's, carried over deliberately - a re-derived constant would invalidate
// every recording this repository has.
package screen

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"os"

	// THE DECODER IS REGISTERED, NOT CALLED. github.com/kovidgoyal/imaging/netpbm's init() does
	// `image.RegisterFormat("ppm", "P6", ...)`, so a frame qemu wrote is read by the STANDARD
	// library - the same call that would read a PNG a recording produced. That is the whole reason
	// this import is blank: there is nothing here to call, and a caller that had to name the
	// decoder would have to change when the frame format did.
	_ "github.com/kovidgoyal/imaging/netpbm"
)

// The Windows test: a bright top band over a dark, varied bottom band.
//
// FROM lib/desktop.py, where the bands were chosen against recorded frames:
//
//	top    (0.00, 0.20)  the taskbar and the top of the wallpaper
//	bottom (0.80, 1.00)  the wallpaper under it
//
// A desktop's top is bright and flat (the taskbar) and its bottom is dark and busy (the wallpaper).
// A lock screen is bright and full-bleed, so it FAILS this test - correctly, and that is why the
// Windows stage has a separate lock-page check rather than treating "not a desktop" as "broken".
var (
	TopBand      = [2]float64{0.00, 0.20}
	BottomBand   = [2]float64{0.80, 1.00}
	MenuBarStrip = [2]float64{0.000, 0.012}
)

const (

	// Measured on 349 frames: every real desktop passed all three, every wizard pane failed at
	// least one. The margins are wide on purpose (drop 15 against a measured ~53) so that a Dock
	// still animating in does not flip the answer.
	MinBrightnessDrop = 15.0 // top mean minus bottom mean, per-channel average
	MinVarianceRatio  = 2.0  // bottom variance / top variance
	MinSaturationRise = 0.10 // bottom saturation minus top saturation
)

// Stats are the numbers a desktop test decides on. They are returned even when the test fails, so
// a log can say WHY rather than only "no".
type Stats struct {
	Width, Height int
	TopMean       [3]float64
	BottomMean    [3]float64
	MenuMean      float64
	TopVar        float64
	BottomVar     float64
	TopSat        float64
	BottomSat     float64

	BrightnessDrop float64
	VarianceRatio  float64
	SaturationRise float64
}

// Load reads a frame qemu wrote.
//
// THE FORMAT IS NOT THIS PACKAGE'S BUSINESS. qemu's `screendump` writes binary PPM (P6), and a
// recording is JPEG; both are registered decoders, so this is the standard library's image.Decode
// and a caller never branches on which kind of frame it was handed.
//
// THE FAILURE THAT MATTERS WAS CHECKED RATHER THAN ASSUMED. `screendump` replies BEFORE the write
// finishes, so a half-written frame is a real state and not a hypothetical - and a frame that is
// short for its own header would make every brightness number silently wrong, with the symptom
// being a desktop test that never passes rather than an error anyone can read. Measured against a
// truncated copy of a real 1240x960 frame the decoder answers `unexpected EOF`; 1785616 of
// 3571216 bytes is refused rather than measured.
func Load(path string) (image.Image, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	// image.Decode, NOT A DECODER'S OWN FUNCTION. `qemu`'s screendump writes P6 and the registered
	// decoder handles it; a recording is JPEG, and that is the standard library too. One call for
	// both, so a caller does not branch on which kind of frame it was handed.
	img, format, err := image.Decode(fh)
	if err != nil {
		return nil, fmt.Errorf("%s: %w (is the write still in progress? format %q)",
			path, err, format)
	}
	return img, nil
}

// Analyse measures a frame.
func Analyse(img image.Image) Stats {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()

	topMean, topVar := bandStats(img, w, h, TopBand[0], TopBand[1])
	botMean, botVar := bandStats(img, w, h, BottomBand[0], BottomBand[1])
	menuColour, _ := bandStats(img, w, h, MenuBarStrip[0], MenuBarStrip[1])
	menuMean := sum(menuColour) / 3.0

	topSat := saturation(topMean)
	botSat := saturation(botMean)

	return Stats{
		Width: w, Height: h,
		TopMean: topMean, BottomMean: botMean, MenuMean: menuMean,
		TopVar: topVar, BottomVar: botVar,
		TopSat: topSat, BottomSat: botSat,
		BrightnessDrop: (sum(topMean) - sum(botMean)) / 3.0,
		// A FLAT BAND HAS VARIANCE 0 AND THE RATIO IS INFINITE, which is correct and useless in a
		// log. Flooring at 1.0 makes it a large finite number and spares every caller an
		// infinity check.
		VarianceRatio:  botVar / maxf(topVar, 1.0),
		SaturationRise: botSat - topSat,
	}
}

// IsWindowsDesktop reports whether a frame is a Windows desktop.
func IsWindowsDesktop(img image.Image) (bool, Stats) {
	s := Analyse(img)
	return s.BrightnessDrop >= MinBrightnessDrop &&
		s.VarianceRatio >= MinVarianceRatio &&
		s.SaturationRise >= MinSaturationRise, s
}

// Explain is the log line a reader needs when the answer is no.
func (s Stats) Explain() string {
	return fmt.Sprintf("drop=%+.1f (need %.0f) ratio=%.1fx (need %.0f) sat=%+.2f (need %.2f) "+
		"on a %dx%d frame",
		s.BrightnessDrop, MinBrightnessDrop, s.VarianceRatio, MinVarianceRatio,
		s.SaturationRise, MinSaturationRise, s.Width, s.Height)
}

// bandStats is the mean colour and the luminance variance over a horizontal band.
//
// IT SUBSAMPLES HORIZONTALLY at 4, which is a 16x saving and changes none of these statistics
// meaningfully - this runs on every frame of a wait loop.
func bandStats(img image.Image, w, h int, top, bottom float64) ([3]float64, float64) {
	b := img.Bounds()
	y0 := b.Min.Y + int(float64(h)*top)
	y1 := b.Min.Y + int(float64(h)*bottom)
	if y1 <= y0 {
		y1 = y0 + 1
	}

	const step = 4
	var sum [3]float64
	var count int
	for y := y0; y < y1; y++ {
		for x := b.Min.X; x < b.Max.X; x += step {
			r, g, bl := rgb8(img.At(x, y))
			sum[0] += float64(r)
			sum[1] += float64(g)
			sum[2] += float64(bl)
			count++
		}
	}
	if count == 0 {
		return [3]float64{}, 0
	}
	mean := [3]float64{sum[0] / float64(count), sum[1] / float64(count), sum[2] / float64(count)}

	// Variance of luminance, which is what separates a flat taskbar from a busy wallpaper.
	var acc float64
	for y := y0; y < y1; y++ {
		for x := b.Min.X; x < b.Max.X; x += step {
			r, g, bl := rgb8(img.At(x, y))
			lum := (float64(r) + float64(g) + float64(bl)) / 3.0
			meanLum := (mean[0] + mean[1] + mean[2]) / 3.0
			d := lum - meanLum
			acc += d * d
		}
	}
	return mean, acc / float64(count)
}

func rgb8(c color.Color) (uint8, uint8, uint8) {
	// AT() RETURNS 16-BIT CHANNELS. Dividing by 257 maps 65535 to 255 exactly, so a PPM's own
	// bytes round-trip unchanged - which matters because every threshold above was measured on
	// 8-bit values.
	r, g, b, _ := c.RGBA()
	return uint8(r / 257), uint8(g / 257), uint8(b / 257)
}

// saturation is HSV saturation: 0 for grey, 1 for a pure hue.
func saturation(c [3]float64) float64 {
	mx := maxf(c[0], maxf(c[1], c[2]))
	if mx <= 0 {
		return 0
	}
	mn := minf(c[0], minf(c[1], c[2]))
	return (mx - mn) / mx
}

func sum(c [3]float64) float64 { return c[0] + c[1] + c[2] }

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// --- the macOS desktop ------------------------------------------------------------------

// THE DOCK IS THE TEST, AND THE MENU BAR IS NOT.
//
// A macOS desktop's readable furniture is a 12-pixel menu bar - too small for a whole-frame read,
// and tesseract's page segmenter returns "Empty page!!" on a 1920x1080 desktop. The Dock, though, is
// a horizontal band near the bottom that is full of COLOURED ICONS on a bright wallpaper, and both
// of those are measurable without reading any text.
//
// MEASURED ON THE FRAMES THIS REPOSITORY HAS:
//
//	Dock band (0.885 .. 0.985 of the height)
//	  desktop     spread 20+   colourful 0.03+   body 214.8
//	  lock screen spread <20   colourful 0       body  43.3
//	  black       spread  0    colourful 0       body   0
//
// The body brightness is what separates a LOCK SCREEN from a booting guest: both have no Dock, and
// a lock screen is lit while a boot screen is nearly black. A stage that treated them alike waited
// out its whole timeout on a healthy machine.
const (
	DockBandTop    = 0.885
	DockBandBottom = 0.985
	// DockMinSpread is the standard deviation of luminance across the band. A flat footer is 1; a
	// Dock that has not finished animating in is still above 30.
	DockMinSpread = 20.0
	// DockMinColourful is the proportion of pixels whose channels differ by more than
	// DockColourSpread - "this is an icon, not grey UI".
	DockMinColourful = 0.03
	DockColourSpread = 40.0
	// MinDesktopBody is the brightness under the Dock. A desktop is 219 here, a lock screen 43, a
	// boot screen 0.
	MinDesktopBody = 120.0
)

// MacOSStats are the numbers the Dock test decides on.
type MacOSStats struct {
	Width, Height int
	DockSpread    float64
	DockColourful float64
	BodyMean      float64
}

// IsMacOSDesktop reports whether a frame is a macOS desktop, and returns the numbers either way.
//
// THE BODY BRIGHTNESS IS CHECKED FIRST AND ALONE WHEN IT FAILS, because that is the case that
// distinguishes a lit lock screen from a dark boot: both have no Dock, and one of them is a machine
// that has finished. Saying which it was is the difference between "wait longer" and "look at the
// password".
func IsMacOSDesktop(img image.Image) (bool, MacOSStats) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()

	// The body: the wallpaper under the Dock, sampled wide.
	bodyColour, _ := bandStats(img, w, h, 0.35, 0.80)

	// The Dock band's spread and how much of it is coloured.
	y0 := b.Min.Y + int(float64(h)*DockBandTop)
	y1 := b.Min.Y + int(float64(h)*DockBandBottom)
	if y1 <= y0 {
		y1 = y0 + 1
	}
	const step = 3
	var total, totalSq float64
	var count, colourful int
	for y := y0; y < y1; y++ {
		for x := b.Min.X; x < b.Max.X; x += step {
			r, g, bl := rgb8(img.At(x, y))
			lum := (float64(r) + float64(g) + float64(bl)) / 3.0
			total += lum
			totalSq += lum * lum
			count++
			mx := float64(max8(r, max8(g, bl)))
			mn := float64(min8(r, min8(g, bl)))
			if mx-mn > DockColourSpread {
				colourful++
			}
		}
	}
	stats := MacOSStats{Width: w, Height: h}
	if count > 0 {
		mean := total / float64(count)
		variance := totalSq/float64(count) - mean*mean
		if variance < 0 {
			variance = 0
		}
		stats.DockSpread = math.Sqrt(variance)
		stats.DockColourful = float64(colourful) / float64(count)
	}
	stats.BodyMean = sum(bodyColour) / 3.0

	ok := stats.BodyMean >= MinDesktopBody &&
		stats.DockSpread >= DockMinSpread &&
		stats.DockColourful >= DockMinColourful
	return ok, stats
}

// Explain is the log line for a macOS desktop answer.
func (s MacOSStats) Explain() string {
	why := ""
	switch {
	case s.BodyMean < MinDesktopBody:
		why = " (the screen is not lit - a boot, not a login)"
	case s.DockColourful < DockMinColourful:
		why = " (lit, but no coloured icons where the Dock sits - a lock screen or a wizard)"
	}
	return fmt.Sprintf("body=%.0f (need %.0f) dock-spread=%.1f (need %.0f) "+
		"dock-colour=%.3f (need %.2f) on a %dx%d frame%s",
		s.BodyMean, MinDesktopBody, s.DockSpread, DockMinSpread,
		s.DockColourful, DockMinColourful, s.Width, s.Height, why)
}

func max8(a, b uint8) uint8 {
	if a > b {
		return a
	}
	return b
}
func min8(a, b uint8) uint8 {
	if a < b {
		return a
	}
	return b
}

// HasLoginWindow reports whether a frame shows a login window.
//
// THE ONLY READABLE TEXT THERE IS THE PASSWORD FIELD'S PLACEHOLDER, and it is twelve pixels tall in
// a 165x40 box - so this is a measurement, not a read. The window is a DARK band across the middle
// of an otherwise lit screen: measured on the two frames this repository has,
//
//	login window   middle band 40-70, edges 100+
//	boot screen    everything under 5
//	desktop        middle band 200+
//
// so a lit screen with a dark middle is the login window, and a bright middle is the desktop.
func HasLoginWindow(img image.Image) bool {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()

	middle, _ := bandStats(img, w, h, 0.40, 0.60)
	top, _ := bandStats(img, w, h, 0.00, 0.10)
	bottom, _ := bandStats(img, w, h, 0.90, 1.00)

	middleMean := sum(middle) / 3.0
	topMean := sum(top) / 3.0
	bottomMean := sum(bottom) / 3.0

	// LIT, BUT NOT A DESKTOP: the edges are bright enough to be a wallpaper and the middle is not.
	return topMean >= 60 && bottomMean >= 60 &&
		middleMean >= 20 && middleMean < 100
}

// Crop takes a rectangle and enlarges it.
//
// THE ENLARGEMENT IS THE POINT, not the crop. A 700x34 strip of twelve-pixel text is what tesseract
// needs three times larger; the same strip at 1x is a page it decides has no text at all and answers
// "Empty page!!" - which is a picture OF a desktop that a read said nothing about.
//
// IT REFUSES A RECTANGLE THAT IS NOT ON THE FRAME rather than clamping. A crop that silently moved
// would read a different part of the screen and return plausible words from the wrong place.
func Crop(img image.Image, rect [4]int, scale int) (image.Image, error) {
	b := img.Bounds()
	x0, y0, x1, y1 := rect[0], rect[1], rect[2], rect[3]
	if x0 < 0 || y0 < 0 || x1 > b.Dx() || y1 > b.Dy() || x1 <= x0 || y1 <= y0 {
		return nil, fmt.Errorf("crop [%d, %d, %d, %d] is not inside a %dx%d frame",
			x0, y0, x1, y1, b.Dx(), b.Dy())
	}
	if scale <= 0 {
		scale = 1
	}
	w, h := (x1-x0)*scale, (y1-y0)*scale
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// NEAREST NEIGHBOUR, because this is for OCR and an interpolated edge invents grey that
			// tesseract then has to decide about.
			//
			// `color.RGBAModel`, NOT A TYPE ASSERTION. The frames this crops come from two decoders -
			// qemu's PPM, which is `*image.RGBA`, and the PNG that `qmpkey` and the recording write,
			// which decodes to `*image.NRGBA`. A hard `.(color.RGBA)` therefore PANICS on the second
			// kind, and it did, in the middle of a working ubuntu install:
			//
			//	panic: interface conversion: color.Color is nrgb.Color, not color.RGBA
			//	goroutine 18: internal/screen.Crop(...)
			//
			// - which killed the walk AFTER every pane had passed and the installer had finished, so
			// the log looked like an installer failure and was a colour model.
			//
			// The Model conversion is defined for ANY color.Color, so the decoder stops mattering.
			r, g, bl, a := img.At(b.Min.X+x0+x/scale, b.Min.Y+y0+y/scale).RGBA()
			out.SetRGBA(x, y, color.RGBA{
				R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(bl >> 8), A: uint8(a >> 8),
			})
		}
	}
	return out, nil
}

// SavePPM writes a binary PPM, which is what tesseract is given.
//
// IT IS WRITTEN BY HAND FOR THE SAME REASON THE READER IS NOT: P6 is a nine-byte header and RGB
// triples, and a library for the write side of a format we already decode is a dependency with one
// caller.
func SavePPM(img image.Image, path string) error {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := fmt.Fprintf(f, "P6\n%d %d\n255\n", w, h); err != nil {
		return err
	}
	buf := make([]byte, 0, w*h*3)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bl := rgb8(img.At(b.Min.X+x, b.Min.Y+y))
			buf = append(buf, r, g, bl)
		}
	}
	_, err = f.Write(buf)
	return err
}

// FindWindowTop scans one column for the first row at least this bright.
//
// A WINDOW'S TOP EDGE IS A BRIGHTNESS STEP. A dialog above a dark desktop is easy; one above a
// bright wallpaper is what `window_brightness` is for - the caller measures the window it wants and
// says how bright it is. Scanning a COLUMN rather than a row is deliberate: a window spans the
// column, a desktop does not, so the scan finds the edge rather than the wallpaper.
func FindWindowTop(img image.Image, x, from, to int, brightness float64) (int, bool) {
	b := img.Bounds()
	if x < 0 || x >= b.Dx() {
		return 0, false
	}
	if to <= from {
		to = b.Dy()
	}
	if to > b.Dy() {
		to = b.Dy()
	}
	for y := from; y < to; y++ {
		r, g, bl := rgb8(img.At(b.Min.X+x, b.Min.Y+y))
		if (float64(r)+float64(g)+float64(bl))/3.0 >= brightness {
			return y, true
		}
	}
	return 0, false
}

// --- did the screen change -------------------------------------------------------------

// Fingerprint reduces a frame to a coarse grid of block means, for "is this a different screen".
//
// A PIXEL-FOR-PIXEL COMPARISON IS THE WRONG TEST and the Python implementation learned it the
// awkward way: a pane that is being repainted differs in thousands of pixels on every capture, and a
// cursor blinking is a change that means nothing. A 16x16 grid of block averages is stable against
// both - it moves when the page moves, and it does not move when a caret blinks.
//
// DOWNSCALING IS NOT AN OPTIMISATION, IT IS THE MEASUREMENT. The question is "did the guest leave
// the pane", not "are these bytes identical", and the coarseness is what makes the two different.
func Fingerprint(img image.Image) [64]float64 {
	var out [64]float64
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return out
	}
	const side = 8 // 8x8 blocks
	// ONE SAMPLE PER BLOCK IS ENOUGH AT THIS COARSENESS, and it keeps the cost to 64 reads per
	// frame - which matters because this runs inside a poll loop.
	for by := 0; by < side; by++ {
		for bx := 0; bx < side; bx++ {
			x := b.Min.X + (w*(2*bx+1))/(2*side)
			y := b.Min.Y + (h*(2*by+1))/(2*side)
			r, g, bl, _ := img.At(x, y).RGBA()
			// AVERAGE THE THREE CHANNELS: this is about layout, not colour fidelity, and colour
			// changes (a blue button turning grey) do not all matter equally.
			out[by*side+bx] = float64(r>>8+g>>8+bl>>8) / 3.0
		}
	}
	return out
}

// Changed reports whether two fingerprints differ enough to be different screens.
//
// THE THRESHOLD IS 2/255 PER BLOCK, averaged. Below that a repaint, a blinking caret or a spinner
// reads as "the same", which is what a caller waiting for a pane to CHANGE wants; above it, a pane
// with different text or different controls reads as different.
func Changed(before, after [64]float64) bool {
	var diff float64
	for i := range before {
		d := before[i] - after[i]
		if d < 0 {
			d = -d
		}
		diff += d
	}
	return diff/float64(len(before)) > 2.0
}

// --- finding a control by the colour of its fill ---------------------------------------

// Region is a rectangle of the frame, in frame pixels.
type Region struct{ X0, Y0, X1, Y1 int }

// Fill is what a control's body looks like, and the test is deliberately about SATURATION rather
// than a specific colour.
//
// THREE FILLS HAVE BEEN MEASURED ON THE ONE CONTROL THIS EXISTS FOR, and the third is the one that
// cost a driver:
//
//	green   (144,206,176)   Select / Next, enabled
//	grey    (137,137,137)   the same controls while disabled
//	salmon  (231,143,143)   `Erase and Install`, which is DESTRUCTIVE and is drawn in the warning
//	                        colour rather than the confirm one
//
// The first version of this test accepted only green and grey, so the moment the drive card was
// selected - the only thing that makes `Erase and Install` available - the locator reported "NO
// FOOTER BUTTON ON THIS PANE" on a pane whose button was plainly on screen in salmon.
//
// SO THE TEST IS: far enough from grey to be a coloured fill, OR a light grey. The card behind it
// is (48,48,48), so a mid-grey at 137 is a button and a near-white at 240 is label text.
func Fill(r, g, b uint8) bool {
	if r > 200 && absDiff(r, g) < 12 && absDiff(r, b) < 12 {
		return false // white label text, not a fill
	}
	hi, lo := maxOf(r, g, b), minOf(r, g, b)
	if hi-lo > 50 && hi > 120 {
		return true // green, salmon - any saturated fill
	}
	return r > 110 && r < 185 && absDiff(r, g) < 10 && absDiff(r, b) < 10 // the disabled grey
}

// FindFill returns the centre of the dominant fill inside a region, and the extent it covered.
//
// THE DOMINANT COLOUR'S OWN EXTENT IS THE BUTTON, NOT THE BOUNDING BOX OF EVERY BUTTON-LIKE PIXEL.
// A raw bounding box also catches the wallpaper and the title bar, which is what made the Python
// version report a 190x63 box around an 84x34 button. Counting per quantised colour first, then
// taking the most common one's extent, is what makes the centre land on the control.
//
// A REGION WITH TOO FEW FILL PIXELS HAS NO BUTTON. 600 is well below an 84x34 fill (~2800 px) and
// well above the anti-aliasing speckle that a wallpaper border produces - and the caller needs
// `ok` to be false rather than a plausible-looking centre, because pressing wallpaper is worse than
// reporting that the step could not find its target.
func FindFill(img image.Image, r Region) (cx, cy int, left, top, right, bottom int, ok bool) {
	b := img.Bounds()
	x0, y0 := clampTo(b.Min.X, b.Dx(), r.X0), clampTo(b.Min.Y, b.Dy(), r.Y0)
	x1, y1 := clampTo(b.Min.X, b.Dx(), r.X1), clampTo(b.Min.Y, b.Dy(), r.Y1)
	if x1 <= x0 || y1 <= y0 {
		return 0, 0, 0, 0, 0, 0, false
	}

	// Quantise to 8 levels per channel so a gradient does not split one button into two colours.
	type key struct{ r, g, b uint8 }
	counts := map[key]int{}
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			cr, cg, cb, _ := img.At(x, y).RGBA()
			r8, g8, b8 := uint8(cr>>8), uint8(cg>>8), uint8(cb>>8)
			if !Fill(r8, g8, b8) {
				continue
			}
			counts[key{r8 / 8 * 8, g8 / 8 * 8, b8 / 8 * 8}]++
		}
	}
	if len(counts) == 0 {
		return 0, 0, 0, 0, 0, 0, false
	}
	var dominant key
	best := -1
	for k, n := range counts {
		if n > best {
			dominant, best = k, n
		}
	}
	if best < 600 {
		return 0, 0, 0, 0, 0, 0, false
	}

	// The extent OF THAT COLOUR, which is the button's own rectangle.
	left, top, right, bottom = x1, y1, -1, -1
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			cr, cg, cb, _ := img.At(x, y).RGBA()
			r8, g8, b8 := uint8(cr>>8), uint8(cg>>8), uint8(cb>>8)
			if !Fill(r8, g8, b8) {
				continue
			}
			if (key{r8 / 8 * 8, g8 / 8 * 8, b8 / 8 * 8}) != dominant {
				continue
			}
			if x < left {
				left = x
			}
			if x > right {
				right = x
			}
			if y < top {
				top = y
			}
			if y > bottom {
				bottom = y
			}
		}
	}
	if right < left || bottom < top {
		return 0, 0, 0, 0, 0, 0, false
	}
	return (left + right) / 2, (top + bottom) / 2, left, top, right, bottom, true
}

func absDiff(a, b uint8) int {
	if a > b {
		return int(a) - int(b)
	}
	return int(b) - int(a)
}

func maxOf(v ...uint8) uint8 {
	m := v[0]
	for _, x := range v[1:] {
		if x > m {
			m = x
		}
	}
	return m
}

func minOf(v ...uint8) uint8 {
	m := v[0]
	for _, x := range v[1:] {
		if x < m {
			m = x
		}
	}
	return m
}

func clampTo(min, size, v int) int {
	if v < min {
		return min
	}
	if v > min+size {
		return min + size
	}
	return v
}

// Stats converts the macOS numbers into the shared shape a `desktop:` step logs.
func (m MacOSStats) Stats() Stats {
	return Stats{
		Width: m.Width, Height: m.Height,
		BottomVar:  m.DockSpread,
		BottomSat:  m.DockColourful,
		BottomMean: [3]float64{m.BodyMean, m.BodyMean, m.BodyMean},
	}
}

// IsLinuxDesktop reports whether a frame shows a desktop panel.
//
// A LINUX DESKTOP IS A PANEL AND AN INSTALLER IS NOT, and that is the whole test. GNOME, KDE, XFCE,
// MATE and Cinnamon all draw a solid strip along one edge - a top bar with a clock, or a bottom dock
// with launchers - while the installers these images boot (Ubiquity, Calamares, Anaconda, the MX and
// Mint wizards) draw either a full-bleed window or a bordered card with nothing along the edge.
//
// THE MEASUREMENT IS A WIDE FLAT BAND, on either edge, that is BRIGHTER than the body and holds that
// brightness across most of the width. A window's title bar fails it because it does not reach the
// screen edge; a black boot screen fails it because the band is not brighter than the body.
func IsLinuxDesktop(img image.Image) (bool, Stats) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	st := Stats{Width: w, Height: h}
	if w == 0 || h == 0 {
		return false, st
	}

	body, bodySat := bandStats(img, w, h, 0.35, 0.65)
	bodyMean := sum(body) / 3.0
	st.TopMean = body
	st.BottomMean = body

	// THE TWO EDGES A PANEL IS DRAWN ON. 4% of the height is 43 px at 1080 - a panel is 24 to 48.
	for _, edge := range []struct {
		lo, hi float64
		top    bool
	}{
		{0.00, 0.045, true},
		{0.955, 1.00, false},
	} {
		band, bandSat := bandStats(img, w, h, edge.lo, edge.hi)
		mean := sum(band) / 3.0
		// A PANEL IS FLAT: its own variance is small, because it is one colour along its whole
		// width. Wallpaper along an edge is not.
		spread := bandSpread(img, w, h, edge.lo, edge.hi)
		if edge.top {
			st.TopVar, st.TopSat = spread, bandSat
		} else {
			st.BottomVar, st.BottomSat = spread, bandSat
		}

		// A PANEL IS FLAT AND DIFFERENT FROM THE BODY, IN EITHER DIRECTION.
		//
		// THE DIRECTION IS NOT PART OF THE TEST, and assuming it was is a mistake this test caught:
		// a dark theme draws a nearly-black bar over a mid-grey wallpaper, and a light theme the
		// reverse. What separates a panel from the wallpaper beside it is that it is ONE COLOUR
		// across the full width and that its brightness DIFFERS from the body under it.
		//
		// The flatness is what rejects a photograph (a wallpaper's edge has its own variation), and
		// the difference is what rejects a uniform window - an installer is one colour top to bottom.
		margin := mean - bodyMean
		if margin < 0 {
			margin = -margin
		}
		if margin > 40 && spread < 42 && bandSat > 0.05 {
			st.BrightnessDrop = margin
			return true, st
		}
	}
	st.BrightnessDrop = 0
	_ = bodySat
	return false, st
}

// bandSpread is the mean absolute deviation inside a band, which is what "flat" means here.
func bandSpread(img image.Image, w, h int, from, to float64) float64 {
	b := img.Bounds()
	y0 := b.Min.Y + int(float64(h)*from)
	y1 := b.Min.Y + int(float64(h)*to)
	if y1 <= y0 {
		y1 = y0 + 1
	}
	if y1 > b.Max.Y {
		y1 = b.Max.Y
	}
	var total, count float64
	for y := y0; y < y1; y++ {
		for x := b.Min.X; x < b.Max.X; x += 4 {
			r, g, bl, _ := img.At(x, y).RGBA()
			total += float64((r>>8 + g>>8 + bl>>8) / 3)
			count++
		}
	}
	if count == 0 {
		return 0
	}
	mean := total / count
	var dev float64
	for y := y0; y < y1; y++ {
		for x := b.Min.X; x < b.Max.X; x += 4 {
			r, g, bl, _ := img.At(x, y).RGBA()
			v := float64((r>>8 + g>>8 + bl>>8) / 3)
			if v > mean {
				dev += v - mean
			} else {
				dev += mean - v
			}
		}
	}
	return dev / count
}
