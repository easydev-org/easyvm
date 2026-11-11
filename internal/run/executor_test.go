package run

import (
	"fmt"
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

func (m *fakeMachine) Start() error { m.d.started++; return nil }
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
		if key == "require_ssh" || key == "desktop" {
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
