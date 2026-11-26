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
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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
	mu            sync.Mutex
	at            time.Time
	sshAns        *bool
	qgaAns        *bool
	hostDiagCount int
}

func (p *probeState) set(ssh, qga *bool) {
	p.mu.Lock()
	p.at = time.Now()
	p.sshAns, p.qgaAns = ssh, qga
	p.mu.Unlock()
}

// hostDiagDone guards the one-time host-side diagnosis when the ssh probe first fails.
// markHostDiag returns true while the host-side diagnosis still has budget. It runs on the FIRST
// few ssh-probe failures and not on all sixty, so a boot-time failure (guest not up yet) and a
// wait-for-sshd failure (guest up, still no route) are BOTH captured without flooding the log.
func (p *probeState) markHostDiag() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.hostDiagCount >= 4 {
		return false
	}
	p.hostDiagCount++
	return true
}

func (p *probeState) resetHostDiag() {
	p.mu.Lock()
	p.hostDiagCount = 0
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
			// TWO SECONDS WAS TOO SHORT FOR AN SSH HANDSHAKE AND IT MADE THE FACT FLAP.
			//
			// MEASURED on freebsd ssh, booted in the cluster's own lab pod from this same image and
			// this same disk: a plain TCP connect to 172.30.0.1:2222 answered at once and returned
			// `SSH-2.0-OpenSSH_10.0 FreeBSD`, while this probe reported
			//
			//	ssh probe: dial tcp 172.30.0.1:2222: i/o timeout
			//
			// on the same second. FreeBSD's sshd completes the key exchange and the password
			// exchange slowly on its first connection - the same disk's `ssh: "id -un"` step, which
			// dials with a THIRTY second budget, succeeded. So `machine_ssh` answered FALSE while the
			// guest was answering ssh perfectly, the walk's `repeat until machine_ssh` ran all sixty
			// attempts, and the stage ended with "sshd does not accept a connection" on a machine
			// that accepted them.
			//
			// TEN SECONDS IS BETWEEN THE TWO, and it is still shorter than the ticker's own interval
			// plus the qga probe, so a slow handshake cannot make the ticker fall behind.
			err := guest.Probe(target, 1, 10*time.Second)
			if err == nil {
				sshUp = true
			} else {
				// THE HOST SIDE IS THE ONLY PLACE THE ANSWER IS NOT, so when the probe fails and
				// it is the first failure of this run, dump what the host sees: the DNAT rules, the
				// conntrack entries for the guest, and the ARP entry. MEASURED on cachyos ssh, run
				// 45810843905: the guest showed `LISTEN 0.0.0.0:22`, the right MAC, `ping
				// 172.30.0.1` at 0% loss, `ip neigh` REACHABLE, ufw inactive and rp_filter=2 - every
				// guest fact correct - while every probe was `no route to host`. Nothing inside the
				// guest can explain that; this prints the half that can.
				if d.lastProbe.markHostDiag() {
					d.logHostSSHDiagnosis(target.Host, target.Port)
				}
			}
			if err != nil {
				if prev := d.lastProbe.sshAnswer(); prev == nil || *prev {
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
	}

	qgaUp := false
	if sock := filepath.Join(d.work, "qga.sock"); fileExistsDaemon(sock) {
		if qgaPing(sock) == nil {
			qgaUp = true
		}
	}
	d.lastProbe.set(&sshUp, &qgaUp)
}

// logHostSSHDiagnosis prints what the HOST sees when the ssh probe cannot reach the guest.
//
// IT IS THE HALF THE WALK CANNOT SEE. Every guest-side fact can be correct - sshd listening on
// 0.0.0.0:22, the right MAC, the guest able to ping the host, a resolved ARP entry, no firewall,
// rp_filter loose - and the probe still answers `no route to host`, because the failure is in the
// host's DNAT or conntrack or ARP, none of which is visible from inside the guest. These are the
// three commands that show it, and they run once per run rather than on every tick.
func (d *Daemon) logHostSSHDiagnosis(host string, port int) {
	if d == nil || d.machine == nil || d.machine.driver == nil {
		return
	}
	logf := d.machine.driver.Log
	log := func(label, cmd string, args ...string) {
		out, err := exec.Command(cmd, args...).CombinedOutput()
		text := strings.TrimRight(string(out), "\n")
		if err != nil && text == "" {
			text = err.Error()
		}
		// SOME LABELS ARE SHORT AND EVERY LINE OF THEM MATTERS (the tap state, iptables' version,
		// `ss -tn`); the long tables are filtered to the ports/addresses worth reading. Filtering
		// the tap line to the guest address turned its own output into "(nothing matched)" and hid
		// whether the tap was even up - which is exactly the kind of blind spot this diagnosis
		// exists to remove.
		full := map[string]bool{
			"tap": true, "iptables version": true, "ss 22": true, "ip route": true,
			"nat jump OUTPUT": true, "policy FORWARD": true, "ip neigh": true, "icmp guest": true,
		}
		var keep []string
		for _, line := range strings.Split(text, "\n") {
			if full[label] || strings.Contains(line, strconv.Itoa(port)) ||
				strings.Contains(line, "172.30.0.2") ||
				strings.Contains(line, "DNAT") || strings.Contains(line, "MASQUERADE") {
				keep = append(keep, line)
			}
		}
		if len(keep) == 0 {
			keep = []string{"(nothing matched)"}
		}
		logf(fmt.Sprintf("  host diag [%s]: %s", label, strings.Join(keep, " | ")))
	}
	logf(fmt.Sprintf("=== HOST-SIDE DIAGNOSIS for %s:%d, once per run ===", host, port))
	log("nat full", "sh", "-c", "iptables -t nat -S 2>/dev/null | head -40")
	log("nat jump OUTPUT", "sh", "-c", "iptables -t nat -S OUTPUT 2>/dev/null; iptables -t nat -S PREROUTING 2>/dev/null")
	log("policy FORWARD", "sh", "-c", "iptables -S FORWARD 2>/dev/null | head -10")
	log("conntrack", "sh", "-c", "conntrack -L 2>/dev/null | grep 172.30.0.2 | head -10")
	log("ip neigh", "sh", "-c", "ip neigh show 2>/dev/null | grep 172.30")
	log("ip route", "sh", "-c", "ip route get 172.30.0.2 2>&1")
	log("tap", "sh", "-c", "ip -br a show qemutap0 2>&1; ip -br a show qemubr0 2>&1; ip link show qemutap0 2>&1 | head -2")
	// WHICH IPTABLES IS THE ENGINE EVEN TALKING TO, AND IS ANYTHING LISTENING ON 22 FROM THE HOST.
	// Docker on a modern kernel programs iptables-NFT; if the `iptables` binary here is the LEGACY
	// one, every ACCEPT this engine writes lands in a table the kernel never consults, and the guest
	// is unreachable through rules that are visibly present. `ss -tn` shows a SYN stuck in SYN_SENT
	// (sent, no reply) versus absent (never sent) - the two look identical from a dial timeout.
	log("iptables version", "sh", "-c", "iptables --version 2>&1; readlink -f $(command -v iptables) 2>&1; nft list tables 2>&1 | head -10")
	log("ss 22", "sh", "-c", "ss -tn 2>&1 | grep -E ':22|syn-sent|SYN-SENT' | head -10; echo ---conn---; ss -tn state syn-sent 2>&1 | head -6")
	// HOST -> GUEST AT L3, NOT TCP. ICMP crosses the same tap/bridge/ARP as the SYN but no port and
	// no tcp rule, so it separates "the path is dead" from "tcp/22 specifically is blocked". This
	// image has python3 and no ping binary, so the echo is built by hand in a one-liner.
	pyscript := "import socket,struct,sys\n" +
		"s=socket.socket(socket.AF_INET,socket.SOCK_RAW,socket.IPPROTO_ICMP); s.settimeout(2)\n" +
		"def ck(b):\n" +
		" b=(b+b'\\x00') if len(b)%2 else b\n" +
		" x=sum(struct.unpack('!%dH'%(len(b)//2),b)); x=(x>>16)+(x&0xffff); x+=(x>>16); return (~x)&0xffff\n" +
		"ok=0\n" +
		"for q in (1,2):\n" +
		" pk=struct.pack('!BBHHH',8,0,0,4242,q)+b'vm'; pk=struct.pack('!BBHHH',8,0,ck(pk),4242,q)+b'vm'\n" +
		" try:\n" +
		"  s.sendto(pk,('172.30.0.2',0)); s.recvfrom(1024); ok+=1\n" +
		" except Exception as e: sys.stdout.write('try%d:%s\\n'%(q,e))\n" +
		"sys.stdout.write('ICMP_OK %d/2\\n'%ok)\n"
	log("icmp guest", "python3", "-c", pyscript)
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
