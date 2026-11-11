// Package qmp owns the connection to a running qemu's QEMU Machine Protocol socket.
//
// IT IS A THIN LAYER ON github.com/digitalocean/go-qemu, AND THE REASON IS SPECIFIC. `qmp/raw` is
// 14,935 lines generated from QEMU's own schema. The commands this repository needs most -
// `send-key` and `input-send-event` - take layered UNION types: a KeyValue is a qcode, a number
// or a string depending on which, and an InputEvent is one of key, btn, rel or abs. Hand-writing
// that JSON is not merely tedious; getting the shape wrong produces a keystroke that is accepted
// and does nothing, which is the hardest kind of failure to read in a log.
//
// WHAT THIS PACKAGE ADDS, AND WHY IT CANNOT COME FROM THE LIBRARY
//
//  1. ONE CLIENT AT A TIME. qemu accepts exactly one connection per QMP socket; a second blocks on
//     the greeting and looks like a hung guest. That cost the Python implementation two runs (a
//     stage holding a Keyboard could not let a walkthrough open its own). A Registry below is the
//     one place a connection is handed out, so a second ask is an error naming the first owner
//     rather than a socket that never answers.
//
//  2. `screendump` IS NOT FINISHED WHEN IT RETURNS. It replies before the PPM write completes, so
//     a reader that opens the path immediately can see a half-written file. Screendump below waits
//     for the size to settle.
//
//  3. A FRAME HAS A NAME. Every dump goes to a numbered file in a work directory, because a
//     post-mortem needs the frame that failed and not "whatever the last dump overwrote".
package qmp

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	qemump "github.com/digitalocean/go-qemu/qmp"
	"github.com/digitalocean/go-qemu/qmp/raw"
)

// Conn is one connection to one qemu instance.
type Conn struct {
	// Path is the unix socket, for error messages.
	Path string

	mon    *qemump.SocketMonitor
	raw    *raw.Monitor
	closer func()

	mu    sync.Mutex
	frame int
}

// Open connects to path and completes the capability handshake.
//
// THE SOCKET IS WAITED FOR, because `boot:` starts qemu and the socket appears a moment later.
// Waiting on the file instead would race qemu creating it, and a connection refused during startup
// reads as a machine that will not run.
func Open(path string, wait time.Duration) (*Conn, error) {
	deadline := time.Now().Add(wait)
	var lastErr error
	for {
		mon, err := qemump.NewSocketMonitor("unix", path, 5*time.Second)
		if err == nil {
			if err = mon.Connect(); err == nil {
				return &Conn{Path: path, mon: mon, raw: raw.NewMonitor(mon)}, nil
			}
			_ = mon.Disconnect()
			lastErr = err
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("qmp %s: %w (waited %s; is another process already holding "+
				"this socket? qemu permits one client)", path, lastErr, wait)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// Close disconnects. It is safe to call more than once.
func (c *Conn) Close() error {
	if c == nil || c.mon == nil {
		return nil
	}
	err := c.mon.Disconnect()
	c.mon = nil
	return err
}

// Frame is a screen capture on disk.
type Frame struct {
	// Path is the PPM qemu wrote.
	Path string
	// Width and Height are read from the PPM header, so a caller never has to trust a constant.
	Width, Height int
}

// Screendump captures the screen into dir and returns the frame.
//
// THE FILENAME IS NUMBERED AND THE SIZE IS WAITED FOR. `screendump` replies before the write is
// done; the Python implementation treated an unparseable frame as "not a failure of the guest",
// which is correct and hides the reason. Waiting for the size to stop changing costs a few
// milliseconds and removes the ambiguity.
func (c *Conn) Screendump(dir string) (Frame, error) {
	c.mu.Lock()
	c.frame++
	n := c.frame
	c.mu.Unlock()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Frame{}, err
	}
	path := filepath.Join(dir, fmt.Sprintf("frame-%06d.ppm", n))
	if err := c.raw.Screendump(path); err != nil {
		return Frame{}, fmt.Errorf("screendump: %w", err)
	}
	if err := waitForStable(path, 5*time.Second); err != nil {
		return Frame{}, err
	}
	w, h, err := ppmSize(path)
	if err != nil {
		return Frame{}, err
	}
	return Frame{Path: path, Width: w, Height: h}, nil
}

func waitForStable(path string, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	var last int64 = -1
	stable := 0
	for time.Now().Before(deadline) {
		fi, err := os.Stat(path)
		if err != nil {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		size := fi.Size()
		if size > 0 && size == last {
			stable++
			if stable >= 2 {
				return nil
			}
		} else {
			stable = 0
		}
		last = size
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("screendump: %s never stopped growing within %s", path, limit)
}

// ppmSize reads the width and height from a binary PPM's header.
//
// IT REFUSES A TRUNCATED FILE rather than reporting a size, because a frame that is 1920 wide and
// 40 tall would make every brightness measurement silently wrong.
func ppmSize(path string) (int, int, error) {
	fh, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer fh.Close()

	head := make([]byte, 64)
	n, _ := fh.Read(head)
	text := string(head[:n])
	if !strings.HasPrefix(text, "P6") {
		return 0, 0, fmt.Errorf("%s: not a binary PPM (starts %q)", path, firstLine(text))
	}
	fields := strings.Fields(text)
	if len(fields) < 3 {
		return 0, 0, fmt.Errorf("%s: PPM header is incomplete (%q)", path, firstLine(text))
	}
	var w, h int
	if _, err := fmt.Sscanf(fields[1]+" "+fields[2], "%d %d", &w, &h); err != nil {
		return 0, 0, fmt.Errorf("%s: PPM header has no dimensions (%q)", path, firstLine(text))
	}
	if w <= 0 || h <= 0 {
		return 0, 0, fmt.Errorf("%s: PPM dimensions are %dx%d", path, w, h)
	}
	fi, err := fh.Stat()
	if err != nil {
		return 0, 0, err
	}
	// P6 has a 3-byte pixel and a header of a few bytes; a file far smaller than its own pixels is
	// a write that has not finished.
	want := int64(w*h*3) + 16
	if fi.Size() < want {
		return 0, 0, fmt.Errorf("%s: %d bytes for a %dx%d frame (needs about %d); the write has "+
			"not finished", filepath.Base(path), fi.Size(), w, h, want)
	}
	return w, h, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// --- one client at a time ------------------------------------------------------------

// ErrBusy is returned when a socket is already held.
var ErrBusy = errors.New("this qmp socket is already held by this process")

// Registry hands out connections and refuses a second one for the same socket.
//
// THE FAILURE IT PREVENTS has a specific shape: qemu accepts one QMP client per socket, so a
// second `Connect` blocks in the greeting and the caller reads it as a machine that stopped
// answering. The Python implementation hit it twice - see the note in lib/steps.py on
// `driver_for`, where a stage that already held a Keyboard could not let a walkthrough open its
// own. The fix there was to pass the connection in; the fix here is to make the second ask an
// error that names the first owner, so the bug reports itself.
type Registry struct {
	mu    sync.Mutex
	held  map[string]*Conn
	owner map[string]string
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{held: map[string]*Conn{}, owner: map[string]string{}}
}

// Acquire returns the connection for path, opening it if this process does not already hold it.
//
// `owner` is who is asking, and is used only in the error.
func (r *Registry) Acquire(path, owner string, wait time.Duration) (*Conn, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.held[path]; ok {
		return nil, fmt.Errorf("%w (by %q); pass that connection instead of opening a second - "+
			"qemu allows one client per socket", ErrBusy, r.owner[path])
	}
	c, err := Open(path, wait)
	if err != nil {
		return nil, err
	}
	r.held[path] = c
	r.owner[path] = owner
	return c, nil
}

// Release closes and forgets a connection.
func (r *Registry) Release(path string) error {
	r.mu.Lock()
	c := r.held[path]
	delete(r.held, path)
	delete(r.owner, path)
	r.mu.Unlock()
	if c == nil {
		return nil
	}
	return c.Close()
}

// CloseAll closes every connection, for a defer in main.
func (r *Registry) CloseAll() error {
	r.mu.Lock()
	conns := make([]*Conn, 0, len(r.held))
	for _, c := range r.held {
		conns = append(conns, c)
	}
	r.held = map[string]*Conn{}
	r.owner = map[string]string{}
	r.mu.Unlock()

	var errs []error
	for _, c := range conns {
		if err := c.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// --- input ----------------------------------------------------------------------------

// Click presses and releases at an absolute point.
//
// ONE EVENT PER AXIS, WHICH IS QEMU'S OWN SHAPE. `InputMoveEvent` carries an axis and a value, and
// the two axes are separate events applied in order - a version that tried to send both at once had
// nowhere to put them, because the generated type has one `Axis` field.
func (c *Conn) Click(x, y int) error {
	if err := c.moveTo(x, y); err != nil {
		return err
	}
	return c.button(raw.InputButtonLeft, true)
}

// DoubleClick moves once and presses twice. Moving between the presses would make it two clicks in
// two places, which is the bug the separate `double:` key exists to avoid.
func (c *Conn) DoubleClick(x, y int) error {
	if err := c.moveTo(x, y); err != nil {
		return err
	}
	if err := c.button(raw.InputButtonLeft, true); err != nil {
		return err
	}
	if err := c.button(raw.InputButtonLeft, false); err != nil {
		return err
	}
	time.Sleep(80 * time.Millisecond)
	if err := c.button(raw.InputButtonLeft, true); err != nil {
		return err
	}
	return c.button(raw.InputButtonLeft, false)
}

func (c *Conn) moveTo(x, y int) error {
	events := []raw.InputEvent{
		raw.InputEventAbs(raw.InputMoveEvent{Axis: raw.InputAxisX, Value: int64(x)}),
		raw.InputEventAbs(raw.InputMoveEvent{Axis: raw.InputAxisY, Value: int64(y)}),
	}
	if err := c.raw.InputSendEvent(nil, nil, events); err != nil {
		return fmt.Errorf("pointer to (%d, %d): %w", x, y, err)
	}
	return nil
}

func (c *Conn) button(b raw.InputButton, down bool) error {
	events := []raw.InputEvent{
		raw.InputEventBtn(raw.InputBtnEvent{Button: b, Down: down}),
	}
	if err := c.raw.InputSendEvent(nil, nil, events); err != nil {
		return fmt.Errorf("button: %w", err)
	}
	return nil
}

// Key sends one key with no modifiers.
func (c *Conn) Key(name string) error {
	return c.Stroke(nil, name)
}

// Stroke holds modifiers around one key.
//
// `KeyValueQcode` WRAPS A `QKeyCode`, not a string: the generated union has a numeric enum for the
// qcodes, so a name like "ctrl" has to be resolved through the library's own table. That is the
// dependency earning its place - the table is 200 keys long and is generated from qemu's schema.
func (c *Conn) Stroke(modifiers []string, key string) error {
	all := make([]raw.KeyValue, 0, len(modifiers)+1)
	for _, m := range modifiers {
		code, err := qcodeByName(m)
		if err != nil {
			return err
		}
		all = append(all, raw.KeyValueQcode(code))
	}
	code, err := qcodeByName(key)
	if err != nil {
		return err
	}
	all = append(all, raw.KeyValueQcode(code))
	hold := int64(100)
	if err := c.raw.SendKey(all, &hold); err != nil {
		return fmt.Errorf("send-key %v+%s: %w", modifiers, key, err)
	}
	return nil
}

// qcodeByName resolves a qcode string through the library's generated enum.
//
// THE ENUM IS THE ONLY AUTHORITY. Hand-writing a name table would be a second copy of 200 keys, and
// the failure it produces - a key that is accepted and does nothing - is the one this whole port is
// about. `QKeyCode.UnmarshalJSON` accepts exactly the names qemu accepts.
func qcodeByName(name string) (raw.KeyValueQcode, error) {
	var code raw.QKeyCode
	if err := code.UnmarshalJSON([]byte(`"` + name + `"`)); err != nil {
		return 0, fmt.Errorf("%q is not a qemu key name: %w", name, err)
	}
	return raw.KeyValueQcode(code), nil
}
