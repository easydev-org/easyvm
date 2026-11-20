package run

// `see:` CAPTURES ONE FRAME AND NAMES IT. `check:` ASKS EVERY QUESTION ABOUT THAT FRAME.
//
// THIS FILE USED TO HOLD THE OTHER MODEL, AND IT WAS THE ONE THING THAT MADE A SCREEN INTO A SINGLE
// QUESTION. A `see:` carried ONE `rect:` and ONE `of:`, so a file that wanted to know three things
// about one screen - "is it the reference image", "does the bottom band say this", "is the mean
// brightness below sixty" - had to capture THREE TIMES, and each capture was a different frame taken
// at a different moment. Two of those questions could disagree about what the screen said.
//
//	see:   {name: shot}                        capture once, name it
//	check: {name: a, from: shot, mode: like, reference: ...}
//	check: {name: b, from: shot, mode: text, rect: middle, words: [[install]]}
//	check: {name: c, from: shot, mode: mean_between, rect: bottom 4.5%, range: [0, 60]}
//
// ONE CAPTURE, THREE QUESTIONS, EACH WITH ITS OWN REGION, ALL ABOUT THE SAME INSTANT.
//
// AND `check:` STILL DOES NOT READ THE SCREEN. It reads the frame the `see:` captured, which is why a
// `loop:` can re-ask the same boolean on every attempt without paying for a picture each time.
//
// THE OLD FORM IS GONE, NOT DEPRECATED. `see: {name, rect, of}` and `ocr:` were migrated: 83 of the
// 83 `of: [...]` in this tree were `of: [text]`, which is `check: {mode: text}`, and the two numeric
// `measuring` lines are `check: {mode: mean_between}`. A vocabulary with two ways to say one thing is
// how a reader comes to write the one that is not maintained.

import (
	"fmt"
	"image"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/easydev-org/easyvm/internal/screen"
	"github.com/easydev-org/easyvm/internal/walk"
)

// doSee captures one frame and binds it under the step's name.
//
// THE FRAME ITSELF IS THE VALUE. Not the numbers measured from it, not the words read out of it -
// those are questions, and questions belong to `check:`. What `see:` establishes is the fact that a
// picture was taken, at a moment, and that is what every later question is about.
func (e *Executor) doSee(s walk.Step) error {
	name := seeName(s)
	if name == "" {
		return fail(s.Name, "`see:` must name the frame: `see: {name: shot}`. A picture nobody "+
			"named cannot be asked about, and an unnamed capture costs a screendump for nothing")
	}
	if err := refuseLegacySee(s); err != nil {
		return err
	}

	f, err := e.Driver.Frame()
	if err != nil {
		return fail(s.Name, "capturing the frame: %v", err)
	}
	if e.shots == nil {
		e.shots = map[string]string{}
	}
	e.shots[name] = f.Path()
	e.Driver.Log(fmt.Sprintf("  see %s: %s (%s)", name, filepath.Base(f.Path()), sizeOf(f)))
	w, h := f.Size()
	e.storePicture(f.Path(), name, "see", w, h)
	return nil
}

// storePicture puts a NAMED capture into the run's library, and records the name.
//
// THIS IS THE ONLY WAY A PICTURE ENTERS THE LIBRARY, and that restriction is the design: a `see:` and
// a `screenshot:` are the two places a FILE or a PERSON asks for a picture by name. The recorder's
// one frame a second is the recording, and putting two thousand unnamed frames in a library would be
// 2 GB of pictures nobody asked for - MEASURED: 542 frames, 541 MB, in one work directory.
//
// IT IS DEDUPLICATED BY CONTENT. MEASURED: 72.5% of a run's frames are byte-identical repeats of the
// frame before, so a `see:` in a loop that captures an unchanged screen eight times stores one file.
//
// A FAILURE TO STORE IS NOT A FAILURE OF THE STEP. The capture happened and the walkthrough may be
// about to act on it; a library that could not be written is reported and the run continues.
func (e *Executor) storePicture(path, name, kind string, w, h int) {
	if e.Library == nil {
		return
	}
	sha, err := e.Library.Put(path)
	if err != nil {
		e.Driver.Log(fmt.Sprintf("  library: could not store %q: %v", name, err))
		return
	}
	if e.OnImage != nil {
		e.OnImage(name, sha, kind, "", 0, w, h, nil, "")
	}
	e.Driver.Log(fmt.Sprintf("  library: %s -> %s", name, sha))
}

// seeName reads the name a `see:` binds, accepting both `see: {name: x}` and `see: x`.
func seeName(s walk.Step) string {
	switch v := s.Spec["see"].(type) {
	case string:
		return strings.TrimSpace(v)
	case map[string]any:
		name, _ := v["name"].(string)
		return strings.TrimSpace(name)
	}
	return ""
}

// refuseLegacySee reports the shape a file must stop writing, WITH the shape to write instead.
//
// A SILENT ACCEPTANCE WOULD BE WORSE THAN AN ERROR: a file that still writes `of: [text]` would
// capture a frame and bind a name, its `check:` would find no words, and the failure would point at
// the check rather than at the step that was written in the old language.
func refuseLegacySee(s walk.Step) error {
	raw, ok := s.Spec["see"].(map[string]any)
	if !ok {
		return nil
	}
	if _, has := raw["of"]; has {
		return fail(s.Name, "`see:` no longer measures anything - it captures a frame and names it. "+
			"Move `of:` to the `check:` that asks the question: "+
			"`see: {name: shot}` then `check: {name: ok, from: shot, mode: text, rect: ..., words: ...}`")
	}
	if _, has := raw["rect"]; has {
		return fail(s.Name, "`see:` no longer takes `rect:` - a region belongs to the QUESTION, not "+
			"the capture. `see: {name: shot}` then `check: {..., from: shot, rect: ...}`")
	}
	if _, has := raw["reference"]; has {
		return fail(s.Name, "`see:` no longer compares to a reference - that is a question. "+
			"`see: {name: shot}` then `check: {name: same, from: shot, mode: like, reference: ...}`")
	}
	return nil
}

// sizeOf formats a frame's dimensions for a log line.
func sizeOf(f Frame) string {
	w, h := f.Size()
	return fmt.Sprintf("%dx%d", w, h)
}

func parseRect(raw any) (screen.Rect, error) {
	return parseRectAt(raw, [2]int{})
}

// parseRectAt is `parseRect` for a step that also says `rect_at:` - the screen its pixels were
// written for. Without it a pixel rect is scaled by the frame, which is wrong whenever the two differ.

// parseRectAt is `parseRect` for a step that also says `rect_at:` - the screen its pixels were
// written for. Without it a pixel rect is scaled by the frame, which is wrong whenever the two differ.
func parseRectAt(raw any, at [2]int) (screen.Rect, error) {
	switch v := raw.(type) {
	case string:
		return parseRectWords(v)
	case []any:
		if len(v) != 4 {
			return screen.Rect{}, fmt.Errorf("`rect:` as a list needs [x0, y0, x1, y1] as " +
				"fractions of the frame")
		}
		var f [4]float64
		for i := range v {
			n, ok := toFloat(v[i])
			if !ok {
				return screen.Rect{}, fmt.Errorf("`rect:` entry %d is not a number", i+1)
			}
			f[i] = n
		}
		// A LIST IS PIXELS OR FRACTIONS, AND THE TWO ARE TOLD APART BY MAGNITUDE.
		//
		// Pixels are what the older files carry - `rect: [760, 200, 1400, 560]` on a 1920x1080 frame -
		// and a fraction is what the newer form prefers, because it cannot be measured at the wrong
		// size. A fraction is at most 1.0 and a pixel coordinate is at least 1 in any real frame, so
		// values above 1 in any position mean PIXELS.
		//
		// IT IS NOT A GUESS: a rect of [0, 0, 1, 1] is the single top-left pixel and no file means
		// that, while a rect whose last two numbers are 1920 and 1080 is a frame this repository
		// measures at on every page. The alternative - a second key - is what `rect_at:` already is,
		// except `rect_at:` says the SIZE and this says the UNIT.
		pixels := f[0] > 1 || f[1] > 1 || f[2] > 1 || f[3] > 1
		if !pixels {
			return screen.Rect{From: f[1], To: f[3], XFrom: f[0], XTo: f[2]}, nil
		}
		// A PIXEL RECT IS CONVERTED AGAINST THE FRAME THE READING ARRIVED FROM, which the caller
		// knows and this function does not - so it is returned unconverted and the caller scales it.
		return screen.Rect{
			From: f[1], To: f[3], XFrom: f[0], XTo: f[2], Pixels: true,
			AtW: at[0], AtH: at[1],
		}, nil
	}
	return screen.Rect{}, fmt.Errorf("`rect:` must be a name like `bottom 4.5%%` or a list of " +
		"four numbers")
}

var rectWords = regexp.MustCompile(`^(middle|top|bottom)(?:\s+(\d+(?:\.\d+)?)\s*(%|px))?$`)

// parseRectWords reads the named forms: `middle`, `top`, `top 4.5%`, `bottom 40px`.
//
// `px` IS ACCEPTED AND CONVERTED, because a panel's height is a pixel measurement everywhere in the
// files (`panel 48 rows`, `panel 8 rows`) and pretending otherwise would make those numbers
// unquotable. The conversion divides by the frame height, which the caller passes.

// parseRectWords reads the named forms: `middle`, `top`, `top 4.5%`, `bottom 40px`.
//
// `px` IS ACCEPTED AND CONVERTED, because a panel's height is a pixel measurement everywhere in the
// files (`panel 48 rows`, `panel 8 rows`) and pretending otherwise would make those numbers
// unquotable. The conversion divides by the frame height, which the caller passes.
func parseRectWords(s string) (screen.Rect, error) {
	m := rectWords.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return screen.Rect{}, fmt.Errorf("`rect: %q` is not a region; use `middle`, `top`, "+
			"`top 4.5%%`, `bottom 40px`, or four fractions", s)
	}
	where, amount, unit := m[1], m[2], m[3]
	if amount == "" {
		switch where {
		case "middle":
			return screen.Rect{From: 0.35, To: 0.65, XTo: 1}, nil
		case "top":
			return screen.Rect{From: 0, To: 0.045, XTo: 1}, nil
		default:
			return screen.Rect{From: 0.955, To: 1, XTo: 1}, nil
		}
	}
	n, err := strconv.ParseFloat(amount, 64)
	if err != nil {
		return screen.Rect{}, fmt.Errorf("`rect: %q`: %v", s, err)
	}
	var frac float64
	switch unit {
	case "%":
		frac = n / 100
	case "px":
		// 1080 is the only height any file in this repository measures at; the guard is that every
		// file says `resolution:` and this is a reading, not a coordinate.
		frac = n / 1080
	default:
		return screen.Rect{}, fmt.Errorf("`rect: %q`: the unit is %% or px", s)
	}
	if where == "top" {
		return screen.Rect{From: 0, To: frac, XTo: 1}, nil
	}
	if where == "bottom" {
		return screen.Rect{From: 1 - frac, To: 1, XTo: 1}, nil
	}
	return screen.Rect{From: 0.5 - frac/2, To: 0.5 + frac/2, XTo: 1}, nil
}

// `evalTextCriteria` WAS HERE, and `checkframe.go` with it: 703 lines plus this function, reachable
// only from `want:` - which no walkthrough ever wrote. A `check:` compares one reading and binds one
// boolean, and that is the whole of what the files use.

// Crop takes a region out of a frame, given the parsed rect.
//
// IT IS THE ONE PIECE OF ARITHMETIC THIS FILE OWES `check:`, and it lives here because the rect is
// parsed here: `screen.Rect` knows fractions, pixels and the screen the pixels were written for, and
// turning that into a rectangle of an actual image is the same work wherever it is asked for.
func Crop(img image.Image, rect screen.Rect) (image.Image, error) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return nil, fmt.Errorf("the frame has no pixels")
	}
	from, to, xf, xt := rect.From, rect.To, rect.XFrom, rect.XTo
	if rect.Pixels {
		aw, ah := rect.AtW, rect.AtH
		if aw <= 0 || ah <= 0 {
			aw, ah = w, h
		}
		from, to = rect.From/float64(ah), rect.To/float64(ah)
		xf, xt = rect.XFrom/float64(aw), rect.XTo/float64(aw)
	}
	y0 := b.Min.Y + int(float64(h)*from)
	y1 := b.Min.Y + int(float64(h)*to)
	x0 := b.Min.X + int(float64(w)*xf)
	x1 := b.Min.X + int(float64(w)*xt)
	if x1 <= x0 {
		x1 = x0 + 1
	}
	if y1 <= y0 {
		y1 = y0 + 1
	}
	if x0 < b.Min.X {
		x0 = b.Min.X
	}
	if y0 < b.Min.Y {
		y0 = b.Min.Y
	}
	if x1 > b.Max.X {
		x1 = b.Max.X
	}
	if y1 > b.Max.Y {
		y1 = b.Max.Y
	}
	if x1 <= x0 || y1 <= y0 {
		return nil, fmt.Errorf("the rect is empty on a %dx%d frame", w, h)
	}
	type subImager interface {
		SubImage(r image.Rectangle) image.Image
	}
	if si, ok := img.(subImager); ok {
		return si.SubImage(image.Rect(x0, y0, x1, y1)), nil
	}
	out := image.NewRGBA(image.Rect(0, 0, x1-x0, y1-y0))
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			out.Set(x-x0, y-y0, img.At(x, y))
		}
	}
	return out, nil
}
