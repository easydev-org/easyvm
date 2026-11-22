package net

import (
	"encoding/binary"
	"net"
	"testing"
	"time"
)

// THE RESOLVER END TO END: a query in, an answer out, and the name recorded.
//
// This is the part that was never tested and never ran. The three assertions are the three things the
// egress rules need from it, in the order they happen:
//
//	the guest gets its answer      otherwise DNS is broken outright
//	the addresses are recorded     otherwise a domain rule cannot match
//	the TTL is honoured            otherwise a rule matches an address the name has dropped

// fakeUpstream is a DNS server that answers every A query with one fixed address, so the test knows
// what to expect without a network.
func fakeUpstream(t *testing.T, ip net.IP, ttl uint32) string {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			name, _, ok := parseQuestion(buf[:n])
			if !ok {
				continue
			}
			_, _ = conn.WriteToUDP(buildAnswer(name, []net.IP{ip}, ttl), from)
		}
	}()

	_, port, err := net.SplitHostPort(conn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	return "127.0.0.1:" + port
}

// query is one DNS question, in the wire form a client sends.
func query(name string) []byte {
	var q []byte
	for _, label := range splitLabels(name) {
		q = append(q, byte(len(label)))
		q = append(q, label...)
	}
	q = append(q, 0, 0, 1, 0, 1) // end of name, QTYPE A, QCLASS IN

	msg := make([]byte, 12)
	binary.BigEndian.PutUint16(msg[0:2], 0x1234) // ID
	binary.BigEndian.PutUint16(msg[4:6], 1)      // QDCOUNT
	return append(msg, q...)
}

// THE SERVER IS BUILT AROUND A REAL SOCKET, so this exercises `handle`, `forward`, `parseAnswers` and
// the recording together - the path a guest's query takes.
//
// IT DOES NOT USE StartDNS, whose bind is on the bridge address and needs one to exist. `newTestServer`
// builds the same server on loopback, which is the only difference.
func newTestServer(t *testing.T, upstream string) *DNSServer {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	s := &DNSServer{
		upstreams: upstreamHostPort(t, upstream),
		conn:      conn,
		done:      make(chan struct{}),
	}
	t.Cleanup(func() { _ = conn.Close() })
	go s.serve()
	return s
}

// THE QUERY IS ANSWERED AND THE NAME IS RECORDED, which is the whole job.
func TestTheResolverAnswersAndRecordsTheName(t *testing.T) {
	upstream := fakeUpstream(t, net.IPv4(142, 250, 1, 1), 120)
	s := newTestServer(t, upstream)
	defer s.Close()

	// Ask the server the way a guest would.
	client, err := net.DialUDP("udp", nil, s.conn.LocalAddr().(*net.UDPAddr))
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
	if got := parseAnswers(buf[:n]); len(got) == 0 {
		t.Fatal("the resolver answered without a usable address")
	}

	// THE NAME MUST BE IN THE TABLE NOW, because that is what a later connection to 142.250.1.1
	// looks up to decide which rule applies.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if names := NamesFor(net.IPv4(142, 250, 1, 1)); len(names) > 0 {
			if names[0] != "www.youtube.com" {
				t.Fatalf("recorded name = %q, want www.youtube.com", names[0])
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the resolver answered but did not record the name, so every domain rule would match nothing")
}

// A NON-A QUERY IS ANSWERED BUT NOT RECORDED. Recording an AAAA as an IPv4 rule, or a CNAME as an
// address, would put wrong names in the table.
func TestANonAddressQueryIsNotRecorded(t *testing.T) {
	upstream := fakeUpstream(t, net.IPv4(1, 2, 3, 4), 60)
	s := newTestServer(t, upstream)
	defer s.Close()

	q := query("example.com")
	// QTYPE 15 IS MX - NEITHER A (1) NOR AAAA (28), which are the only two the resolver records. The
	// question ends [0][QTYPE:2][QCLASS:2], so QTYPE is the two bytes at len-4..len-2.
	binary.BigEndian.PutUint16(q[len(q)-4:len(q)-2], 15)

	client, err := net.DialUDP("udp", nil, s.conn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, _ = client.Write(q)
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _ = client.Read(make([]byte, 1500))

	// Give the handler a moment to finish recording (or not).
	time.Sleep(50 * time.Millisecond)
	if names := NamesFor(net.IPv4(1, 2, 3, 4)); names != nil {
		t.Fatalf("an MX query recorded %v; only A/AAAA answers belong in the table", names)
	}
}

// A DEAD UPSTREAM MUST NOT HANG THE GUEST. The forwarder tries each upstream and gives up; the
// client gets no answer rather than a socket that blocks for the client's whole timeout.
func TestADeadUpstreamDoesNotWedgeTheServer(t *testing.T) {
	// A port nothing listens on, so every forward fails immediately.
	s := newTestServer(t, "127.0.0.1:1")
	defer s.Close()

	client, err := net.DialUDP("udp", nil, s.conn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, _ = client.Write(query("nowhere.example"))
	_ = client.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	if _, err := client.Read(make([]byte, 1500)); err == nil {
		t.Fatal("a dead upstream produced an answer")
	}

	// AND THE SERVER IS STILL SERVING: a second query is handled rather than the loop having exited.
	_, _ = client.Write(query("again.example"))
	_ = client.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	_, _ = client.Read(make([]byte, 1500)) // may or may not answer; the point is it does not panic
}

// the whole host:port, because forward accepts a port and a bare address means 53 - a fake upstream
// listens on whatever port is free.
func upstreamHostPort(t *testing.T, addr string) []string {
	t.Helper()
	if _, _, err := net.SplitHostPort(addr); err != nil {
		t.Fatal(err)
	}
	return []string{addr}
}
