package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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

// consoleServer is the HTTP surface: the page, and the bridge to VNC.
type consoleServer struct {
	// addr is where it listens; 127.0.0.1 by default because the page is reached through a
	// published port and needs no other exposure.
	addr string
	// vncAddr is qemu's own VNC listener.
	vncAddr string
	srv     *http.Server
	// errc receives a listener failure that happened after Start returned.
	errc chan error
}

// startConsole serves the noVNC page and bridges its WebSocket to qemu.
//
// A MISSING PAGE IS NOT A FAILURE TO START THE MACHINE. If /usr/share/novnc is absent this says so
// and returns nil: the guest is the product, the page is how a person looks at it, and taking a VM
// down over a console script is the wrong trade. A PORT ALREADY IN USE IS DIFFERENT and is an error,
// because continuing would leave the reader believing a page exists.
func startConsole(host string, port, vncPort int, log func(string)) (*consoleServer, error) {
	if _, err := os.Stat(filepath.Join(novncRoot, "vnc.html")); err != nil {
		log(fmt.Sprintf("  noVNC: no page at %s/vnc.html; the guest is reachable on VNC port %d only",
			novncRoot, vncPort))
		return nil, nil
	}
	if portInUse(port) {
		return nil, fmt.Errorf("port %d is already in use, so the noVNC page cannot be served; "+
			"free it or choose another with --novnc-port", port)
	}

	c := &consoleServer{
		addr:    net.JoinHostPort(host, fmt.Sprint(port)),
		vncAddr: net.JoinHostPort("127.0.0.1", fmt.Sprint(vncPort)),
		errc:    make(chan error, 1),
	}

	mux := http.NewServeMux()
	// ORDER MATTERS IN A MUX ONLY FOR OVERLAPPING PATTERNS, and these do not overlap - but the
	// handler is registered first because it is the whole reason this server exists.
	mux.HandleFunc(wsPath, c.bridge)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		// A CONTAINER HEALTH CHECK THAT TOUCHES ONLY THE HTTP SERVER WOULD SAY HEALTHY FOR A DEAD
		// GUEST. This one answers "the console is up", and the daemon's own `health` command is
		// what asks about the machine.
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "console ok\n")
	})
	// THE PAGE ITSELF, AND EVERYTHING IT ASKS FOR: app/, core/, vendor/, images/.
	//
	// http.FileServer refuses a path that escapes its root, so serving the directory is safe. The
	// root redirect is spelled out because a browser pointed at :6080/ should land on the console
	// rather than on a directory listing.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/vnc.html", http.StatusFound)
			return
		}
		http.FileServer(http.Dir(novncRoot)).ServeHTTP(w, r)
	})

	c.srv = &http.Server{Addr: c.addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	ln, err := net.Listen("tcp", c.addr)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", c.addr, err)
	}
	go func() {
		if err := c.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			c.errc <- err
		}
	}()
	log(fmt.Sprintf("  noVNC: http://%s/vnc.html  (WebSocket %s -> %s, no websockify)",
		c.addr, wsPath, c.vncAddr))
	return c, nil
}

// bridge is the whole of what websockify did.
func (c *consoleServer) bridge(w http.ResponseWriter, r *http.Request) {
	// THE SUBPROTOCOL IS WHAT noVNC ASKS FOR. websocket.Accept echoes the client's first requested
	// subprotocol when none is given here, which is what noVNC expects; stating it would be a
	// promise this code cannot keep if the page ever changes.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// THE PAGE IS SERVED BY THIS SAME SERVER, so a browser sends Origin equal to this host. A
		// different origin is a different page and has no business here - but the check is disabled
		// because the port is published on a host the daemon cannot know in advance (`localhost:6080`
		// in a container is not the host's name). The socket is on 127.0.0.1 and the page is the only
		// thing served; there is nothing to protect from CSRF here beyond the VNC session itself.
		InsecureSkipVerify: true,
	})
	if err != nil {
		// Accept has already written the failure.
		return
	}
	defer conn.CloseNow()

	vnc, err := net.DialTimeout("tcp", c.vncAddr, 10*time.Second)
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError,
			"the machine's VNC port is not listening")
		return
	}
	defer vnc.Close()

	// NetConn IS THE TRANSLATION. WebSocket message framing is one side's business; VNC is a byte
	// stream; this hands over a net.Conn that does the conversion, so the copy below is the same
	// code that would bridge two TCP sockets.
	ws := websocket.NetConn(context.Background(), conn, websocket.MessageBinary)
	defer ws.Close()

	// BOTH DIRECTIONS, AND THE FIRST TO FAIL ENDS BOTH. A one-way copy would let the page draw a
	// frame and then never send a keystroke, which reads as a frozen guest.
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(ws, vnc); done <- struct{}{} }()
	go func() { _, _ = io.Copy(vnc, ws); done <- struct{}{} }()
	<-done
	// Closing the VNC side is what tells qemu the client went away; the WebSocket is closed by the
	// deferred CloseNow.
}

// stop shuts the server down.
func (c *consoleServer) stop() {
	if c == nil || c.srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = c.srv.Shutdown(ctx)
	c.srv = nil
}

// err reports a listener failure, for a daemon that wants to know.
func (c *consoleServer) err() error {
	if c == nil {
		return nil
	}
	select {
	case err := <-c.errc:
		return err
	default:
		return nil
	}
}

// portInUse reports whether something already accepts TCP on port.
func portInUse(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// unused keeps the strings import honest while the mux's pattern matching is the only use.
var _ = strings.TrimSpace
