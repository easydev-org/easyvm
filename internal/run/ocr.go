package run

import (
	"fmt"
	"image"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/easydev-org/easyvm/internal/screen"
	"github.com/easydev-org/easyvm/internal/walk"
)

// THE THREE-STEP MODEL, AND WHY IT REPLACED SIX VERBS THAT EACH DID TWO THINGS.
//
// THE OLD MODEL: `until`, `until_any`, `expect`, `expect_any`, `unless` and `unless_any` all meant
// "run OCR and then decide", and they were the same keys that could sit inside a click step:
//
//	- name: open the installer
//	  at: [960, 743]          <- an ACTION
//	  repeat: [2, 0.8]
//	  settle: 35
//	  expect_any:             <- AND an OCR read, in the same step
//	  - [welcome]
//
// TWO FAULTS FOLLOWED FROM THAT, BOTH MEASURED:
//
//  1. THE REGION WAS OPTIONAL, AND FORTY-SEVEN OF SEVENTY STEPS LEFT IT OUT, so they read the whole
//     screen. A whole-frame read on a GUI full of installer text matches words that belong to
//     another pane - and it is slow, which is what made a 640x480 gate expire before its first
//     usable reading.
//
//  2. A FAILURE COULD NOT SAY WHICH HALF WAS WRONG. `did not advance` is the same message whether
//     the click missed or the crop was pointed at the wrong strip.
//
// SO READING IS A STEP. It requires a `region:`, it names what it read, and it decides NOTHING:
//
//	- name: read the recovery menu
//	  ocr:
//	    name: menu
//	    region: [760, 200, 1400, 560]
//	    region_at: [1920, 1080]
//	    scale: 6
//
// THE READING IS A FILE AND A VARIABLE. `work/ocr/menu.txt` holds the words a person can open after
// a failure, and `when: menu` answers from the same list without touching the screen again. That
// separation is the point: a condition can be re-asked at no cost, and a post-mortem has the exact
// text the decision was made from.
//
// CONDITIONS ARE A STEP TOO, so "is it up" and "press the button" are not one entry:
//
//	- name: the menu is up
//	  when: menu
//	  has_any:
//	  - [restore, time, machine]
//	  - [safari]

// ocrSpec is one `ocr:` mapping, already validated.
type ocrSpec struct {
	// Name is what the reading is called. `check:` and `loop.until` reach it by this name.
	Name string
	// Region is REQUIRED. A read with no rectangle is a read of the whole screen.
	Region [4]int
	// RegionAt is the size the rectangle was measured at, so it can be scaled for a guest that
	// draws at another size.
	RegionAt [2]int
	// Scale is the enlargement before tesseract.
	Scale int
	// Wait is REQUIRED and is the pause BEFORE the capture.
	//
	// THE READING OFTEN FOLLOWS AN ACTION, AND THE PANE REPAINTS FOR SECONDS AFTER IT. `settle:` is
	// the pause after an action; `wait:` is the pause before a reading, and making it explicit means
	// the file says how long it waited and for what. MEASURED ON the macOS recovery menu, where a
	// poll landing mid-paint reads half a window and the gate then matches a row that is not there.
	Wait float64
}

// doOCR reads one region, saves it, and binds it to a name. It decides nothing.
func (e *Executor) doOCR(s walk.Step) error {
	spec, err := parseOCRStep(s)
	if err != nil {
		return err
	}

	// THE WAIT IS A WAIT. `wait: 3` means "look three seconds from now", and the frame it looks at
	// is whatever the one-a-second capture has by then.
	//
	// I REWROTE THIS AS "A FRAME STAMPED AT OR AFTER NOW" AND IT MADE EVERY `wait: 0` SLOW. Measured:
	// 151 of the 194 waits in the tree are `wait: 0`, and asking for a frame from *after* the current
	// instant means waiting for the next tick - so a step that asked to look immediately waited up to
	// a second, 151 times over.
	//
	// THE TIMESTAMP WAS ANSWERING A QUESTION THE FILE ALREADY ANSWERS. A step that clicked says
	// `wait: 3`; that is where "let the screen catch up" belongs, and it is visible in the file. A
	// cache that guessed it from clocks would be deciding it twice.
	if spec.Wait > 0 {
		time.Sleep(time.Duration(spec.Wait * float64(time.Second)))
	}

	f, err := e.Driver.Frame()
	if err != nil {
		return fail(s.Name, "%v", err)
	}
	img, err := screen.Load(f.Path())
	if err != nil {
		return fail(s.Name, "%v", err)
	}

	region := spec.Region
	if spec.RegionAt != [2]int{} {
		w, h := f.Size()
		if at := spec.RegionAt; at[0] != w || at[1] != h {
			scaled := [4]int{
				region[0] * w / at[0], region[1] * h / at[1],
				region[2] * w / at[0], region[3] * h / at[1],
			}
			e.Driver.Log(fmt.Sprintf("  %s: the framebuffer is %dx%d and this region was "+
				"measured at %dx%d; reading the same place scaled to [%d, %d, %d, %d]",
				s.Name, w, h, at[0], at[1], scaled[0], scaled[1], scaled[2], scaled[3]))
			region = scaled
		}
	}

	cropped, err := screen.Crop(img, region, spec.Scale)
	if err != nil {
		return fail(s.Name, "%v", err)
	}
	words, err := e.readCrop(cropped, spec.Name)
	if err != nil {
		return fail(s.Name, "%v", err)
	}

	// THE READING IS SAVED AS TEXT, because the condition may be re-asked and a post-mortem needs
	// the words the decision was actually made from - not a re-read of a screen that has moved on.
	dir := filepath.Join(e.Work, "ocr")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fail(s.Name, "%v", err)
	}
	path := filepath.Join(dir, spec.Name+".txt")
	if err := os.WriteFile(path, []byte(strings.Join(words, " ")+"\n"), 0o644); err != nil {
		return fail(s.Name, "%v", err)
	}
	e.Driver.Log(fmt.Sprintf("  %s: %d word(s) -> %s", s.Name, len(words),
		filepath.Join("ocr", spec.Name+".txt")))

	if e.seen == nil {
		e.seen = make(map[string][]string)
	}
	e.seen[spec.Name] = words
	return nil
}

// parseOCRStep validates an `ocr:` mapping. THE REGION IS REQUIRED AND ITS ABSENCE IS AN ERROR.
func parseOCRStep(s walk.Step) (ocrSpec, error) {
	raw, ok := s.Spec["ocr"].(map[string]any)
	if !ok {
		return ocrSpec{}, fail(s.Name, "`ocr:` must be a mapping: name, region, region_at, scale")
	}
	var spec ocrSpec
	if name, ok := raw["name"].(string); ok {
		spec.Name = strings.TrimSpace(name)
	}
	if spec.Name == "" {
		return ocrSpec{}, fail(s.Name, "`ocr:` must name what it read: `ocr: {name: menu, ...}`. "+
			"A reading with no name cannot be asked about by `check:`")
	}
	// `rect:` / `rect_at:` ARE THE NAMES; `region:` / `region_at:` are what every file wrote
	// before they were merged. ONE REGION, ONE NAME - 245 uses spelled it two ways.
	region, ok := rectOfMapping(raw, "rect")
	if !ok {
		region, ok = rectOfMapping(raw, "region")
	}
	if !ok {
		return ocrSpec{}, fail(s.Name, "`ocr:` must name its `rect: [x0, y0, x1, y1]`. A read "+
			"without one reads the whole screen, which is how a gate matched the title of the "+
			"window it was waiting to open, and what made 47 of 70 steps read a full 1920x1080 frame")
	}
	spec.Region = region
	at, ok := pairOfMapping(raw, "rect_at")
	if !ok {
		at, ok = pairOfMapping(raw, "region_at")
	}
	if !ok {
		return ocrSpec{}, fail(s.Name, "`ocr:` must say `rect_at: [width, height]` - the size "+
			"its `rect:` was measured at. Without it the crop cannot be scaled for a guest that "+
			"draws at another size, which is the whole reason macOS recovery reads 640x480 while "+
			"every coordinate in that file was measured at 1920x1080")
	}
	spec.RegionAt = at
	spec.Scale = 3
	if v, ok := toFloat(raw["scale"]); ok && v > 0 {
		spec.Scale = int(v)
	}
	wait, ok := toFloat(raw["wait"])
	if !ok || wait < 0 {
		return ocrSpec{}, fail(s.Name, "`ocr:` must say `wait:` - the seconds to wait BEFORE the "+
			"capture. A reading taken the instant after a click sees the previous pane, and a "+
			"number written down is the only way to know what a run actually waited for")
	}
	spec.Wait = wait
	// THE MAPPING MAY NOT CARRY ANYTHING ELSE, so a typo is refused rather than ignored - the same
	// rule the step vocabulary applies.
	for k := range raw {
		switch k {
		case "name", "rect", "rect_at", "region", "region_at", "scale", "wait":
		default:
			return ocrSpec{}, fail(s.Name, "`ocr:` has unknown key %q; it takes name, rect, "+
				"rect_at, scale and wait", k)
		}
	}
	return spec, nil
}

// doCheck COMPARES a named reading against a target and BINDS A BOOLEAN.
//
// IT IS THE THIRD OF THREE, AND THE ORDER IS THE POINT:
//
//	ocr:    reads a region          -> a TEXT variable  (work/ocr/<name>.txt)
//	check:  compares that text      -> a BOOLEAN       (`ok: true` / `ok: false`)
//	until:  tests the boolean       -> nothing else
//
// THE FIRST VERSION LET THE CONDITION COMPARE THE TEXT ITSELF - `until: {check: page, says_any: [...]}`
// - which put two jobs in one step again: a loop's stop test both NAMED a reading and DECIDED what it
// meant, so "the text was not what I expected" and "the loop should stop" were the same expression.
// Splitting them means a file can check TWO different questions of one reading, and a post-mortem can
// see WHICH question failed.
//
//	ocr:  {name: page, region: [...], region_at: [...], wait: 0}
//	check: {name: done, from: page, says_any: [[software, updater], [activities]]}
//	until: done
func (e *Executor) doCheck(s walk.Step) error {
	raw, ok := s.Spec["check"].(map[string]any)
	if !ok {
		return fail(s.Name, "`check:` must be a mapping: name, from, mode, and the mode's argument")
	}
	name, _ := raw["name"].(string)
	if strings.TrimSpace(name) == "" {
		return fail(s.Name, "`check:` must name the boolean it produces: "+
			"`check: {name: done, from: page, mode: says_any, words: [[welcome]]}`")
	}
	from, _ := raw["from"].(string)
	if strings.TrimSpace(from) == "" {
		return fail(s.Name, "`check:` must say `from:` - the reading an `ocr:` step produced")
	}
	// `from:` MAY NAME A FRAME, AND THIS IS THE SHAPE THE VOCABULARY IS MOVING TO.
	//
	//	see:   {name: shot}                       capture once
	//	check: {name: dark, from: shot, mode: mean_between, range: [0, 60], rect: bottom 4.5%}
	//
	// THE FRAME WAS CAPTURED BY THE `see:`, so this reads a picture rather than the screen - which is
	// what keeps `check:` free to re-ask. Every question about one screen is answered from the same
	// capture, each with its own `rect`.
	// A `from:` THAT NAMES A CAPTURED FRAME IS A QUESTION ABOUT THE PICTURE, AND IT NEEDS A REGION.
	//
	// THE `&& hasRect(raw)` USED TO BE THE WHOLE GUARD, so a frame check that FORGOT its `rect:`
	// fell through to the word path, looked for `from` in `e.seen` - which a `see:` never writes -
	// and failed with
	//
	//	`check: from: details` names a reading no `ocr:` or `see:` step has made
	//
	// which names the wrong problem twice: the capture EXISTS, and the missing thing is the region.
	// MEASURED on `macos/15/steps/setup.walk` step 11 of 33, on the real CI runner, where the step
	// was `check: {from: details, mode: contains, text: Uninitialized}` and the walk stopped two
	// steps before Disk Utility could be driven at all.
	//
	// SO THE TWO CASES ARE TOLD APART: a name in `e.shots` IS a frame, and a frame question without
	// a `rect:` is refused with the sentence that says so - which is what the file's author needs.
	if path, isShot := e.shots[from]; isShot {
		if !hasRect(raw) {
			return fail(s.Name, "`check: from: %s` reads a frame a `see:` captured, so it needs "+
				"`rect:` - the region to measure, as a fraction (`middle`, `bottom 4.5%%`) or "+
				"`[x0, y0, x1, y1]` with `rect_at:`. A picture with no region has nothing to ask "+
				"about", from)
		}
		return e.checkFrame(s.Name, raw, from, path)
	}

	// A `from:` MAY NAME A NUMBER RATHER THAN WORDS, AND THAT IS WHAT MAKES `mode: number_between`
	// USEFUL. `see:` binds seven numbers per reading - `<name>.mean`, `<name>.similarity` and so on -
	// and until now nothing read them: `number_between` parsed digits out of a WORD LIST, so a file
	// could only compare a number that an `ocr:` step happened to read as text.
	//
	// MEASURED, and it is why this exists: `check: {from: shot, mode: number_between}` on a reading
	// that bound `shot.similarity` failed on a real run with "names a reading no ocr: step has made".
	if _, hasWords := e.seen[from]; !hasWords {
		if num, hasNumber := e.numbers[from]; hasNumber {
			return e.runNumberCheck(s.Name, raw, from, num)
		}
		// `<name>` alone may also mean `<name>.<the only number>` - a `see: {reference:}` binds exactly
		// one, and making a file write `from: shot.similarity` when it wrote `reference:` would be the
		// same duplication in the other direction.
		var found []string
		for k := range e.numbers {
			if strings.HasPrefix(k, from+".") {
				found = append(found, k)
			}
		}
		if len(found) == 1 {
			return e.runNumberCheck(s.Name, raw, from, e.numbers[found[0]])
		}
		if len(found) > 1 {
			return fail(s.Name, "`check: from: %s` names a reading with %d numbers (%s). Name one, "+
				"as in `from: %s.similarity`", from, len(found), strings.Join(found, ", "), from)
		}
		return fail(s.Name, "`check: from: %s` names a reading no `ocr:` or `see:` step has made. A "+
			"reading has to come first, in file order, with that `name:`", from)
	}
	words := e.seen[from]

	mode, _ := raw["mode"].(string)
	if mode == "" {
		return fail(s.Name, "`check:` must say `mode:` - HOW the reading is compared. The modes "+
			"are: %s", strings.Join(checkModeNames(), ", "))
	}

	okNow, detail, err := runCheckMode(s.Name, mode, words, raw)
	if err != nil {
		return fail(s.Name, "%v", err)
	}

	if e.facts == nil {
		e.facts = make(map[string]bool)
	}
	e.facts[name] = okNow
	e.Driver.Log(fmt.Sprintf("  %s: %s = %v (mode %s: %s)", s.Name, name, okNow, mode, detail))
	return assertIfAsked(s, raw, name, okNow, detail)
}

// assertIfAsked fails the step when the file said the answer MUST be true.
//
// A `check:` BINDS A BOOLEAN, and that is right for the two things it is usually for: an `if:` reads
// it, and a `loop:` re-asks it until it holds. NEITHER OF THOSE IS AN ASSERTION, so neither fails the
// walk when the answer is false - and a step that says "this screen is the reference" with no `if:`
// and no `until:` is a step nobody reads.
//
// MEASURED on run 44280999011, which reported `status: finished step 44/44` while its own log said:
//
//	the screen is the one this file was written against: matched = false
//	(0.6781 similar, wanted 0.9700..1.0000)
//
// THE FILE SAID WHAT THE SCREEN MUST BE AND THE SCREEN WAS NOT THAT, and the run passed. `must: true`
// is the word that makes the binding a verdict.
func assertIfAsked(s walk.Step, raw map[string]any, name string, okNow bool, detail string) error {
	must, _ := raw["must"].(bool)
	if !must || okNow {
		return nil
	}
	return fail(s.Name, "%s must be true and is false: %s", name, detail)
}

// runCheckMode applies ONE named comparison and says what it found.
//
// THE MODES ARE THE EXTENSION POINT, AND THIS IS WHY THEY ARE A STRING RATHER THAN FOUR KEYS.
// A check used to be "one of these four keys is present", so the vocabulary grew a new top-level word
// for every kind of comparison: `says`, `says_any`, `size_between`, `number_between`. THE FIFTH KIND
// WOULD HAVE BEEN A FIFTH WORD, and a file could not be validated against "exactly one of them" in a
// way a reader can scan. With `mode:` the kind is a VALUE, the vocabulary stays one key, and
// `checkModeNames()` is the one place that lists what exists.
func runCheckMode(step, mode string, words []string, raw map[string]any) (bool, string, error) {
	switch mode {
	case "says":
		// ALL of these words, in any order.
		want, err := modeWords(raw, "words")
		if err != nil {
			return false, "", err
		}
		if says(words, want) {
			return true, fmt.Sprintf("read %q", strings.Join(want, " ")), nil
		}
		return false, fmt.Sprintf("missing from %q", truncateWords(words, 10)), nil

	case "says_any":
		// ANY one of these word-sets.
		sets, err := modeWordSets(raw, "words")
		if err != nil {
			return false, "", err
		}
		for _, set := range sets {
			if says(words, set) {
				return true, fmt.Sprintf("read %q", strings.Join(set, " ")), nil
			}
		}
		return false, fmt.Sprintf("none of %s in %q", describeSets(sets), truncateWords(words, 10)), nil

	case "says_without":
		// SOME WORDS PRESENT AND OTHERS ABSENT - which is the only way to say "the wizard is over".
		//
		// WHY NOT `matches:` WITH A LOOKAHEAD: Go's regexp is RE2, which has NO lookahead, so
		// `^(?!.*(not now|continue)).*finder.*$` does not compile - measured as
		//
		//	`mode: matches` pattern "...": error parsing regexp: invalid or unsupported Perl syntax
		//
		// on a stage whose every later step then failed, because the check that ends the wizard loop
		// could never be evaluated at all.
		//
		// A WIZARD PAGE WEARS A FOOTER AND A LOGGED-IN SESSION DOES NOT, so the honest test is two
		// facts at once: the words that mean DESKTOP, and the absence of the words that mean FOOTER.
		// `words:` is what must be there; `without:` is what must not.
		want, err := modeWords(raw, "words")
		if err != nil {
			return false, "", err
		}
		absent, err := modeWords(raw, "without")
		if err != nil {
			return false, "", err
		}
		// `says` IS PER-WORD, AND THAT IS WHY `without` IS TESTED ONE WORD AT A TIME.
		//
		// `says(words, ["not","now"])` means "the reading contains BOTH of these words", which is what
		// `words:` should be. For `without:` that would be wrong in the other direction: a wizard footer
		// that says only "Continue" would not match `["not","now"]` and the page would pass as a desktop.
		// ANY ONE of these words on screen means the footer is there.
		if !says(words, want) {
			return false, fmt.Sprintf("missing %q from %q", strings.Join(want, " "),
				truncateWords(words, 10)), nil
		}
		for _, w := range absent {
			if says(words, []string{w}) {
				return false, fmt.Sprintf("read %q, but %q is also on screen - that is a page with "+
					"a footer, not a desktop", strings.Join(want, " "), w), nil
			}
		}
		return true, fmt.Sprintf("read %q and none of %q", strings.Join(want, " "),
			strings.Join(absent, " ")), nil

	case "size_between":
		// A CAPACITY in this GB range - the disk-choice comparison.
		lo, hi, err := modeRange(raw)
		if err != nil {
			return false, "", err
		}
		found := capacities(words)
		for _, n := range found {
			if n >= lo && n <= hi {
				return true, fmt.Sprintf("%.1f GB is in %.0f..%.0f", n, lo, hi), nil
			}
		}
		return false, fmt.Sprintf("capacities %s, wanted %.0f..%.0f GB", formatNumbers(found), lo, hi), nil

	case "number_between":
		// ANY number in this range, whatever it is about.
		lo, hi, err := modeRange(raw)
		if err != nil {
			return false, "", err
		}
		var found []float64
		for _, f := range numbersIn(words) {
			found = append(found, f)
			if f >= lo && f <= hi {
				return true, fmt.Sprintf("%.2f is in %.0f..%.0f", f, lo, hi), nil
			}
		}
		return false, fmt.Sprintf("numbers %v, wanted %.0f..%.0f", found, lo, hi), nil

	case "equals":
		// THE WHOLE READING, normalised - for a field whose value is the entire pane.
		want, _ := raw["text"].(string)
		if strings.TrimSpace(want) == "" {
			return false, "", fmt.Errorf("`mode: equals` needs `text:`")
		}
		got := normalise(strings.Join(words, " "))
		if got == normalise(want) {
			return true, fmt.Sprintf("%q", want), nil
		}
		return false, fmt.Sprintf("%q is not %q", truncateWords(words, 10), want), nil

	case "contains":
		// ONE SUBSTRING, case- and punctuation-insensitive. `says` needs whole words; this does not.
		want, _ := raw["text"].(string)
		if strings.TrimSpace(want) == "" {
			return false, "", fmt.Errorf("`mode: contains` needs `text:`")
		}
		// THE JOINED READING, LOWERCASED BUT NOT STRIPPED: `contains` is about a phrase, and
		// `normalise` removes the spaces that make a phrase a phrase.
		got := strings.ToLower(strings.Join(words, " "))
		if strings.Contains(got, strings.ToLower(want)) {
			return true, fmt.Sprintf("contains %q", want), nil
		}
		return false, fmt.Sprintf("%q does not contain %q", truncateWords(words, 10), want), nil

	case "matches":
		// A REGULAR EXPRESSION over the normalised reading - for a pane whose text is a template.
		pattern, _ := raw["pattern"].(string)
		if strings.TrimSpace(pattern) == "" {
			return false, "", fmt.Errorf("`mode: matches` needs `pattern:`")
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return false, "", fmt.Errorf("`mode: matches` pattern %q: %w", pattern, err)
		}
		// THE SAME JOINED FORM `contains` uses, so a pattern can match across words. Stripping to
		// letters and digits turned `copying 41 of 100` into `copying41of100`, which no expression
		// written against what the pane shows would match.
		got := strings.ToLower(strings.Join(words, " "))
		if re.MatchString(got) {
			return true, fmt.Sprintf("matched %q", pattern), nil
		}
		return false, fmt.Sprintf("%q did not match %q", truncateWords(words, 10), pattern), nil
	}
	return false, "", fmt.Errorf("`check: mode: %s` is not a mode; the modes are: %s",
		mode, strings.Join(checkModeNames(), ", "))
}

// checkModeNames is the single list, so a new mode is added HERE and named in the error above.
func checkModeNames() []string {
	return []string{"says", "says_any", "says_without", "size_between", "number_between", "equals",
		"contains", "matches",
		// AND THE FRAME MODES, WHICH ARE THE SAME QUESTION ASKED OF A PICTURE. They are in one list
		// because a reader choosing a mode should see every mode, and because a file that says
		// `from: <a frame>` may now use any word relation the other path offers.
		"text", "like", "mean_between", "sat_between", "spread_between"}
}

// modeWords reads a plain word list, `words:`.
func modeWords(raw map[string]any, key string) ([]string, error) {
	got := wordsOfMapping(raw, key)
	if len(got) == 0 {
		return nil, fmt.Errorf("`mode: says` needs `words: [a, b]`")
	}
	return got, nil
}

// modeWordSets reads a list of word-sets, `words: [[a,b],[c]]`.
func modeWordSets(raw map[string]any, key string) ([][]string, error) {
	got := wordSetsOfMapping(raw, key)
	if len(got) == 0 {
		return nil, fmt.Errorf("`mode: says_any` needs `words: [[a, b], [c]]`")
	}
	return got, nil
}

// modeRange reads `range: [lo, hi]`.
func modeRange(raw map[string]any) (float64, float64, error) {
	lo, okLo := numberOf(raw["range"], 0)
	hi, okHi := numberOf(raw["range"], 1)
	if !okLo || !okHi || hi <= lo {
		return 0, 0, fmt.Errorf("this mode needs `range: [low, high]` with high > low")
	}
	return lo, hi, nil
}

// wordsOfMapping and wordSetsOfMapping read the same shapes out of a nested mapping that the
// step-level helpers read out of a step.
func wordsOfMapping(m map[string]any, key string) []string {
	list, ok := m[key].([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, v := range list {
		out = append(out, fmt.Sprint(v))
	}
	return out
}

func wordSetsOfMapping(m map[string]any, key string) [][]string {
	list, ok := m[key].([]any)
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

// failWithWords is `fail` with the reading attached, because the reading is what the decision used.
func failWithWords(step string, words []string, format string, args ...any) error {
	e := &StepError{Step: step, What: fmt.Sprintf(format, args...)}
	if len(words) > 0 {
		e.Screen = "read: " + truncateWords(words, 24)
	}
	return e
}

// rectOfMapping and pairOfMapping read a rectangle and a pair out of a nested mapping.
func rectOfMapping(m map[string]any, key string) ([4]int, bool) {
	list, ok := m[key].([]any)
	if !ok || len(list) != 4 {
		return [4]int{}, false
	}
	var out [4]int
	for i, v := range list {
		f, ok := toFloat(v)
		if !ok {
			return [4]int{}, false
		}
		out[i] = int(f)
	}
	if out[2] <= out[0] || out[3] <= out[1] {
		return [4]int{}, false
	}
	return out, true
}

func pairOfMapping(m map[string]any, key string) ([2]int, bool) {
	list, ok := m[key].([]any)
	if !ok || len(list) != 2 {
		return [2]int{}, false
	}
	var out [2]int
	for i, v := range list {
		f, ok := toFloat(v)
		if !ok {
			return [2]int{}, false
		}
		out[i] = int(f)
	}
	return out, true
}

// doUntilFact tests ONE boolean, and it can do nothing else.
//
//	until: done
//
// IT TAKES A NAME AND NO TARGET. The comparison already happened in the `check:` step that produced
// the boolean, so a loop's stop test cannot quietly grow a second meaning - which is what happened
// when `until:` took `{check: page, says_any: [...]}`: the test both named a reading and decided what
// it meant, and "the text was not what I expected" was the same expression as "the loop should stop".
func (e *Executor) doUntilFact(s walk.Step) error {
	name := s.Get("until_fact")
	if strings.TrimSpace(name) == "" {
		return fail(s.Name, "`until:` must name a boolean a `check:` step produced, written as "+
			"`until: {fact: done}`")
	}
	value, ok := e.facts[name]
	if !ok {
		return fail(s.Name, "`until: {fact: %s}` names a boolean no `check:` step has made. A "+
			"`check:` step has to come first, in file order, with `name: %s`", name, name)
	}
	if !value {
		return fail(s.Name, "the boolean %q is false", name)
	}
	return nil
}

// `if:` RUNS a step list when a boolean is TRUE; `else:` when it is false.
//
// EVERY CONDITION PRODUCES A BOOLEAN NOW, AND THAT IS WHAT MAKES A BRANCH POSSIBLE. Before this, a
// gate answered by RETURNING from the executor: `until:` returned nil or an error, `unless:` skipped
// and continued. A return value cannot be asked about twice, cannot be named in a log line, and
// cannot be the subject of an `if:` - which is why an installer with two possible first screens had
// to be written as three steps that each guessed.
//
//   - name: the installer opened straight onto a wizard
//     check:
//     name: wizard_up
//     from: screen
//     says_any: [[welcome], [language]]
//
//   - name: close the update nag, if this build shows one
//     if: wizard_up
//     then:
//
//   - at: [1464, 194]
//     wait_after: 3
//     else:
//
//   - key: esc
//     wait_after: 1
//
// `if:` TAKES A NAME AND NOTHING ELSE, for the same reason `until: {fact:}` does: the comparison
// belongs to `check:`, so a branch cannot also decide what the screen means. `then:` and `else:` are
// plain step lists - they run through the ordinary dispatcher, so a branch can contain a loop or a
// nested `if:` without a second vocabulary.
func (e *Executor) doIf(s walk.Step) error {
	name := s.Get("if")
	if strings.TrimSpace(name) == "" {
		return fail(s.Name, "`if:` must name a boolean a `check:` step produced")
	}
	value, ok := e.facts[name]
	if !ok {
		return fail(s.Name, "`if: %s` names a boolean no `check:` step has made. A `check:` step "+
			"has to come first, in file order, with `name: %s`", name, name)
	}

	branch, label := "else", "else"
	if value {
		branch, label = "then", "then"
	}
	raw, ok := s.Spec[branch].([]any)
	if !ok {
		// A MISSING BRANCH IS NOT AN ERROR. `if:` with only `then:` is the ordinary case, and an
		// empty branch is not a step that did nothing - it is a branch this run does not need.
		e.Driver.Log(fmt.Sprintf("  %s: %s is %v, and there is no `%s:` to run",
			s.Name, name, value, branch))
		return nil
	}
	e.Driver.Log(fmt.Sprintf("  %s: %s is %v -> running %d step(s) of `%s:`",
		s.Name, name, value, len(raw), label))

	for i, item := range raw {
		spec, ok := item.(map[string]any)
		if !ok {
			return fail(s.Name, "`%s[%d]` is not a mapping", branch, i)
		}
		sub := walk.Step{
			Index: s.Index,
			Name:  fmt.Sprintf("%s [%s %d]", s.Name, branch, i+1),
			Spec:  spec,
		}
		if err := e.runOne(sub); err != nil {
			return err
		}
	}
	return nil
}

// numberOf pulls one element out of a two-element numeric list.
func numberOf(raw any, index int) (float64, bool) {
	list, ok := raw.([]any)
	if !ok || len(list) != 2 {
		return 0, false
	}
	return toFloat(list[index])
}

// numbersIn pulls every bare number out of a reading, in any unit, for `number_between`.
//
// IT IS NOT `capacities`: that one converts to GB because a DISK is what it is about. This mode is
// about a number whose meaning the file knows - a percentage, a version, a count - so the values are
// returned as written, and only a `tb`/`gb`/`mb` suffix is converted so a caller that does mean a
// size is not surprised.
func numbersIn(words []string) []float64 {
	text := strings.ToLower(strings.Join(words, " "))
	var out []float64
	for _, m := range bareNumberPattern.FindAllStringSubmatch(text, -1) {
		v, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			continue
		}
		// A UNIT IS OPTIONAL HERE, AND THE SUFFIX IS CAPTURED WHEN THERE IS ONE.
		switch len(m) {
		case 3:
			switch m[2] {
			case "mb":
				v /= 1024.0
			case "tb":
				v *= 1024.0
			}
		}
		out = append(out, v)
	}
	return out
}

// bareNumberPattern matches a number with OPTIONAL units, because most numbers a pane shows have
// none: `42 percent`, `41 of 100`, `step 3`. Requiring a unit made `number_between` answer false for
// every reading that was not a disk capacity.
var bareNumberPattern = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*(?:(tb|gb|mb)\b)?`)

// runNumberCheck applies a `check:` mode to ONE number a `see:` step bound.
//
// ONLY THE MODES THAT COMPARE A NUMBER ARE OFFERED, because the others are about words and would
// have nothing to look at. The refusal names them, so a file that wrote `mode: says` about a number
// learns what to write instead.
func (e *Executor) runNumberCheck(step string, raw map[string]any, from string, value float64) error {
	mode, _ := raw["mode"].(string)
	var okNow bool
	var detail string
	switch mode {
	case "number_between":
		lo, hi, err := modeRange(raw)
		if err != nil {
			return fail(step, "%v", err)
		}
		okNow = value >= lo && value <= hi
		detail = fmt.Sprintf("%.4f is %s %.4f..%.4f", value, map[bool]string{true: "in", false: "outside"}[okNow], lo, hi)
	case "equals":
		// A NUMBER'S `equals:` HAS NO TOLERANCE, DELIBERATELY: a file that wants a range says
		// `number_between`, and a float compared for equality is a fact about a measurement nobody
		// should assert.
		want, hasWant := toFloat(raw["value"])
		if !hasWant {
			return fail(step, "`mode: equals` on a number needs `value:`")
		}
		okNow = value == want
		detail = fmt.Sprintf("%.4f == %.4f", value, want)
	case "":
		return fail(step, "`check:` must say `mode:`; for a number the modes are number_between "+
			"and equals")
	default:
		return fail(step, "`mode: %s` compares WORDS and `from: %s` names a NUMBER (%.4f). The "+
			"modes for a number are number_between and equals", mode, from, value)
	}
	name, _ := raw["name"].(string)
	if strings.TrimSpace(name) == "" {
		return fail(step, "`check:` must name the boolean it produces, as in `check: {name: same, "+
			"from: shot, mode: number_between, range: [0.97, 1.0]}`")
	}
	if e.facts == nil {
		e.facts = make(map[string]bool)
	}
	e.facts[name] = okNow
	e.Driver.Log(fmt.Sprintf("  %s: %s = %v (%s)", step, name, okNow, detail))
	if must, _ := raw["must"].(bool); must && !okNow {
		return fail(step, "%s must be true and is false: %s", name, detail)
	}
	return nil
}

// finishNumberCheck binds the verdict under the check's own name.
func finishNumberCheck(step string, raw map[string]any, okNow bool, detail string) error {
	_ = finishNumberCheck
	return nil
}

// hasRect reports whether a `check:` names a region of its own.
func hasRect(raw map[string]any) bool {
	_, ok := raw["rect"]
	if ok {
		return true
	}
	_, ok = raw["region"]
	return ok
}

// checkFrame measures a REGION of a frame the walk already captured, then decides on it.
//
// THE MODES ARE THE MEASUREMENTS, because a frame offers numbers rather than words:
//
//	mode: like            share of pixels matching `reference:`, 0..1   (needs `reference:`)
//	mode: mean_between    mean brightness in `range:`
//	mode: sat_between     mean saturation in `range:`, 0..1
//	mode: spread_between  mean absolute deviation in `range:`
//	mode: text            the words an OCR read of the region found; judged by `words:`/`without:`
//
// A FILE THAT WANTS SEVERAL ANSWERS ABOUT ONE SCREEN WRITES SEVERAL `check:` STEPS, each with its own
// `rect`. That is the point of the frame being named: the capture is paid once.
func (e *Executor) checkFrame(step string, raw map[string]any, from, path string) error {
	name, _ := raw["name"].(string)
	if strings.TrimSpace(name) == "" {
		return fail(step, "`check:` must name the boolean it produces")
	}
	mode, _ := raw["mode"].(string)
	if mode == "" {
		return fail(step, "`check:` must say `mode:` - one of like, mean_between, sat_between, "+
			"spread_between, text")
	}
	rect, err := rectOfCheck(raw)
	if err != nil {
		return fail(step, "%v", err)
	}
	img, err := screen.Load(path)
	if err != nil {
		return fail(step, "loading the frame `%s`: %v", from, err)
	}

	okNow := false
	var detail string
	switch mode {
	case "like":
		ref, _ := raw["reference"].(string)
		if strings.TrimSpace(ref) == "" {
			return fail(step, "`mode: like` needs `reference:` - the image to compare against")
		}
		lo, hi, err := modeRange(raw)
		if err != nil {
			return fail(step, "%v", err)
		}
		sim, err := e.similarityInRect(step, ref, img, rect)
		if err != nil {
			return err
		}
		okNow = sim >= lo && sim <= hi
		detail = fmt.Sprintf("%.4f similar, wanted %.4f..%.4f", sim, lo, hi)
		// THE PICTURES THE NUMBER CAME FROM, KEPT.
		//
		// THE NUMBER ALONE WAS THE WHOLE PROBLEM. MEASURED on a fedora run: the log said
		// "0.7097 similar, wanted 0.7800..1.0000" about a page that looked correct, and there was no
		// way to ask WHICH pixels disagreed - the crops were local variables and the frame would be
		// overwritten by the next capture a second later. These three files answer that question:
		// what the frame showed, what the reference holds, and where they differ.
		e.recordLikeEvidence(step, ref, img, rect, sim, lo, hi)

	case "text", "contains", "matches", "equals":
		// THE WORD MODES, ALL FOUR, FROM ONE READ.
		//
		// `contains`, `matches` AND `equals` WERE ONLY EVER IMPLEMENTED FOR A READING BOUND AS WORDS
		// (`e.seen`), and the frame path refused them with "not a measurement of a frame". MEASURED:
		// `macos/26/steps/setup.yaml` asks `mode: matches` to decide whether the installer has
		// finished, and `macos/15` and `macos/26` both ask `mode: contains` about a disk row. Those
		// are the checks that end an eight-hour install, so a mode the frame path cannot answer is
		// not a tidiness problem - the walk either fails or, worse, a `must:`-less check binds false
		// and the loop runs to its ceiling.
		//
		// SO THE READ HAPPENS ONCE AND THE RELATION IS APPLIED TO IT, which is the same thing
		// `runCheckMode` does for `e.seen`. The two paths now agree about what a mode means.
		//
		// `scale:` IS THE ENLARGEMENT FOR SMALL TEXT, and it belongs to the READ rather than to the
		// capture. MEASURED on the real 640x480 recovery frame: at scale 6 four rows read, at scale 2
		// only two - so a file that needs a small row to be legible says so where it reads it.
		scale := 3
		if v, ok := toFloat(raw["scale"]); ok && v > 0 {
			scale = int(v)
		}
		words, err := e.readWords(img, rect, scale)
		if err != nil {
			return fail(step, "%v", err)
		}
		if mode == "text" {
			okNow, detail, err = e.judgeWords(step, raw, words)
			if err != nil {
				return err
			}
			// THE WORDS IT READ, AND WHAT IT WANTED. The log prints one of these - the sentence in
			// `detail` names either the words that matched or the ones that did not - and neither
			// tells a reader what the OCR ACTUALLY SAW. Recording the full read is what makes
			// "tesseract misread the pane" distinguishable from "the pane was not there".
			e.recordTextEvidence(words, textWanted(raw), okNow, textMissing(raw, words))
			break
		}
		okNow, detail, err = runCheckMode(step, mode, words, raw)
		if err != nil {
			return fail(step, "%v", err)
		}
	default:
		r := screen.Measure(img, rect)
		got, what := 0.0, ""
		switch mode {
		case "mean_between":
			got, what = r.Mean, "mean brightness"
		case "sat_between":
			got, what = r.Sat, "saturation"
		case "spread_between":
			got, what = r.Spread, "spread"
		default:
			return fail(step, "`mode: %s` is not a measurement of a frame; the ones this build "+
				"has are like, mean_between, sat_between, spread_between, text, contains, "+
				"matches and equals", mode)
		}
		lo, hi, err := modeRange(raw)
		if err != nil {
			return fail(step, "%v", err)
		}
		okNow = got >= lo && got <= hi
		detail = fmt.Sprintf("%s %.4f, wanted %.4f..%.4f", what, got, lo, hi)
	}

	if e.facts == nil {
		e.facts = make(map[string]bool)
	}
	e.facts[name] = okNow
	e.Driver.Log(fmt.Sprintf("  %s: %s = %v (%s)", step, name, okNow, detail))
	// `must: true` MAKES THE ANSWER A VERDICT. Without it a check binds a boolean for an `if:` or a
	// `loop: until:` to read - and a file whose check nothing reads is a file whose assertion cannot
	// fail. MEASURED on run 44280999011: `matched = false` in the log, `44/44` in the status.
	if must, _ := raw["must"].(bool); must && !okNow {
		return fail(step, "%s must be true and is false: %s", name, detail)
	}
	return nil
}

// rectOfCheck reads a `check:`'s own region.
func rectOfCheck(raw map[string]any) (screen.Rect, error) {
	rectRaw, ok := raw["rect"]
	if !ok {
		rectRaw, ok = raw["region"]
	}
	if !ok {
		return screen.Rect{}, fmt.Errorf("a `check:` of a frame needs `rect:` - the region to " +
			"measure, as a fraction (`bottom 4.5%%`, `middle`) or `[x0, y0, x1, y1]` with `rect_at:`")
	}
	at, _ := pairOfMapping(raw, "rect_at")
	if at == ([2]int{}) {
		at, _ = pairOfMapping(raw, "region_at")
	}
	rect, err := parseRectAt(rectRaw, at)
	if err != nil {
		return screen.Rect{}, err
	}
	return rect, nil
}

// similarityInRect compares one region of a frame to a reference image.
//
// THE REGION IS CROPPED FROM BOTH, so a reference may be a full screen and the question about a band
// of it - which is the case a single `rect:` could not express before.
func (e *Executor) similarityInRect(step, ref string, frame image.Image, rect screen.Rect) (float64, error) {
	path := ref
	if !filepath.IsAbs(path) {
		beside := filepath.Join(filepath.Dir(e.File.Path), ref)
		if fileExists(beside) {
			path = beside
		}
	}
	reference, err := screen.Load(path)
	if err != nil {
		return 0, fail(step, "reading the reference %q: %v", ref, err)
	}
	fc, err := Crop(frame, rect)
	if err != nil {
		return 0, fail(step, "%v", err)
	}
	rc, err := Crop(reference, rect)
	if err != nil {
		return 0, fail(step, "%v", err)
	}
	diff, err := screen.Compare(fc, rc, 8)
	if err != nil {
		return 0, fail(step, "%v", err)
	}
	return 1 - diff.Changed, nil
}

// judgeWords applies `words:`/`without:` to a word list, the same rules `check:` uses on a reading.
func (e *Executor) judgeWords(step string, raw map[string]any, words []string) (bool, string, error) {
	if sets, ok := raw["words"]; ok {
		alts, err := wordSetsOfValue(sets)
		if err != nil {
			return false, "", fail(step, "%v", err)
		}
		for _, set := range alts {
			if says(words, set) {
				return true, fmt.Sprintf("read %q", strings.Join(set, " ")), nil
			}
		}
		return false, fmt.Sprintf("none of %s in %q", describeSets(alts), truncateWords(words, 10)), nil
	}
	if without, ok := raw["without"]; ok {
		want := stringsToSlice(without)
		for _, w := range want {
			for _, have := range words {
				if strings.EqualFold(have, w) {
					return false, fmt.Sprintf("%q is on screen and the check said it would not be", w), nil
				}
			}
		}
		return true, "none of the excluded words are present", nil
	}
	return false, "", fail(step, "`mode: text` needs `words:` (any of these word-sets) or `without:`")
}

// wordSetsOfValue reads `[[a, b], [c]]` or `[a, b]` as a list of word-sets.
func wordSetsOfValue(v any) ([][]string, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("`words:` must be a list")
	}
	var out [][]string
	for _, item := range list {
		switch t := item.(type) {
		case string:
			out = append(out, []string{t})
		case []any:
			out = append(out, stringsToSlice(t))
		default:
			return nil, fmt.Errorf("`words:` items must be strings or lists of strings")
		}
	}
	return out, nil
}

func stringsToSlice(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, item := range list {
		if str, ok := item.(string); ok {
			out = append(out, str)
		}
	}
	return out
}

// textWanted is the words a text condition asked for, as one flat list for the journal.
func textWanted(raw map[string]any) []string {
	var out []string
	if sets, ok := raw["words"]; ok {
		if alts, err := wordSetsOfValue(sets); err == nil {
			for _, set := range alts {
				out = append(out, strings.Join(set, " "))
			}
		}
	}
	if without, ok := raw["without"]; ok {
		for _, w := range stringsToSlice(without) {
			out = append(out, "!"+w)
		}
	}
	return out
}

// textMissing names the wanted words that the read did not contain.
//
// IT IS COMPUTED FOR THE REPORT, not for the verdict: the verdict already happened in `judgeWords`.
// A reader looking at a failed text check needs to know WHICH word was absent, because "none of
// welcome/install matched" and "it read 'welcome' but not 'install'" are different problems.
func textMissing(raw map[string]any, words []string) []string {
	var out []string
	have := map[string]bool{}
	for _, w := range words {
		have[strings.ToLower(w)] = true
	}
	if sets, ok := raw["words"]; ok {
		if alts, err := wordSetsOfValue(sets); err == nil {
			for _, set := range alts {
				if says(words, set) {
					return nil
				}
				for _, w := range set {
					if !have[strings.ToLower(w)] {
						out = append(out, w)
					}
				}
			}
		}
	}
	return out
}
