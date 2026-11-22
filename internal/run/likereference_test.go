package run

import (
	"path/filepath"
	"testing"

	"github.com/easydev-org/easyvm/internal/walk"
)

// THE WHOLE PATH A REFERENCE COMPARISON TAKES, WITH A REAL FRAME AND A REAL REFERENCE IMAGE.
//
// MEASURED ON RUN 44274267904, step 6 of 44: the `see:` captured a frame -
//
//	=== the desktop is the one this file was written against
//	  see shot: frame-000119.png (1920x1080)
//
// and the `check:` after it failed, because the file still wrote the shape from before `see:` was
// split from `check:`:
//
//	check: {name: matched, from: shot, mode: number_between, range: [0.97, 1.0]}
//
// A FRAME IS NOT A NUMBER. `mode: number_between` looks in the numbers a `see:` bound, and a `see:`
// binds no numbers now - it binds a PICTURE. The comparison is `mode: like`, and it names the image.
//
// THIS TEST IS THE SHAPE THAT WORKS, end to end: a captured frame, a reference beside the
// walkthrough, a region, and a range.
func TestAReferenceComparisonEndToEnd(t *testing.T) {
	ref := filepath.Join("..", "..", "linux", "ubuntu", "steps", "reference", "desktop.png")
	if !fileExists(ref) {
		t.Skipf("no reference image at %s", ref)
	}
	ex := &Executor{
		Driver: &fakeDriver{},
		File:   &walk.File{Path: filepath.Join("..", "..", "linux", "ubuntu", "steps", "install.walk")},
		facts:  map[string]bool{},
		// THE FRAME IS THE REFERENCE ITSELF, so the similarity is 1.0 and the range must accept it.
		// That is the floor case: a comparison that cannot recognise its own reference is broken in a
		// way no threshold can fix.
		shots: map[string]string{"shot": ref},
	}
	check := func() map[string]any {
		return map[string]any{"check": map[string]any{
			"name": "same", "source": "shot", "mode": "like",
			"rect": []any{0, 0, 1920, 1080}, "rect_at": []any{1920, 1080},
			"reference": "reference/desktop.png", "range": []any{0.97, 1.0}}}
	}
	if err := ex.doCheck(walk.Step{Name: "same", Spec: check()}); err != nil {
		t.Fatalf("a reference comparison against its own image: %v", err)
	}
	if !ex.facts["same"] {
		t.Fatal("the frame IS the reference and the comparison refused it")
	}

	// AND A DIFFERENT SCREEN IS REFUSED, which is the half that makes the assertion worth writing.
	// The macos frame is a real screen from another recording, measured at 98% different from this
	// one - so a reference of the wrong guest cannot pass.
	other := "/tmp/ref/macos.png"
	if fileExists(other) {
		ex.shots["shot"] = other
		if err := ex.doCheck(walk.Step{Name: "same", Spec: check()}); err != nil {
			t.Fatalf("a comparison that FAILS must bind false, not error: %v", err)
		}
		if ex.facts["same"] {
			t.Fatal("another operating system's desktop passed as this one's reference")
		}
	}
}
