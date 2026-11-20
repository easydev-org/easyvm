// UDP RELAY: the half of the guest's egress that a CONNECT tunnel cannot carry.
//
// THE PROBLEM THIS SOLVES, AND WHY THE OBVIOUS ANSWERS WERE ALL WRONG:
//
//	TPROXY across pods    impossible - `--on-port` hands a packet to a socket in the SAME network
//	                      namespace, and preserving the original destination is what makes it useful
//	TPROXY in this pod    impossible HERE - measured: the rules match and the counters climb, but the
//	                      packet never reaches the socket because delivering a non-local address needs
//	                      `net.ipv4.ip_nonlocal_bind`, and a container cannot set it:
//	                        sysctl: permission denied on key "net.ipv4.ip_nonlocal_bind"
//	a TUN sidecar          works, but needs a second container in the pod for one protocol
//	SOCKS5 by hand         works, but only for programs told to use it
//
// WHAT DOES WORK IS A TUN DEVICE IN THIS POD, AND IT IS SMALLER THAN IT SOUNDS.
//
// A tun device hands every packet to a file descriptor, so the code below reads an IPv4 packet, picks
// out the UDP header, and writes a SOCKS5 UDP datagram to the cluster's proxy. The reply comes back
// with the source the guest expects, and is written into the tun as if it had always been there. No
// gVisor, no protocol stack, no `ip_nonlocal_bind`: an IPv4 header is twenty bytes and a UDP header
// is eight, and this daemon only ever needs to rewrite them.
//
// MEASURED end to end against a running guest: `curl --http3-only https://cloudflare-quic.com`
// answered `HTTP/3 200`, through this pod's tun, the cluster's SOCKS5 port and a node that forwards
// UDP. The same path carried DNS to 9.9.9.9.
//
// WHY A SEPARATE DEVICE AND A SEPARATE ROUTE TABLE. The tun takes only what policy routing sends it:
// `ip rule from <GuestIP> ipproto udp lookup <table>` with a default route into the tun. The pod's own
// traffic, and the guest's TCP, keep the routes they had - so the existing CONNECT proxy is untouched
// and this daemon does not proxy itself into a loop.
package net

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	// tunName cannot collide with a device a person would create by hand.
	tunRelayName = "easyvm-udp"
	tunRelayAddr = "10.99.0.1"
	tunRelayCIDR = "10.99.0.1/24"
	// tableID is a routing table of our own, so nothing else in this namespace is disturbed.
	relayTable = 511
	// NO UDP SESSION LIVES LONGER THAN THIS WITHOUT TRAFFIC. A guest that opens QUIC to a host and
	// then stops leaves a SOCKS5 control connection open for as long as the process runs, and there
	// is no close frame in UDP to tell us.
	relaySessionIdle = 2 * time.Minute
	relaySweep       = 30 * time.Second
)

// udpRelayUpstreamEnv names the SOCKS5 address the relay forwards to.
//
// IT IS SEPARATE FROM `EASYVM_TRANSPARENT_PROXY` ON PURPOSE. That variable is an HTTP proxy address
// and its port speaks CONNECT; a SOCKS5 UDP relay is usually a DIFFERENT port on the same host
// (mihomo: 7890 HTTP, 7891 SOCKS5). Deriving one from the other by adding one would be a guess that
// happens to be right for one implementation.
const udpRelayUpstreamEnv = "EASYVM_UDP_PROXY"

// StartUDPRelay brings up the tun device and begins forwarding.
//
// IT RETURNS NIL WITHOUT AN ERROR WHEN NO UPSTREAM IS CONFIGURED, which is the CI case: a runner that
// connects directly has nothing to tunnel through, and adding a tun device there would break its
// egress rather than help it.
func StartUDPRelay(guestIP string) (*UDPRelay, error) {
	upstream, ok := UDPRelayConfigured()
	if !ok {
		return nil, nil
	}

	tun, err := openTun(tunRelayName)
	if err != nil {
		return nil, fmt.Errorf("the udp relay's tun device: %w", err)
	}

	// THE DEVICE IS ADDRESSED AND RAISED BEFORE ANY ROUTE POINTS AT IT. A route into a DOWN device is
	// accepted and then blackholes everything it captures - MEASURED as `default dev tunproto
	// linkdown` on the way to a relay that never saw a packet.
	for _, cmd := range [][]string{
		{"addr", "add", tunRelayCIDR, "dev", tunRelayName},
		{"link", "set", tunRelayName, "up"},
	} {
		if out, err := run("ip", cmd...); err != nil {
			tun.Close()
			_ = deleteTun(tunRelayName)
			return nil, fmt.Errorf("`ip %v`: %v (%s)", cmd, err, firstLine(out))
		}
	}

	r := &UDPRelay{
		tun:      tun,
		upstream: upstream,
		sessions: map[relayKey]*relaySession{},
		done:     make(chan struct{}),
	}

	// POLICY ROUTING, AND ONLY FOR THIS GUEST'S UDP. `ipproto udp` is what keeps TCP out of the tun -
	// without it every connection the guest makes would arrive here instead of at the CONNECT proxy,
	// which carries them today.
	spec := guestIP + "/32"
	for _, step := range [][]string{
		{"route", "replace", "default", "dev", tunRelayName, "table", strconv.Itoa(relayTable)},
		{"rule", "add", "from", spec, "ipproto", "udp", "lookup", strconv.Itoa(relayTable)},
	} {
		if out, err := run("ip", step...); err != nil {
			// A RULE THAT ALREADY EXISTS IS NOT A FAILURE: `Up` can be called twice on a daemon that
			// was restarted without its network being torn down, and EEXIST here is the same state.
			if !containsWord(out, "File exists") {
				r.Close()
				return nil, fmt.Errorf("`ip %v`: %v (%s)", step, err, firstLine(out))
			}
		}
	}

	go r.readTun()
	go r.sweep()
	return r, nil
}

// UDPRelayConfigured reports the SOCKS5 address this build was told to use.
func UDPRelayConfigured() (string, bool) {
	v := osGetenv(udpRelayUpstreamEnv)
	if v == "" {
		return "", false
	}
	if _, _, err := net.SplitHostPort(v); err != nil {
		return "", false
	}
	return v, true
}

// udpRelay is the process-wide instance, so a shutdown that is not a full Down() can stop it - the
// same arrangement StopTransparentProxy has with `proxy`.
var udpRelay *UDPRelay

// StopUDPRelay stops it and removes its device and route.
func StopUDPRelay() {
	if udpRelay != nil {
		udpRelay.Close()
		udpRelay = nil
	}
}

// UDPRelay is the running tun device plus its session table.
type UDPRelay struct {
	tun      *os.File
	upstream string

	mu       sync.Mutex
	sessions map[relayKey]*relaySession

	done chan struct{}
	once sync.Once
}

// relayKey identifies a flow by its two endpoints, so a reply can be addressed back to the guest.
type relayKey struct {
	guestIP   [4]byte
	guestPort uint16
	dstIP     [4]byte
	dstPort   uint16
}

type relaySession struct {
	ctl   net.Conn
	relay *net.UDPAddr
	sock  *net.UDPConn
	last  time.Time
	mu    sync.Mutex
	// direct says this flow bypasses the proxy: `ctl` is nil and `sock` is CONNECTED to the
	// destination rather than to a relay, so a reply from any other peer is filtered by the kernel.
	direct bool
	// upstream is the SOCKS5 address this session was opened against, so a rule change can be
	// noticed when the same flow next uses it.
	upstream string
}

// ---------------------------------------------------------------- the tun side

func (r *UDPRelay) readTun() {
	buf := make([]byte, 65535)
	for {
		n, err := r.tun.Read(buf)
		if err != nil {
			select {
			case <-r.done:
				return
			default:
			}
			// A READ ERROR ON A TUN DEVICE IS NOT ALWAYS FATAL - EINTR arrives when the process
			// handles a signal - but a closed device must not spin.
			if errors.Is(err, os.ErrClosed) {
				return
			}
			continue
		}
		// ONLY IPv4 UDP IS OURS. Anything else (IPv6 ND chatter is constant on a tun) is dropped:
		// this relay is not a router and has no answer for it.
		if n < 28 || buf[0]>>4 != 4 || buf[9] != 17 {
			continue
		}
		ihl := int(buf[0]&0x0f) * 4
		if n < ihl+8 {
			continue
		}
		var k relayKey
		copy(k.guestIP[:], buf[12:16])
		copy(k.dstIP[:], buf[16:20])
		k.guestPort = binary.BigEndian.Uint16(buf[ihl : ihl+2])
		k.dstPort = binary.BigEndian.Uint16(buf[ihl+2 : ihl+4])
		payload := buf[ihl+8 : n]
		// THE IP CHECKSUM IS NOT VERIFIED. A malformed packet is one the far end will drop; the cost
		// of checking it is a per-packet pass over data that has already crossed a hypervisor.
		if err := r.forward(k, payload); err != nil {
			fmt.Fprintf(os.Stderr, "udp relay: %v\n", err)
		}
	}
}

func (r *UDPRelay) forward(k relayKey, payload []byte) error {
	// THE SAME RULES THE TCP PROXY USES, APPLIED TO A DATAGRAM.
	//
	// `block` drops it: there is no RST in UDP, so the guest's QUIC client simply times out, which is
	// what a filtered UDP port looks like everywhere. `direct` sends it from this pod with no proxy -
	// a plain UDP socket to the destination, and a reader that writes the reply back into the tun the
	// same way the proxied path does.
	dstIP := net.IP(k.dstIP[:])
	v := VerdictFor(dstIP, int(k.dstPort))
	switch v.Kind {
	case KindBlock:
		return nil
	case KindDirect:
		return r.forwardDirect(k, dstIP, payload)
	}

	// A NAMED UPSTREAM WITH NO SOCKS5 ADDRESS FALLS BACK TO THE ONE THAT HAS IT, which is the UDP
	// half of the same decision the TCP path makes: an upstream configured for CONNECT only still
	// works for TCP and leaves UDP on the default rather than dropping it.
	upstream := r.upstream
	if v.Upstream != "" {
		if addr, ok := socksFor(v.Upstream); ok {
			upstream = addr
		}
	}

	s, err := r.sessionFor(k, upstream)
	if err != nil {
		return err
	}
	framed := socksUDPRequest(k.dstIP, k.dstPort, payload)
	s.mu.Lock()
	_, err = s.sock.WriteToUDP(framed, s.relay)
	s.mu.Unlock()
	return err
}

// forwardDirect sends a datagram without the proxy, and arranges for the reply.
//
// A DIRECT FLOW NEEDS ITS OWN SOCKET, because a reply must be matched to the guest's endpoint. The
// session table holds it under the same key as a proxied flow - so `strictly first match wins` is the
// rules' business, not the table's, and a flow cannot be both.
func (r *UDPRelay) forwardDirect(k relayKey, dstIP net.IP, payload []byte) error {
	r.mu.Lock()
	existing, ok := r.sessions[k]
	if !ok {
		sock, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: dstIP, Port: int(k.dstPort)})
		if err != nil {
			r.mu.Unlock()
			return err
		}
		existing = &relaySession{sock: sock, last: time.Now(), direct: true}
		r.sessions[k] = existing
		go r.drainDirect(k, existing)
	}
	existing.last = time.Now()
	r.mu.Unlock()

	_, err := existing.sock.Write(payload)
	return err
}

// drainDirect reads replies on a direct socket and writes them into the tun.
func (r *UDPRelay) drainDirect(k relayKey, s *relaySession) {
	buf := make([]byte, 65535)
	for {
		n, err := s.sock.Read(buf)
		if err != nil {
			return
		}
		// THE SOURCE IS THE DESTINATION THE GUEST ASKED FOR. A connected UDP socket answers only for
		// that peer, so the address in the packet is always k.dst - stated rather than re-read,
		// because a reply from anywhere else would have been filtered by the kernel already.
		pkt := buildIPv4UDP(net.IP(k.dstIP[:]), k.dstPort, k.guestIP[:], k.guestPort, buf[:n])
		if _, err := r.tun.Write(pkt); err != nil {
			return
		}
	}
}

// sessionFor returns the session for a flow, creating one on first use.
func (r *UDPRelay) sessionFor(k relayKey, upstream string) (*relaySession, error) {
	r.mu.Lock()
	if s, ok := r.sessions[k]; ok {
		s.last = time.Now()
		r.mu.Unlock()
		return s, nil
	}
	r.mu.Unlock()

	s, err := newRelaySession(upstream)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	// A SECOND GOROUTINE MAY HAVE CREATED ONE WHILE THE DIAL WAS IN FLIGHT. Keeping the newer one
	// would leak the older session's socket and its control connection.
	if existing, ok := r.sessions[k]; ok {
		r.mu.Unlock()
		s.close()
		return existing, nil
	}
	r.sessions[k] = s
	r.mu.Unlock()

	go r.drainReplies(k, s)
	return s, nil
}

// drainReplies turns one SOCKS5 UDP reply into one packet written into the tun.
func (r *UDPRelay) drainReplies(k relayKey, s *relaySession) {
	buf := make([]byte, 65535)
	for {
		n, err := s.sock.Read(buf)
		if err != nil {
			return
		}
		srcIP, srcPort, payload, ok := socksUDPReply(buf[:n])
		if !ok {
			continue
		}
		// THE REPLY MUST LOOK LIKE IT CAME FROM WHERE THE GUEST SENT IT. That is the whole illusion:
		// the guest asked 104.18.27.14:443 and would discard a datagram from 10.99.0.1.
		pkt := buildIPv4UDP(srcIP, srcPort, k.guestIP[:], k.guestPort, payload)
		if _, err := r.tun.Write(pkt); err != nil {
			select {
			case <-r.done:
				return
			default:
			}
			return
		}
	}
}

// sweep closes sessions that have seen no traffic, because UDP has no close and a QUIC flow that is
// finished leaves nothing behind to tell us.
func (r *UDPRelay) sweep() {
	t := time.NewTicker(relaySweep)
	defer t.Stop()
	for {
		select {
		case <-r.done:
			return
		case <-t.C:
			cut := time.Now().Add(-relaySessionIdle)
			r.mu.Lock()
			for k, s := range r.sessions {
				if s.last.Before(cut) {
					delete(r.sessions, k)
					s.close()
				}
			}
			r.mu.Unlock()
		}
	}
}

// Close stops the relay and removes every trace of it from this namespace.
func (r *UDPRelay) Close() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		close(r.done)
		_ = r.tun.Close()

		r.mu.Lock()
		for k, s := range r.sessions {
			delete(r.sessions, k)
			s.close()
		}
		r.mu.Unlock()

		// THE ROUTE GOES FIRST: a rule that outlives its table sends the guest's UDP to a table with
		// no default route, which fails closed but silently.
		spec := GuestIP + "/32"
		_, _ = run("ip", "rule", "del", "from", spec, "ipproto", "udp",
			"lookup", strconv.Itoa(relayTable))
		_, _ = run("ip", "route", "flush", "table", strconv.Itoa(relayTable))
		_ = deleteTun(tunRelayName)
	})
}

// ---------------------------------------------------------------- SOCKS5

func newRelaySession(upstream string) (*relaySession, error) {
	ctl, err := net.DialTimeout("tcp", upstream, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("the udp relay's control connection to %s: %w", upstream, err)
	}
	if _, err := ctl.Write([]byte{5, 1, 0}); err != nil { // SOCKS5, one method, no auth
		ctl.Close()
		return nil, err
	}
	greeting := make([]byte, 2)
	if _, err := io.ReadFull(ctl, greeting); err != nil {
		ctl.Close()
		return nil, err
	}
	if greeting[0] != 5 || greeting[1] != 0 {
		ctl.Close()
		return nil, fmt.Errorf("the udp relay's proxy refused no-auth (%v)", greeting)
	}
	// UDP ASSOCIATE, with 0.0.0.0:0 meaning "the address this connection came from".
	if _, err := ctl.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		ctl.Close()
		return nil, err
	}
	hdr := make([]byte, 10)
	if _, err := io.ReadFull(ctl, hdr); err != nil {
		ctl.Close()
		return nil, err
	}
	if hdr[1] != 0 {
		ctl.Close()
		return nil, fmt.Errorf("the udp relay's proxy refused UDP ASSOCIATE (rep=%d)", hdr[1])
	}
	relayIP := net.IP(hdr[4:8])
	relayPort := binary.BigEndian.Uint16(hdr[8:10])

	// AN UNSPECIFIED RELAY ADDRESS MEANS THE PROXY ITSELF - a common answer, and the only one where
	// the port alone is meaningful.
	relayAddr := &net.UDPAddr{IP: relayIP, Port: int(relayPort)}
	if relayIP.IsUnspecified() {
		host, _, _ := net.SplitHostPort(upstream)
		if a, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, strconv.Itoa(int(relayPort)))); err == nil {
			relayAddr = a
		}
	}
	sock, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		ctl.Close()
		return nil, err
	}
	return &relaySession{ctl: ctl, relay: relayAddr, sock: sock, last: time.Now(), upstream: upstream}, nil
}

func (s *relaySession) close() {
	if s.sock != nil {
		_ = s.sock.Close()
	}
	// A DIRECT SESSION HAS NO CONTROL CONNECTION; closing a nil interface would panic.
	if s.ctl != nil {
		_ = s.ctl.Close()
	}
}

// socksUDPRequest frames a payload the way SOCKS5 wants it: RSV(2) FRAG(1) ATYP(1) ADDR PORT DATA.
func socksUDPRequest(dstIP [4]byte, dstPort uint16, payload []byte) []byte {
	out := make([]byte, 10+len(payload))
	out[3] = 1 // ATYP = IPv4
	copy(out[4:8], dstIP[:])
	binary.BigEndian.PutUint16(out[8:10], dstPort)
	copy(out[10:], payload)
	return out
}

// socksUDPReply strips that framing from a reply.
func socksUDPReply(b []byte) (net.IP, uint16, []byte, bool) {
	// FRAGMENTED REPLIES ARE NOT SUPPORTED and there is no reason to: the fragmentation field exists
	// for proxies that split a datagram, none of the ones measured here do, and a partial UDP
	// datagram is useless to the guest anyway.
	if len(b) < 10 || b[0] != 0 || b[1] != 0 || b[2] != 0 || b[3] != 1 {
		return nil, 0, nil, false
	}
	return net.IP(b[4:8]), binary.BigEndian.Uint16(b[8:10]), b[10:], true
}

// ---------------------------------------------------------------- IP packets

// buildIPv4UDP assembles a minimal IPv4+UDP packet with the given endpoints.
//
// THE UDP CHECKSUM IS LEFT ZERO, which IPv4 allows and means "not computed". Computing it would be a
// pass over every byte of a payload that has already crossed a hypervisor and a proxy.
func buildIPv4UDP(srcIP net.IP, srcPort uint16, dstIP net.IP, dstPort uint16, payload []byte) []byte {
	total := 20 + 8 + len(payload)
	p := make([]byte, total)
	p[0] = 0x45
	binary.BigEndian.PutUint16(p[2:4], uint16(total))
	p[8] = 64 // TTL
	p[9] = 17 // UDP
	copy(p[12:16], srcIP.To4())
	copy(p[16:20], dstIP.To4())
	binary.BigEndian.PutUint16(p[10:12], ipv4Checksum(p[:20]))
	binary.BigEndian.PutUint16(p[20:22], srcPort)
	binary.BigEndian.PutUint16(p[22:24], dstPort)
	binary.BigEndian.PutUint16(p[24:26], uint16(8+len(payload)))
	copy(p[28:], payload)
	return p
}

func ipv4Checksum(h []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(h); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(h[i : i+2]))
	}
	if len(h)%2 == 1 {
		sum += uint32(h[len(h)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

// ---------------------------------------------------------------- the device

const (
	iffTun    = 0x0001
	iffNoPI   = 0x1000
	tunSetIFF = 0x400454ca
)

type ifreq struct {
	name  [16]byte
	flags uint16
	_     [22]byte
}

// openTun creates (or attaches to) a tun device.
//
// `IFF_NO_PI` IS WHAT MAKES EVERY READ A BARE IP PACKET. Without it the kernel prefixes four bytes
// of flags and protocol, and a parser that assumed offset zero would read the flags word as the IP
// version - MEASURED as `ipversion=6 proto=128` on a device that was working correctly.
func openTun(name string) (*os.File, error) {
	fd, err := syscall.Open("/dev/net/tun", os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	var req ifreq
	copy(req.name[:], name)
	req.flags = iffTun | iffNoPI
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(tunSetIFF),
		uintptr(unsafe.Pointer(&req))); e != 0 {
		syscall.Close(fd)
		return nil, e
	}
	return os.NewFile(uintptr(fd), "/dev/net/tun"), nil
}

func deleteTun(name string) error {
	out, err := run("ip", "link", "del", name)
	if err != nil && !containsWord(out, "Cannot find device") {
		return fmt.Errorf("removing %s: %v (%s)", name, err, firstLine(out))
	}
	return nil
}

// containsWord is for the "already exists" / "no such device" cases, where the exit status is the
// same but the state is the one we wanted.
func containsWord(s, word string) bool { return strings.Contains(s, word) }
