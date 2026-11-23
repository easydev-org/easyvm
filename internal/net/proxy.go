package net

import (
	"fmt"
	"net"
	"strconv"

	"github.com/easydev-org/easytun"
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
// this container is a machine we control. An iptables jump sends every outbound TCP connection to a
// listener on the bridge, and THE LISTENER IS github.com/easydev-org/easytun's - it speaks CONNECT,
// recovers the original destination and copies bytes, and none of that is about a virtual machine.
//
// WHAT IS LEFT IN THIS FILE IS THE PART THAT IS: the iptables chains (below), the port they name, and
// the verdict-to-Route translation that connects this daemon's rules to the library's listener.
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

// TransparentProxy is what this daemon keeps: the port its rules name, and the library listener behind
// it.
//
// THE LISTENER IS github.com/easydev-org/easytun's. What is here is the port (which an iptables rule
// names) and the shutdown, because those are the daemon's.
type TransparentProxy struct {
	// Port is the bridge port iptables redirects to. It is 0 when no proxy was configured.
	Port int
	// Upstream is the host:port this listener tunnels to, empty when no proxy is configured.
	Upstream string

	tcp *easytun.TCP
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

// StartTransparentProxy listens on the bridge and tunnels every connection to where the rules say.
//
// THE LISTENER BINDS THE BRIDGE, NOT THE LOOPBACK, AND THAT IS THE WHOLE FIX.
//
// MEASURED: with the rule written as `REDIRECT --to-ports <port>` the listener came up on 127.0.0.1 and
// accepted nothing from a guest. The guest's packets are not addressed to this machine - they are
// FORWARDED through it from 172.30.0.2 - and a `REDIRECT` rewrites them to 127.0.0.1:<port>, an address
// that belongs to the guest's own stack the moment the packet is no longer local. The counters said
// exactly that:
//
//	EASYVM_PROXY  REDIRECT   108 packets
//	FORWARD       ACCEPT       0 packets
//	guest         pkg        Could not connect to server
//
// So the rule is `DNAT --to-destination <bridge>:<port>` and the listener has to be reachable at that
// address. Binding the bridge makes the guest's connection an ordinary one to a host on its own /24,
// which is the only shape a forwarded packet can complete.
//
// THE PORT IS CHOSEN BY THE KERNEL (port 0) because it is written into an iptables rule, so a second
// proxy on the same host - a second daemon, a restart - must not collide with the first.
func StartTransparentProxy(upstream string) (*TransparentProxy, error) {
	tcp, err := easytun.StartTCP(easytun.TCPConfig{
		Bind:     BridgeIP + ":0",
		Upstream: upstream,
		Route:    tcpRoute,
		Logf:     func(format string, args ...any) { logf(format, args...) },
	})
	if err != nil {
		return nil, err
	}
	if tcp == nil {
		return nil, fmt.Errorf("the listener did not start")
	}
	return &TransparentProxy{Port: tcp.Port, Upstream: upstream, tcp: tcp}, nil
}

// tcpRoute turns an egress verdict into what the TCP listener should do.
//
// IT IS THE SAME TABLE THE UDP RELAY ASKS, which is the whole point of a policy engine: a rule for
// `.youtube.com` means the same thing to an HTTPS connection and to a QUIC one.
func tcpRoute(dst net.IP, port uint16) easytun.Route {
	// THE NAME, WHEN THIS DAEMON INVENTED THE ADDRESS. A fake address exists nowhere, so the name is
	// the only thing that reaches the real host - and it is also what a name rule matched.
	host, _ := NameForFakeIP(dst)

	v := VerdictFor(dst, int(port))
	switch v.Kind {
	case KindBlock:
		return easytun.Route{Kind: easytun.RouteDrop, Host: host}
	case KindDirect:
		return easytun.Route{Kind: easytun.RouteDirect, Host: host}
	}
	if v.Upstream != "" {
		if addr, ok := httpFor(v.Upstream); ok {
			return easytun.Route{Upstream: addr, Host: host}
		}
		// A NAMED UPSTREAM WITH NO HTTP ADDRESS CANNOT CARRY TCP. Refusing is honest; dialling the
		// default would be a rule that silently did something other than what it says.
		return easytun.Route{Kind: easytun.RouteDrop, Host: host}
	}
	return easytun.Route{Host: host}
}

// Close stops the listener.
func (p *TransparentProxy) Close() {
	if p == nil || p.tcp == nil {
		return
	}
	_ = p.tcp.Close()
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

// parseHost and portOf split a `host:port` without parsing the host as an address.
//
// `splitDst` requires an IP, which is right for a redirected connection - SO_ORIGINAL_DST is always
// an address. These are for the fake-ip case, where the "host" is the fake address and the name is
// looked up separately; a failure to split returns the input and an empty port, so a malformed
// destination degrades to the address it already was rather than to a panic.
func parseHost(dst string) net.IP {
	host, _, err := net.SplitHostPort(dst)
	if err != nil {
		return nil
	}
	return net.ParseIP(host)
}

func portOf(dst string) string {
	_, port, err := net.SplitHostPort(dst)
	if err != nil {
		return ""
	}
	return port
}
