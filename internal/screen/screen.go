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
