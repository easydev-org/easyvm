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
	dhcpUpstreams = "1.1.1.1,8.8.8.8"
)

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

	cmd := exec.Command("dnsmasq",
		"--conf-file="+dhcpConfPath,
		"--pid-file="+dhcpPIDPath,
		"--log-facility="+dhcpLogPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
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
# NO DNS SERVICE. The guest uses the upstream directly, and dnsmasq's resolver on this bridge
# would answer for a domain nobody asked it about.
port=0
# THE UPLINK'S MTU, TOLD TO THE CLIENT.
#
# The guest's default is 1500 and a guest larger than its tap drops every full segment - measured
# here as a TLS handshake that never completed, reported as a failed installer download. qemu's
# host_mtu= is the primary fix; this is the second telling, for a client that honours it.
dhcp-option=option:mtu,%d
dhcp-option=option:router,%s
dhcp-option=option:dns-server,%s
log-dhcp
`, BridgeName, Prefix, BridgeName, GuestIP, GuestIP, TapMAC, GuestIP, dhcpLeasePath, MTU,
		BridgeIP, dhcpUpstreams)

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
// THE CHAIN IS OURS, AND IT IS ENTERED FROM PREROUTING. A rule added to PREROUTING directly cannot
// be removed without matching its exact spec, and the Python implementation had four of them.
func applyForwards(sshPort int, extra []int) error {
	ports := buildForwardTable(sshPort, extra)

	spec := fmt.Sprintf("%s/%d", BridgeIP, Prefix)
	if _, err := run("iptables", "-t", "nat", "-C", "PREROUTING", "-d", spec,
		"-j", forwardChain); err != nil {
		if _, err := run("iptables", "-t", "nat", "-A", "PREROUTING", "-d", spec,
			"-j", forwardChain); err != nil {
			return fmt.Errorf("entering %s from PREROUTING: %w", forwardChain, err)
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
