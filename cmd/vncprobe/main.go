// A MINIMAL noVNC PROBE: connect to /websockify, read the RFB version string, complete the
// handshake, and count bytes that arrive. This is what "the picture is really being forwarded"
// has to mean - a 200 on vnc.html proves only that the file is served.
package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/coder/websocket"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: probe <ws-url>")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	c, _, err := websocket.Dial(ctx, os.Args[1], &websocket.DialOptions{
		Subprotocols: []string{"binary"},
	})
	if err != nil {
		fmt.Println("dial:", err)
		os.Exit(1)
	}
	defer c.CloseNow()
	c.SetReadLimit(64 << 20)

	// A BACKGROUND CONTEXT, NOT THE DIAL ONE: `websocket.NetConn` ties the conn's lifetime to the
	// context it is given, and the dial context above is deliberately short. The first version
	// reused it and the read died with "context canceled" right after the handshake.
	nc := websocket.NetConn(context.Background(), c, websocket.MessageBinary)

	// RFB 3.x: the server speaks first. Twelve bytes, "RFB 003.008\n".
	banner := make([]byte, 12)
	if _, err := io.ReadFull(nc, banner); err != nil {
		fmt.Println("banner:", err)
		os.Exit(1)
	}
	fmt.Printf("RFB banner: %q\n", banner)

	// Answer with the same version, then do the noVNC handshake: no auth (qemu with no password
	// offers security type 1), then ClientInit, then wait for ServerInit.
	if _, err := nc.Write(banner); err != nil {
		fmt.Println("version reply:", err)
		os.Exit(1)
	}
	nc.SetReadDeadline(time.Now().Add(10 * time.Second))
	n := make([]byte, 1)
	if _, err := io.ReadFull(nc, n); err != nil {
		fmt.Println("security count:", err)
		os.Exit(1)
	}
	fmt.Printf("security types offered: %d\n", n[0])
	types := make([]byte, n[0])
	if _, err := io.ReadFull(nc, types); err != nil {
		fmt.Println("security types:", err)
		os.Exit(1)
	}
	fmt.Printf("security types: %v\n", types)
	if _, err := nc.Write([]byte{1}); err != nil { // choose None
		fmt.Println("security choose:", err)
		os.Exit(1)
	}
	res := make([]byte, 4)
	if _, err := io.ReadFull(nc, res); err != nil {
		fmt.Println("security result:", err)
		os.Exit(1)
	}
	code := binary.BigEndian.Uint32(res)
	fmt.Printf("security result: %d\n", code)
	if code != 0 {
		os.Exit(1)
	}
	// ClientInit: shared = 1
	if _, err := nc.Write([]byte{1}); err != nil {
		fmt.Println("client init:", err)
		os.Exit(1)
	}
	// ServerInit: width(2) height(2) pixelformat(16) namelen(4) name
	hdr := make([]byte, 24)
	if _, err := io.ReadFull(nc, hdr); err != nil {
		fmt.Println("server init:", err)
		os.Exit(1)
	}
	w := binary.BigEndian.Uint16(hdr[0:2])
	h := binary.BigEndian.Uint16(hdr[2:4])
	nameLen := binary.BigEndian.Uint32(hdr[20:24])
	name := make([]byte, nameLen)
	_, _ = io.ReadFull(nc, name)
	fmt.Printf("framebuffer: %dx%d  name=%q\n", w, h, string(name))

	// Ask for a real copy of the framebuffer: SetEncodings with Raw, then FramebufferUpdateRequest.
	// raw = 0
	enc := make([]byte, 4+4)
	binary.BigEndian.PutUint32(enc[0:4], 1) // one encoding
	binary.BigEndian.PutUint32(enc[4:8], 0) // Raw
	if _, err := nc.Write(append([]byte{2, 0}, enc...)); err != nil {
		fmt.Println("set encodings:", err)
		os.Exit(1)
	}
	// FramebufferUpdateRequest: incremental=0, x=0,y=0,w,h
	req := make([]byte, 10)
	req[0] = 3
	binary.BigEndian.PutUint16(req[2:4], 0)
	binary.BigEndian.PutUint16(req[4:6], 0)
	binary.BigEndian.PutUint16(req[6:8], w)
	binary.BigEndian.PutUint16(req[8:10], h)
	if _, err := nc.Write(req); err != nil {
		fmt.Println("fb request:", err)
		os.Exit(1)
	}

	// THE REPLY IS READ FROM THE WEBSOCKET READER, NOT FROM A `NetConn` DEADLINE.
	//
	// `websocket.NetConn` IMPLEMENTS `SetReadDeadline` BY CANCELLING THE CONNECTION'S CONTEXT: there
	// is no per-read deadline in the WebSocket protocol, so the only thing a deadline can do is end
	// the connection. The first version of this probe set one and read the handshake perfectly, then
	// died at the first pixel with `failed to get reader: context canceled` - which looks like a
	// bridge failure and is the probe killing itself.
	//
	// SO THE RAW MESSAGE READER IS USED HERE, which takes a context per read.
	typ, data, err := c.Read(ctx)
	if err != nil {
		fmt.Println("read update:", err)
		os.Exit(1)
	}
	fmt.Printf("first message: type=%v bytes=%d", typ, len(data))
	if len(data) >= 4 {
		fmt.Printf("  rfb-type=%d nrects=%d", data[0], binary.BigEndian.Uint16(data[2:4]))
	}
	fmt.Println()

	// A FramebufferUpdate for a full 1920x1080 raw rectangle is ~8 MB, so a single message may be the
	// whole picture. Anything large is proof the pixels crossed the bridge.
	total := len(data)
	for total < 1<<20 {
		_, more, err := c.Read(ctx)
		if err != nil {
			break
		}
		total += len(more)
	}
	fmt.Printf("pixel payload received: %d bytes\n", total)
	if total < 1024 {
		fmt.Println("NO PIXELS - the bridge carried the handshake but not the picture")
		os.Exit(1)
	}
	fmt.Println("PICTURE FORWARDED")
}
