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
		case "max", "do", "see", "check", "until", "every", "clock", "on":
		default:
			return fail(s.Name, "`loop:` has unknown key %q; it takes max, do, ocr, check, until, "+
				"every and on", k)
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
	// `see:` IS THE READING, and `ocr:` is the name every file wrote before the two were merged:
	// `see: {of: [text]}` is exactly what `ocr:` did, so one key covers a word list and a set of
	// numbers. The old spelling is read so a file that has not been migrated runs.
	//
	// THE KEY IT ARRIVED UNDER IS KEPT, because the two spellings are parsed by two functions with
	// different vocabularies: `see:` takes `of: [text]` and binds a word list, `ocr:` takes a region
	// and nothing else. MEASURED on run 44083524097, after the mapping was handed over as `ocr:`:
	//
	//	wait for the installer window [ocr]: `ocr:` has unknown key "of"
	//
	// - on a loop whose `see:` was correct. Canonicalising the key LOST information rather than
	// gaining it, so the loop now runs the same step the file wrote.
	readingKey := "see"
	ocrRaw, ok := raw["see"].(map[string]any)
	if !ok {
		readingKey = "ocr"
		ocrRaw, ok = raw["ocr"].(map[string]any)
	}
	if !ok {
		return fail(s.Name, "`loop:` must say `see:` - the reading its condition is answered from. "+
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

	// THE PARTS ARE STEPS, BUILT BY `loopParts` AND RUN THROUGH THE ORDINARY DISPATCHER.
	//
	// IT IS A FUNCTION RATHER THAN FOUR LINES HERE so that a test can call THE SAME CODE that runs.
	// The test that exists built its own correctly-shaped sub-step and asserted on that, which is a
	// test of the test: when this function handed `parseOCRStep` the wrong key, the test still
	// passed and every loop in every walkthrough failed in a real run. MEASURED on 44078798458.
	readingStep, checkStep, untilStep := loopParts(s, readingKey, ocrRaw, checkRaw, untilRaw)
	// THE READING IS VALIDATED BY RUNNING ITS OWN PARSER PATH, so a `see:` mapping is checked
	// against `see:`'s vocabulary and an `ocr:` mapping against `ocr:`'s. Both answer "is this a
	// reading this vocabulary can take", and the alternative is a second copy of those rules here.
	if err := e.checkReading(readingStep); err != nil {
		return err
	}

	// `every:` IS A CLOCK, NOT A READING, AND THAT IS WHY IT EXISTS.
	//
	// MEASURED ACROSS FOUR CI RUNS (easydev-org/easyvm 44150638949, 44190274785, 44227587443): a macOS
	// 26 install writes its disk, then the screen goes black - or, on another host, shows OpenCore's
	// boot picker whose labels OCR reads as `@® cos inner en poset nvFan` - and NOTHING on that screen
	// means "the loop is over". Every screen-based test I wrote was a guess about that frame:
	//
	//	`says_any` on the Setup Assistant panes   -> 0 word(s) matches none of them, 270 attempts burned
	//	`matches: '^$'`                            -> catches the 0-word frame, misses the OCR noise
	//	`matches: '^.{0,3}$'`                      -> catches both, still a guess about a host I cannot see
	//
	// SO THIS LOOP CAN ALSO END BY THE CLOCK: `every: <seconds>` runs the `on:` actions once that long
	// has passed, whatever the screen says. It is the honest shape of "this pane has had long enough",
	// and it does not pretend to recognise a frame the walkthrough has never seen.
	//
	// THE ACTION RUNS ONCE, NOT ONCE PER ATTEMPT, and the clock starts when the loop does - a loop
	// that reset every sixty seconds would boot the installer forever.
	every := 0.0
	var timedAction []any
	if v, ok := raw["every"]; ok {
		f, ok := toFloat(v)
		if !ok || f <= 0 {
			return fail(s.Name, "`loop: every:` must be a positive number of seconds")
		}
		every = f
		var ok2 bool
		timedAction, ok2 = raw["clock"].([]any)
		if !ok2 || len(timedAction) == 0 {
			return fail(s.Name, "`loop: every:` needs `clock:` - the action to run once when the time "+
				"has passed. A clock with nothing to do is a loop that never ends")
		}
	} else if _, ok := raw["clock"]; ok {
		return fail(s.Name, "`loop: clock:` needs `every:` - say how long to wait before running it")
	}
	started := time.Now()
	fired := false

	e.Driver.Log(fmt.Sprintf("  loop: up to %.0f attempt(s) of %d action(s), then read %q",
		max, len(action), ocrRaw["name"]))

	for attempt := 1; attempt <= int(max); attempt++ {
		if e.ShouldStop != nil && e.ShouldStop() {
			return fail(s.Name, "stopped after %d attempt(s)", attempt-1)
		}

		// 0. THE CLOCK, BEFORE THE ACTION, so a loop whose screen never changes still moves.
		if every > 0 && !fired && time.Since(started) >= time.Duration(every*float64(time.Second)) {
			e.Driver.Log(fmt.Sprintf("  loop: %.0fs have passed, so the `clock:` actions run", every))
			for i, item := range timedAction {
				spec, ok := item.(map[string]any)
				if !ok {
					return fail(s.Name, "`loop.clock[%d]` is not a mapping", i)
				}
				sub := walk.Step{
					Index: s.Index,
					Name:  fmt.Sprintf("%s [on %d.1]", s.Name, i+1),
					Spec:  spec,
				}
				if err := e.runOne(sub); err != nil {
					return err
				}
			}
			fired = true
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
		if err := e.runOne(readingStep); err != nil {
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

// loopParts assembles a loop's three sub-steps, each carrying its mapping under the key its own
// parser reads.
//
// THE KEYS ARE `ocr`, `check` AND `until_fact`, and they are NOT the keys the file writes: the file
// says `see:`, `check:` and `until: {fact: ...}` because that is the vocabulary, and these are the
// three steps that vocabulary is turned into. Getting one of them wrong produces an error message
// that names the walkthrough, which is why this is a named function with a caller-shaped test.
func loopParts(s walk.Step, readingKey string, ocrRaw, checkRaw, untilRaw map[string]any) (readingStep, checkStep, untilStep walk.Step) {
	readingStep = walk.Step{Index: s.Index, Name: s.Name + " [" + readingKey + "]",
		Spec: map[string]any{readingKey: ocrRaw}}
	checkStep = walk.Step{Index: s.Index, Name: s.Name + " [check]",
		Spec: map[string]any{"check": checkRaw}}
	untilStep = walk.Step{Index: s.Index, Name: s.Name + " [until]",
		Spec: map[string]any{"until_fact": untilRaw["fact"]}}
	return readingStep, checkStep, untilStep
}

// checkReading validates a loop's reading WITHOUT capturing a frame.
//
// IT ASKS THE PARSER, which is the same code the step will hit when it runs - so a mapping that passes
// here cannot be refused a second later for a reason the loop did not mention. `see:` validates by its
// own rules and `ocr:` through `parseOCRStep`, because a loop may hold either.
func (e *Executor) checkReading(step walk.Step) error {
	if _, ok := step.Spec["see"]; ok {
		if seeName(step) == "" {
			return fail(step.Name, "`see:` must name the frame: `see: {name: shot}`")
		}
		return refuseLegacySee(step)
	}
	_, err := parseOCRStep(step)
	return err
}
