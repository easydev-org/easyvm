package net

import (
	"os"
	"strings"
	"testing"
)

// THE DNS REDIRECT IS THE HALF THAT MAKES NAME RULES WORK FOR A GUEST THAT IGNORES ITS LEASE.
//
// These tests pin the SHAPE of the rules rather than running iptables, because the rule text is the
// thing a reader can get wrong - `REDIRECT` instead of `DNAT`, a missing `-p udp`, the jump in the
// wrong chain - and each mistake has a distinct, silent failure mode.

// THE CHAIN IS ENTERED FROM PREROUTING BY SOURCE. A guest's query is FORWARDED (source 172.30.0.2),
// so OUTPUT never sees it; entering from anywhere else would simply not match.
func TestDNSRedirectEntries(t *testing.T) {
	src := sourceOf(t, "dnsredirect.go")
	for _, want := range []struct{ text, why string }{
		{`"PREROUTING", "-s", spec`, "the guest's query is forwarded, so PREROUTING is where it is first seen"},
		{`"DNAT", "--to-destination", BridgeIP`, "a forwarded packet cannot be delivered to 127.0.0.1, so DNAT to the bridge"},
		{`"-p", "udp", "--dport", "53"`, "the resolver listens on UDP; a TCP query would be delivered and produce nothing"},
		{`const dnsChain = "EASYVM_DNS"`, "a chain of our own can be flushed and deleted without touching anything else"},
	} {
		if !strings.Contains(src, want.text) {
			t.Errorf("dnsredirect.go lost %q: %s", want.text, want.why)
		}
	}
	// AND NOT REDIRECT, which rewrites to 127.0.0.1 - the exact bug that kept the TCP proxy from
	// reaching a guest (see applyTransparentProxy's measured comment).
	if strings.Contains(src, `"REDIRECT"`) {
		t.Error("dnsredirect.go uses REDIRECT; a forwarded guest packet cannot be delivered to 127.0.0.1")
	}
}

// WITH NO RESOLVER THE REDIRECT MUST NOT EXIST, because sending queries to a closed port makes DNS
// fail outright - worse than letting them out un-recorded.
func TestDNSRedirectIsFlushedWhenNoResolver(t *testing.T) {
	src := sourceOf(t, "dnsredirect.go")
	if !strings.Contains(src, "if !resolverUp {") {
		t.Fatal("applyDNSRedirect does not branch on resolverUp, so it would redirect into a closed port")
	}
	// The branch must both FLUSH and remove the jump; either alone leaves half a redirect behind.
	body := src[strings.Index(src, "if !resolverUp {"):]
	body = body[:strings.Index(body, "return nil")]
	if !strings.Contains(body, `"-F", dnsChain`) {
		t.Error("the no-resolver branch does not flush the chain")
	}
	if !strings.Contains(body, `"-D", "PREROUTING"`) {
		t.Error("the no-resolver branch does not remove the PREROUTING jump, so it would point at a flushed chain")
	}
}

// AND UP/DOWN MUST AGREE: every chain Up creates, Down removes. A jump left behind points at a chain
// that no longer exists, which iptables fails on silently here and accumulates per run.
func TestDownRemovesTheDNSRedirect(t *testing.T) {
	src := sourceOf(t, "net.go")
	if !strings.Contains(src, "applyDNSRedirect(") {
		t.Fatal("Up does not install the DNS redirect")
	}
	if !strings.Contains(src, "removeDNSRedirect(spec)") {
		t.Fatal("Down does not remove the DNS redirect")
	}
}

// THE RESOLVER IS STARTED, WHICH IS THE WHOLE DEFECT THIS WORK FIXED.
//
// `StartDNS` existed complete and was called from nowhere, so `dnsNames` was always empty and every
// domain rule matched nothing - silently. This asserts the call exists, because deleting it again
// would restore that failure with no test failing anywhere else.
func TestUpStartsTheResolver(t *testing.T) {
	src := sourceOf(t, "net.go")
	if !strings.Contains(src, "StartDNS(") {
		t.Fatal("Up does not start the resolver, so every domain rule would match nothing")
	}
	if !strings.Contains(src, "StopDNS()") {
		t.Fatal("Up does not stop a previous resolver, so a restart would fail to bind port 53")
	}
}

func sourceOf(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(b)
}
