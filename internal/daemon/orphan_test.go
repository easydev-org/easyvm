package daemon

import (
	"os"
	"path/filepath"
	"strings"
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

// A QEMU THAT THIS FUNCTION STARTED BELONGS TO IT, EVEN WHEN THE BOOT FAILS.
//
// MEASURED ON THE POD, WITH A LIVE qemu TO PROVE IT:
//
//	easyvm destroy  ->  [easyvm] the machine was destroyed    (exit 0)
//	ps              ->  383 Sl  qemu-system-x86               (still running)
//	next boot       ->  this qmp socket is already held by this process
//
// The chain was: `cmd.Start()` succeeded, `qmpSocket` failed (the daemon's own sampler had taken the
// path), and this function returned WITHOUT setting `d.proc`. `shutdown` guards on `proc != nil`,
// `Stop` answers "not running", and `destroy` reports success on a machine it cannot see. The orphan
// then owns the socket, and every later boot in that pod is refused - which is why the workaround was
// `kill -9` by hand, and why a fresh pod always worked.
//
// THE CONTRACT: from `cmd.Start()` onward the process is registered before anything else can fail.
func TestBootRegistersTheProcessBeforeItCanFail(t *testing.T) {
	src, err := os.ReadFile("driver.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)

	start := strings.Index(text, "if err := cmd.Start(); err != nil {")
	if start < 0 {
		t.Fatal("cmd.Start() not found")
	}
	after := text[start:]

	register := strings.Index(after, "d.proc = cmd")
	// the next statement that can return, after Start
	guard := strings.Index(after, "bootFailed := true")
	if register < 0 {
		t.Fatal("nothing assigns `d.proc` after `cmd.Start()`. A qemu that is started and then not " +
			"registered is a process `destroy` cannot kill, and the next boot is refused by its own " +
			"daemon - see the measured orphan above")
	}
	if guard < 0 || guard < register {
		t.Fatal("`d.proc` is assigned but the failure guard is missing or precedes it; an error " +
			"returned between the two leaves a live qemu nobody owns")
	}
	if !strings.Contains(after, "cmd.Process.Kill()") {
		t.Fatal("the failure path does not kill the process it started")
	}
}
