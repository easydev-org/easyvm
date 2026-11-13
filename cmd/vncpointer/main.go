// A POINTER THROUGH VNC RATHER THAN QMP.
//
// MEASURED ON ghostbsd: every QMP `input-send-event` was accepted with no error and the pointer never
// moved, while a click in the browser console - which reaches the guest through qemu's VNC server -
// worked. The two paths converge inside qemu, so the difference is in how the events are delivered
// rather than in the device, and this is the one that is known to work.
package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"time"
)

func main() {
	if len(os.Args) < 4 {
		fmt.Println("usage: vncpointer <host:port> <x> <y>")
		os.Exit(2)
	}
	x, _ := strconv.Atoi(os.Args[2])
	y, _ := strconv.Atoi(os.Args[3])

	nc, err := net.DialTimeout("tcp", os.Args[1], 10*time.Second)
	if err != nil {
		fmt.Println("dial:", err)
		os.Exit(1)
	}
	defer nc.Close()
	nc.SetDeadline(time.Now().Add(30 * time.Second))

	banner := make([]byte, 12)
	if _, err := io.ReadFull(nc, banner); err != nil {
		fmt.Println("banner:", err)
		os.Exit(1)
	}
	fmt.Printf("banner %q\n", banner)
	if _, err := nc.Write([]byte("RFB 003.008\n")); err != nil {
		fmt.Println("version:", err)
		os.Exit(1)
	}
	n := make([]byte, 1)
	if _, err := io.ReadFull(nc, n); err != nil {
		fmt.Println("sectype count:", err)
		os.Exit(1)
	}
	types := make([]byte, n[0])
	if _, err := io.ReadFull(nc, types); err != nil {
		fmt.Println("sectypes:", err)
		os.Exit(1)
	}
	if _, err := nc.Write([]byte{1}); err != nil { // None
		os.Exit(1)
	}
	res := make([]byte, 4)
	if _, err := io.ReadFull(nc, res); err != nil {
		fmt.Println("result:", err)
		os.Exit(1)
	}
	if binary.BigEndian.Uint32(res) != 0 {
		fmt.Println("security refused")
		os.Exit(1)
	}
	if _, err := nc.Write([]byte{1}); err != nil { // shared
		os.Exit(1)
	}
	init := make([]byte, 24)
	if _, err := io.ReadFull(nc, init); err != nil {
		fmt.Println("server init:", err)
		os.Exit(1)
	}
	w := binary.BigEndian.Uint16(init[0:2])
	h := binary.BigEndian.Uint16(init[2:4])
	nameLen := binary.BigEndian.Uint32(init[20:24])
	name := make([]byte, nameLen)
	_, _ = io.ReadFull(nc, name)
	fmt.Printf("framebuffer %dx%d %q\n", w, h, string(name))

	// A REAL CLIENT ASKS FOR THE FRAMEBUFFER AND KEEPS READING IT. This is what noVNC does and what
	// the first version of this probe did not, and it is the whole difference being tested: qemu may
	// only route input from a client it considers active.
	enc := make([]byte, 8)
	binary.BigEndian.PutUint32(enc[0:4], 1) // one encoding
	binary.BigEndian.PutUint32(enc[4:8], 0) // Raw
	if _, err := nc.Write(append([]byte{2, 0}, enc...)); err != nil {
		fmt.Println("set encodings:", err)
		os.Exit(1)
	}
	req := make([]byte, 10)
	req[0] = 3 // FramebufferUpdateRequest
	binary.BigEndian.PutUint16(req[6:8], w)
	binary.BigEndian.PutUint16(req[8:10], h)
	if _, err := nc.Write(req); err != nil {
		fmt.Println("fb request:", err)
		os.Exit(1)
	}
	// DRAIN THE FIRST UPDATE so the server knows this client is consuming, then wait a moment.
	done := make(chan int, 1)
	go func() {
		buf := make([]byte, 256<<10)
		total := 0
		nc.SetReadDeadline(time.Now().Add(3 * time.Second))
		for {
			n, err := nc.Read(buf)
			total += n
			if err != nil {
				done <- total
				return
			}
		}
	}()
	time.Sleep(1500 * time.Millisecond)

	// PointerEvent: type 5, button-mask, x, y in PIXELS - no 0..32767 scaling, which is one reason
	// this path is simpler than the QMP one.
	ev := make([]byte, 6)
	ev[0] = 5
	binary.BigEndian.PutUint16(ev[2:4], uint16(x))
	binary.BigEndian.PutUint16(ev[4:6], uint16(y))
	if _, err := nc.Write(ev); err != nil {
		fmt.Println("pointer:", err)
		os.Exit(1)
	}
	time.Sleep(400 * time.Millisecond)
	// press: mask bit 0
	ev[1] = 1
	if _, err := nc.Write(ev); err != nil {
		os.Exit(1)
	}
	time.Sleep(200 * time.Millisecond)
	ev[1] = 0
	if _, err := nc.Write(ev); err != nil {
		os.Exit(1)
	}
	fmt.Printf("pointer to (%d,%d) and clicked\n", x, y)
}
