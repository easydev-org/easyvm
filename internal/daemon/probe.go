package daemon

// THE TWO PROBES THAT MUST NOT RUN INSIDE A SNAPSHOT.
//
// `ssh_ready` and `qga_ready` are answers to questions that COST TIME: an SSH handshake has to
// complete, and the guest agent has to answer `guest-ping` over a unix socket whose timeout is sixty
// seconds. A snapshot that asked them would be as slow as the slowest one.
//
// MEASURED, ON A REAL RUN: it was. `log-stream` polls `/api/state` every ten seconds, the probe took
// longer than that whenever the agent was absent, and the whole install was unobservable:
//
//	FAILED: GET http://127.0.0.1:8080/api/state: context deadline exceeded
//
// SO THE PROBES RUN ON THEIR OWN TICKER and the snapshot reads the last answer. That is a fact with
// an age rather than a fact that is current, which is the honest shape: "sshd answered 3 seconds ago"
// is what a person watching a run actually wants, and `wait_until:` polls this same state so a step
// that needs it current is already asking repeatedly.

import (
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/easydev-org/easyvm/internal/guest"
)

// probeInterval is how often the two probes are asked.
//
// TEN SECONDS IS THE POLLER'S OWN CADENCE (`log-stream`), so a person watching sees at most one stale
// answer, and the guest is asked a question no more often than a page refresh would ask it.
const probeInterval = 10 * time.Second

// probeState is the last answer from each probe, and when it was taken.
type probeState struct {
	mu     sync.Mutex
	at     time.Time
	sshAns *bool
	qgaAns *bool
}

func (p *probeState) set(ssh, qga *bool) {
	p.mu.Lock()
	p.at = time.Now()
	p.sshAns, p.qgaAns = ssh, qga
	p.mu.Unlock()
}

func (p *probeState) sshAnswer() *bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sshAns
}

func (p *probeState) qgaAnswer() *bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.qgaAns
}

// probeOnce measures both and records the answers.
//
// A NIL DRIVER CLEARS THEM RATHER THAN LEAVING THE LAST MACHINE'S ANSWER: a `stopped` machine has no
// sshd, and reporting the previous one's success would let `wait_until: ssh` pass on a guest that is
// not running.
func (d *Daemon) probeOnce() {
	slot := d.machine
	if slot == nil || slot.driver == nil {
		d.lastProbe.set(nil, nil)
		return
	}
	drv := slot.driver

	sshUp := false
	if tp, ok := drv.(interface{ SSHTarget() guest.Target }); ok {
		target := tp.SSHTarget()
		if target.Port != 0 {
			err := guest.Probe(target, 1, 2*time.Second)
			if err == nil {
				sshUp = true
			} else if prev := d.lastProbe.sshAnswer(); prev == nil || *prev {
				// A GUEST WHOSE SSH NEVER COMES UP MUST SAY WHY. MEASURED on freebsd ssh, run
				// 45096500475: the console showed `Starting sshd.` and a DHCP lease, and the walk
				// still failed with the whole sentence "sshd does not accept a connection" - which
				// is true and names nothing. The probe HAD the reason (a refused port, a password
				// rejection, a handshake that timed out) and threw it away, so the next hour went
				// into reading qemu arguments instead of the one line that mattered.
				drv.Log(fmt.Sprintf("  ssh probe: %v", err))
			}
		}
	}

	qgaUp := false
	if sock := filepath.Join(d.work, "qga.sock"); fileExistsDaemon(sock) {
		if qgaPing(sock) == nil {
			qgaUp = true
		}
	}
	d.lastProbe.set(&sshUp, &qgaUp)
}

// runProbes is the ticker, started once by the daemon.
func (d *Daemon) runProbes(stop <-chan struct{}) {
	// ONE MEASUREMENT IMMEDIATELY, so the first `/api/state` after a boot is not blank for ten
	// seconds - which is exactly the window in which somebody is watching.
	d.probeOnce()
	ticker := time.NewTicker(probeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			d.probeOnce()
		}
	}
}
