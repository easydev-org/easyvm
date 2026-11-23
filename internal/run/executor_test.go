package run

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/easydev-org/easyvm/internal/guest"
	"github.com/easydev-org/easyvm/internal/walk"
)

// fakeDriver records what a step asked for, so the dispatch and the counting can be tested without
// a machine. The Python implementation had no such seam: its drivers opened sockets in their
// constructors, so every test of the step loop needed qemu running.
type fakeDriver struct {
	log []string
	// machine is the ONE machine this driver hands out; see Machine().
	machine  *fakeMachine
	clicks   [][2]int
	doubles  [][2]int
	keys     []string
	strokes  [][2]string
	typed    []string
	started  int
	halted   bool
	boot     *BootSpec
	released bool
	stopped  []string
	graces   []time.Duration
	frames   int
	guestNil bool
	// facts overrides the map `Facts` returns, so a test can say what a machine fact answers.
	facts map[string]string
}

func (f *fakeDriver) Frame() (Frame, error) {
	f.frames++
	return stubFrame{}, nil
}
func (f *fakeDriver) Click(x, y int) error {
	f.clicks = append(f.clicks, [2]int{x, y})
	return nil
}
func (f *fakeDriver) DoubleClick(x, y int) error {
	f.doubles = append(f.doubles, [2]int{x, y})
	return nil
}
func (f *fakeDriver) Key(q string) error {
	f.keys = append(f.keys, q)
	return nil
}
func (f *fakeDriver) Stroke(mods []string, key string) error {
	f.strokes = append(f.strokes, [2]string{strings.Join(mods, "+"), key})
	return nil
}
func (f *fakeDriver) Type(text string, _ time.Duration) ([]rune, error) {
	f.typed = append(f.typed, text)
	return nil, nil
}
func (f *fakeDriver) Guest() *guest.Client {
	if f.guestNil {
		return nil
	}
	return nil
}

// Machine returns ONE fakeMachine for the driver's lifetime, which is what a real driver does.
//
// IT USED TO BUILD A NEW ONE PER CALL, so `halted` was forgotten between steps: a test that stopped a
// guest and asked whether it was still running got a fresh machine that had never stopped. Two of the
// four tests about switching guests passed on that accident.
func (f *fakeDriver) Machine() Machine {
	if f.machine == nil {
		f.machine = &fakeMachine{d: f}
	}
	return f.machine
}
func (f *fakeDriver) Log(msg string) { f.log = append(f.log, msg) }

// Facts is what a `wait_until:` reads, and a fake without it answers the question with an error:
//
//	wait for the machine to stop: this driver cannot report the facts a `wait_until:` waits on
//
// so a test that stops a machine and waits for it was testing that error rather than the wait.
func (f *fakeDriver) Facts() (map[string]string, error) {
	running := f.started > 0 && !f.halted
	out := map[string]string{
		"qemu_running": fmt.Sprintf("%t", running),
		"ssh_ready":    "false",
		"qga_ready":    "false",
		"guest_phase":  "",
	}
	for k, v := range f.facts {
		out[k] = v
	}
	return out, nil
}

type stubFrame struct{}

// Path is a REAL PNG, because a `check:` that measures the captured frame has to be able to load it.
//
// IT USED TO BE `/dev/null`, which was enough while a `see:` did the measuring and a `check:` only
// compared what it bound. Now the frame IS the thing compared, so a fake that cannot be decoded makes
// every frame-based check fail with "image: unknown format" - a test failure that says nothing about
// the code under test.
func (stubFrame) Path() string {
	const onePixel = "stub-frame.png"
	p := filepath.Join(os.TempDir(), onePixel)
	if _, err := os.Stat(p); err != nil {
		// SMALL, BECAUSE ONLY THE DECODE MATTERS: a `rect` larger than the frame is an error from
		// `Crop`, and the tests that need a full-frame region say so in their own step.
		img := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
		if f, err := os.Create(p); err == nil {
			_ = png.Encode(f, img)
			_ = f.Close()
		}
	}
	return p
}
func (stubFrame) Size() (int, int) { return 1920, 1080 }
func (stubFrame) Words() []string  { return nil }

type fakeMachine struct{ d *fakeDriver }

func (m *fakeMachine) ReleaseDiff() error { m.d.released = true; return nil }

func (m *fakeMachine) Boot(spec BootSpec) error {
	// THE SPEC IS RECORDED, so a test can assert what a `boot:` step asked for without a machine.
	m.d.boot = &spec
	m.d.started++
	m.d.halted = false
	return nil
}
func (m *fakeMachine) Stop(method string, grace time.Duration) error {
	m.d.stopped = append(m.d.stopped, method)
	m.d.graces = append(m.d.graces, grace)
	// A STOPPED MACHINE IS NOT A MACHINE, and the fake has to say so or it answers a question about
	// `doBoot`'s "one daemon holds one machine" guard that the code never asked it.
	//
	// MEASURED AS A TEST FAILURE: with `IsRunning` returning `started > 0` forever, a walk that
	// stopped one guest and started another saw the second boot as "already running" - and every test
	// that could have noticed the real behaviour was answered by the fake instead.
	m.d.halted = true
	return nil
}
func (m *fakeMachine) Reboot() error { return nil }

// RunningTheSame is what `doBoot` compares when a machine is already up.
//
// A BOOT NAMES QEMU ARGUMENTS NOW, not a guest family, so "is it the same machine" is a comparison of
// what the two boots asked for. WITHOUT IT THE FAKE ANSWERED `false` FOR EVERY SECOND BOOT and the
// guard fired on a machine the file had described identically.
func (m *fakeMachine) RunningTheSame(args []string) bool {
	if m.d.boot == nil {
		return false
	}
	have := append([]string{m.d.boot.Machine}, m.d.boot.Args...)
	if len(have) != len(args) {
		return false
	}
	for i := range have {
		if have[i] != args[i] {
			return false
		}
	}
	return true
}

// OS is kept for the tests that assert what a boot asked for; nothing switches on it any more.
func (m *fakeMachine) OS() string {
	if m.d.boot == nil {
		return ""
	}
	return m.d.boot.OS
}
func (m *fakeMachine) IsRunning() bool { return m.d.started > 0 && !m.d.halted }
func (m *fakeMachine) PressPowerButton() error {
	m.d.stopped = append(m.d.stopped, "acpi")
	return nil
}
func (m *fakeMachine) ResetMachine() error { return nil }
func (m *fakeMachine) GuestAgent(string, map[string]any) (string, error) {
	return "", nil
}

func newExec(d Driver, steps ...map[string]any) (*Executor, []walk.Step) {
	var docs []walk.Step
	for i, spec := range steps {
		docs = append(docs, walk.Step{Index: i + 1, Name: fmt.Sprint(spec["name"]), Spec: spec})
	}
	return &Executor{Driver: d, Work: "/tmp/x", SSHPort: 2222,
		File: &walk.File{SSHUser: "vmuser"}}, docs
}

// A STEP DOES ONE THING, AND THE DISPATCHER PROVES IT: a click is a click, a key is a key, and a
// `type` with an `at` clicks before it types because that is one act on a form.
func TestActionsReachTheRightChannel(t *testing.T) {
	d := &fakeDriver{}
	ex, steps := newExec(d,
		map[string]any{"name": "click", "input": true, "click": []any{100, 200}},
		map[string]any{"name": "double", "input": true, "double": []any{10, 20}, "click": []any{10, 20}},
		map[string]any{"name": "key", "input": true, "key": "ret"},
		map[string]any{"name": "combo", "input": true, "key": "ctrl+alt+t"},
		map[string]any{"name": "type", "input": true, "text": "hello"},
		map[string]any{"name": "type in field", "input": true, "at": []any{5, 6}, "text": "world"},
	)
	res, err := ex.Run(steps)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Answered) != 6 {
		t.Fatalf("answered %d steps, want 6", len(res.Answered))
	}
	if len(d.clicks) != 2 || d.clicks[0] != [2]int{100, 200} || d.clicks[1] != [2]int{5, 6} {
		t.Fatalf("clicks = %v; the at:] steps should click, and the type-in-field step too", d.clicks)
	}
	if len(d.doubles) != 1 || d.doubles[0] != [2]int{10, 20} {
		t.Fatalf("doubles = %v", d.doubles)
	}
	// ctrl+alt+t is ONE stroke, not three keys - and `ret` is one stroke of its own.
	if len(d.strokes) != 2 {
		t.Fatalf("strokes = %v; want one for `ret` and one for ctrl+alt+t", d.strokes)
	}
	if d.strokes[0] != [2]string{"", "ret"} {
		t.Fatalf("ret became %v", d.strokes[0])
	}
	if d.strokes[1] != [2]string{"ctrl+alt", "t"} {
		t.Fatalf("ctrl+alt+t became %v", d.strokes[1])
	}
	if len(d.typed) != 2 {
		t.Fatalf("typed = %v", d.typed)
	}
}

// A FAILURE STOPS THE WALK. Continuing past a failed step means every later message is about a
// machine in a state the failed step was supposed to create.
func TestFailureStopsTheWalk(t *testing.T) {
	d := &fakeDriver{}
	ex, steps := newExec(d,
		map[string]any{"name": "one", "input": true, "click": []any{1, 1}},
		map[string]any{"name": "broken", "click": "not a pair"},
		map[string]any{"name": "three", "input": true, "click": []any{3, 3}},
	)
	res, err := ex.Run(steps)
	if err == nil {
		t.Fatal("a broken step must fail the walk")
	}
	if len(res.Answered) != 1 {
		t.Fatalf("answered %v; only the step before the failure should be counted", res.Answered)
	}
	if len(d.clicks) != 1 {
		t.Fatalf("the step after the failure ran: %v", d.clicks)
	}
}

// AN OPTIONAL FAILURE IS SAID OUT LOUD AND THE WALK CONTINUES. A step that failed and was passed
// over silently is indistinguishable, in a log, from one that worked.
func TestOptionalFailureIsLoggedAndPassed(t *testing.T) {
	d := &fakeDriver{}
	ex, steps := newExec(d,
		map[string]any{"name": "flaky", "click": "not a pair", "optional": true},
		map[string]any{"name": "after", "input": true, "click": []any{9, 9}},
	)
	res, err := ex.Run(steps)
	if err != nil {
		t.Fatalf("an optional failure must not fail the walk: %v", err)
	}
	if len(res.Answered) != 2 {
		t.Fatalf("answered %v", res.Answered)
	}
	joined := strings.Join(d.log, "\n")
	if !strings.Contains(joined, "optional") {
		t.Fatalf("the optional failure was not logged:\n%s", joined)
	}
}

// AND THE VERDICT IS NOT THE SAME AS THE ACTION. A step whose action ran and whose own check failed
// is NOT counted as answered - a run that says "13 of 14 answered" must not include the one that
// clicked and then failed, or the transcript disagrees with the verdict.
func TestAFailedStepIsNotCounted(t *testing.T) {
	d := &fakeDriver{}
	ex, steps := newExec(d,
		map[string]any{"name": "click", "input": true, "click": []any{1, 1}},
		map[string]any{"name": "bad pair", "input": true, "click": []any{1}},
	)
	res, _ := ex.Run(steps)
	for _, name := range res.Answered {
		if name == "bad pair" {
			t.Fatal("a step that failed its own argument check was counted as answered")
		}
	}
}

// THE DISPATCHER AND THE CLAIMED KEY LIST CANNOT DRIFT. This is the test the Python implementation
// never had: a key accepted by the vocabulary and never handled is a step that does nothing and
// reports success.
func TestEveryClaimedKeyIsHandled(t *testing.T) {
	claimed := ExecutableKeys()
	if len(claimed) == 0 {
		t.Fatal("no keys claimed")
	}
	for _, key := range claimed {
		// A step carrying only this key must not reach "no implementation".
		//
		// THE STEP MAY STILL FAIL - a `disk:` with no source image does - and that is fine: the
		// question here is only whether the dispatcher has a branch. What must not happen is the
		// "no implementation" sentinel, and what must not happen either is the test taking
		// minutes, so the probe values are chosen to fail fast rather than to work.
		d := &fakeDriver{}
		ex, steps := newExec(d, map[string]any{"name": "probe", key: probeValue(key)})
		ex.Work = t.TempDir()
		// A BRANCH THAT WOULD WAIT FOR A GUEST MUST NOT WAIT IN A TEST. The probe values are
		// shaped so each step refuses or returns at once; where a key's only behaviour is "wait",
		// the timeout is set to a hair so the branch is still reached.
		//
		// `until_console` BELONGS ON THIS LIST AND WAS MISSING, so it waited out its own 180-second
		// default and the whole package's test run timed out at ninety seconds. The list is the
		// thing that has to grow when a waiting key is added, which is why it sits here rather than
		// being derived: derived-from-nothing would never have caught it either.
		//
		// `adb_connect` BELONGS HERE AND WAS MISSING, and the cost was five minutes of every run:
		// MEASURED, the package's tests were 331 seconds and 320 of them were this one key waiting
		// out its own 300-second default for an adbd that no fake driver has. The list is a
		// statement about WHICH KEYS WAIT, so a key that waits without being on it is invisible -
		// the step does not fail, it is slow, and a slow test reads as a large package.
		if key == "require_ssh" || key == "desktop" || key == "until_console" ||
			key == "until" || key == "until_any" || key == "expect" || key == "expect_any" ||
			key == "repeat_until" || key == "wait_for" || key == "wait" ||
			key == "adb_connect" || key == "adb" || key == "adb_privileged" ||
			key == "ssh" || key == "ssh_probe" {
			steps[0].Spec["timeout"] = 1
		}
		_, err := ex.Run(steps)
		if err != nil && strings.Contains(err.Error(), "no implementation") {
			t.Errorf("%q is claimed executable but the dispatcher has no branch for it", key)
		}
	}
}

// probeValue is a value of the right shape for each key, so the step reaches its branch.
func probeValue(key string) any {
	switch key {
	case "boot":
		return map[string]any{"os": "windows"}
	case "shutdown":
		return "kill"
	case "disk":
		return map[string]any{"from": "/nonexistent"}
	case "record":
		// A real path, so the step reaches the START branch. It then starts a sampler goroutine
		// that draws one frame, which the fake driver answers instantly - the test finishes
		// because the goroutine is stopped by the executor's own cleanup.
		return "/tmp/probe.mp4"
	case "compress":
		// no disk is known yet, so this refuses immediately after reaching its branch.
		return "/tmp/nothing.qcow2"
	case "require_ssh":
		return true
	// (an explicit timeout is added below so this branch does not really wait 900s)
	case "ssh":
		return "true"
	case "script":
		return "echo hi"
	case "desktop":
		// timeout 0 makes the wait zero-length, so the branch is reached and returns at once.
		return map[string]any{"desktop_family": "linux"} // not implemented -> refuses fast
	case "share_serve", "share_mount":
		return true
	case "scp":
		return []any{"/nonexistent", "/remote"}
	case "type":
		return "text"
	case "key":
		return "ret"
	case "at", "double":
		return []any{1, 1}
	case "wait":
		// `Has` treats 0 as absent, so a probe value of 0 would never reach the branch. A
		// fraction of a second reaches it and returns immediately.
		return 0.001
	}
	return true
}

// `record: false` WAS THE CASE THIS TEST EXISTED FOR, AND THE KEY IS GONE.
//
// It was a step whose only key was a boolean false, and `Step.Has` answers "present AND true", so
// every dispatcher branch ignored it and the step fell through to "no implementation". The fix was
// `hasKey`, which asks only about presence - and `hasKey` stays, because a key whose `false` is a
// statement is a shape the vocabulary still allows.
//
// The step itself cannot exist now: the recording starts with the run and `finishRecording` encodes
// it however the walk ended, so nothing writes a false-only step to dispatch.
// `disk_gb:` IS A COUNT, NOT A DURATION, AND THE TWO ARE EASY TO CONFUSE.
//
// `seconds()` RETURNS A time.Duration. Reading `disk_gb` through it turned 128 into 128e9 and qemu-img
// refused the request:
//
//	error: create the empty disk: qemu-img create: exit status 1
//	  (qemu-img: Invalid image size specified. Must be between 0 and 9223372036854775807.)
//
// - a message about the NUMBER that names neither the key nor the unit, so a reader is sent looking
// at qemu-img rather than at the step.
//
// A UNIT TEST WOULD HAVE CAUGHT IT, AND IT HAS TO BE ON THE ARGUMENT RATHER THAN THE OUTCOME: the
// step must hand qemu-img `128G`, not a duration-shaped string.
func TestDiskGBCreatesTheSizeItSays(t *testing.T) {
	d := &fakeDriver{}
	ex, steps := newExec(d, map[string]any{
		"name": "create the empty disk", "disk": "disk.qcow2", "disk_gb": 128,
	})
	ex.Work = t.TempDir()
	_, err := ex.Run(steps)

	// qemu-img IS NOT INSTALLED EVERYWHERE, so the two outcomes are told apart deliberately: a
	// missing binary is a skip, and a REACHED qemu-img that rejected the size is a failure of this
	// step's arithmetic.
	if err == nil {
		return // it created the disk, which is the answer
	}
	if strings.Contains(err.Error(), "qemu-img is not installed") {
		t.Skip("qemu-img is not installed here")
	}
	if strings.Contains(err.Error(), "Invalid image size") {
		t.Fatalf("the step built a size qemu-img rejected, which means `disk_gb` went through "+
			"`seconds()` and became a duration: %v", err)
	}
	// Anything else is a real failure of this step and should be seen.
	t.Fatalf("createDisk failed unexpectedly: %v", err)
}

// A SUCCESSFUL `require_ssh:` MAKES THE DRIVER DROP ITS CACHED CONNECTION.
//
// THE DRIVER KEEPS ONE SSH CONNECTION FOR THE RUN, and `require_ssh:` probes with a FRESH one. So a
// sequence of `reboot:` -> `require_ssh:` -> `ssh:` had the probe succeed and then the next `ssh:`
// write into the pre-reboot socket:
//
//	status: failed step 20/20 "read what this boot did"
//	error: read what this boot did: ssh session: EOF
//
// Measured on opensuse-tumbleweed, whose last step is exactly that. `Reboot()` closes the cache for
// the `reboot:` step, but a machine that restarts from `systemctl reboot` INSIDE an `ssh:` command
// never goes through it - and that is the case the probe exists for.
//
// THE RULE: whichever way the guest restarted, the connection that survives a successful probe is the
// one the probe made.
func TestAStepThatNeedsTheGuestRepairsADeadSession(t *testing.T) {
	// THE DRIVER OWNS THE REPAIR, NOT THE STEP, AND THAT IS THE POINT.
	//
	// `require_ssh:` USED TO DROP THE CACHED CONNECTION after a successful probe, because the probe
	// dials fresh while the driver kept one for the run. The fix worked for the one path that had it
	// and missed the other:
	//
	//	reboot: -> require_ssh: -> ssh:          the probe drops it, the ssh step reconnects
	//	ssh: systemctl reboot -> ssh:            NOTHING drops it, and the step reports `ssh session: EOF`
	//
	// MEASURED on opensuse-tumbleweed, whose last step is the second shape. So the repair moved to the
	// session: `sshSession.Do` retries once on a fresh connection when an operation fails because the
	// session died.
	//
	// THIS TEST READS WHICH LAYER HOLDS IT, because the property is "a step after a reboot works" and
	// the way it fails is silent: a type assertion against a method that does not exist compiles and
	// does nothing.
	sess, err := os.ReadFile("../daemon/sshsession.go")
	if err != nil {
		t.Skipf("no ssh session here (%v)", err)
	}
	src := withoutComments(string(sess))
	if !strings.Contains(src, "func (s *sshSession) Do(fn func(*guest.Client) error) error {") {
		t.Fatal("the ssh session has no `Do`, so nothing repairs a connection that died with a " +
			"reboot - and every step has to remember to DropGuest instead")
	}
	i := strings.Index(src, "func (s *sshSession) Do(")
	body := src[i:]
	if end := strings.Index(body[1:], "\nfunc "); end > 0 {
		body = body[:end+1]
	}
	if !strings.Contains(body, "isSessionGone(") {
		t.Fatal("`sshSession.Do` retries unconditionally or not at all; it must retry ONLY when the " +
			"error means the session died, or a failing command runs twice")
	}
	if !strings.Contains(body, "s.Drop()") || !strings.Contains(body, "s.Get()") {
		t.Fatal("`sshSession.Do` does not drop the dead connection and dial a new one, so the retry " +
			"uses the same broken socket")
	}

	// AND THE STEP ASKS THE DRIVER TO DO IT, when the driver has the capability.
	exec, err := os.ReadFile("executor.go")
	if err != nil {
		t.Fatal(err)
	}
	esrc := withoutComments(string(exec))
	if !strings.Contains(esrc, "DoGuest(func(*guest.Client) error) error") {
		t.Fatal("`doSSH` does not go through the driver's repairing entry point, so its connection " +
			"drops are back to being every step's problem")
	}
	if strings.Contains(esrc, "closer.DropGuest()") {
		t.Fatal("`require_ssh:` still drops the connection itself; that is the per-path fix that " +
			"missed `ssh: systemctl reboot`, and it is now the session's job")
	}
}

// `scp:` LOOKS FOR ITS FILE WHERE `write:` PUT IT.
//
// MEASURED ON MANJARO'S VM LEG:
//
//	copy the launcher in: scp bootstrap-launcher.sh -> /tmp/bootstrap-launcher.sh:
//	open bootstrap-launcher.sh: no such file or directory
//
// `write: launcher` WROTE IT - `internal/bootstrap` produces the script and `doWrite` joins the name
// onto the WORK DIRECTORY - and `doSCP` used the process's own working directory. Two places, one
// file, and the step that produced it was blamed for the step that looked elsewhere.
//
// THE WORK DIRECTORY WINS, with the caller's directory as the fallback: a file run by hand has its
// inputs beside it, and a file run by the daemon has them in `work`. `disk:` already followed exactly
// this rule.
func TestSCPLooksInTheWorkDirectory(t *testing.T) {
	body, err := os.ReadFile("actions.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	start := strings.Index(src, "func (e *Executor) doSCP(")
	if start < 0 {
		t.Fatal("no doSCP")
	}
	rest := src[start:]
	if end := strings.Index(rest[1:], "\nfunc "); end > 0 {
		rest = rest[:end+1]
	}
	if !strings.Contains(rest, "filepath.Join(e.Work, local)") {
		t.Fatal("`scp:` does not resolve a relative path against the work directory, so a file " +
			"`write:` produced there is reported missing")
	}
	// AND A MISSING FILE NAMES THE PLACES IT LOOKED, rather than a bare open(2) message.
	if !strings.Contains(rest, "no file to copy at") {
		t.Fatal("a missing source file reports the raw open error, which reads as a failed copy " +
			"rather than as a file that no step produced")
	}
}

// `desktop: linux` NAMES THE FAMILY, AND READING ONLY `os:` MADE EVERY ONE OF THEM WINDOWS.
//
// MEASURED ON UBUNTU'S INSTALL LEG, WHOSE FOURTH STEP IS `desktop: linux`:
//
//	wait for the live desktop: no windows desktop within 15m0s; drop=+17.8 (need 15) ratio=0.0x
//	(need 2) sat=-0.57 (need 0.10) on a 1920x1080 frame
//
// Fifteen minutes spent looking for a WINDOWS DESKTOP on an Ubuntu guest. The family came from
// `desktop_family` then `os:`, both empty on that step, and the fallback is `windows` - so the key's
// OWN VALUE was never read.

// A REGION MEASURED AT ONE RESOLUTION IS READ AT ANOTHER BY SCALING IT, NOT BY GIVING UP.
//
// MEASURED ON mx-install, THE FIRST install LEG EVER TO GET PAST THE DESKTOP GATE - it passed in five
// seconds once the Desktop ISO replaced the Server one - WHICH THEN FAILED THIRTY TIMES IN A ROW:
//
//	wait for the terms page: the framebuffer is 1024x768 but this region was measured at 1920x1080;
//	not reading it
//
// THE LIVE SESSION NEVER SWITCHED MODES. `-device VGA,xres=1920,yres=1080` tells the ADAPTER what it
// supports; the guest's X server still chose 1024x768 because nothing asked it for more. `ubuntu-vm`
// reads at 1920x1080 with the same qemu command, so the parameter is right and the difference is that
// an INSTALLED system has a display configuration and a live session does not.
//
// THE REGION IS A FRACTION OF THE PAGE, not a place on a screen: [690, 225, 1560, 265] on 1920x1080 is
// the right-hand content pane, and the same pane at 1024x768 is the same fraction of the frame. Scaling
// keeps what the measurement was about.
func TestARegionIsScaledWhenTheFramebufferIsAnotherSize(t *testing.T) {
	body, err := os.ReadFile("text.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	i := strings.Index(src, "func (e *Executor) readScreen(")
	if i < 0 {
		t.Fatal("no readScreen")
	}
	rest := src[i:]
	if end := strings.Index(rest[1:], "\nfunc "); end > 0 {
		rest = rest[:end+1]
	}
	// THE COMMENTS ARE STRIPPED FIRST: the note explaining this defect QUOTES the message it removed,
	// so a check over raw text fails on its own explanation - the same trap `stripGoComments` was
	// written for in the daemon package.
	rest = stripCommentsForTest(rest)
	// THE OLD BEHAVIOUR: report and read nothing. That is what failed thirty polls in a row.
	if strings.Contains(rest, "not reading it") {
		t.Fatal("`readScreen` still abandons the read on a size mismatch, so a live session at " +
			"1024x768 can never be read against a region measured at 1920x1080")
	}
	// AND THE SCALE IS THERE, multiplying by the frame's size and dividing by the measured one.
	for _, want := range []string{"region[0] * w / at[0]", "region[3] * h / at[1]"} {
		if !strings.Contains(rest, want) {
			t.Fatalf("the scaling does not compute %q", want)
		}
	}
	// AND IT SAYS SO IN THE LOG, because a scaled read is taken from somewhere other than where the
	// file said and that must be visible when the step fails.
	if !strings.Contains(rest, "reading the same place scaled to") {
		t.Fatal("the scaling is silent")
	}
}

// stripCommentsForTest removes `//` comments and /* */ blocks so a check can read the CODE.
func stripCommentsForTest(src string) string {
	var out []byte
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == '"' || c == '\'':
			quote := c
			out = append(out, c)
			i++
			for i < len(src) {
				out = append(out, src[i])
				if src[i] == '\\' && i+1 < len(src) {
					out = append(out, src[i+1])
					i += 2
					continue
				}
				if src[i] == quote {
					i++
					break
				}
				i++
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			i += 2
		default:
			out = append(out, c)
			i++
		}
	}
	return string(out)
}

// A SNAPSHOT IS A MIGRATION AND NOT `savevm`, AND THE GUEST IS WHY.
//
// `savevm` writes the machine state INTO a block device and requires every writable one to support
// internal snapshots. Every guest here boots UEFI, and UEFI's variable store is a pflash device - a
// firmware chip, not a qcow2 - so it refuses:
//
//	savevm s1  ->  Error: Device 'pflash1' is writable but does not support snapshots
//
// A migration stream is a file of its own and asks nothing of the devices. MEASURED on Linux, Windows
// and macOS guests; the macOS one additionally needs `tsc-frequency=` on the cpu or the invtsc blocker
// refuses the migration before it starts.
func TestASnapshotIsAMigrationNotSavevm(t *testing.T) {
	body, err := os.ReadFile("../qmp/qmp.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	if !strings.Contains(src, "func (c *Conn) MigrateToFile(") {
		t.Fatal("there is no migration-to-file primitive, so a snapshot has nowhere to go")
	}
	// AND IT WAITS, because `migrate` returns as soon as it starts and a caller that returned early
	// would let the next step delete a half-written file.
	if !strings.Contains(src, "QueryMigrate()") {
		t.Fatal("the migration is started and never waited for; `migrate` answers immediately")
	}
	// AND THE STATE FILE IS REMOVED FIRST: qemu appends to an existing one rather than truncating it.
	if !strings.Contains(src, "os.Remove(path)") {
		t.Fatal("an existing state file is not removed before writing, and qemu appends to it - a " +
			"second snapshot of a smaller guest would carry the first one's tail")
	}
	// AND IT REFUSES `savevm`, so nobody reintroduces it on the belief that it would work.
	if strings.Contains(src, "savevm") && strings.Contains(src, `raw.Savevm(`) {
		t.Fatal("`savevm` is used, and it cannot work on a UEFI guest")
	}
}

// `snapshot:` AND `boot_from:` ARE TWO KEYS BECAUSE THEY ANSWER TWO QUESTIONS.
//
// `boot: true` asks for a boot loader; `boot_from:` asks for a machine whose memory is already
// decided. A file that meant one and got the other takes minutes to notice - the guest boots happily
func TestUntilIsOneMeasurementAndRefusesATimeout(t *testing.T) {
	body, err := os.ReadFile("text.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)

	i := strings.Index(src, "func (e *Executor) doUntil(")
	if i < 0 {
		t.Fatal("doUntil is gone")
	}
	end := strings.Index(src[i:], "\n}\n")
	// the comment block above the function is where the contract is written
	head := src[maxInt(0, i-4000):i]

	if strings.Contains(src[i:i+end], "time.Sleep(") || strings.Contains(src[i:i+end], "continue") {
		t.Error("`doUntil` still loops. A retry belongs to `loop:`, where the file says how many " +
			"attempts and what it waits between them")
	}
	if !strings.Contains(head, "A BARE `until:` REMAINS FOR THE CASE THAT NEEDS NO RETRY") {
		t.Error("`doUntil` no longer says that a bare `until:` is a ONE-SHOT check; without that " +
			"the next reader will add a timeout back to it")
	}
	// AND THE TWO KEYS THAT USED TO CONFIGURE THE LOOP ARE REFUSED WITH IT.
	for _, key := range []string{`s.Has("timeout")`, `s.Has("poll")`, `s.Has("keep_alive")`} {
		if !strings.Contains(src[i:i+end], key) {
			t.Errorf("`doUntil` does not refuse %s; accepting it would restore the hidden retry", key)
		}
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// AN ACTION THAT CARRIES ITS OWN CHECK MUST BE DISPATCHED BEFORE THE CHECK.
//
// `runOne` IS A `switch` AND THE FIRST MATCHING CASE WINS, so the ORDER of two branches is the
// difference between "click, then read" and "read only, and never click".
//
// MEASURED ON macos-setup, THREE RUNS IN A ROW: `erase - open the sheet` carries `at_window:` to press
// the Erase icon and `expect_any:` to see the sheet appear. `expect_any` was matched first, so
// `doExpectAny` ran - which only reads the screen - and the step failed complaining that the sheet had
// not opened, on a click that never happened:
//
//	erase - open the sheet: did not advance: wanted "erase deleted" ...
//	                        <- and NOT ONE `clicked (1289, 271)` line in the log
//
// The coordinate was right the whole time. The comment above `run` already described this class of bug;
// `at_window` and `at_button` were simply on the wrong side of the line.
func TestActionsAreDispatchedBeforeTheirChecks(t *testing.T) {
	body, err := os.ReadFile("executor.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	i := strings.Index(src, "func (e *Executor) runOne(")
	if i < 0 {
		t.Fatal("no runOne")
	}
	rest := src[i:]

	// POSITIONS OF THE BRANCHES THAT MATTER, in the order the switch reads them.
	pos := func(needle string) int { return strings.Index(rest, needle) }
	atWindow := pos(`case s.Has("at_color"):`)
	atButton := pos(`case hasKey(s, "at_button"):`)
	expect := pos(`case s.Has("expect_any") || s.Has("expect"):`)
	if atWindow < 0 || atButton < 0 {
		t.Fatal("a branch this test looks for is missing, so it would pass vacuously")
	}
	// `expect:` HAS NO BRANCH ANY MORE, so the misordering it caused cannot recur: it is an argument
	// of `run:` and `doRun` calls the check after the typing. If a top-level branch returns, the two
	// assertions below apply again.
	if expect >= 0 {
		if atWindow > expect {
			t.Error("`at_color` is dispatched AFTER `expect`, so a step with both clicks NOTHING and " +
				"reports that the thing it was meant to open did not open")
		}
		if atButton > expect {
			t.Error("`at_button` is dispatched AFTER `expect`, for the same reason")
		}
	}
}

// A SHUTDOWN IS NOT A FAILURE, AND NEITHER IS ANYTHING THAT READS THE DISK AFTERWARDS.
//
// The dead-guest check refused EVERY step once the machine had exited, which broke the most ordinary
// ending a walk has:
//
//	shut the installed system down       <- the step's whole purpose is to stop the machine
//	publish the disk                     <- `qemu-img convert`, which needs the FILE, not a guest
//
//	error: publish the disk: the guest is no longer running, so this step cannot be done
//
// MEASURED ON windows, at step 14 of 16, after a complete installation.
//
// WHAT CANNOT SURVIVE is a step that expects an ANSWER - a click, a keystroke, a reading, an ssh
// command - so those are what the check refuses. The split is an allowlist of the file steps rather
// than a denylist of the guest ones: a key added later without a thought here is refused noisily after
// a shutdown, which is the safer way to be wrong.
func TestAStepThatNeedsNoGuestSurvivesAShutdown(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spec  map[string]any
		needs bool
	}{
		{"shutdown", map[string]any{"name": "x", "stop": "kill"}, false},
		{"stop and publish", map[string]any{"name": "x", "stop": "kill", "save": "out.qcow2"}, false},
		// `save:` ON ITS OWN IS THE LAST STEP OF A WALK, after the guest is already stopped.
		// MEASURED on android blissos16 install: 31 steps passed and the 32nd was refused with
		// "the guest is no longer running" - a `save` is a file copy, not a question for a guest.
		{"publish a stopped guest's disk", map[string]any{"name": "publish the disk", "save": "out.qcow2"}, false},
		{"rewrite a stopped guest's ESP", map[string]any{"name": "copy the loader",
			"esp_fallback": map[string]any{"disk": "disk", "from": "/EFI/BLISSOS"}}, false},
		{"stop recording", map[string]any{"name": "x", "record": false}, false},
		{"start recording", map[string]any{"name": "x", "record": "v.mp4"}, true},
		{"a click", map[string]any{"name": "x", "input": true, "click": []any{1, 2}}, true},
		{"a reading", map[string]any{"name": "x", "read": map[string]any{"name": "s"}}, true},
		{"a key", map[string]any{"name": "x", "input": true, "key": "ret"}, true},
		{"a wait", map[string]any{"name": "x", "settle": 5}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := walk.Step{Name: tc.name, Spec: tc.spec}
			if got := stepNeedsAGuest(s); got != tc.needs {
				t.Errorf("stepNeedsAGuest = %v, want %v; getting this wrong either refuses a step "+
					"that would have worked or lets one run against a machine that is gone", got, tc.needs)
			}
		})
	}
}

// withoutComments drops whole-line `//` comments, so a test that looks for code cannot be satisfied by
// prose that mentions it. It is a local copy rather than a shared helper because `internal/run` has no
// other source-reading test and a new exported helper for one caller is worse than eight lines.
func withoutComments(src string) string {
	var out []string
	for _, line := range strings.Split(src, "\n") {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "//") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// A `shutdown:` STEP HANDS THE DRIVER THE STEP'S OWN TIMEOUT IN SECONDS, NOT IN SQUARED SECONDS.
//
// `seconds()` WAS ALREADY A `time.Duration`, AND THE CALL SITE MULTIPLIED BY `time.Second` AGAIN:
//
//	timeout: 300  ->  1347804h7m  =  153.9 YEARS
//
// so `waitForExit` was given a deadline no run would ever reach, and a guest that did not power off
// was never killed. TWO STAGES SAT IN "stop the machine" FOR FORTY MINUTES while this was in the
// tree - macos-ssh and windows-vm-configure - and a `shutdown: acpi` stage looked healthy only because
// qemu exits on its own. THE UNIT IS THE WHOLE TEST: a grace of years and a grace of five minutes are
// one multiplication apart and only one of them ends.
func TestShutdownPassesTheStepsTimeoutAsASecondsDuration(t *testing.T) {
	cases := []struct {
		name string
		spec map[string]any
		want time.Duration
	}{
		{"the step names a timeout", map[string]any{"name": "x", "stop": "ssh", "timeout": 300}, 300 * time.Second},
		// NO TIMEOUT MEANS "THE DRIVER'S DEFAULT", which `graceOrDefault` reads as `<= 0`.
		{"no timeout is the driver's default", map[string]any{"name": "x", "stop": "acpi"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			driver := &fakeDriver{}
			e, steps := newExec(driver, tc.spec)
			if _, err := e.Run(steps); err != nil {
				t.Fatalf("the step failed: %v", err)
			}
			if len(driver.graces) != 1 {
				t.Fatalf("the machine was stopped %d time(s), want 1", len(driver.graces))
			}
			if got := driver.graces[0]; got != tc.want {
				t.Fatalf("grace is %v, want %v. A duration multiplied by a second twice is a deadline "+
					"of years, and a guest that never powers off is then never killed", got, tc.want)
			}
			if driver.graces[0] > time.Hour {
				t.Fatalf("grace is %v, which no shutdown step in this repository means", driver.graces[0])
			}
		})
	}
}
