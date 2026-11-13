package qmp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/easydev-org/easyvm/internal/keys"
)

// THE PPM READER IS THE PART THAT CAN BE WRONG QUIETLY. A frame whose dimensions are misread makes
// every brightness measurement silently wrong, and the symptom is a desktop test that never
// passes rather than an error anyone can see. So it is tested against a file built to the spec and
// against the three ways a file can be truncated.
func writePPM(t *testing.T, dir, name string, w, h int, fill byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	body := make([]byte, w*h*3)
	for i := range body {
		body[i] = fill
	}
	head := []byte("P6\n" + itoa(w) + " " + itoa(h) + "\n255\n")
	if err := os.WriteFile(path, append(head, body...), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func TestPPMDimensionsAreReadFromTheFile(t *testing.T) {
	dir := t.TempDir()
	path := writePPM(t, dir, "ok.ppm", 1920, 1080, 0x40)
	w, h, err := ppmSize(path)
	if err != nil {
		t.Fatal(err)
	}
	if w != 1920 || h != 1080 {
		t.Fatalf("read %dx%d, want 1920x1080", w, h)
	}
}

// A HALF-WRITTEN FRAME MUST BE AN ERROR, not a frame with the wrong shape. `screendump` replies
// before the write finishes, so this is a real race and not a hypothetical.
func TestTruncatedFrameIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := writePPM(t, dir, "short.ppm", 1920, 1080, 0x00)

	full, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Cut the pixel data in half, keeping a valid header.
	head := strings.Index(string(full), "255\n") + 4
	truncated := append([]byte{}, full[:head+(len(full)-head)/2]...)
	if err := os.WriteFile(path, truncated, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := ppmSize(path); err == nil {
		t.Fatal("a truncated frame must be refused; every measurement on it would be wrong")
	}
}

func TestNotAPPMIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.ppm")
	if err := os.WriteFile(path, []byte("PNG not really"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ppmSize(path); err == nil {
		t.Fatal("a file that is not a binary PPM must be refused")
	}
}

// --- the one-client rule -------------------------------------------------------------

// QEMU ACCEPTS ONE CLIENT PER SOCKET, so a second ask must be an error that names the first owner
// rather than a Connect that blocks. This is the bug the Python implementation hit twice.
func TestSecondAcquireIsRefusedByName(t *testing.T) {
	r := NewRegistry()
	// Opening a socket that does not exist fails, so this exercises the bookkeeping half: pretend
	// the first acquire succeeded by inserting directly, which is what Release/Acquire manage.
	r.held["/tmp/not-a-real.sock"] = &Conn{Path: "/tmp/not-a-real.sock"}
	r.owner["/tmp/not-a-real.sock"] = "the ssh stage"

	_, err := r.Acquire("/tmp/not-a-real.sock", "a walkthrough", 10)
	if err == nil {
		t.Fatal("a second Acquire must be refused while the socket is held")
	}
	if !strings.Contains(err.Error(), "the ssh stage") {
		t.Fatalf("the refusal must name who holds it; got %v", err)
	}
	if !strings.Contains(err.Error(), "one client") {
		t.Fatalf("the refusal must say why; got %v", err)
	}
}

func TestReleaseForgetsTheSocket(t *testing.T) {
	r := NewRegistry()
	r.held["/tmp/x.sock"] = &Conn{Path: "/tmp/x.sock"}
	r.owner["/tmp/x.sock"] = "first"
	if err := r.Release("/tmp/x.sock"); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.held["/tmp/x.sock"]; ok {
		t.Fatal("Release must forget the socket so the next Acquire can open it")
	}
}

// --- keys ------------------------------------------------------------------------------

// A COLON IS SHIFT+SEMICOLON. Getting this wrong types a semicolon, which reads in a guest as a
// shell syntax error rather than as a keyboard bug - and that is why the table lives in one place.
func TestShiftedCharactersAreCombos(t *testing.T) {
	strokes, err := keys.ForText("a:b")
	if err != nil {
		t.Fatal(err)
	}
	if len(strokes) != 3 {
		t.Fatalf("got %d strokes for 3 characters", len(strokes))
	}
	if strokes[1].Key != "semicolon" || len(strokes[1].Modifiers) != 1 ||
		strokes[1].Modifiers[0] != "shift" {
		t.Fatalf("':' became %+v; it must be shift+semicolon", strokes[1])
	}
}

func TestUppercaseIsShifted(t *testing.T) {
	strokes, err := keys.ForText("Ab")
	if err != nil {
		t.Fatal(err)
	}
	if strokes[0].Key != "a" || strokes[0].Modifiers[0] != "shift" {
		t.Fatalf("'A' became %+v", strokes[0])
	}
	if strokes[1].Key != "b" || len(strokes[1].Modifiers) != 0 {
		t.Fatalf("'b' became %+v", strokes[1])
	}
}

// A NEWLINE IS REFUSED BY NAME, because qemu has no key for it and a caller that passes one has a
// command that needs splitting - a decision, not a retry.
func TestNewlineIsRefused(t *testing.T) {
	if _, err := keys.ForText("echo hi\nwhoami"); err == nil {
		t.Fatal("a newline must be refused")
	}
}

// A COMBO IS ONE STROKE, NOT THREE. `ctrl+alt+t` opens a terminal; sending the three keys in
// sequence types them and does nothing.
func TestComboIsOneStroke(t *testing.T) {
	strokes, err := keys.ParseCombo("ctrl+alt+t")
	if err != nil {
		t.Fatal(err)
	}
	if len(strokes) != 1 {
		t.Fatalf("ctrl+alt+t must be one stroke, got %d", len(strokes))
	}
	if len(strokes[0].Modifiers) != 2 || strokes[0].Key != "t" {
		t.Fatalf("ctrl+alt+t became %+v", strokes[0])
	}
}

func TestMisspelledKeyIsRefused(t *testing.T) {
	for _, bad := range []string{"enter", "Return", "ctrl+", "nonsense"} {
		if _, err := keys.ParseCombo(bad); err == nil {
			t.Fatalf("%q must be refused; a misspelled key is a step that sends nothing", bad)
		}
	}
}

func TestNamedKeysParse(t *testing.T) {
	for _, good := range []string{"ret", "esc", "tab", "f12", "ctrl+alt+t", "a", "shift+tab"} {
		if _, err := keys.ParseCombo(good); err != nil {
			t.Fatalf("%q should parse: %v", good, err)
		}
	}
}

// A COMPLETE 640x480 FRAME MUST NOT BE CALLED TRUNCATED - AND IT WAS, BY ONE BYTE.
//
// The check required `w*h*3 + 16`, assuming a sixteen-byte header. A P6 header is
//
//	P6\n640 480\n255\n
//
// which is FIFTEEN bytes, so a perfect frame of 921615 bytes was rejected:
//
//	frame-000001.ppm: 921615 bytes for a 640x480 frame (needs about 921616);
//	the write has not finished
//
// MEASURED ON THE FIRST macOS RUN WHOSE GUEST ACTUALLY BOOTED, on the step after it. The frame was
// complete and the check was wrong - and it would reject that size on every 640x480 capture, which is
// what a guest draws while the firmware is still switching modes.
func TestAPerfectPPMIsNotTruncated(t *testing.T) {
	for _, c := range []struct{ w, h int }{
		{640, 480},   // what a guest draws while the firmware switches modes
		{1280, 800},  // qemu's own default
		{1920, 1080}, // what every coordinate here was measured at
	} {
		dir := t.TempDir()
		header := "P6\n" + com_itoa(c.w) + " " + com_itoa(c.h) + "\n255\n"
		body := make([]byte, c.w*c.h*3)

		path := filepath.Join(dir, "frame.ppm")
		if err := os.WriteFile(path, append([]byte(header), body...), 0o644); err != nil {
			t.Fatal(err)
		}
		w, h, err := ppmSize(path)
		if err != nil {
			t.Fatalf("%dx%d: a complete frame was rejected: %v", c.w, c.h, err)
		}
		if w != c.w || h != c.h {
			t.Fatalf("dimensions read as %dx%d, want %dx%d", w, h, c.w, c.h)
		}

		// AND ONE BYTE SHORT IS STILL TRUNCATED, which is the check's whole job: a capture that lands
		// mid-write must not be measured.
		short := filepath.Join(dir, "short.ppm")
		if err := os.WriteFile(short, append([]byte(header), body[:len(body)-1]...), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := ppmSize(short); err == nil {
			t.Fatalf("%dx%d: a frame missing its last pixel byte was accepted", c.w, c.h)
		}
	}
}

func com_itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}

// A CLICK MOVES THE POINTER FIRST, AS ITS OWN REQUEST, AND THE PRESS FOLLOWS.
//
// THE PREVIOUS VERSION OF THIS TEST ASSERTED THE OPPOSITE, on a real measurement from popos-install:
// three requests - move, press, release - produced a pointer that never left the top-left corner, and
// the conclusion drawn was "the position and the press must be one event list".
//
// THAT CONCLUSION WAS WRONG, AND ghostbsd SHOWED IT ON ONE MACHINE, WITH THE KEYBOARD WORKING:
//
//	POST /api/click  {"x":1331,"y":785}  -> the pointer did not move AT ALL
//	POST /api/double {"x":400,"y":300}   -> the pointer moved to (400,300)
//
// `DoubleClick` called `moveTo` first; `Click` did not. The popos measurement's real cause was the
// one-request version applying the press at the position the guest held BEFORE the axes - a HID
// tablet reports position and buttons separately, so "x, y, pressed" in one report is "the button
// went down somewhere, and the pointer is now at x,y".
//
// THE TWO THINGS THAT MUST BE TRUE, AND BOTH ARE ASSERTED:
//
//	1  the move is `moveTo`, which scales pixels into the DEVICE'S RANGE (0..32767) and ESTABLISHES
//	   the position twice - a corner, then the target - because the first absolute report after
//	   enumeration can be ignored
//	2  the press and release are separate requests with a delay between them, because a widget that
//	   polls needs to see the button DOWN for at least one poll
func TestAClickMovesBeforeItPresses(t *testing.T) {
	body, err := os.ReadFile("qmp.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)

	// THE AXIS RANGE EXISTS AND IS USED.
	if !strings.Contains(src, "AxisMax = 32767") {
		t.Fatal("no axis range constant; `input-send-event` does not take pixels")
	}
	i := strings.Index(src, "func (c *Conn) Click(")
	if i < 0 {
		t.Fatal("no Click")
	}
	rest := src[i:]
	if end := strings.Index(rest[1:], "\nfunc "); end > 0 {
		rest = rest[:end+1]
	}
	code := dropComments(rest)

	// THE MOVE COMES FIRST, THROUGH `moveTo`.
	moveAt := strings.Index(code, "c.moveTo(")
	pressAt := strings.Index(code, "InputEventBtn")
	if moveAt < 0 && pressAt < 0 {
		// the press may be spelled `c.button`, which is the same thing
		pressAt = strings.Index(code, "c.button(")
	}
	if moveAt < 0 {
		t.Fatal("`Click` does not call `moveTo`, so the pointer stays where it was and the press " +
			"lands there - measured on ghostbsd as a pointer that never moved")
	}
	if pressAt >= 0 && pressAt < moveAt {
		t.Fatal("`Click` presses before it moves; the guest would act at the old position")
	}
	if !strings.Contains(code, "toAxis(") && !strings.Contains(src, "func (c *Conn) moveTo") {
		t.Fatal("no pixel-to-axis conversion anywhere; the pointer lands near the left edge")
	}
	// AND THE RELEASE IS DELAYED.
	if !strings.Contains(code, "150 * time.Millisecond") {
		t.Fatal("`Click` releases immediately; a polling widget needs the button down for at least " +
			"one poll or it reads up/up and opens nothing")
	}
}

// AND THE POINTER IS SCALED BY THE GUEST'S OWN SIZE, NOT A CONSTANT.
func TestThePointerUsesTheGuestsOwnSize(t *testing.T) {
	body, err := os.ReadFile("qmp.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	i := strings.Index(src, "func (c *Conn) screen()")
	if i < 0 {
		t.Fatal("no screen(); the pointer would be scaled by an assumed size")
	}
	rest := src[i:]
	if end := strings.Index(rest[1:], "\nfunc "); end > 0 {
		rest = rest[:end+1]
	}
	if !strings.Contains(rest, "c.lastW") {
		t.Fatal("`screen()` does not use the size remembered from a screendump, so a guest that " +
			"switched modes gets a pointer scaled by the firmware's size")
	}
	// AND THE SCREENDJUMP RECORDS IT.
	if !strings.Contains(src, "c.lastW, c.lastH = w, h") {
		t.Fatal("`Screendump` does not record the frame size, so `screen()` has nothing to report")
	}
}

// dropComments removes `//` comments so a check reads the CODE rather than the paragraph explaining it.
func dropComments(src string) string {
	var out []byte
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == '"' || c == '\'':
			q := c
			out = append(out, c)
			i++
			for i < len(src) {
				out = append(out, src[i])
				if src[i] == '\\' && i+1 < len(src) {
					out = append(out, src[i+1])
					i += 2
					continue
				}
				if src[i] == q {
					i++
					break
				}
				i++
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			i += 2
		default:
			out = append(out, c)
			i++
		}
	}
	return string(out)
}

// TWO READERS ON ONE CONNECTION MUST NOT INTERLEAVE THEIR COMMANDS.
//
// MEASURED ON macos-setup, AND IT IS ARITHMETIC: the recovery-menu gate ran `poll: 10` with
// `timeout: 1800`, which is about 180 attempts, and its own daemon.log holds FOURTEEN read lines.
// The other 166 returned an error and `doUntil` continued without a word - so the gate that decides
// whether a step passed was reading nothing nine times in ten.
//
// THE OTHER READER IS THE RECORDING SAMPLER, WHICH ASKS FOR A FRAME EVERY SECOND. Only the frame
// COUNTER was under the mutex, so `raw.Screendump` ran concurrently on one QMP socket: the two
// calls interleave commands and responses, and the loser gets an answer to somebody else's request.
//
// `Screendump` now holds `wire` for its whole conversation. This test is a source check because the
// failure needs a real qemu to reproduce, and the thing that must not regress is the locking.
// ONE CONNECTION CARRIES ONE COMMAND AT A TIME, AND THE TRANSPORT IS WHAT SAYS SO.
//
// TWO READERS SHARE THIS CONNECTION: the recording sampler, which asks for a frame EVERY SECOND, and
// whatever gate is polling. Without serialisation they interleave their commands and replies on one
// socket, and MEASURED on macos-setup that lost 166 of 180 gate polls silently - the gate is what
// decides whether a step passed, so losing nine attempts in ten is not a recording problem.
//
// THE LOCK MOVED FROM `Screendump` TO THE TRANSPORT, AND THAT IS AN IMPROVEMENT RATHER THAN A
// RELOCATION: a lock taken by one command protects the commands that remembered to take it. `line.Run`
// takes it for the socket write and the socket read, so EVERY command is serialised by construction -
// including the next one somebody adds.
func TestEveryCommandOnAConnectionIsSerialised(t *testing.T) {
	body, err := os.ReadFile("monitor.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	start := strings.Index(src, "func (l *line) Run(")
	if start < 0 {
		t.Fatal("no `line.Run`; the transport that serialises commands is gone")
	}
	rest := src[start:]
	if end := strings.Index(rest[1:], "\nfunc "); end > 0 {
		rest = rest[:end+1]
	}
	var code []string
	for _, line := range strings.Split(rest, "\n") {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "//") {
			continue
		}
		code = append(code, line)
	}
	fn := strings.Join(code, "\n")

	lock := strings.Index(fn, "l.mu.Lock()")
	if lock < 0 {
		t.Fatal("`line.Run` takes no lock, so two readers interleave their commands and replies on " +
			"one socket - measured as 166 of 180 polls reading nothing")
	}
	unlock := strings.Index(fn, "defer l.mu.Unlock()")
	if unlock < 0 {
		t.Fatal("`line.Run` takes the lock without releasing it on every path")
	}
	write := strings.Index(fn, "l.nc.Write(command)")
	read := strings.Index(fn, "dec.Decode(&reply)")
	if write < 0 || read < 0 {
		t.Fatal("`line.Run` no longer writes a command and reads a reply")
	}
	if lock > write || lock > read {
		t.Fatalf("the lock is taken AFTER the socket work (lock %d, write %d, read %d), so it "+
			"guards nothing", lock, write, read)
	}

	// AND EVERY COMMAND HAS A DEADLINE, which is what turns "qemu stopped answering" into an error
	// instead of a caller parked forever - the seven-minute hang.
	if !strings.Contains(fn, "SetDeadline(") {
		t.Fatal("`line.Run` sets no deadline, so a qemu that stops answering parks the caller " +
			"forever. Measured as `boot the recovery image` sitting seven minutes past its own " +
			"`timeout:`")
	}

	// AND `Close` MUST NOT WANT THE SAME LOCK, or a close during a command waits for a reply that is
	// never coming - measured as a `destroy` that never returned.
	cs := strings.Index(src, "func (l *line) Close() error {")
	if cs < 0 {
		t.Fatal("no `line.Close`")
	}
	crest := src[cs:]
	if end := strings.Index(crest[1:], "\nfunc "); end > 0 {
		crest = crest[:end+1]
	}
	if strings.Contains(crest, "l.mu.Lock()") {
		t.Fatal("`line.Close` takes `mu`, the lock a command holds while it waits for a reply. A " +
			"close during a command then waits for that reply, which is the hang this transport " +
			"replaced. It must close the descriptor instead")
	}
}

// A FRAME IS FINISHED WHEN IT DECODES, AND A PNG DECODES ONLY AT ITS IEND CHUNK.
//
// MEASURED ON windows, AND THE FAILURE WAS SILENT: every frame was written as PNG and `screen.Load`
// only had the PPM decoder registered, so `IsWindowsDesktop` was never handed an image -
//
//	image: unknown format (is the write still in progress? format "")
//
// - and `wait for the desktop` sat on a desktop that was fully drawn.
//
// THE ORDER IS: two polls must see the same size (a write that is still growing is not worth reading),
// and THEN the file must decode. A PNG that is half-written fails `image.Decode` because the reader
// reaches EOF before IEND, which is exactly the property this test asserts.
func TestAFrameIsCompleteWhenItDecodes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "frame-000001.png")

	// A COMPLETE 1920x1080 PNG, ENCODED BY THE STANDARD LIBRARY so the bytes are real.
	img := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	for y := 0; y < 1080; y += 8 {
		for x := 0; x < 1920; x += 8 {
			img.Set(x, y, color.RGBA{uint8(x % 255), uint8(y % 255), 64, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	full := buf.Bytes()

	// THE FIRST CHUNK IS MOST OF THE FILE, cut in the middle of the pixel data.
	half := len(full) * 2 / 3
	if err := os.WriteFile(path, full[:half], 0o644); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- waitForStable(path, 5*time.Second) }()

	// THE GAP IN WHICH THE OLD CODE WOULD HAVE RELEASED THE READER.
	time.Sleep(300 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("waitForStable released a reader on a TRUNCATED png (err=%v); a caller handed that "+
			"file cannot decode it, which is what made `wait for the desktop` read nothing", err)
	default:
	}

	if err := os.WriteFile(path, full, 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("waitForStable did not accept a complete 1920x1080 png: %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("waitForStable never accepted a complete frame")
	}
}

// A peer that speaks QMP: the greeting, then one JSON object per line for every command.
//
// IT DECODES BY JSON AND NOT BY NEWLINE COUNTING, AND THAT IS THE BUG THIS FILE HAD ONCE. The first
// version read replies with `bufio.ReadBytes('\n')`, but `json.Marshal` writes no trailing newline - so
// the peer never answered, every command parked in `<-mon.stream`, and the test "found" a deadlock in
// the vendored library that was really its own scaffolding.
type fakeQMP struct {
	ln   net.Listener
	path string
	// reply is what each command gets. A nil function means `{"return":{}}`.
	reply func(command string) (string, bool)
	// silent makes the peer accept commands and never answer them, which is what a qemu that has
	// stopped responding looks like.
	silent bool
}

func newFakeQMP(t *testing.T) *fakeQMP {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "qmp.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeQMP{ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	f.path = path
	go f.serve()
	return f
}

func (f *fakeQMP) serve() {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		go func(nc net.Conn) {
			defer nc.Close()
			_ = nc.SetDeadline(time.Now().Add(30 * time.Second))
			_, _ = io.WriteString(nc, `{"QMP":{"version":{"qemu":{"major":11,"minor":1,"micro":0,"package":""}},"capabilities":["oob"]}}`+"\n")
			dec := json.NewDecoder(nc)
			for {
				var cmd struct {
					Execute string `json:"execute"`
				}
				if err := dec.Decode(&cmd); err != nil {
					return
				}
				// THE CAPABILITY HANDSHAKE IS ALWAYS ANSWERED: `silent` models a qemu that stops
				// responding to COMMANDS, not one that never greets, because an unanswered greeting is
				// a different failure - the socket is held by another client.
				if f.silent && cmd.Execute != "qmp_capabilities" {
					continue
				}
				body := `{"return":{}}`
				if f.reply != nil {
					if r, ok := f.reply(cmd.Execute); ok {
						body = r
					}
				}
				// A NEWLINE AFTER THE JSON, because that is what the transport decodes against. QMP is
				// newline-delimited, and a peer that omits it is a peer that never answers.
				if _, err := io.WriteString(nc, body+"\n"); err != nil {
					return
				}
			}
		}(conn)
	}
}

// THE CONNECTION REPORTS WHAT THE GREETING SAID, so a caller can tell an old qemu from a broken one.
func TestOpenReadsTheGreeting(t *testing.T) {
	f := newFakeQMP(t)
	c, err := Open(f.path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	v := c.l.Version()
	if v.Major != 11 || v.Minor != 1 {
		t.Fatalf("the greeting said qemu %d.%d, wanted 11.1", v.Major, v.Minor)
	}
	if caps := c.l.capabilities(); len(caps) != 1 || caps[0] != "oob" {
		t.Fatalf("capabilities %v, wanted [oob]", caps)
	}
}

// A CLOSED CONNECTION IS AN ERROR, NOT A NIL DEREFERENCE, AND NOT A HANG.
//
// THIS IS THE EXACT CRASH THAT TOOK A DAEMON DOWN AS PID 1:
//
//	panic: runtime error: invalid memory address or nil pointer dereference
//	qmp.(*SocketMonitor).RunWithFile(0x0, ...)     <- the receiver was nil
//	daemon.(*Driver).Frame()
//	run.(*Executor).sampler.func1()                <- the recorder's 1 Hz tick
//
// `destroy` closed the connection; the recorder's sampler had ALREADY been handed that `*Conn` and
// called `Screendump` a moment later. And the second half: a `Close` while a command is in flight must
// return, not wait for a reply that will never come.
func TestCloseIsSafeWhileACommandIsInFlight(t *testing.T) {
	f := newFakeQMP(t)
	// A PEER THAT NEVER ANSWERS is what makes this the hard case: a command blocked in Read can only be
	// released by the socket closing.
	f.reply = func(string) (string, bool) { return "", false }
	f.silent = true

	c, err := Open(f.path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	problems := make(chan error, 64)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					problems <- fmt.Errorf("a command on a closing connection panicked: %v", r)
				}
			}()
			if err := c.SystemPowerdown(); err != nil && !errors.Is(err, ErrClosed) {
				problems <- err
			}
		}()
	}
	// GIVE THE COMMANDS TIME TO BE INSIDE THE SOCKET, so the close genuinely races them.
	time.Sleep(50 * time.Millisecond)
	closed := make(chan error, 1)
	go func() { closed <- c.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Errorf("Close returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return within 5s while commands were blocked on a silent peer. " +
			"This is the seven-minute hang: a close that waits for a reply nobody will send")
	}
	wg.Wait()
	close(problems)
	for err := range problems {
		t.Error(err)
	}

	// AND AFTERWARDS THE ANSWER IS ALWAYS ErrClosed, EVERY TIME, NOT A PANIC.
	if err := c.Click(10, 10); !errors.Is(err, ErrClosed) {
		t.Fatalf("Click after Close returned %v, wanted ErrClosed", err)
	}
	if err := c.SystemPowerdown(); !errors.Is(err, ErrClosed) {
		t.Fatalf("SystemPowerdown after Close returned %v, wanted ErrClosed", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("the second Close returned %v", err)
	}
}

// A COMMAND HAS A DEADLINE, so a machine that stops answering fails the step instead of the leg.
//
// WITHOUT THIS the failure mode is not an error, it is NOTHING: `Run` waits on a channel that qemu will
// never write to. MEASURED as `boot the recovery image` sitting for seven minutes past its own
// `timeout:` - the walk could not report the hang because the code was inside it.
func TestACommandOnASilentPeerTimesOut(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the command deadline")
	}
	f := newFakeQMP(t)
	f.silent = true
	c, err := Open(f.path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	start := time.Now()
	err = c.SystemPowerdown()
	if err == nil {
		t.Fatal("a command against a peer that never answers returned nil")
	}
	// IT MUST BE THE DEADLINE, NOT AN EOF: the socket is still open, the peer is merely quiet.
	if elapsed := time.Since(start); elapsed > commandTimeout+10*time.Second {
		t.Fatalf("the command took %s, and the deadline is %s", elapsed, commandTimeout)
	}
}

// A SECOND CALLER FOR THE SAME SOCKET GETS `ErrBusy`, AND THAT IS A HANDOVER RATHER THAN A FAILURE.
//
// THIS IS THE CONTRACT THE DAEMON'S BOOT RELIES ON. The recorder's sampler asks for a frame every second
// and `Frame()` needs the connection, so during a boot the sampler and the boot race for it. Whoever
// loses gets `ErrBusy` - and the daemon turns that into "wait for the winner's connection" instead of
// "this machine cannot boot".
//
// MEASURED ON CI: without that translation, ubuntu-install and bsd-ghostbsd-install sat at
// `status: starting` until the job timed out, produced a ONE-SECOND video, and never printed the
// `qemu is up` line - because `Boot` returned the busy error for a machine that was running perfectly.
func TestASecondAcquireOnOneSocketIsBusyRatherThanASecondClient(t *testing.T) {
	f := newFakeQMP(t)
	reg := NewRegistry()

	first, err := reg.Acquire(f.path, "the-recorder", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Release(f.path)

	// THE SECOND CALLER MUST BE TOLD, NOT GIVEN A SECOND CONNECTION - qemu greets one client and
	// queues the rest in a backlog it never answers.
	_, err = reg.Acquire(f.path, "the-boot", 5*time.Second)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("the second Acquire returned %v, wanted ErrBusy: qemu serves ONE client per socket, "+
			"and a second connection blocks in the greeting until the first is closed", err)
	}
	// AND THE MESSAGE NAMES THE HOLDER, because "busy" without a name sends a reader to the socket.
	if !strings.Contains(err.Error(), "the-recorder") {
		t.Fatalf("the busy error %q does not name the first owner", err.Error())
	}

	// AND AFTER THE RELEASE IT IS AVAILABLE AGAIN, which is what makes the wait worthwhile: the winner
	// publishes its connection and the loser never has to ask a second time.
	if err := reg.Release(f.path); err != nil {
		t.Fatal(err)
	}
	_ = first
	again, err := reg.Acquire(f.path, "the-boot", 5*time.Second)
	if err != nil {
		t.Fatalf("the socket was not acquirable after Release: %v", err)
	}
	_ = again.Close()
}
