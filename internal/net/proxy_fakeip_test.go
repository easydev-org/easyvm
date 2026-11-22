package net

// THE PROXY MUST ASK FOR THE NAME, NOT THE ADDRESS THIS DAEMON INVENTED.
//
// This is the failure fake-ip would have if `tunnel` were left alone: the destination is 198.18.x.x,
// which exists nowhere, so `CONNECT 198.18.7.3:443` is a request no proxy can satisfy and the guest's
// connection dies at the proxy. The test drives the REAL `tunnel` function against a fake proxy that
// records what it was asked for.

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"
)

func TestTheProxyIsAskedForTheNameOfAFakeAddress(t *testing.T) {
	resetFakeIP(t)

	// The address the guest would be using, out of the pool.
	fake := allocateFakeIP("www.youtube.com")
	if fake == nil {
		t.Fatal("no address allocated")
	}
	dst := net.JoinHostPort(fake.String(), "443")

	// A fake proxy that records the CONNECT line.
	up, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()

	seen := make(chan string, 1)
	go func() {
		conn, err := up.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		_, _ = conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
		seen <- strings.TrimSpace(line)
	}()

	p := &TransparentProxy{Upstream: up.Addr().String()}
	guestSide, tunnelSide := net.Pipe()
	go func() {
		// Give the tunnel time to write its CONNECT line, then close so the test does not hang.
		time.Sleep(80 * time.Millisecond)
		_ = guestSide.Close()
	}()
	p.tunnel(tunnelSide, dst)

	select {
	case line := <-seen:
		if !strings.HasPrefix(line, "CONNECT www.youtube.com:443 ") {
			t.Fatalf("the proxy was asked for %q; a fake address exists nowhere and must be "+
				"replaced by the name the guest asked for", line)
		}
		// AND THE ADDRESS MUST NOT LEAK INTO THE REQUEST, because a proxy that sees it cannot resolve
		// it and the guest's connection fails there rather than here.
		if strings.Contains(line, fake.String()) {
			t.Fatalf("the CONNECT line still names the fake address: %q", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the fake proxy never saw a request")
	}
}

// A REAL ADDRESS IS UNCHANGED, so nothing about the existing path moves when fake-ip is on but the
// destination is not one this daemon invented.
func TestARealDestinationStillConnectsToItsAddress(t *testing.T) {
	resetFakeIP(t)
	up, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()

	seen := make(chan string, 1)
	go func() {
		conn, err := up.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		_, _ = conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
		seen <- strings.TrimSpace(line)
	}()

	p := &TransparentProxy{Upstream: up.Addr().String()}
	guestSide, tunnelSide := net.Pipe()
	go func() {
		time.Sleep(80 * time.Millisecond)
		_ = guestSide.Close()
	}()
	p.tunnel(tunnelSide, "203.0.113.9:443")

	select {
	case line := <-seen:
		if !strings.HasPrefix(line, "CONNECT 203.0.113.9:443 ") {
			t.Fatalf("a real destination was rewritten to %q", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the fake proxy never saw a request")
	}
}
