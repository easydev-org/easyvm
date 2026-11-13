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
		case "max", "do", "ocr", "check", "until":
		default:
			return fail(s.Name, "`loop:` has unknown key %q; it takes max, do, ocr, check and until", k)
		}
	}

	max, ok := toFloat(raw["max"])
	if !ok || max <= 0 {
		return fail(s.Name, "`loop:` must say `max:` - a loop with no ceiling presses a key until "+
			"the stage times out")
	}
	// THE CEILING IS TIME, NOT ATTEMPTS, AND MY FIRST NUMBER WAS A GUESS THAT BROKE AN INSTALL.
	//
	// MEASURED: ubuntu-install reached step 26 of 29 and failed with
	//
	//	the installer finishes: `loop: max: 270` is over the ceiling of 200 attempts
	//
	// - 270 x 10 seconds is FORTY-FIVE MINUTES, which is an ordinary Ubuntu install. A ceiling of 200
	// attempts said "no loop may take longer than thirty-three minutes", which is not a fact about
	// this repository; it was a number small enough to feel safe.
	//
	// SO THE BOUND IS THE STEP'S OWN `timeout:`, and `max` is bounded by that: a loop cannot ask to
	// run longer than the step is willing to wait. 24 hours is the ceiling on the ceiling - a guard
	// against a typo that would otherwise run until the job is killed, not a judgement about how long
	// an install takes.
	if max > 24*60*60 {
		return fail(s.Name, "`loop: max: %.0f` is absurd; the ceiling is one day of attempts", max)
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
		return fail(s.Name, "`loop:` must say `until: {fact: <name>}` - the boolean a `check:` "+
			"step produced")
	}
	if _, ok := untilRaw["fact"].(string); !ok {
		return fail(s.Name, "`until:` takes `fact: <name>`, the boolean a `check:` step produced. "+
			"IT DOES NOT TAKE A TARGET: the comparison belongs to `check:`, so a loop's stop test "+
			"cannot also decide what the reading means")
	}
	checkRaw, ok := raw["check"].(map[string]any)
	if !ok {
		return fail(s.Name, "`loop:` must say `check:` - the comparison that turns its reading "+
			"into a boolean. Three parts: do:, ocr:, check:, and `until:` tests the boolean")
	}

	// THE PARTS ARE STEPS, built once and run through the ordinary dispatcher.
	// THE SUB-STEP CARRIES THE `ocr:` KEY, NOT ITS CONTENTS.
	//
	// `parseOCRStep` reads `s.Spec["ocr"]`, because a top-level `ocr:` step is written
	//
	//	- name: read the pane
	//	  ocr: {name: screen, region: [...]}
	//
	// so the mapping has to sit UNDER that key. The first version copied the contents straight into
	// the spec, so the parser looked for an `ocr` key that was not there and every loop in every
	// walkthrough failed on its first attempt with
	//
	//	wait for the installer window [ocr]: `ocr:` must be a mapping
	//
	// WHICH READ AS A MISTAKE IN THE FILE, and it was a mistake in this function.
	ocrStep := walk.Step{Index: s.Index, Name: s.Name + " [ocr]",
		Spec: map[string]any{"ocr": ocrRaw}}
	if _, err := parseOCRStep(ocrStep); err != nil {
		return err
	}
	untilSpec := map[string]any{"until_fact": untilRaw["fact"]}
	untilStep := walk.Step{Index: s.Index, Name: s.Name + " [until]", Spec: untilSpec}
	// THE SAME SHAPE FOR `check:` - its mapping sits under its own key.
	checkStep := walk.Step{Index: s.Index, Name: s.Name + " [check]",
		Spec: map[string]any{"check": checkRaw}}

	e.Driver.Log(fmt.Sprintf("  loop: up to %.0f attempt(s) of %d action(s), then read %q",
		max, len(action), ocrRaw["name"]))

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

		// 2. THE READING -> a TEXT variable and work/ocr/<name>.txt.
		//
		// A READING THAT FAILED IS A FRAME THAT WAS NOT FINISHED, NOT A LOOP THAT SHOULD END.
		//
		// MEASURED ON THE FIRST LOCAL macos-setup THAT REACHED THE MENU:
		//
		//	wait for the recovery menu [ocr]: screendump: frame-000196.ppm was not readable
		//	                                  within 5s: unexpected EOF
		//
		// qemu's `screendump` replies before the PPM write completes, so a capture can land on a
		// half-written file - the driver already waits for the size to settle, and a 640x480 frame
		// still races that. THE OLD `until:` CONTINUED ON A BAD READ, which is why it survived this;
		// the loop returned the error and ended a thirty-minute gate at three minutes.
		//
		// THE ATTEMPT CEILING STILL BOUNDS IT: a guest whose frames never become readable runs out of
		// attempts and fails with the last read error.
		if err := e.doOCR(ocrStep); err != nil {
			e.Driver.Log(fmt.Sprintf("  loop: attempt %d could not read the screen (%v); trying again",
				attempt, err))
			if attempt == int(max) {
				return fail(s.Name, "the loop ran %.0f attempt(s) and never read a frame: %v", max, err)
			}
			time.Sleep(2 * time.Second)
			continue
		}

		// 3. THE COMPARISON -> a BOOLEAN.
		if err := e.doCheck(checkStep); err != nil {
			return err
		}

		// 4. THE TEST -> the boolean, and nothing else.
		err := e.doUntilFact(untilStep)
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
