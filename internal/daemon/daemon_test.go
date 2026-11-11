package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/easydev-org/easyvm/internal/client"
	"github.com/easydev-org/easyvm/internal/run"
)

// THESE TESTS DRIVE THE REAL SOCKET. A daemon tested by calling its methods directly would prove
// nothing about the thing that matters - that a CLIENT can reach it - and the transport is where
// "one owner" either holds or does not.

// startDaemon runs a daemon in a temporary work directory and returns its path.
func startDaemon(t *testing.T) (string, *Daemon) {
	t.Helper()
	work := t.TempDir()
	d := New()
	ready := make(chan string, 1)
	go func() {
		_ = d.Serve(ServeOptions{
			Work:  work,
			Ready: func(sock string) { ready <- sock },
		})
	}()
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("the daemon never became ready; a socket that does not appear is a daemon " +
			"nobody can reach")
	}
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

// AND A DAEMON THAT IS NOT THERE IS SAID SO, NOT WAITED FOR.
//
// The common mistake is calling `run` before `serve`. A client that hung for thirty seconds and then
// reported a timeout would send the reader looking at the machine.
func TestDialWithoutADaemonIsImmediate(t *testing.T) {
	work := t.TempDir()
	start := time.Now()
	_, err := client.Dial(work)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("dial must fail when no daemon is listening")
	}
	if !strings.Contains(err.Error(), "no socket at") {
		t.Fatalf("the message must say there is no socket there; got %v", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("the refusal took %s; a missing daemon is a fact, not a timeout", elapsed)
	}
}

// A SECOND DAEMON ON ONE SOCKET IS REFUSED BY NAME.
//
// THIS IS THE STRUCTURAL CLAIM OF THE PACKAGE. Two owners of one machine is the failure that an
// embedded-CLI fallback would have allowed, and the daemon is where it becomes impossible.
func TestSecondDaemonIsRefused(t *testing.T) {
	work, _ := startDaemon(t)

	second := New()
	err := second.Serve(ServeOptions{Work: work})
	if err == nil {
		t.Fatal("a second daemon on one socket must be refused")
	}
	if !strings.Contains(err.Error(), "already listening") {
		t.Fatalf("the refusal must say a daemon is already there; got %v", err)
	}
	// AND IT MUST NOT HAVE REMOVED THE FIRST ONE'S SOCKET. A stale-socket cleanup that ran on a
	// live daemon would take the machine away from its owner.
	if _, err := os.Stat(SocketPath(work)); err != nil {
		t.Fatalf("the first daemon's socket was removed: %v", err)
	}
}

// A SOCKET LEFT BY A CRASHED DAEMON DOES NOT BLOCK A RESTART.
//
// The distinction is measured rather than assumed: a socket file that accepts a connect and reads
// EOF is a dead one, and refusing to start because of it would be a daemon that cannot be restarted
// after the crash it exists to survive.
func TestStaleSocketIsReplaced(t *testing.T) {
	work := t.TempDir()
	sock := SocketPath(work)

	// A socket nobody listens on: create it and close it, which is exactly what a killed process
	// leaves behind.
	ln, err := listenUnix(sock)
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()

	if socketAnswers(sock) {
		t.Fatal("a closed socket must not answer; this test cannot distinguish the cases")
	}

	d := New()
	ready := make(chan string, 1)
	go func() {
		_ = d.Serve(ServeOptions{Work: work, Ready: func(s string) { ready <- s }})
	}()
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("a stale socket must not stop a restart")
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

// THE SOCKET IS NOT WORLD-READABLE. There is no token on this channel; the permission is the
// security model, so it is asserted rather than assumed.
func TestSocketIsPrivate(t *testing.T) {
	work, _ := startDaemon(t)
	fi, err := os.Stat(SocketPath(work))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("the socket is %04o; it must be 0600, because the permission IS the security "+
			"model on this channel", perm)
	}
}

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
