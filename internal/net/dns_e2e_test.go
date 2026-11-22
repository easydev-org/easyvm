package net

import (
	"net"
	"testing"
	"time"
)

// THE WHOLE CHAIN, ON THE WIRE: a query goes to the resolver, the answer's address is recorded, and a
// later connection to that address takes the path the domain rule chose.
//
// IT STARTS FROM A NAME, WHICH IS THE PART THAT NEVER WORKED BEFORE. Every other test here sets up
// one link; this one runs the two in sequence, the way a guest does:
//
//	ask www.youtube.com        -> the resolver answers 142.250.1.1 and remembers it
//	connect to 142.250.1.1:443 -> VerdictFor finds the name and returns proxy:fast
//
// A regression in either half breaks this, and the failure it guards against is the one that shipped:
// the resolver was complete and was never started, so the second step silently got the default.
func TestTheDomainRuleMatchesThroughARealQuery(t *testing.T) {
	oldU := UpstreamsCopy()
	t.Cleanup(func() { _ = SetUpstreams(oldU) })
	if err := SetUpstreams([]Upstream{
		{Name: "default", HTTP: "d:1", SOCKS5: "d:2"},
		{Name: "fast", HTTP: "fast:1", SOCKS5: "fast:2"},
	}); err != nil {
		t.Fatal(err)
	}
	oldP, _, _ := Egress()
	t.Cleanup(func() { _ = SetEgress(oldP) })
	if err := SetEgress(Policy{
		Default: Verdict{Kind: KindProxy},
		Rules:   []Rule{{Match: ".youtube.com", Verdict: Verdict{Kind: KindProxy, Upstream: "fast"}}},
	}); err != nil {
		t.Fatal(err)
	}

	const answer = "203.0.113.5" // TEST-NET-3, so this address belongs to nothing real
	upstream := fakeUpstream(t, net.ParseIP(answer), 300)
	server := newTestServer(t, upstream)
	defer server.Close()

	// --- a guest asks, and gets an answer ---
	client, err := net.DialUDP("udp", nil, server.conn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Write(query("www.youtube.com")); err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := client.Read(make([]byte, 1500)); err != nil {
		t.Fatalf("the resolver did not answer: %v", err)
	}

	// --- and the connection that follows is routed by the NAME ---
	//
	// The record is written by the server's handler goroutine, so this waits for it rather than
	// assuming it landed before the read returned - the answer is sent before the recording, on
	// purpose, because a client must not wait on bookkeeping.
	ip := net.ParseIP(answer)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(NamesFor(ip)) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if names := NamesFor(ip); len(names) == 0 {
		t.Fatal("the resolver answered but recorded nothing, so no domain rule can match")
	}

	v := VerdictFor(ip, 443)
	if v.Upstream != "fast" {
		t.Fatalf("the connection to %s got %+v, want proxy:fast through the .youtube.com rule", answer, v)
	}

	// AND THE CONNECTION THE RULE WOULD HAVE MISSED: a different address from the same query name
	// space that nobody resolved gets the default, which is what makes the match above meaningful.
	if other := VerdictFor(net.ParseIP("203.0.113.6"), 443); other.Upstream != "" {
		t.Fatalf("an unresolved address got %+v, want the default", other)
	}
}
