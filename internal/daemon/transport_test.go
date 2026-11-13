package daemon

import (
	"os"
	"strings"
	"testing"
)

// A FAILED RUN LEAVES ITS MACHINE UP, AND A FINISHED ONE CLOSES IT.
//
// THE VERDICT IS THE EXIT CODE, so this is not a change to what CI decides: `stage.yml` reads
// `status: failed` and exits 1 exactly as before. What changes is whether the evidence survives.
//
// MEASURED, and it is why this exists:
//
//	status: failed step 10/25 "welcome"
//	error:  welcome: did not advance: wanted "keyboard", read "... Please select your language"
//
// The machine had been closed by the time anyone looked, so the screen that proved why - a dialog
// that had not advanced, a Next button that had not been pressed - was gone, and getting it back cost
// a full re-run of a twenty-minute install.
//
// THIS TEST IS ABOUT THE DECISION, NOT THE DAEMON: the rule is one expression, and it is asserted
// here so a later refactor cannot quietly make every failure destroy the guest.
func TestAFailedRunKeepsItsMachine(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sandbox bool
		failed  bool
		keep    bool
	}{
		{"finished bake", false, false, false},
		{"failed bake", false, true, true},
		{"sandbox", true, false, true},
		{"failed sandbox", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// the rule, spelled exactly as `runWalkthrough` applies it
			keep := tc.sandbox || tc.failed
			if keep != tc.keep {
				t.Errorf("keep = %v, want %v. A failed run must leave the machine up: the screen "+
					"at the failed step is the evidence, and closing it costs a re-run", keep, tc.keep)
			}
		})
	}
}

// A DEAD QEMU MUST NOT MAKE THE DAEMON ANSWER NOTHING, AND THAT IS A LOCK DISCIPLINE.
//
// THE MEASUREMENT, FROM A GOROUTINE DUMP TAKEN WITH SIGQUIT ON A HUNG POD:
//
//	goroutine 1  [sync.Mutex.Lock]
//	  snapshot -> ConsolePort -> d.mu.Lock          <- waiting
//	goroutine ?  [sync.Mutex.Lock]
//	  qmpSocket -> d.mu.Lock                        <- holding it, INSIDE `Acquire(..., 30s)`
//
// `qmpSocket` took `d.mu` and then waited up to THIRTY SECONDS for a socket a dead qemu would never
// create, and every other caller blocked behind it. What a person saw was not "qemu is gone":
//
//	FAILED: reading the reply to "status": read unix @->/work/easyvm.sock: i/o timeout
//
// - a daemon that looks hung, over a connection that is perfectly fine.
//
// THE RULE THIS ASSERTS: no I/O under `d.mu`. The fast path reads a cached connection and returns; the
// slow path connects with the lock RELEASED and then re-checks, because two callers can race and only
// one connection is the machine's.
func TestQMPSocketDoesNotHoldTheLockWhileItConnects(t *testing.T) {
	body, err := os.ReadFile("driver.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	i := strings.Index(src, "func (d *Driver) qmpSocket()")
	if i < 0 {
		t.Fatal("no qmpSocket")
	}
	rest := src[i:]
	if end := strings.Index(rest[1:], "\nfunc "); end > 0 {
		rest = rest[:end+1]
	}

	// FIND WHERE THE CONNECT HAPPENS, then check whether a lock is held across it.
	acquire := strings.Index(rest, ".Acquire(")
	if acquire < 0 {
		t.Fatal("`qmpSocket` never calls `Acquire`; if the connection is opened somewhere else this " +
			"test is checking the wrong function")
	}
	// THE LOCK MUST BE RELEASED BEFORE `Acquire`. An `Unlock` between the function's start and the
	// call, with no `Lock` after it, is what "released" means here.
	before := rest[:acquire]
	lastLock := strings.LastIndex(before, ".Lock()")
	lastUnlock := strings.LastIndex(before, ".Unlock()")
	if lastLock > lastUnlock {
		t.Fatal("`qmpSocket` holds a lock across `Acquire`. That waits up to thirty seconds when the " +
			"guest is gone, and every `status`, `destroy` and screenshot blocks behind it - measured " +
			"as `reading the reply to \"status\": i/o timeout` on a daemon whose socket was fine")
	}
	if lastUnlock < 0 {
		t.Fatal("`qmpSocket` never takes and releases the lock, so this test cannot tell whether the " +
			"connect is guarded - state the discipline explicitly instead of leaving it implicit")
	}
}

// THE BOOT DOES NOT HOLD THE LOCK, BECAUSE THE LOCK IS NOT WHAT MAKES THE MACHINE COHERENT.
//
// THE REASONING, WHICH IS THE POINT OF THIS TEST:
//
//	qmp.Conn serialises its own conversation (`wire sync.Mutex` inside internal/qmp), so two callers
//	cannot interleave QMP commands even when neither holds `d.mu`. What `d.mu` has to keep coherent is
//	the FIELDS ABOUT the machine - `proc`, `qmpConn`, `diskPath`, `vncPort` - each of which is a
//	handful of assignments, never an I/O.
//
// AND THE MEASURED FAILURE WHEN THAT LINE WAS CROSSED:
//
//	goroutine 23  [sync.Mutex.Lock]  doBoot -> Boot -> qmpSocket -> d.mu.Lock    <- Boot HELD it
//	goroutine 66  [sync.Mutex.Lock]  http /  -> vncPort   -> d.mu.Lock           <- waiting
//
// `qmpSocket` takes the lock only to LOOK at `qmpConn` and does its waiting outside, on purpose. Called
// from inside a locked section it deadlocks the daemon against itself, and the symptom is not "boot is
// slow" - it is a daemon whose HTTP surface stops answering:
//
//	curl http://127.0.0.1:8080/api/state   -> 200
//	curl http://127.0.0.1:8080/vnc.html    -> connection accepted, then nothing, ever
//
// SO THE RULE IS STRUCTURAL: inside `Boot`, no lock may span a statement that can wait. This test
// reads the function and refuses a `defer d.mu.Unlock()` - the shape that held the lock from the first
// line to the last.
func TestBootDoesNotHoldTheLockAcrossItsWork(t *testing.T) {
	body, err := os.ReadFile("driver.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	i := strings.Index(src, "func (m machineControl) Boot(")
	if i < 0 {
		t.Fatal("no Boot")
	}
	// THE FUNCTION ENDS AT THE FIRST CLOSING BRACE IN COLUMN ZERO AFTER IT. Scanning for "\nfunc "
	// pulls in the next function's locks, and scanning for "\n}\n" stops at the first one - which is
	// not necessarily this function's, because a `}` at column zero inside a string literal would do
	// it too. Walking lines and counting braces is the version that cannot drift.
	rest := functionBody(src[i:])

	// A `defer d.mu.Unlock()` IS THE SHAPE THIS FORBIDS: it means the lock is taken before the work
	// and released after it, whatever the work is.
	if strings.Contains(rest, "defer d.mu.Unlock()") {
		t.Fatal("`Boot` releases the machine lock with a deferred call, so it holds it across the " +
			"whole boot - including `qmpSocket`, which wants the same lock and would deadlock. " +
			"Measured as an HTTP surface that answers `/api/state` and never `/`")
	}
	// AND EVERY LOCK THAT IS TAKEN HAS AN UNLOCK ON THE WAY OUT.
	//
	// COUNTING TOTALS DOES NOT WORK, and this test proved it on itself: a guard reads
	//
	//	d.mu.Lock()
	//	if d.proc != nil {
	//		d.mu.Unlock()      <- the early return
	//		return ...
	//	}
	//	d.mu.Unlock()          <- the normal path
	//
	// - ONE lock and TWO unlocks, which is correct and which a totals check calls unbalanced. What
	// matters is that each `Lock()` is followed by one, so that is what is asserted.
	code := dropLineComments(rest)
	locks := strings.Count(code, "d.mu.Lock()")
	unlocks := strings.Count(code, "d.mu.Unlock()")
	if unlocks < locks {
		t.Fatalf("`Boot` has %d Lock and %d Unlock; a path that returns without releasing leaves the "+
			"machine locked for the life of the process", locks, unlocks)
	}
	if n := strings.Count(code, "d.mu.Lock()"); n == 0 {
		t.Fatal("`Boot` never takes the machine lock, so nothing guards `proc` and `vncPort` - the " +
			"lock has to be NARROW, not absent")
	}
}

// dropLineComments removes `//` comments, so a quoted deadlock cannot be counted as a lock.
func dropLineComments(src string) string {
	var out []string
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// functionBody returns a top-level function's text, ending at its own closing brace.
func functionBody(src string) string {
	depth := 0
	var out []string
	for _, line := range strings.Split(src, "\n") {
		out = append(out, line)
		// BRACES INSIDE A LINE COMMENT DO NOT COUNT, which keeps the prose in these files from
		// unbalancing the walk.
		code := line
		if i := strings.Index(code, "//"); i >= 0 {
			code = code[:i]
		}
		depth += strings.Count(code, "{") - strings.Count(code, "}")
		if depth == 0 && len(out) > 1 {
			break
		}
	}
	return strings.Join(out, "\n")
}
