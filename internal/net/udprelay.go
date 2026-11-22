// THE GUEST'S UDP, THROUGH A TUN DEVICE THE DAEMON OWNS.
//
// THE MECHANISM LIVES IN github.com/easydev-org/easytun. This file is the part that is specific to a
// virtual machine: it reads the daemon's environment, turns an egress verdict into easytun's Route
// callback, and holds the process-wide instance so a shutdown that is not a full Down() can stop it.
//
// WHY A SEPARATE LIBRARY. The relay needed nothing from this package - only the standard library and
// three ten-line helpers - so leaving it here meant every service that wanted to proxy UDP through a
// tun device reimplemented the SOCKS5 framing and the IFF_NO_PI measurement. What is left is the
// local policy, which is the part easytun deliberately does not have.
package net

import (
	"fmt"
	"net"
	"os"

	"github.com/easydev-org/easytun"
)

// udpRelayUpstreamEnv names the SOCKS5 address the relay forwards to.
//
// IT IS SEPARATE FROM `EASYVM_TRANSPARENT_PROXY` ON PURPOSE. That variable is an HTTP proxy address
// and its port speaks CONNECT; a SOCKS5 UDP relay is usually a DIFFERENT port on the same host
// (mihomo: 7890 HTTP, 7891 SOCKS5). Deriving one from the other by adding one would be a guess that
// happens to be right for one implementation.
const udpRelayUpstreamEnv = "EASYVM_UDP_PROXY"

// tunRelayName is the device's name. It is a name a person would not pick by hand, so it cannot
// collide with one they created.
const tunRelayName = "easyvm-udp"

// udpRelay is the process-wide instance, so a shutdown that is not a full Down() can stop it - the
// same arrangement StopTransparentProxy has with `proxy`.
var udpRelay *easytun.Relay

// StartUDPRelay brings up the tun device and begins forwarding.
//
// IT RETURNS NIL WITHOUT AN ERROR WHEN NO UPSTREAM IS CONFIGURED, which is the CI case: a runner that
// connects directly has nothing to tunnel through, and adding a tun device there would break its
// egress rather than help it.
func StartUDPRelay(guestIP string) (*easytun.Relay, error) {
	upstream, ok := UDPRelayConfigured()
	if !ok {
		return nil, nil
	}
	return easytun.Start(easytun.Config{
		GuestIP:  guestIP,
		Upstream: upstream,
		Name:     tunRelayName,
		// POLICY: THE SAME RULES THE TCP PROXY USES, APPLIED TO A DATAGRAM. easytun does not know
		// what a verdict is; this callback is where the two meet.
		Route: routeFor,
		// THE DEVICE'S FAILURES ARE THIS DAEMON'S LOG. easytun prints nothing by itself, so a
		// per-packet error does not vanish into a container's stdout.
		Logf: func(format string, args ...any) { logf("udp relay: "+format, args...) },
	})
}

// routeFor turns an egress verdict into what easytun should do with one datagram.
//
// `block` drops it: there is no RST in UDP, so the guest's QUIC client simply times out, which is
// what a filtered UDP port looks like everywhere. `direct` sends it from this pod with no proxy. A
// named upstream is resolved through the same table the TCP path uses, and an upstream that has no
// SOCKS5 address falls back to the default - so an upstream configured for CONNECT only still
// carries TCP and leaves UDP on the default rather than dropping it.
func routeFor(dst net.IP, port uint16) easytun.Route {
	// THE NAME, WHEN THIS DAEMON INVENTED THE ADDRESS. In fake-ip mode the destination is a fake
	// address, so `UDP ASSOCIATE 198.18.7.3` would ask the proxy for a host that exists nowhere - the
	// name is the only thing that reaches the real server, and it is also what a name rule matched.
	host, _ := NameForFakeIP(dst)

	v := VerdictFor(dst, int(port))
	switch v.Kind {
	case KindBlock:
		return easytun.Route{Drop: true, Host: host}
	case KindDirect:
		return easytun.Route{Direct: true, Host: host}
	}
	if v.Upstream != "" {
		if addr, ok := socksFor(v.Upstream); ok {
			return easytun.Route{Upstream: addr, Host: host}
		}
	}
	return easytun.Route{Host: host}
}

// UDPRelayConfigured reports the SOCKS5 address this build was told to use.
func UDPRelayConfigured() (string, bool) {
	v := os.Getenv(udpRelayUpstreamEnv)
	if v == "" {
		return "", false
	}
	if _, _, err := net.SplitHostPort(v); err != nil {
		return "", false
	}
	return v, true
}

// StopUDPRelay stops it and removes its device and route.
func StopUDPRelay() {
	if udpRelay != nil {
		if err := udpRelay.Close(); err != nil {
			logf("udp relay: %v", err)
		}
		udpRelay = nil
	}
}

// logf is the adapter's one line of output. It is a variable so the daemon can install its sink and
// a bare `go test` still sees the message.
var logf = func(format string, args ...any) {
	fmt.Printf("  "+format+"\n", args...)
}

// SetLogf points this package's network messages at a caller's sink.
//
// IT EXISTS BECAUSE THE FAILURE WAS SILENT IN A CONTAINER. `fmt.Printf` writes to stdout, and the
// daemon's log is what a stage reads - so a tun device that could not be brought up produced one line
// in a terminal nobody was watching while the guest's QUIC timed out twenty minutes earlier. The
// daemon calls this before Up, and the same message now lands in the log the run reports.
func SetLogf(f func(format string, args ...any)) {
	if f != nil {
		logf = f
	}
}
