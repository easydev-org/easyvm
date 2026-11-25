package run

// A GUEST THAT FIXES ITS OWN RESOLUTION MUST STILL BE CLICKABLE.
//
// `doClick` refused every coordinate unless the framebuffer was exactly 1920x1080, and there is no
// qemu argument that changes some guests' minds:
//
//	ReactOS 0.4.16   draws 800x600 whatever the card is told (`VESA Display (800x600x32)`)
//	9front (Plan 9)  `aux/vga` picks 1024x768 from the VESA table and announces it
//
// MEASURED with `-device VGA,xres=1920,yres=1080`: both still drew their own size. For 9front the
// result was a desktop that came up and a walk that could never finish driving it, because the only
// way to make its terminal the current window is a click. That is issue 11.
//
// `click_at: [w, h]` is the declaration that fixes it, and it is the same treatment `rect_at:` gives
// a reading. THE THIRD TEST IS THE ONE THAT KEEPS THE OLD SAFETY: a coordinate nobody declared, on a
// frame nobody expected, is still refused.

import (
	"testing"

	"github.com/easydev-org/easyvm/internal/walk"
)

// sizedFrame is a frame of a chosen size, so a test can be 9front's 1024x768 or ReactOS's 800x600.
type sizedFrame struct{ w, h int }

func (f sizedFrame) Path() string  { return stubFrame{}.Path() }
func (f sizedFrame) Size() (int, int) { return f.w, f.h }
func (f sizedFrame) Words() []string  { return nil }

// sizedDriver answers Frame with a size the test chose.
type sizedDriver struct {
	*fakeDriver
	w, h int
}

func (d *sizedDriver) Frame() (Frame, error) { return sizedFrame{d.w, d.h}, nil }

func newSizedExec(w, h int, spec ...map[string]any) (*Executor, []walk.Step, *sizedDriver) {
	d := &sizedDriver{fakeDriver: &fakeDriver{}, w: w, h: h}
	ex, steps := newExec(d, spec...)
	return ex, steps, d
}

// THE DECLARATION IS WHAT MAKES A SMALLER FRAME CLICKABLE, AND THE POINT IS SCALED.
func TestAClickAtADeclaredSizeIsRescaled(t *testing.T) {
	// 9front: the frame is 1024x768 and the file says so.
	ex, steps, d := newSizedExec(1024, 768, map[string]any{
		"name": "select the terminal", "input": true,
		"click": []any{960, 540}, "click_at": []any{1920, 1080},
	})
	if _, err := ex.Run(steps); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(d.clicks) != 1 {
		t.Fatalf("the click was not sent at all: %v. A guest that draws 1024x768 is exactly the "+
			"case `click_at:` exists for", d.clicks)
	}
	if got := d.clicks[0]; got != [2]int{512, 384} {
		t.Fatalf("clicked %v; (960, 540) measured at 1920x1080 is (512, 384) on a 1024x768 frame",
			got)
	}
}

// AND A POINT THAT ALREADY MATCHES IS SENT UNCHANGED.
func TestAClickAtTheSameSizeIsNotMoved(t *testing.T) {
	ex, steps, d := newSizedExec(1920, 1080, map[string]any{
		"name": "click", "input": true,
		"click": []any{800, 431}, "click_at": []any{1920, 1080},
	})
	if _, err := ex.Run(steps); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(d.clicks) != 1 || d.clicks[0] != [2]int{800, 431} {
		t.Fatalf("clicked %v; a point measured at the frame's own size must not move", d.clicks)
	}
}

// THE OLD SAFETY STAYS: a coordinate NOBODY DECLARED, on a frame nobody expected, is refused.
func TestAnUndeclaredClickOnAnotherSizeIsStillRefused(t *testing.T) {
	ex, steps, d := newSizedExec(1024, 768, map[string]any{
		"name": "click", "input": true,
		"click": []any{800, 431},
	})
	if _, err := ex.Run(steps); err == nil {
		t.Fatalf("a click with no `click_at:` was sent to a 1024x768 guest (%v). That is the "+
			"silent-wrong-place defect the guard was written for, and it must survive the fix",
			d.clicks)
	}
	if len(d.clicks) != 0 {
		t.Fatalf("the refused click was sent anyway: %v", d.clicks)
	}
}

// A DOUBLE-CLICK CARRIES THE SAME DECLARATION, because it is the same question.
func TestADoubleClickAtADeclaredSizeIsRescaled(t *testing.T) {
	ex, steps, d := newSizedExec(1024, 768, map[string]any{
		"name": "open it", "input": true,
		"click": []any{960, 540}, "double": []any{960, 540}, "click_at": []any{1920, 1080},
	})
	if _, err := ex.Run(steps); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(d.doubles) != 1 || d.doubles[0] != [2]int{512, 384} {
		t.Fatalf("double-clicked %v; it must be scaled like a single click", d.doubles)
	}
}

// AND THE CLICK BEFORE TYPING TAKES IT TOO - a form field at a 1920x1080 coordinate is at another
// place on a smaller frame, and typing into the wrong place is the failure this path exists to avoid.
func TestTheClickBeforeTypingIsRescaled(t *testing.T) {
	ex, steps, d := newSizedExec(1024, 768, map[string]any{
		"name": "type the name", "input": true,
		"at": []any{960, 540}, "text": "vmuser", "click_at": []any{1920, 1080},
	})
	if _, err := ex.Run(steps); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(d.clicks) != 1 || d.clicks[0] != [2]int{512, 384} {
		t.Fatalf("clicked %v before typing; it must be scaled like any other click", d.clicks)
	}
	if len(d.typed) != 1 || d.typed[0] != "vmuser" {
		t.Fatalf("typed %q; the text must still go in", d.typed)
	}
}

var _ = walk.Step{}