package run

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/easydev-org/easyvm/internal/guest"
	"github.com/easydev-org/easyvm/internal/journal"
	"github.com/easydev-org/easyvm/internal/keys"
	"github.com/easydev-org/easyvm/internal/walk"
)

// Executor runs a walkthrough's steps.
type Executor struct {
	Driver Driver
	// File is the walkthrough, for whole-file facts a step may need.
	File *walk.File
	// Work is the scratch directory frames and copied scripts go into.
	Work string
	// SSHPort is the forwarded port, resolved once.
	SSHPort int
	// Password is the guest account's password, for sudo and for a step that logs in.
	Password string
	// StartAt and StopBefore narrow the run, for re-running from the middle.
	StartAt    string
	StopBefore string
	// startedAt is when this run began, for the recording's name.
	startedAt time.Time
	// `ManualAfter` WAS HERE AND HAD NO CALLER.
	//
	// The intent was "this run prepares a machine for a person to drive, so do not record it". Nothing
	// sets it: `create` starts a sandbox WITHOUT running a walkthrough, so it never reaches `Run` at
	// all. A field that says a run can hand the machine over, on a code path that cannot, is the same
	// shape as `requires:` - a claim no code makes. It comes back when there is a caller.

	// rec is the recording in progress. recDone stops its sampler and recStopped is closed by the
	// goroutine on its way out, so stopping it is a JOIN rather than a hope - a tick in flight can
	// otherwise outlive `finishRecording` clearing `rec`.
	rec *recorder
	// recVideo is where the running recording is being written, so a walk that fails can still
	// produce the file - the `record: false` step is unreachable once a step before it fails.
	recVideo   string
	recDone    chan struct{}
	recStopped chan struct{}

	// BootDisk is the disk a `boot:` starts when no `disk:` step named one - the --boot-disk flag.
	BootDisk string
	// madeDisk is true once a `disk_gb:` step has created the target, so a later `boot:` knows the
	// disk is this run's rather than one from somewhere else. See BootSpec.FreshDisk.
	madeDisk bool
	// bootBudget is the `timeout:` a `boot:` step carried, for the wait that follows it.
	//
	// `timeout:` ON A BOOT STEP WAS WRITTEN 53 TIMES IN THIS TREE AND READ BY NOTHING. `doBoot`
	// starts qemu and returns; the waiting is the NEXT step's, and every waiting step had its own
	// hard-coded default - `untilConsole` 180 seconds. So a file that wrote
	//
	//	boot == true
	//	os == macos
	//	timeout == 900
	//
	// was saying "this guest may take fifteen minutes" to a step that never looked, and the wait
	// that did matter was budgeted at three. MEASURED on a loaded runner: a NixOS guest whose boot
	// needed longer than the following step's 180 seconds failed at that step, whose name says
	// nothing about a boot.
	//
	// SO THE NUMBER IS CARRIED FORWARD rather than dropped: a waiting step that does NOT name its
	// own `timeout:` uses the boot's. A step that names one still wins, because a per-step budget
	// is more specific than the one a boot declared.
	bootBudget time.Duration
	// bootBudgetSet distinguishes a boot that said `timeout == 0` from no boot at all, so the
	// inheritance is opt-in and a run with no boot behaves exactly as it did before.
	bootBudgetSet bool
	// adb is the Android Debug Bridge connection, when a step asked for one.
	//
	// IT IS A FIELD BESIDE THE ssh ONE AND NOT A Driver METHOD: adb is one family's transport, not
	// a property of every machine, so a `Driver.ADB()` would be a method that five of six
	// implementations answer with nil.
	adb *adbGuest
	// assets is what `fetch:` produced, by kind, so a later step can name a file it did not compute.
	//
	// IT EXISTS BECAUSE THE PATHS ARE NOT THE FILE'S BUSINESS. An answer CD's name is decided by
	// the asset layer; a `boot:` that hard-coded `answer.iso` would be a second place that name
	// lives, and the two would disagree the first time one moved.
	assets map[string]string
	// assetExtras is the other files one `fetch:` produced, in its own order - the three OpenCore
	// outputs are a boot image, a ROM and a variables file, and a boot step names all three.
	assetExtras map[string][]string
	// Library is the content-addressed store a run's named pictures go into.
	Library ArtifactLibrary

	// OnImage is told about each named picture, so the daemon can write its row.
	OnImage func(name, sha, kind, step string, index, w, h int, rect []int, note string)

	// Journal receives one structured record per step - what it decided, what it read, and the
	// pictures it decided from. IT IS OPTIONAL: a nil satisfies it, and a test that builds an
	// Executor by value gets a silent no-op.
	//
	// IT IS NOT `OnStep`. `OnStep` is one callback with three arguments, enough for "where is it
	// now"; this carries the EVIDENCE, which is what a person asking "why did this fail" needs and
	// the log prints as a sentence they then have to decode.
	Journal Journal

	// OnStep is called as each step begins, for a daemon that must answer "where is it now".
	//
	// IT IS NOT LOGGING. The log is a stream a human reads; this is the one fact a client needs to
	// decide whether to wait, and keeping them separate is why `status` can answer without parsing
	// anything.
	OnStep func(index, total int, name string)
	// ShouldStop is asked at each step boundary, so a `stop` request takes effect between steps
	// rather than waiting out a 900-second ssh timeout.
	ShouldStop func() bool

	// seen is what an `ocr:` step read, by the name the file gave it.
	//
	// IT IS THE SECOND HALF OF THE THREE-STEP MODEL. An action does not read the screen, an `ocr:`
	// step reads one region and NAMES what it found, and a condition answers from that name:
	//
	//   - name: read the installer pane
	//     ocr: {name: pane, region: [690, 225, 1560, 265]}
	//   - name: the installer is up
	//     when: pane
	//     has_any: [[welcome], [location]]
	//
	// WITHOUT THIS THE CONDITION WOULD HAVE TO READ THE SCREEN ITSELF, which is exactly the coupling
	// that made a `until:` inside a click step report "did not advance" when the click had missed.
	//
	// THE ZERO VALUE IS USABLE, so a walk that never calls an `ocr:` step needs no setup, and the map
	// is created on first write because an Executor is built by value in places.
	seen map[string][]string

	// machineSeen is whether a `boot:` has succeeded in this walk, so "the guest is gone" is only a
	// verdict once there was a guest. The first steps of every install create a disk and fetch an ISO,
	// and none of them has a machine yet.
	machineSeen bool

	// facts is what a `check:` step decided, by the name it gave the boolean.
	//
	// THE THIRD OF THREE. `ocr:` produces TEXT, `check:` compares that text and produces a BOOLEAN,
	// and `until:` tests only the boolean. Keeping the text and the decision in separate stores is
	// what makes them separate steps: a loop can check two different questions of one reading, and a
	// failure names WHICH question came back false.
	facts map[string]bool

	// numbers is what a `see:` step MEASURED, by `<readname>.<measurement>` - `bottom.spread`,
	// `body.mean`. It is the fourth store, and the one that makes a picture a VALUE rather than a Go
	// function: a `want:` compares these without touching the screen, and a number that is not here
	// is a typo rather than a zero.
	//
	// THE ZERO VALUE IS USABLE, like `seen` and `facts` above.
	numbers map[string]float64

	// evidence is what the current step measured, filled in as it runs and consumed by `decorate`
	// into the journal record. It is per-step state, reset at each step boundary.
	evidence *evidence

	// attempts are a `loop:`'s passes, for the journal. A loop is the one step whose most useful
	// evidence is its HISTORY rather than its verdict: eight identical log lines say nothing about
	// which attempt came closest.
	attempts []journal.Attempt

	// skipped is set by a gate (`when:`/`unless:`) that decided the step does not apply, so the
	// journal can say "skipped" rather than "passed". A STEP THAT DID NOT RUN IS NOT A PASS, and a
	// report that cannot tell them apart is the same lie `optional:` used to tell.
	skipped bool

	// shots is the FRAME a `see:` step captured, by the name it was given.
	//
	// THIS IS WHAT MAKES `check:` ABLE TO DECIDE ON PIXELS WITHOUT READING THE SCREEN. `see:` names a
	// frame once; every `check:` that follows measures THAT frame, with its own `rect` - so one
	// capture serves any number of questions, and a `loop:` that re-asks a boolean pays nothing.
	//
	// MEASURED over the tree when this was added: 43 `see:` steps, every one of them followed by AT
	// MOST ONE `check:` - so the vocabulary had a store for text, a store for numbers and no store
	// for the picture, which is the thing a screen actually is.
	shots map[string]string
}

// Result is the whole walkthrough's outcome.
type Result struct {
	// Answered are the steps that ran and passed, in order.
	Answered []string
	// Err is nil when every non-optional step passed.
	Err error
}

// Run executes the steps in file order.
//
// A FAILURE STOPS THE WALK. Continuing past a failed step means every later step's message is about
// a machine in a state the failed step was supposed to create, which is how a stage reports "the
// toolchain is missing" for a disk where an install never ran.
func (e *Executor) Run(steps []walk.Step) (Result, error) {
	var res Result
	// A RECORDING STILL RUNNING WHEN THE WALK ENDS IS STOPPED **AND ENCODED**, WHICHEVER WAY IT ENDED.
	//
	// The Python implementation learned the "stop" half as "a failed stage left qemu running and held
	// /dev/kvm for every later job". THE "ENCODE" HALF IS THIS PROJECT'S OWN, and its absence is why a
	// failing leg had NO VIDEO:
	//
	//	popos-install failed at step 6 of 26, and its artifacts were `-logs` (1,693 bytes) and `-disk`
	//	(518 bytes) - NO `-video` AT ALL, because `encode` was only ever called by the `record: false`
	//	STEP and a failed walk never reaches it.
	//
	// A FAILING RUN IS THE ONE THAT NEEDS THE RECORDING. The frames are on disk; only the encode was
	// skipped, and the encode is what turns them into the file a person can watch.
	e.startedAt = time.Now()
	defer e.finishRecording()

	// THE RECORDING STARTS WITH THE WALK, NOT WITH A STEP.
	//
	// `record: <name>.mp4` AND `record: false` WERE BOTH STEPS, and both were bookkeeping: the first
	// said "start now" and the second said "stop at the end", which is what the run itself already
	// knows. Measured over the tree: 74 `record:` uses, of which 36 are the `false` half - and a walk
	// that failed before its `record: false` was the case `finishRecording` was added for, so the
	// stop was already not the step's doing.
	//
	// THE NAME IS DERIVED, because `work/*.mp4` is what the workflow uploads and nothing reads the
	// name. The sixteen handwritten ones disagreed with each other - `install.mp4`, `ubuntu-install.mp4`,
	// `vm-configure.mp4` - so a stage could not tell from a filename which leg it belonged to.
	if err := e.startAutomaticRecording(); err != nil {
		// NOT FATAL. A machine that cannot be photographed is a broken stage, but a recording that
		// cannot be started is a missing artifact - and the steps that follow are the ones that find
		// out whether the machine works.
		e.Driver.Log(fmt.Sprintf("  recording NOT started: %v", err))
	}

	started := e.StartAt == ""

	for i, s := range steps {
		if e.ShouldStop != nil && e.ShouldStop() {
			e.Driver.Log("stopping at the step boundary as asked")
			res.Err = errStopped
			return res, res.Err
		}
		// A GUEST THAT IS GONE ENDS THE WALK AT THE NEXT STEP BOUNDARY.
		//
		// MEASURED ON macos-setup: qemu exited during `wait for the recovery menu`, and the loop spent
		// its full `max: 180` - THIRTY MINUTES - on
		//
		//	attempt 40 could not read the screen (screendump: ... write: broken pipe); trying again
		//
		// against a machine that was never coming back. Every later step is a sentence about a guest
		// that does not exist, and the run should say so once.
		//
		// THE CHECK IS HERE AND NOT IN THE LOOP because "is there a machine" is a fact about the walk,
		// not about the step: a `boot:` that has not run yet has no machine either, and the first steps
		// of every install are exactly that. So it is asked only AFTER a machine has been seen.
		// A STEP THAT DOES NOT NEED THE GUEST IS STILL VALID AFTER THE GUEST IS GONE.
		//
		// THE FIRST VERSION OF THIS CHECK REFUSED EVERY STEP ONCE THE MACHINE HAD EXITED, and it broke
		// the most ordinary ending a walk has:
		//
		//	shut the installed system down       <- the step's whole purpose is to stop the machine
		//	publish the disk                     <- `qemu-img convert`, which needs the FILE, not a guest
		//
		//	error: publish the disk: the guest is no longer running, so this step cannot be done
		//
		// A SHUTDOWN IS NOT A FAILURE, and neither is anything that reads the disk afterwards:
		// `compress:`, `publish:`, `snapshot:`, `record: false`, `stop recording`, `shutdown:` itself.
		// What cannot survive is a step that expects an ANSWER - a click, a keystroke, a reading, an ssh
		// command - so those are the steps this refuses.
		if e.machineSeen && i > 0 && stepNeedsAGuest(s) {
			if m := e.Driver.Machine(); m != nil {
				if alive, ok := m.(interface{ IsRunning() bool }); ok && !alive.IsRunning() {
					res.Err = fail(s.Name, "the guest is no longer running, so this step cannot be "+
						"done. The machine exited during the step before it - its qemu is gone and "+
						"nothing that follows can be true of it")
					return res, res.Err
				}
			}
		}
		if e.OnStep != nil {
			e.OnStep(i+1, len(steps), s.Name)
		}
		if !started {
			if s.Name == e.StartAt {
				started = true
			} else {
				continue
			}
		}
		if e.StopBefore != "" && s.Name == e.StopBefore {
			e.Driver.Log(fmt.Sprintf("stopping before %q as asked", e.StopBefore))
			break
		}

		e.Driver.Log("=== " + s.Name)
		// THE STEP IS ANNOUNCED BEFORE IT RUNS, so a reader following a long step sees WHICH step the
		// time is going into. The finished record replaces it by ordinal, which is how a step that
		// hangs still appears in the report rather than vanishing from it.
		began := time.Now()
		kind := kindOf(s)
		e.journStep(s, i+1, len(steps), kind, journal.StatusRunning, time.Time{})
		err := e.runOne(s)
		// WHAT THE STEP MEASURED IS ATTACHED HERE, while `e.lastEvidence` still describes this step.
		finished := e.journStep(s, i+1, len(steps), kind, journal.StatusPassed, began)
		finished = e.decorate(finished, s)
		if err == nil {
			// A GATED STEP THAT DID NOT RUN IS NOT A PASS, and saying "passed" for it makes a stage
			// that skipped its own verification look complete. `when:`/`unless:` set this.
			if e.skipped {
				finished.Status = journal.StatusSkipped
				finished.Message = "the step's `when:`/`unless:` did not hold, so it did not run"
				e.skipped = false
			}
			e.journ(finished)
			res.Answered = append(res.Answered, s.Name)
			continue
		}
		if s.Has("optional") {
			// AN OPTIONAL FAILURE IS SAID OUT LOUD. A step that failed and was passed over
			// silently is indistinguishable, in the log, from one that worked.
			e.Driver.Log(fmt.Sprintf("  %s: failed and the step is optional: %v", s.Name, err))
			// AND IT IS ITS OWN STATUS IN THE JOURNAL, not a pass with a note. A stage that stepped
			// over a failure is exactly what a reader of this report most needs to see.
			finished.Status = journal.StatusOptional
			finished.Message = err.Error()
			e.journ(finished)
			res.Answered = append(res.Answered, s.Name)
			continue
		}
		finished.Status = journal.StatusFailed
		finished.Message = err.Error()
		e.journ(finished)
		res.Err = err
		return res, err
	}
	return res, nil
}

// StopNow stops the machine immediately, for a `stop` request or a signal.
//
// IT IS CALLED FROM ANOTHER GOROUTINE, so it touches only the driver - not the executor's own
// fields. That is the narrowest thing that can interrupt a step blocked on a guest which will never
// answer: `ShouldStop` is checked between steps, and a step already inside a 900-second wait needs
// the transport torn down rather than a flag.
func (e *Executor) StopNow(method string) {
	if e.Driver == nil {
		return
	}
	if m := e.Driver.Machine(); m != nil {
		// NO STEP HERE, SO NO GRACE: this is the failure path, where patience is not wanted.
		_ = m.Stop(method, 0)
	}
}

// runOne dispatches on the step's keys.
//
// THE ORDER OF THESE BRANCHES IS THE ORDER THE KEYS WERE ADDED, and that matters for a step that
// carries more than one: a `boot:` with `os:` is a boot, not an `os:` step. The Python
// implementation's dispatcher had the same shape and the same reason.
// hasKey reports whether a key is PRESENT, whatever its value.
//
// IT EXISTS FOR THE KEYS WHERE `false` IS A STATEMENT rather than an absence - `record: false` stops
// a recording. Everywhere else `Has` is the right question, because `boot: false` means "this step
// does not boot".
func hasKey(s walk.Step, key string) bool {
	_, ok := s.Spec[key]
	return ok
}

// gateKeys are the three spellings of "only do this if the screen says so".
//
// ONE NEGATIVE FORM AND ONE POSITIVE ONE, and the negative has a folded name as well, so a walk can
// write any of the three and all of them arrive at `doWhen`/`doUnless`.
func hasGate(s walk.Step) bool {
	return s.Has("when") || s.Has("when_not") || s.Has("when_not_any")
}

// actionKeys are the keys that MAKE THE STEP DO SOMETHING. A key that is a modifier - `rect`,
// `scale`, `settle`, `optional`, `timeout`, `after` - is not one, so `settle` beside a gate is not
// mistaken for an action with no gate.
func hasAction(s walk.Step) bool {
	for _, k := range []string{
		"boot", "input", "run", "copy", "read", "check", "control", "if", "stop", "repeat",
		"ssh", "adb", "adb_connect", "adb_privileged", "write", "disk",
		"acpi", "qga", "wait_until", "save", "screenshot",
		"at_color", "at_bottom", "at_button", "wait",
	} {
		if s.Has(k) {
			return true
		}
	}
	return false
}

func (e *Executor) runOne(s walk.Step) error {
	// A GATE IS READ BEFORE THE ACTION IT GUARDS, AND THE ORDER OF THE CASES BELOW DID NOT SAY SO.
	//
	// `runOne` dispatches on the FIRST key it recognises, and `input` was case 2 while `when` was
	// case 9. A step written as an action with a gate therefore reached `doInput` - which sends the
	// keystroke and returns - and `doWhen` was NEVER CALLED. The gate was dropped and the action ran
	// on every frame.
	//
	// MEASURED on fedora-workstation install, run 45397314773, at the step that closes the GNOME
	// Settings window anaconda's `Next` raises:
	//
	//	=== close the settings window that Next raises
	//	  key alt+f4
	//	  see: 51 word(s): () Fedora Linux 45 (Workstation Edition) installation : 1 Welcome Destination
	//
	// NO `applies:` LINE, NO `skipped:` LINE, AND THE FRAME IT READ WAS THE INSTALLER - the very
	// screen the gate existed to protect. `alt+f4` closed anaconda; the desktop was bare from t=294
	// and `the editor is up` read "" for ten minutes and failed the leg.
	//
	// THE SAME SHAPE IS IN THE WINDOWS WALKS, where the consequence is worse: `input click [1035,620]`
	// gated on `when [sure]` is the guard against CANCELLING an install in progress, and ungated it
	// lands on whatever the page has under that point.
	//
	// THE GATE BRANCHES BELOW STILL EXIST for a step that carries a gate and NO action - a gate is
	// then the whole statement. The guard here is the pairing: a gate AND an action, in one step,
	// asks whether to do the action, and that question comes first.
	//
	// `dispatchRest` DROPS the gate keys from the copy it re-dispatches, so this cannot recurse.
	gateGovernsAction := hasGate(s) && hasAction(s)

	switch {
	// ---- 0. the gate on an action, which comes before the action -------------------------
	case gateGovernsAction && s.Has("when"):
		return e.doWhen(s)
	case gateGovernsAction && (s.Has("when_not") || s.Has("when_not_any")):
		if err := e.doUnless(s); err != nil {
			if errors.Is(err, errUnlessAlready) {
				e.skipped = true
				return nil
			}
			return err
		}
		return e.dispatchRest(s)

	// ---- 1. `boot` ---------------------------------------------------------------------
	case s.Has("boot"):
		if err := e.doBoot(s); err != nil {
			return err
		}
		e.machineSeen = true
		return nil

	// ---- 2. `press` --------------------------------------------------------------------
	//
	// ONE VERB FOR THE POINTER AND THE KEYBOARD. A coordinate is a click, `key:` is a keystroke,
	// `text:` is typing, `colour:` is a blob to find, and `double:`/`anchor:` are modifiers on a
	// click. The discriminator is the field, which is the same rule `expect` and `control` follow.
	// `press` IS THE MARKER AND THE SHAPES ARE THE ACTION, so the shapes come first: a step with
	// `click:` runs `doClick`, and `doInput` is only reached for a step whose argument had no shape
	// the other branches recognise.
	case s.Has("input"):
		return e.doInput(s)

	// ---- 3. `run` ----------------------------------------------------------------------
	//
	// ONE VERB FOR A COMMAND, WITH `via:` SAYING WHERE. A bare `run:` types on the console; `via ssh`
	// and `via adb` use the transports. The old keys `ssh:`, `adb:`, `adb_connect:` and
	// `adb_privileged:` are the same operation with the transport named, and they route here.
	case s.Has("run"):
		return e.doRun(s)
	case s.Has("ssh"):
		return e.doSSH(s)
	case s.Has("adb"):
		return e.doADB(s)
	case s.Has("adb_connect"):
		return e.doADBConnect(s)
	case s.Has("adb_privileged"):
		return e.doADBPrivileged(s)

	// ---- 4. `copy` ---------------------------------------------------------------------
	case s.Has("scp"):
		return e.doSCP(s)

	// ---- 5. `read` ---------------------------------------------------------------------
	//
	// CAPTURE ONE FRAME AND BIND IT. It measures nothing and decides nothing: the region and the
	// question belong to the `expect` that follows. `see:` and `ocr:` were the same operation under
	// two names, and `screenshot:` was a third that bound no name.
	case s.Has("read"):
		return e.doSee(s)
	case s.Has("screenshot"):
		return e.doScreenshot(s)

	// ---- 6. `expect` -------------------------------------------------------------------
	//
	// ASSERT ONCE. The condition is the whole argument, and its shape is the same one a `repeat`'s
	// `until` takes, so there is one way to ask a question.
	case s.Has("check"):
		return e.doCheck(s)

	// ---- 7. `control` ------------------------------------------------------------------
	//
	// A MESSAGE TO THE MACHINE rather than to the guest: an ACPI request, the guest agent, or "wait
	// until the process has gone".
	case s.Has("control"):
		return e.doControl(s)
	case s.Has("acpi"):
		return e.doACPI(s)
	case s.Has("qga"):
		return e.doQGA(s)
	case s.Has("wait_until"):
		return e.doWaitUntil(s)

	// ---- 8. `if` -----------------------------------------------------------------------
	case s.Has("if"):
		return e.doIf(s)

	// ---- 9. `stop` ---------------------------------------------------------------------
	//
	// STOP THE MACHINE, AND PUBLISH THE DISK IN THE SAME STEP. Publishing only ever happens after a
	// stop - qemu holds the qcow2's write lock while it runs - so the two facts are one step.
	case s.Has("stop"):
		return e.doStop(s)
	// `save:` IS `stop`'s SECOND HALF AS A STEP OF ITS OWN, and it had no branch here at all.
	//
	// THE VOCABULARY DECLARED IT - `Actions["save"]` says "publish the disk to this path: the
	// second half of `stop:`" - so `easyvm check` accepted a file that wrote it and said nothing was
	// wrong. At run time `runOne` fell through every case and the step did NOTHING, silently:
	//
	//	=== publish the disk
	//	(no output, no error, no file)
	//
	// MEASURED on android blissos16 install, run 45068115059: the installer finished, the ESP was
	// rewritten, and the last step printed its name and returned. Every walk in every repository
	// ends this way, so it was the one step that could never have worked.
	case s.Has("save"):
		return e.doStop(s)

	// ---- the statement -----------------------------------------------------------------
	case s.Has("repeat"):
		return e.doRepeat(s)

	// ---- gates and modifiers, which are not verbs --------------------------------------
	case s.Has("when"):
		return e.doWhen(s)
	case s.Has("when_not"), s.Has("when_not_any"):
		if err := e.doUnless(s); err != nil {
			if errors.Is(err, errUnlessAlready) {
				e.skipped = true
				return nil
			}
			return err
		}
		return e.dispatchRest(s)
	case s.Has("do"):
		return e.doActionList(s)
	case s.Has("until"), s.Has("until_any"):
		return e.doUntil(s)

	// ---- the preparation and the disk, which are steps of their own ---------------------
	case s.Has("needs"), s.Has("require"):
		return e.doRequire(s)
	case s.Has("disk"):
		return e.doDisk(s)
	case s.Has("write"):
		return e.doWrite(s)

	// ---- the screen shapes a step may carry on its own ---------------------------------
	//
	// `desktop:` USED TO BE HERE, and it is gone. It asked "is a desktop of this family on screen"
	// and answered with SIX FAMILY-WIDE THRESHOLDS in `internal/screen` - brightness, drop, spread,
	// ratio, saturation, band means - each chosen against one renderer at one size. MEASURED on a
	// single ubuntu recording, comparing every frame with the desktop reference the file already
	// shipped:
	//
	//	 30s   similarity 0.0004   the boot splash
	//	 90s   similarity 0.0041   still splashing
	//	120s   similarity 0.9909   the desktop
	//	1600s  similarity 0.9767   the desktop, an hour later
	//
	// TWO ORDERS OF MAGNITUDE - 0.0043 at the widest for "not a desktop" against 0.9669 at the
	// tightest for "a desktop" - where the thresholds that windows failed on were three numbers
	// within a few percent of each other. So the thresholds are deleted rather than tuned, and a
	// walk waits with `repeat until check {mode: like, reference: ...}`, which is one comparison
	// every file already had the picture for.
	//
	// WHAT IT KEPT IS ELSEWHERE: a loop re-asks when the frame is the wrong SIZE (`errFrameSize` in
	// doRepeat), and a guest that blanks its screen says so in its own file.
	case s.Has("at_color"):
		return e.doAtColor(s)
	case hasKey(s, "at_bottom"):
		return e.doAtBottom(s)
	case hasKey(s, "at_button"):
		return e.doAtButton(s)

	// ---- a pause -----------------------------------------------------------------------
	case s.Has("wait"), s.Has("settle"):
		return e.doWait(s)
	}
	return fail(s.Name, "no implementation for this step's keys")
}

// dispatchRest runs the action of a step whose `unless:` gate passed.
//
// `unless` IS A GATE ON THE SAME STEP, not a step of its own: a file writes `unless:` next to the
// click it qualifies. The dispatcher therefore has to run the rest after the gate opens, and the
// alternative - treating `unless` as its own step - would need the file to name the action twice.
func (e *Executor) dispatchRest(s walk.Step) error {
	// A COPY WITHOUT THE GATE, so dispatchRest cannot recurse.
	rest := walk.Step{Index: s.Index, Name: s.Name, Spec: make(map[string]any, len(s.Spec))}
	for k, v := range s.Spec {
		if k == "when_not" || k == "when_not_any" || k == "when" ||
			k == "region" || k == "region_at" || k == "scale" {
			continue
		}
		rest.Spec[k] = v
	}
	return e.runOne(rest)
}

// --- the actions ---------------------------------------------------------------------

// doClick presses at a point. THE VALUE SAYS HOW THE POINT IS NAMED.
//
// v0.0.1 HAD SIX STEP KEYS FOR ONE VERB - `click`, `double`, `at_button`, `at_color`, `at_bottom`
// and `at_fallback` - and a reader looking for "how does this file press things" had to know all
// six. They differ only in the ADDRESS FORM of the point, which is what a mapping value is for.
//
//	click: [936, 628]                              a coordinate
//	click: {button: [703, 336]}                    inside a window, by position
//	click: {button: [703, 336], fallback: [10,10]} and a second place if the first has no window
//	click: {colour: [255, 0, 0]}                   the largest blob of one colour
//	click: [936, 628], double: true                a double-click
//	click: [936, 628], anchor: bottom              measured from the bottom edge
//
// THE OLD SPELLINGS STILL WORK AND STILL MEAN THE SAME THING, because 306 uses of them are in
// files that are not being rewritten in this release; they are simply no longer the vocabulary a
// new file should reach for. `click:` with a mapping is the folded form.
func (e *Executor) doClick(s walk.Step) error {
	if m, ok := s.Spec["click"].(map[string]any); ok {
		return e.doClickAddressed(s, m)
	}
	if s.Has("double") || s.Get("anchor") == "bottom" {
		// `click:` BESIDE `double:` / `anchor:` IS THE FOLDED FORM OF TWO OLD KEYS. Dispatched here
		// so the file writes ONE verb with a modifier rather than a second key name.
		if s.Has("double") {
			return e.doDoubleClick(s)
		}
		return e.doAtBottom(s)
	}
	x, y, err := pair(s, "click")
	if err != nil {
		return err
	}
	// EVERY COORDINATE IN THIS REPOSITORY WAS MEASURED ON A 1920x1080 FRAME, AND A CLICK AT ANOTHER
	// SIZE LANDS SOMEWHERE ELSE - SILENTLY.
	//
	// The `ocr:` steps already report this and scale, because a reading can be rescaled and still be
	// the right reading. A CLICK AT A SIZE NOBODY DECLARED CANNOT: (510, 557) is the middle of
	// Android's home-app chooser at 1920x1080 and is past the bottom edge at 1280x800, where the same
	// dialog sits at (510, 557) of a SMALLER frame - so the click lands on whatever is there. Nothing
	// reports it; the walk just fails a step later with "did not advance".
	//
	// MEASURED, on the first attempt at driving this guest: a hand-started qemu with
	// `virtio-vga,xres=1280,yres=800` photographed the chooser correctly and the click did nothing,
	// and the frame size was the reason.
	//
	// `click_at:` IS THE DECLARATION THAT MAKES RESCALING HONEST, and it is the same treatment
	// `rect_at:` already gives a reading. SOME GUESTS FIX THEIR OWN MODE AND CANNOT BE TOLD
	// OTHERWISE, which is why "set the machine to 1920x1080" is not an answer:
	//
	//	ReactOS 0.4.16  says `VESA Display (800x600x32)` whatever the card is told
	//	9front (Plan 9) `aux/vga` picks 1024x768 from the VESA table and announces it
	//
	// MEASURED with `-device VGA,xres=1920,yres=1080`: both still drew their own size. For 9front the
	// consequence was a desktop that demonstrably came up and a walk that could never finish driving
	// it, because the only way to make its terminal the current window is a click.
	x, y, rescaled, ferr := e.rescalePoint(s, x, y)
	if ferr != nil {
		return fail(s.Name, "%v", ferr)
	}
	if err := e.Driver.Click(x, y); err != nil {
		return fail(s.Name, "click (%d, %d): %v", x, y, err)
	}
	_ = rescaled
	e.Driver.Log(fmt.Sprintf("  clicked (%d, %d)", x, y))
	return e.settle(s)
}

// rescalePoint turns a coordinate the file measured into the coordinate this frame needs.
//
// THREE CASES, AND THE THIRD IS THE ONE THAT KEEPS TODAY'S SAFETY:
//
//	`click_at:` present, frame matches it        the point as written
//	`click_at:` present, frame differs           scaled by the frame's own ratio
//	`click_at:` absent, frame is not 1920x1080    REFUSED - a coordinate nobody declared
//
// `click_at:` absent AND a 1920x1080 frame is the shape every existing walk uses, and it stays
// exactly as it was: no declaration, no scaling, no change.
func (e *Executor) rescalePoint(s walk.Step, x, y int) (int, int, bool, error) {
	f, err := e.Driver.Frame()
	if err != nil {
		return x, y, false, nil
	}
	w, h := f.Size()
	if w == 0 || h == 0 {
		return x, y, false, nil
	}
	at, declared := pairAt(s)
	if !declared {
		if w != 1920 || h != 1080 {
			return 0, 0, false, fmt.Errorf(
				"click (%d, %d) is a coordinate measured at 1920x1080 but the framebuffer is "+
					"%dx%d. Either set the machine's resolution to 1920x1080, or say "+
					"`click_at: [%d, %d]` when the guest draws its own size", x, y, w, h, w, h)
		}
		return x, y, false, nil
	}
	if at[0] == w && at[1] == h {
		return x, y, false, nil
	}
	if at[0] <= 0 || at[1] <= 0 {
		return 0, 0, false, fmt.Errorf("`click_at:` must be a size, and [%d, %d] is not",
			at[0], at[1])
	}
	nx := x * w / at[0]
	ny := y * h / at[1]
	e.Driver.Log(fmt.Sprintf("  click_at %dx%d: (%d, %d) -> (%d, %d) on a %dx%d frame",
		at[0], at[1], x, y, nx, ny, w, h))
	return nx, ny, true, nil
}

// translateClick turns the mapping form of `click:` into the step that carries the old key.
//
// IT IS A PURE FUNCTION SO A TEST CAN ASSERT THE TRANSLATION without a guest: every path after it is
// the implementation that was already measured, so the only new code is this mapping.
func translateClick(spec map[string]any) map[string]any {
	m, ok := spec["click"].(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]any, len(spec)+2)
	for k, v := range spec {
		if k != "click" {
			out[k] = v
		}
	}
	switch {
	case m["button"] != nil:
		out["at_button"] = m["button"]
		if m["fallback"] != nil {
			out["at_fallback"] = m["fallback"]
		}
	case m["colour"] != nil:
		out["at_color"] = m["colour"]
	default:
		return nil
	}
	return out
}

// doClickAddressed reads the mapping form of `click:`.
//
// The mapping is translated into a COPY of the step that carries the old key, so every existing
// implementation (and every guard in it) is reused rather than duplicated: `{button: [...]}` is
// `at_button: [...]`, and the resolve-then-click path, its framebuffer check and its logging are
// the ones already measured.
func (e *Executor) doClickAddressed(s walk.Step, m map[string]any) error {
	spec := translateClick(s.Spec)
	if spec == nil {
		return fail(s.Name, "`click:` as a mapping needs `button:` or `colour:` - "+
			"`click: {button: [x, y]}`, `click: {button: [x, y], fallback: [x, y]}`, or "+
			"`click: {colour: [r, g, b]}`")
	}
	sub := walk.Step{Index: s.Index, Name: s.Name, Spec: spec}
	switch {
	case spec["at_button"] != nil:
		return e.doAtButton(sub)
	case spec["at_color"] != nil:
		return e.doAtColor(sub)
	}
	return fail(s.Name, "`click:` mapping did not translate")
}

func (e *Executor) doDoubleClick(s walk.Step) error {
	x, y, err := pair(s, "double")
	if err != nil {
		return err
	}
	// THE SAME GUARD AS `click:`, AND THE SAME DECLARATION GETS PAST IT. `click_at:` is one key for
	// one question - "this point was measured on a frame of THIS size" - so a double-click carries it
	// too rather than growing a second spelling.
	nx, ny, _, ferr := e.rescalePoint(s, x, y)
	if ferr != nil {
		return fail(s.Name, "%v", ferr)
	}
	if err := e.Driver.DoubleClick(nx, ny); err != nil {
		return fail(s.Name, "double-click (%d, %d): %v", nx, ny, err)
	}
	e.Driver.Log(fmt.Sprintf("  double-clicked (%d, %d)", nx, ny))
	return e.settle(s)
}

func (e *Executor) doKey(s walk.Step) error {
	value := s.Get("key")
	strokes, err := keys.ParseCombo(value)
	if err != nil {
		return fail(s.Name, "%v", err)
	}
	for _, stroke := range strokes {
		if err := e.Driver.Stroke(stroke.Modifiers, stroke.Key); err != nil {
			return fail(s.Name, "key %q: %v", value, err)
		}
	}
	e.Driver.Log(fmt.Sprintf("  key %s", value))
	return e.settle(s)
}

func (e *Executor) doType(s walk.Step) error {
	// `at` AND `type` ARE ONE STEP ON PURPOSE. Split into "click the box" and "type into it", the
	// second has no `at` and types into whatever has focus - and a form that was going to be
	// cleared is typed into instead. Measured in the Python implementation on a System Settings
	// search field that remembered its last query.
	if s.Has("click") {
		x, y, err := pair(s, "click")
		if err != nil {
			return err
		}
		// THE POINT BEFORE TYPING IS A CLICK, so it takes the same declaration and the same
		// rescaling - a form field at (960, 628) of a 1024x768 guest is at (512, 448) of its own
		// frame, and typing into the wrong place is the failure this file exists to avoid.
		nx, ny, _, ferr := e.rescalePoint(s, x, y)
		if ferr != nil {
			return fail(s.Name, "%v", ferr)
		}
		if err := e.Driver.Click(nx, ny); err != nil {
			return fail(s.Name, "click before typing: %v", err)
		}

	}

	// `clear: true` SELECTS ALL AND DELETES BEFORE TYPING, AND IT WAS DECLARED AND NEVER IMPLEMENTED.
	//
	// MEASURED on AnduinOS' account form: the username box arrives holding a `vm`, `input text "vmuser"
	// at ...` appended, and the disk was built with the account `vmvmuser`. Every later `ssh:` then
	// failed with `unable to authenticate, attempted methods [none password]` against a machine whose
	// sshd was active and whose `PasswordAuthentication yes` was in place - a message that names
	// nothing and sent three runs into the wrong file.
	//
	// THE VOCABULARY HAS ADVERTISED THIS KEYWORD the whole time (vocab.go: `clear:` "select-all and
	// delete before typing"), which is why the walk was written with it. Nothing read it, so the flag
	// was a comment: a typo would have been refused as an unknown key, and a supported-but-ignored one
	// is the worse of the two.
	//
	// `modifier:` SAYS WHICH SELECT-ALL, ctrl by default. The delete is `backspace`: a selection replaced
	// by a delete leaves an empty field, which is what the caller asked for.
	if s.Get("clear") != "" {
		if clear, present := s.Bool("clear"); present && clear {
			modifier := s.Get("modifier")
			if modifier == "" {
				modifier = "ctrl"
			}
			if err := e.Driver.Stroke([]string{modifier}, "a"); err != nil {
				return fail(s.Name, "clear: select all with %s+a: %v", modifier, err)
			}
			if err := e.Driver.Key("backspace"); err != nil {
				return fail(s.Name, "clear: delete the selection: %v", err)
			}
			e.Driver.Log(fmt.Sprintf("  cleared the field (%s+a, backspace)", modifier))
		}
	}

	// 200 ms, MATCHING `run:` - see the note in text.go for the measured duplication at 20 ms.
	perChar := 200 * time.Millisecond
	if v, err := s.Float("type_gap", 0); err == nil && v > 0 {
		perChar = time.Duration(v * float64(time.Second))
	}
	// `text:` IS WHAT 2.0 WRITES AND `type:` IS THE 1.0 NAME. Reading only `type:` made every
	// `input text "..."` TYPE NOTHING - which the log said plainly and nothing treated as a failure:
	//
	//	=== create your account - full name - type the value
	//	  typed 0 character(s)
	//
	// MEASURED on ubuntu install, run 45089339918: the installer's account form was never filled,
	// the Continue button did nothing, the walk photographed "your account" twenty-four times, and
	// the stage died at step 52 of 59 - on a form whose every other step (the click, the select-all,
	// the delete) had worked.
	//
	// THE SAME FOLD EXISTS IN `doInput`, WHICH ACCEPTS `s.Has("text") || s.Has("type")` AND THEN
	// CALLS THIS FUNCTION. The dispatcher knew both names; the typing did not.
	value := s.Get("text")
	if value == "" {
		value = s.Get("type")
	}
	unsupported, err := e.Driver.Type(value, perChar)
	if err != nil {
		return fail(s.Name, "typing: %v", err)
	}
	if len(unsupported) > 0 {
		// NOT FATAL, BUT NOT SILENT EITHER. A character with no qcode is a gap in the table, and
		// typing the rest while saying which were skipped is what the Python implementation's
		// caller expected.
		e.Driver.Log(fmt.Sprintf("  COULD NOT TYPE %q - no qcode for those characters",
			string(unsupported)))
	}
	e.Driver.Log(fmt.Sprintf("  typed %d character(s)", len([]rune(value))))
	return e.settle(s)
}

func (e *Executor) doWait(s walk.Step) error {
	// `settle:` AND `wait:` ARE THE SAME OPERATION. `settle:` is the name the vocabulary keeps;
	// `wait:` is what the newer step shape used for the same thing, and it stays readable so a file
	// written against either still runs.
	seconds, err := s.Float("settle", 0)
	if err != nil {
		return err
	}
	if seconds <= 0 {
		if seconds, err = s.Float("wait", 0); err != nil {
			return err
		}
	}
	e.Driver.Log(fmt.Sprintf("  waiting %.0fs, as measured", seconds))
	deadline := time.Now().Add(time.Duration(seconds * float64(time.Second)))
	for time.Now().Before(deadline) {
		// A WAIT LISTENS FOR `stop`, BECAUSE A WAIT IS THE LONGEST THING A WALK DOES.
		//
		// `ghostbsd`'s `installation runs` is `wait: 1800` - THIRTY MINUTES INSIDE A SINGLE STEP - and
		// `ShouldStop` was only read BETWEEN steps, so `easyvm stop` set the flag and nothing happened
		// for the rest of the half hour:
		//
		//	status: running step 22/25 "installation runs"
		//	REFUSED: a run is already in progress (step "installation runs"); stop it first
		//
		// and the only way out was to kill PID 1 and restart the container, which threw away the
		// machine and the frames with it.
		//
		// EVERY LONG LOOP NEEDS THIS. A wait that cannot be interrupted is not a wait, it is a wall.
		if e.ShouldStop != nil && e.ShouldStop() {
			e.Driver.Log("  stopping the wait as asked")
			return errStopped
		}
		if f, err := e.Driver.Frame(); err == nil {
			_ = f // a frame keeps the screendump path warm, as the Python one did
		}
		time.Sleep(minDuration(5*time.Second, time.Until(deadline)))
	}
	return nil
}

// --- ssh ------------------------------------------------------------------------------

func (e *Executor) doSSH(s walk.Step) error {
	command := s.Get("ssh")
	timeout := e.budget(s, "timeout", 900)

	var res guest.Result
	run := func(client *guest.Client) error {
		var runErr error
		res, runErr = client.Run(command, timeout)
		return runErr
	}
	// THE COMMAND GOES THROUGH THE DRIVER'S SESSION WHEN IT HAS ONE, so a connection that died while
	// the guest rebooted is repaired here rather than failing the step.
	//
	// MEASURED, on opensuse-tumbleweed, whose last step is `reboot:` followed by an `ssh:`: without the
	// repair it reported `ssh session: EOF` and pointed at the step, while the cause was the reboot
	// three steps earlier. WITH it, the step dials once more and succeeds - and the log still shows the
	// command's own output, because the retry is the connection's business and not the step's.
	err := e.withGuest(run)
	e.Driver.Log(fmt.Sprintf("  ssh: %s -> %d", oneLine(command), res.Status))
	// THE COMMAND AND ITS EXIT STATUS, recorded: an `ssh:` step is one command in the guest, and a
	// report that names the step but not the command cannot be checked against the machine.
	if e.evidence == nil {
		e.evidence = &evidence{}
	}
	e.evidence.Command = []string{command}
	if err == nil {
		okNow := res.Status == 0
		e.evidence.OK = &okNow
	}
	for _, line := range headAndTail(res.Combined(), 18, 6) {
		e.Driver.Log("    " + line)
	}
	if err != nil {
		return fail(s.Name, "%v", err)
	}
	if res.Status != 0 {
		return fail(s.Name, "the command exited %d", res.Status)
	}
	return nil
}

// withGuest runs one operation against the guest, repairing a dead ssh session once when the driver
// knows how.
//
// THE CAPABILITY IS OPTIONAL ON PURPOSE. A fake driver in a test has no session to repair, and a
// transport other than ssh would not have this failure at all - so this is a type assertion rather
// than a method on `Driver`, the same way `DropGuest` used to be.
func (e *Executor) withGuest(fn func(*guest.Client) error) error {
	if doer, ok := e.Driver.(interface {
		DoGuest(func(*guest.Client) error) error
	}); ok {
		return doer.DoGuest(fn)
	}
	client := e.Driver.Guest()
	if client == nil {
		return fail("", "no ssh connection; a `require_ssh:` step must come first")
	}
	return fn(client)
}

// oneLine shortens a command for a log line without hiding that it was shortened.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 160 {
		return s[:160] + "..."
	}
	return s
}

// headAndTail prints the beginning AND the end of a long output.
//
// THE TAIL ALONE WAS WRONG THREE TIMES in the Python implementation: `schtasks /Query /V` puts
// `Last Run Time` in the first twenty lines, winget puts its version at the top, and DISM puts its
// progress at the bottom. A diagnostic that truncates the diagnosis is worse than none, because it
// looks like it was read.
func headAndTail(body string, head, tail int) []string {
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	if len(lines) <= head+tail+1 {
		return lines
	}
	out := make([]string, 0, head+tail+2)
	out = append(out, lines[:head]...)
	out = append(out, fmt.Sprintf("... %d more line(s) ...", len(lines)-head-tail))
	out = append(out, lines[len(lines)-tail:]...)
	return out
}

func (e *Executor) target() guest.Target {
	// THE GUEST'S OWN SOCKET, NOT THE HOST'S DNAT PORT - the same change as daemon.NewDriver.
	// Measured on run 45823139196: ARP to 172.30.0.2 is REACHABLE and the route is right, but the
	// DNAT hop to 172.30.0.1:2222 leaves no conntrack entry and the host answers `no route to
	// host` while the guest listens on 0.0.0.0:22. Dialling the guest directly skips that hop.
	return guest.Target{
		Host:     "172.30.0.1",
		Port:     e.SSHPort,
		User:     e.File.SSHUser,
		Password: e.Password,
		Key:      mustKey(e),
	}
}

// --- small helpers --------------------------------------------------------------------

// settle waits out a step's own pause — the time between an action and the screen being ready.
//
// `settle:` IS THE NAME AND `wait_after:` IS THE OLD ONE, and both are read here rather than in two
// places. MEASURED across the 57 walkthroughs: `wait_after` 627 uses and `wait` 118, the same idea
// under two names, and one step in three carried a guess. `docs/PRIMITIVES.md` records the
// retirement; this function is why the migration is per-file and cannot break a walkthrough — an old
// file keeps working while a migrated one says `settle:`.
//
// A STEP THAT CAN WAIT ON A FACT SHOULD. The validator reports a `settle:` followed by a `check:` of
// the same region, because a timed guess is what failed cachyos and endeavouros on CI: the number was
// right on the measuring machine and wrong on the runner.
// settle waits out a step's own pause — the time between an action and the screen being ready.
//
// `settle:` IS A MODIFIER AND NOT A WAIT, AND THE DIFFERENCE IS MEASURED RATHER THAN STYLISTIC.
//
// A `settle:` is a SHORT pause after an action, and a blind `time.Sleep` is right for it: 640 of the
// 738 pauses in these 57 files ride on an action step, and 534 are ten seconds or less. There is
// nothing to interrupt and nothing to measure - the action just happened and the pane needs a moment.
//
// `wait:` IS A STEP OF ITS OWN AND MUST STAY ONE when the pause is long, because `doWait` is the
// thing that listens for `easyvm stop`: it polls `ShouldStop` every five seconds. A `settle:` of
// 2400 seconds - which `ghostbsd`'s `installation runs` really is - would be a WALL, and the files
// record what that cost:
//
//	status: running step 22/25 "installation runs"
//	REFUSED: a run is already in progress (step "installation runs"); stop it first
//
// and the only way out was to kill PID 1 and restart the container, throwing away the machine with
// it. SO THE RULE IS:
//
//	`settle: N` on an action step   N is short, and there is nothing to wait FOR
//	`- settle: N` as its own step   long, and interruptible through `doWait`
//
// THE NAME IS THE SAME IN BOTH POSITIONS so a file does not have to know which is which, and this
// function is why: a step whose only key is `settle:` is dispatched to `doWait`, which polls. A
// `settle:` beside a `click:` never reaches here for longer than the pause it asked for.
func (e *Executor) settle(s walk.Step) error {
	value, err := s.Float("settle", 0)
	if err != nil {
		return err
	}
	if value <= 0 {
		if value, err = s.Float("wait_after", 0); err != nil {
			return err
		}
	}
	if value <= 0 {
		return nil
	}
	// A MODIFIER'S PAUSE IS STILL INTERRUPTIBLE. `time.Sleep` here made `easyvm stop` a suggestion
	// for the length of the pause, and this repository has already paid for that: a 1800-second
	// `wait:` was a wall and the only way out was to kill PID 1.
	//
	// SO THIS POLLS, in five-second steps, exactly as `doWait` does. The cost is a few more wakeups;
	// the difference is that a stop between two of them is honoured. That is why the validator can
	// allow a `settle:` beside an action at all.
	deadline := time.Now().Add(time.Duration(value * float64(time.Second)))
	for time.Now().Before(deadline) {
		if e.ShouldStop != nil && e.ShouldStop() {
			e.Driver.Log("  stopping the pause as asked")
			return errStopped
		}
		time.Sleep(minDuration(5*time.Second, time.Until(deadline)))
	}
	return nil
}

// budget is `seconds` for the waits that FOLLOW a boot, so a step that says nothing inherits the
// patience the file already declared for its guest.
//
// A STEP THAT NAMES ITS OWN `timeout:` STILL WINS - a per-step budget is more specific than the one
// a boot step declared - and a run with no `boot:` behaves exactly as it did before, because
// `bootBudgetSet` is only true once a boot carried a number.
func (e *Executor) budget(s walk.Step, key string, fallback float64) time.Duration {
	if _, named := s.Spec[key]; !named && e != nil && e.bootBudgetSet && e.bootBudget > 0 {
		return e.bootBudget
	}
	return seconds(s, key, fallback)
}

func seconds(s walk.Step, key string, fallback float64) time.Duration {
	v, err := s.Float(key, fallback)
	if err != nil || v <= 0 {
		v = fallback
	}
	return time.Duration(v * float64(time.Second))
}

func pair(s walk.Step, key string) (int, int, error) {
	v, ok := s.Spec[key]
	if !ok {
		return 0, 0, fail(s.Name, "%q is missing", key)
	}
	list, ok := v.([]any)
	if !ok || len(list) != 2 {
		return 0, 0, fail(s.Name, "%q must be [x, y]", key)
	}
	x, okx := asInt(list[0])
	y, oky := asInt(list[1])
	if !okx || !oky {
		return 0, 0, fail(s.Name, "%q must be two whole numbers", key)
	}
	return x, y, nil
}

// colorTriple reads `[r, g, b]`, which is how a control's colour is named.
func colorTriple(s walk.Step, key string) (int, int, int, error) {
	v, ok := s.Spec[key]
	if !ok {
		return 0, 0, 0, fail(s.Name, "%q is missing", key)
	}
	list, ok := v.([]any)
	if !ok || len(list) != 3 {
		return 0, 0, 0, fail(s.Name, "%q must be [r, g, b], each 0..255", key)
	}
	out := [3]int{}
	for i := range out {
		n, ok := asInt(list[i])
		if !ok || n < 0 || n > 255 {
			return 0, 0, 0, fail(s.Name, "%q must be three numbers 0..255; %v is not", key, list[i])
		}
		out[i] = n
	}
	return out[0], out[1], out[2], nil
}

func asInt(v any) (int, bool) {
	switch t := v.(type) {
	case int:
		return t, true
	case float64:
		return int(t), true
	}
	return 0, false
}

func minDuration(a, b time.Duration) time.Duration {
	if b < a {
		return b
	}
	return a
}

// errNotImplemented is what a step returns when its key is valid vocabulary but has no executor
// yet. It is distinct from a failure because a missing implementation must never be counted as one
// - the reader needs to know the difference between "the guest did not do it" and "this build
// cannot ask".
var errNotImplemented = errors.New("not implemented in this build")

// notImplemented is used by the steps that are vocabulary-only for now.
func notImplemented(s walk.Step, key string) error {
	return fmt.Errorf("%w: step %q needs %q", errNotImplemented, s.Name, key)
}

// errStopped is returned when a `stop` request ended the walk at a step boundary.
var errStopped = errors.New("stopped as asked")

// stepNeedsAGuest reports whether a step expects something to ANSWER.
//
// IT IS AN ALLOWLIST OF THE OPPOSITE: the steps that work on FILES are listed, and everything else is
// assumed to need the machine. That way a key added to the vocabulary without a thought here is
// refused after a shutdown - noisy and correct - rather than silently writing into a guest that is
// not there.
func stepNeedsAGuest(s walk.Step) bool {
	switch {
	// the ending of a walk, and anything that turns files into other files
	case s.Has("stop"):
		// `stop` STOPS THE MACHINE and may also publish the disk. Neither asks the guest anything.
		return false
	// A `boot:` IS THE ONE STEP THAT DOES NOT NEED A GUEST TO BE THERE, because it is what PUTS one
	// there. THE "the guest is gone" GUARD IS RIGHT FOR EVERY OTHER STEP and wrong for this one: a
	// walk that stops a guest and starts another - which is how one recording shows two operating
	// systems - was refused at its second `boot:` with
	//
	//	boot linux: the guest is no longer running, so this step cannot be done
	//
	// which is a true sentence about the step before the one that starts a machine.
	case s.Has("boot"), s.Has("boot_from"):
		return false
	// AND `wait_until:` IS THE OTHER ONE, FOR THE SAME REASON IN THE OTHER DIRECTION.
	//
	// `step "stop the machine"` is followed by `step "wait for the machine to stop"` with
	// `wait_until: stopped` - and the FIRST of those succeeds, so the guest is gone when the second
	// begins. THE GUARD SAW A DEAD GUEST AND REFUSED THE STEP THAT WAS WAITING FOR IT TO DIE:
	//
	//	status: failed step 29/31 "stop the machine"
	//	error:   wait for the machine to stop: the guest is no longer running, so this step cannot
	//	         be done. The machine exited during the step before it
	//
	// measured on macos 15 ssh, run 44461859215, on the step whose whole purpose is to confirm the
	// shutdown that had just worked. `wait_until:` asks ABOUT the machine rather than asking it
	// something, so it must be allowed to run when the answer is "gone".
	case s.Has("wait_until"):
		return false
	// AND `save:` IS THE SAME KIND OF THING - IT COPIES THE DISK FILE. `save:` is what `stop`'s
	// publishing half compiles to, and the ordinary ending of a walk is:
	//
	//	step "stop the installer"          stop kill
	//	step "publish the disk"            save "{{ out }}"
	//
	// The first stops the machine and the second copies the disk. Neither asks the guest anything,
	// and both must run with qemu gone - qemu holds the disk's write lock while it runs.
	//
	// MEASURED on android blissos16 install, run 45065093494: steps 1 to 31 passed, and the 32nd -
	// `save` - was refused with "the guest is no longer running, so this step cannot be done. The
	// machine exited during the step before it", which is a true sentence about a step whose whole
	// job is to copy the file that machine left behind.
	case s.Has("save"):
		return false
	case hasKey(s, "record"):
		// `record: false` stops the sampler; the string form starts one and needs a screen.
		//
		// IT IS `hasKey` AND NOT `s.Has` BECAUSE `Has` IS ABOUT TRUTH, NOT PRESENCE: it answers false for
		// a `false` value, so `record: false` looked like a step without a `record:` key at all.
		if v, ok := s.Spec["record"].(bool); ok && !v {
			return false
		}
		return true
	}
	return true
}

// ArtifactLibrary is the content-addressed store a run's named pictures go into.
//
// IT IS AN INTERFACE FOR THE SAME REASON `Journal` IS: `Executor` is built by value in 340 tests and
// the zero value has to work. `Put` returns the content address the picture is known by.
type ArtifactLibrary interface {
	Put(path string) (string, error)
}

// mustKey loads the walk's `ssh_key:` file, logging rather than failing when it cannot be read.
//
// A KEY THAT WAS NAMED AND CANNOT BE READ IS REPORTED ON THE LINE THAT NAMED IT, and the walk then
// proceeds with the password path - which is what a stage without the key would have done anyway. The
// alternative (failing the whole run at target() time) would turn a missing optional input into a
// stage that cannot do anything at all.
func mustKey(e *Executor) []byte {
	if e.File == nil || e.File.SSHKeyPath == "" {
		return nil
	}
	key, err := guest.LoadKey(e.File.SSHKeyPath)
	if err != nil {
		e.Driver.Log(fmt.Sprintf("  %v (the password path is still offered)", err))
		return nil
	}
	return key
}
