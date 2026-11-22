package net

// FAKE-IP: ANSWER A NAME WITH AN ADDRESS THIS DAEMON INVENTED, SO THE CONNECTION THAT FOLLOWS STILL
// CARRIES THE NAME.
//
// THE PROBLEM REAL-IP MODE CANNOT SOLVE. A rule is written about a name, and a connection carries an
// address. The resolver records `address -> name`, so the rule matches - but only for the addresses
// that this guest actually resolved, and only until the TTL runs out:
//
//	one name, many addresses   a CDN answers with four; the guest connects to a fifth that another
//	                           resolver gave it, and that address was never recorded
//	stale addresses            the name moved, the guest cached the old answer, the address no longer
//	                           maps to anything
//	the guest's own resolver   a query that went out before the redirect was in place, or through a
//	                           path this daemon never saw
//
// Each is a rule that silently matches nothing, which is the failure this whole area keeps producing.
//
// WHAT FAKE-IP DOES INSTEAD. It does not record the real answer - it REPLACES it. `www.youtube.com`
// is answered with an address out of a private range that belongs to nothing (`198.18.0.0/15`, RFC
// 2544's benchmark range), and the daemon remembers that address is a name. When the guest connects
// to it, the destination still carries the name, because the daemon put the name there. There is no
// lookup that can miss: the mapping is the only reason that address exists.
//
//	guest asks www.youtube.com        -> 198.18.7.3, and the daemon notes 198.18.7.3 = www.youtube.com
//	guest connects 198.18.7.3:443     -> the daemon reads the name back and CONNECTs www.youtube.com
//
// THE COST, STATED PLAINLY: a connection this daemon cannot name cannot be made at all. A program that
// dials a literal address - `curl https://1.1.1.1` - gets a real address from no resolver, so it never
// enters the fake range and works normally. But a program that resolves a name through a resolver this
// daemon does not see, and then connects to the real address, bypasses the fake pool entirely - it is
// not blocked, it is simply not subject to a name rule. That is the same boundary real-ip mode has,
// and it is why the DNS redirect exists: the more queries this daemon sees, the smaller that hole is.
//
// IT IS A MODE, NOT A REPLACEMENT. `EASYVM_FAKE_IP=1` turns it on. Off, the resolver forwards and
// records real answers exactly as before - which is the right default, because fake-ip changes what a
// guest sees and a bake that inspects a DNS answer would notice.

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"time"
)

// THE POOL IS RFC 2544's BENCHMARK RANGE, 198.18.0.0/15.
//
// IT IS PRIVATE IN THE ONLY SENSE THAT MATTERS: it is reserved for network equipment testing, so no
// real host is at one of these addresses, and a connection to one can only be to this daemon. A
// public range would risk a real destination behind a broken fake-ip lookup; a 10/8 or 192.168/16
// address could collide with the container's own network. 131072 addresses is far more than one
// guest's working set, which is what keeps reuse from recycling an address a live connection holds.
const (
	fakeNet   = "198.18.0.0/15"
	fakeBase  = uint32(198)<<24 | uint32(18)<<16
	fakeCount = 1 << 17         // a /15 is 131072 addresses
	fakeTTL   = 10              // seconds in the DNS answer: short, so a guest re-asks and the pool stays fresh
	fakeKeep  = 5 * time.Minute // how long an allocation is remembered past its last use
)

// fakeTable maps between names and the addresses this daemon invented for them.
//
// BOTH DIRECTIONS ARE KEPT because both are asked for, at different times: the resolver asks "what
// address does this name get", and the proxy asks "what name is this address". Deriving one from the
// other each time would be a search over the table on every packet.
type fakeTable struct {
	mu     sync.Mutex
	byName map[string]*fakeAlloc
	byIP   map[uint32]string
}

type fakeAlloc struct {
	idx  uint32
	name string
	exp  time.Time
}

var fakeIPs = &fakeTable{byName: map[string]*fakeAlloc{}, byIP: map[uint32]string{}}

// enabled is the mode flag, read once at startup. A var rather than a const so a test can turn it on
// without an environment variable leaking into every other test.
var fakeIPEnabled = osGetenv("EASYVM_FAKE_IP") == "1"

// FakeIPEnabled reports whether the resolver invents addresses.
func FakeIPEnabled() bool { return fakeIPEnabled }

// setFakeIP is for tests: it changes the mode and returns the previous value to restore.
func setFakeIP(on bool) bool {
	prev := fakeIPEnabled
	fakeIPEnabled = on
	return prev
}

// fakeIPFor returns the address this name is answered with, allocating one on first use.
//
// THE ALLOCATION IS A HASH AND THEN A LINEAR PROBE, and the reason is that a name should keep its
// address across re-queries. A guest that asks twice and gets two addresses is a guest making two
// connections the daemon sees as different flows; stable allocation makes them the same flow, which is
// what a keep-alive connection expects. The probe is what makes it correct when two names hash to one
// slot - a pure hash would give them the same address and the second connection would take the first
// name's route.
func (t *fakeTable) fakeIPFor(name string) net.IP {
	t.mu.Lock()
	defer t.mu.Unlock()

	if a, ok := t.byName[name]; ok {
		a.exp = time.Now().Add(fakeKeep)
		return ipOfIndex(a.idx)
	}

	start := fnvIndex(name)
	for probe := uint32(0); probe < fakeCount; probe++ {
		idx := (start + probe) % fakeCount
		holder, taken := t.byIP[idx]
		if taken && !t.expired(holder) {
			continue
		}
		// FREE OR EXPIRED. An expired holder's forward entry must go too, or the name would be
		// answered with the address a second name now owns.
		if taken {
			if old, ok := t.byName[holder]; ok && old.idx == idx {
				delete(t.byName, holder)
			}
		}
		t.byIP[idx] = name
		t.byName[name] = &fakeAlloc{idx: idx, name: name, exp: time.Now().Add(fakeKeep)}
		// AND THE NAME GOES IN THE REAL TABLE TOO, so `VerdictFor(ip, port)` matches a name rule with
		// no change to the matcher: the fake address is looked up exactly like a resolved one.
		dnsNames.remember(ipOfIndex(idx).String(), name, fakeKeep)
		return ipOfIndex(idx)
	}
	// THE POOL IS FULL, which for one guest means 131072 live names - not a state that happens. Nil is
	// returned rather than an arbitrary address, because a wrong answer would route a connection to
	// another name's destination.
	return nil
}

// expired reports whether an allocation's name has outlived its last use. The caller holds the lock.
func (t *fakeTable) expired(name string) bool {
	a, ok := t.byName[name]
	return !ok || time.Now().After(a.exp)
}

// nameFor returns the name an address was invented for, and whether it is in the pool at all.
//
// THE POOL CHECK COMES FIRST: a real address is not in `byIP`, so this is also how a caller tells a
// fake destination from a real one without a second function.
func (t *fakeTable) nameFor(ip net.IP) (string, bool) {
	v4 := ip.To4()
	if v4 == nil {
		return "", false
	}
	idx, ok := indexOfIP(v4)
	if !ok {
		return "", false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	name, ok := t.byIP[idx]
	if !ok || t.expired(name) {
		return "", false
	}
	return name, true
}

// sweep drops allocations nobody has used for fakeKeep, so a long-lived daemon does not fill the pool
// with names from machines that are gone.
func (t *fakeTable) sweep() {
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	for name, a := range t.byName {
		if now.After(a.exp) {
			delete(t.byName, name)
			if holder, ok := t.byIP[a.idx]; ok && holder == name {
				delete(t.byIP, a.idx)
			}
		}
	}
}

// --- the range arithmetic ---

func ipOfIndex(idx uint32) net.IP {
	v := fakeBase + idx
	return net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

func indexOfIP(ip net.IP) (uint32, bool) {
	v := binary.BigEndian.Uint32(ip.To4())
	if v < fakeBase || v >= fakeBase+fakeCount {
		return 0, false
	}
	return v - fakeBase, true
}

// fnvIndex spreads names across the pool, so a small working set does not cluster at the front.
func fnvIndex(name string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(name); i++ {
		h ^= uint32(name[i])
		h *= 16777619
	}
	return h % fakeCount
}

// --- what the rest of the package uses ---

// NameForFakeIP answers "is this address one of ours, and for what name".
//
// IT IS THE ONE FUNCTION THE PROXY AND THE RELAY NEED, and it folds the pool check into the lookup so
// a caller cannot accidentally treat a real address as a fake one - the bool is false for anything
// outside 198.18.0.0/15, whatever is in the table.
func NameForFakeIP(ip net.IP) (string, bool) {
	if !fakeIPEnabled {
		return "", false
	}
	return fakeIPs.nameFor(ip)
}

// allocateFakeIP is the resolver's entry point.
func allocateFakeIP(name string) net.IP {
	return fakeIPs.fakeIPFor(name)
}

// resolveReal answers a name the way the UPSTREAM would, bypassing this daemon's fake pool.
//
// IT IS NEEDED BECAUSE THE DAEMON'S OWN RESOLVER LIES IN THIS MODE: asking the bridge's resolver for
// `youtube.com` returns a fake address, and a `direct` rule that then dialled it would connect back to
// this daemon. So a direct connection resolves the name against the upstreams directly. The pool is
// the point of fake-ip; the daemon is the one caller that must see through it.
func resolveReal(name string) (net.IP, error) {
	reply := forwardForReal(name)
	if reply == nil {
		return nil, fmt.Errorf("could not resolve %s against any upstream", name)
	}
	for _, rr := range parseAnswers(reply) {
		if ip := net.ParseIP(rr.ip); ip != nil && ip.To4() != nil {
			return ip, nil
		}
	}
	return nil, fmt.Errorf("%s resolved to no IPv4 address", name)
}

// fakeAnswer builds a DNS response naming this daemon's address for the query, or nil when the query
// is not one this can answer.
//
// AN EMPTY ANSWER IS DELIBERATE FOR AAAA. A guest that gets a fake IPv4 address and no IPv6 address
// falls back to IPv4, which is the only family the proxy and the relay carry. Answering AAAA with
// something would invite a connection this daemon cannot route.
func fakeAnswer(query []byte, name string, qtype uint16) []byte {
	// THE QUESTION MUST END WHERE THE ANSWER BEGINS, and skipName is how that offset is found - a
	// compressed question would otherwise make every offset after it wrong.
	if len(query) < 12 {
		return nil
	}
	end := skipName(query, 12)
	if end < 0 || end+4 > len(query) {
		return nil
	}
	qEnd := end + 4 // QTYPE + QCLASS

	// THE RESPONSE HEADER, WITH THE QUERY'S OWN ID AND QUESTION COPIED VERBATIM. `0x81 0x80` is
	// QR=1 (a response), RD=1 (recursion desired, echoed), RA=1 (recursion available).
	msg := make([]byte, 12)
	copy(msg[0:2], query[0:2]) // ID
	msg[2], msg[3] = 0x81, 0x80
	binary.BigEndian.PutUint16(msg[4:6], 1) // QDCOUNT
	if qtype == 1 {
		binary.BigEndian.PutUint16(msg[6:8], 1) // ANCOUNT
	}
	msg = append(msg, query[12:qEnd]...)

	if qtype != 1 {
		// AAAA AND EVERYTHING ELSE: NOERROR WITH NO RECORDS, which is an answer a client accepts.
		return msg
	}

	ip := allocateFakeIP(name)
	if ip == nil {
		// THE POOL IS FULL. SERVFAIL rather than a wrong address: a client that gets SERVFAIL retries
		// or falls back, where a client given another name's address would connect to the wrong host.
		msg[3] = 0x82 // RCODE 2 = SERVFAIL
		binary.BigEndian.PutUint16(msg[6:8], 0)
		return msg
	}

	// THE ANSWER'S NAME IS A POINTER TO THE QUESTION'S (0xc00c), which is what every resolver emits and
	// what keeps the message small.
	rr := make([]byte, 16)
	copy(rr[0:2], []byte{0xc0, 0x0c})
	binary.BigEndian.PutUint16(rr[2:4], 1)                // TYPE A
	binary.BigEndian.PutUint16(rr[4:6], 1)                // CLASS IN
	binary.BigEndian.PutUint32(rr[6:10], uint32(fakeTTL)) // TTL
	binary.BigEndian.PutUint16(rr[10:12], 4)              // RDLENGTH
	copy(rr[12:16], ip.To4())
	return append(msg, rr...)
}

// forwardForReal sends an A query for a name to the upstreams and returns the reply.
//
// IT IS THE SAME FORWARDER THE RESOLVER USES, given a query it builds itself - so a `direct` rule
// resolves through exactly the path a real answer would have taken.
func forwardForReal(name string) []byte {
	q := make([]byte, 12)
	binary.BigEndian.PutUint16(q[0:2], uint16(time.Now().UnixNano())) // any id; the reply is matched by socket
	binary.BigEndian.PutUint16(q[4:6], 1)                             // QDCOUNT
	for _, label := range splitName(name) {
		q = append(q, byte(len(label)))
		q = append(q, label...)
	}
	q = append(q, 0, 0, 1, 0, 1) // end of name, QTYPE A, QCLASS IN

	s := &DNSServer{upstreams: resolverUpstreamsFn()}
	return s.forward(q)
}

// resolverUpstreamsFn is the upstream list for a direct resolution. A var so a test can point it at a
// fake resolver, and so the fake path and the real path cannot disagree about where "the upstream" is.
var resolverUpstreamsFn = ResolverUpstreams

// splitName breaks a dotted name into its labels, tolerating a trailing dot.
func splitName(name string) []string {
	name = trimDot(name)
	if name == "" {
		return nil
	}
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

func trimDot(s string) string {
	if len(s) > 0 && s[len(s)-1] == '.' {
		return s[:len(s)-1]
	}
	return s
}
