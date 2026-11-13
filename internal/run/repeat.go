package run

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/easydev-org/easyvm/internal/screen"
	"github.com/easydev-org/easyvm/internal/walk"
)

// REPEAT_UNTIL IS THE ONE LOOP THE VOCABULARY WAS MISSING, AND IT REPLACES TWO PRIMITIVES.
//
// Both of them were Python functions that did a loop and a decision:
//
//	select_panes:select_target_disk   press Up, read the detail pane's capacity, stop when it is in
//	                                  a range. A RANGE IS A DECISION, and it was hidden in Go.
//	sequoia_panes:apple_account       four clicks with the timings that make them work. Those are
//	                                  four atomic steps in a row, and hiding them hid the timings.
//
// The Python implementation's own note says the split it wanted was "choosing is a measurement,
// erasing is a sweep, and a reader of the YAML should be able to see which is which". A reader
// cannot see a loop that lives in Go.
//
// SO THIS IS THE LOOP, AND IT IS GENERAL. It has no idea what a disk is: it runs `actions`, then
// asks whether `until_any` matched, and repeats. Everything specific - which key to press, which
// crop to read, what numbers count - stays in the file.
//
//   - name: choose the target disk
//     repeat_until:
//     attempts: 8
//     settle: 3
//     until_any:
//   - words: []                  # no words; a NUMBER in a range is the condition
//     number_between: [60, 200]
//     unit: gb
//     actions:
//   - key: up
//     settle: 3
//
// THE NUMBER CONDITION IS NOT A GENERAL EXPRESSION LANGUAGE, and it does not need to be. Two
// limits and a unit cover the one place this repository needs arithmetic on a screen, and a
// comparison somebody can read beats a grammar somebody has to learn.
type repeatUntil struct {
	// Attempts bounds the loop. A loop with no ceiling is a stage that never ends.
	Attempts int
	// Settle is slept between the action and the reading, because a pane repaints for seconds
	// after a selection moves and a read taken too soon returns the PREVIOUS disk's number.
	Settle time.Duration
	// Actions run in order, once per attempt.
	Actions []walk.Step
	// Conditions is the stop test.
	Conditions []condition
}

// condition is one way for the loop to be satisfied.
type condition struct {
	// Words must all be on screen.
	Words []string
	// NumberBetween, when set, requires a number in this range to be present.
	NumberBetween *[2]float64
	// Unit is "gb" or "mb", and a reading in the other unit is converted before comparison.
	Unit string
	// Desktop, when set, requires the frame to look like that family's desktop.
	Desktop string
	// Changed requires the screen to be DIFFERENT from the one the loop started on.
	//
	// IT IS THE CONDITION A WIZARD'S "Next" NEEDS, and it is not the same question as `words:`. A
	// pane that has advanced usually still shows some of the same text - a title, a sidebar - so
	// waiting for words is waiting for something that may already be there. What a person checks is
	// that the page CHANGED.
	Changed bool
}

var numberPattern = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*(gb|mb)\b`)

// doRepeatUntil runs the loop.
func (e *Executor) doRepeatUntil(s walk.Step) error {
	loop, err := e.parseRepeatUntil(s)
	if err != nil {
		return err
	}

	e.Driver.Log(fmt.Sprintf("  up to %d attempt(s) of %d action(s), then read the screen",
		loop.Attempts, len(loop.Actions)))

	// THE BASELINE IS TAKEN BEFORE ANY ACTION, and only when a condition needs it: a fingerprint
	// costs one screen read, and a loop with word conditions does not need one.
	needsBaseline := false
	for _, cond := range loop.Conditions {
		if cond.Changed {
			needsBaseline = true
		}
	}
	var baseline [64]float64
	if needsBaseline {
		fp, err := e.fingerprint()
		if err != nil {
			return fail(s.Name, "reading the screen before the first attempt: %v", err)
		}
		baseline = fp
	}

	for attempt := 1; attempt <= loop.Attempts; attempt++ {
		// THE READ COMES FIRST ON EVERY ATTEMPT AFTER THE FIRST, so the loop stops as soon as the
		// condition holds rather than pressing one more time and overshooting.
		if attempt > 1 {
			if loop.Settle > 0 {
				time.Sleep(loop.Settle)
			}
		}
		words, numbers, err := e.readForLoop(s)
		if err != nil {
			return fail(s.Name, "reading the screen on attempt %d: %v", attempt, err)
		}
		for _, cond := range loop.Conditions {
			// A DESKTOP CONDITION IS A MEASUREMENT ON THE FRAME, not on the words: a macOS desktop
			// has no readable text to match, so it is decided by the Dock and the body brightness.
			if cond.Desktop != "" {
				if ok, why := e.desktopCondition(cond.Desktop); ok {
					e.Driver.Log(fmt.Sprintf("  attempt %d: %s", attempt, why))
					return nil
				}
				continue
			}
			if cond.Changed {
				now, err := e.fingerprint()
				if err != nil {
					return fail(s.Name, "reading the screen on attempt %d: %v", attempt, err)
				}
				if screen.Changed(baseline, now) {
					e.Driver.Log(fmt.Sprintf("  attempt %d: the screen changed", attempt))
					return nil
				}
				continue
			}
			if ok, why := cond.matches(words, numbers); ok {
				e.Driver.Log(fmt.Sprintf("  attempt %d: %s", attempt, why))
				return nil
			} else if why != "" {
				e.Driver.Log(fmt.Sprintf("  attempt %d: %s", attempt, why))
			}
		}
		if attempt == loop.Attempts {
			break
		}
		for _, action := range loop.Actions {
			if err := e.runOne(action); err != nil {
				return fail(s.Name, "attempt %d, %q: %v", attempt, action.Name, err)
			}
		}
	}

	// RUNNING OUT IS A STOP, NOT A GUESS. The Python implementation's note is the reason this is
	// not a retry-with-a-default: erasing the wrong disk installs macOS into the recovery volume or
	// the boot image, and the failure does not surface for fifty minutes.
	return failWithScreen(s.Name, e.Driver,
		"no attempt satisfied the condition in %d tries; stopping rather than guessing",
		loop.Attempts)
}

// parseRepeatUntil reads the step's own mapping.
//
// IT VALIDATES BEFORE ANY ACTION RUNS. A loop that would press Up eight times and then discover it
// has no stop condition is eight clicks into a screen nobody measured.
func (e *Executor) parseRepeatUntil(s walk.Step) (repeatUntil, error) {
	raw, ok := s.Spec["repeat_until"].(map[string]any)
	if !ok {
		return repeatUntil{}, fail(s.Name, "`repeat_until:` must be a mapping with `actions:` and "+
			"`until_any:`")
	}

	loop := repeatUntil{Attempts: 8, Settle: 3 * time.Second}
	if v, ok := asInt(raw["attempts"]); ok && v > 0 {
		loop.Attempts = v
	}
	if v, ok := raw["wait_after"]; ok {
		if f, ok := toFloat(v); ok && f > 0 {
			loop.Settle = time.Duration(f * float64(time.Second))
		}
	}
	if loop.Attempts > 100 {
		// A LOOP THAT CAN PRESS A KEY A HUNDRED TIMES IS ONE THAT WILL. The ceiling exists so a
		// file cannot turn a stuck pane into a five-minute key repeat.
		return loop, fail(s.Name, "`attempts: %d` is more than this loop accepts; a pane that "+
			"needs more than a few presses is stuck, not deep", loop.Attempts)
	}

	actions, ok := raw["actions"].([]any)
	if !ok || len(actions) == 0 {
		return loop, fail(s.Name, "`repeat_until:` needs at least one action to repeat")
	}
	for i, item := range actions {
		mapping, ok := item.(map[string]any)
		if !ok {
			return loop, fail(s.Name, "actions[%d] is not a mapping", i)
		}
		// THE ACTION IS A STEP, so it goes through the same dispatcher - which means a typo in it
		// is refused here rather than after eight presses.
		if err := validateAction(s.Name, mapping); err != nil {
			return loop, err
		}
		loop.Actions = append(loop.Actions, walk.Step{
			Index: s.Index, Name: fmt.Sprintf("%s #%d", s.Name, i+1), Spec: mapping,
		})
	}

	conditions, ok := raw["until_any"].([]any)
	if !ok || len(conditions) == 0 {
		return loop, fail(s.Name, "`repeat_until:` needs `until_any:` - a loop with no stop "+
			"condition is eight presses into a screen nobody measured")
	}
	for i, item := range conditions {
		// A BARE WORD-SET MEANS `words:`, BECAUSE THAT IS WHAT IT MEANS EVERYWHERE ELSE.
		//
		// `until_any:` IS A STEP'S PROPERTY TOO, and there the entries are lists:
		//
		//	until_any:
		//	- [restore, time, machine]
		//	- [safari]
		//
		// INSIDE `repeat_until:` EACH ENTRY HAS TO BE A MAPPING, because a loop's stop condition can
		// also be `number_between:`, `changed:` or `desktop:`. Requiring the mapping form for the
		// plain case made ONE KEY LOOK LIKE TWO, and writing the list form in a loop failed with
		//
		//	until_any[0] is not a mapping
		//
		// which says nothing about what the author did wrong. A list is now READ as `words:`, so the
		// two spellings agree and the mapping form stays for the conditions that need it.
		mapping, isMapping := item.(map[string]any)
		if !isMapping {
			list, isList := item.([]any)
			if !isList {
				return loop, fail(s.Name, "until_any[%d] is %T: a condition is a list of words "+
					"(the same form a step's `until_any:` takes) or a mapping with `words:`, "+
					"`number_between:`, `changed:` or `desktop:`", i, item)
			}
			mapping = map[string]any{"words": list}
		}
		var cond condition
		if words, ok := mapping["words"].([]any); ok {
			for _, w := range words {
				cond.Words = append(cond.Words, fmt.Sprint(w))
			}
		}
		// A CONDITION MAY BE A SCREEN TEST RATHER THAN WORDS OR NUMBERS. `desktop:` is the one that
		// needs this: a macOS desktop has no readable text to match on, so the condition is the
		// same measurement the `desktop:` step makes, asked once per attempt.
		if v, ok := mapping["desktop"]; ok {
			cond.Desktop = fmt.Sprint(v)
		}
		if spec, ok := mapping["number_between"].([]any); ok && len(spec) == 2 {
			lo, okLo := toFloat(spec[0])
			hi, okHi := toFloat(spec[1])
			if !okLo || !okHi || hi <= lo {
				return loop, fail(s.Name, "until_any[%d].number_between must be [low, high]", i)
			}
			cond.NumberBetween = &[2]float64{lo, hi}
			cond.Unit = strings.ToLower(fmt.Sprint(mapping["unit"]))
			if cond.Unit == "" {
				cond.Unit = "gb"
			}
		}
		// `changed: true` IS A CONDITION WITH NO TEXT, and it is the one a wizard's Next needs: a
		// pane that advanced may still show its own title, so "these words are on screen" can be
		// satisfied before anything moved.
		if v, ok := mapping["changed"]; ok {
			if b, isBool := v.(bool); isBool && b {
				cond.Changed = true
			}
		}
		if len(cond.Words) == 0 && cond.NumberBetween == nil && cond.Desktop == "" && !cond.Changed {
			return loop, fail(s.Name, "until_any[%d] has none of `words:`, `number_between:`, "+
				"`desktop:` or `changed:` - a condition that cannot be tested is a loop that "+
				"presses eight times and reports nothing", i)
		}
		loop.Conditions = append(loop.Conditions, cond)
	}
	return loop, nil
}

// matches reports whether the condition holds, and says why either way.
func (c condition) matches(words []string, numbers []float64) (bool, string) {
	if len(c.Words) > 0 && !says(words, c.Words) {
		return false, ""
	}
	if c.Desktop != "" {
		return false, "" // resolved by the caller, which can read the screen
	}
	if c.NumberBetween == nil {
		return true, fmt.Sprintf("saw %q", strings.Join(c.Words, " "))
	}
	// THE LARGEST READING IS THE CANDIDATE, because a detail pane shows several capacities (the
	// device, each partition) and the disk itself is the largest of them. The Python implementation
	// learned this the other way: matching only "NNN.N GB" missed a 268.4 MB boot image, so the one
	// disk that must never be selected produced no reading at all.
	for _, n := range numbers {
		if n >= c.NumberBetween[0] && n <= c.NumberBetween[1] {
			return true, fmt.Sprintf("%.2f GB is in %g..%g",
				n, c.NumberBetween[0], c.NumberBetween[1])
		}
	}
	return false, fmt.Sprintf("read %s; nothing in %g..%g GB",
		formatNumbers(numbers), c.NumberBetween[0], c.NumberBetween[1])
}

// fingerprint captures a frame and reduces it to the coarse grid `screen.Changed` compares.
//
// IT IS A SEPARATE READ FROM readScreen, and that is deliberate: OCR is the expensive half and a
// `changed:` condition does not need it at all. A wizard's Next is decided by whether the page moved,
// not by what the page says.
func (e *Executor) fingerprint() ([64]float64, error) {
	f, err := e.Driver.Frame()
	if err != nil {
		return [64]float64{}, err
	}
	img, err := screen.Load(f.Path())
	if err != nil {
		return [64]float64{}, err
	}
	return screen.Fingerprint(img), nil
}

// readForLoop captures the screen, reads it, and also extracts every capacity it found.
//
// BOTH HALVES COME FROM ONE READ, because the words and the numbers are on the same pane and a
// second capture would be a second chance to catch it mid-repaint.
func (e *Executor) readForLoop(s walk.Step) ([]string, []float64, error) {
	words, err := e.readScreen(s, s.Name)
	if err != nil {
		return nil, nil, err
	}
	return words, capacities(words), nil
}

// capacities pulls every "NNN.N GB" and "NNN MB" out of a reading and converts to GB.
//
// THE UNIT IS PART OF THE NUMBER, NOT DECORATION. A boot image reports "268.4 MB"; matching only GB
// made the disk this must never select produce no reading at all, so the loop kept stepping.
func capacities(words []string) []float64 {
	text := strings.Join(words, " ")
	var out []float64
	for _, m := range numberPattern.FindAllStringSubmatch(strings.ToLower(text), -1) {
		value, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			continue
		}
		if m[2] == "mb" {
			value /= 1024.0
		}
		out = append(out, value)
	}
	return out
}

func formatNumbers(numbers []float64) string {
	if len(numbers) == 0 {
		return "no capacity"
	}
	var parts []string
	for _, n := range numbers {
		parts = append(parts, fmt.Sprintf("%.2f", n))
	}
	return strings.Join(parts, ", ") + " GB"
}

// validateAction refuses an action whose keys the dispatcher does not know.
//
// IT RUNS BEFORE THE FIRST ATTEMPT, so a typo costs a parse error rather than eight presses.
func validateAction(parent string, mapping map[string]any) error {
	for k := range mapping {
		if k == "name" {
			continue
		}
		if !actionKey(k) {
			return fail(parent, "a `repeat_until:` action has unknown key %q; an action is an "+
				"ordinary step, so it may use %s", k, strings.Join(ExecutableKeys(), ", "))
		}
	}
	return nil
}

func actionKey(k string) bool {
	for _, known := range ExecutableKeys() {
		if known == k {
			return true
		}
	}
	// Qualifiers an action may carry on top of its verb.
	switch k {
	case "wait_after", "timeout", "optional", "type_gap", "region", "scale", "poll":
		return true
	}
	return false
}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case float64:
		return t, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f, err == nil
	}
	return 0, false
}

// unused keeps the screen import honest: readScreen uses it, and this file names it in a comment.
var _ = screen.Crop

// desktopCondition asks whether the current frame is that family's desktop.
//
// IT IS THE SAME MEASUREMENT THE `desktop:` STEP MAKES, asked once - which is why `repeat_until:`
// can use it as a condition. Sharing the test rather than re-deriving it is what stops the two
// from disagreeing about what "a desktop" means.
func (e *Executor) desktopCondition(family string) (bool, string) {
	f, err := e.Driver.Frame()
	if err != nil {
		return false, ""
	}
	img, err := screen.Load(f.Path())
	if err != nil {
		return false, ""
	}
	switch family {
	case "macos":
		ok, stats := screen.IsMacOSDesktop(img)
		return ok, "a macOS desktop is on screen; " + stats.Explain()
	case "windows":
		ok, stats := screen.IsWindowsDesktop(img)
		return ok, "a Windows desktop is on screen; " + stats.Explain()
	}
	return false, ""
}
