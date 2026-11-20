package run

import (
	"testing"

	"github.com/easydev-org/easyvm/internal/walk"
)

// EVERY CONDITION PRODUCES A BOOLEAN, AND `if:` IS HOW A BOOLEAN CHANGES WHAT RUNS NEXT.
//
// THE OLD MODEL COULD NOT DO THIS. A gate answered by RETURNING from the executor - `until:` nil or
// an error, `unless:` skip-and-continue - and a return value cannot be asked about twice, cannot be
// named in a log line, and cannot be the subject of a branch. An installer with two possible first
// screens therefore had to be written as three steps that each guessed, and the wrong guess was a
// click into whatever was actually on screen.
//
// THE PARTS, EACH ONE JOB:
//
//	ocr:    a region          -> TEXT      work/ocr/<name>.txt
//	check:  a text + a target -> BOOLEAN   e.facts[<name>]
//	if:     a boolean         -> which step list runs
//	unless: a text + a target -> BOOLEAN, and skips the rest of the step
func TestAConditionBindsABooleanAndABranchReadsIt(t *testing.T) {
	d := &fakeDriver{}
	ex, _ := newExec(d)

	// A `check:` binds its answer under the name the file gave it.
	ex.seen = map[string][]string{"screen": {"welcome", "to", "ubuntu", "install"}}
	check := walk.Step{Index: 1, Name: "is the wizard up", Spec: map[string]any{
		"name": "is the wizard up",
		"check": map[string]any{
			"name":  "wizard_up",
			"from":  "screen",
			"mode":  "says_any",
			"words": []any{[]any{"welcome"}, []any{"language"}},
		},
	}}
	if err := ex.doCheck(check); err != nil {
		t.Fatalf("check: %v", err)
	}
	if !ex.facts["wizard_up"] {
		t.Fatal("`check:` did not bind `wizard_up` as true, though the reading holds `welcome`")
	}

	// `if:` runs `then:` and NOT `else:`.
	branch := walk.Step{Index: 2, Name: "close the nag", Spec: map[string]any{
		"name": "close the nag",
		"if":   "wizard_up",
		"then": []any{map[string]any{"click": []any{10, 20}}},
		"else": []any{map[string]any{"key": "esc"}},
	}}
	if err := ex.doIf(branch); err != nil {
		t.Fatalf("if: %v", err)
	}
	if len(d.clicks) != 1 || d.clicks[0] != [2]int{10, 20} {
		t.Fatalf("clicks = %v; `then:` should have run and `else:` should not", d.clicks)
	}
	if len(d.strokes) != 0 {
		t.Fatalf("strokes = %v; `else:` must not run when the boolean is true", d.strokes)
	}

	// AND THE OTHER WAY ROUND, from the same reading with a different target.
	ex.seen["screen"] = []string{"some", "other", "pane"}
	check.Spec["check"].(map[string]any)["name"] = "wizard_up"
	if err := ex.doCheck(check); err != nil {
		t.Fatalf("check: %v", err)
	}
	if ex.facts["wizard_up"] {
		t.Fatal("`check:` bound `wizard_up` true for a reading that holds neither target")
	}
	if err := ex.doIf(branch); err != nil {
		t.Fatalf("if: %v", err)
	}
	if len(d.clicks) != 1 {
		t.Fatalf("clicks = %v; `then:` must not run when the boolean is false", d.clicks)
	}
	if len(d.strokes) != 1 {
		t.Fatalf("strokes = %v; `else:` should have run once", d.strokes)
	}
}

// A BRANCH WITH NO `else:` IS NOT AN ERROR - IT IS THE ORDINARY CASE.
//
// Most uses are "do this if the pane is up"; the pane not being up is what the next step handles, so
// demanding an `else:` would make every file write an empty list to satisfy a validator.
func TestABranchWithNoElseIsAllowed(t *testing.T) {
	d := &fakeDriver{}
	ex, _ := newExec(d)
	ex.facts = map[string]bool{"up": false}
	if err := ex.doIf(walk.Step{Name: "x", Spec: map[string]any{
		"if":   "up",
		"then": []any{map[string]any{"key": "ret"}},
	}}); err != nil {
		t.Fatalf("an `if:` with no `else:` must not fail: %v", err)
	}
	if len(d.strokes) != 0 {
		t.Fatalf("strokes = %v; the false branch has nothing to run", d.strokes)
	}
}

// AN UNKNOWN BOOLEAN IS A REFUSAL, not a silent false.
//
// A MISSPELLED NAME would otherwise take the `else:` branch on every run, which reads as "the pane
// was never up" - the same class of fault as a key with no implementation: it reports success and
// does the wrong thing.
func TestAnUnknownBooleanIsRefused(t *testing.T) {
	ex, _ := newExec(&fakeDriver{})
	for _, spec := range []map[string]any{
		{"name": "if", "if": "nope", "then": []any{map[string]any{"key": "ret"}}},
		{"name": "until", "until_fact": "nope"},
	} {
		step := walk.Step{Name: fmtStr(spec["name"]), Spec: spec}
		var err error
		if spec["if"] != nil {
			err = ex.doIf(step)
		} else {
			err = ex.doUntilFact(step)
		}
		if err == nil {
			t.Fatalf("%v: an unknown boolean name must be refused", spec["name"])
		}
	}
}

func fmtStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return "step"
}

// A `loop:` SUB-STEP CARRIES ITS OWN KEY, AND GETTING THAT WRONG BROKE EVERY LOOP IN THE REPOSITORY.
//
// MEASURED ON THE FIRST LOCAL ubuntu-install, at step 6 of 29:
//
//	wait for the installer window [ocr]: `ocr:` must be a mapping: name, region, region_at, scale
//
// THE FILE WAS RIGHT AND THE EXECUTOR WAS WRONG. `parseOCRStep` reads `s.Spec["ocr"]`, because a
// top-level `ocr:` step is written `ocr: {name: ...}` - so the mapping has to sit UNDER that key. The
// loop built its sub-step by copying the mapping's CONTENTS into the spec, so the parser looked for an
// `ocr` key that was not there. Every `loop:` in every walkthrough failed on its first attempt, and
// the message pointed at the file.
//
// THE ASSERTION IS ON THE ASSEMBLY, which is where the bug was: a loop's parts must be handed to their
// parsers as `{ocr: {...}}` and `{check: {...}}`. Running the whole loop would need a real frame and a
// real tesseract, which would make this a slow test of OCR rather than a fast test of the executor.
func TestALoopAssemblesItsPartsWithTheirKeys(t *testing.T) {
	ocrRaw := map[string]any{
		"name": "screen", "rect": []any{0, 0, 1920, 1080},
		"rect_at": []any{1920, 1080}, "scale": 1, "wait": 0,
	}
	// THE SHAPE THE LOOP MUST BUILD - AND IT IS BUILT BY THE LOOP.
	//
	// This test used to construct the sub-step itself, and so it asserted on its own copy of the
	// shape rather than on the code that runs. When `doLoop` started handing `parseOCRStep` the `see:`
	// mapping under a `see` key, this test still passed and EVERY LOOP IN EVERY WALKTHROUGH failed in
	// a real run - measured on 44078798458, on a migrated `.walk`. A test that rebuilds the thing it
	// is testing cannot see the thing change.
	ocrStep, _, _ := loopParts(walk.Step{Index: 1, Name: "probe"}, "ocr", ocrRaw, nil, nil)
	spec, err := parseOCRStep(ocrStep)
	if err != nil {
		t.Fatalf("a sub-step built as {ocr: {...}} is refused: %v", err)
	}
	if spec.Name != "screen" {
		t.Fatalf("the reading's name came out %q, want screen", spec.Name)
	}

	// AND THE SHAPE THAT WAS WRONG is refused, so a future edit in the same direction fails here.
	bad := walk.Step{Name: "probe [ocr]", Spec: ocrRaw}
	if _, err := parseOCRStep(bad); err == nil {
		t.Fatal("a sub-step passed as the mapping's CONTENTS was accepted; it must be refused, " +
			"because that is the mistake that made every loop in the repository fail with a " +
			"message that pointed at the walkthrough")
	}

	// `check:` IS THE SAME SHAPE.
	checkRaw := map[string]any{
		"name": "done", "from": "screen", "mode": "says",
		"words": []any{"welcome"},
	}
	ex, _ := newExec(&fakeDriver{})
	ex.seen = map[string][]string{"screen": {"welcome", "to", "ubuntu"}}
	checkStep := walk.Step{Name: "probe [check]", Spec: map[string]any{"check": checkRaw}}
	if err := ex.doCheck(checkStep); err != nil {
		t.Fatalf("a sub-step built as {check: {...}} is refused: %v", err)
	}
	if !ex.facts["done"] {
		t.Fatal("the check did not bind its boolean")
	}
}

// A LOOP'S `see:` IS A CAPTURE, AND THE LOOP MUST NOT DEMAND PARAMETERS IT NO LONGER HAS.
//
// `see:` USED TO CARRY `of:` AND A `rect:`, and a loop canonicalised the key to `ocr:` - a different
// vocabulary with different parameters - so every converted file was refused with
//
//	wait for the installer window [ocr]: `ocr:` has unknown key "of"
//
// MEASURED on run 44083524097. The two vocabularies are one now: `see: {name: shot}` captures, and the
// questions live in the loop's `check:`. This test builds the mapping a converted file writes and
// requires the loop to accept it.
func TestALoopReadsSeeAsACapture(t *testing.T) {
	ex, _ := newExec(&fakeDriver{})
	gate := walk.Step{Index: 1, Name: "gate"}

	// THE SHAPE EVERY CONVERTED FILE WRITES.
	see := map[string]any{"name": "screen"}
	reading, _, _ := loopParts(gate, "see", see, nil, nil)
	if _, ok := reading.Spec["see"]; !ok {
		t.Fatal("the reading was not handed over under `see`")
	}
	if err := ex.checkReading(reading); err != nil {
		t.Fatalf("a loop's `see: {name: screen}` was refused: %v", err)
	}

	// AND THE OLD SHAPE IS REFUSED WITH A SENTENCE THAT NAMES WHERE `of:` BELONGS.
	legacy := map[string]any{"name": "screen", "of": []any{"text"},
		"rect": []any{0, 0, 1920, 1080}, "rect_at": []any{1920, 1080}}
	old, _, _ := loopParts(gate, "see", legacy, nil, nil)
	err := ex.checkReading(old)
	if err == nil {
		t.Fatal("a loop's `see:` still carrying `of:` was accepted; its `check:` would then find no " +
			"words and fail pointing at the wrong step")
	}
}
