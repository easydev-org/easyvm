package run

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/easydev-org/easyvm/internal/walk"
)

// `must: true` IS THE WORD THAT MAKES A `check:` A VERDICT RATHER THAN A BINDING.
//
// MEASURED ON RUN 44280999011, which reported `status: finished step 44/44` and OVERALL SUCCESS while
// its own log said:
//
//	the screen is the one this file was written against: matched = false
//	(0.6781 similar, wanted 0.9700..1.0000)
//
// THE FILE SAID WHAT THE SCREEN MUST BE, THE SCREEN WAS NOT THAT, AND THE RUN PASSED. A `check:` binds
// a boolean for an `if:` or a `loop: until:` to read, and when nothing reads it the binding is a
// sentence nobody hears.
func TestMustMakesAFalseReferenceComparisonFailTheStep(t *testing.T) {
	ref := filepath.Join("..", "..", "linux", "ubuntu", "steps", "reference", "desktop.png")
	other := "/tmp/ref/macos.png"
	if !fileExists(ref) || !fileExists(other) {
		t.Skip("reference images are not here")
	}

	base := func(must bool) map[string]any {
		ck := map[string]any{
			"name": "matched", "source": "shot", "mode": "like",
			"rect": []any{0, 0, 1920, 1080}, "rect_at": []any{1920, 1080},
			"reference": "reference/desktop.png", "range": []any{0.97, 1.0},
		}
		if must {
			ck["must"] = true
		}
		return map[string]any{"check": ck}
	}
	newEx := func(frame string) *Executor {
		return &Executor{
			Driver: &fakeDriver{},
			File:   &walk.File{Path: filepath.Join("..", "..", "linux", "ubuntu", "steps", "install.walk")},
			facts:  map[string]bool{},
			shots:  map[string]string{"shot": frame},
		}
	}

	t.Run("must: true and a matching frame passes", func(t *testing.T) {
		ex := newEx(ref)
		if err := ex.doCheck(walk.Step{Name: "same", Spec: base(true)}); err != nil {
			t.Fatalf("the frame IS the reference: %v", err)
		}
		if !ex.facts["matched"] {
			t.Fatal("the comparison bound false against its own reference")
		}
	})

	t.Run("must: true and another screen FAILS the step", func(t *testing.T) {
		ex := newEx(other)
		err := ex.doCheck(walk.Step{Name: "same", Spec: base(true)})
		if err == nil {
			t.Fatal("a screen 98% different from the reference was accepted with `must: true`. This " +
				"is the defect run 44280999011 shipped: the log said matched = false and the walk " +
				"reported 44/44")
		}
		if !strings.Contains(err.Error(), "must be true") {
			t.Fatalf("the refusal does not say what was required: %v", err)
		}
	})

	t.Run("without must the answer is bound and the step passes", func(t *testing.T) {
		ex := newEx(other)
		if err := ex.doCheck(walk.Step{Name: "same", Spec: base(false)}); err != nil {
			t.Fatalf("a check with no `must:` is a BINDING - an `if:` reads it, and a false answer "+
				"is information rather than a verdict: %v", err)
		}
		if ex.facts["matched"] {
			t.Fatal("the boolean did not record the answer")
		}
	})
}
