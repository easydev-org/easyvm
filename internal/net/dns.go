// THE GUEST'S DNS, WHICH NOW LIVES IN github.com/easydev-org/easytun/names.
//
// THIS FILE IS THE PART THAT IS THIS DAEMON'S: the two addresses the guest and the bridge use, the
// environment switch for fake-ip, and the upstream list read from the container's own resolv.conf. The
// resolver, the name table and the pool are the library's - none of it needs to know what a guest is.
package net

import (
	"net"
	"os"

	"github.com/easydev-org/easytun/names"
)

// dnsPort is the port the resolver binds and the redirect targets. It is a constant rather than two
// 53s so the two cannot drift.
const dnsPort = 53

// dnsServer is the process-wide instance, so a shutdown that is not a full Down() can stop it.
var dnsServer *names.Server

// StopDNS stops the resolver.
func StopDNS() {
	if dnsServer != nil {
		dnsServer.Close()
		dnsServer = nil
	}
}

// StartDNS binds the bridge's port 53.
//
// IT BINDS THE BRIDGE AND NOT 0.0.0.0. The guest is told to ask the bridge (the DHCP lease says so), and
// a resolver reachable from the pod's own eth0 would be a second way in for no benefit.
//
// dnsmasq MUST HAVE GIVEN UP 53 FIRST, which is why `port=0` is in its configuration now - two sockets
// cannot bind the same address, and the failure would be this daemon refusing to start with an
// address-in-use error that says nothing about DNS being split between two processes.
func StartDNS(upstreams []string) (*names.Server, error) {
	s, err := names.Start(names.Config{
		Bind:      net.JoinHostPort(BridgeIP, "53"),
		Upstreams: upstreams,
		FakeIP:    FakeIPEnabled(),
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}

// NamesFor returns the names an address was resolved for.
//
// IT IS THE READ SIDE the rule engine and the proxy both use, and it is wired into the engine once in
// this package's init. An unknown address has no names, and a name rule cannot match it - which is the
// honest boundary rather than a failure.
func NamesFor(ip net.IP) []string { return names.For(ip) }

// FakeIPEnabled reports whether the resolver should invent addresses instead of forwarding.
func FakeIPEnabled() bool { return os.Getenv("EASYVM_FAKE_IP") == "1" }

// NameForFakeIP answers "is this address one this daemon invented, and for what name".
func NameForFakeIP(ip net.IP) (string, bool) {
	if !FakeIPEnabled() {
		return "", false
	}
	return names.NameForFakeIP(ip)
}

// resolveReal answers a name the way the upstream would, ignoring this daemon's fake pool.
//
// IT IS NEEDED BECAUSE THE DAEMON'S OWN RESOLVER LIES IN THIS MODE: a `direct` rule that dialled a fake
// address would connect back to this daemon.
func resolveReal(name string) (net.IP, error) {
	return names.ResolveReal(ResolverUpstreams(), name)
}
