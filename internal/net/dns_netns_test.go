package net

// THE REDIRECT, PROVEN ON A REAL KERNEL: a "guest" socket sends its query to 8.8.8.8, iptables DNATs
// it to this daemon's resolver, and the answer's address is recorded - which is what makes a domain
// rule match for a guest that ignored its DHCP lease.
//
// IT NEEDS NET_ADMIN AND A NETWORK NAMESPACE, so it skips without them. `unshare -Urn --keep-caps`
// is enough; see e2e notes. The point is that nothing here is mocked: a real UDP socket, real
// iptables, the same rule shape applyDNSRedirect writes.

import (
	"net"
	"os/exec"

	"github.com/easydev-org/easytun/names"
	"strings"
	"testing"
	"time"
)

func TestE2EDNSRedirectCatchesAQueryToAnotherServer(t *testing.T) {
	if _, err := exec.LookPath("iptables"); err != nil {
		t.Skip("iptables is not installed, so the redirect cannot be tested")
	}
	if out, err := exec.Command("iptables", "-t", "nat", "-L", "-n").CombinedOutput(); err != nil {
		t.Skipf("no NET_ADMIN for iptables: %v (%s)", err, firstLine(string(out)))
	}

	const bridge = "127.0.0.2" // loopback range, so no interface needs to be created
	const guest = "127.0.0.3"

	// A QUERY TO 8.8.8.8 NEEDS A ROUTE TO EXIST, or the socket fails with "network is unreachable"
	// before iptables ever sees a packet. A blackhole default is enough: the packet is DNAT'd in
	// OUTPUT before it would be routed to it.
	if out, err := exec.Command("ip", "route", "add", "default", "via", "127.0.0.1",
		"dev", "lo", "metric", "9999").CombinedOutput(); err != nil {
		t.Logf("adding a default route: %v (%s) - the query may not have one", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("ip", "route", "del", "default", "via", "127.0.0.1", "dev", "lo",
			"metric", "9999").Run()
	})

	upstream := fakeUpstream(t, net.IPv4(142, 250, 1, 1), 120)
	server, err := startDNSOn(bridge, []string{upstream})
	if err != nil {
		t.Fatalf("binding the resolver on %s: %v", bridge, err)
	}
	defer server.Close()

	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("iptables", args...).CombinedOutput(); err != nil {
			t.Fatalf("iptables %v: %v (%s)", args, err, firstLine(string(out)))
		}
	}
	run("-t", "nat", "-N", "EASYVM_DNS_E2E")
	t.Cleanup(func() { _ = exec.Command("iptables", "-t", "nat", "-F", "EASYVM_DNS_E2E").Run() })
	t.Cleanup(func() { _ = exec.Command("iptables", "-t", "nat", "-X", "EASYVM_DNS_E2E").Run() })
	t.Cleanup(func() {
		_ = exec.Command("iptables", "-t", "nat", "-D", "OUTPUT", "-p", "udp", "--dport", "53",
			"-j", "EASYVM_DNS_E2E").Run()
	})

	// THE RULE SHAPE applyDNSRedirect WRITES, entered from OUTPUT here because both sockets are local.
	run("-t", "nat", "-A", "EASYVM_DNS_E2E", "-p", "udp", "--dport", "53",
		"-j", "DNAT", "--to-destination", bridge+":53")
	run("-t", "nat", "-A", "OUTPUT", "-p", "udp", "--dport", "53", "-j", "EASYVM_DNS_E2E")

	// A GUEST THAT ASKS 8.8.8.8, which is the case the DHCP lease cannot cover.
	client, err := net.DialUDP("udp",
		&net.UDPAddr{IP: net.ParseIP(guest)},
		&net.UDPAddr{IP: net.IPv4(8, 8, 8, 8), Port: 53})
	if err != nil {
		t.Fatalf("the guest socket: %v", err)
	}
	defer client.Close()
	if _, err := client.Write(query("www.youtube.com")); err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1500)
	n, err := client.Read(buf)
	if err != nil {
		t.Fatalf("the redirect did not deliver the query to the resolver: %v", err)
	}
	if got := answersOf(buf[:n]); len(got) == 0 {
		t.Fatal("the resolver answered without an address")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(NamesFor(net.IPv4(142, 250, 1, 1))) > 0 {
			return // proven
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the query reached the resolver but the name was not recorded")
}

// startDNSOn is the library's Start with the address chosen by the caller, so a test can use loopback
// rather than the bridge.
func startDNSOn(addr string, upstreams []string) (*names.Server, error) {
	return names.Start(names.Config{
		Bind:      net.JoinHostPort(addr, itoaPort(dnsPort)),
		Upstreams: upstreams,
	})
}

// itoaPort renders the resolver's constant port.
func itoaPort(p int) string {
	if p == 0 {
		return "0"
	}
	var b []byte
	for p > 0 {
		b = append([]byte{byte('0' + p%10)}, b...)
		p /= 10
	}
	return string(b)
}

// silence the unused import if the file is built without the test running
var _ = strings.Contains
