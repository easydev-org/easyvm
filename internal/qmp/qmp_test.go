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
