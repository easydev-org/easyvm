package net

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

// THE TRANSPARENT PROXY: A GUEST THAT KNOWS NOTHING ABOUT PROXIES STILL GOES THROUGH ONE.
//
// THE PROBLEM THIS SOLVES IS A LAB ONE, AND IT IS MEASURED RATHER THAN IMAGINED. The route from this
// cluster to FreeBSD's package mirrors is 38-68 KB/s - the same rate whether the download runs in the
// pod or in the guest, so nothing about the guest is wrong - while the cluster's mihomo answers the
// same file in under a second. A desktop is four to six hundred megabytes, which is three hours on
// that route and two minutes through the proxy.
//
// SETTING `http_proxy` IN THE GUEST DOES NOT WORK, AND THAT IS THE POINT OF THIS FILE. A walkthrough
// says `pkg install`, and whether `pkg` honours an environment variable is `pkg`'s business: a
// download that uses a bundled libcurl, a Go program that reads only `HTTPS_PROXY`, a `fetch(8)`
// call, a git submodule - each answers differently, and a bake that works only for the tools that
// happened to read the variable is a bake that measures those tools rather than the system.
//
// SO THE REDIRECTION IS DONE WHERE THE GUEST CANNOT SEE IT: its packets reach this container, and
// this container is a machine we control. The rule is one iptables jump -
//
//	-A EASYVM_FWD -p tcp -j REDIRECT --to-ports <port>
//
// - and every outbound TCP connection the guest makes lands on the listener below instead of on the
// internet. This file is that listener.
//
// WHY REDIRECT NEEDS A PROGRAM AT ALL. `REDIRECT` rewrites the DESTINATION and delivers the socket;
// it does not speak a proxy protocol. mihomo's 7890 is an HTTP proxy, which expects a
//
//	CONNECT pkg.freebsd.org:443 HTTP/1.1
//
// line before the bytes. So this listener accepts the redirected connection, notes the original
// destination - which the kernel preserved in SO_ORIGINAL_DST - says that line to mihomo, and then
// copies bytes in both directions. The guest sees an ordinary TCP connection to port 443 and never
// learns that anything happened.
//
// WHAT IS DELIBERATELY NOT PROXIED HERE:
//
//	the guest's traffic to this container (172.30.0.0/24)   a NAT rule that caught it would break
//	                                                        the daemon's own ssh and screen calls
//	DNS on port 53                                          a redirected TCP :53 would be a DNS query
//	                                                        this tunnel cannot answer; the DHCP lease
//	                                                        already gives the guest a resolver
//
// UDP IS NOT ABSENT, IT IS SOMEWHERE ELSE: `udprelay.go` carries it through a tun device and a SOCKS5
// relay, because CONNECT cannot. The two are kept apart on purpose - this chain matches `-p tcp`
// only, so a guest's datagrams never arrive at a listener that would read them as garbage.
//
// AND CI HAS NONE OF THIS. `EASYVM_TRANSPARENT_PROXY` is unset on a runner, so no listener starts and
// no iptables rule is added: the guest uses the internet directly, exactly as it does on a desk.

// proxyEnv names the upstream HTTP proxy the guest's traffic is tunnelled through.
//
// IT IS AN ADDRESS, NOT A SWITCH: `172.17.0.121:7890`. An empty value means "no transparent proxy",
// which is what CI gets, and the code below adds no rules in that case rather than adding rules that
// point at nothing.
const proxyEnv = "EASYVM_TRANSPARENT_PROXY"

// TransparentProxy is a running listener plus the rules that feed it.
type TransparentProxy struct {
	// Port is the loopback port iptables redirects to. It is 0 when no proxy was configured.
	Port int
	// Upstream is the host:port this listener tunnels to, empty when no proxy is configured.
	Upstream string

	listener net.Listener
	done     chan struct{}
	once     sync.Once
}

// ProxyConfigured reports the upstream this build was told to use, and whether there is one.
//
// THE PARSE IS SEPARATE FROM Start() SO THE CALLER CAN DECIDE ABOUT RULES BEFORE ANYTHING LISTENS:
// the iptables jump has to name the port, and a port that is chosen at random has to come back from
// Start() first. This reads the environment only.
func ProxyConfigured() (string, bool) {
	v := osGetenv(proxyEnv)
	if v == "" {
		return "", false
	}
	return v, true
}

// StartTransparentProxy listens on a loopback port and tunnels every connection through upstream.
//
// `127.0.0.1:0` RATHER THAN A FIXED PORT: the port is written into an iptables rule, so a second
// proxy on the same host (a second daemon, a restart) must not collide with the first. The kernel
// picks a free one and Port reports it.
func StartTransparentProxy(upstream string) (*TransparentProxy, error) {
	host, port, err := net.SplitHostPort(upstream)
	if err != nil {
		return nil, fmt.Errorf("EASYVM_TRANSPARENT_PROXY=%q is not host:port: %w", upstream, err)
	}
	if _, err := strconv.Atoi(port); err != nil {
		return nil, fmt.Errorf("EASYVM_TRANSPARENT_PROXY=%q has no numeric port: %w", upstream, err)
	}
	// THE UPSTREAM IS RESOLVED ONCE, AT START, so a daemon whose DNS is briefly broken still fails
	// here - at startup, with a sentence - rather than on the guest's first request.
	if _, err := net.LookupHost(host); err != nil {
		return nil, fmt.Errorf("EASYVM_TRANSPARENT_PROXY=%q does not resolve: %w", upstream, err)
	}

	// THE LISTENER BINDS THE BRIDGE, NOT THE LOOPBACK, AND THAT IS THE WHOLE FIX.
	//
	// MEASURED: with the rule written as `REDIRECT --to-ports <port>` the listener came up on
	// 127.0.0.1 and accepted nothing from a guest. The guest's packets are not addressed to this
	// machine - they are FORWARDED through it from 172.30.0.2 - and a `REDIRECT` rewrites them to
	// 127.0.0.1:<port>, an address that belongs to the guest's own stack the moment the packet is
	// no longer local. The counters said exactly that:
	//
	//	EASYVM_PROXY  REDIRECT   108 packets
	//	FORWARD       ACCEPT       0 packets
	//	guest         pkg        Could not connect to server
	//
	// So the rule is `DNAT --to-destination <bridge>:<port>` and the listener has to be reachable at
	// that address. Binding the bridge makes the guest's connection an ordinary one to a host on its
	// own /24, which is the only shape a forwarded packet can complete.
	l, err := net.Listen("tcp", BridgeIP+":0")
	if err != nil {
		return nil, fmt.Errorf("listening for the transparent proxy: %w", err)
	}
	p := &TransparentProxy{
		Port:     l.Addr().(*net.TCPAddr).Port,
		Upstream: upstream,
		listener: l,
		done:     make(chan struct{}),
	}
	go p.serve()
	return p, nil
}

// Close stops the listener and waits for the accept loop to finish.
func (p *TransparentProxy) Close() {
	if p == nil {
		return
	}
	p.once.Do(func() {
		close(p.done)
		_ = p.listener.Close()
	})
}

func (p *TransparentProxy) serve() {
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			select {
			case <-p.done:
				return
			default:
			}
			// A FAILED ACCEPT IS NOT FATAL: the listener stays up and the next connection is
			// handled. The alternative - returning - turns one transient error into a proxy that
			// silently stopped.
			time.Sleep(10 * time.Millisecond)
			continue
		}
		go p.handle(conn)
	}
}

// handle turns one redirected connection into one CONNECT request.
func (p *TransparentProxy) handle(conn net.Conn) {
	// THE GUEST THINKS IT OPENED A CONNECTION TO THE INTERNET; THE KERNEL REMEMBERS WHERE IT WAS
	// GOING. SO_ORIGINAL_DST is that memory, and without it the destination is this listener's own
	// address - which would make the proxy connect back to itself.
	dst, err := originalDestination(conn)
	if err != nil {
		_ = conn.Close()
		return
	}
	p.tunnel(conn, dst)
}

// tunnel is the part that can be tested without a redirected socket, and it is separate for exactly
// that reason: `handle` needs SO_ORIGINAL_DST, which only the kernel can supply, so a test that went
// through `handle` could assert nothing but "the connection closed".
//
// IT TAKES THE DESTINATION rather than reading it, which is the whole seam: production passes the
// kernel's answer, and a test passes whatever it wants to see on the wire.
func (p *TransparentProxy) tunnel(conn net.Conn, dst string) {
	defer conn.Close()

	// THE RULES ARE CONSULTED BEFORE THE PROXY IS, BECAUSE THEY DECIDE WHETHER IT IS USED AT ALL.
	//
	// `block` closes the connection here: the guest sees a refused connection, which is what a
	// firewall looks like from inside and is the honest answer. `direct` dials the destination from
	// THIS pod with no proxy in the path - the same code, one branch, and the copy loop below is
	// shared so neither path can drift from the other.
	// THE UPSTREAM IS THE RULE'S DECISION WHEN A RULE MATCHED, AND THE DEFAULT OTHERWISE.
	//
	// `defaultUpstream` IS READ ONCE AT STARTUP AND IS WHAT p.Upstream WAS: the address this daemon
	// was told about by `EASYVM_TRANSPARENT_PROXY`. A rule that names an upstream only changes this
	// one thing - which proxy is dialled - so the CONNECT framing, the refusal handling and the copy
	// loop below are all shared by every upstream, and a new one cannot behave differently.
	upstream := p.Upstream
	if ip, port, err := splitDst(dst); err == nil {
		v := VerdictFor(ip, port)
		switch v.Kind {
		case KindBlock:
			return
		case KindDirect:
			up, err := net.DialTimeout("tcp", dst, 15*time.Second)
			if err != nil {
				return
			}
			defer up.Close()
			_ = up.SetDeadline(time.Time{})
			copyBoth(conn, up, up, true)
			return
		}
		if v.Upstream != "" {
			// A NAMED UPSTREAM THAT HAS NO HTTP ADDRESS CANNOT CARRY TCP. Dialling the default in
			// that case would be a rule that silently did something other than what it says, so the
			// connection is refused instead - and the rules API validates the name at apply time, so
			// this is the case of a table that changed underneath a rule.
			if addr, ok := httpFor(v.Upstream); ok {
				upstream = addr
			} else {
				return
			}
		}
	}

	// `CONNECT host:port HTTP/1.1` IS THE WHOLE PROTOCOL. There is no TLS to negotiate here: the
	// guest's TLS runs end to end THROUGH this tunnel, which is why the proxy needs no certificate
	// and cannot read the traffic.
	up, err := net.DialTimeout("tcp", upstream, 15*time.Second)
	if err != nil {
		return
	}
	defer up.Close()
	_ = up.SetDeadline(time.Time{})

	// THE CONNECT GOES TO THE PROXY, AND THE 200 COMES BACK BEFORE ANY GUEST BYTE IS FORWARDED.
	// A tunnel that starts copying first would send the guest's TLS ClientHello into a socket the
	// proxy is still expecting a request line on.
	if _, err := fmt.Fprintf(up, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", dst, dst); err != nil {
		return
	}
	br := bufio.NewReader(up)
	resp, err := br.ReadString('\n')
	if err != nil {
		return
	}
	// `200` IS THE ONLY SUCCESS THE PROTOCOL HAS, and a proxy that refuses says 403 or 502 here.
	// Nothing is reported to the guest: it sees its connection close, which is what a refused
	// connection looks like from inside a program that never knew a proxy was involved.
	if len(resp) < 12 || resp[9:12] != "200" {
		return
	}
	// THE REST OF THE HEADERS ARE DRAINED, because they belong to the CONNECT response and not to
	// the tunnelled bytes behind it. A leftover `Proxy-Agent:` line would arrive as garbage at the
	// front of the guest's TLS stream.
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}

	// BOTH DIRECTIONS, AND EITHER ONE CLOSING ENDS THE PAIR. A tunnel is over when the guest is
	// done; waiting for the upstream as well would hold the connection open for the proxy's own
	// idle timeout.
	copyBoth(conn, up, br, false)
}

// copyBoth runs the two directions and returns when EITHER finishes, which is what makes a tunnel end
// when one side closes rather than waiting for the other's idle timeout.
//
// `from` IS SEPARATE FROM `to` BECAUSE THE PROXY PATH HAS A bufio.Reader: the CONNECT response was
// read through a buffered reader and any bytes after its headers are the start of the tunnel, so the
// upstream-to-guest direction has to drain THAT reader rather than the socket.
// copyBoth runs the two directions and returns when the GUEST side is done, with `drain` controlling
// whether the reply side is given time to finish.
//
// THE TWO PATHS GENUINELY NEED DIFFERENT ANSWERS, AND TREATING THEM THE SAME WAS A BUG.
//
// A CONNECT TUNNEL IS A BYTE PIPE: when one end closes, the other has nothing left to say, and waiting
// for it would hold a socket open on a peer that has gone. That is the original `<-done` and it is
// right for the proxy path.
//
// A DIRECT CONNECTION IS NOT. MEASURED: with `<-done`, `curl http://deb.debian.org` printed
// `Connected to deb.debian.org (199.232.162.132) port 80` and then `Operation timed out with 0 bytes
// received` - because the guest's direction finishes the moment its request line is written, `<-done`
// returned, and `tunnel`'s `defer conn.Close()` tore down a socket whose reply was still in flight.
//
// SO THE DIRECT PATH WAITS FOR THE REPLY, BOUNDED. The bound is what keeps a peer that half-closed and
// went silent from holding the tunnel open forever.
func copyBoth(guest net.Conn, up net.Conn, from io.Reader, drainReply bool) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(up, guest)
		// HALF-CLOSE, NOT CLOSE: the other direction may still have a response to deliver.
		if tc, ok := up.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(guest, from)
		if tc, ok := guest.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
		done <- struct{}{}
	}()
	<-done
	if !drainReply {
		return
	}
	// A SHORT GRACE, NOT A SECOND FULL COPY. The reply is usually already in the kernel buffer or
	// arrives within a round trip; this window is for that, not for streaming a large body.
	_ = up.SetReadDeadline(time.Now().Add(5 * time.Second))
	<-done
}

// splitDst turns "1.2.3.4:443" into its parts, for the rules lookup.
func splitDst(dst string) (net.IP, int, error) {
	host, port, err := net.SplitHostPort(dst)
	if err != nil {
		return nil, 0, err
	}
	p, err := strconv.Atoi(port)
	if err != nil {
		return nil, 0, err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil, 0, fmt.Errorf("%q is not an address", host)
	}
	return ip, p, nil
}

// originalDestination reads SO_ORIGINAL_DST from a redirected socket.
//
// THE OPTION IS LINUX-SPECIFIC AND ITS NUMBER DIFFERS BY PROTOCOL: `80` for IPv4 and `81` for IPv6,
// in the SOL_IP / SOL_IPV6 level. Both are tried, because a guest may reach either family and a
// proxy that worked only for v4 would fail on the first AAAA answer.
//
// THE PARSING IS BY HAND RATHER THAN THROUGH A HELPER LIBRARY: the payload is a `sockaddr_in` -
// family, port (network byte order), address, padding - and there is no protocol here worth a
// dependency for sixteen bytes.
func originalDestination(conn net.Conn) (string, error) {
	tc, ok := conn.(*net.TCPConn)
	if !ok {
		return "", fmt.Errorf("not a tcp connection")
	}
	rc, err := tc.SyscallConn()
	if err != nil {
		return "", err
	}

	var addr string
	var sockErr error
	err = rc.Control(func(fd uintptr) {
		addr, sockErr = originalDstFromFD(int(fd))
	})
	if err != nil {
		return "", err
	}
	if sockErr != nil {
		return "", sockErr
	}
	return addr, nil
}

// originalDstFromFD does the getsockopt and the byte order.
func originalDstFromFD(fd int) (string, error) {
	// SOL_IP = 0, SO_ORIGINAL_DST = 80. The buffer holds `struct sockaddr_in`, which is 16 bytes,
	// so 128 is generous and keeps a kernel that returns more from being truncated.
	buf := make([]byte, 128)
	n := uint32(len(buf))
	if err := getsockopt(fd, solIP, soOriginalDst, buf, &n); err != nil {
		// IPv6 IS TRIED SECOND, because a v4 socket answers ENOENT and a v6 one answers the same
		// for the v4 option - so the failure of the first is not the answer.
		if err6 := getsockopt(fd, solIPv6, soOriginalDst, buf, &n); err6 != nil {
			return "", fmt.Errorf("SO_ORIGINAL_DST: v4: %v; v6: %v", err, err6)
		}
	}
	if n < 8 {
		return "", fmt.Errorf("SO_ORIGINAL_DST returned %d bytes", n)
	}
	// THE PORT IS BIG-ENDIAN AND SITS AT OFFSET 2: family is two bytes at 0, then the port.
	port := binary.BigEndian.Uint16(buf[2:4])
	var host string
	// FAMILY 10 IS AF_INET6, WHOSE ADDRESS IS AT OFFSET 8 AND IS SIXTEEN BYTES.
	if binary.LittleEndian.Uint16(buf[0:2]) == 10 {
		host = net.IP(buf[8:24]).String()
	} else {
		host = net.IP(buf[4:8]).String()
	}
	return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
}

// --- the rules that feed the listener ----------------------------------------------------

// proxyChain is the chain the redirect lives in, separate from forwardChain so the two can be
// reasoned about and flushed independently.
const proxyChain = "EASYVM_PROXY"

// proxy is the listener this process started, kept so Down() can stop it.
var proxy *TransparentProxy

// applyTransparentProxy adds the REDIRECT that sends every outbound guest connection to the listener.
//
// IT DOES NOTHING WHEN NO PROXY IS CONFIGURED, AND THAT IS THE CI CASE. `EASYVM_TRANSPARENT_PROXY` is
// unset on a runner, so this returns after flushing the chain - which is what removes rules from a
// previous run in the same container - and no listener is started.
//
// THE GUEST'S OWN NETWORK IS EXCLUDED FROM THE JUMP, and so is this container's bridge address. A
// redirect that caught 172.30.0.2 -> 172.30.0.1:2222 would send the daemon's own ssh connection into
// the proxy, and the connection it needs in order to watch the machine would be tunnelled to the
// internet.
func applyTransparentProxy(spec string) error {
	upstream, ok := ProxyConfigured()
	if !ok {
		// NO PROXY: FLUSH AND LEAVE. Flushing matters on a restart in a container that had one.
		_, _ = run("iptables", "-t", "nat", "-F", proxyChain)
		_, _ = run("iptables", "-t", "nat", "-D", "PREROUTING", "-s", spec, "-j", proxyChain)
		return nil
	}

	if proxy != nil {
		proxy.Close()
		proxy = nil
	}
	p, err := StartTransparentProxy(upstream)
	if err != nil {
		return fmt.Errorf("the transparent proxy: %w", err)
	}
	proxy = p

	if out, err := run("iptables", "-t", "nat", "-N", proxyChain); err != nil && !alreadyExists(out) {
		return fmt.Errorf("creating the %s chain: %w", proxyChain, err)
	}
	_, _ = run("iptables", "-t", "nat", "-F", proxyChain)

	// THE JUMP HAS TO COME FIRST IN PREROUTING, OR THE DNAT RULES IN forwardChain WOULD TAKE THE
	// PACKETS THAT ARE MEANT FOR THE PROXY. `-I` inserts at the top rather than appending.
	if _, err := run("iptables", "-t", "nat", "-C", "PREROUTING", "-s", spec, "-j", proxyChain); err != nil {
		if out, err := run("iptables", "-t", "nat", "-I", "PREROUTING", "-s", spec,
			"-j", proxyChain); err != nil {
			return fmt.Errorf("entering %s from PREROUTING: %v (%s)", proxyChain, err, firstLine(out))
		}
	}

	upstreamHost, _, err := net.SplitHostPort(upstream)
	if err != nil {
		return fmt.Errorf("the upstream %q has no host: %w", upstream, err)
	}

	port := strconv.Itoa(p.Port)
	for _, rule := range [][]string{
		// THIS CONTAINER'S OWN TRAFFIC IS NOT REDIRECTED, and it is the same rule shape the forwards
		// use. The daemon's ssh client dials the bridge address; catching it would send a connection
		// to the guest through mihomo, and the machine would stop being reachable at the moment the
		// proxy became useful.
		{"-d", spec, "-j", "RETURN"},
		// THE GUEST'S LOOPBACK AND ITS OWN LINK ARE ITS OWN. A redirect that caught them would
		// break anything the guest does locally.
		{"-d", "127.0.0.0/8", "-j", "RETURN"},
		// THE PROXY'S OWN UPSTREAM MUST NOT BE REDIRECTED, OR THE TUNNEL LOOPS.
		//
		// THIS RULE IS THE FIX FOR A MEASURED DEADLOCK. The chain is entered from OUTPUT for
		// everything this container sends - that is what lets a `fetch:` run go through the proxy
		// too - and the listener's connection to mihomo is one of those sends. Without this line
		// that connection is REDIRECTed straight back to the listener, which dials mihomo again,
		// and the two ends wait for each other:
		//
		//	ss -ltn                127.0.0.1:34943 LISTEN     (the listener was alive)
		//	curl --max-time 20    0 B/s, exit 56              (and every download failed at once)
		//
		// A guest trying to install anything then sees `Updating repositories failed` from
		// bsdinstall, which reads as a network problem rather than as a two-rule mistake.
		//
		// THE ADDRESS IS THE UPSTREAM'S HOST, NOT A RANGE: `p.Upstream` is `host:port` already
		// resolved by StartTransparentProxy, so a proxy given by name becomes an address here and the
		// rule matches what the packet carries.
		{"-d", upstreamHost + "/32", "-j", "RETURN"},
		// DNS OVER TCP IS NOT PROXIED: mihomo's proxy port speaks HTTP CONNECT, and a raw DNS query
		// sent through it would be answered with a 400 that the resolver reads as a broken server.
		// The DHCP lease already hands the guest a resolver that works.
		{"-p", "tcp", "--dport", "53", "-j", "RETURN"},
		// EVERYTHING ELSE GOES TO THE LISTENER, ON THE BRIDGE ADDRESS.
		//
		// `DNAT --to-destination <bridge>:<port>`, NOT `REDIRECT --to-ports <port>`, AND THE
		// DIFFERENCE IS THE WHOLE BUG THAT KEPT THE GUEST OFFLINE.
		//
		// A `REDIRECT` rewrites the destination to 127.0.0.1:<port>. That works for a connection this
		// container made itself, because such a packet is already local - but a guest's packet is
		// FORWARDED, its source is 172.30.0.2, and 127.0.0.1 is not an address that packet can be
		// delivered to. MEASURED:
		//
		//	EASYVM_PROXY  REDIRECT   108 packets      the rule matched
		//	FORWARD       ACCEPT       0 packets      and nothing was ever forwarded
		//	guest         pkg        Could not connect to server
		//
		// `DNAT` to the bridge address keeps the packet a routed one: the guest dials a host on its
		// own /24, which is a connection it can complete, and the listener answers from that address.
		//
		// `SO_ORIGINAL_DST` IS STILL WHAT RECOVERS THE TARGET - after either rewrite the socket's
		// peer is this listener, and the address the guest asked for survives only in that option.
		{"-p", "tcp", "-j", "DNAT", "--to-destination", BridgeIP + ":" + port},
	} {
		args := append([]string{"-t", "nat", "-A", proxyChain}, rule...)
		if out, err := run("iptables", args...); err != nil {
			return fmt.Errorf("proxy rule %v: %v (%s)", rule, err, firstLine(out))
		}
	}

	// AND THE SAME QUESTION FOR THIS CONTAINER'S OWN OUTBOUND TRAFFIC.
	//
	// A guest is not the only thing that downloads here: `fetch:` runs in the pod, and the pod's own
	// egress is the same slow route. The OUTPUT chain is where a locally generated connection is
	// judged, and without a jump there it is the only traffic that bypasses the proxy.
	//
	// THE BRIDGE ADDRESS IS EXCLUDED IN THE JUMP ITSELF, not inside the chain: the daemon's ssh client
	// and its screen requests go to 172.30.0.1, and redirecting those would tunnel the machine's own
	// control channel to the internet. The chain's own `-d spec -j RETURN` would also catch it, but a
	// jump that never matches is cheaper to reason about than one that enters a chain to be told to
	// leave it - and the exclusion is what a reader checks first.
	//
	// THE LISTENER'S OWN PORT IS EXCLUDED TOO. The proxy connects from this container to mihomo, and
	// if that connection were itself redirected it would loop - mihomo's address is a cluster pod, so
	// it is outside the `-d spec` match, and this rule makes the intent explicit rather than relying
	// on that.
	if _, err := run("iptables", "-t", "nat", "-C", "OUTPUT", "-p", "tcp",
		"!", "-d", spec, "-j", proxyChain); err != nil {
		if out, err := run("iptables", "-t", "nat", "-A", "OUTPUT", "-p", "tcp",
			"!", "-d", spec, "-j", proxyChain); err != nil {
			return fmt.Errorf("entering %s from OUTPUT: %v (%s)", proxyChain, err, firstLine(out))
		}
	}
	return nil
}

// StopTransparentProxy stops the listener, for a shutdown that is not a full Down().
func StopTransparentProxy() {
	if proxy != nil {
		proxy.Close()
		proxy = nil
	}
}
