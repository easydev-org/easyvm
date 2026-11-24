package net

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// dnsmasq IS HOW THE GUEST GETS AN ADDRESS, AND IT IS WHY THE MAC MATTERS.
//
// A bridge hands out nothing. The tap is a device; the guest brings up virtio-net, sends DHCP
// DISCOVER, and something has to answer. dnsmasq does that, leases by MAC, and is also what makes
// `GuestIP` a fact rather than a hope - the port forwards below are written against 172.30.0.2
// before any guest exists.
//
// WHY NOT A STATIC leasefile PER RUN: because a second run with a different MAC would be refused a
// lease by a dnsmasq still holding the first one. The Python implementation learned this the hard
// way and recorded it in `dnsmasq.vm.mac`: the MAC is written down, and a differing MAC restarts
// dnsmasq rather than hoping it re-leases.
const (
	dhcpConfPath  = "/tmp/easyvm-dnsmasq.conf"
	dhcpLogPath   = "/tmp/easyvm-dnsmasq.log"
	dhcpLeasePath = "/tmp/easyvm-dnsmasq.leases"
	dhcpMacPath   = "/tmp/easyvm-dnsmasq.mac"
	dhcpPIDPath   = "/tmp/easyvm-dnsmasq.pid"
	// dnsFallback is what dnsmasq forwards to when the container's own resolv.conf names nothing.
	//
	// IT IS A LAST RESORT, NOT THE PLAN. The plan is ResolverUpstreams() below, which reads the file
	// the container itself uses - a hard-coded address is right for exactly one cluster:
	//
	//	lab     /etc/resolv.conf   nameserver 172.18.0.10     (the cluster DNS)
	//	CI      /etc/resolv.conf   nameserver <runner's own>  (a GitHub runner has its own)
	//
	// MEASURED: with 172.18.0.10 written into the config, a machine that is not on this cluster gets
	// dnsmasq forwarding to an address that does not answer, waiting out its timeout on every query
	// before falling through.
	dnsFallback = "1.1.1.1"
)

// ResolverUpstreams reads the nameservers THIS CONTAINER uses, for dnsmasq to forward to.
//
// THE GUEST CANNOT USE THEM DIRECTLY AND THAT IS THE WHOLE POINT: its UDP 53 is MASQUERADEd at the
// bridge and leaves by a path that is dropped, while the container's own queries to the same server
// are answered. Handing the container's resolvers to dnsmasq gives the guest the answers without
// giving it the broken route.
//
// IT IS READ, NOT HARD-CODED, because the address differs by where this runs:
//
//	lab   nameserver 172.18.0.10        the cluster's DNS
//	CI    nameserver 10.x.x.x           the runner's own
//
// A constant here was right for one of those and silently wrong for the other - the guest would still
// resolve, after dnsmasq waited out a timeout on a server that does not exist on that network.
//
// A LOOPBACK ENTRY IS SKIPPED: a container whose resolv.conf says 127.0.0.11 (Docker's embedded
// resolver) or 127.0.0.53 (systemd-resolved) is describing a proxy inside its own namespace, and
// dnsmasq on the bridge cannot reach that - the guest's packets would have to arrive from outside.
func ResolverUpstreams() []string {
	b, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return []string{dnsFallback}
	}
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "nameserver" {
			continue
		}
		addr := f[1]
		// A LOOPBACK RESOLVER IS THE CONTAINER'S OWN, not a server the bridge can forwards to.
		if strings.HasPrefix(addr, "127.") || addr == "::1" {
			continue
		}
		if seen[addr] {
			continue
		}
		seen[addr] = true
		out = append(out, addr)
	}
	if len(out) == 0 {
		return []string{dnsFallback}
	}
	return out
}

// startDHCP runs dnsmasq for this bridge, or confirms the one already running is right.
func startDHCP() error {
	// A RUNNING DNSMASQ WITH A DIFFERENT MAC IS NOT REUSABLE, and this is measured: a guest whose
	// MAC changed asked for an address, got none, and the stage reported "no route to host" for a
	// network that was correctly configured for a different machine.
	servedMAC, _ := os.ReadFile(dhcpMacPath)
	if string(servedMAC) == TapMAC && dhcpAnswers() {
		return nil
	}
	if len(servedMAC) > 0 && string(servedMAC) != TapMAC {
		stopDHCP()
	}

	if err := writeDHCPConfig(); err != nil {
		return err
	}
	// THE LEASE FILE IS THIS RUN'S OWN, and it is emptied first: a lease left by a previous guest
	// with the same MAC would be handed to this one and every forward would point at an address
	// the old machine also believes it owns.
	_ = os.WriteFile(dhcpLeasePath, nil, 0o644)

	// THE LOG AND THE PID FILE ARE REMOVED, NOT JUST OVERWRITTEN, AND THE REASON IS A RESTART THAT
	// WAS BLOCKED BY ITS OWN PREVIOUS RUN:
	//
	//	dnsmasq: exit status 3 (dnsmasq: cannot open log /tmp/easyvm-dnsmasq.log: Permission denied)
	//
	// dnsmasq DROPS PRIVILEGES after start-up, so the log it creates is owned by `nobody`:
	//
	//	-rw-rw---- 1 nobody root   477 /tmp/easyvm-dnsmasq.log      <- mode 0660
	//	-rw-r--r-- 1 nobody nogroup  6 /tmp/easyvm-dnsmasq.pid      <- mode 0644
	//
	// THE NEXT RUN STARTS BEFORE IT DROPS them, and root cannot open a 0660 file it does not own:
	//
	//	dnsmasq: cannot open log /tmp/probe.log: Permission denied      exit=3
	//
	// REPRODUCED BY HAND with `touch /tmp/probe.log; chown nobody:root; chmod 660` and the same
	// `--log-facility`. The paths are fixed on purpose (one container holds one guest, so there is
	// nothing to disambiguate), which is exactly why the stale file has to go rather than be trusted.
	//
	// THIS IS NOT A POD-RESTART PROBLEM. `/tmp` here is the container's overlay upper layer, not a
	// tmpfs, so it survives for as long as the CONTAINER does - and a pod that is up for hours serves
	// many runs with `restartCount: 0`. CI starts a fresh container per job and never sees this; the
	// local loop, which re-runs a failing step on a machine that stays up, sees it every time.
	//
	// A REMOVAL RATHER THAN A CHMOD: the file is dnsmasq's, and recreating it is how dnsmasq itself
	// would have made it.
	for _, stale := range []string{dhcpLogPath, dhcpPIDPath} {
		if err := os.Remove(stale); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing the previous run's %s: %w", stale, err)
		}
	}

	cmd := exec.Command("dnsmasq",
		"--conf-file="+dhcpConfPath,
		"--pid-file="+dhcpPIDPath,
		"--log-facility="+dhcpLogPath,
	)
	// IT IS STARTED WITH `Start` AND WAITED EXPLICITLY, BECAUSE THE CHILD IT REPORTS ON IS NOT
	// ALWAYS THE ONE IT LEAVES BEHIND.
	//
	// dnsmasq daemonizes: the process this starts forks the real daemon, writes its pid file and
	// exits. `CombinedOutput` reports THAT exit - usually zero - and the daemon continues as an
	// orphan. In a container whose init is this binary, every such orphan is re-parented to PID 1
	// and, if nothing waits for it, sits as a zombie for the life of the pod:
	//
	//	  73 Zs  dnsmasq     74 Z  dnsmasq
	//	 187 Zs  dnsmasq    188 Z  dnsmasq     ... twelve of them, measured on a pod
	//
	// `CombinedOutput` DOES collect the one process it started, so the zombie is not that one - the
	// twelve were left by previous runs whose own `cmd` value had gone out of scope while the real
	// daemon kept running. Waiting here, on the value we own, is the part this function can do; the
	// rest is PID 1's job and belongs in the container's init, not in a library that may also run
	// under a normal shell.
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("dnsmasq: %v (%s)", err, firstLine(string(out)))
	}
	if err := os.WriteFile(dhcpMacPath, []byte(TapMAC), 0o644); err != nil {
		return err
	}

	// IT BINDS 53 AND 67, so "started" is not "ready": a check that returned immediately would let
	// the first guest ask for an address before anything was listening.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if dhcpAnswers() {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("dnsmasq started but nothing answers on %s:67 within 10s", BridgeIP)
}

// writeDHCPConfig writes the one config this package needs.
//
// THE LEASE IS BY MAC, NOT BY NAME. A guest may present any hostname or none, and a lease matched
// by name would be a lease that depends on what an installer typed.
func writeDHCPConfig() error {
	conf := fmt.Sprintf(`# written by easyvm; one guest per container on %s/%d
interface=%s
bind-interfaces
except-interface=lo
# DHCP ONLY FOR THIS ONE MACHINE. A pool of one is what makes the address a fact.
dhcp-range=%s,%s,255.255.255.0,1h
dhcp-host=%s,%s
dhcp-leasefile=%s
# DNS FOR THE GUEST, WHICH IT CANNOT DO FOR ITSELF ON THIS CLUSTER.
#
# THE GUEST'S OWN QUERIES DO NOT WORK, AND THE MEASUREMENT IS WORTH STATING because "DNS is fine"
# was true of the container and false of everything behind it:
#
#	pod -> 1.1.1.1:53 UDP      answered, 5 records
#	pod -> 8.8.8.8:53 UDP      answered, 5 records
#	guest -> 1.1.1.1:53 UDP    never answered
#
# The difference is the path. The container's own resolver traffic goes out through the cluster's
# networking; a guest's UDP 53 is MASQUERADEd at the bridge and leaves from a different source, and
# that path is dropped - which showed up as a pkg error, Could not connect to server, for every repository
# URL while the pod could fetch the same host at 3 MB/s.
#
# AND THE TRANSPARENT PROXY DOES NOT COVER IT: that chain matches -p tcp only, because mihomo's
# 7890 port speaks HTTP CONNECT, which is a TCP protocol. An iptables -t nat -S listing filtered for
# udp is empty on purpose, so a UDP query cannot be tunnelled the way the guest's TCP is.
#
# SO THIS DAEMON ANSWERS FOR THE GUEST, and forwards to an upstream that DOES work. This is the same
# resolver the container itself uses, so it inherits whatever the cluster does for the container.
#
# DNS IS NO LONGER dnsmasq's JOB. internal/net/dns.go runs its own resolver on the bridge's port 53,
# for one reason nothing else can provide: A RULE ABOUT A NAME CAN ONLY BE ENFORCED WHERE THE NAME IS
# STILL A NAME. The proxy sees 142.250.1.1:443, so a rule for .youtube.com needs somebody who watched
# the query and remembered the answer. dnsmasq answered and forgot. Port 0 turns its DNS half off
# entirely; DHCP is untouched, and DHCP is why dnsmasq is still here at all.
#
# port=0 IS THE WHOLE CHANGE. Without it dnsmasq binds 0.0.0.0:53 and this daemon's resolver cannot
# bind the bridge's 53 - two sockets cannot hold one address, and the failure is an address-in-use
# error that says nothing about DNS being split between two processes.
#
# no-resolv AND THE server= LINES GO WITH IT: dnsmasq no longer answers queries, so a forwarder here
# would be configuration that is read and never used. The upstream list moved to the caller of
# StartDNS, which reads the same /etc/resolv.conf through ResolverUpstreams().
port=0
no-resolv
# THE UPLINK'S MTU, TOLD TO THE CLIENT.
#
# The guest's default is 1500 and a guest larger than its tap drops every full segment - measured
# here as a TLS handshake that never completed, reported as a failed installer download. qemu's
# host_mtu= is the primary fix; this is the second telling, for a client that honours it.
dhcp-option=option:mtu,%d
dhcp-option=option:router,%s
# THE GUEST IS TOLD TO ASK THE BRIDGE, because that is the only DNS address it can reach. The
# cluster's resolver (the server= line above) is on a network the guest has no route to - telling it
# 172.18.0.10 would be a lease that names an address outside its own /24.
dhcp-option=option:dns-server,%s
log-dhcp
`, BridgeName, Prefix, BridgeName, GuestIP, GuestIP, TapMAC, GuestIP, dhcpLeasePath,
		UplinkMTU(), BridgeIP, BridgeIP)

	if err := os.WriteFile(dhcpConfPath, []byte(conf), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", dhcpConfPath, err)
	}
	return nil
}

// dhcpAnswers reports whether dnsmasq has bound its DHCP port on the bridge.
func dhcpAnswers() bool {
	// A UDP socket cannot be "connected to" to prove a listener exists, so the check is the pid
	// file plus a live process - which is what dnsmasq writes and what it removes when it exits.
	pid, err := os.ReadFile(dhcpPIDPath)
	if err != nil {
		return false
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(pid)))
	if err != nil || n <= 0 {
		return false
	}
	_, err = os.Stat(filepath.Join("/proc", strconv.Itoa(n)))
	return err == nil
}

func stopDHCP() {
	pid, err := os.ReadFile(dhcpPIDPath)
	if err != nil {
		return
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(pid)))
	if err != nil || n <= 0 {
		return
	}
	_, _ = run("kill", "-TERM", strconv.Itoa(n))
	// IT IS WAITED FOR, because the next start binds the same ports and a lingering process makes
	// that fail with "Address already in use" - which reads as a broken network.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join("/proc", strconv.Itoa(n))); err != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = os.Remove(dhcpPIDPath)
}

// --- NAT and the forwards --------------------------------------------------------------

// forwardChain is this package's own chain, so its rules can be flushed without touching anything
// else on the host or in the container.
const forwardChain = "EASYVM_FWD"

// applyNAT makes the guest's traffic reach the outside and the host reach the guest.
//
// MASQUERADE IS THE FIRST HALF: a guest at 172.30.0.2 has no route to the internet by itself, so
// everything it sends is rewritten to the container's address on the way out.
func applyNAT() error {
	spec := fmt.Sprintf("%s/%d", BridgeIP, Prefix)

	// A CHAIN OF OUR OWN, so `Down` can remove exactly what `Up` added. Rules added directly to
	// POSTROUTING accumulate one per run and there is no way to tell ours from anybody else's.
	if out, err := run("iptables", "-t", "nat", "-N", forwardChain); err != nil && !alreadyExists(out) {
		return fmt.Errorf("creating the %s chain: %w", forwardChain, err)
	}
	_, _ = run("iptables", "-t", "nat", "-F", forwardChain)

	// THE MASQUERADE RULE IS CHECKED BEFORE IT IS ADDED. `-C` is the only idempotent way with
	// iptables, and a second identical rule is not an error - it is a rule that never matches
	// because the first one did.
	if _, err := run("iptables", "-t", "nat", "-C", "POSTROUTING", "-s", spec,
		"-j", "MASQUERADE"); err != nil {
		if _, err := run("iptables", "-t", "nat", "-A", "POSTROUTING", "-s", spec,
			"-j", "MASQUERADE"); err != nil {
			return fmt.Errorf("MASQUERADE for %s: %w", spec, err)
		}
	}

	// THE TRANSPARENT PROXY GOES HERE, BETWEEN MASQUERADE AND THE MSS CLAMP, AND THE ORDER OF THE
	// iptables CHAIN IS WHY IT IS A SEPARATE FUNCTION RATHER THAN A LINE: REDIRECT has to be jumped
	// to BEFORE anything that DNATs the same packet, and the chain this writes is flushed and rebuilt
	// on every Up.
	if err := applyTransparentProxy(spec); err != nil {
		return err
	}

	// MSS CLAMPING, WHICH IS THE SECOND HALF OF THE MTU FIX AND THE HALF THAT SURVIVES A GUEST THAT
	// IGNORES THE LEASE.
	//
	// THE DHCP LEASE TELLS THE GUEST THE RIGHT MTU, and `Up` sets the bridge and the tap to the
	// uplink's. THAT IS NOT ENOUGH. A guest that ignores the option, a guest whose stack clamps only
	// its own side, and a path whose MTU changes after the lease all fail the same way: full-size
	// segments vanish on a link the container cannot see, no ICMP comes back, and TLS stalls in its
	// handshake because the server's Certificate is the first packet too big to carry.
	//
	// MEASURED, FROM THE REFERENCE IMPLEMENTATION: macOS 15 sat on the Setup Assistant for eight
	// minutes with three ESTABLISHED connections to an Apple CDN whose byte counter did not move.
	// The handshake completed - small packets - and so did the ClientHello; the Certificate did not.
	//
	// CLAMPING REWRITES THE MSS IN THE SYN AND SYN-ACK so the peer never offers more than fits. It
	// costs one rule per direction and removes the silent failure for good.
	//
	//	FORWARD   the guest's traffic on its way out
	//	OUTPUT    this container's own connections, which share the uplink and were measured dropping
	//	          1500-byte packets too
	//
	// THE INSTALL IS CHECKED AND ITS FAILURE IS LOUD. A kernel without xt_TCPMSS reports
	//
	//	Warning: Extension TCPMSS revision 0 not supported, missing kernel module?
	//	iptables v1.8.11 (nf_tables): RULE_APPEND failed (No such file or directory)
	//
	// and a version of this that discarded both return codes made "no module" look exactly like
	// "rule installed", with an empty `iptables -t mangle -L FORWARD` for the life of the guest.
	mss := strconv.Itoa(UplinkMTU() - 40) // 20 bytes of IP header, 20 of TCP
	for _, chain := range []string{"FORWARD", "OUTPUT"} {
		rule := []string{"-t", "mangle", "-C", chain, "-p", "tcp", "-m", "tcp",
			"--tcp-flags", "SYN,RST", "SYN", "-j", "TCPMSS", "--set-mss", mss}
		if _, err := run("iptables", rule...); err == nil {
			continue
		}
		add := []string{"-t", "mangle", "-A", chain, "-p", "tcp", "-m", "tcp",
			"--tcp-flags", "SYN,RST", "SYN", "-j", "TCPMSS", "--set-mss", mss}
		if out, err := run("iptables", add...); err != nil {
			// A MISSING MODULE IS REPORTED AND THE RUN CONTINUES, because the DHCP lease still
			// carries the right MTU and a guest that honours it works. Silently pretending the rule
			// exists is the failure this message replaces.
			fmt.Printf("  network: MSS clamping for %s is unavailable: %s\n",
				chain, firstLine(out))
		}
	}

	// FORWARDING FOR THIS PAIR ONLY. The container's default FORWARD policy is usually DROP, so a
	// guest with a lease and a route still cannot be reached - and the symptom is "timed out",
	// which reads as a dead guest rather than a filter.
	for _, rule := range [][]string{
		{"-s", spec, "-j", "ACCEPT"},
		{"-d", GuestIP + "/32", "-j", "ACCEPT"},
	} {
		args := append([]string{"-C", "FORWARD"}, rule...)
		if _, err := run("iptables", args...); err != nil {
			add := append([]string{"-A", "FORWARD"}, rule...)
			if _, err := run("iptables", add...); err != nil {
				return fmt.Errorf("FORWARD ACCEPT for %v: %w", rule, err)
			}
		}
	}
	return nil
}

// applyForwards publishes the guest's ports on the bridge address.
//
// DNAT, NOT qemu's hostfwd. With a tap there is no qemu-side forwarding at all: the host addresses
// the guest directly, so a port is published by rewriting its destination. That is also why
// `ssh 172.30.0.1:2222` reaches the guest's 22 without qemu being involved.
//
// THE CHAIN IS OURS, AND IT IS ENTERED FROM TWO PLACES - WHICH IS THE WHOLE POINT.
//
//	PREROUTING   a packet ARRIVING from outside, on its way to the bridge address
//	OUTPUT       a connection made BY A PROCESS IN THIS CONTAINER to the bridge address
//
// AND THE SECOND ONE IS NOT OPTIONAL HERE, BECAUSE EVERY CLIENT IN THIS PROGRAM IS INSIDE THE
// CONTAINER. The daemon's own ssh client dials 172.30.0.1:2222, which is an OUTPUT packet;
// PREROUTING never sees it. Without the OUTPUT jump the SYN is delivered to the bridge interface
// itself, nothing listens on port 2222 there, and the kernel answers
//
//	dial tcp 172.30.0.1:2222: connect: connection refused
//
// in three milliseconds - which is exactly what run 43022310562 reported after the whole Windows
// stage had worked: the desktop was up in 3m43s, OpenSSH was installed, sshd was configured, and
// the step that connects to it was refused by the container's own stack.
func applyForwards(sshPort int, extra []int) error {
	ports := buildForwardTable(sshPort, extra)

	spec := fmt.Sprintf("%s/%d", BridgeIP, Prefix)

	// ENTER FROM OUTSIDE. This is the case a published port exists for: something outside the
	// container connects to the bridge address.
	if _, err := run("iptables", "-t", "nat", "-C", "PREROUTING", "-d", spec,
		"-j", forwardChain); err != nil {
		if _, err := run("iptables", "-t", "nat", "-A", "PREROUTING", "-d", spec,
			"-j", forwardChain); err != nil {
			return fmt.Errorf("entering %s from PREROUTING: %w", forwardChain, err)
		}
	}

	// AND ENTER FROM THIS CONTAINER'S OWN PROCESSES.
	//
	// `-p tcp` AND THE BRIDGE ADDRESS ARE BOTH LOAD-BEARING, and the Python implementation paid
	// for each separately:
	//
	//	no address   the rule also caught probes to 127.0.0.1:2222 and DNAT'd them to the guest.
	//	             DNAT rewrites the DESTINATION and leaves the SOURCE alone, so the guest saw a
	//	             SYN from 127.0.0.1 and replied to what is, on the guest, its OWN loopback. The
	//	             reply never arrived and nothing reset the connection, so the probe sat on its
	//	             connect timeout - ten minutes, on a disk that was fine (run 42188488710):
	//
	//	                 172.30.0.1:2222  -> refused in 3ms   (reply came back; the link is fine)
	//	                 127.0.0.1:2222   -> timed out at 4s   (reply went to the guest's loopback)
	//
	// So the match is the bridge address, which is what every client in this program dials.
	if _, err := run("iptables", "-t", "nat", "-C", "OUTPUT", "-d", spec,
		"-p", "tcp", "-j", forwardChain); err != nil {
		if out, err := run("iptables", "-t", "nat", "-A", "OUTPUT", "-d", spec,
			"-p", "tcp", "-j", forwardChain); err != nil {
			return fmt.Errorf("entering %s from OUTPUT: %v (%s). Without this rule a client "+
				"INSIDE this container - the ssh client included - is refused by the bridge "+
				"address, because that connection never passes PREROUTING",
				forwardChain, err, firstLine(out))
		}
	}

	for host, guest := range ports {
		rule := []string{"-p", "tcp", "-d", spec, "--dport", strconv.Itoa(host),
			"-j", "DNAT", "--to-destination", fmt.Sprintf("%s:%d", GuestIP, guest)}
		// THE DESTINATION IS THE INTERFACE ADDRESS, AND THAT IS DELIBERATE: the Python
		// implementation measured a rule written against 127.0.0.1 answering "connection timed
		// out", because DNAT rewrote the destination and the reply went to the guest's loopback.
		args := append([]string{"-t", "nat", "-A", forwardChain}, rule...)
		if _, err := run("iptables", args...); err != nil {
			return fmt.Errorf("forwarding %d -> %d: %w", host, guest, err)
		}
	}
	return nil
}

// buildForwardTable is the set of ports to publish, and the decision is worth its own function:
// it is the half of applyForwards that can be tested without root.
//
// SSH IS ALWAYS THERE. A network without it is one where a failure can only be looked at, never
// asked about - and every stage reaches its guest over ssh.
//
// AN EXTRA PORT IS FORWARDED TO ITSELF. `adb_port` and a Termux sshd on 8022 are cases where the
// guest's port is what the file names; renumbering it here would be a second place to keep in step
// with the file, and the two would disagree.
func buildForwardTable(sshPort int, extra []int) map[int]int {
	ports := map[int]int{sshPort: 22}
	for _, p := range extra {
		if p > 0 {
			ports[p] = p
		}
	}
	return ports
}
