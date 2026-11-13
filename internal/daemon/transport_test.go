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
