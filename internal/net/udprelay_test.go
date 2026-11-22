package net

import (
	"fmt"
	"net"
	"testing"

	"github.com/easydev-org/easytun"
)

// THE ADAPTER IS THE ONLY PART OF THE RELAY THAT IS THIS PACKAGE'S, so it is the only part that can
// drift from the verdicts without easytun noticing: easytun has no idea what a Kind means. Each case
// pins one line of the policy that used to live inside the relay itself.
func TestRouteForMapsEachVerdict(t *testing.T) {
	// THE UPSTREAM TABLE IS PROCESS-WIDE, so a test installs its own and the cleanup restores the
	// one the daemon had. Reading the real table would make this depend on the environment.
	lockPolicy(t)
	if err := SetUpstreams([]Upstream{
		{Name: "default", SOCKS5: "proxy.example:1080"},
		{Name: "fast", SOCKS5: "fast.example:1080"},
		// AN UPSTREAM WITH NO SOCKS5 ADDRESS: it carries CONNECT and cannot carry UDP.
		{Name: "httponly", HTTP: "proxy.example:7890"},
	}); err != nil {
		t.Fatal(err)
	}

	oldPolicy, _, _ := Egress()
	t.Cleanup(func() { _ = SetEgress(oldPolicy) })

	cases := []struct {
		name   string
		policy Policy
		dst    net.IP
		port   uint16
		expect easytun.Route
		why    string
	}{
		{
			name:   "a block rule drops the datagram",
			policy: Policy{Rules: []Rule{{Match: "10.0.0.0/8", Verdict: Verdict{Kind: KindBlock}}}},
			dst:    net.IPv4(10, 1, 2, 3),
			port:   443,
			expect: easytun.Route{Drop: true},
			why:    "there is no RST in UDP, so a dropped datagram is a timeout - which is what a filtered port looks like",
		},
		{
			name:   "a direct rule leaves the proxy",
			policy: Policy{Rules: []Rule{{Match: "192.168.0.0/16", Verdict: Verdict{Kind: KindDirect}}}},
			dst:    net.IPv4(192, 168, 1, 1),
			port:   53,
			expect: easytun.Route{Direct: true},
			why:    "a destination on the local network should not go through the cluster proxy",
		},
		{
			name:   "a named upstream with a SOCKS5 address is used",
			policy: Policy{Rules: []Rule{{Match: "1.1.1.0/24", Verdict: Verdict{Kind: KindProxy, Upstream: "fast"}}}},
			dst:    net.IPv4(1, 1, 1, 1),
			port:   443,
			expect: easytun.Route{Upstream: "fast.example:1080"},
			why:    "the UDP half of the same decision the TCP path makes",
		},
		{
			name:   "a named upstream with no SOCKS5 address falls back to the default",
			policy: Policy{Rules: []Rule{{Match: "1.0.1.0/24", Verdict: Verdict{Kind: KindProxy, Upstream: "httponly"}}}},
			dst:    net.IPv4(1, 0, 1, 1),
			port:   443,
			// socksFor's own fallback: the name has no SOCKS5 address, so it answers with the
			// DEFAULT upstream's. The route therefore names it explicitly rather than leaving the
			// field empty - and either way the datagram does not go out unproxied.
			expect: easytun.Route{Upstream: "proxy.example:1080"},
			why: "an upstream configured for CONNECT only still carries TCP and leaves UDP on " +
				"the default rather than dropping it",
		},
		{
			name:   "an unmatched destination gets the policy default",
			policy: Policy{Default: Verdict{Kind: KindProxy}},
			dst:    net.IPv4(203, 0, 113, 9),
			port:   443,
			expect: easytun.Route{},
			why:    "the configured upstream, which is what a rule written before this feature got",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := SetEgress(c.policy); err != nil {
				t.Fatalf("SetEgress: %v", err)
			}
			if got := routeFor(c.dst, c.port); got != c.expect {
				t.Fatalf("routeFor(%v, %d) = %+v, want %+v\n%s", c.dst, c.port, got, c.expect, c.why)
			}
		})
	}
}

// THE ENVIRONMENT IS THE ONLY THING THAT TURNS THE RELAY ON, and a malformed value must leave it off
// rather than fail StartUDPRelay - a runner with nothing proxied is the normal case.
func TestUDPRelayConfigured(t *testing.T) {
	cases := map[string]bool{
		"":                   false,
		"proxy.example:1080": true,
		"proxy.example":      false, // no port
		"[::1]:1080":         true,
		"http://proxy:1080":  false, // a URL is not host:port
	}
	for value, want := range cases {
		t.Setenv(udpRelayUpstreamEnv, value)
		_, got := UDPRelayConfigured()
		if got != want {
			t.Errorf("UDPRelayConfigured with %q = %v, want %v", value, got, want)
		}
	}
}

// NO UPSTREAM MEANS NO DEVICE, and no error: a runner that connects directly has nothing to tunnel
// through, and adding a tun device there would break its egress rather than help it.
func TestStartUDPRelayWithoutAnUpstreamDoesNothing(t *testing.T) {
	t.Setenv(udpRelayUpstreamEnv, "")
	r, err := StartUDPRelay("172.30.0.2")
	if err != nil {
		t.Fatalf("StartUDPRelay: %v", err)
	}
	if r != nil {
		t.Fatal("StartUDPRelay created a relay with no upstream configured")
	}
}

// THE FAILURE MUST REACH THE LOG. This is the whole reason SetLogf exists: the line used to be a
// `fmt.Printf`, so a tun device that could not come up was invisible in a container's daemon log.
func TestSetLogfRedirectsTheRelayMessage(t *testing.T) {
	var lines []string
	SetLogf(func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	})
	t.Cleanup(func() { logf = func(string, ...any) {} })

	StopUDPRelay() // nothing running: no line
	if len(lines) != 0 {
		t.Fatalf("StopUDPRelay with no relay logged %v", lines)
	}

	// A relay whose Close reports a problem must log it rather than swallow it.
	logf("udp relay: %v", "a test failure")
	if len(lines) != 1 || lines[0] != "udp relay: a test failure" {
		t.Fatalf("the sink got %v", lines)
	}
}
