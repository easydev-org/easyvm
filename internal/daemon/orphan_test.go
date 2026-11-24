package daemon

import (
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/easydev-org/easyvm/internal/qmp"
)

// A MACHINE THAT HAS STOPPED MUST LEAVE NOTHING BEHIND.
//
// EIGHT OCCURRENCES OF ONE PATTERN ON THE POD, and every one was the same fact stored twice:
//
//	easyvm destroy  ->  [easyvm] the machine was destroyed    (exit 0)
//	ps              ->  383 Sl  qemu-system-x86               (still running)
//	next boot       ->  this qmp socket is already held by this process
//
// The daemon kept a process field, a connection cache AND a process-wide registry of socket paths,
// and a boot could leave any one of them claiming a machine that was gone. The workaround was
// `kill -9` by hand, and a fresh pod always worked because its registry was empty.
//
// THE MODEL NOW IS ONE OBJECT: `qemuProcess` owns the process, the socket path and the connection,
// and `Alive()` is the only answer to "is this machine up". `watch` closes the connection when it
// reaps the process, so there is no map to clear and no cache to keep in step.
//
// These tests cannot start a real qemu, so they drive the parts that do not need one: the liveness
// answer, the request queue's behaviour after an exit, and the guarantee that a dead machine is
// reported as gone rather than as busy.

// A qemuProcess with no process is not alive, and every call on it is safe.
func TestANilOrStoppedMachineIsNotAlive(t *testing.T) {
	var none *qemuProcess
	if none.Alive() {
		t.Fatal("a nil machine must not report itself alive")
	}

	// A LIVE CHILD, so `Alive` has something to probe. `sleep` is a real process; the earlier version
	// of this test used a bare struct and passed only because `Alive` was a channel test - which is
	// exactly the assumption CI disproved (a machine that had exited still read as running).
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start a child process here: %v", err)
	}
	p := &qemuProcess{cmd: cmd, done: make(chan struct{})}
	if !p.Alive() {
		t.Fatal("a running child must report alive")
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	// THE PROCESS IS GONE BUT `done` IS STILL OPEN, which is the state CI measured: `watch` had not
	// closed it yet and every reader saw a live machine.
	if p.Alive() {
		t.Fatal("a killed, reaped child must not report alive even before `done` closes - the kernel " +
			"probe is what makes `wait_until: stopped` answer in seconds rather than out its budget")
	}
	close(p.done)
	if p.Alive() {
		t.Fatal("a machine whose `done` is closed must not report alive")
	}
}

// ASKING A STOPPED MACHINE IS `errMachineGone`, NOT A SOCKET CONFLICT.
//
// The message matters: `this qmp socket is already held by this process (by "easyvm")` sent every
// reader looking for a second client, on a machine that had simply exited.
func TestAskingAStoppedMachineSaysItIsGone(t *testing.T) {
	p := &qemuProcess{done: make(chan struct{})}
	close(p.done)

	err := p.Run(func(*qmp.Conn) error { return nil })
	if err == nil {
		t.Fatal("a command against a stopped machine must fail")
	}
	if err != errMachineGone {
		t.Fatalf("got %v, want errMachineGone - a stopped machine is GONE, not busy", err)
	}
}

// THE OWNER GOROUTINE ANSWERS EVERY QUEUED REQUEST, INCLUDING ON THE WAY OUT.
//
// A caller that queued a command just as the machine exited must not block for ever: `serve` drains
// what arrived and answers `errMachineGone`, which is why `Run`'s second select exists.
func TestQueuedRequestsAreAnsweredWhenTheMachineExits(t *testing.T) {
	p := &qemuProcess{
		done: make(chan struct{}),
		reqs: make(chan qmpRequest),
	}
	go p.serve(func() (*qmp.Conn, error) { return nil, errMachineGone })

	var wg sync.WaitGroup
	results := make([]error, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = p.Run(func(*qmp.Conn) error { return nil })
		}(i)
	}
	// Let the callers queue, then end the machine underneath them.
	time.Sleep(50 * time.Millisecond)
	close(p.done)

	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("callers blocked after the machine exited; `serve` must drain its queue and answer")
	}
	for _, err := range results {
		if err == nil {
			t.Fatal("a command against a machine that exited must not report success")
		}
	}
}

// A STARTED PROCESS IS REGISTERED BEFORE ANYTHING ELSE CAN FAIL.
//
// This is the orphan that `destroy` could not kill: `cmd.Start()` succeeded, the handshake then
// failed, and the function returned without the process being owned by anything. It is asserted on
// the source because the alternative is starting a real qemu in a unit test.
func TestBootOwnsTheProcessBeforeItCanFail(t *testing.T) {
	src, err := readFileString("driver.go")
	if err != nil {
		t.Fatal(err)
	}
	start := indexOf(src, "qemu, err := startQemu(cmd")
	if start < 0 {
		t.Fatal("`Boot` no longer starts the process through `startQemu`; the ownership this whole " +
			"type exists for has been lost")
	}
	after := src[start:]
	guard := indexOf(after, "bootFailed := true")
	if guard < 0 {
		t.Fatal("the failure guard is gone: an error returned after `startQemu` would leave a live " +
			"qemu that nothing owns, which is exactly the orphan `destroy` reported success over")
	}
	if indexOf(after, "qemu.Kill()") < 0 {
		t.Fatal("the failure path does not kill the process it started")
	}
	// AND THE OLD SHAPE MUST NOT COME BACK.
	if indexOf(src, "d.proc = cmd") >= 0 {
		t.Fatal("`d.proc` is assigned again; the process is owned by `qemuProcess` and a second field " +
			"would be a second answer to the same question")
	}
}

// WHICH CONNECTION TO USE, WHEN, AND WHETHER IT NEEDS REPAIRING, belongs to the machine - not to a
// daemon-wide map. This pins that the registry is gone rather than merely unused.
func TestThereIsNoProcessWideSocketRegistry(t *testing.T) {
	src, err := readFileString("driver.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, dead := range []string{"qmpRegistry", "qmpRegistryLocked", "qmpRegAll"} {
		if indexOf(src, dead) >= 0 {
			t.Fatalf("%q is back; a map that outlives every machine is a fact stored in a second "+
				"place, which is what produced eight orphaned-socket failures", dead)
		}
	}
}

func readFileString(name string) (string, error) {
	b, err := os.ReadFile(name)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func indexOf(hay, needle string) int {
	return strings.Index(hay, needle)
}
// A BOOTED MACHINE'S CONNECTION KNOWS WHICH KIND OF POINTER THE GUEST HAS.
//
// MEASURED ON THE POD, 2026-11-24, straight after the registry was deleted: `click` returned
// `{"ok":true}` and the pointer did not move on a Windows Server Setup page whose list moved for the
// ARROW KEYS - so the keyboard was fine and only the pointer was wrong.
//
// THE CAUSE WAS OWNERSHIP, NOT PROTOCOL. The boot publishes its connection through `SetConnection`,
// and only `connection()` - the lazy path - was setting the flag. A booted machine therefore answered
// `HasAbsolutePointer() == false` and every `moveTo` sent RELATIVE events to a tablet, which ignores
// them: a click that lands nowhere, reported as success.
func TestAConnectionPublishedByBootKeepsItsPointerKind(t *testing.T) {
	// A CONNECTION NOBODY TOLD BEHAVES LIKE A TABLET, which is the safe default - so the test asks
	// the OTHER question: a machine that says it has NO tablet must produce a connection that also
	// says so. That is the direction the boot was getting wrong.
	p := &qemuProcess{done: make(chan struct{}), reqs: make(chan qmpRequest), pointer: false}
	c := &qmp.Conn{}
	p.SetConnection(c)
	if c.HasAbsolutePointer() {
		t.Fatal("a machine booted with NO tablet produced a connection that thinks it has one; every " +
			"`click:` would then send absolute events to a PS/2 pointer and land nowhere - the " +
			"ToaruOS/Hurd defect with the sides swapped")
	}
	// AND THE OTHER DIRECTION, which is the one that was measured broken.
	p2 := &qemuProcess{done: make(chan struct{}), reqs: make(chan qmpRequest), pointer: true}
	c2 := &qmp.Conn{}
	p2.SetConnection(c2)
	if !c2.HasAbsolutePointer() {
		t.Fatal("a booted machine's connection does not know the guest has a tablet, so every " +
			"`click:` sends relative events to an absolute device and lands nowhere")
	}
	close(p.done)
}

// AND THE FACT REACHES `dialQMP` BEFORE IT DIALS.
//
// `dialQMP` reads `bootArgs` to decide the pointer kind. MEASURED: with that assignment AFTER the
// handshake, the dial saw an empty argument list, concluded there was no tablet, and mislabelled the
// connection. The order is the fix, so the order is what is asserted.
func TestBootRecordsItsArgumentsBeforeTheHandshake(t *testing.T) {
	src, err := readFileString("driver.go")
	if err != nil {
		t.Fatal(err)
	}
	assign := indexOf(src, "d.bootArgs = append([]string{spec.Machine}, spec.Args...)")
	dial := indexOf(src, "conn, err := d.dialQMP()")
	if assign < 0 || dial < 0 {
		t.Fatal("`Boot` no longer records its arguments or no longer dials")
	}
	if assign > dial {
		t.Fatal("`Boot` records `bootArgs` AFTER it dials. `dialQMP` reads them to decide how to move " +
			"the pointer, so the connection is labelled from an empty list and every click is sent as " +
			"a relative event to a tablet")
	}
}
