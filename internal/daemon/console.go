package daemon

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// THIS REPLACES websockify, AND THE REASON IS THAT websockify ONLY DOES TWO THINGS.
//
// It serves a directory and it turns a WebSocket into a TCP stream. Both are in the standard
// library plus one small package, and the third thing websockify costs is a PROCESS: a child to
// start, to notice dying, to reap, and to explain in a log when it did not start. The Python
// implementation had all of that, guarded by a `shutil.which` and a `!` line that nothing read.
//
// THE TRANSLATION ITSELF IS ONE CALL. coder/websocket's NetConn turns an accepted WebSocket into a
// net.Conn, and qemu's VNC port is a net.Conn, so the bridge is two io.Copy calls. There is no
// framing to reimplement: WebSocket carries opaque binary frames, and VNC is opaque bytes.
//
// WHAT IS DELIBERATELY NOT REIMPLEMENTED: nothing about noVNC. The page is the distro's, served
// verbatim from /usr/share/novnc. A hand-written console would be a second thing to keep in step
// with the VNC protocol.

// novncRoot is where the distro package keeps the page. Debian and Ubuntu both put vnc.html,
// app/, core/ and vendor/ directly here.
const novncRoot = "/usr/share/novnc"

// wsPath is the path noVNC connects to by default.
//
// IT IS "websockify" AND NOT "websocket", which is a detail worth writing down: noVNC's own default
// is the string `websockify` (app/ui.js: `UI.initSetting('path', 'websockify')`), so a console that
// listened on /websocket would serve a perfect page whose connection 404s - and the page reports
// that as "Failed to connect to server", which reads as a broken VM.
const wsPath = "/websockify"

// bridgeWebSocket turns one accepted WebSocket into a byte stream to the guest's VNC port.
//
// THIS IS THE WHOLE OF WHAT websockify DID, and it needs no server of its own: the daemon's listener
// already serves the page, the assets and this socket, so there is no second port to own, no second
// lifecycle to keep in step, and no way for two copies of it to fight over 6080.
//
// THE ADDRESS IS PASSED IN RATHER THAN CAPTURED, because it belongs to the CURRENT machine - see
// `Daemon.vncPort`. An empty address is not a bug in the bridge: it is a daemon with no guest, and the
// page is told so in words a person can act on.
func bridgeWebSocket(w http.ResponseWriter, r *http.Request, vncAddr string) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// THE PAGE IS SERVED BY THIS SAME SERVER, so a browser sends Origin equal to this host. A
		// different origin is a different page and has no business here - but the check is disabled
		// because the daemon cannot know the name a Service will publish it under. The socket is the
		// only thing served and the guest's VNC port is on localhost; there is nothing to protect from
		// CSRF beyond the VNC session itself.
		InsecureSkipVerify: true,
	})
	if err != nil {
		// Accept has already written the failure.
		return
	}
	defer conn.CloseNow()

	if vncAddr == "" {
		_ = conn.Close(websocket.StatusTryAgainLater,
			"no machine is running; start one and reload")
		return
	}

	vnc, err := net.DialTimeout("tcp", vncAddr, 10*time.Second)
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError,
			"the machine's VNC port is not listening")
		return
	}
	defer vnc.Close()

	// NetConn IS THE TRANSLATION. WebSocket message framing is one side's business; VNC is a byte
	// stream; this hands over a net.Conn that does the conversion, so the copy below is the same code
	// that would bridge two TCP sockets.
	ws := websocket.NetConn(context.Background(), conn, websocket.MessageBinary)
	defer ws.Close()

	// BOTH DIRECTIONS, AND THE FIRST TO FAIL ENDS BOTH. A one-way copy would let the page draw a frame
	// and then never send a keystroke, which reads as a frozen guest.
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(ws, vnc); done <- struct{}{} }()
	go func() { _, _ = io.Copy(vnc, ws); done <- struct{}{} }()
	<-done
	// Closing the VNC side is what tells qemu the client went away; the WebSocket is closed by the
	// deferred CloseNow.
}

// portInUse reports whether another PROCESS already accepts TCP on port.
//
// IT IS ONLY ASKED AT STARTUP, to refuse a port somebody else owns. It is no longer asked on every
// boot, because the console is no longer rebound on every boot - which is what made
//
//	boot: port 6080 is already in use, so the noVNC page cannot be served
//
// a daemon reporting a conflict with itself.
func portInUse(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
