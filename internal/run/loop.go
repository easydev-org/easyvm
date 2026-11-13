package run

import (
	"fmt"
	"time"

	"github.com/easydev-org/easyvm/internal/walk"
)

// `loop:` IS THE THREE PARTS WRITTEN DOWN: an action, a reading, a condition.
//
// THE SHAPE IS THE POINT. `repeat_until:` already existed and it put the reading inside the loop's
// own condition, with the condition's own keys - so a reader had to know that `until_any` there took
// mappings and elsewhere took lists, and the reading's region was inherited from the parent step.
// THE LOOP NOW SAYS WHAT IT DOES, IN THREE NAMED PARTS:
//
//   - name: dismiss the first-login pages
//     loop:
//     max: 12                      # REQUIRED: a loop with no ceiling never ends
//     do:                          # 1. THE ACTION
//   - at: [1464, 194]
//     settle: 3
//     ocr:                         # 2. THE READING - one region, named
//     name: page
//     region: [0, 0, 1920, 1080]
//     until:                       # 3. THE CONDITION - answered from that reading
//     when: page
//     has_any:
//   - [software, updater]
//   - [activities]
//
// EACH PART RUNS THROUGH THE SAME DISPATCHER THE REST OF THE FILE USES, so `do:` entries are ordinary
// steps and the `ocr:`/`until:` parts are the same two steps the file writes at top level. A loop is
// not a second language; it is a scope around three steps.
func (e *Executor) doLoop(s walk.Step) error {
	raw, ok := s.Spec["loop"].(map[string]any)
	if !ok {
		return fail(s.Name, "`loop:` must be a mapping with `max:`, `do:`, `ocr:` and `until:`")
	}
	for k := range raw {
		switch k {
		case "max", "do", "ocr", "until":
		default:
			return fail(s.Name, "`loop:` has unknown key %q; it takes max, do, ocr and until", k)
		}
	}

	max, ok := toFloat(raw["max"])
	if !ok || max <= 0 {
		return fail(s.Name, "`loop:` must say `max:` - a loop with no ceiling presses a key until "+
			"the stage times out")
	}
	if max > 200 {
		return fail(s.Name, "`loop: max: %.0f` is over the ceiling of 200 attempts", max)
	}
	action, ok := raw["do"].([]any)
	if !ok || len(action) == 0 {
		return fail(s.Name, "`loop:` must say `do:` - the action it repeats")
	}
	ocrRaw, ok := raw["ocr"].(map[string]any)
	if !ok {
		return fail(s.Name, "`loop:` must say `ocr:` - the reading its condition is answered from. "+
			"A condition with no reading is a condition that reads the screen itself, which is the "+
			"coupling this shape exists to remove")
	}
	untilRaw, ok := raw["until"].(map[string]any)
	if !ok {
		return fail(s.Name, "`loop:` must say `until:` - the condition that stops it")
	}

	// THE PARTS ARE STEPS, built once and run through the ordinary dispatcher.
	ocrSpec := map[string]any{}
	for k, v := range ocrRaw {
		ocrSpec[k] = v
	}
	ocrStep := walk.Step{Index: s.Index, Name: s.Name + " [ocr]", Spec: ocrSpec}
	if _, err := parseOCRStep(ocrStep); err != nil {
		return err
	}
	untilSpec := map[string]any{}
	for k, v := range untilRaw {
		untilSpec[k] = v
	}
	untilStep := walk.Step{Index: s.Index, Name: s.Name + " [until]", Spec: untilSpec}

	e.Driver.Log(fmt.Sprintf("  loop: up to %.0f attempt(s) of %d action(s), then read %q",
		max, len(action), ocrSpec["name"]))

	for attempt := 1; attempt <= int(max); attempt++ {
		if e.ShouldStop != nil && e.ShouldStop() {
			return fail(s.Name, "stopped after %d attempt(s)", attempt-1)
		}

		// 1. THE ACTION.
		for i, item := range action {
			spec, ok := item.(map[string]any)
			if !ok {
				return fail(s.Name, "`loop.do[%d]` is not a mapping", i)
			}
			sub := walk.Step{
				Index: s.Index,
				Name:  fmt.Sprintf("%s [%d.%d]", s.Name, attempt, i+1),
				Spec:  spec,
			}
			if err := e.runOne(sub); err != nil {
				return err
			}
		}

		// 2. THE READING. Its name is stable across attempts, so each one overwrites the last - the
		// text file always holds the frame the decision was made from.
		if err := e.doOCR(ocrStep); err != nil {
			return err
		}

		// 3. THE CONDITION, answered from the reading and not from a second capture.
		err := e.doWhen(untilStep)
		if err == nil {
			e.Driver.Log(fmt.Sprintf("  loop: satisfied after %d attempt(s)", attempt))
			return nil
		}
		if attempt == int(max) {
			return fail(s.Name, "the loop ran %.0f attempt(s) and the condition never held: %v",
				max, err)
		}
		// A settlng pause between attempts, because a pane repaints for seconds after a press and a
		// second read taken too soon describes the PREVIOUS pane.
		time.Sleep(2 * time.Second)
	}
	return nil
}
