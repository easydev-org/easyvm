package guest

import (
	"net"
	"testing"
	"time"
)

// A GUEST THAT IS NOT READY YET MUST NOT BE REPORTED AS BROKEN ON THE FIRST TRY.
//
// MEASURED ON macOS 26, at the step that turns Remote Login on: the first probe answered
//
//	the port is open but no shell answers: ssh: handshake failed: EOF
//
// and the SAME guest answered `sw_vers -productVersion` seconds later. sshd accepts the TCP
// connection before it can serve a session, so the first handshake ends in EOF - which is "not
// ready", not "broken".
//
// THE OLD LOOP BROKE OUT ON THAT ERROR ("a broken channel is not a slow guest"), so `attempts: 3`
// was really `attempts: 1` for exactly the case the attempts existed for. These tests pin the new
// behaviour: a handshake that fails early is retried, and the error is still returned when the
// attempts run out.
func TestProbeRetriesAHandshakeThatFailsEarly(t *testing.T) {
	// A listener that ACCEPTS and then closes immediately - the shape of an sshd that is up but not
	// ready. Nothing here speaks SSH; Dial fails, which is the path this test is about.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)

	target := Target{Host: "127.0.0.1", Port: addr.Port, User: "vmuser", Password: "vmuser"}
	start := time.Now()
	err = Probe(target, 2, 2*time.Second)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a socket that closes without speaking ssh must not report success")
	}
	// TWO ATTEMPTS WITH A PAUSE BETWEEN THEM is the difference this test exists for. The old loop
	// returned after the first failure, so elapsed would be a handful of milliseconds.
	if elapsed < 1500*time.Millisecond {
		t.Errorf("Probe gave up after %s; it must make every attempt it is given, because the first "+
			"handshake against a starting sshd ends in EOF and the guest answers seconds later "+
			"(measured on macOS 26)", elapsed.Round(time.Millisecond))
	}
}

// AND THE ATTEMPT COUNT IS STILL A CEILING, so a guest that never answers does not hold the stage.
func TestProbeStillGivesUpWhenTheAttemptsRunOut(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)

	target := Target{Host: "127.0.0.1", Port: addr.Port, User: "vmuser", Password: "vmuser"}
	done := make(chan error, 1)
	go func() { done <- Probe(target, 3, 1*time.Second) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("three failed attempts must be reported, not swallowed")
		}
	case <-time.After(60 * time.Second):
		t.Fatal("Probe did not return within a minute with three one-second attempts; the ceiling " +
			"is not being enforced")
	}
}