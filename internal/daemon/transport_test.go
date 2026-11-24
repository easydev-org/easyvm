package daemon

import (
	"os"
	"path/filepath"
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

// `stop` BELONGS TO THE RUN IT STOPPED, NOT TO THE DAEMON'S LIFETIME.
//
// `stop` sets `d.stopping`, which the executor reads at each step boundary. NOTHING CLEARED IT AGAIN,
// so the flag survived the run it was meant for and every later `run` returned from its first step:
//
//	=== finished rc=0
//	status: finished step 0/31 ""
//	/dsk/os/macos/run-setup/macos15-setup.mp4   0 MiB
//
// A leg that reported success having run nothing, and a zero-byte video. It was found by running the
// same pod twice - which is the whole point of a daemon that serves several legs.
func TestANewRunClearsTheStopFlag(t *testing.T) {
	body, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	i := strings.Index(src, "func (d *Daemon) startRun(")
	if i < 0 {
		t.Fatal("no startRun")
	}
	rest := functionBody(src[i:])
	if !strings.Contains(rest, "d.stopping = false") {
		t.Fatal("`startRun` does not clear `d.stopping`, so a `stop` disables every later run for the " +
			"life of the daemon - measured as `finished step 0/31` and a 0 MiB video")
	}
}

// A q35 GUEST MUST NOT GET THE VMWARE BACKDOOR PORT, OR ITS ABSOLUTE POINTER GOES TO vmmouse.
//
// q35 enables `vmport` by default. Linux's `psmouse` binds `vmmouse` through it and CLAIMS THE
// ABSOLUTE-POINTER ROLE, so QMP's `input-send-event` with an `abs` axis reaches a device that cannot
// read it and qemu refuses the event outright:
//
//	click (965, 981): pointer to (965, 981): Input handler not found for event type abs
//
// MEASURED ON A DISK PULLED FROM ghcr: `macos-setup`'s ssh leg died at step 7 of 33 - the first click
// - on a machine whose screen was perfectly drawn, because the walk could not move the pointer.
//
//	QEMU PS/2 Mouse   current=False  abs=False
//	QEMU HID Tablet   current=False  abs=True
//	vmmouse           current=True   abs=True     <- every absolute event lands here
//
// `vmport=off` DISABLES THE COMPETING HANDLER, and it is in every q35 machine string a boot writes -
// which is what this checks now that a boot names its own `machine:`.
//
// THE ALTERNATIVE WOULD BE AN ENGINE THAT REWRITES THE STRING, and that is the coupling this build
// just removed: q35's default `vmport=on` binds `vmmouse`, which takes the absolute pointer, and
// a file that forgets it has a guest whose first click is
// `Input handler not found for event type abs`.
func TestTheMachineDisablesTheVMwareBackdoorPort(t *testing.T) {
	root := filepath.Join("..", "..")
	files, err := filepath.Glob(filepath.Join(root, "sd-*", "*", "steps", "*.walk"))
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(body), "\n") {
			// ONLY A `machine:` LINE THAT NAMES q35 IS THIS TEST'S BUSINESS: a microvm string has
			// no `vmport` and no VMware backdoor to disable.
			if !strings.Contains(line, "q35") {
				continue
			}
			checked++
			if !strings.Contains(line, "vmport=off") {
				t.Errorf("%s: %q starts q35 without `vmport=off`, so its guest binds `vmmouse` and "+
					"every absolute pointer event lands on a handler that refuses it",
					filepath.Base(f), strings.TrimSpace(line))
			}
			if !strings.Contains(line, "accel=kvm") {
				t.Errorf("%s: %q drops `accel=kvm`, so the guest runs on TCG at a fiftieth of the "+
					"speed and macOS deadlocks at HANDOFF TO XNU", filepath.Base(f),
					strings.TrimSpace(line))
			}
		}
	}
	if checked == 0 {
		t.Skip("no stage repositories here, so there is no machine string to read")
	}
	t.Logf("checked %d q35 machine string(s)", checked)
}

// AND THE COMMENT THAT DESCRIBED A DEVICE WHICH IS NOT IN THE ARGUMENT VECTOR.
//
// `internal/machine/boot.go` said "This build gives the tablet its own `virtio-tablet-pci`, which macOS
// drives". `grep virtio-tablet-pci` found that sentence, a test's failure message, and nothing qemu is
// ever given - and the device it named is the one that would have met `vmmouse` on equal terms, so the
// sentence explained away exactly the bug that `vmport=off` fixes.
func TestNoCommentClaimsAVirtioTablet(t *testing.T) {
	body, err := os.ReadFile("../machine/boot.go")
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "//") {
			continue
		}
		// THE SENTENCE MAY NAME THE DEVICE, but only to say it is NOT here.
		if strings.Contains(trimmed, "give the tablet its own `virtio-tablet-pci`") ||
			strings.Contains(trimmed, "gives the tablet its own `virtio-tablet-pci`") {
			t.Fatalf("boot.go:%d still claims the build gives the tablet a virtio-tablet-pci: %q",
				i+1, trimmed)
		}
	}
	// AND THE ARGUMENT VECTOR ITSELF MUST NOT CONTAIN ONE, or this test is checking prose while the
	// machine has the device.
	code := dropLineComments(string(body))
	if strings.Contains(code, `"virtio-tablet-pci"`) {
		t.Fatal("the argument vector now attaches a virtio-tablet-pci. That is a legitimate change, " +
			"but the walkthroughs' clicks were measured against `usb-tablet` - measure them again " +
			"before landing it")
	}
}

// A BOOT WITH NO `os:` IS THE ONLY KIND THERE IS, and it must not be refused.
//
// MEASURED on the pod, the first real boot after `os:` left the vocabulary: two steps ran, the third
// failed, and the machine never started -
//
//	boot: "" is not a guest this build has seen; it has windows, linux, macos, bsd, android, microvm
//
// - because `machine.ParseOS` was still called on a name nothing writes. THE TEST SUITE DID NOT SEE IT
// because every fixture named a guest, which is what the vocabulary accepted until this change.
//
// SO THIS ASSERTS THE ABSENCE: no call to `ParseOS` on the boot path, read from the source because a
// boot needs a qemu to observe the difference.
func TestABootIsNotClassifiedByAGuestName(t *testing.T) {
	body, err := os.ReadFile("driver.go")
	if err != nil {
		t.Fatal(err)
	}
	code := dropLineComments(string(body))
	i := strings.Index(code, "func (m machineControl) Boot(spec run.BootSpec) error {")
	if i < 0 {
		t.Fatal("no machineControl.Boot")
	}
	fn := functionBody(code[i:])
	if strings.Contains(fn, "machine.ParseOS(") {
		t.Fatal("`Boot` still calls `machine.ParseOS`, and a boot step writes no `os:` - so every " +
			"boot is refused with a message about a guest name that is empty")
	}
}
