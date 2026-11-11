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

	// FORWARDING FIRST. Every rule below depends on it, and `sysctl` is the only way to set it
	// without writing /proc by hand.
	if _, err := run("sysctl", "-w", "net.ipv4.ip_forward=1"); err != nil {
		return fmt.Errorf("net.ipv4.ip_forward: %w", err)
	}

	// THE BRIDGE. `ip link add` fails if it exists, which is the normal case on a second run.
	if _, err := run("ip", "link", "add", "dev", BridgeName, "type", "bridge"); err != nil {
		if !alreadyExists(err) {
			return fmt.Errorf("creating %s: %w", BridgeName, err)
		}
	}
	for _, step := range [][]string{
		{"address", "add", fmt.Sprintf("%s/%d", BridgeIP, Prefix), "dev", BridgeName},
		{"link", "set", BridgeName, "mtu", strconv.Itoa(MTU)},
		{"link", "set", BridgeName, "up"},
	} {
		// EACH IS ALLOWED TO SAY "already there": `address add` on an interface that has it, and
		// `link set` on one already up, both exit non-zero on some kernels.
		if out, err := run("ip", step...); err != nil && !alreadyExists(err) {
			return fmt.Errorf("ip %s: %v (%s)", strings.Join(step, " "), err, firstLine(out))
		}
	}
	// IPv6 IS DISABLED PER INTERFACE rather than globally, so the container's own stack is not
	// changed for a guest that never asked.
	_, _ = run("sysctl", "-w", "net.ipv6.conf."+BridgeName+".disable_ipv6=1")

	// THE TAP. `user` is what lets qemu open it as an unprivileged owner of the device, which is
	// why the container does not need to run qemu as root.
	if out, err := run("ip", "tuntap", "add", "dev", TapName, "mode", "tap", "user",
		strconv.Itoa(os.Getuid())); err != nil && !alreadyExists(err) {
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

// Down removes what Up created, and is safe on a partially-created network.
func Down() {
	// THE FORWARDS AND NAT GO WITH THE BRIDGE, so the rules are not individually undone: an
	// iptables flush of a chain this package named is enough, and leaving MASERADE rules behind
	// accumulates one per run.
	_, _ = run("iptables", "-t", "nat", "-F", forwardChain)
	_, _ = run("iptables", "-t", "nat", "-X", forwardChain)
	_, _ = run("iptables", "-F", "FORWARD")
	_, _ = run("iptables", "-t", "nat", "-D", "POSTROUTING", "-s",
		fmt.Sprintf("%s/%d", BridgeIP, Prefix), "-j", "MASQUERADE")
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
// THE MESSAGE IS THE ONLY WAY TO KNOW, because `ip` and `iptables` both use exit 2 for everything.
// Matching text is not satisfying; the alternative is an idempotent operation that is not
// idempotent, which is worse.
func alreadyExists(err error) bool {
	if err == nil {
		return false
	}
	for _, needle := range []string{"File exists", "already exists", "RTNETLINK answers: File exists"} {
		if strings.Contains(err.Error(), needle) {
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
