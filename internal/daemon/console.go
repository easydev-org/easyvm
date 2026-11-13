package daemon

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	// mux is the page and the bridge, kept so the DAEMON'S OWN LISTENER can serve the same two
	// things. The front end is one page on one port: a person opens the Service URL and lands on
	// the console, rather than being told that the console lives on another port.
	mux *http.ServeMux
	// errc receives a listener failure that happened after Start returned.
	errc chan error
}

// Handler is the noVNC page and its WebSocket bridge, for a listener this package does not own.
func (c *consoleServer) Handler() http.Handler {
	if c == nil {
		return nil
	}
	return c.mux
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

	c.mux = mux
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

// --- the pointer, through VNC rather than QMP ------------------------------------------------
//
// WHY THIS EXISTS AT ALL, MEASURED ON ghostbsd AT 26.3:
//
//	POST /api/click {"x":1331,"y":785}   -> the pointer did not move AT ALL, no QMP error
//	POST /api/double {"x":100,"y":100}   -> the pointer stayed at the CENTRE of the screen
//	a click in the browser console       -> the installer advanced
//
// `input-send-event` was accepted every time - no error, no refusal - and nothing reached the guest's
// input layer. The keyboard over QMP worked on the same machine, so the connection was fine and the
// absolute-pointer path was not.
//
// THE TWO PATHS CONVERGE INSIDE QEMU, so the difference is not the device: qemu's VNC server handles a
// `PointerEvent` itself, and QMP's `input-send-event` goes through a different entry that this guest
// apparently never consumes. What matters is that ONE OF THEM IS KNOWN TO WORK - it is the one every
// person who opened the browser console used - so the walkthroughs use it too.
//
// AND IT HAS TO BE THE EXISTING CONNECTION. qemu's VNC server accepts ONE client:
//
//	second connection: banner "RFB 003.008\n", then EOF at ServerInit
//
// so a second socket is refused as long as the browser bridge is attached. The console keeps a
// connection of its own and both the page and the pointer share it - with a mutex, because VNC is a
// byte stream and interleaving two writers would corrupt the protocol.

// vncConn is one RFB session, used for pointer and key events.
type vncConn struct {
	conn net.Conn
	mu   sync.Mutex
	w, h int
}

// dialVNC opens one RFB session and completes the handshake.
//
// THE HANDSHAKE IS FOUR STEPS AND EVERY ONE IS REQUIRED: the server speaks first, the client echoes a
// version, the server offers security types, the client chooses one, and only then does ServerInit
// arrive. A session that skips to sending events gets them ignored.
func dialVNC(addr string) (*vncConn, error) {
	c, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("vnc dial %s: %w", addr, err)
	}
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))

	banner := make([]byte, 12)
	if _, err := io.ReadFull(c, banner); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("vnc banner: %w", err)
	}
	if _, err := c.Write([]byte("RFB 003.008\n")); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("vnc version: %w", err)
	}
	n := make([]byte, 1)
	if _, err := io.ReadFull(c, n); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("vnc security count: %w", err)
	}
	types := make([]byte, n[0])
	if _, err := io.ReadFull(c, types); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("vnc security types: %w", err)
	}
	// TYPE 1 IS "NONE", which is what a qemu with no VNC password offers. Anything else would need a
	// credential this daemon does not have, and saying so is better than a handshake that hangs.
	none := false
	for _, t := range types {
		if t == 1 {
			none = true
		}
	}
	if !none {
		_ = c.Close()
		return nil, fmt.Errorf("vnc at %s offers security types %v and this daemon only speaks None",
			addr, types)
	}
	if _, err := c.Write([]byte{1}); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("vnc security choose: %w", err)
	}
	res := make([]byte, 4)
	if _, err := io.ReadFull(c, res); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("vnc security result: %w", err)
	}
	if binary.BigEndian.Uint32(res) != 0 {
		_ = c.Close()
		return nil, fmt.Errorf("vnc at %s refused the None security type", addr)
	}
	if _, err := c.Write([]byte{1}); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("vnc client init: %w", err)
	}
	init := make([]byte, 24)
	if _, err := io.ReadFull(c, init); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("vnc server init: %w", err)
	}
	vc := &vncConn{
		conn: c,
		w:    int(binary.BigEndian.Uint16(init[0:2])),
		h:    int(binary.BigEndian.Uint16(init[2:4])),
	}
	// SERVERINIT IS THE LAST THING THE HANDSHAKE NEEDS, and the name that follows it is not: it is
	// read and discarded so the stream is positioned at the first server message.
	nameLen := binary.BigEndian.Uint32(init[20:24])
	if nameLen > 0 {
		name := make([]byte, nameLen)
		_, _ = io.ReadFull(c, name)
	}
	_ = c.SetDeadline(time.Time{})
	return vc, nil
}

// Pointer sends one absolute pointer event, in GUEST PIXELS.
//
// NO 0..32767 SCALING, which is the other reason this path is simpler than QMP's: RFB's PointerEvent
// carries x and y in the framebuffer's own coordinates.
func (v *vncConn) Pointer(x, y int, mask byte) error {
	if v == nil {
		return errors.New("no VNC session")
	}
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	if v.w > 0 && x >= v.w {
		x = v.w - 1
	}
	if v.h > 0 && y >= v.h {
		y = v.h - 1
	}
	ev := make([]byte, 6)
	ev[0] = 5 // PointerEvent
	ev[1] = mask
	binary.BigEndian.PutUint16(ev[2:4], uint16(x))
	binary.BigEndian.PutUint16(ev[4:6], uint16(y))
	v.mu.Lock()
	defer v.mu.Unlock()
	_ = v.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err := v.conn.Write(ev)
	return err
}

// Click moves, presses, and releases - in THREE events, because RFB's button mask is part of the
// PointerEvent and a press is a mask bit rather than a separate message.
func (v *vncConn) Click(x, y int) error {
	if err := v.Pointer(x, y, 0); err != nil {
		return err
	}
	// A WIDGET THAT POLLS NEEDS TO SEE THE BUTTON DOWN, which is the same reason the QMP path waited
	// between press and release.
	time.Sleep(120 * time.Millisecond)
	if err := v.Pointer(x, y, 1); err != nil {
		return err
	}
	time.Sleep(150 * time.Millisecond)
	return v.Pointer(x, y, 0)
}

// Key sends one RFB KeyEvent.
//
// THE KEYSYM IS X11'S, not qemu's qcode: RFB predates qemu and carries X keysyms, which is why this
// is a second table rather than a reuse of the QMP one.
func (v *vncConn) Key(keysym uint32, down bool) error {
	if v == nil {
		return errors.New("no VNC session")
	}
	ev := make([]byte, 8)
	ev[0] = 4 // KeyEvent
	if down {
		ev[1] = 1
	}
	binary.BigEndian.PutUint32(ev[4:8], keysym)
	v.mu.Lock()
	defer v.mu.Unlock()
	_ = v.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err := v.conn.Write(ev)
	return err
}

// Close ends the session.
func (v *vncConn) Close() error {
	if v == nil {
		return nil
	}
	return v.conn.Close()
}
