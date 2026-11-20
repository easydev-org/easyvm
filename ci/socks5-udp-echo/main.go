// A SOCKS5 UDP ECHO, FOR THE CI JOB THAT PROVES THE GUEST'S UDP EGRESS.
//
// THE RELAY IN `internal/net/udprelay.go` NEEDS A SOCKS5 PROXY, AND A GITHUB RUNNER HAS NONE. Pointing
// it at the real cluster proxy would make this job depend on a lab - which is the opposite of what a CI
// job is for. Pointing it at nothing would leave `EASYVM_UDP_PROXY` unset, which means the tun device
// is never created and the test asserts the absence of the thing it exists to test.
//
// SO THIS IS THE SMALLEST PROXY THAT MAKES THE PATH REAL. It speaks enough SOCKS5 to accept a UDP
// ASSOCIATE, and answers every datagram by sending the same payload back from the address it was
// addressed to. That is enough for the daemon's side to be exercised end to end:
//
//	the control connection and the greeting
//	UDP ASSOCIATE and its reply header
//	the request framing RSV/FRAG/ATYP/ADDR/PORT/DATA
//	the reply framing, and the rewrite back into the tun
//
// WHAT IT DOES NOT PROVE, AND THE WALKTHROUGH SAYS SO TOO: that a real proxy forwards to a real
// server. That was measured against the cluster's mihomo - `curl --http3-only` answered `HTTP/3 200`
// from inside a guest - and is quoted in the commit that added the relay.
//
// IT IS WRITTEN IN GO BECAUSE THE `tester` STAGE ALREADY HAS A TOOLCHAIN. A shell implementation would
// need a SOCKS5 server that does not exist on a stock runner image; this is sixty lines and no
// dependency beyond the standard library.
package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os"
)

func main() {
	addr := ":7891"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}
	ctl, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatal(err)
	}
	// THE RELAY SOCKET IS BOUND ONCE AND SHARED. A per-association socket would also work; sharing
	// one is what a real proxy does and keeps this file to the interesting part.
	relay, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("socks5-udp-echo: control %s, relay %s\n", addr, relay.LocalAddr())

	go func() {
		buf := make([]byte, 65535)
		for {
			n, from, err := relay.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if n < 10 {
				continue
			}
			// THE REQUEST HEADER IS STRIPPED AND THE SAME HEADER PUT BACK, because the client parses
			// the reply with the same code that built the request. The address echoed is the one the
			// datagram was sent to, which is what makes the daemon's rewrite-back observable.
			atyp := buf[3]
			var hdrLen int
			switch atyp {
			case 1:
				hdrLen = 10
			case 4:
				hdrLen = 22
			case 3:
				if n < 5 {
					continue
				}
				hdrLen = 5 + int(buf[4]) + 2
			default:
				continue
			}
			if n < hdrLen {
				continue
			}
			reply := make([]byte, n)
			copy(reply, buf[:n])
			reply[2] = 0 // FRAG
			_, _ = relay.WriteToUDP(reply, from)
		}
	}()

	for {
		c, err := ctl.Accept()
		if err != nil {
			return
		}
		go handle(c, relay)
	}
}

func handle(c net.Conn, relay *net.UDPConn) {
	defer c.Close()
	greet := make([]byte, 2)
	if _, err := io.ReadFull(c, greet); err != nil {
		return
	}
	nmethods := int(greet[1])
	methods := make([]byte, nmethods)
	if _, err := io.ReadFull(c, methods); err != nil {
		return
	}
	// NO AUTHENTICATION IS OFFERED, AND NONE IS REQUIRED. A runner has nothing to protect; requiring
	// credentials would make the test assert a configuration rather than the transport.
	if _, err := c.Write([]byte{5, 0}); err != nil {
		return
	}
	req := make([]byte, 10)
	if _, err := io.ReadFull(c, req); err != nil {
		return
	}
	if req[1] != 3 { // only UDP ASSOCIATE
		_, _ = c.Write([]byte{5, 7, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	port := relay.LocalAddr().(*net.UDPAddr).Port
	reply := []byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint16(reply[8:10], uint16(port))
	if _, err := c.Write(reply); err != nil {
		return
	}
	// THE CONTROL CONNECTION MUST STAY OPEN for the association to be valid; its close ends the UDP
	// session. Reading until EOF is the whole of the protocol from here.
	_, _ = io.Copy(io.Discard, c)
}
