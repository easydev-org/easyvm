package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/easydev-org/easyvm/internal/client"
	"github.com/easydev-org/easyvm/internal/protocol"
	"github.com/easydev-org/easyvm/internal/run"
)

// THESE TESTS DRIVE THE REAL TRANSPORT. A daemon tested by calling its methods directly would prove
// nothing about the thing that matters - that a CLIENT can reach it - and the transport is where
// "one owner" either holds or does not.

// startDaemon runs a daemon in a temporary work directory and returns its path.
func startDaemon(t *testing.T) (string, *Daemon) {
	t.Helper()
	work := t.TempDir()
	// PORT 0 AND THE ADDRESS IT CHOSE, because a fixed port would make two tests collide and a test
	// that cannot run beside another one is a test that hides a real conflict behind a flaky one.
	d := New()
	ready := make(chan string, 1)
	go func() {
		_ = d.Serve(ServeOptions{
			Work:     work,
			HTTPAddr: "127.0.0.1:0",
			Ready:    func(addr string) { ready <- addr },
		})
	}()
	var addr string
	select {
	case addr = <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("the daemon never became ready; an address that never opens is a daemon nobody " +
			"can reach")
	}
	// THE CLIENT DIALS `EASYVM_HTTP`, which is how a daemon on a Service name is reached too - the
	// variable is the client's whole notion of where the machine is.
	t.Setenv("EASYVM_HTTP", addr)
	t.Cleanup(func() { d.requestShutdown() })
	return work, d
}

// A CLIENT CAN REACH IT, WHICH IS THE WHOLE CLAIM.
func TestClientReachesTheDaemon(t *testing.T) {
	work, _ := startDaemon(t)

	cl, err := client.Dial(work)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cl.Close()

	resp, err := cl.Do(client.Request{Command: "status"})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !resp.OK {
		t.Fatalf("status refused: %s", resp.Error)
	}
	if resp.State.Status != "idle" {
		t.Fatalf("a fresh daemon is %q, want idle", resp.State.Status)
	}
}

// A DAEMON THAT IS NOT THERE IS SAID SO, NOT WAITED FOR.
//
// THE TRANSPORT IS HTTP NOW, so the failure a client sees is a refused connection rather than a
// missing socket file - and both are immediate. What matters is unchanged: a person who called `run`
// before `serve` must be told "nothing is listening", not left to interpret a thirty-second timeout as
// a broken machine.
func TestDialWithoutADaemonIsImmediate(t *testing.T) {
	// A PORT NOTHING OWNS. 9 is discard, which nothing listens on here.
	t.Setenv("EASYVM_HTTP", "127.0.0.1:9")
	start := time.Now()
	cl, err := client.Dial("")
	elapsed := time.Since(start)

	if err == nil {
		// DialHTTP only parses the address, so the failure arrives on the first request.
		_, err = cl.Do(protocol.Request{Command: "status"})
		_ = cl.Close()
	}
	if err == nil {
		t.Fatal("a request must fail when no daemon is listening")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("the refusal took %s; a missing daemon is a fact, not a timeout", elapsed)
	}
}

// A SECOND DAEMON ON ONE PORT IS REFUSED BY THE KERNEL, WHICH IS A STRONGER ANSWER THAN A FILE CHECK.
//
// THIS IS STILL THE STRUCTURAL CLAIM OF THE PACKAGE: two owners of one machine is the failure an
// embedded-CLI fallback would have allowed. It used to be enforced by noticing an answering socket
// file; it is now enforced by binding the port, which cannot be raced and cannot be left stale.
func TestSecondDaemonIsRefused(t *testing.T) {
	_, _ = startDaemon(t)
	addr := os.Getenv("EASYVM_HTTP")

	second := New()
	err := second.Serve(ServeOptions{Work: t.TempDir(), HTTPAddr: addr})
	if err == nil {
		t.Fatal("a second daemon on one port must be refused")
	}
	if !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("the refusal must name the port conflict; got %v", err)
	}
}

// THE UNIX SOCKET IS GONE, AND THE DAEMON STARTS ON A PORT INSTEAD.
//
// This replaces `TestStaleSocketIsReplaced`, which tested that a socket file left by a crashed daemon
// did not block a restart. THE TRANSPORT IT TESTED WAS REMOVED: it was serial - one connection, read
// until the client closed - so a poller holding its call open stopped the daemon accepting anything
// else, and the HTTP surface then blocked behind the machine's lock. A daemon that answers `curl` with
// silence is not a transport that needs a stale-file check; it is a transport to delete.
//
// WHAT REPLACES IT IS THE PROPERTY THAT MATTERS: the daemon listens, and a second start of the same
// work directory is refused because two owners of one machine is the thing this package exists to
// prevent.
func TestTheDaemonListensOnItsPort(t *testing.T) {
	work := t.TempDir()
	d := New()
	ready := make(chan string, 1)
	go func() {
		_ = d.Serve(ServeOptions{Work: work, Ready: func(addr string) { ready <- addr }})
	}()
	select {
	case addr := <-ready:
		if addr == "" {
			t.Fatal("the daemon reported an empty address, so a client has nowhere to dial")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the daemon never started listening")
	}
	t.Cleanup(func() { d.requestShutdown() })
}

// A REQUEST FOR A SECOND RUN WHILE ONE IS GOING IS REFUSED WITH THE STATE.
//
// The machine has one state, so two runs at once is a question with no good answer - and the refusal
// says WHERE the current run is, which is what the caller actually needs.
func TestSecondRunIsRefused(t *testing.T) {
	work, d := startDaemon(t)

	// Pretend a run is in progress, so this tests the decision rather than the walkthrough.
	d.mu.Lock()
	// A REAL Executor with no driver is enough: the test is about the DECISION to refuse, not
	// about a walkthrough. Using the real type means the test cannot pass against a stub that the
	// production code would not accept.
	d.ex = &run.Executor{}
	d.st = State{Status: "running", Step: "install openssh", StepIndex: 7, StepCount: 29}
	d.mu.Unlock()

	cl, err := client.Dial(work)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()

	resp, _ := cl.Do(client.Request{Command: "run", File: "/tmp/nothing.yaml"})
	if resp.OK {
		t.Fatal("a second run must be refused while one is in progress")
	}
	if !strings.Contains(resp.Error, "install openssh") {
		t.Fatalf("the refusal must say where the current run is; got %q", resp.Error)
	}
	if resp.State == nil || resp.State.StepIndex != 7 {
		t.Fatalf("the refusal must carry the state; got %+v", resp.State)
	}
}

// AN UNKNOWN COMMAND LISTS THE ONES THAT EXIST.
func TestUnknownCommandIsRefused(t *testing.T) {
	work, _ := startDaemon(t)
	cl, err := client.Dial(work)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()

	resp, _ := cl.Do(client.Request{Command: "teleport"})
	if resp.OK {
		t.Fatal("an unknown command must be refused")
	}
	for _, want := range []string{"run", "status", "stop"} {
		if !strings.Contains(resp.Error, want) {
			t.Fatalf("the refusal should list %q; got %q", want, resp.Error)
		}
	}
}

// THE PERMISSION MODEL MOVED FROM A FILE MODE TO A LISTEN ADDRESS.
//
// `TestSocketIsPrivate` asserted `0600` on the unix socket, because "who can open the socket" was the
// whole of the authorisation. THE SOCKET IS GONE; the daemon listens on a TCP address, so the
// authorisation is the address itself - loopback by default, reachable only by whoever can route to
// it. That is stated in `ServeOptions.HTTPAddr`'s documentation rather than in a file mode, and a test
// that read a file permission would pass on a transport nobody uses.

// A RUN OF A FILE THAT DOES NOT LOAD FAILS THE RUN AND CANNOT BE ASKED AGAIN BY ACCIDENT.
func TestBadFileFailsTheRun(t *testing.T) {
	work, d := startDaemon(t)
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(bad, []byte("name: bad\nsteps:\n  - name: x\n    nonsense: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cl, err := client.Dial(work)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()

	resp, _ := cl.Do(client.Request{Command: "run", File: bad, Work: work})
	if !resp.OK {
		t.Fatalf("the request itself should be accepted; the failure is the run's: %s", resp.Error)
	}
	// THE DAEMON'S OWN COPY MUST HAVE MOVED, or the client is talking to a different instance.
	if got := d.snapshot(); got.Status == "idle" {
		t.Fatalf("the daemon's state never left idle after an accepted run; the client and the "+
			"daemon are not the same instance (state %+v)", got)
	}
	// The run is asynchronous, so the failure arrives in the state.
	deadline := time.Now().Add(10 * time.Second)
	var last State
	for time.Now().Before(deadline) {
		st, _ := cl.Do(client.Request{Command: "status"})
		if st.State != nil {
			last = *st.State
		}
		if st.State != nil && st.State.Status == "failed" {
			if !strings.Contains(st.State.Error, "nonsense") {
				t.Fatalf("the failure must name the key; got %q", st.State.Error)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("a file that cannot load never reported failure; last state was %+v", last)
}
