package run

import "testing"

// A `check:` MAY DECIDE ON A NUMBER THAT A `see:` STEP BOUND, AND UNTIL THIS TEST NOTHING COULD.
//
// `see:` binds seven numbers per reading - `<name>.mean`, `<name>.similarity` and so on - and
// `e.numbers` was written by `see:` and READ BY NOTHING. The modes that take a number parsed digits
// out of a WORD LIST instead, so a file could only compare a number that an `ocr:` step happened to
// read as text.
//
// MEASURED on run 44264717738, step 6 of 44:
//
//	the screen is the one this file was written against:
//	  `check: from: shot` names a reading no `ocr:` step has made
//
// The reading existed. It was a number, and `check:` looked only in `e.seen`.
func TestCheckCanDecideOnASeeNumber(t *testing.T) {
	check := func(name, from, mode string, rng []any) map[string]any {
		m := map[string]any{"name": name, "source": from, "mode": mode}
		if rng != nil {
			m["range"] = rng
		}
		return map[string]any{"check": m}
	}

	t.Run("a bare name finds the one number it bound", func(t *testing.T) {
		d := &fakeDriver{}
		ex, steps := newExec(d, check("same", "shot", "number_between", []any{0.97, 1.0}))
		ex.numbers = map[string]float64{"shot.similarity": 0.9938}
		ex.facts = map[string]bool{}
		if err := ex.doCheck(steps[0]); err != nil {
			t.Fatalf("`from: shot` with one number bound: %v", err)
		}
		if !ex.facts["same"] {
			t.Fatal("0.9938 was not accepted by [0.97, 1.0]")
		}
	})

	t.Run("a value outside the range binds false rather than failing", func(t *testing.T) {
		d := &fakeDriver{}
		ex, steps := newExec(d, check("same", "shot", "number_between", []any{0.97, 1.0}))
		ex.numbers = map[string]float64{"shot.similarity": 0.0131}
		ex.facts = map[string]bool{}
		if err := ex.doCheck(steps[0]); err != nil {
			t.Fatalf("a false verdict is not an error: %v", err)
		}
		if ex.facts["same"] {
			t.Fatal("0.0131 was accepted by [0.97, 1.0], which is a different screen passing as the " +
				"reference")
		}
	})

	t.Run("the qualified name works too", func(t *testing.T) {
		d := &fakeDriver{}
		ex, steps := newExec(d, check("diff", "shot.similarity", "number_between", []any{0.0, 0.5}))
		ex.numbers = map[string]float64{"shot.similarity": 0.0131}
		ex.facts = map[string]bool{}
		if err := ex.doCheck(steps[0]); err != nil {
			t.Fatal(err)
		}
		if !ex.facts["diff"] {
			t.Fatal("0.0131 was not accepted by [0, 0.5]")
		}
	})

	t.Run("an ambiguous bare name is refused by name", func(t *testing.T) {
		d := &fakeDriver{}
		ex, steps := newExec(d, check("x", "shot", "number_between", []any{0.0, 1.0}))
		ex.numbers = map[string]float64{"shot.mean": 40, "shot.spread": 12}
		ex.facts = map[string]bool{}
		err := ex.doCheck(steps[0])
		if err == nil {
			t.Fatal("`from: shot` with two numbers bound was accepted; which one would it compare?")
		}
	})

	t.Run("a word mode on a number is refused with the modes that work", func(t *testing.T) {
		d := &fakeDriver{}
		ex, steps := newExec(d, map[string]any{"check": map[string]any{
			"name": "bad", "source": "shot", "mode": "says_any", "words": []any{"x"}}})
		ex.numbers = map[string]float64{"shot.similarity": 0.9}
		ex.facts = map[string]bool{}
		if err := ex.doCheck(steps[0]); err == nil {
			t.Fatal("`mode: says_any` against a NUMBER was accepted; there are no words to compare")
		}
	})
}
