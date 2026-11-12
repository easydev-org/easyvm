// Package net creates the bridge, the tap, DHCP, NAT and the port forwards a guest is reached on.
//
// THERE IS ONE NETWORK MODE AND IT IS THIS ONE.
//
// qemu can also do `-netdev user`, which needs no privileges, no tap and no helper. It is not
// available here, and the reason is not preference:
//
//	THE HOST'S smbd LISTENS ON THE BRIDGE ADDRESS. A guest mounts ~/shared over SMB, the server is
//	host-side (see internal/share), and it binds 172.30.0.1. A user-mode guest lives on 10.0.2.0/24
//	and cannot route to 172.30.0.1, so every `share_mount:` step would fail with a network error
//	that reads as a broken share.
//
//	qemu's own `-netdev user,smb=<dir>` was the other route and it was removed: it makes qemu fork
//	its own smbd, which is a second server with a second config, and the Python implementation
//	spent several runs discovering which of the two was refusing a macOS client.
//
// SO A CONTAINER NEEDS /dev/net/tun AND NET_ADMIN, AND IT IS TOLD SO BEFORE ANYTHING IS CREATED.
// The Python implementation found out at `ip link add`, where the error is
//
//	RTNETLINK answers: Operation not permitted
//
// which reads as a broken network rather than a missing capability. `Preflight` below answers the
// question first, and separates the three failures because they have three different fixes.
package net

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Network is the fixed addressing every guest uses.
//
// THE ADDRESSES ARE FIXED, NOT ALLOCATED. One guest per container means there is nothing to
// coordinate, and a fixed pair is what makes a log line like `ssh 172.30.0.2` readable without
// looking anything up. dnsmasq hands the guest its address by MAC.
const (
	BridgeName = "qemubr0"
	TapName    = "qemutap0"
	BridgeIP   = "172.30.0.1"
	GuestIP    = "172.30.0.2"
	Prefix     = 24
	// TapMAC is the MAC the tap presents and the one dnsmasq leases GuestIP to.
	TapMAC = "52:54:00:56:4d:01"
	// MTU follows the uplink. A guest larger than its tap is the broken case: measured on this
	// cluster, guest 1500 over a 1450 tap drops every full segment, and the symptom is a TLS
	// handshake that never completes rather than a network error.
	MTU = 1450
)

// ErrNoTun and ErrNoNetAdmin are the two capability failures, separate because the fixes differ.
var (
	// ErrNoTun means /dev/net/tun is absent or unopenable: the container was started without
	// `--device /dev/net/tun`.
	ErrNoTun = errors.New("/dev/net/tun is not usable")
	// ErrNoNetAdmin means a bridge cannot be created: the container needs `--cap-add NET_ADMIN`.
	ErrNoNetAdmin = errors.New("a bridge cannot be created")
)

// Preflight answers "can this environment run a guest at all" before anything is created.
//
// IT IS THREE CHECKS AND THEY ARE NOT THE SAME QUESTION:
//
//  1. does /dev/net/tun exist      -> the device was not mounted   -> --device /dev/net/tun
//  2. can it be opened             -> mounted, but no permission    -> --device /dev/net/tun
//  3. can a bridge be created      -> NET_ADMIN is missing          -> --cap-add NET_ADMIN
//
// Reading CapEff would answer 3 too and would be wrong: it reports the capability set, not whether
// the kernel allows the operation in this namespace. Creating and deleting a bridge is the only
// honest test, and it costs a millisecond.
func Preflight() error {
	f, err := os.OpenFile("/dev/net/tun", os.O_RDWR, 0)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("%w: the file does not exist, so the container was started without "+
			"`--device /dev/net/tun`. Without it no guest can be reached on the bridge, and qemu's "+
			"user-mode networking cannot replace it because the host's smbd listens on %s",
			ErrNoTun, BridgeIP)
	case errors.Is(err, os.ErrPermission):
		return fmt.Errorf("%w: the file exists but cannot be opened, so the container needs "+
			"`--device /dev/net/tun`", ErrNoTun)
	case err != nil:
		return fmt.Errorf("%w: %v", ErrNoTun, err)
	}
	f.Close()

	// A BRIDGE NAMED FOR THIS CHECK, so a concurrent run cannot be disturbed by it.
	probe := "qemuprobe0"
	if out, err := run("ip", "link", "add", "dev", probe, "type", "bridge"); err != nil {
		return fmt.Errorf("%w: `ip link add type bridge` failed: %v (%s). The container needs "+
			"`--cap-add NET_ADMIN`", ErrNoNetAdmin, err, firstLine(out))
	}
	// THE PROBE IS DELETED AND ITS FAILURE IS NOT FATAL: leaving it would make the next run's
	// preflight fail for a reason that is this run's fault.
	_, _ = run("ip", "link", "del", "dev", probe)
	return nil
}

// Up creates the bridge, the tap, DHCP, NAT and the port forwards.
//
// IT IS IDEMPOTENT, because a daemon may be asked to run more than one walkthrough and a second
// `ip link add` on an existing device is an error that would look like a failure.
func Up(sshPort int, forwards []int) error {
	if err := Preflight(); err != nil {
		return err
	}

	// FORWARDING FIRST, BECAUSE EVERY RULE BELOW ASSUMES IT - AND IT IS READ BEFORE IT IS WRITTEN.
	//
	// `ip_forward` IS PER NETWORK NAMESPACE. If this process is in a namespace that already
	// forwards - the host's, which is what a CI job container usually gets - there is nothing to
	// set, and attempting the write is what produced, three runs in a row,
	//
	//	boot the disk: boot: net.ipv4.ip_forward: exit status 1
	//
	// a message that named neither the setting's current value nor the reason. /proc/sys is
	// mounted read-only in most containers, so a write fails even where the value is already
	// right; reading first is the difference between "this must be changed" and "this is fine".
	//
	// THE PYTHON IMPLEMENTATION NEVER FOUND THIS because lib/net.py runs
	// `sysctl -w net.ipv4.ip_forward=1` through `_sh()`, whose `check` defaults to False - the
	// return code is discarded. It was never failing; it was never asking.
	if err := ensureIPForward(); err != nil {
		return err
	}

	// THE BRIDGE. `ip link add` fails if it exists, which is the normal case on a second run.
	if out, err := run("ip", "link", "add", "dev", BridgeName, "type", "bridge"); err != nil {
		if !alreadyExists(out) {
			return fmt.Errorf("creating %s: %v (%s)", BridgeName, err, firstLine(out))
		}
	}
	for _, step := range [][]string{
		{"address", "add", fmt.Sprintf("%s/%d", BridgeIP, Prefix), "dev", BridgeName},
		{"link", "set", BridgeName, "mtu", strconv.Itoa(MTU)},
		{"link", "set", BridgeName, "up"},
	} {
		// EACH IS ALLOWED TO SAY "already there": `address add` on an interface that has it, and
		// `link set` on one already up, both exit non-zero on some kernels.
		if out, err := run("ip", step...); err != nil && !alreadyExists(out) {
			return fmt.Errorf("ip %s: %v (%s)", strings.Join(step, " "), err, firstLine(out))
		}
	}
	// IPv6 IS DISABLED PER INTERFACE rather than globally, so the container's own stack is not
	// changed for a guest that never asked.
	_, _ = run("sysctl", "-w", "net.ipv6.conf."+BridgeName+".disable_ipv6=1")

	// THE TAP. `user` is what lets qemu open it as an unprivileged owner of the device, which is
	// why the container does not need to run qemu as root.
	if out, err := run("ip", "tuntap", "add", "dev", TapName, "mode", "tap", "user",
		strconv.Itoa(os.Getuid())); err != nil && !alreadyExists(out) {
		return fmt.Errorf("ip tuntap add: %v (%s)", err, firstLine(out))
	}
	for _, step := range [][]string{
		{"link", "set", TapName, "mtu", strconv.Itoa(MTU)},
		{"link", "set", "dev", TapName, "up", "promisc", "on"},
		{"link", "set", "dev", TapName, "master", BridgeName},
	} {
		if out, err := run("ip", step...); err != nil {
			return fmt.Errorf("ip %s: %v (%s)", strings.Join(step, " "), err, firstLine(out))
		}
	}
	_, _ = run("sysctl", "-w", "net.ipv6.conf."+TapName+".disable_ipv6=1")

	if err := startDHCP(); err != nil {
		return err
	}
	if err := applyNAT(); err != nil {
		return err
	}
	return applyForwards(sshPort, forwards)
}

// ipForwardPath is the kernel's own answer to "is forwarding on", per namespace.
const ipForwardPath = "/proc/sys/net/ipv4/ip_forward"

// ensureIPForward turns on IPv4 forwarding, or explains why it could not.
//
// IT READS FIRST. A namespace that already forwards needs nothing, and the write would fail
// anyway in a container whose /proc/sys is read-only - which is most of them, including GitHub
// Actions' job containers. So "already 1" is success, not a skipped check.
func ensureIPForward() error {
	if cur, err := os.ReadFile(ipForwardPath); err == nil {
		if strings.TrimSpace(string(cur)) == "1" {
			return nil
		}
	}

	// NOT ALREADY ON, SO IT HAS TO BE WRITTEN. `sysctl -w` is used rather than writing /proc
	// directly because it also applies the change if the kernel accepts it through its own
	// interface, and it fails with a sentence when it does not.
	out, err := run("sysctl", "-w", "net.ipv4.ip_forward=1")
	if err != nil {
		now, readErr := os.ReadFile(ipForwardPath)
		state := "unreadable"
		if readErr == nil {
			state = strings.TrimSpace(string(now))
		}
		return fmt.Errorf("net.ipv4.ip_forward is %s and could not be set: %v (%s). Forwarding "+
			"is what lets the guest reach anything outside its tap - without it a DHCP lease "+
			"arrives and every later connection times out. In a container this is /proc/sys/net "+
			"being read-only, not a missing capability: the namespace has to be created with "+
			"`--sysctl net.ipv4.ip_forward=1`, or be the host's", state, err, firstLine(out))
	}
	return nil
}

// Down removes what Up created, and is safe on a partially-created network.
func Down() {
	spec := fmt.Sprintf("%s/%d", BridgeIP, Prefix)

	// THE JUMPS GO BEFORE THE CHAIN, OR THE CHAIN CANNOT GO.
	//
	// `iptables -X <chain>` REFUSES a chain that is still referenced, so flushing and deleting it
	// first leaves the jump in PREROUTING pointing at a chain that no longer exists - which fails
	// silently here and accumulates a dangling rule per run. The Python implementation has the
	// same three lines in the same order for the same reason.
	//
	// BOTH JUMPS, because both are added by applyForwards: PREROUTING for a connection from
	// outside the container, OUTPUT for one of this container's own processes.
	for _, where := range [][]string{
		{"PREROUTING", "-d", spec},
		{"OUTPUT", "-d", spec, "-p", "tcp"},
	} {
		// A JUMP THAT IS NOT THERE IS NOT A FAILURE, and `-D` says so with exit 1.
		for {
			if _, err := run("iptables", append([]string{"-t", "nat", "-D"}, append(where,
				"-j", forwardChain)...)...); err != nil {
				break
			}
		}
	}
	_, _ = run("iptables", "-t", "nat", "-F", forwardChain)
	_, _ = run("iptables", "-t", "nat", "-X", forwardChain)

	_, _ = run("iptables", "-F", "FORWARD")
	_, _ = run("iptables", "-t", "nat", "-D", "POSTROUTING", "-s", spec, "-j", "MASQUERADE")
	stopDHCP()
	_, _ = run("ip", "link", "del", "dev", TapName)
	_, _ = run("ip", "link", "del", "dev", BridgeName)
}

// --- helpers ---------------------------------------------------------------------------

func run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// alreadyExists reports whether a creation failed because the thing was already there.
//
// IT TAKES THE COMMAND'S OUTPUT, NOT ITS ERROR, AND THAT WAS A REAL BUG. `ip` and `iptables` both
// exit 2 for every kind of failure and put the reason on stderr, so the error value is always
// exactly `exit status 2` - it never contains "File exists". This function was written against
// `err.Error()`, which means it ALWAYS RETURNED FALSE and every "idempotent" call in this package
// was not: a second `Up` would fail on the existing bridge, and the log would say
// `creating br-easyvm: exit status 2`, naming neither the device nor the reason.
//
// The message is still the only way to tell the cases apart - exit 2 is all `ip` says otherwise -
// so the text is what is matched, from the place the text actually is.
func alreadyExists(output string) bool {
	// `Address already assigned` IS THE ONE A SECOND RUN HITS AND THE ONE THAT WAS MISSING.
	//
	// `ip addr add` on a bridge that already carries the address fails with
	// `Error: ipv4: Address already assigned.` - a different sentence from the `File exists` the
	// bridge creation produces - so a run after a failed one refused to start with
	//
	//	boot the disk: boot: ip address add 172.30.0.1/24 dev qemubr0:
	//	exit status 2 (Error: ipv4: Address already assigned.)
	//
	// Measured here: the guest had already been configured by the previous attempt, and the retry
	// could not get past its own leftovers. Idempotence is the whole point of the check.
	for _, needle := range []string{
		"File exists", "already exists", "RTNETLINK answers: File exists",
		"Address already assigned", "Cannot assign requested address",
	} {
		if strings.Contains(output, needle) {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 160 {
		s = s[:160] + "..."
	}
	return s
}

// WaitForPort waits until something accepts TCP on the guest's address.
//
// THIS IS NOT "IS SSHD UP". A listening socket means the bridge, the tap, the DHCP lease and the
// DNAT rule all work; whether the daemon authenticates is a different question, and keeping them
// apart is what lets a log say "no route to host" (no lease), "connection refused" (lease, no
// listener) and "timed out" (lease, packets unanswered) - three faults that read identically if
// collapsed into one.
func WaitForPort(host string, port int, timeout, poll time.Duration,
	onWait func(elapsed time.Duration, last error)) error {
	if poll <= 0 {
		poll = 2 * time.Second
	}
	start := time.Now()
	deadline := start.Add(timeout)
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	var last error
	for {
		conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
		if err == nil {
			conn.Close()
			return nil
		}
		last = err
		if time.Now().After(deadline) {
			return fmt.Errorf("nothing accepted TCP on %s within %s: %w", addr, timeout, last)
		}
		if onWait != nil {
			onWait(time.Since(start), last)
		}
		time.Sleep(poll)
	}
}
