package daemon

import (
	"os"
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

	p := &qemuProcess{done: make(chan struct{})}
	if !p.Alive() {
		t.Fatal("a machine whose `done` is open must report alive")
	}
	close(p.done)
	if p.Alive() {
		t.Fatal("a machine whose `done` is closed must not report alive - that channel IS the answer, " +
			"and reading it instead of a stale field is the whole point of this type")
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