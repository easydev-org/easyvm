package net

// FAKE-IP IS A SET OF INVARIANTS, NOT A FEATURE WITH A HAPPY PATH. Each test below pins one property
// that, if broken, produces a connection that goes to the WRONG host rather than one that fails - and
// a wrong host is worse than a failure, because nothing reports it.

import (
	"net"
	"sync"
	"testing"
	"time"
)

// resetFakeIP clears the pool between tests, since it is process-wide like the name table.
// resetFakeIP clears the pool and turns the mode on.
//
// ITS LOCK IS ITS OWN, not policyMu: a test that needs both (a fake address matching a domain rule)
// takes lockPolicy and then this, and sharing one mutex would deadlock on the second acquire. The two
// protect different globals - this one the pool and the mode flag, that one the policy tables.
func resetFakeIP(t *testing.T) {
	t.Helper()
	fakeMu.Lock()
	prev := setFakeIP(true)
	fakeIPs.mu.Lock()
	oldNames := fakeIPs.byName
	oldIPs := fakeIPs.byIP
	fakeIPs.byName = map[string]*fakeAlloc{}
	fakeIPs.byIP = map[uint32]string{}
	fakeIPs.mu.Unlock()

	t.Cleanup(func() {
		fakeIPs.mu.Lock()
		fakeIPs.byName = oldNames
		fakeIPs.byIP = oldIPs
		fakeIPs.mu.Unlock()
		setFakeIP(prev)
		fakeMu.Unlock()
	})
}

// fakeMu serialises the tests that touch the fake pool and its mode flag.
var fakeMu sync.Mutex

// A NAME GETS AN ADDRESS OUT OF THE RFC 2544 RANGE, and the same name gets the SAME address twice -
// because a guest that re-asks must not get a second address for what is one flow.
func TestFakeIPIsStableAndInsideTheRange(t *testing.T) {
	resetFakeIP(t)
	first := allocateFakeIP("www.youtube.com")
	if first == nil {
		t.Fatal("no address allocated")
	}
	if _, ok := indexOfIP(first); !ok {
		t.Fatalf("%v is outside 198.18.0.0/15, so it could collide with a real host", first)
	}
	second := allocateFakeIP("www.youtube.com")
	if !first.Equal(second) {
		t.Fatalf("the same name got %v then %v", first, second)
	}
}

// TWO NAMES DO NOT SHARE AN ADDRESS, EVEN WHEN THEIR HASHES COLLIDE. The probe is what guarantees this;
// a pure hash would give the second name the first one's address and route it to the wrong host.
func TestTwoNamesNeverShareAnAddress(t *testing.T) {
	resetFakeIP(t)
	seen := map[string]string{}
	for i := 0; i < 500; i++ {
		name := "host" + itoa(i) + ".example"
		ip := allocateFakeIP(name)
		if ip == nil {
			t.Fatal("pool exhausted at 500 names")
		}
		if other, ok := seen[ip.String()]; ok {
			t.Fatalf("%q and %q both got %v", other, name, ip)
		}
		seen[ip.String()] = name
	}
}

// THE ADDRESS READS BACK AS ITS NAME, which is the whole reason it was invented: the proxy needs the
// name to reach the host, and the address alone points nowhere.
func TestTheAddressReadsBackAsItsName(t *testing.T) {
	resetFakeIP(t)
	ip := allocateFakeIP("api.github.com")
	name, ok := NameForFakeIP(ip)
	if !ok || name != "api.github.com" {
		t.Fatalf("NameForFakeIP(%v) = %q, %v", ip, name, ok)
	}
}

// A REAL ADDRESS IS NOT A FAKE ONE. The bool is false outside the range whatever is in the table, so a
// caller cannot route a real destination through the fake lookup.
func TestARealAddressIsNotInThePool(t *testing.T) {
	resetFakeIP(t)
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "198.17.255.255", "198.20.0.1", "10.0.0.1"} {
		if name, ok := NameForFakeIP(net.ParseIP(s)); ok {
			t.Errorf("%s read back as %q, but it is outside the pool", s, name)
		}
	}
}

// WHEN THE MODE IS OFF NOTHING IS INVENTED, which is the default because fake-ip changes what a guest
// sees in a DNS answer.
func TestThePoolIsInertWhenFakeIPIsOff(t *testing.T) {
	resetFakeIP(t)
	ip := allocateFakeIP("example.com")
	setFakeIP(false)
	defer setFakeIP(true)
	if name, ok := NameForFakeIP(ip); ok {
		t.Fatalf("with the mode off, %v still read back as %q", ip, name)
	}
}

// AN EXPIRED ALLOCATION IS FREE, and its name is gone with it - so a second name can take the address
// without the first name still reading back from it.
func TestAnExpiredAllocationIsReusable(t *testing.T) {
	resetFakeIP(t)
	ip := allocateFakeIP("gone.example")
	// Expire it by hand rather than waiting five minutes.
	fakeIPs.mu.Lock()
	fakeIPs.byName["gone.example"].exp = time.Now().Add(-time.Second)
	fakeIPs.mu.Unlock()

	if name, ok := NameForFakeIP(ip); ok {
		t.Fatalf("an expired allocation still reads back as %q", name)
	}
	// And the same name, asked again, gets a live allocation.
	again := allocateFakeIP("gone.example")
	if name, ok := NameForFakeIP(again); !ok || name != "gone.example" {
		t.Fatalf("a re-allocated name reads back as %q, %v", name, ok)
	}
}

// THE ANSWER IS A WELL-FORMED DNS RESPONSE naming the fake address, and AAAA IS ANSWERED EMPTY so the
// client falls back to IPv4 - the only family the proxy and the relay carry.
func TestFakeAnswerIsAValidResponse(t *testing.T) {
	resetFakeIP(t)
	q := query("www.youtube.com")

	reply := fakeAnswer(q, "www.youtube.com", 1)
	if reply == nil {
		t.Fatal("no answer built")
	}
	// The ID is echoed, QR is set, and there is one answer.
	if reply[0] != q[0] || reply[1] != q[1] {
		t.Error("the query id was not echoed")
	}
	if reply[2]&0x80 == 0 {
		t.Error("QR is not set, so this is not a response")
	}
	answers := parseAnswers(reply)
	if len(answers) != 1 {
		t.Fatalf("got %d answers, want 1", len(answers))
	}
	ip := net.ParseIP(answers[0].ip)
	if _, ok := indexOfIP(ip); !ok {
		t.Fatalf("the answer %s is outside the pool", answers[0].ip)
	}
	if name, _ := NameForFakeIP(ip); name != "www.youtube.com" {
		t.Fatalf("the answered address reads back as %q", name)
	}

	// AAAA IS AN EMPTY NOERROR.
	aaaa := fakeAnswer(q, "www.youtube.com", 28)
	if aaaa == nil {
		t.Fatal("no AAAA answer built")
	}
	if got := parseAnswers(aaaa); len(got) != 0 {
		t.Fatalf("AAAA got %d records, want none", len(got))
	}
}

// THE RECORDED NAME MAKES A DOMAIN RULE MATCH with no change to the matcher, which is the point: the
// fake address is looked up exactly like a resolved one.
func TestAFakeAddressMatchesADomainRule(t *testing.T) {
	resetFakeIP(t)
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

	ip := allocateFakeIP("www.youtube.com")
	if v := VerdictFor(ip, 443); v.Upstream != "fast" {
		t.Fatalf("the fake address got %+v, want proxy:fast", v)
	}
}

// splitName IS THE WIRE-FORM INPUT, so a trailing dot must not produce an empty label.
func TestSplitNameHandlesATrailingDot(t *testing.T) {
	got := splitName("example.com.")
	want := []string{"example", "com"}
	if len(got) != len(want) {
		t.Fatalf("splitName = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("splitName = %v, want %v", got, want)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// A DIRECT RULE MUST SEE THROUGH THE FAKE POOL, or it would dial an address this daemon invented and
// connect back to itself.
//
// `resolveReal` asks the UPSTREAMS directly - it builds a query and calls the same forwarder the
// resolver uses, without going through `handle`, which is where the fake answer is produced. This test
// pins that: a fake upstream answers a real address, and the fake pool must not appear in the answer.
func TestResolveRealBypassesTheFakePool(t *testing.T) {
	resetFakeIP(t)

	const real = "93.184.216.34"
	upstream := fakeUpstream(t, net.ParseIP(real), 60)
	prev := resolverUpstreamsFn
	resolverUpstreamsFn = func() []string { return []string{upstream} }
	t.Cleanup(func() { resolverUpstreamsFn = prev })

	got, err := resolveReal("example.com")
	if err != nil {
		t.Fatalf("resolveReal: %v", err)
	}
	if got.String() != real {
		t.Fatalf("resolveReal = %v, want %s - it must ask the upstream, not the fake pool", got, real)
	}
	if _, inPool := indexOfIP(got); inPool {
		t.Fatalf("resolveReal returned a fake address %v", got)
	}
}

// AND AN UPSTREAM THAT ANSWERS NOTHING IS AN ERROR, so a direct rule refuses the connection rather
// than dialling a zero address.
func TestResolveRealFailsWhenTheUpstreamIsSilent(t *testing.T) {
	resetFakeIP(t)
	prev := resolverUpstreamsFn
	resolverUpstreamsFn = func() []string { return []string{"127.0.0.1:1"} }
	t.Cleanup(func() { resolverUpstreamsFn = prev })

	if _, err := resolveReal("nowhere.example"); err == nil {
		t.Fatal("resolveReal succeeded with no upstream")
	}
}
