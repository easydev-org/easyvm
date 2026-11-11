package run

import (
	"fmt"
	"os"
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
	log      []string
	clicks   [][2]int
	doubles  [][2]int
	keys     []string
	strokes  [][2]string
	typed    []string
	started  int
	boot     *BootSpec
	released bool
	stopped  []string
	frames   int
	guestNil bool
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
func (f *fakeDriver) Machine() Machine { return &fakeMachine{d: f} }
func (f *fakeDriver) Log(msg string)   { f.log = append(f.log, msg) }

type stubFrame struct{}

func (stubFrame) Path() string     { return "/dev/null" }
func (stubFrame) Size() (int, int) { return 1920, 1080 }
func (stubFrame) Words() []string  { return nil }

type fakeMachine struct{ d *fakeDriver }

func (m *fakeMachine) ReleaseDiff() error { m.d.released = true; return nil }

func (m *fakeMachine) Boot(spec BootSpec) error {
	// THE SPEC IS RECORDED, so a test can assert what a `boot:` step asked for without a machine.
	m.d.boot = &spec
	m.d.started++
	return nil
}
func (m *fakeMachine) Stop(method string) error {
	m.d.stopped = append(m.d.stopped, method)
	return nil
}
func (m *fakeMachine) Reboot() error   { return nil }
func (m *fakeMachine) IsRunning() bool { return m.d.started > 0 }

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
		map[string]any{"name": "click", "at": []any{100, 200}},
		map[string]any{"name": "double", "double": []any{10, 20}},
		map[string]any{"name": "key", "key": "ret"},
		map[string]any{"name": "combo", "key": "ctrl+alt+t"},
		map[string]any{"name": "type", "type": "hello"},
		map[string]any{"name": "type in field", "at": []any{5, 6}, "type": "world"},
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
		map[string]any{"name": "one", "at": []any{1, 1}},
		map[string]any{"name": "broken", "at": "not a pair"},
		map[string]any{"name": "three", "at": []any{3, 3}},
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
		map[string]any{"name": "flaky", "at": "not a pair", "optional": true},
		map[string]any{"name": "after", "at": []any{9, 9}},
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
		map[string]any{"name": "click", "at": []any{1, 1}},
		map[string]any{"name": "bad pair", "at": []any{1}},
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
		if key == "require_ssh" || key == "desktop" || key == "until_console" ||
			key == "until" || key == "until_any" || key == "expect" || key == "expect_any" ||
			key == "repeat_until" || key == "wait_for" || key == "wait" {
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

// A STEP WHOSE ONLY KEY IS A BOOLEAN FALSE STILL HAS TO DISPATCH.
//
// `record: false` STOPS A RECORDING, and `Step.Has` is false for it because `Has` answers "is this
// key present AND true". Every dispatcher branch tests `Has`, so the step matched nothing and fell
// through to "no implementation for this step's keys".
//
// THE COST WAS THE LAST STEP OF A STAGE THAT HAD ALREADY SUCCEEDED. Run 43035390408 passed the
// shutdown, published the disk and then failed:
//
//	status: failed step 29/29 "stop recording"
//	error:   stop recording: no implementation for this step's keys
//
// A run that did everything it was asked and then reported failure, one line from the end.
func TestBooleanFalseIsAnActionRatherThanAnAbsence(t *testing.T) {
	d := &fakeDriver{}
	ex, steps := newExec(d, map[string]any{"name": "stop recording", "record": false})
	ex.Work = t.TempDir()
	_, err := ex.Run(steps)
	if err != nil && strings.Contains(err.Error(), "no implementation") {
		t.Fatalf("`record: false` reached no branch: %v. It is an ACTION - stopping a recording - "+
			"and `Has` treats a false boolean as the key being absent", err)
	}
	// AND IT IS NOT SILENTLY COUNTED AS DONE. Nothing was recording, so the step has a real
	// complaint - and that complaint proves the branch ran rather than the dispatcher skipping it.
	if err == nil {
		t.Fatal("`record: false` with nothing recording must say so; a silent success is " +
			"indistinguishable from a dispatcher that never ran the step")
	}
	if strings.Contains(err.Error(), "no implementation") {
		t.Fatalf("the branch ran but reported no implementation: %v", err)
	}
}

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
func TestRequireSSHDropsTheCachedConnection(t *testing.T) {
	body, err := os.ReadFile("executor.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	start := strings.Index(src, "func (e *Executor) doRequireSSH(")
	if start < 0 {
		t.Fatal("no doRequireSSH")
	}
	rest := src[start:]
	end := strings.Index(rest[1:], "\nfunc ")
	if end > 0 {
		rest = rest[:end+1]
	}
	if !strings.Contains(rest, "DropGuest()") {
		t.Fatal("`require_ssh:` does not drop the driver's cached connection, so a step after a " +
			"guest-initiated reboot writes into a dead socket and reports `ssh session: EOF`")
	}
	// AND THE DRIVER MUST HAVE THE METHOD, OR THE TYPE ASSERTION SILENTLY DOES NOTHING.
	driver, err := os.ReadFile("../daemon/driver.go")
	if err != nil {
		t.Skipf("no daemon driver here (%v)", err)
	}
	if !strings.Contains(string(driver), "func (d *Driver) DropGuest()") {
		t.Fatal("DropGuest exists in the executor's type assertion and not on the driver, so the " +
			"assertion fails silently and nothing is dropped")
	}
}
