package net

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// THE PREFLIGHT IS THE ONE THING THIS PACKAGE MUST GET RIGHT BEFORE IT TOUCHES ANYTHING.
//
// The Python implementation found a missing capability at `ip link add`, where the error is
//
//	RTNETLINK answers: Operation not permitted
//
// which reads as a broken network rather than a missing `--cap-add NET_ADMIN`. A stage then spends
// its budget looking at the guest. The three failures below have three different fixes, so they are
// three different errors.

func TestPreflightNamesTheMissingDevice(t *testing.T) {
	// ON A MACHINE WITH /dev/net/tun THIS CANNOT BE SIMULATED WITHOUT ROOT, so the check is that
	// the error TYPE is distinguishable and the message says what to do. What matters is that a
	// caller can tell "no tun" from "no NET_ADMIN" without parsing prose.
	if _, err := os.Stat("/dev/net/tun"); err == nil {
		t.Skip("/dev/net/tun exists here; the absent-device path cannot be exercised without " +
			"removing it, and a test that removed it would break the machine it runs on")
	}
	err := Preflight()
	if err == nil {
		t.Fatal("Preflight must fail when /dev/net/tun is absent")
	}
	if !errors.Is(err, ErrNoTun) {
		t.Fatalf("expected ErrNoTun, got %v", err)
	}
	if !strings.Contains(err.Error(), "--device /dev/net/tun") {
		t.Fatalf("the message must name the flag that fixes it; got %q", err.Error())
	}
	// AND IT MUST SAY WHY USER-MODE IS NOT A SUBSTITUTE, because that is the first thing a reader
	// tries when a tap is unavailable.
	if !strings.Contains(err.Error(), "smbd") {
		t.Fatalf("the message must explain why qemu's user-mode networking cannot replace the "+
			"tap; got %q", err.Error())
	}
}

// THE ADDRESSING IS FIXED AND THE FIXTURES DEPEND ON IT. A change here invalidates every recorded
// frame's coordinates and every `ssh 172.30.0.2` in a log.
func TestAddressingIsFixed(t *testing.T) {
	cases := map[string]string{
		"BridgeIP": BridgeIP,
		"GuestIP":  GuestIP,
		"TapMAC":   TapMAC,
	}
	want := map[string]string{
		"BridgeIP": "172.30.0.1",
		"GuestIP":  "172.30.0.2",
		"TapMAC":   "52:54:00:56:4d:01",
	}
	for name, got := range cases {
		if got != want[name] {
			t.Fatalf("%s changed to %q; every measurement in this repository was taken with %q",
				name, got, want[name])
		}
	}
	if Prefix != 24 {
		t.Fatalf("prefix changed to /%d; the DNSMASQ pool and the DNAT rules are written for /24",
			Prefix)
	}
}

// A GUEST LARGER THAN ITS TAP IS THE BROKEN CASE. Measured on this cluster: guest 1500 over a 1450
// tap drops every full segment, and the symptom is a TLS handshake that never completes - reported
// as a failed installer download rather than as a network fault.
func TestMTUIsSmallEnoughForTheUplink(t *testing.T) {
	if MTU >= 1500 {
		t.Fatalf("MTU is %d; a tap at 1500 on a 1450-byte uplink drops every full segment, and "+
			"the guest would report a failed download with no network error", MTU)
	}
	if MTU < 1280 {
		t.Fatalf("MTU is %d, below IPv6's minimum; a guest on this link would fail to "+
			"establish anything", MTU)
	}
}

// THE FORWARD TABLE IS DERIVED, AND SSH IS NEVER OPTIONAL.
//
// A network without ssh is one where a failure can only be looked at, never asked about - so the
// port is added regardless of what the caller passed, and a caller that passed it twice gets one
// rule rather than two.
func TestSSHIsAlwaysForwarded(t *testing.T) {
	// applyForwards is not called here because it needs root; the TABLE it builds is the part with
	// a decision in it, so it is tested through the same helper the code uses.
	ports := buildForwardTable(2222, nil)
	if guest, ok := ports[2222]; !ok || guest != 22 {
		t.Fatalf("2222 must forward to the guest's 22; got %v", ports)
	}

	ports = buildForwardTable(2222, []int{2222, 5555})
	if len(ports) != 2 {
		t.Fatalf("2222 asked for twice must be one rule; got %v", ports)
	}
	// A PORT FORWARDED TO ITSELF, for a Termux sshd on 8022 or adb on 5555.
	if guest, ok := ports[5555]; !ok || guest != 5555 {
		t.Fatalf("5555 must forward to itself; got %v", ports)
	}
}

// alreadyExists IS THE ONLY WAY TO MAKE `ip` AND `iptables` IDEMPOTENT, because both use exit 2 for
// everything. It is text matching, so it is tested against the exact strings it must recognise and
// one it must not.
func TestAlreadyExistsRecognisesTheRealMessages(t *testing.T) {
	for _, msg := range []string{
		"RTNETLINK answers: File exists",
		"Error: Device \"qemubr0\" already exists.",
		"iptables: Chain already exists.",
	} {
		if !alreadyExists(errors.New(msg)) {
			t.Fatalf("%q must be recognised as 'already there'; without it a second run fails", msg)
		}
	}
	for _, msg := range []string{
		"RTNETLINK answers: Operation not permitted",
		"RTNETLINK answers: No such device",
	} {
		if alreadyExists(errors.New(msg)) {
			t.Fatalf("%q must NOT be treated as 'already there'; that would hide a real failure",
				msg)
		}
	}
	if alreadyExists(nil) {
		t.Fatal("nil is not 'already exists'")
	}
}

// THE DHCP CONFIG IS A CONTRACT WITH dnsmasq, and three lines in it are load-bearing.
func TestDHCPConfigCarriesTheLoadBearingLines(t *testing.T) {
	if err := writeDHCPConfig(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(dhcpConfPath)
	if err != nil {
		t.Fatal(err)
	}
	conf := string(body)

	for _, want := range []struct{ line, why string }{
		{"dhcp-host=" + TapMAC + "," + GuestIP,
			"the lease is by MAC; a guest that presents any hostname still gets the same address"},
		{"port=0", "no DNS service: dnsmasq's resolver on this bridge would answer for a domain " +
			"nobody asked it about"},
		{"option:mtu", "the guest must learn the tap's MTU, or every full segment is dropped"},
		{"interface=" + BridgeName, "bind to this bridge only, not every interface in the container"},
	} {
		if !strings.Contains(conf, want.line) {
			t.Fatalf("the DHCP config lost %q: %s\ngot:\n%s", want.line, want.why, conf)
		}
	}
	// AND THE LEASE FILE IS THIS RUN'S OWN, so a lease from a previous guest is not inherited.
	if !strings.Contains(conf, "dhcp-leasefile="+dhcpLeasePath) {
		t.Fatalf("the lease file must be this package's own; got:\n%s", conf)
	}
}
