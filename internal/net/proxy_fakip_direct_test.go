package net

// THE `direct` RULE WITH A FAKE DESTINATION, WHICH IS THE ONE BRANCH THAT HAS TO DO TWO THINGS AT ONCE:
// resolve the name against the upstream, and connect to the real address with no proxy.
//
// Getting this wrong is not a failure, it is a loop: dialling the fake address connects back to this
// daemon, which redirects it again. So it is tested against a real listener rather than inspected.

import (
	"net"
	"testing"
	"time"
)

func TestADirectRuleResolvesAFakeAddressToTheRealOne(t *testing.T) {
	resetFakeIP(t)

	// A real server the direct connection should reach, standing in for the internet.
	real, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer real.Close()
	go func() {
		for {
			c, err := real.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte("REAL"))
			_ = c.Close()
		}
	}()
	_, realPort, _ := net.SplitHostPort(real.Addr().String())

	// The name resolves to that server through the upstream, and the guest is given a fake address for
	// it - so the connection the tunnel sees is to the fake one.
	prev := resolverUpstreamsFn
	resolverUpstreamsFn = func() []string {
		return []string{fakeUpstreamPort(t, net.ParseIP("127.0.0.1"), 60)}
	}
	t.Cleanup(func() { resolverUpstreamsFn = prev })

	oldU := UpstreamsCopy()
	t.Cleanup(func() { _ = SetUpstreams(oldU) })
	if err := SetUpstreams([]Upstream{{Name: "default", HTTP: "127.0.0.1:1"}}); err != nil {
		t.Fatal(err)
	}
	oldP, _, _ := Egress()
	t.Cleanup(func() { _ = SetEgress(oldP) })
	if err := SetEgress(Policy{
		Default: Verdict{Kind: KindDirect},
		Rules:   []Rule{{Match: ".direct.example", Verdict: Verdict{Kind: KindDirect}}},
	}); err != nil {
		t.Fatal(err)
	}

	fake := allocateFakeIP("host.direct.example")
	if fake == nil {
		t.Fatal("no fake address")
	}
	dst := net.JoinHostPort(fake.String(), realPort)

	// THE GUEST IS ONE END OF THE PIPE AND `tunnel` GETS THE OTHER. So what the real server wrote is
	// read from the GUEST side, which is where a guest would see it.
	guestSide, tunnelSide := net.Pipe()

	done := make(chan string, 1)
	go func() {
		buf := make([]byte, 4)
		_ = guestSide.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, err := guestSide.Read(buf)
		if err != nil {
			done <- "READ ERROR: " + err.Error()
			return
		}
		done <- string(buf[:n])
	}()

	p := &TransparentProxy{}
	go p.tunnel(tunnelSide, dst)

	select {
	case got := <-done:
		if got != "REAL" {
			t.Fatalf("the direct connection delivered %q, want REAL - it did not resolve the fake "+
				"address, or it dialled the fake one and looped", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the direct connection never delivered anything")
	}
	_ = guestSide.Close()
}

// fakeUpstreamPort is fakeUpstream with the port included, for a caller that needs host:port.
func fakeUpstreamPort(t *testing.T, ip net.IP, ttl uint32) string {
	t.Helper()
	return fakeUpstream(t, ip, ttl)
}
