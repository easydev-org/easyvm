// THE GUEST'S DNS, AND THE TABLE THAT MAKES NAME RULES POSSIBLE.
//
// WHY THIS EXISTS AT ALL, WHEN dnsmasq ALREADY ANSWERED DNS FOR THE GUEST:
//
// A RULE ABOUT A NAME CAN ONLY BE ENFORCED WHERE THE NAME IS STILL A NAME. The proxy sees a
// connection to 142.250.1.1:443 - an address, not a domain - so a rule for `youtube.com` or
// `.doubleclick.net` needs somebody who watched the DNS answer that produced that address and
// remembered it.
//
// mihomo does exactly this, and it is worth saying plainly because the mechanism is not obvious: it
// runs its OWN resolver in the same process as its proxy, so the answer to `youtube.com -> 142.250.1.1`
// is in the same memory as the later connection to 142.250.1.1. No ipset, no TLS sniffing, no packet
// inspection - just one process that saw both halves.
//
// THIS DAEMON HAD THE TWO HALVES IN DIFFERENT PROCESSES. dnsmasq answered the query and forgot it;
// the proxy received the connection and could not name it. That is the whole reason name rules could
// not exist until now, and it is why this file replaces dnsmasq's DNS rather than adding to it.
//
// DHCP IS STILL dnsmasq'S, AND THAT IS DELIBERATE. It works, it is tested by every boot, and
// replacing it would mean reimplementing lease negotiation, option encoding and renewal to gain
// nothing - the DHCP half does not touch names. Only `port=53` moves here.
//
// WHAT IS RECORDED, AND WHAT IS NOT: A and AAAA answers, with the query's own TTL. Nothing else is
// remembered - a CNAME that resolves elsewhere is followed by the upstream, and the address in the
// final answer is what a connection will carry, which is the only thing a rule can match.
package net

import (
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// dnstable maps an address back to the names it was resolved for.
//
// IT IS KEYED BY ADDRESS BECAUSE THAT IS THE LOOKUP A CONNECTION PERFORMS: the proxy has an IP and
// asks "what was this called". The reverse would be a map per name, which answers a question nothing
// asks at connection time.
type dnstable struct {
	mu sync.RWMutex
	// byIP holds every name an address has been seen for, newest first. A single address can serve
	// many names (a CDN), and a rule matching ANY of them should match the connection.
	byIP map[string]*dnsEntry
}

type dnsEntry struct {
	names []string
	// exp is when the LAST name in this entry expires. Entries are evicted whole, which is coarser
	// than per-name expiry and much simpler: a stale name on a shared address can only cause a rule
	// to match that would not have, and the TTL it was recorded with is the upstream's own.
	exp time.Time
}

var dnsNames = &dnstable{byIP: map[string]*dnsEntry{}}

// remember records that ip answered for name until the given expiry.
func (t *dnstable) remember(ip, name string, ttl time.Duration) {
	if ttl <= 0 {
		// A ZERO TTL MEANS "DO NOT CACHE", and honouring it is what keeps a rule that depends on a
		// load balancer's short answers from matching an address the name no longer has.
		return
	}
	if ttl > time.Hour {
		ttl = time.Hour
	}
	e := time.Now().Add(ttl)

	t.mu.Lock()
	defer t.mu.Unlock()
	entry := t.byIP[ip]
	if entry == nil {
		entry = &dnsEntry{}
		t.byIP[ip] = entry
	}
	for _, n := range entry.names {
		if n == name {
			if e.After(entry.exp) {
				entry.exp = e
			}
			return
		}
	}
	entry.names = append(entry.names, name)
	if e.After(entry.exp) {
		entry.exp = e
	}
}

// namesFor returns the names an address was resolved for, and whether any are still live.
func (t *dnstable) namesFor(ip string) []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	e := t.byIP[ip]
	if e == nil || time.Now().After(e.exp) {
		return nil
	}
	return append([]string(nil), e.names...)
}

// sweep drops entries whose every name has expired.
func (t *dnstable) sweep() {
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	for ip, e := range t.byIP {
		if now.After(e.exp) {
			delete(t.byIP, ip)
		}
	}
}

// NamesFor is the read side the proxy and the relay use.
//
// IT RETURNS AN EMPTY SLICE FOR AN UNKNOWN ADDRESS, and every caller must treat that as "no name
// rule can match" rather than as an error - a connection to a literal address is a legitimate thing
// a guest does, and it is exactly the case a name rule cannot cover.
func NamesFor(ip net.IP) []string {
	if ip == nil {
		return nil
	}
	return dnsNames.namesFor(ip.String())
}

// ---------------------------------------------------------------- the server

// dnsServer is the process-wide instance, so a shutdown can stop it.
var dnsServer *DNSServer

// StopDNS stops the resolver, for a shutdown that is not a full Down().
func StopDNS() {
	if dnsServer != nil {
		dnsServer.Close()
		dnsServer = nil
	}
}

// DNSServer answers the guest's queries and records what it learned.
type DNSServer struct {
	upstreams []string
	conn      *net.UDPConn
	done      chan struct{}
	once      sync.Once
}

// StartDNS binds the bridge's port 53.
//
// IT BINDS 172.30.0.1 AND NOT 0.0.0.0. The guest is told to ask the bridge (the DHCP lease says so),
// and a resolver reachable from the pod's own eth0 would be a second way in for no benefit.
//
// dnsmasq MUST HAVE GIVEN UP 53 FIRST, which is why `port=53` is no longer in its configuration -
// two sockets cannot bind the same address, and the failure would be this daemon refusing to start
// with an address-in-use error that says nothing about DNS being split.
func StartDNS(upstreams []string) (*DNSServer, error) {
	if len(upstreams) == 0 {
		return nil, fmt.Errorf("no upstream resolvers: the guest would have DNS that answers nothing")
	}
	addr := &net.UDPAddr{IP: net.ParseIP(BridgeIP), Port: 53}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("binding %s:53: %w", BridgeIP, err)
	}
	s := &DNSServer{upstreams: upstreams, conn: conn, done: make(chan struct{})}
	go s.serve()
	go s.sweep()
	return s, nil
}

// Close stops the listener.
func (s *DNSServer) Close() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		close(s.done)
		_ = s.conn.Close()
	})
}

func (s *DNSServer) sweep() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			dnsNames.sweep()
		}
	}
}

func (s *DNSServer) serve() {
	buf := make([]byte, 1500)
	for {
		n, from, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-s.done:
				return
			default:
				continue
			}
		}
		query := append([]byte(nil), buf[:n]...)
		go s.handle(query, from)
	}
}

// handle forwards one query and records what came back.
func (s *DNSServer) handle(query []byte, from *net.UDPAddr) {
	name, qtype, ok := parseQuestion(query)
	// A MALFORMED QUERY IS STILL FORWARDED. This is a resolver, not a validator: the upstream's own
	// answer (or refusal) is more informative than silence, and refusing here would make a broken
	// client look like a broken network.
	reply := s.forward(query)
	if reply == nil {
		return
	}
	_, _ = s.conn.WriteToUDP(reply, from)

	if !ok || (qtype != 1 && qtype != 28) {
		return
	}
	for _, rr := range parseAnswers(reply) {
		dnsNames.remember(rr.ip, name, time.Duration(rr.ttl)*time.Second)
	}
}

// forward asks each upstream in turn and returns the first answer.
func (s *DNSServer) forward(query []byte) []byte {
	for _, up := range s.upstreams {
		addr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(up, "53"))
		if err != nil {
			continue
		}
		c, err := net.DialUDP("udp", nil, addr)
		if err != nil {
			continue
		}
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := c.Write(query); err != nil {
			c.Close()
			continue
		}
		buf := make([]byte, 4096)
		n, err := c.Read(buf)
		c.Close()
		if err != nil {
			continue
		}
		return append([]byte(nil), buf[:n]...)
	}
	return nil
}

// ---------------------------------------------------------------- DNS wire format
//
// ONLY THE PARTS THIS NEEDS: the question's name and type, and the A/AAAA records in the answer. A
// full parser is a library; this is sixty lines because a resolver that records addresses does not
// need EDNS, DNSSEC or compression on the way back out.

// parseQuestion returns the queried name (lowercased, no trailing dot) and QTYPE.
func parseQuestion(msg []byte) (string, uint16, bool) {
	if len(msg) < 12 {
		return "", 0, false
	}
	qdcount := binary.BigEndian.Uint16(msg[4:6])
	if qdcount == 0 {
		return "", 0, false
	}
	off := 12
	var labels []string
	for {
		if off >= len(msg) {
			return "", 0, false
		}
		l := int(msg[off])
		off++
		if l == 0 {
			break
		}
		// COMPRESSION POINTERS IN A QUESTION ARE LEGAL BUT RARE; a name that points elsewhere would
		// need the full decoder, so it is treated as unparseable rather than mis-parsed.
		if l&0xc0 != 0 {
			return "", 0, false
		}
		if off+l > len(msg) {
			return "", 0, false
		}
		labels = append(labels, string(msg[off:off+l]))
		off += l
	}
	if off+4 > len(msg) {
		return "", 0, false
	}
	qtype := binary.BigEndian.Uint16(msg[off : off+2])
	return strings.ToLower(strings.Join(labels, ".")), qtype, true
}

type answerRR struct {
	ip  string
	ttl uint32
}

// parseAnswers walks the answer section and returns its A and AAAA records.
func parseAnswers(msg []byte) []answerRR {
	if len(msg) < 12 {
		return nil
	}
	qd := int(binary.BigEndian.Uint16(msg[4:6]))
	an := int(binary.BigEndian.Uint16(msg[6:8]))
	if an == 0 {
		return nil
	}
	off := 12
	// SKIP THE QUESTIONS, WITH COMPRESSION SUPPORT ON THE WAY, because an answer's name is almost
	// always a pointer into one of them.
	for i := 0; i < qd; i++ {
		off = skipName(msg, off)
		if off < 0 || off+4 > len(msg) {
			return nil
		}
		off += 4
	}
	var out []answerRR
	for i := 0; i < an; i++ {
		off = skipName(msg, off)
		if off < 0 || off+10 > len(msg) {
			return out
		}
		rtype := binary.BigEndian.Uint16(msg[off : off+2])
		ttl := binary.BigEndian.Uint32(msg[off+4 : off+8])
		rdlen := int(binary.BigEndian.Uint16(msg[off+8 : off+10]))
		off += 10
		if off+rdlen > len(msg) {
			return out
		}
		switch {
		case rtype == 1 && rdlen == 4:
			out = append(out, answerRR{ip: net.IP(msg[off : off+4]).String(), ttl: ttl})
		case rtype == 28 && rdlen == 16:
			out = append(out, answerRR{ip: net.IP(msg[off : off+16]).String(), ttl: ttl})
		}
		off += rdlen
	}
	return out
}

// skipName advances past a possibly-compressed name and returns the offset after it.
func skipName(msg []byte, off int) int {
	for {
		if off >= len(msg) {
			return -1
		}
		l := int(msg[off])
		if l == 0 {
			return off + 1
		}
		if l&0xc0 == 0xc0 {
			// A POINTER IS TWO BYTES AND ENDS THE NAME, whatever it points at.
			if off+2 > len(msg) {
				return -1
			}
			return off + 2
		}
		off += 1 + l
	}
}
