package main

import (
	"os"
	"testing"
)

// THE REAPER MUST NOT RUN OUTSIDE PID 1, and the guard is the process's own id rather than a flag.
//
// MEASURED: a pod that had run a few create/destroy cycles held twelve zombie `dnsmasq` processes and
// a zombie qemu. dnsmasq daemonizes, so the process `CombinedOutput` waits for exits and the real
// daemon is re-parented to PID 1 - which is this binary in a container and which nothing reaps. The
// visible cost is a machine slot that never finishes closing:
//
//	FAILED: the previous machine is still closing; retry in a moment
//
// In a NORMAL parent this loop would steal exit statuses from `exec.Cmd.Wait` and every child would
// fail with ECHILD, so the guard is not a nicety.
func TestReapOrphansOnlyRunsAsPID1(t *testing.T) {
	if os.Getpid() == 1 {
		t.Skip("this test process IS pid 1 (a container); the guard's other branch cannot be exercised here")
	}
	// The function returns without starting its goroutine when it is not PID 1, so calling it must
	// not block and must not disturb this process's own children.
	reapOrphans()
	// A child started here still reports its exit status: if the reaper had claimed it, this Wait
	// would come back ECHILD.
	cmd := osExecEcho()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("a child of this process could not be waited for after reapOrphans: %v. A reaper "+
			"outside PID 1 steals exit statuses from the real parent", err)
	}
}
