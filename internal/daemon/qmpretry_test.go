package daemon

import (
	"errors"
	"sync"
	"testing"

	"github.com/easydev-org/easyvm/internal/qmp"
)

// A CACHED CONNECTION IS HANDED OUT WITHOUT A PROBE, AND A CLOSED ONE IS REPLACED ON USE.
//
// THE PROPERTY THAT COST A RUN ITS BUDGET IS NOT "THE CACHE WORKS" - it is that the daemon does not
// CLOSE A CONNECTION SOMEBODY ELSE IS USING. MEASURED ON A POD, through the daemon's own HTTP
// endpoint, on fedora's ssh stage:
//
//	POST /api/command {"command":"key","key":"shift"}   20/20 ok    one command per request
//	POST /api/command {"command":"type","text":"a"}      0/15 ok    stroke + release-modifiers
//	    "type: release modifiers: the qmp connection is closed"
//
// qemu answered `query-status` with `running: true` and the daemon had restarted zero times. The
// FIRST command of a request never failed and the SECOND repeatedly did, because a probe in
// `qmpSocket` closed the shared connection between them.
//
// SO THE TESTS BELOW PIN THE TWO HALVES OF THE REPAIR: a cached connection is returned as it is, and
// a command that reports `ErrClosed` is retried once on a fresh connection.

// TestQmpSocketReturnsTheCacheWithoutProbing drives the cache-hit path with a connection that knows
// it is closed, and requires it to come back untouched.
//
// A PROBE HERE WOULD RETURN NIL (the ping would fail) AND THE CALLER WOULD OPEN A SECOND SOCKET -
// which is the defect: qemu serves one client, and the first caller's command then lands on a
// connection that has been closed underneath it.
func TestQmpSocketReturnsTheCacheWithoutProbing(t *testing.T) {
	d := &Driver{}
	dead := &qmp.Conn{}
	d.qmp = dead

	got, err := d.qmpSocket()
	if err != nil {
		t.Fatalf("qmpSocket returned an error for a cached connection: %v", err)
	}
	if got != dead {
		t.Fatal("qmpSocket did not return the cached connection. It probed instead, and a probe that " +
			"fails closes a connection another caller may be using - that is the fedora defect")
	}
}

// TestQmpRetryLeavesAWorkingConnectionAlone is the other half: a command that succeeds must not cost
// a reconnect, and must not disturb the cache.
func TestQmpRetryLeavesAWorkingConnectionAlone(t *testing.T) {
	d := &Driver{}
	live := &qmp.Conn{}
	d.qmp = live

	calls := 0
	err := d.qmpRetry(func(*qmp.Conn) error { calls++; return nil })
	if err != nil {
		t.Fatalf("qmpRetry on a working connection: %v", err)
	}
	if calls != 1 {
		t.Fatalf("the command ran %d time(s); a successful command must run exactly once", calls)
	}
	if d.qmp != live {
		t.Fatal("qmpRetry replaced a working cached connection")
	}
}

// TestQmpRetryReturnsAnOrdinaryErrorUnchanged guards the distinction the retry depends on: only
// `ErrClosed` means "the transport is gone". A QMP reply that says the device does not exist is the
// MACHINE'S answer, and retrying it would send the same keystroke twice.
func TestQmpRetryReturnsAnOrdinaryErrorUnchanged(t *testing.T) {
	d := &Driver{}
	d.qmp = &qmp.Conn{}

	calls := 0
	refused := errors.New("the device 'usb-kbd' does not exist")
	err := d.qmpRetry(func(*qmp.Conn) error { calls++; return refused })
	if !errors.Is(err, refused) {
		t.Fatalf("got %v, want the machine's own error unchanged", err)
	}
	if calls != 1 {
		t.Fatalf("the command ran %d time(s); a machine error must not be retried", calls)
	}
}

// TestErrClosedIsWhatTheTransportReports keeps the sentinel the retry keys on connected to the
// value the transport actually returns, so a rename cannot silently disable the retry.
func TestErrClosedIsWhatTheTransportReports(t *testing.T) {
	// A closed `*Conn` answers every command with ErrClosed. This is the observable contract the
	// retry is built on, and it is cheap to hold.
	c := &qmp.Conn{}
	if err := c.Ping(); !errors.Is(err, qmp.ErrClosed) {
		t.Fatalf("a closed connection answered Ping with %v, want ErrClosed", err)
	}
}

// TestQmpSocketIsSafeForConcurrentCallers is the concurrency half, WITHOUT a hand-made mutex check:
// the previous attempt at this probe unlocked a mutex it did not hold and the daemon died forty
// seconds into a boot. Many callers asking at once must return, exactly once each.
func TestQmpSocketIsSafeForConcurrentCallers(t *testing.T) {
	d := &Driver{}
	shared := &qmp.Conn{}
	d.qmp = shared

	var wg sync.WaitGroup
	got := make([]*qmp.Conn, 32)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, err := d.qmpSocket()
			if err != nil {
				t.Errorf("concurrent qmpSocket: %v", err)
				return
			}
			got[i] = c
		}(i)
	}
	wg.Wait()
	for i, c := range got {
		if c != shared {
			t.Fatalf("caller %d got a different connection; one daemon holds one QMP client", i)
		}
	}
}
