package run

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/easydev-org/easyvm/internal/screen"
	"github.com/easydev-org/easyvm/internal/walk"
)

// READING THE SCREEN IS THE ONE THING THAT NEEDS OCR, and it is why tesseract is in the image.
//
// THE CROP MATTERS MORE THAN THE RECOGNITION. macOS's menu bar is twelve-pixel text; read whole, a
// 1920x1080 desktop gives tesseract a page it decides has no text at all and answers
// "Empty page!!" - which is run 42383829203's frame, a picture OF a desktop that a whole-frame read
// said nothing about. `region:` and `scale:` in a step are what make the difference: a 700x34 strip
// enlarged three times is what tesseract needs.
//
// WORDS ARE COMPARED WITH PUNCTUATION STRIPPED. A matcher for `[remote, login, on]` would otherwise
// fail on "Remote Login: On", and the Python implementation's own note records that variant costing
// a stage.

// tesseract reads text from an image file.
//
// IT IS A TOOL, NOT A LIBRARY, and that is deliberate: tesseract's language data is ~15 MB of trained
// models that a Go binding would have to ship or shell out to anyway. The image's own package is
// what `--list-langs` reports, so a missing language pack is visible in the build.
func tesseract(path, lang string) ([]string, error) {
	bin, err := exec.LookPath("tesseract")
	if err != nil {
		return nil, fmt.Errorf("tesseract is not installed: `expect:` and `until:` read text off "+
			"the screen, and there is no other way to do it. Install tesseract-ocr and %s", lang)
	}
	if lang == "" {
		lang = "eng"
	}
	cmd := exec.Command(bin, path, "stdout", "-l", lang, "--psm", "6")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("tesseract: %v (%s)", err, firstLineOf(string(out)))
	}
	var words []string
	for _, w := range strings.Fields(string(out)) {
		words = append(words, w)
	}
	return words, nil
}

// normalise strips everything that is not a letter or a digit, and lowercases.
//
// A MATCHER MUST NOT DEPEND ON PUNCTUATION. "Remote Login: On" and "remote login on" are the same
// claim, and the Python implementation lost a stage to the difference - with the extra trap that a
// phrase containing the word `on` was written unquoted in YAML, so YAML made it a boolean and the
// crash happened only when the switch was ON.
func normalise(w string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(w) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Says reports whether every one of `want` appears in `words`.
func says(words []string, want []string) bool {
	have := make(map[string]bool, len(words))
	for _, w := range words {
		have[normalise(w)] = true
	}
	for _, w := range want {
		if !have[normalise(w)] {
			return false
		}
	}
	return true
}

// readScreen captures a frame and reads it, cropping when the step says so.
//
// THE CROP IS DONE IN GO RATHER THAN WITH AN EXTERNAL TOOL. `lib/assistant.py` used Pillow for this,
// which is a Python dependency in an image that no longer has Python - and the operation is a
// rectangle of pixels copied into a smaller image.
func (e *Executor) readScreen(s walk.Step, label string) ([]string, error) {
	f, err := e.Driver.Frame()
	if err != nil {
		return nil, err
	}
	img, err := screen.Load(f.Path())
	if err != nil {
		return nil, err
	}

	crop := img
	if region, ok := rectOf(s, "region"); ok {
		// `region_at` EXISTS BECAUSE A CROP MEASURED AT ONE RESOLUTION IS WRONG AT ANOTHER. A step
		// that says [880, 960, 1045, 1000] was measured on a 1920x1080 guest; a guest booted at
		// another size would have the crop on the wrong part of the screen, and the read would
		// come back empty for a reason nobody could see.
		if at, ok := pairOf(s, "region_at"); ok {
			w, h := f.Size()
			if at[0] != w || at[1] != h {
				return nil, fmt.Errorf("`region_at` is [%d, %d] but this frame is %dx%d: the crop "+
					"was measured at another resolution and would land on the wrong part of the "+
					"screen", at[0], at[1], w, h)
			}
		}
		scale := 3
		if v, err := s.Float("scale", 3); err == nil && v > 0 {
			scale = int(v)
		}
		cropped, err := screen.Crop(img, region, scale)
		if err != nil {
			return nil, err
		}
		crop = cropped
	}

	path := filepath.Join(e.Work, "read-"+sanitise(label)+".ppm")
	if err := os.MkdirAll(e.Work, 0o755); err != nil {
		return nil, err
	}
	if err := screen.SavePPM(crop, path); err != nil {
		return nil, err
	}
	words, err := tesseract(path, "eng")
	if err != nil {
		return nil, err
	}
	e.Driver.Log(fmt.Sprintf("  %s: %d word(s): %s", label, len(words),
		truncateWords(words, 12)))
	return words, nil
}

// doUntil waits for words to appear.
func (e *Executor) doUntil(s walk.Step) error {
	want := wordsOf(s, "until")
	var alternatives [][]string
	if len(want) > 0 {
		alternatives = append(alternatives, want)
	}
	for _, set := range wordSetsOf(s, "until_any") {
		alternatives = append(alternatives, set)
	}
	if len(alternatives) == 0 {
		return fail(s.Name, "`until:` or `until_any:` must name the words to wait for")
	}

	timeout := seconds(s, "timeout", 300)
	poll := seconds(s, "poll", 3)
	e.Driver.Log(fmt.Sprintf("  waiting up to %s for %s", timeout, describeSets(alternatives)))

	start := time.Now()
	var last []string
	for time.Since(start) < timeout {
		words, err := e.readScreen(s, s.Name)
		if err != nil {
			return fail(s.Name, "%v", err)
		}
		last = words
		for _, set := range alternatives {
			if says(words, set) {
				e.Driver.Log(fmt.Sprintf("  saw %q (%s)", strings.Join(set, " "),
					time.Since(start).Round(time.Second)))
				return nil
			}
		}
		if s.Has("keep_alive") {
			// A GUEST THAT BLANKS ITS OWN SCREEN wakes on a click, and a blank screen reads as no
			// words at all - which is indistinguishable from "the pane has not appeared".
			_ = e.Driver.Click(400+(int(time.Since(start).Seconds())%40), 300)
		}
		time.Sleep(poll)
	}
	return failWithScreen(s.Name, e.Driver, "never saw %s within %s; last read: %s",
		describeSets(alternatives), timeout, truncateWords(last, 16))
}

// doUnless runs a step only when the screen does NOT already show the words.
//
// IT IS HOW A RETRY IS WRITTEN WITHOUT A CONDITIONAL INSIDE A CLICK. opensuse's installer retry must
// not press the desktop icon while YaST2 is already open, because a second double-click lands on the
// wizard - and a step that cannot say "unless this is already up" has to be a Python function.
func (e *Executor) doUnless(s walk.Step) error {
	want := wordsOf(s, "unless")
	if len(want) == 0 {
		return fail(s.Name, "`unless:` must name words")
	}
	words, err := e.readScreen(s, s.Name+" - unless")
	if err != nil {
		// A SCREEN THAT CANNOT BE READ IS NOT "THE WORDS ARE ABSENT". Guessing would run the step on
		// a screen nobody looked at, which is the failure the whole check exists to prevent.
		return fail(s.Name, "could not read the screen to decide `unless: %s`: %v",
			strings.Join(want, " "), err)
	}
	if says(words, want) {
		e.Driver.Log(fmt.Sprintf("  skipped: the screen already shows %q", strings.Join(want, " ")))
		return nil
	}
	// THE STEP CONTINUES. `unless` is a gate on the rest of the same step, and the dispatcher ran
	// this first.
	return nil
}

// --- small helpers ---------------------------------------------------------------------

func wordsOf(s walk.Step, key string) []string {
	v, ok := s.Spec[key]
	if !ok {
		return nil
	}
	switch t := v.(type) {
	case string:
		return strings.Fields(t)
	case []any:
		var out []string
		for _, item := range t {
			out = append(out, fmt.Sprint(item))
		}
		return out
	}
	return nil
}

func wordSetsOf(s walk.Step, key string) [][]string {
	v, ok := s.Spec[key]
	if !ok {
		return nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	var out [][]string
	for _, item := range list {
		switch t := item.(type) {
		case []any:
			var set []string
			for _, w := range t {
				set = append(set, fmt.Sprint(w))
			}
			out = append(out, set)
		case string:
			out = append(out, strings.Fields(t))
		}
	}
	return out
}

func describeSets(sets [][]string) string {
	var parts []string
	for _, set := range sets {
		parts = append(parts, fmt.Sprintf("%q", strings.Join(set, " ")))
	}
	return strings.Join(parts, " or ")
}

func truncateWords(words []string, n int) string {
	if len(words) <= n {
		return strings.Join(words, " ")
	}
	return strings.Join(words[:n], " ") + " ..."
}

func sanitise(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 40 {
		return out[:40]
	}
	return out
}

func firstLineOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 160 {
		s = s[:160] + "..."
	}
	return s
}

func rectOf(s walk.Step, key string) ([4]int, bool) {
	v, ok := s.Spec[key]
	if !ok {
		return [4]int{}, false
	}
	list, ok := v.([]any)
	if !ok || len(list) != 4 {
		return [4]int{}, false
	}
	var out [4]int
	for i := 0; i < 4; i++ {
		n, ok := asInt(list[i])
		if !ok {
			return [4]int{}, false
		}
		out[i] = n
	}
	return out, true
}

func pairOf(s walk.Step, key string) ([2]int, bool) {
	v, ok := s.Spec[key]
	if !ok {
		return [2]int{}, false
	}
	list, ok := v.([]any)
	if !ok || len(list) != 2 {
		return [2]int{}, false
	}
	a, oka := asInt(list[0])
	b, okb := asInt(list[1])
	if !oka || !okb {
		return [2]int{}, false
	}
	return [2]int{a, b}, true
}

// doExpectAny checks that one of several word-sets appeared after the action.
//
// IT IS THE VERDICT HALF OF A STEP, and a step that does not advance is NOT COUNTED AS ANSWERED: a
// run that says "13 of 14 answered" must not include the one that clicked and then failed its own
// check, or the transcript disagrees with the verdict.
func (e *Executor) doExpectAny(s walk.Step) error {
	sets := wordSetsOf(s, "expect_any")
	if len(sets) == 0 {
		if single := wordsOf(s, "expect"); len(single) > 0 {
			sets = [][]string{single}
		}
	}
	if len(sets) == 0 {
		return fail(s.Name, "`expect_any:` must name the word-sets that would pass")
	}
	words, err := e.readScreen(s, s.Name+" - after")
	if err != nil {
		return fail(s.Name, "%v", err)
	}
	for _, set := range sets {
		if says(words, set) {
			e.Driver.Log(fmt.Sprintf("  advanced: saw %q", strings.Join(set, " ")))
			return nil
		}
	}
	if s.Has("optional") {
		// AN OPTIONAL CHECK WHOSE NEGATIVE RESULT IS INFORMATION. It says so and continues.
		e.Driver.Log(fmt.Sprintf("  expected %s, did not see it, and the step is optional",
			describeSets(sets)))
		return nil
	}
	return failWithScreen(s.Name, e.Driver, "did not advance: wanted %s, read %q",
		describeSets(sets), truncateWords(words, 16))
}

// doAtWindow clicks a point a fixed distance below a window's top edge.
//
// THE WINDOW IS FOUND BY BRIGHTNESS, NOT BY OCR. `window_from`/`window_to`/`window_x` name a column
// scanned from one row to another, and the first row whose brightness crosses `window_brightness` is
// the window's top - which is how a dialog whose vertical position depends on what is above it can
// still be clicked at a fixed offset into itself.
func (e *Executor) doAtWindow(s walk.Step) error {
	x, dy, err := pair(s, "at_window")
	if err != nil {
		return err
	}
	from, _ := s.Float("window_from", 0)
	to, _ := s.Float("window_to", 0)
	windowX, _ := s.Float("window_x", 0)
	bright, _ := s.Float("window_brightness", 200)

	f, ferr := e.Driver.Frame()
	if ferr != nil {
		return fail(s.Name, "%v", ferr)
	}
	img, ierr := screen.Load(f.Path())
	if ierr != nil {
		return fail(s.Name, "%v", ierr)
	}
	top, ok := screen.FindWindowTop(img, int(windowX), int(from), int(to), bright)
	if !ok {
		return failWithScreen(s.Name, e.Driver,
			"no window edge in column %d between rows %d and %d at brightness %.0f",
			int(windowX), int(from), int(to), bright)
	}
	y := top + dy
	if err := e.Driver.Click(x, y); err != nil {
		return fail(s.Name, "click (%d, %d): %v", x, y, err)
	}
	e.Driver.Log(fmt.Sprintf("  clicked (%d, %d), %d px below the window top at row %d",
		x, y, dy, top))
	return e.settle(s)
}
