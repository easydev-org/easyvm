package run

import (
	"errors"
	"fmt"
	"time"

	"github.com/easydev-org/easyvm/internal/walk"
)

// `repeat:` IS THE ONLY LOOP, AND IT IS A STATEMENT RATHER THAN THREE PARTS.
//
// THE SHAPE:
//
//	step "answer the wizard until the desktop appears"
//	  repeat
//	    do
//	      item "click the password field"
//	        press 960 628
//	        after 2
//	    until
//	      from screen
//	      words [finder]
//	      none [continue, back, later]
//	    max 40
//	    pause 3
//
//   - `do:`     the steps to run before each test (optional; a pure poll omits it)
//   - `until:`  the condition, written with the SAME children an `expect` takes
//   - `max:`    how many attempts (required - a loop with no ceiling never ends)
//   - `pause:`  seconds to wait between attempts (optional, default 2)
//   - `nudge N` an action that fires AT MOST ONCE, N seconds into the loop
//
// IT REPLACES `loop:`/`check:`/`until: {fact:}`, WHICH SAID THE SAME THING IN FOUR PARTS AND NEEDED A
// NAME TO CARRY THE BOOLEAN BETWEEN THEM. A `repeat` binds nothing: the condition IS the test, so
// there is no boolean to name and nothing to look up. MEASURED: of the 230 checks in the old tree,
// only 44 bound a name an `if:` ever read - the other 186 were loop internals with no reader.
func (e *Executor) doRepeat(s walk.Step) error {
	raw, ok := s.Spec["repeat"].(map[string]any)
	if !ok {
		return fail(s.Name, "`repeat:` must be a mapping with `max:` and `until:`")
	}
	for k := range raw {
		switch k {
		case "max", "do", "until", "pause", "nudge", "nudge_after":
		default:
			return fail(s.Name, "`repeat:` has unknown key %q; it takes do, until, max, pause and nudge", k)
		}
	}

	max, ok := toFloat(raw["max"])
	if !ok || max <= 0 {
		return fail(s.Name, "`repeat:` needs `max:` - how many attempts before it gives up. A loop "+
			"with no ceiling never ends")
	}
	// THE CEILING IS THE STEP'S `timeout:` AND NOT A NUMBER OF ITS OWN. A loop of 270 attempts ten
	// seconds apart is forty-five minutes, which is an ordinary install; a fixed attempt count is not
	// a fact about any repository. Twenty-four hours is a guard against a typo, not a judgement.
	if max > 24*60*60 {
		return fail(s.Name, "`repeat: max: %.0f` is absurd; the ceiling is one day of attempts", max)
	}

	// `until:` IS THE CONDITION. It is the same vocabulary `expect` uses, so the reader has one way
	// to ask a question whether the answer ends a loop or fails a step.
	// `until:` IS EITHER A PICTURE CONDITION OR THE NAME OF A MACHINE FACT. The second form makes
	// `repeat until machine_stopped` the same question `wait_until: stopped` asks, with the retry
	// parameters visible instead of implied - see machinefact.go.
	untilText, isName := raw["until"].(string)
	until, isBlock := raw["until"].(map[string]any)
	if !isName && (!isBlock || len(until) == 0) {
		return fail(s.Name, "`repeat:` must say `until:` - either a condition block (`until` then "+
			"`source screen`, `words [done]`) or the name of a machine fact such as "+
			"`machine_stopped`")
	}

	// `do:` IS THE BODY, and a `repeat` that has none is a pure poll.
	action, _ := raw["do"].([]any)

	// `pause:` IS THE SETTLE BETWEEN ATTEMPTS. It has a default because almost every loop wants a
	// few seconds - a pane repaints for seconds after a press, and a second read taken too soon
	// describes the PREVIOUS pane - and the default is named so a file that needs longer can say so.
	pause := 2.0
	if v, ok := raw["pause"]; ok {
		f, ok := toFloat(v)
		if !ok || f < 0 {
			return fail(s.Name, "`repeat: pause:` must be a number of seconds")
		}
		pause = f
	}

	// `nudge N` FIRES AT MOST ONCE, N SECONDS IN. It exists for one measured case: macOS's installer
	// goes idle and the screen never changes on its own, so a loop that only looks can wait forever
	// on a guest that is asleep. The threshold is the argument; the body is the action.
	nudge, _ := raw["nudge"].([]any)
	nudgeAfter := 0.0
	if v, ok := raw["nudge_after"]; ok {
		if f, ok := toFloat(v); ok {
			nudgeAfter = f
		}
	}
	if len(nudge) > 0 && nudgeAfter <= 0 {
		return fail(s.Name, "`repeat: nudge:` needs a number of seconds: `nudge 5400`")
	}
	if nudgeAfter > 0 && len(nudge) == 0 {
		return fail(s.Name, "`repeat: nudge N` needs an action under it")
	}
	started := time.Now()
	fired := false

	e.Driver.Log(fmt.Sprintf("  repeat: up to %.0f attempt(s) of %d action(s)", max, len(action)))

	for attempt := 1; attempt <= int(max); attempt++ {
		if e.ShouldStop != nil && e.ShouldStop() {
			return fail(s.Name, "stopped after %d attempt(s)", attempt-1)
		}

		// 0. THE NUDGE, BEFORE THE ACTION, so a loop whose screen never changes still moves.
		if nudgeAfter > 0 && !fired && time.Since(started) >= time.Duration(nudgeAfter*float64(time.Second)) {
			e.Driver.Log(fmt.Sprintf("  repeat: %.0fs have passed, so the `nudge:` actions run", nudgeAfter))
			for i, item := range nudge {
				spec, ok := item.(map[string]any)
				if !ok {
					return fail(s.Name, "`repeat.nudge[%d]` is not a mapping", i)
				}
				sub := walk.Step{
					Index: s.Index,
					Name:  fmt.Sprintf("%s [nudge %d.%d]", s.Name, attempt, i+1),
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
				return fail(s.Name, "`repeat.do[%d]` is not a mapping", i)
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

		// 2. THE CONDITION. A reading that fails is a frame that was not finished, not a loop that
		// should end: qemu's screendump replies before the write completes, so a capture can
		// land on a half-written file and the next attempt reads it.
		var err error
		if isName {
			var why string
			_, why, err = e.factValue(s, untilText)
			if err == nil && why != "" {
				err = fmt.Errorf("%s", why)
			}
		} else {
			err = e.expectOnce(s, until)
		}
		if err == nil {
			e.Driver.Log(fmt.Sprintf("  repeat: satisfied after %d attempt(s)", attempt))
			return nil
		}
		// A FRAME AT THE WRONG SIZE IS "NOT YET", NOT "NEVER".
		//
		// MEASURED, and it is the failure this replaces: a live medium draws at 800x600 before its
		// session switches to 1920x1080. A comparison against a 1920x1080 reference fails on the
		// SIZE, and a loop that treated that as a verdict gave up on a guest that was about to
		// switch - while the old `desktop:` gate waited for exactly this.
		//
		// THE BUDGET IS NOT THE LOOP'S CEILING. A medium that takes ninety minutes to unpack its ISO
		// may need all of it to draw at all; the switch happens once the panel is up and takes tens
		// of seconds. So a wrong size in the last two minutes of a long loop is still worth waiting
		// for, and the ceiling stays the file's own `max:`.
		if errors.Is(err, errFrameSize) {
			e.Driver.Log(fmt.Sprintf("  repeat: %v - waiting for the guest to switch modes", err))
		} else if attempt == int(max) {
			return fail(s.Name, "the repeat ran %.0f attempt(s) and the condition never held: %v",
				max, err)
		}
		// THE LOOP DOES NOT CLICK THE SCREEN. A LOOP THAT NUDGED WOULD CHANGE THE THING IT IS
		// MEASURING, and most of them are asking a question whose answer is "nothing has changed yet":
		// a menu that has not drawn, a pane that has not closed, an installer that is still extracting.
		//
		// THE GUEST THAT BLANKS ITS OWN SCREEN IS THE EXCEPTION, and it says so in its own file: a
		// step with `input` in the loop's `do:` is a step that types anyway, and one that needs a
		// nudge writes it. `wakeTheScreen` is still the function for it - see `publish.go`, whose
		// `disk_settle` uses it - but attaching it to every loop made 22 loops click a screen they
		// were waiting to stay still.
		if pause > 0 {
			time.Sleep(time.Duration(pause * float64(time.Second)))
		}
	}
	return nil
}

// expectOnce answers a condition once and reports whether it held.
//
// IT IS `doCheck` WITH THE ANSWER RETURNED RATHER THAN STORED. A `repeat` binds nothing - the
// condition IS the test - so the boolean has no name to carry it to the next step, and the loop needs
// the value directly. The comparison itself is the same code: one place decides what a mode means.
func (e *Executor) expectOnce(parent walk.Step, cond map[string]any) error {
	// THE CONDITION IS GIVEN A NAME ONLY BECAUSE THE COMPARATOR INSISTS ON ONE. It is a private name
	// that nothing else reads, which is what makes this different from a `check:` that binds a fact
	// for an `if:`.
	spec := map[string]any{}
	for k, v := range cond {
		spec[k] = v
	}
	if _, ok := spec["name"]; !ok {
		spec["name"] = "__until__"
	}
	// `must: true` IS WHAT MAKES THE ANSWER A VERDICT RATHER THAN A BINDING, AND WITHOUT IT EVERY
	// `until` WAS TRUE ON ITS FIRST ATTEMPT.
	//
	// A plain `check:` binds a boolean and does NOT fail when it is false - that is right for a step
	// an `if:` reads. But this function exists to answer "has the loop's condition held", and a
	// false answer that returns nil means "yes". MEASURED on freebsd install, run 45074859661:
	//
	//	wait for the partitioning menu [until]: __until__ = false
	//	    (none of "guided" or "manual" in "FreeBSD Installer -ase-dbqigbase ...")
	//	repeat: satisfied after 1 attempt(s)
	//
	// The loop stopped on the FIRST attempt with the condition visibly false, the walk then pressed
	// keys into a menu that was not up, and bsdinstall ended on a ZFS pool dialog instead of the UFS
	// path - a disk with 0 MiB written, refused by the publish floor.
	spec["must"] = true
	sub := walk.Step{
		Index: parent.Index,
		Name:  parent.Name + " [until]",
		Spec:  map[string]any{"check": spec},
	}
	return e.doCheck(sub)
}
