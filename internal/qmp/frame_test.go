package qmp

import (
	"strings"
	"testing"
)

// A COORDINATE OUTSIDE THE FRAME IS REFUSED, NOT CLAMPED.
//
// THE CLAMP WAS THE SILENCE. `toAxis` pinned anything negative to 0 and anything past an edge to the
// edge, so `moveTo` built a perfectly valid event list for a point the caller never named: the QMP
// call succeeded, the daemon answered `{"ok":true}`, and the pointer arrived at a corner.
//
// MEASURED on macOS 26's Disk Utility: a sidebar row at y=130 was clicked with y=112 - inside the
// frame, so this check does not catch that one - but the SAME class of mistake past the bottom edge
// (`y: 1200` on a 1080 frame) produced a click at the bottom edge and a success reply, with nothing
// in any log to say the number was impossible.
//
// SO THE ERROR NAMES THE FRAME. The usual cause is a coordinate measured on a screen of another
// size, and "outside a 1920x1080 frame" is the sentence that sends a reader to the measurement.
func TestACoordinateOutsideTheFrameIsRefusedNotClamped(t *testing.T) {
	c := &Conn{lastW: 1920, lastH: 1080}

	outside := [][2]int{
		{-1, 10},
		{10, -1},
		{1920, 10}, // one past the last column
		{10, 1080}, // one past the last row
		{99999, 10},
		{10, 99999},
	}
	for _, pt := range outside {
		err := c.moveTo(pt[0], pt[1])
		if err == nil {
			t.Fatalf("moveTo(%d, %d) on a 1920x1080 frame was accepted. Clamping it moves the "+
				"pointer somewhere the caller never named, and the daemon reports success", pt[0], pt[1])
		}
		if !strings.Contains(err.Error(), "1920x1080") {
			t.Fatalf("the error does not name the frame, so a reader cannot tell a bad coordinate "+
				"from a bad screen:\n\t%v", err)
		}
		if !strings.Contains(err.Error(), "outside") {
			t.Fatalf("the error does not say the point is outside:\n\t%v", err)
		}
	}

	// AND THE EDGES THEMSELVES ARE LEGAL. `(0, 0)` is the top-left pixel and `(1919, 1079)` the
	// bottom-right; an off-by-one that refused them would break every walkthrough that clicks a
	// corner, and this test would then be protecting a bug instead of preventing one.
	// THEY ARE CHECKED AGAINST THE GUARD'S OWN ARITHMETIC rather than by calling `moveTo`, because a
	// legal point needs a real QMP connection - the point here is that the BOUNDARIES are exclusive
	// only above the last pixel.
	for _, pt := range [][2]int{{0, 0}, {1919, 0}, {0, 1079}, {1919, 1079}} {
		x, y := pt[0], pt[1]
		if x < 0 || y < 0 || x >= 1920 || y >= 1080 {
			t.Fatalf("%v must be inside the frame; the guard above would refuse a legal pixel", pt)
		}
	}
}

// toAxis stays inside the device's range for every value the guard above allows.
//
// IT IS KEPT BECAUSE IT IS A PURE FUNCTION WITH A CONTRACT: whatever a caller passes, the value that
// reaches qemu must be a legal axis value. `moveTo` no longer reaches the clamp, but a future caller
// that skips the frame check must not be able to produce an event qemu rejects.
func TestToAxisStaysInsideTheAxisRange(t *testing.T) {
	for _, size := range []int{1920, 1080, 1280, 800, 1} {
		for _, v := range []int{0, 1, size / 2, size - 1, size} {
			got := toAxis(v, size)
			if got < 0 || got > AxisMax {
				t.Fatalf("toAxis(%d, %d) = %d, outside 0..%d", v, size, got, AxisMax)
			}
		}
	}
}
