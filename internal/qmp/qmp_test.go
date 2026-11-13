package qmp

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
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
func TestScreendumpHoldsTheWireForTheWholeConversation(t *testing.T) {
	body, err := os.ReadFile("qmp.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	start := strings.Index(src, "func (c *Conn) Screendump(")
	if start < 0 {
		t.Fatal("Screendump is gone")
	}
	end := strings.Index(src[start:], "\n}\n")
	if end < 0 {
		t.Fatal("cannot find the end of Screendump")
	}
	fn := src[start : start+end]

	if !strings.Contains(fn, "c.wire.Lock()") {
		t.Error("Screendump does not take `wire`. Two readers share this connection - the " +
			"recording sampler ticks every second - and without serialisation they interleave " +
			"their QMP commands. Measured on macos-setup: 14 of ~180 polls read a frame and 166 " +
			"failed silently")
	}
	if !strings.Contains(fn, "defer c.wire.Unlock()") {
		t.Error("Screendump takes `wire` without releasing it on every path")
	}
	// AND THE WHOLE DUMP IS INSIDE IT. A lock taken after `raw.Screendump` is a lock that guards
	// the bookkeeping and not the conversation. COMMENTS ARE STRIPPED FIRST: the note above the
	// lock mentions `raw.Screendump` by name, and matching that made this check fail on code that
	// was right.
	var code []string
	for _, line := range strings.Split(fn, "\n") {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "//") {
			continue
		}
		code = append(code, line)
	}
	lines := strings.Join(code, "\n")
	iLock := strings.Index(lines, "c.wire.Lock()")
	// THE DUMP GOES THROUGH `c.mon.Run` NOW, because PNG needs a `format:` argument the library's
	// `raw.Screendump` does not pass - so the call to serialise is that one.
	iDump := strings.Index(lines, "c.mon.Run(cmd)")
	if iDump < 0 {
		iDump = strings.Index(lines, "raw.Screendump")
	}
	if iLock < 0 || iDump < 0 || iLock > iDump {
		t.Errorf("`wire` is not taken before the screendump call (lock at %d, dump at %d), so it "+
			"does not serialise it", iLock, iDump)
	}
	// `mu` MUST NOT BE HELD ACROSS THE WAIT: the wait is up to five seconds and `screen()` takes
	// `mu` for every click.
	if strings.Contains(fn, "defer c.mu.Unlock()") {
		t.Error("Screendump holds `mu` for its whole body; `mu` guards the struct's state and a " +
			"five-second wait under it stops every click")
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
