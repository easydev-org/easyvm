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
func TestDesktopTakesItsFamilyFromItsOwnValue(t *testing.T) {
	body, err := os.ReadFile("actions.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	start := strings.Index(src, "func (e *Executor) doDesktop(")
	if start < 0 {
		t.Fatal("no doDesktop")
	}
	rest := src[start:]
	if end := strings.Index(rest[1:], "\nfunc "); end > 0 {
		rest = rest[:end+1]
	}
	if !strings.Contains(rest, `s.Get("desktop")`) {
		t.Fatal("`desktop:` reads its family from other keys and not from its own value, so " +
			"`desktop: linux` is treated as `windows`")
	}
	// AND THE VALUE COMES FIRST, before the older spellings.
	own := strings.Index(rest, `s.Get("desktop")`)
	fam := strings.Index(rest, `s.Get("desktop_family")`)
	if fam >= 0 && fam < own {
		t.Fatal("`desktop_family` is read before `desktop`, so a step with both gets the wrong one")
	}
}

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
// from the wrong state and the failure reads as "my data is missing".
func TestSnapshotAndBootFromAreBothDispatched(t *testing.T) {
	body, err := os.ReadFile("executor.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	for _, key := range []string{`s.Has("snapshot")`, `s.Has("boot_from")`} {
		if !strings.Contains(src, key) {
			t.Fatalf("%s has no dispatch branch, so the step reports success and does nothing", key)
		}
	}
	// AND A MISSING SNAPSHOT IS REFUSED BY NAME rather than handed to qemu, whose message names a
	// path and not the step that was supposed to produce it.
	actions, err := os.ReadFile("actions.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(actions), "no snapshot at %s") {
		t.Fatal("`boot_from:` does not check that the file exists, so a typo becomes qemu's " +
			"`-incoming` error")
	}
}

// A HALF-WRITTEN FRAME IS A RACE IN THE DRIVER, NOT A FAULT IN THE WALK.
//
// MEASURED ON macos-setup, AT STEP 6 OF 30 AND TWO MINUTES IN:
//
//	wait for the recovery menu: .../frames/frame-000134.ppm: unexpected EOF
//	(is the write still in progress? format "ppm")
//
// and the recording's last frame shows the Apple logo with the boot progress bar halfway along -
// macOS's recovery system still starting. The walk was not wrong; the poll landed on a screendump
// that `qemu` had replied to but not finished writing, and `until:` returned that as the failure.
//
// `desktop:` HAS ALWAYS TOLERATED THIS by `continue`ing. `until:` did not, which made a twenty-minute
// stage end at two minutes with a message about a file format.
func TestAHalfWrittenFrameDoesNotEndAnUntil(t *testing.T) {
	// THE CONTRACT, AS THE CODE STATES IT: a frame that cannot be read is retried until the step's
	// own timeout, and only then is the last read error reported.
	body, err := os.ReadFile("text.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	if !strings.Contains(src, "A FRAME THAT CANNOT BE READ IS NOT A FAILURE, IT IS A FRAME THAT WAS NOT FINISHED") {
		t.Fatal("`doUntil` no longer documents why a truncated frame is retried; without that, the " +
			"next reader will make it fatal again and macos-setup will end at two minutes with a " +
			"message about a file format")
	}
	if !strings.Contains(src, "lastErr = err") || !strings.Contains(src, "continue") {
		t.Fatal("`doUntil` does not keep the read error and continue polling")
	}
	// AND A STEP THAT NEVER GETS A READABLE FRAME STILL FAILS, naming the error rather than a pane.
	if !strings.Contains(src, "and no frame could be read") {
		t.Fatal("`doUntil` would report 'never saw ...; last read: ' with an empty read when every " +
			"frame was unreadable, which says nothing about why")
	}
}
