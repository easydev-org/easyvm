package run

import (
	"fmt"
	"os"
	"path/filepath"
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
		return fail(s.Name, "`check:` must be a mapping: name, from, and says/says_any")
	}
	name, _ := raw["name"].(string)
	if strings.TrimSpace(name) == "" {
		return fail(s.Name, "`check:` must name the boolean it produces: "+
			"`check: {name: done, from: page, says_any: [...]}`")
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

	// THREE KINDS OF COMPARISON, AND A CHECK MAY USE ONE OF THEM:
	//
	//	says: / says_any:      the reading holds these words
	//	number_between: [a, b] the reading holds a number in this range
	//	size_between: [a, b]   the same, in GB, for a disk's capacity
	//
	// THE NUMBER FORM IS WHAT MAKES A CHOICE EXPRESSIBLE. "Click the first disk, read its capacity,
	// and use it only if it is between 60 and 200 GB" is a decision about arithmetic, and a word list
	// cannot say it - `says: [200]` would match any reading that happened to contain the digits,
	// including `1,200 GB` and `20.0 GB`.
	var sets [][]string
	if one := wordsOfMapping(raw, "says"); len(one) > 0 {
		sets = append(sets, one)
	}
	for _, set := range wordSetsOfMapping(raw, "says_any") {
		sets = append(sets, set)
	}
	lo, hasLo := numberOf(raw["number_between"], 0)
	hi, hasHi := numberOf(raw["number_between"], 1)
	sizeLo, hasSizeLo := numberOf(raw["size_between"], 0)
	sizeHi, hasSizeHi := numberOf(raw["size_between"], 1)
	isNumber := hasLo && hasHi
	isSize := hasSizeLo && hasSizeHi
	if len(sets) == 0 && !isNumber && !isSize {
		return fail(s.Name, "`check:` needs `says:`, `says_any:`, `number_between:` or "+
			"`size_between:` - a comparison with no target")
	}

	okNow := false
	var hit string
	for _, set := range sets {
		if says(words, set) {
			okNow = true
			hit = strings.Join(set, " ")
			break
		}
	}
	if !okNow && (isNumber || isSize) {
		wantLo, wantHi := lo, hi
		if isSize {
			wantLo, wantHi = sizeLo, sizeHi
		}
		// `capacities` already converts MB to GB, so both units a pane might print are comparable.
		found := capacities(words)
		for _, n := range found {
			if n >= wantLo && n <= wantHi {
				okNow = true
				hit = fmt.Sprintf("%.1f GB", n)
				break
			}
		}
		if !okNow {
			hit = "nothing in range"
			e.Driver.Log(fmt.Sprintf("  %s: capacities read %s; wanted %.0f..%.0f GB",
				s.Name, formatNumbers(found), wantLo, wantHi))
		}
	}
	if e.facts == nil {
		e.facts = make(map[string]bool)
	}
	e.facts[name] = okNow
	if okNow {
		e.Driver.Log(fmt.Sprintf("  %s: %s = true (read %q)", s.Name, name, hit))
	} else {
		e.Driver.Log(fmt.Sprintf("  %s: %s = false (read %q, wanted %s)", s.Name, name,
			truncateWords(words, 10), describeSets(sets)))
	}
	return nil
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
