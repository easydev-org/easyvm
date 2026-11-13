package run

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/easydev-org/easyvm/internal/guest"
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
	// assets is what `fetch:` produced, by kind, so a later step can name a file it did not compute.
	//
	// IT EXISTS BECAUSE THE PATHS ARE NOT THE FILE'S BUSINESS. An answer CD's name is decided by
	// the asset layer; a `boot:` that hard-coded `answer.iso` would be a second place that name
	// lives, and the two would disagree the first time one moved.
	assets map[string]string
	// assetExtras is the other files one `fetch:` produced, in its own order - the three OpenCore
	// outputs are a boot image, a ROM and a variables file, and a boot step names all three.
	assetExtras map[string][]string
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
	defer e.finishRecording()

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
		err := e.runOne(s)
		if err == nil {
			res.Answered = append(res.Answered, s.Name)
			continue
		}
		if s.Has("optional") {
			// AN OPTIONAL FAILURE IS SAID OUT LOUD. A step that failed and was passed over
			// silently is indistinguishable, in the log, from one that worked.
			e.Driver.Log(fmt.Sprintf("  %s: failed and the step is optional: %v", s.Name, err))
			res.Answered = append(res.Answered, s.Name)
			continue
		}
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
		_ = m.Stop(method)
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

func (e *Executor) runOne(s walk.Step) error {
	switch {
	case s.Has("boot"), s.Has("boot_from"):
		// BOTH START A MACHINE; ONLY THE STARTING STATE DIFFERS. `doBoot` reads `boot_from:` and
		// passes it as the migration stream, so the two keys were never two implementations.
		if err := e.doBoot(s); err != nil {
			return err
		}
		e.machineSeen = true
		return nil
	case s.Has("shutdown"):
		return e.doShutdown(s)
	case s.Has("reboot"):
		return e.doReboot(s)
	case s.Has("disk"):
		return e.doDisk(s)
	case hasKey(s, "record"):
		// `s.Has` IS FALSE FOR `record: false`, AND THAT IS THE POINT OF THE KEY. Stopping a
		// recording is an ACTION, not the absence of one: run 43035390408 failed at step 29 of 29 -
		// after the machine had shut down and the disk had been published - with
		//
		//     stop recording: no implementation for this step's keys
		//
		// because the step's only key was a boolean false and every dispatcher branch tests `Has`.
		return e.doRecord(s)
	case s.Has("compress"):
		return e.doCompress(s)
	case s.Has("snapshot"):
		return e.doSnapshot(s)
	// THE THREE-STEP MODEL, IN DISPATCH ORDER. `ocr:` reads ONE region and names it; `check:` answers
	// from that name without touching the screen. Both are separate steps on purpose - see ocr.go.
	case s.Has("ocr"):
		return e.doOCR(s)
	case s.Has("check"):
		return e.doCheck(s)
	case s.Has("loop"):
		return e.doLoop(s)
	case s.Has("if"):
		return e.doIf(s)
	case s.Has("require_ssh"):
		return e.doRequireSSH(s)
	case s.Has("ssh"):
		return e.doSSH(s)
	case s.Has("repeat_until"):
		return e.doRepeatUntil(s)
	case s.Has("unless"), s.Has("unless_any"):
		if err := e.doUnless(s); err != nil {
			return err
		}
		return e.dispatchRest(s)
	case s.Has("until_console"):
		return e.doUntilConsole(s)
	case s.Has("until") || s.Has("until_any"):
		return e.doUntil(s)
	// `run` BEFORE `expect`, AND THAT ORDER IS THE FIX RATHER THAN A PREFERENCE.
	//
	// A STEP THAT TYPES A COMMAND AND CHECKS THE SCREEN CARRIES BOTH KEYS. With `expect_any` tested
	// first, such a step went to `doExpectAny` - WHICH ONLY READS THE SCREEN - so the typewriter never
	// ran and the check then reported "did not advance". Measured on manjaro's `install openssh`, whose
	// trace had NO `running N line(s)` line at all:
	//
	//	=== install openssh
	//	error:   install openssh: did not advance: wanted "pacman" or "sudo", read "_ io"
	//
	// THE FIRST FIX WAS IN `doRun` - perform the check after the typing - AND IT COULD NOT WORK, because
	// `run` never reached `doRun`. That is why the same failure appeared again on the next image.
	//
	// THE ACTION COMES FIRST AND THE CHECK IS PART OF IT, so `run` is matched before `expect`.
	case s.Has("run"):
		return e.doRun(s)
	// AND SO IS EVERY OTHER ACTION THAT CARRIES ITS OWN CHECK.
	//
	// `at_window` AND `at_button` ARE CLICKS WITH A `expect_any:` AFTER THEM, and dispatching them below
	// the `expect` branch meant the FIRST key matched was `expect_any` - so `doExpectAny` ran, which
	// only READS THE SCREEN, and the click never happened. The step then failed on the sheet it was
	// supposed to open:
	//
	//	erase - open the sheet: did not advance: wanted "erase deleted" or "free space" ...
	//	                        <- and NOT ONE `clicked (1289, 271)` LINE IN THE LOG
	//
	// Measured on macos-setup, three runs in a row, while the coordinate itself was correct the whole
	// time. The comment above `run` already describes this exact class of bug; these two were left on
	// the wrong side of the line.
	case s.Has("at_color"):
		return e.doAtColor(s)
	case hasKey(s, "at_button"):
		return e.doAtButton(s)
	case s.Has("expect_any") || s.Has("expect"):
		return e.doExpectAny(s)
	case s.Has("disk_settle"):
		return e.doDiskSettle(s)
	case hasKey(s, "host_log"):
		return e.doHostLog(s)
	case hasKey(s, "do"):
		return e.doActions(s)
	case s.Has("wait_for"):
		return e.doWaitFor(s)
	case s.Has("screenshot"):
		return e.doScreenshot(s)
	case s.Has("demo_script"):
		return e.doDemoScript(s)
	case s.Has("write"):
		return e.doWrite(s)
	// THE THREE PREPARATION KINDS ARE ONE DISPATCHER, because they are one operation with three
	// names: a file is obtained, assembled, or rendered, and all three end as a path in the work
	// directory that a later step names. `doFetch` reads whichever key is present.
	case s.Has("asset"), s.Has("build"), s.Has("generate"):
		return e.doFetch(s)
	case s.Has("scp"):
		return e.doSCP(s)
	case s.Has("script"):
		return e.doScript(s)
	case s.Has("desktop"):
		return e.doDesktop(s)
	case s.Has("share_serve"):
		return e.doShareServe(s)
	case s.Has("share_mount"):
		return e.doShareMount(s)
	case s.Has("type"):
		return e.doType(s)
	case s.Has("key"):
		return e.doKey(s)
	case s.Has("double"):
		return e.doDoubleClick(s)
	case s.Has("click"):
		return e.doClick(s)
	case s.Has("wait"):
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
		if k == "unless" || k == "unless_any" || k == "region" || k == "region_at" || k == "scale" {
			continue
		}
		rest.Spec[k] = v
	}
	return e.runOne(rest)
}

// --- the actions ---------------------------------------------------------------------

func (e *Executor) doClick(s walk.Step) error {
	x, y, err := pair(s, "click")
	if err != nil {
		return err
	}
	if err := e.Driver.Click(x, y); err != nil {
		return fail(s.Name, "click (%d, %d): %v", x, y, err)
	}
	e.Driver.Log(fmt.Sprintf("  clicked (%d, %d)", x, y))
	return e.settle(s)
}

func (e *Executor) doDoubleClick(s walk.Step) error {
	x, y, err := pair(s, "double")
	if err != nil {
		return err
	}
	if err := e.Driver.DoubleClick(x, y); err != nil {
		return fail(s.Name, "double-click (%d, %d): %v", x, y, err)
	}
	e.Driver.Log(fmt.Sprintf("  double-clicked (%d, %d)", x, y))
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
		if err := e.Driver.Click(x, y); err != nil {
			return fail(s.Name, "click before typing: %v", err)
		}
		if s.Has("clear") {
			modifier := s.Get("modifier")
			if modifier == "" {
				modifier = "ctrl"
			}
			if err := e.Driver.Stroke([]string{modifier}, "a"); err != nil {
				return fail(s.Name, "select-all: %v", err)
			}
			if err := e.Driver.Key("backspace"); err != nil {
				return fail(s.Name, "clear: %v", err)
			}
		}
	}

	perChar := 150 * time.Millisecond
	if v, err := s.Float("type_gap", 0); err == nil && v > 0 {
		perChar = time.Duration(v * float64(time.Second))
	}
	unsupported, err := e.Driver.Type(s.Get("type"), perChar)
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
	e.Driver.Log(fmt.Sprintf("  typed %d character(s)", len([]rune(s.Get("type")))))
	return e.settle(s)
}

func (e *Executor) doWait(s walk.Step) error {
	seconds, err := s.Float("wait", 0)
	if err != nil {
		return err
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

func (e *Executor) doRequireSSH(s walk.Step) error {
	timeout := seconds(s, "timeout", 900)
	probe := s.Get("ssh_probe")
	if probe == "" {
		probe = "echo ok"
	}
	target := e.target()

	// `ssh_after_reboot:` EXISTS BECAUSE A WAIT PLACED AFTER `systemctl reboot` PASSES ON THE
	// PROCESS THAT IS ABOUT TO DIE. It waits for the old daemon to stop answering first, so the
	// success it reports is the new one's.
	if s.Has("ssh_after_reboot") {
		e.Driver.Log("  waiting for the old sshd to stop answering first")
		gone := time.Now().Add(timeout)
		for time.Now().Before(gone) {
			if err := guest.Probe(target, 1, 2*time.Second); err != nil {
				break
			}
			time.Sleep(2 * time.Second)
		}
	}

	e.Driver.Log(fmt.Sprintf("  waiting up to %s for sshd on port %d", timeout, target.Port))
	start := time.Now()
	err := guest.WaitForPort(target, timeout, 2*time.Second,
		func(elapsed time.Duration, last error) {
			e.Driver.Log(fmt.Sprintf("  still waiting after %s (%v)",
				elapsed.Round(time.Second), last))
		})
	if err != nil {
		return fail(s.Name, "%v", err)
	}
	if err := guest.Probe(target, 3, 10*time.Second); err != nil {
		return fail(s.Name, "the port is open but no shell answers: %v", err)
	}
	e.Driver.Log(fmt.Sprintf("  ssh answers on port %d after %s",
		target.Port, time.Since(start).Round(time.Second)))

	// THE CONNECTION IS NOT DROPPED HERE ANY MORE, AND THAT IS THE IMPROVEMENT.
	//
	// `require_ssh` USED TO DROP THE DRIVER'S CACHED CONNECTION AFTER A SUCCESSFUL PROBE, because the
	// probe dials fresh while the driver kept one for the run - so
	//
	//	reboot: -> require_ssh: -> ssh:
	//
	// had the probe succeed and the next `ssh:` write into the pre-reboot socket:
	//
	//	status: failed step 20/20 "read what this boot did"
	//	error: read what this boot did: ssh session: EOF
	//
	// MEASURED on opensuse-tumbleweed, whose last step is exactly that pair. The fix then was a call
	// here, which meant EVERY OTHER PATH THAT RESTARTED THE GUEST had to remember it too - and
	// `ssh: systemctl reboot` does not come through this function at all.
	//
	// THE SESSION NOW REPAIRS ITSELF: `sshSession.Do` retries once on a fresh connection when an
	// operation fails because the session died. So there is nothing for this step to remember, and a
	// reboot is repaired by whichever step needs the guest next.
	return nil
}

func (e *Executor) doSSH(s walk.Step) error {
	command := s.Get("ssh")
	timeout := seconds(s, "timeout", 900)

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
	return guest.Target{
		Host:     "172.30.0.1",
		Port:     e.SSHPort,
		User:     e.File.SSHUser,
		Password: e.Password,
	}
}

// --- small helpers --------------------------------------------------------------------

func (e *Executor) settle(s walk.Step) error {
	value, err := s.Float("wait_after", 0)
	if err != nil {
		return err
	}
	if value <= 0 {
		return nil
	}
	time.Sleep(time.Duration(value * float64(time.Second)))
	return nil
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

// scriptName is the file a `script:` body is written as.
func scriptName(s walk.Step) string {
	if v := s.Get("script_name"); v != "" {
		return v
	}
	base := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		}
		return '-'
	}, strings.ToLower(s.Name))
	return filepath.Base(base) + ".ps1"
}

// errStopped is returned when a `stop` request ended the walk at a step boundary.
var errStopped = errors.New("stopped as asked")

// stepNeedsAGuest reports whether a step expects something to ANSWER.
//
// IT IS AN ALLOWLIST OF THE OPPOSITE: the steps that work on FILES are listed, and everything else is
// assumed to need the machine. That way a key added to the vocabulary without a thought here is refused
// after a shutdown - noisy and correct - rather than silently writing into a guest that is not there.
func stepNeedsAGuest(s walk.Step) bool {
	switch {
	// the ending of a walk, and anything that turns files into other files
	case s.Has("shutdown"), s.Has("compress"), s.Has("publish"), s.Has("snapshot"):
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
