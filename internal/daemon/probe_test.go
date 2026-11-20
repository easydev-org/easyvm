package daemon

import (
	"testing"
	"time"
)

// A SNAPSHOT MUST BE INSTANT, AND THAT IS A MEASUREMENT RATHER THAN AN OPINION.
//
// MEASURED ON A REAL RUN: `/api/state` probed sshd and the guest agent while building its answer.
// `log-stream` polls it every ten seconds, the agent's dial timeout is sixty, so every request was
// slower than the poller's own timeout and the entire install was unobservable:
//
//	FAILED: GET http://127.0.0.1:8080/api/state: context deadline exceeded
//
// repeated for the whole run.
//
// SO THE PROBES RUN ON A TICKER AND THE SNAPSHOT READS THE LAST ANSWER. These tests pin the two
// properties that makes true: the snapshot never blocks, and a nil driver clears the answers rather
// than leaving the previous machine's.

// TestTheSnapshotDoesNotProbe blocks the probe ticker and proves `/api/state` still answers.
func TestTheSnapshotDoesNotProbe(t *testing.T) {
	d := &Daemon{work: t.TempDir()}

	// A RECORDED ANSWER, AS THE TICKER WOULD LEAVE IT.
	yes, no := true, false
	d.lastProbe.set(&yes, &no)

	done := make(chan State, 1)
	go func() { done <- d.snapshot() }()

	select {
	case st := <-done:
		if st.SSHReady == nil || !*st.SSHReady {
			t.Fatalf("ssh_ready is %v and the last probe said true", st.SSHReady)
		}
		if st.QGAReady == nil || *st.QGAReady {
			t.Fatalf("qga_ready is %v and the last probe said false", st.QGAReady)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the snapshot did not return within two seconds. It must not probe - the poller's " +
			"own timeout is shorter than a guest-agent handshake, which is how a whole install " +
			"became unobservable")
	}
}

// TestAnEmptyProbeSaysNothingRatherThanFalse is the "never asked yet" case.
//
// A NIL ANSWER IS NOT `false`. With no machine there is no sshd to be down, and a caller that
// reported `false` would be asserting something nobody measured - the same shape as the cached
// booleans this design replaced.
func TestAnEmptyProbeSaysNothingRatherThanFalse(t *testing.T) {
	d := &Daemon{work: t.TempDir()}
	st := d.snapshot()
	if st.SSHReady != nil {
		t.Fatalf("ssh_ready is %v before any probe ran; nothing has been measured", *st.SSHReady)
	}
	if st.QGAReady != nil {
		t.Fatalf("qga_ready is %v before any probe ran", *st.QGAReady)
	}
}

// TestAStoppedMachineClearsTheProbe is the stale-answer failure this design has to avoid.
//
// Without the clear, `wait_until: ssh` on a machine that was stopped and restarted would pass on the
// PREVIOUS guest's success.
func TestAStoppedMachineClearsTheProbe(t *testing.T) {
	d := &Daemon{work: t.TempDir()}
	yes := true
	d.lastProbe.set(&yes, &yes)

	// No machine at all: the shape `shutdown:` leaves behind.
	d.probeOnce()

	if got := d.lastProbe.sshAnswer(); got != nil {
		t.Fatalf("ssh_ready is %v after the machine went away, and the previous answer was true - a "+
			"`wait_until: ssh` would pass on a guest that is not running", *got)
	}
	if got := d.lastProbe.qgaAnswer(); got != nil {
		t.Fatalf("qga_ready is %v after the machine went away", *got)
	}
}
