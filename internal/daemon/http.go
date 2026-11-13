package daemon

import (
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/easydev-org/easyvm/internal/protocol"
	"github.com/easydev-org/easyvm/internal/screen"
)

// THE HTTP SURFACE IS ONE PROCESS WITH THE DAEMON, NOT A SECOND SERVICE.
//
// A browser has to look at a machine and touch it: a screenshot to see, a click or a key to act. Every
// one of those operations already exists against the QMP connection the daemon holds, and qemu serves
// ONE client per socket - so a separate web service would either block on the greeting or need the
// daemon to proxy for it. Serving HTTP from the daemon itself removes that whole question.
//
// THE UNIX SOCKET REMAINS THE AUTHORITATIVE INTERFACE. It is a filesystem permission, which is the
// right shape for "only this user may drive this VM", and every command the browser sends goes through
// the same `handle` the CLI uses - so the two cannot disagree about what a click means.
//
//	GET  /                    the page: a live screen, a click map, keys, and the state
//	GET  /api/state           the daemon's state as JSON
//	POST /api/click           {x, y}          press and release
//	POST /api/double          {x, y}          double-click
//	POST /api/key             {key: "ret"}    one qcode or combo, e.g. ctrl+alt+t
//	POST /api/type            {text: "..."}   typed at the file's own pace
//	GET  /screen.png          the newest frame, converted from qemu's PPM
//	GET  /api/screen          the same, as a JSON envelope with the size
//
// WHY PNG AT A PATH AND NOT BASE64 IN JSON. A 1920x1080 frame is 6 MB as a PPM and about 8 MB as
// base64; at one frame a second that is 8 MB/s of JSON through a browser's parser. The image endpoint
// is a plain GET a browser caches and streams, and the JSON calls stay small.

// HTTPServeOptions configures ServeHTTP.
type HTTPServeOptions struct {
	// Addr is the listen address, e.g. ":8080".
	Addr string
	// Ready is called once the listener is open.
	Ready func(addr string)
}

// ServeHTTP serves the browser surface until the daemon shuts down.
func (d *Daemon) ServeHTTP(opts HTTPServeOptions) error {
	addr := opts.Addr
	if addr == "" {
		addr = DefaultHTTPAddr
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("http listen %s: %w", addr, err)
	}
	if opts.Ready != nil {
		opts.Ready(ln.Addr().String())
	}

	mux := http.NewServeMux()
	// THE FRONT END IS THE OFFICIAL noVNC PAGE, AND IT IS THE ROOT.
	//
	// THIS WAS A HAND-WRITTEN PAGE THAT POLLED /screen.png ONCE A SECOND. It worked, and it was still
	// the wrong thing: a screenshot a second is not a console, the page had to answer "where is my
	// click" with its own arithmetic, and the keyboard was a text field rather than a keyboard. The
	// repository already contained the noVNC bridge - `console.go`, the whole websockify replacement -
	// and it was serving a second port nobody opened.
	//
	// SO THERE IS ONE PAGE ON ONE PORT: the distro's `vnc.html` and the bridge that feeds it, mounted
	// here beside the API rather than on :6080. A person opens the Service URL and gets the machine.
	//
	// A DAEMON WITH NO MACHINE STILL HAS TO ANSWER SOMETHING, and it says so rather than showing a
	// console that cannot connect. `d.console()` is nil until a guest has booted.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		page := d.console()
		if page == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, noMachineHTML)
			return
		}
		page.mux.ServeHTTP(w, r)
	})
	mux.HandleFunc("/api/state", d.handleState)
	mux.HandleFunc("/api/screen", d.handleScreenJSON)
	mux.HandleFunc("/screen.png", d.handleScreenPNG)
	mux.HandleFunc("/api/click", d.handleClick)
	mux.HandleFunc("/api/double", d.handleDouble)
	mux.HandleFunc("/api/key", d.handleKey)
	mux.HandleFunc("/api/type", d.handleType)
	// EVERY OTHER COMMAND, INCLUDING `run` AND `create`, ARRIVES HERE with the whole Request. A
	// browser has no use for `run`, but a CLI talking over HTTP needs all eleven commands and a route
	// per command would be eleven lines of nothing.
	mux.HandleFunc("/api/command", d.handleCommand)
	// THE SOCKET noVNC CONNECTS TO. It is the same path the console's own mux uses, and it is
	// registered here because this listener is the one a browser reaches.
	mux.HandleFunc(wsPath, func(w http.ResponseWriter, r *http.Request) {
		page := d.console()
		if page == nil {
			http.Error(w, "no machine is running", http.StatusServiceUnavailable)
			return
		}
		page.bridge(w, r)
	})

	srv := &http.Server{
		Handler: mux,
		// NO WRITE TIMEOUT ON THE IMAGE ROUTE. A screenshot is 6 MB read from disk and encoded; a
		// 10-second write deadline would cut a slow viewer off mid-frame and the browser would show a
		// broken image rather than the machine.
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-d.shutdown
		_ = srv.Close()
	}()
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// DefaultHTTPAddr is where the browser surface listens when nothing says otherwise.
const DefaultHTTPAddr = ":8080"

func (d *Daemon) handleState(w http.ResponseWriter, r *http.Request) {
	st := d.snapshot()
	writeJSON(w, http.StatusOK, Response{OK: true, State: &st})
}

func (d *Daemon) handleScreenJSON(w http.ResponseWriter, r *http.Request) {
	resp := d.screenshot()
	if !resp.OK {
		writeJSON(w, http.StatusConflict, resp)
		return
	}
	// THE PATH IS NOT SENT. It is a host path, and a browser has no use for it - what it needs is
	// whether the frame exists and how big it is, which is what Screen carries.
	writeJSON(w, http.StatusOK, resp)
}

func (d *Daemon) handleScreenPNG(w http.ResponseWriter, r *http.Request) {
	resp := d.screenshot()
	if !resp.OK || resp.Screen == nil {
		http.Error(w, resp.Error, http.StatusConflict)
		return
	}
	img, err := screen.Load(resp.Screen.Path)
	if err != nil {
		http.Error(w, fmt.Sprintf("reading the frame: %v", err), http.StatusInternalServerError)
		return
	}
	// THE PPM IS THE DAEMON'S, THE PNG IS THE BROWSER'S, and the conversion is one line because both
	// are registered decoders in the standard library.
	var buf writerOnly
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, img); err != nil {
		http.Error(w, fmt.Sprintf("encoding: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buf.bytes)
}

func (d *Daemon) handleClick(w http.ResponseWriter, r *http.Request) {
	var req protocol.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "the body must be JSON: {\"x\": 100, \"y\": 200}", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, d.handle(protocol.Request{
		Command: protocol.CommandClick, X: req.X, Y: req.Y,
	}))
}

func (d *Daemon) handleDouble(w http.ResponseWriter, r *http.Request) {
	var req protocol.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "the body must be JSON: {\"x\": 100, \"y\": 200}", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, d.handle(protocol.Request{
		Command: protocol.CommandDouble, X: req.X, Y: req.Y,
	}))
}

func (d *Daemon) handleKey(w http.ResponseWriter, r *http.Request) {
	var req protocol.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "the body must be JSON: {\"key\": \"ret\"}", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, d.handle(protocol.Request{
		Command: protocol.CommandKey, Key: req.Key,
	}))
}

func (d *Daemon) handleType(w http.ResponseWriter, r *http.Request) {
	var req protocol.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "the body must be JSON: {\"text\": \"hello\"}", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, d.handle(protocol.Request{
		Command: protocol.CommandType, Text: req.Text,
	}))
}

// handleCommand runs any request the socket would accept.
//
// IT IS THE SAME `handle`, DELIBERATELY. A second dispatcher would be a second answer to "what does
// `stop` mean", and the whole point of the two transports is that they cannot disagree.
func (d *Daemon) handleCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST a Request as JSON", http.StatusMethodNotAllowed)
		return
	}
	var req protocol.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("the body must be a Request: %v", err), http.StatusBadRequest)
		return
	}
	if req.Command == "" {
		http.Error(w, "`command` is required", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, d.handle(req))
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writerOnly is a bytes.Buffer that does not grow the API surface of this file.
type writerOnly struct{ bytes []byte }

func (w *writerOnly) Write(p []byte) (int, error) {
	w.bytes = append(w.bytes, p...)
	return len(p), nil
}

// it is also an io.Writer for png.Encoder, which is the only thing that writes to it.
var _ = image.Rect
var _ = os.Getenv

// console is the running machine's noVNC surface, or nil when nothing is booted.
//
// THE DAEMON OWNS THE MACHINE AND THE PAGE IS THE MACHINE'S, so the two are asked for together: a page
// from a previous guest would be a console attached to a VNC port that no longer exists, which noVNC
// reports as "Failed to connect to server" - a message about the network for a problem that is about
// which machine is running.
func (d *Daemon) console() *consoleServer {
	d.mu.Lock()
	machine := d.machine
	d.mu.Unlock()
	if c, ok := machine.(interface{ Console() *consoleServer }); ok {
		return c.Console()
	}
	return nil
}

// noMachineHTML is what a browser gets before a machine exists.
//
// IT IS NOT AN ERROR PAGE IN THE USUAL SENSE: nothing is wrong, and the same URL works as soon as a
// run starts. Saying so is more useful than a console that cannot connect.
const noMachineHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>easyvm</title>
<style>
 body{margin:0;height:100vh;display:flex;align-items:center;justify-content:center;
      background:#16181d;color:#e6e6e6;font:16px/1.6 system-ui,sans-serif}
 div{max-width:34rem;padding:2rem}
 h1{font-size:1.1rem;font-weight:600;margin:0 0 .75rem}
 p{margin:.4rem 0;color:#a8adb8}
 code{background:#22262e;padding:.15rem .4rem;border-radius:4px;color:#e6e6e6}
</style></head><body><div>
<h1>No machine is running</h1>
<p>This page is the console, and it appears when a guest boots.</p>
<p>Start one with <code>easyvm run &lt;file&gt;</code>, or read the state with <code>GET /api/state</code>.</p>
</div></body></html>
`
