package net

// HELPERS FOR THE INTEGRATION TESTS, WHICH ARE ABOUT HOW THIS DAEMON'S PIECES FIT - the resolver, the
// rule engine and the proxy - rather than about DNS itself. The resolver's own behaviour is tested in
// `github.com/easydev-org/easytun/names`; what these need is a fake upstream and a query to send it.

import (
	"encoding/binary"
	"net"
	"testing"
)

// fakeUpstream answers every A query with one fixed address, and AAAA with nothing.
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
			name, qtype, ok := questionOf(buf[:n])
			if !ok {
				continue
			}
			_, _ = conn.WriteToUDP(answerFor(buf[:n], name, qtype, ip, ttl), from)
		}
	}()
	return conn.LocalAddr().String()
}

// query is one DNS question in the wire form a client sends.
func query(name string) []byte {
	q := make([]byte, 12)
	binary.BigEndian.PutUint16(q[0:2], 0x1234)
	binary.BigEndian.PutUint16(q[4:6], 1)
	return appendQuestion(q, name, 1)
}

// queryAAAA is the same with QTYPE 28.
func queryAAAA(name string) []byte {
	q := make([]byte, 12)
	binary.BigEndian.PutUint16(q[0:2], 0x1235)
	binary.BigEndian.PutUint16(q[4:6], 1)
	return appendQuestion(q, name, 28)
}

func appendQuestion(msg []byte, name string, qtype uint16) []byte {
	cur := ""
	flush := func() {
		if cur != "" {
			msg = append(msg, byte(len(cur)))
			msg = append(msg, cur...)
			cur = ""
		}
	}
	for _, c := range name {
		if c == '.' {
			flush()
			continue
		}
		cur += string(c)
	}
	flush()
	msg = append(msg, 0)
	var t [2]byte
	binary.BigEndian.PutUint16(t[:], qtype)
	msg = append(msg, t[:]...)
	msg = append(msg, 0, 1) // QCLASS IN
	return msg
}

// questionOf reads the name and type out of a query.
func questionOf(msg []byte) (string, uint16, bool) {
	if len(msg) < 13 {
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
		if off+l > len(msg) {
			return "", 0, false
		}
		labels = append(labels, string(msg[off:off+l]))
		off += l
	}
	if off+4 > len(msg) {
		return "", 0, false
	}
	name := ""
	for i, l := range labels {
		if i > 0 {
			name += "."
		}
		name += l
	}
	return name, binary.BigEndian.Uint16(msg[off : off+2]), true
}

// answerFor builds an A (or empty AAAA) answer for a query.
func answerFor(q []byte, name string, qtype uint16, ip net.IP, ttl uint32) []byte {
	if len(q) < 12 {
		return nil
	}
	// find the end of the question
	off := 12
	for off < len(q) && q[off] != 0 {
		off += 1 + int(q[off])
	}
	if off+5 > len(q) {
		return nil
	}
	qEnd := off + 5

	msg := make([]byte, 12)
	copy(msg[0:2], q[0:2])
	msg[2], msg[3] = 0x81, 0x80
	binary.BigEndian.PutUint16(msg[4:6], 1)
	if qtype == 1 {
		binary.BigEndian.PutUint16(msg[6:8], 1)
	}
	msg = append(msg, q[12:qEnd]...)
	if qtype != 1 {
		return msg
	}
	rr := make([]byte, 16)
	copy(rr[0:2], []byte{0xc0, 0x0c})
	binary.BigEndian.PutUint16(rr[2:4], 1)
	binary.BigEndian.PutUint16(rr[4:6], 1)
	binary.BigEndian.PutUint32(rr[6:10], ttl)
	binary.BigEndian.PutUint16(rr[10:12], 4)
	copy(rr[12:16], ip.To4())
	return append(msg, rr...)
}

// answersOf returns the A/AAAA addresses in a reply.
func answersOf(msg []byte) []net.IP {
	if len(msg) < 12 {
		return nil
	}
	qd := int(binary.BigEndian.Uint16(msg[4:6]))
	an := int(binary.BigEndian.Uint16(msg[6:8]))
	off := 12
	for i := 0; i < qd; i++ {
		off = skipLabel(msg, off)
		if off < 0 || off+4 > len(msg) {
			return nil
		}
		off += 4
	}
	var out []net.IP
	for i := 0; i < an; i++ {
		off = skipLabel(msg, off)
		if off < 0 || off+10 > len(msg) {
			return out
		}
		rtype := binary.BigEndian.Uint16(msg[off : off+2])
		rdlen := int(binary.BigEndian.Uint16(msg[off+8 : off+10]))
		off += 10
		if off+rdlen > len(msg) {
			return out
		}
		switch {
		case rtype == 1 && rdlen == 4:
			out = append(out, net.IP(append([]byte(nil), msg[off:off+4]...)))
		case rtype == 28 && rdlen == 16:
			out = append(out, net.IP(append([]byte(nil), msg[off:off+16]...)))
		}
		off += rdlen
	}
	return out
}

func skipLabel(msg []byte, off int) int {
	for {
		if off >= len(msg) {
			return -1
		}
		l := int(msg[off])
		if l == 0 {
			return off + 1
		}
		if l&0xc0 == 0xc0 {
			if off+2 > len(msg) {
				return -1
			}
			return off + 2
		}
		off += 1 + l
	}
}
