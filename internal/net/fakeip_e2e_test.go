package net

// THE WHOLE FAKE-IP CHAIN, WITH THE PART THAT MATTERS MOST: the destination the PROXY is told.
//
// The tests in fakeip_test.go prove the pool and the DNS answer. This one proves the join that the
// feature exists for - that a guest's connection to an address this daemon invented becomes a CONNECT
// to the NAME, because an address that exists nowhere cannot be asked of a proxy.

import (
	"net"
	"testing"
	"time"
)

// A GUEST ASKS A NAME, GETS A FAKE ADDRESS, AND THE CONNECTION THAT FOLLOWS RESOLVES BACK TO THE NAME.
//
// This is the sequence a guest performs, and every step is the real code: the resolver's handle, the
// pool, and the lookup the proxy uses.
func TestTheFakeAddressBecomesTheNameTheProxyConnectsTo(t *testing.T) {
	resetFakeIP(t)
	server := newTestServer(t, "127.0.0.1:1") // never used: fake-ip answers without forwarding
	defer server.Close()

	client, err := net.DialUDP("udp", nil, server.conn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Write(query("www.youtube.com")); err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1500)
	n, err := client.Read(buf)
	if err != nil {
		t.Fatalf("no answer from the resolver: %v", err)
	}

	answers := parseAnswers(buf[:n])
	if len(answers) != 1 {
		t.Fatalf("got %d answers, want 1", len(answers))
	}
	fake := net.ParseIP(answers[0].ip)
	if _, ok := indexOfIP(fake); !ok {
		t.Fatalf("%s is not in the fake pool", answers[0].ip)
	}

	// THE DESTINATION THE PROXY IS TOLD. In proxy.go this is the branch that replaces `dst` with the
	// name; here it is the same calls, so a change to either is caught.
	name, ok := NameForFakeIP(fake)
	if !ok {
		t.Fatal("the fake address did not resolve back to a name")
	}
	if name != "www.youtube.com" {
		t.Fatalf("the address resolved to %q, want www.youtube.com", name)
	}
	connectTo := net.JoinHostPort(name, "443")
	if connectTo != "www.youtube.com:443" {
		t.Fatalf("the CONNECT target is %q", connectTo)
	}
}

// AN AAAA QUERY GETS AN EMPTY ANSWER, SO THE CLIENT USES IPv4 - and then the connection still carries
// the name. A guest that preferred an IPv6 answer would connect where the proxy cannot follow.
func TestAAAAIsAnsweredEmptyAndTheNameStillResolves(t *testing.T) {
	resetFakeIP(t)
	server := newTestServer(t, "127.0.0.1:1")
	defer server.Close()

	q := query("dual.example")
	// QTYPE 28 = AAAA. The question ends [0][QTYPE:2][QCLASS:2].
	q[len(q)-4], q[len(q)-3] = 0, 28

	client, err := net.DialUDP("udp", nil, server.conn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, _ = client.Write(q)
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1500)
	n, err := client.Read(buf)
	if err != nil {
		t.Fatalf("no AAAA answer: %v", err)
	}
	if got := parseAnswers(buf[:n]); len(got) != 0 {
		t.Fatalf("AAAA got %d records, want an empty NOERROR", len(got))
	}

	// AND THE A RECORD FOR THE SAME NAME IS INVENTED, so the client falls back to it.
	a := fakeAnswer(query("dual.example"), "dual.example", 1)
	got := parseAnswers(a)
	if len(got) != 1 {
		t.Fatalf("the A answer has %d records, want 1", len(got))
	}
	if name, ok := NameForFakeIP(net.ParseIP(got[0].ip)); !ok || name != "dual.example" {
		t.Fatalf("the A record reads back as %q, %v", name, ok)
	}
}
