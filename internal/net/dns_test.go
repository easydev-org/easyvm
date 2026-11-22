package net

import (
	"encoding/binary"
	"net"
	"testing"
	"time"
)

// THE RESOLVER HAD NO TESTS AT ALL, and it is the piece that makes every domain rule work: a rule for
// `.youtube.com` matches only because this server watched the query and remembered the answer. The
// three parts below are the three places that can go wrong.

// --- the answer parser ---

// buildAnswer assembles a DNS message the way a real resolver would: one question, and the given A
// records as answers. A pointer is used for the answer's name, because that is what real resolvers
// emit and it is the case the parser's compression handling exists for.
func buildAnswer(name string, addrs []net.IP, ttl uint32) []byte {
	var q []byte
	for _, label := range splitLabels(name) {
		q = append(q, byte(len(label)))
		q = append(q, label...)
	}
	q = append(q, 0)    // end of name
	q = append(q, 0, 1) // QTYPE A
	q = append(q, 0, 1) // QCLASS IN

	msg := make([]byte, 12)
	binary.BigEndian.PutUint16(msg[4:6], 1)                  // QDCOUNT
	binary.BigEndian.PutUint16(msg[6:8], uint16(len(addrs))) // ANCOUNT
	msg = append(msg, q...)

	for _, a := range addrs {
		msg = append(msg, 0xc0, 12) // pointer to the question's name at offset 12
		var rr [10]byte
		binary.BigEndian.PutUint16(rr[0:2], 1)   // TYPE A
		binary.BigEndian.PutUint16(rr[2:4], 1)   // CLASS IN
		binary.BigEndian.PutUint32(rr[4:8], ttl) // TTL
		binary.BigEndian.PutUint16(rr[8:10], 4)  // RDLENGTH
		msg = append(msg, rr[:]...)
		msg = append(msg, a.To4()...)
	}
	return msg
}

func splitLabels(name string) []string {
	var out []string
	cur := ""
	for _, c := range name {
		if c == '.' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(c)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// THE QUESTION IS READ OUT OF THE MESSAGE, lowercased and without a trailing dot, because that is
// the form a rule pattern is compared against.
func TestParseQuestionReadsTheName(t *testing.T) {
	msg := buildAnswer("YouTube.COM", []net.IP{net.IPv4(142, 250, 1, 1)}, 60)
	name, qtype, ok := parseQuestion(msg)
	if !ok {
		t.Fatal("parseQuestion rejected a well-formed query")
	}
	if name != "youtube.com" {
		t.Errorf("name = %q, want youtybe.com lowercased", name)
	}
	if qtype != 1 {
		t.Errorf("qtype = %d, want 1 (A)", qtype)
	}
}

// THE ANSWERS ARE THE WHOLE POINT: these addresses are what a later connection will carry.
func TestParseAnswersReadsAAndAAAA(t *testing.T) {
	v4 := buildAnswer("example.com", []net.IP{net.IPv4(93, 184, 216, 34)}, 300)
	got := parseAnswers(v4)
	if len(got) != 1 {
		t.Fatalf("got %d answers, want 1", len(got))
	}
	if got[0].ip != "93.184.216.34" {
		t.Errorf("ip = %q", got[0].ip)
	}
	if got[0].ttl != 300 {
		t.Errorf("ttl = %d, want 300", got[0].ttl)
	}
}

// A MALFORMED MESSAGE IS NOT A PANIC. The resolver forwards whatever a client sent, and the upstream
// may answer with something truncated; a parser that indexed past the end would crash the daemon.
func TestParseSurvivesTruncatedInput(t *testing.T) {
	for _, b := range [][]byte{
		nil,
		{},
		make([]byte, 3),
		make([]byte, 12),
		append(make([]byte, 12), 0xff, 0xff),
	} {
		// Neither must panic.
		_, _, _ = parseQuestion(b)
		_ = parseAnswers(b)
		_ = skipName(b, 0)
	}
}

// A COMPRESSION POINTER IS TWO BYTES AND ENDS THE NAME, whatever it points at - the answer's own name
// is almost always a pointer into the question.
func TestSkipNameHandlesAPointer(t *testing.T) {
	msg := []byte{0xc0, 0x0c, 0x00, 0x01}
	if got := skipName(msg, 0); got != 2 {
		t.Fatalf("skipName on a pointer = %d, want 2", got)
	}
}

// --- the name table ---

// REMEMBER THEN LOOK UP is the contract the proxy depends on, and a wrong direction here would make
// every domain rule match nothing - silently.
func TestRememberThenNamesFor(t *testing.T) {
	table := &dnstable{byIP: map[string]*dnsEntry{}}
	table.remember("142.250.1.1", "youtube.com", time.Minute)

	got := table.namesFor("142.250.1.1")
	if len(got) != 1 || got[0] != "youtube.com" {
		t.Fatalf("namesFor = %v, want [youtube.com]", got)
	}
	if names := table.namesFor("1.2.3.4"); names != nil {
		t.Errorf("an unknown address has names %v, want none", names)
	}
}

// ONE ADDRESS CAN SERVE MANY NAMES, which is what a CDN does. A rule matching ANY of them must match
// the connection, so every name is kept rather than the last one winning.
func TestAnAddressRemembersEveryName(t *testing.T) {
	table := &dnstable{byIP: map[string]*dnsEntry{}}
	table.remember("1.2.3.4", "a.example", time.Minute)
	table.remember("1.2.3.4", "b.example", time.Minute)

	got := table.namesFor("1.2.3.4")
	if len(got) != 2 {
		t.Fatalf("namesFor = %v, want both names", got)
	}
}

// A ZERO TTL MEANS "DO NOT CACHE", and honouring it is what stops a rule from matching an address a
// load balancer has already stopped using.
func TestAZeroTTLIsNotCached(t *testing.T) {
	table := &dnstable{byIP: map[string]*dnsEntry{}}
	table.remember("1.2.3.4", "x.example", 0)
	if names := table.namesFor("1.2.3.4"); names != nil {
		t.Fatalf("a zero-TTL answer was cached: %v", names)
	}
}

// AN EXPIRED ENTRY IS NOT A NAME, so a stale address cannot keep matching a rule.
func TestAnExpiredEntryDisappears(t *testing.T) {
	table := &dnstable{byIP: map[string]*dnsEntry{}}
	table.remember("1.2.3.4", "x.example", time.Nanosecond)
	time.Sleep(2 * time.Millisecond)
	if names := table.namesFor("1.2.3.4"); names != nil {
		t.Fatalf("an expired entry was returned: %v", names)
	}
}

// --- the join: a recorded name makes a domain rule match ---

// THIS IS THE TEST THAT WOULD HAVE FOUND THE DEAD RESOLVER. Before `StartDNS` was called, the table
// was always empty, so every `.domain` rule matched nothing - and nothing said so.
func TestARecordedNameMakesADomainRuleMatch(t *testing.T) {
	lockPolicy(t)
	if err := SetUpstreams([]Upstream{
		{Name: "default", HTTP: "d:1", SOCKS5: "d:2"},
		{Name: "fast", HTTP: "fast:1", SOCKS5: "fast:2"},
	}); err != nil {
		t.Fatal(err)
	}

	if err := SetEgress(Policy{
		Default: Verdict{Kind: KindProxy},
		Rules:   []Rule{{Match: ".youtube.com", Verdict: Verdict{Kind: KindProxy, Upstream: "fast"}}},
	}); err != nil {
		t.Fatal(err)
	}

	// AN ADDRESS NO OTHER TEST RECORDS, because dnsNames is process-wide: a shared address would let
	// one test's recorded name make another test's "before" assertion fail, which is exactly what
	// happened when this used 142.250.1.1.
	ip := net.IPv4(203, 0, 113, 77)
	if v := VerdictFor(ip, 443); v.Upstream != "" {
		t.Fatalf("before the name is known, verdict = %+v, want the default", v)
	}

	// WITH THE NAME RECORDED - which is what the resolver does when the guest asks - the rule matches.
	dnsNames.remember(ip.String(), "www.youtube.com", time.Minute)
	defer func() {
		dnsNames.mu.Lock()
		delete(dnsNames.byIP, ip.String())
		dnsNames.mu.Unlock()
	}()

	if v := VerdictFor(ip, 443); v.Upstream != "fast" {
		t.Fatalf("with the name recorded, verdict = %+v, want proxy:fast", v)
	}
}
