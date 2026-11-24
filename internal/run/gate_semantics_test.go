package run

import "testing"

// A NESTED SET IN `when_not_any` IS ONE CONDITION, AND ITS WORDS ARE AND.
//
// This exists because the difference is what makes zorin's gate correct and a careless rewrite of it
// wrong. The step is:
//
//	step "updates and other software, when this build shows it"
//	  input 1361 764
//	  settle 18
//	  when_not_any [[installation, type]]
//
// `[[installation, type]]` is a LIST OF ONE SET of two words, so the gate closes only when BOTH are
// on screen - which is the NEXT page's title, `Installation type`. It is deliberately not
// `[[installation], [type]]`, because the updater page this step is about says
// `Download updates while installing Zorin OS`, and its own OCR read contains the single word
// `installation`. A gate of `[installation]` alone would therefore skip the click on the very page
// the step exists to advance.
//
// MEASURED on the zorin install that passed (run 45397305215), t=300s of its recording: the page is
// `Updates and other software`, and tesseract's whole-frame word list contains `installation` and
// does NOT contain `type`. So this gate is false there and the click goes out, which is right.
func TestANestedSetInWhenNotAnyIsOneANDCondition(t *testing.T) {
	// ONE SET OF TWO WORDS: satisfied only when both are present.
	sets := setsOfAny([]any{[]any{"installation", "type"}})
	if len(sets) != 1 {
		t.Fatalf("`[[installation, type]]` read as %d set(s); it is ONE set", len(sets))
	}
	if got := says([]string{"installation", "updates", "and", "other", "software"}, sets[0]); got {
		t.Fatalf("the updater page's own words satisfied `[[installation, type]]`. That page says " +
			"`installation` in `while installing Zorin OS`, so a gate that accepted it would skip " +
			"the Continue click and leave the installer on the page it was meant to leave")
	}
	if got := says([]string{"installation", "type"}, sets[0]); !got {
		t.Fatalf("`Installation type` did not satisfy `[[installation, type]]`; the next page's own " +
			"title must close the gate, or the step clicks Continue on a page that has no Continue")
	}
	// AND THE ALTERNATIVES FORM IS THE OTHER THING, so the two spellings are not interchangeable.
	alts := setsOfAny([]any{[]any{"installation"}, []any{"type"}})
	if len(alts) != 2 {
		t.Fatalf("`[[installation], [type]]` read as %d set(s); it is TWO alternatives", len(alts))
	}
	if !says([]string{"installation"}, alts[0]) {
		t.Fatalf("the alternatives form must be satisfied by either word alone - that is what makes " +
			"it a different question from the AND form")
	}
}