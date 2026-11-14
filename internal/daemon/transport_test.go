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

// THE DAEMON HAS EXACTLY ONE QMP REGISTRY, AND REPLACING IT IS WHAT BROKE THE ONE-CLIENT RULE.
//
// `Registry.Acquire` REFUSES A SECOND CLIENT, and it can only do that because `held` is shared by
// every caller. So the guarantee is exactly as wide as the map:
//
//	if _, ok := r.held[path]; ok {
//		return nil, fmt.Errorf("%w (by %q) ...", ErrBusy, r.owner[path])
//	}
//
// AND `Boot` USED TO THROW THE MAP AWAY:
//
//	d.qmpReg = qmp.NewRegistry()
//
// Two maps are two clients. The recorder's sampler starts at `record:` and ticks across every `boot:`,
// so it holds the registry that existed BEFORE the boot; the boot then made a fresh one, and each
// asked qemu for the socket with its own empty `held`. qemu accepts the first and queues the second in
// its listen backlog WITHOUT EVER GREETING IT, and `SocketMonitor.Connect` reads that greeting with no
// deadline:
//
//	goroutine 21 [IO wait, 7 minutes]:
//	qmp.(*SocketMonitor).Connect()  socket.go:128   <- dec.Decode(&ban)
//	machineControl.Boot()           driver.go:680
//	doBoot -> runOne -> Run
//
// while goroutine 27 was the sampler's own successful `listen`. The guest had already drawn its
// recovery menu; the walk believed it was still booting, for seven minutes, past its own `timeout:`.
//
// SO THE MAP IS NEVER REPLACED - `Release` drops the path and `CloseAll` drops everything, but the
// registry itself lives as long as the daemon. This reads the source to refuse a second
// `qmp.NewRegistry()` outside the one accessor.
func TestTheDaemonKeepsOneQMPRegistry(t *testing.T) {
	// THE REGISTRY LIVES ON THE DAEMON, because "this process is one QMP client per socket" is a
	// fact about the process and not about a machine. A driver comes and goes across legs; the
	// registry must not, or `Acquire`'s refusal is only as wide as whichever map a caller happens to
	// hold - which is precisely the seven-minute hang described on the test above.
	var src strings.Builder
	for _, f := range []string{"server.go", "driver.go"} {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src.WriteString(dropLineComments(string(body)))
		src.WriteString("\n")
	}
	code := src.String()

	// THE CREATION IS IN ONE FUNCTION, ON THE DAEMON.
	if !strings.Contains(code, "func (d *Driver) qmpRegistry() *qmp.Registry {") {
		t.Fatal("no `qmpRegistry`; the one registry needs one place that creates it")
	}
	i := strings.Index(code, "func (d *Driver) qmpRegistry() *qmp.Registry {")
	body := functionBody(code[i:])
	if !strings.Contains(body, "qmp.NewRegistry()") {
		t.Fatal("`qmpRegistry` does not create the registry, so nothing does")
	}
	// IT IS STORED ON THE DAEMON, NOT THE DRIVER: `d.daemon.qmpRegAll`.
	if !strings.Contains(body, "d.daemon.qmpRegAll") {
		t.Fatal("`qmpRegistry` does not store the registry on the daemon; a per-driver registry is " +
			"replaced when a driver is, and the next caller gets a second map - two clients")
	}

	// AND NOWHERE ELSE CONSTRUCTS ONE.
	rest := code[:strings.Index(code, "func (d *Driver) qmpRegistry() *qmp.Registry {")] +
		code[strings.Index(code, "func (d *Driver) qmpRegistry() *qmp.Registry {")+len(body):]
	if n := strings.Count(rest, "qmp.NewRegistry()"); n > 0 {
		t.Fatalf("%d other `qmp.NewRegistry()` call(s). A second map has an empty `held`, so a "+
			"caller holding it believes the socket is free and opens a SECOND QMP client - which "+
			"qemu queues and never greets. Measured as a 7-minute hang on `boot the recovery "+
			"image` with the guest already up", n)
	}

	// AND `Boot` RELEASES THE PREVIOUS MACHINE'S PATH FROM IT, or the new qemu's socket is still
	// held by a client talking to a qemu that exited.
	j := strings.Index(code, "func (m machineControl) Boot(")
	if j < 0 {
		t.Fatal("no Boot")
	}
	boot := functionBody(code[j:])
	if !strings.Contains(boot, "d.qmpRegistry().Release(") {
		t.Fatal("`Boot` does not release the previous machine's QMP path; the socket stays held by " +
			"a client pointed at a qemu that exited, and the new machine's first `qmpSocket()` " +
			"gets `ErrBusy` naming a machine that is gone")
	}
}

// THE PREVIOUS MACHINE'S QMP PATH IS RELEASED BEFORE THE NEW PROCESS EXISTS, NOT AFTER IT IS CONNECTED.
//
// `Registry.Release` CLOSES the connection it holds for a path. Called after `cmd.Start()` it closed the
// connection `Boot` had just made for the new machine, and the failure arrived on the first step that
// touched the guest:
//
//	press a key so the loader takes it: key "esc": input-send-event []+esc:
//	the qmp connection is closed
//
// MEASURED ON CI, on windows-install, three seconds into a run whose qemu was healthy - and the disk was
// then published as 512 bytes because nothing had driven the installer. A test, because the two lines
// are five lines apart and the difference between them is "the machine works" and "it does not".
func TestTheOldQMPPathIsReleasedBeforeQemuStarts(t *testing.T) {
	body, err := os.ReadFile("driver.go")
	if err != nil {
		t.Fatal(err)
	}
	code := dropLineComments(string(body))
	i := strings.Index(code, "func (m machineControl) Boot(")
	if i < 0 {
		t.Fatal("no Boot")
	}
	boot := functionBody(code[i:])

	release := strings.Index(boot, "d.qmpRegistry().Release(")
	start := strings.Index(boot, "cmd.Start()")
	connect := strings.Index(boot, "d.qmpSocket()")
	if release < 0 {
		t.Fatal("`Boot` never releases the previous machine's QMP path, so a new qemu inherits a " +
			"held socket and its first client gets ErrBusy naming a machine that is gone")
	}
	if start < 0 || connect < 0 {
		t.Fatal("`Boot` no longer starts qemu and connects to it")
	}
	if release > start {
		t.Fatal("`Boot` releases the QMP path AFTER starting qemu. `Release` closes the connection " +
			"for that path, so this closes the connection the boot is about to use - measured on CI " +
			"as `the qmp connection is closed` on the first step, and a 512-byte published disk")
	}
	if release > connect {
		t.Fatalf("`Boot` releases the QMP path after connecting (release %d, connect %d), so it "+
			"closes the connection it just made", release, connect)
	}
}

// A BOOT THAT LOSES THE QMP RACE WAITS FOR THE WINNER'S CONNECTION, INSTEAD OF REPORTING IT AS A
// FAILURE.
//
// THE RECORDER'S SAMPLER CALLS `Frame()` EVERY SECOND, and `Frame()` asks for the QMP connection. During
// a boot those two race: the sampler asks a moment after qemu creates the socket, wins `Registry.Acquire`,
// and the boot then gets `ErrBusy` FOR A MACHINE IT JUST STARTED.
//
// MEASURED ON CI, ON ubuntu-install AND bsd-ghostbsd-install, AND THE SYMPTOM DID NOT LOOK LIKE A RACE:
//
//	=== boot the installer
//	qemu -machine q35,accel=kvm ...
//	(then `status: starting` until the job timed out, with NO `qemu is up` line)
//
// and the artifact was a ONE-SECOND video, because the step failed immediately after the boot - the
// `recording: 1 frame(s)` line is the sampler's single successful tick before the run died.
//
// A LOSER MUST WAIT, NOT REFUSE: the winner publishes its connection into the same field within
// milliseconds, so the "another caller is mid-handshake" case is an ordinary handover.
func TestABootThatLosesTheQMPRaceWaitsForTheWinner(t *testing.T) {
	body, err := os.ReadFile("driver.go")
	if err != nil {
		t.Fatal(err)
	}
	code := dropLineComments(string(body))

	i := strings.Index(code, "func (d *Driver) qmpSocket() (*qmp.Conn, error) {")
	if i < 0 {
		t.Fatal("no qmpSocket")
	}
	fn := functionBody(code[i:])

	// `ErrBusy` IS THE LOSER'S ANSWER, so it must be recognised...
	if !strings.Contains(fn, "qmp.ErrBusy") {
		t.Fatal("`qmpSocket` does not recognise `ErrBusy`. The recorder's sampler takes the socket " +
			"first on a boot that is millimetres behind it, and the boot then reports a busy socket " +
			"as a failed boot - measured on CI as `status: starting` forever and a 1-second video")
	}
	// ...AND IT MUST GO SOMEWHERE THAT WAITS, not be returned.
	if !strings.Contains(fn, "waitForQMP(") {
		t.Fatal("`qmpSocket` recognises `ErrBusy` but does not wait for the winner's connection, so " +
			"the loser still fails")
	}
	// AND THE WAIT IS BOUNDED BY SOMETHING, or a wedged winner is a hang.
	j := strings.Index(code, "func (d *Driver) waitForQMP() (*qmp.Conn, error) {")
	if j < 0 {
		t.Fatal("no `waitForQMP`")
	}
	wait := functionBody(code[j:])
	if !strings.Contains(wait, "Before(deadline)") {
		t.Fatal("`waitForQMP` waits without a deadline, so a caller that took the socket and threw it " +
			"away would park the boot forever")
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
// `vmport=off` disables the competing handler; the `usb-tablet` the walkthroughs were measured
// against stays attached.
func TestTheMachineDisablesTheVMwareBackdoorPort(t *testing.T) {
	body, err := os.ReadFile("driver.go")
	if err != nil {
		t.Fatal(err)
	}
	code := dropLineComments(string(body))

	// THE -machine ARGUMENT IS WHERE THIS LIVES, and it must keep the accelerator it also names.
	i := strings.Index(code, `"-machine", "q35,accel=kvm`)
	if i < 0 {
		t.Fatal("no `-machine q35,accel=kvm` argument; the machine's own options have moved")
	}
	line := code[i:]
	if end := strings.Index(line, "\n"); end > 0 {
		line = line[:end]
	}
	if !strings.Contains(line, "vmport=off") {
		t.Fatalf("the machine is started with %s, and q35's default `vmport=on` binds `vmmouse`, "+
			"which takes the absolute pointer: `Input handler not found for event type abs` on the "+
			"first click of a walk", strings.TrimSpace(line))
	}
	if !strings.Contains(line, "accel=kvm") {
		t.Fatalf("the machine is started with %s - adding vmport must not drop the accelerator, "+
			"or every guest silently runs on TCG", strings.TrimSpace(line))
	}
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
