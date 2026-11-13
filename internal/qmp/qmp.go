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
	"image"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	// THE FRAME THIS PACKAGE WRITES IS A P6 PPM, AND THE DECODER IS WHAT DECIDES WHEN IT IS DONE.
	// A blank import registers the format with `image.Decode`, which `waitForStable` calls - the
	// same decoder `internal/screen` uses, so "complete" has ONE definition rather than two.
	_ "github.com/kovidgoyal/imaging/netpbm"

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
	lastW int
	lastH int
	// wire serialises every command that goes down the socket. `mu` above guards the struct's
	// own state; this one guards the CONVERSATION, which is what two concurrent readers corrupt.
	wire sync.Mutex
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
	// THE WHOLE DUMP IS SERIALISED, NOT JUST THE COUNTER, AND THE DIFFERENCE COST macos-setup
	// THIRTY MINUTES OF BLIND WAITING.
	//
	// TWO READERS SHARE THIS CONNECTION: the recording sampler, which asks for a frame EVERY
	// SECOND, and whatever gate is polling. Only the frame counter was under the mutex, so
	// `raw.Screendump` ran concurrently on one QMP socket and the two calls interleaved their
	// commands and responses.
	//
	// MEASURED, AND IT IS ARITHMETIC RATHER THAN A GUESS - macos-setup's gate ran
	// `poll: 10` for `timeout: 1800`, which is about 180 attempts, and its own log holds
	// FOURTEEN. The other 166 returned an error and `doUntil` continued without a word.
	//
	// SO THE SAMPLER WON ALMOST EVERY TICK and the gate mostly read nothing at all. The gate is
	// what decides whether a step passed; losing nine attempts in ten to the recorder is not a
	// recording problem.
	c.wire.Lock()
	defer c.wire.Unlock()
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
	// THIRTY SECONDS, NOT FIVE, AND THE FIVE WAS MEASURED WRONG ON A LOADED GUEST.
	//
	// The wait is "the frame decodes", so it is bounded by how long qemu takes to WRITE 6.2 MB of PPM
	// plus however long a 1920x1080 decode takes. On an idle host that is a few hundred milliseconds.
	// ON A GUEST WHOSE RECOVERY SYSTEM IS BOOTING IT IS NOT: macos-setup's gate reported
	//
	//	180 attempt(s) and never read a frame: frame-000837.ppm was not readable within 5s
	//
	// for EVERY attempt, and the frames on disk are 6,220,817 bytes - the exact size of a complete
	// 1920x1080 P6 frame. The file was fine; the reader gave up first.
	//
	// A CAP ON THE CAP: thirty seconds is longer than any screendump takes, and the attempt ceiling of
	// the caller still bounds a guest whose frames never arrive at all.
	if err := waitForStable(path, 30*time.Second); err != nil {
		return Frame{}, err
	}
	w, h, err := ppmSize(path)
	if err != nil {
		return Frame{}, err
	}
	// THE SIZE IS REMEMBERED, because the pointer's pixels are in it - see `screen()`.
	c.mu.Lock()
	c.lastW, c.lastH = w, h
	c.mu.Unlock()
	return Frame{Path: path, Width: w, Height: h}, nil
}

// waitForStable waits until the frame DECODES, because that is what the caller does with it.
//
// MEASURED ON macos-setup, AND THE NUMBERS ARE THE POINT:
//
//	the recording sampler: 1803 frames captured, 2 tick(s) lost   <- it only WRITES
//	the recovery menu gate: 165 of 179 poll(s) read no frame at all <- it also DECODES
//
// Same connection, same directory, same counter, same 1920x1080 screen. The reader that only takes
// the file almost never failed; the reader that then decoded it failed nine times in ten, with
//
//	frame-001972.ppm: unexpected EOF (is the write still in progress? format "ppm")
//
// - AND THAT ERROR COMES FROM THE DECODER, NOT FROM A SIZE CHECK. The arithmetic test this replaced
// (`stat size >= header + width*height*3`) PASSED on files the PPM decoder could not read to the end,
// so the gate was handed frames its own next call rejected. Two definitions of "complete" is one too
// many: this one now uses the consumer's.
//
// THE COST IS ONE EXTRA DECODE per frame, and it buys the only guarantee that matters - a caller
// never receives a path it cannot read. `image.Decode` is already imported by `internal/screen`, and
// a frame is 6 MB, which is milliseconds against a screendump that just spent longer writing it.
func waitForStable(path string, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	var lastErr error
	for time.Now().Before(deadline) {
		fh, err := os.Open(path)
		if err != nil {
			lastErr = err
			time.Sleep(20 * time.Millisecond)
			continue
		}
		_, _, decErr := image.Decode(fh) // registered: see the blank import in internal/screen
		_ = fh.Close()
		if decErr == nil {
			return nil
		}
		lastErr = decErr
		time.Sleep(20 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("the file never appeared")
	}
	return fmt.Errorf("screendump: %s was not readable within %s: %w", path, limit, lastErr)
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
	// P6 HAS A 3-BYTE PIXEL, AND THE HEADER LENGTH IS EXACT RATHER THAN APPROXIMATE.
	//
	// This used to require `w*h*3 + 16`, assuming a sixteen-byte header. A P6 header is
	//
	//	P6\n<width> <height>\n255\n
	//
	// which is FIFTEEN bytes for 640x480 and sixteen for nothing in particular - so a perfect 640x480
	// frame failed by one byte:
	//
	//	frame-000001.ppm: 921615 bytes for a 640x480 frame (needs about 921616);
	//	the write has not finished
	//
	// MEASURED ON THE FIRST MACOS RUN WHOSE GUEST ACTUALLY BOOTED. The frame was complete; the check
	// was wrong - and it would have failed that exact size on every 640x480 capture, which is the
	// size a guest draws at while the firmware is still switching modes.
	//
	// SO THE HEADER IS COUNTED. The payload begins after the maxval field's terminating newline, and
	// any byte before that is header. A file shorter than header+pixels is a write in progress; a file
	// that has exactly that is a finished frame.
	data := head[:n]
	hdr := ppmHeaderLen(data)
	if hdr == 0 {
		return 0, 0, fmt.Errorf("%s: PPM header does not end in a newline within %d bytes",
			filepath.Base(path), n)
	}
	want := int64(hdr) + int64(w*h*3)
	if fi.Size() < want {
		return 0, 0, fmt.Errorf("%s: %d bytes for a %dx%d frame (needs %d: %d header + %d pixels); "+
			"the write has not finished", filepath.Base(path), fi.Size(), w, h, want, hdr, w*h*3)
	}
	return w, h, nil
}

// ppmHeaderLen returns the offset of a binary PPM's pixel data: one past the maxval line's newline.
//
// THE FORMAT IS `P6\n<w> <h>\n<maxval>\n` AND THE FIELDS MAY BE SEPARATED BY ANY WHITESPACE, so the
// count cannot be a constant. Four whitespace-separated tokens are consumed - magic, width, height,
// maxval - and the byte after the whitespace that ends the last one is the first pixel.
func ppmHeaderLen(data []byte) int {
	tokens := 0
	for i := 0; i < len(data); i++ {
		switch data[i] {
		case ' ', '\t', '\r', '\n':
			continue
		default:
			// A token starts here; walk to its end.
			for i < len(data) && !isPPMSpace(data[i]) {
				i++
			}
			tokens++
			if tokens == 4 {
				// The whitespace that ended the maxval is the header's last byte.
				return i + 1
			}
			i--
		}
	}
	return 0
}

func isPPMSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r' || b == '\n'
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
// Click moves the pointer and presses, AND THE POSITION AND THE PRESS GO IN ONE EVENT LIST.
//
// THREE SEPARATE REQUESTS - MOVE, THEN PRESS, THEN RELEASE - LET THE GUEST HANDLE THE BUTTON BEFORE IT
// HAS APPLIED THE COORDINATES. QMP does not reorder events inside one `input-send-event`, so putting
// the axes and the press together makes "at (x, y), pressed" a single fact; split across requests it
// is two facts the guest may see in either order.
//
// MEASURED ON popos-install: four Select clicks across 250 seconds, and the recording is BYTE-IDENTICAL
// from the 90th second to the 340th - the pointer never moved from the top-left corner, because every
// press arrived at the position the guest still had, which was (0, 0).
//
// AND THE RELEASE FOLLOWS IN ITS OWN REQUEST after 150ms, because a widget that polls needs to see the
// button DOWN for at least one poll: sent back to back, the guest reads up/up and opens nothing.
func (c *Conn) Click(x, y int) error {
	w, h := c.screen()
	ax, ay := toAxis(x, w), toAxis(y, h)

	if err := c.raw.InputSendEvent(nil, nil, []raw.InputEvent{
		raw.InputEventAbs(raw.InputMoveEvent{Axis: raw.InputAxisX, Value: ax}),
		raw.InputEventAbs(raw.InputMoveEvent{Axis: raw.InputAxisY, Value: ay}),
		raw.InputEventBtn(raw.InputBtnEvent{Down: true, Button: raw.InputButtonLeft}),
	}); err != nil {
		return fmt.Errorf("click at (%d, %d): %w", x, y, err)
	}
	time.Sleep(150 * time.Millisecond)
	return c.button(raw.InputButtonLeft, false)
}

// DoubleClick moves once and presses twice. Moving between the presses would make it two clicks in
// two places, which is the bug the separate `double:` key exists to avoid.
func (c *Conn) DoubleClick(x, y int) error {
	if err := c.moveTo(x, y); err != nil {
		return err
	}
	_ = 0
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

// AxisMax is the range QEMU's absolute pointer axes use.
//
// `input-send-event` DOES NOT TAKE PIXELS. An `abs` value is in the device's own axis range, 0..32767,
// and a pixel value in a 1920-wide frame is therefore about SIX PERCENT of the way across the screen -
// which puts the pointer near the left edge whatever the caller asked for.
const AxisMax = 32767

// moveTo puts the pointer at a PIXEL coordinate, in the range the guest expects.
func (c *Conn) moveTo(x, y int) error {
	w, h := c.screen()
	events := []raw.InputEvent{
		// A CORNER FIRST, because a pointer that has never moved may not be where the device says.
		// The guest's first absolute report can be ignored, so the position is ESTABLISHED before it
		// is used - and the second move is the one the guest acts on.
		raw.InputEventAbs(raw.InputMoveEvent{Axis: raw.InputAxisX, Value: 0}),
		raw.InputEventAbs(raw.InputMoveEvent{Axis: raw.InputAxisY, Value: 0}),
		raw.InputEventAbs(raw.InputMoveEvent{Axis: raw.InputAxisX, Value: toAxis(x, w)}),
		raw.InputEventAbs(raw.InputMoveEvent{Axis: raw.InputAxisY, Value: toAxis(y, h)}),
	}
	if err := c.raw.InputSendEvent(nil, nil, events); err != nil {
		return fmt.Errorf("pointer to (%d, %d): %w", x, y, err)
	}
	// AND IT IS GIVEN A MOMENT, because the move is a report the guest has to process before a press
	// means anything at that position.
	time.Sleep(150 * time.Millisecond)
	return nil
}

// screen is the frame size the pointer's pixels are in, remembered from the last screendump.
//
// IT IS NOT ASSUMED. A guest starts at the firmware's mode and switches to the desktop's, so the size
// that was true at boot is not the size that is true when a click happens - and a pointer scaled by
// the wrong one lands in the wrong place in a way that looks like a dead widget.
func (c *Conn) screen() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastW > 0 && c.lastH > 0 {
		return c.lastW, c.lastH
	}
	return DefaultScreenWidth, DefaultScreenHeight
}

// DefaultScreenWidth and DefaultScreenHeight are what a guest gets before its first screendump, and
// they are the size every walkthrough in this repository was measured at.
const (
	DefaultScreenWidth  = 1920
	DefaultScreenHeight = 1080
)

// toAxis converts a pixel coordinate into the device's absolute range.
func toAxis(v, size int) int64 {
	if size <= 0 {
		return int64(v)
	}
	if v < 0 {
		v = 0
	}
	if v > size {
		v = size
	}
	return int64(v) * AxisMax / int64(size)
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

// Reset asks qemu to reset the guest, which is a warm reboot.
func (c *Conn) Reset() error {
	return c.raw.SystemReset()
}

// SystemPowerdown presses the guest's ACPI power button.
//
// IT IS A REQUEST, NOT A GUARANTEE. Windows and Linux shut down from it; macOS puts up a dialog and
// waits for a click nobody makes, which is why every macOS walkthrough names `shutdown: ssh` or
// `shutdown: guest_agent` instead.
func (c *Conn) SystemPowerdown() error {
	return c.raw.SystemPowerdown()
}

// --- snapshots ---------------------------------------------------------------------
//
// A SNAPSHOT IS A MIGRATION TO A FILE, AND THAT IS NOT A WORKAROUND.
//
// `savevm` / `loadvm` are the obvious commands and they cannot work for these guests:
//
//	savevm s1  ->  Error: Device 'pflash1' is writable but does not support snapshots
//
// `pflash1` is OVMF's variable store (NVRAM). `savevm` writes the machine state INTO a block device,
// and it requires every writable block device to support that. pflash models a firmware chip, not a
// qcow2, so it refuses - and the requirement is real, not cosmetic.
//
// `migrate` is the other path to the same thing: it serialises memory, device state AND the pflash
// contents into one byte stream. It asks nothing of the block devices, so it works with UEFI where
// `savevm` cannot. Measured on Linux, Windows and macOS guests; the macOS one needs
// `tsc-frequency=` on the cpu (see machine.Boot.CPU) or the migration is refused by the invtsc
// blocker before it starts.
//
// THE STREAM IS ALSO THE THING A PAUSE/RESUME API WANTS. It is self-contained, can be stored in an
// object store, can be read by a different node, and can be opened by many processes at once without
// copying - which is what makes forking a sandbox cheap.

// MigrateToFile writes the whole machine state to path and waits until it has landed.
//
// IT WAITS BECAUSE A CALLER THAT RETURNED EARLY WOULD LET THE NEXT STEP DELETE A HALF-WRITTEN FILE.
// The status is polled rather than assumed: `migrate` answers as soon as it starts.
func (c *Conn) MigrateToFile(path string, timeout time.Duration) error {
	// A FILE THAT ALREADY EXISTS IS A FAILED RESTORE WAITING TO HAPPEN, and qemu appends to it
	// rather than truncating.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing the previous state file: %w", err)
	}
	if err := c.raw.Migrate("file:"+path, nil, nil, nil); err != nil {
		return fmt.Errorf("starting the migration: %w", err)
	}
	deadline := time.Now().Add(timeout)
	for {
		info, err := c.raw.QueryMigrate()
		if err != nil {
			return fmt.Errorf("asking the migration how it is going: %w", err)
		}
		// A NIL STATUS IS A MIGRATION THAT HAS NOT DECIDED YET, not an error.
		if info.Status != nil {
			switch *info.Status {
			case raw.MigrationStatusCompleted:
				return nil
			case raw.MigrationStatusFailed, raw.MigrationStatusCancelled:
				desc := ""
				if info.ErrorDesc != nil {
					desc = *info.ErrorDesc
				}
				return fmt.Errorf("the migration %s: %s", *info.Status, desc)
			}
		}
		if time.Now().After(deadline) {
			st := "unknown"
			if info.Status != nil {
				st = info.Status.String()
			}
			return fmt.Errorf("the migration did not finish within %s (status %q). The state file "+
				"is %s and must be treated as unusable", timeout, st, path)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// MigrationStatus is one reading of where a migration is, for a caller that wants to log progress.
//
// IT EXISTS SO A LONG SNAPSHOT CAN SAY WHAT IT IS DOING. A 12 GiB state file is a minute of work, and
// a step that printed nothing until it finished would be indistinguishable from a hang - the same
// lesson the run loop learned about its own steps.
func (c *Conn) MigrationStatus() (status string, transferred int64, err error) {
	info, err := c.raw.QueryMigrate()
	if err != nil {
		return "", 0, err
	}
	if info.Status == nil {
		return "unknown", info.RAM.Transferred, nil
	}
	return info.Status.String(), info.RAM.Transferred, nil
}
