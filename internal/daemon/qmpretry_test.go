package daemon

import (
	"errors"
	"sync"
	"testing"

	"github.com/easydev-org/easyvm/internal/qmp"
)

// THE MACHINE OWNS ITS CONNECTION, AND THESE ARE THE PROPERTIES THAT MATTER ABOUT THAT.
//
// The tests here used to drive `d.qmp`, `d.qmpSocket` and `d.qmpRetry` directly, because the daemon
// kept a connection cache beside a process-wide registry of socket paths and both had to be kept in
// step by hand. That pairing is gone: `qemuProcess` holds the connection, its owner goroutine is the
// only reader, and a dead process closes it.
//
// What still needs holding is the behaviour callers depend on: a cached connection is handed out
// without a probe, a command that succeeds runs once, a machine's own error is returned unchanged, and
// only `ErrClosed` triggers a repair.

// A CACHED CONNECTION COMES BACK UNPROBED.
//
// A probe here would close a connection another caller is using - MEASURED through the daemon's own
// endpoint as `type: release modifiers: the qmp connection is closed` on the SECOND command of every
// request, while the first succeeded.
func TestACachedConnectionIsHandedOutWithoutProbing(t *testing.T) {
	p := &qemuProcess{done: make(chan struct{}), reqs: make(chan qmpRequest)}
	dead := &qmp.Conn{}
	p.SetConnection(dead)

	if got := p.Connection(); got != dead {
		t.Fatal("the cached connection was not returned; a probe that fails closes a connection " +
			"another caller may be using")
	}
	close(p.done)
}

// A COMMAND AGAINST A WORKING MACHINE RUNS EXACTLY ONCE.
//
// `Run` hands the command to the owner goroutine, which uses the cached connection. A successful
// command must not cost a reconnect, and the count is what proves it.
func TestAWorkingCommandRunsExactlyOnce(t *testing.T) {
	p := &qemuProcess{done: make(chan struct{}), reqs: make(chan qmpRequest)}
	live := &qmp.Conn{}
	p.SetConnection(live)
	go p.serve(func() (*qmp.Conn, error) { return live, nil })

	calls := 0
	if err := p.Run(func(*qmp.Conn) error { calls++; return nil }); err != nil {
		t.Fatalf("a command on a working connection: %v", err)
	}
	if calls != 1 {
		t.Fatalf("the command ran %d time(s); a successful command must run exactly once", calls)
	}
	if p.Connection() != live {
		t.Fatal("a successful command replaced the working connection")
	}
	close(p.done)
}

// ONLY `ErrClosed` MEANS "THE TRANSPORT IS GONE".
//
// A QMP reply that says the device does not exist is the MACHINE'S answer, and the distinction is
// what the repair keys on. Getting it wrong sends the same keystroke twice.
func TestAnOrdinaryErrorIsReturnedUnchanged(t *testing.T) {
	p := &qemuProcess{done: make(chan struct{}), reqs: make(chan qmpRequest)}
	live := &qmp.Conn{}
	p.SetConnection(live)
	go p.serve(func() (*qmp.Conn, error) { return live, nil })

	calls := 0
	refused := errors.New("the device 'usb-kbd' does not exist")
	err := p.Run(func(*qmp.Conn) error { calls++; return refused })
	if !errors.Is(err, refused) {
		t.Fatalf("got %v, want the machine's own error unchanged", err)
	}
	if calls != 1 {
		t.Fatalf("the command ran %d time(s); a machine error must not be retried", calls)
	}
	close(p.done)
}

// THE SENTINEL THE REPAIR KEYS ON IS THE VALUE THE TRANSPORT ACTUALLY RETURNS.
func TestErrClosedIsWhatTheTransportReports(t *testing.T) {
	c := &qmp.Conn{}
	if err := c.Ping(); !errors.Is(err, qmp.ErrClosed) {
		t.Fatalf("a closed connection answered Ping with %v, want ErrClosed", err)
	}
}

// MANY CALLERS ASKING AT ONCE GET ONE CONNECTION, AND NOBODY RACES THE OWNER.
//
// `qmpSocket` is now a narrow surface over `qemuProcess`, so what matters is that concurrent callers
// cannot each open a client. The field they used to do read-modify-write on is behind the process
// object, which publishes a connection once and hands the same one to everybody after.
func TestQmpSocketIsSafeForConcurrentCallers(t *testing.T) {
	shared := &qmp.Conn{}
	p := &qemuProcess{done: make(chan struct{}), reqs: make(chan qmpRequest)}
	p.SetConnection(shared)

	var wg sync.WaitGroup
	got := make([]*qmp.Conn, 32)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i] = p.Connection()
		}(i)
	}
	wg.Wait()
	for i, c := range got {
		if c != shared {
			t.Fatalf("caller %d got a different connection; one machine holds one QMP client", i)
		}
	}
	close(p.done)
}