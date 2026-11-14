package run

import (
	"fmt"
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

	// THE WAIT IS EXPLICIT IN THE FILE AND HONOURED HERE.
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
	region, ok := rectOfMapping(raw, "region")
	if !ok {
		return ocrSpec{}, fail(s.Name, "`ocr:` must name its `region: [x0, y0, x1, y1]`. A read "+
			"without one reads the whole screen, which is how a gate matched the title of the "+
			"window it was waiting to open, and what made 47 of 70 steps read a full 1920x1080 frame")
	}
	spec.Region = region
	at, ok := pairOfMapping(raw, "region_at")
	if !ok {
		return ocrSpec{}, fail(s.Name, "`ocr:` must say `region_at: [width, height]` - the size "+
			"its `region:` was measured at. Without it the crop cannot be scaled for a guest that "+
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
		case "name", "region", "region_at", "scale", "wait":
		default:
			return ocrSpec{}, fail(s.Name, "`ocr:` has unknown key %q; it takes name, region, "+
				"region_at, scale and wait", k)
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
	words, ok := e.seen[from]
	if !ok {
		return fail(s.Name, "`check: from: %s` names a reading no `ocr:` step has made. An `ocr:` "+
			"step has to come first, in file order, with that `name:`", from)
	}

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
	return nil
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
		"contains", "matches"}
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
