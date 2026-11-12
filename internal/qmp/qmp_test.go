package qmp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// `input-send-event` DOES NOT TAKE PIXELS, AND A CLICK SPLIT ACROSS REQUESTS LANDS WHERE THE POINTER WAS.
//
// MEASURED ON popos-install, AND THE RECORDING IS THE PROOF: the installer's Select button was pressed
// four times across 250 seconds, and the video is BYTE-IDENTICAL from the 90th second to the 340th.
// The pointer never moved; every press arrived at the position the guest still held, which was (0,0).
//
// THREE THINGS WERE WRONG, AND THE REFERENCE IMPLEMENTATION HAD ALL THREE RIGHT:
//
//	1  an `abs` axis value is in the DEVICE'S RANGE (0..32767), not in pixels. A pixel value in a
//	   1920-wide frame is six percent of the way across, which is the left edge whatever was asked for.
//	2  THE POSITION AND THE PRESS MUST BE ONE EVENT LIST. Three requests leave the guest free to handle
//	   the button before it applied the coordinates - "a click delivered at the OLD pointer position is
//	   what the frames show: the dock redrawing around the cursor and no launch".
//	3  THE RELEASE MUST WAIT. A widget that polls needs the button DOWN for at least one poll; sent
//	   back to back, the guest reads up/up and opens nothing.
func TestAClickScalesAndPressesInOneList(t *testing.T) {
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
	if !strings.Contains(code, "toAxis(x, w)") {
		t.Fatal("`Click` sends the pixel coordinate without converting it to the axis range, so the " +
			"pointer lands near the left edge whatever was asked for")
	}
	// AND THE PRESS IS IN THE SAME LIST AS THE MOVE.
	if !strings.Contains(code, "InputEventBtn") {
		t.Fatal("`Click` does not press inside the same `input-send-event` as the move, so the guest " +
			"can handle the button before it applied the coordinates")
	}
	// THE MOVE+PRESS IS ONE CALL IN `Click`; THE RELEASE IS `c.button(...)`, ITS OWN REQUEST.
	if n := strings.Count(code, "InputSendEvent"); n != 1 {
		t.Fatalf("`Click` makes %d `input-send-event` calls for the move-and-press; it must be exactly "+
			"one, with the release in its own request afterwards", n)
	}
	if !strings.Contains(code, "c.button(") {
		t.Fatal("`Click` never releases the button")
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
