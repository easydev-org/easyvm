package daemon

import (
	"path/filepath"
	"testing"

	"github.com/easydev-org/easyvm/internal/qmp"
)

// A DAEMON THAT HAS STOPPED A MACHINE MUST LEAVE NO QMP ENTRY BEHIND, EVEN WITH NO CACHED CONNECTION.
//
// THIS IS THE EIGHTH OCCURRENCE OF ONE PATTERN, and it is the first time it could be pinned here: a
// pod that runs create/destroy more than once ends up with
//
//	this qmp socket is already held by this process (by "easyvm")
//
// on the next boot, while A FRESH POD ALWAYS SUCCEEDS. State that survives a machine, on a daemon
// whose registry lives for the life of the process - that is this map.
//
// THE STATE THE BUG NEEDS IS TWO LINES AND A RACE:
//
//	a capture tick is inside `qmpSocket()` and `Acquire`d the path
//	the guest dies; a `qmpRetry` elsewhere sees ErrClosed, sets `d.qmp = nil`, and drops the entry
//	  ONLY IF `d.qmp == conn` at that instant - which it may not be, if a boot replaced it
//
// and then `shutdown` looked at `conn != nil`, found nil, and skipped the release. The cache and the
// registry disagreed, and only the registry is what `Acquire` consults.
//
// SO THE CONTRACT IS: release happens on every stop, whatever the cache says.
func TestShutdownReleasesTheQmpPathWithNoCachedConnection(t *testing.T) {
	d := &Driver{daemon: &Daemon{}}
	d.work = t.TempDir()
	path := filepath.Join(d.work, "drive.sock.qmp")

	// The registry holds the path - as it does after a capture tick - and the cache is EMPTY, which
	// is what a `qmpRetry` leaves when it drops a dead connection whose identity had changed.
	hold(d.qmpRegistry(), path)

	if d.qmp != nil {
		t.Fatal("this test is about the no-cached-connection case")
	}

	d.shutdown()

	if held(d.qmpRegistry(), path) {
		t.Fatal("shutdown left the qmp path in the registry with no cached connection. " +
			"The next machine on this daemon is refused by its own process: " +
			"`this qmp socket is already held by this process`")
	}
}

// hold and held reach the registry's bookkeeping the way `TestSecondAcquireIsRefusedByName` does,
// because a real socket cannot be made in a unit test and the entry is what the bug is about.
func hold(r *qmp.Registry, path string) { r.Hold(path, "easyvm") }
func held(r *qmp.Registry, path string) bool { return r.Held(path) }
