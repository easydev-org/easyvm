package net

// THE GUEST'S DNS IS SENT TO THIS DAEMON'S RESOLVER, WHETHER OR NOT THE GUEST CO-OPERATES.
//
// THE DHCP LEASE ALREADY TELLS THE GUEST TO ASK THE BRIDGE, and for a guest that honours it that is
// enough. This file exists for the guest that does not: an installer with 8.8.8.8 compiled in, a
// resolver configuration restored from a disk image, a `dig @1.1.1.1` in a provisioning script. Those
// queries leave the guest addressed to somebody else, and without these rules they would go out
// through MASQUERADE and be answered by an upstream that this daemon never sees - so the answer would
// exist but the name would not be recorded, and every domain rule for that name would silently fail
// to match.
//
// IT IS EXACTLY THE HALF THAT MAKES NAME RULES WORK: the proxy and the relay see addresses, and the
// only place a name exists is in the query. Redirecting the query is what guarantees this daemon sees
// it.
//
// ONLY UDP: this daemon's resolver listens on UDP, and a TCP query redirected to a UDP socket would
// be accepted by the kernel and then produce nothing the client can read. DNS over TCP is rare - a
// truncated answer, a zone transfer - and leaving it un-redirected means it still resolves (through
// the guest's chosen server) but without being recorded. That is stated rather than hidden: the
// failure mode is one un-matched rule for one large answer, not a broken resolver.
//
// THE CHAIN IS ENTERED FROM PREROUTING, WHICH IS WHERE A FORWARDED PACKET IS FIRST SEEN. The guest's
// query is forwarded (source 172.30.0.2, not this container), so OUTPUT - where a locally made
// connection is judged - never sees it.

import (
	"fmt"
	"strconv"
)

// dnsChain is this package's own chain, so its rules can be flushed without touching anything else,
// for the same reason forwardChain and proxyChain are separate.
const dnsChain = "EASYVM_DNS"

// applyDNSRedirect sends the guest's UDP :53 to this daemon's resolver on the bridge.
//
// IT DOES NOTHING WHEN THE RESOLVER IS NOT RUNNING, and that is the degraded case, not the failure:
// if StartDNS could not bind, redirecting queries here would send them to a closed port and make DNS
// fail outright, which is worse than letting them leave un-recorded. So the caller passes whether the
// resolver is up, and false means "flush and leave" - the same shape applyTransparentProxy uses when
// no proxy is configured.
func applyDNSRedirect(spec string, resolverUp bool) error {
	if !resolverUp {
		// FLUSH AND LEAVE, because a restart in a container that HAD one must not keep sending
		// queries to a socket that is gone.
		_, _ = run("iptables", "-t", "nat", "-F", dnsChain)
		_, _ = run("iptables", "-t", "nat", "-D", "PREROUTING", "-s", spec, "-j", dnsChain)
		return nil
	}

	if out, err := run("iptables", "-t", "nat", "-N", dnsChain); err != nil && !alreadyExists(out) {
		return fmt.Errorf("creating the %s chain: %v (%s)", dnsChain, err, firstLine(out))
	}
	_, _ = run("iptables", "-t", "nat", "-F", dnsChain)

	// THE JUMP GOES IN BEFORE ANYTHING THAT WOULD DNAT THE SAME PACKET. The transparent proxy's chain
	// is entered from PREROUTING too, and although it matches only `-p tcp`, putting this jump first
	// keeps the two from being reordered by a future change that adds a UDP rule there.
	if _, err := run("iptables", "-t", "nat", "-C", "PREROUTING", "-s", spec, "-j", dnsChain); err != nil {
		if out, err := run("iptables", "-t", "nat", "-I", "PREROUTING", "-s", spec,
			"-j", dnsChain); err != nil {
			return fmt.Errorf("entering %s from PREROUTING: %v (%s)", dnsChain, err, firstLine(out))
		}
	}

	port := strconv.Itoa(dnsPort)
	for _, rule := range [][]string{
		// THIS CONTAINER'S OWN QUERIES ARE NOT THE GUEST'S. The chain is entered by source, so this
		// cannot normally match - but the rule states the intent, and it protects a future change
		// that widens the jump.
		{"-d", spec, "-j", "RETURN"},
		// A QUERY ALREADY ADDRESSED TO THE RESOLVER IS ALREADY WHERE IT BELONGS. Without this the
		// DNAT below would rewrite it to the same address and the kernel would see a no-op rule
		// matching every query; harmless, but a pointless DNAT per packet.
		{"-d", BridgeIP + "/32", "-p", "udp", "--dport", "53", "-j", "RETURN"},
		// EVERYTHING ELSE ADDRESSED TO PORT 53 BECOMES A QUERY TO THE RESOLVER.
		//
		// DNAT TO THE BRIDGE ADDRESS, NOT REDIRECT: the guest's packet is FORWARDED, so 127.0.0.1 is
		// not an address it can be delivered to - the same measurement that made the TCP proxy use
		// DNAT (see applyTransparentProxy).
		{"-p", "udp", "--dport", "53", "-j", "DNAT", "--to-destination", BridgeIP + ":" + port},
	} {
		args := append([]string{"-t", "nat", "-A", dnsChain}, rule...)
		if out, err := run("iptables", args...); err != nil {
			return fmt.Errorf("dns rule %v: %v (%s)", rule, err, firstLine(out))
		}
	}
	return nil
}

// removeDNSRedirect undoes applyDNSRedirect for Down().
func removeDNSRedirect(spec string) {
	_, _ = run("iptables", "-t", "nat", "-D", "PREROUTING", "-s", spec, "-j", dnsChain)
	_, _ = run("iptables", "-t", "nat", "-F", dnsChain)
	_, _ = run("iptables", "-t", "nat", "-X", dnsChain)
}
