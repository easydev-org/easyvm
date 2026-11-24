package run

// A GATE ON AN ACTION STEP MUST BE EVALUATED BEFORE THE ACTION RUNS.
//
// `runOne` dispatches on the FIRST key it recognises, in a fixed order that has nothing to do with
// what the file meant:
//
//	case s.Has("input"):   -> doInput      (line ~381)
//	case s.Has("when"):    -> doWhen       (line ~464)
//
// So a step written as an action with a gate -
//
//	item "close it, only while Settings is the window in front"
//	  input alt+f4
//	  when [Settings, Keyboard]
//
// - reaches `doInput`, which sends the keystroke and returns. `doWhen` is never called, the gate is
// never read, and the action runs UNCONDITIONALLY.
//
// MEASURED on fedora-workstation install, run 45397314773, at the step that closes the GNOME
// Settings window anaconda's `Next` raises:
//
//	=== close the settings window that Next raises
//	  key alt+f4                       <- pressed at t=291s
//	  see: 51 word(s): () Fedora Linux 45 (Workstation Edition) installation : 1 Welcome Destination ...
//
// NO `applies:` LINE AND NO `skipped:` LINE. The frame that was read is the INSTALLER - the thing the
// gate was written to protect - and the key went out anyway. `alt+f4` closed anaconda, the desktop was
// bare from t=294, and `the editor is up` then read "" for ten minutes and failed the leg.
//
// THE SAME SHAPE IS IN THE WINDOWS WALKS, where the consequence is worse: `input esc` gated on
// `when [sure]` is a guard against CANCELLING an install in progress. Ungated, it presses `esc` into
// whatever the page focuses, which is the Cancel button the guard exists to avoid.
//
// These tests are written against the DISPATCHER rather than any one walk, because the walks are
// right and the engine is what drops the gate.

import (
	"testing"

	"github.com/easydev-org/easyvm/internal/walk"
)

// A GATED KEYSTROKE MUST NOT BE SENT WHEN THE GATE'S WORDS ARE ABSENT.
func TestAGatedKeystrokeIsSkippedWhenTheScreenDoesNotSayIt(t *testing.T) {
	d := &fakeDriver{}
	// The stub frame answers OCR with no words at all, so `when: [Settings, Keyboard]` cannot hold.
	ex, steps := newExec(d, map[string]any{
		"name": "close the settings window",
		"input": true, "key": "alt+f4",
		"when": []any{"Settings", "Keyboard"},
		// A readable rect, because `when:` reads a frame and a reading with no rect is an error
		// rather than a skip.
		"rect":    []any{0, 0, 100, 100},
		"rect_at": []any{1920, 1080},
		"scale":   1,
	})
	if _, err := ex.Run(steps); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(d.strokes) != 0 {
		t.Fatalf("the keystroke was sent %d time(s) on a screen whose words do not include the "+
			"gate's: %v. `runOne` reaches `case s.Has(\"input\")` before `case s.Has(\"when\")`, so a "+
			"gate written on an action step is DROPPED and the action runs unconditionally. On "+
			"fedora-workstation that sent `alt+f4` into anaconda and closed the installer",
			len(d.strokes), d.strokes)
	}
}

// AND A GATED CLICK MUST NOT BE CLICKED WHEN THE GATE'S WORDS ARE ABSENT - the shape the Windows
// walks use for the quit dialog, where the click is the one thing that cannot be redirected.
func TestAGatedClickIsSkippedWhenTheScreenDoesNotSayIt(t *testing.T) {
	d := &fakeDriver{}
	ex, steps := newExec(d, map[string]any{
		"name": "dismiss the quit dialog",
		"input": true, "click": []any{1035, 620},
		"when":    []any{"sure"},
		"rect":    []any{820, 460, 1520, 660},
		"rect_at": []any{1920, 1080},
		"scale":   2,
	})
	if _, err := ex.Run(steps); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(d.clicks) != 0 {
		t.Fatalf("the click was sent %d time(s) on a screen that does not say the gate's word: %v. "+
			"A gate on a click step must be read first; the Windows walks gate this exact click on "+
			"`when [sure]` so it cannot land on an install in progress",
			len(d.clicks), d.clicks)
	}
}

var _ = walk.Step{}
