package run

import (
	"os"
	"strings"
	"testing"
)

// `unless:` SHUTS THE GATE, AND IT USED TO ONLY SAY SO.
//
// MEASURED ON THE macOS 26 ssh STAGE, and it cost that stage a whole run. The step was
//
//   - name: open the remote login pane from the keyboard
//     unless: [allow, full, disk, access]
//     region: [...]
//     do:
//   - key: meta_l+f
//   - key: ret
//
// The panel was already open, so the reading held the words and the gate was shut. The log said
//
//	skipped: the screen already shows "allow full disk access"
//
// AND THEN THE STEP PRESSED Cmd+F AND RETURN ANYWAY, which closed the panel. The next step read
// `screen: 0 word(s)` - a blank screen - and the stage failed there. Three later fixes were aimed at
// the search box, the window position and the result row, all because that log line said the step had
// been skipped.
//
// THE CAUSE WAS THAT `doUnless` RETURNED `nil` FOR BOTH OUTCOMES: "skipped" and "go ahead" were the
// same value, so `dispatchRest` could not tell them apart and ran the action on either.
//
// THIS IS A SOURCE CHECK RATHER THAN A RUN, which is the same instrument `repeat_test.go` uses for the
// `unless_any` gate: running it needs a real PNG for `readScreen`, and what has to be pinned is that the
// two outcomes are DIFFERENT VALUES, which is visible in the function's own body.
func TestUnlessThatFindsItsWordsReturnsASkipTheDispatcherHonours(t *testing.T) {
	body, err := os.ReadFile("text.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	i := strings.Index(src, "func (e *Executor) doUnless(")
	if i < 0 {
		t.Fatal("no doUnless")
	}
	rest := src[i:]
	if end := strings.Index(rest[1:], "\nfunc "); end > 0 {
		rest = rest[:end+1]
	}

	// 1. THE SHUT GATE RETURNS SOMETHING THAT IS NOT nil.
	if !strings.Contains(rest, "return errUnlessAlready") {
		t.Fatal("`doUnless` returns nil when the words are on the screen, so `dispatchRest` cannot " +
			"tell a shut gate from an open one and runs the action anyway. THAT IS THE DEFECT: the " +
			"step logged \"skipped\" and then pressed Cmd+F and Return, closing the panel the stage " +
			"had just opened. The shut gate must return errUnlessAlready")
	}
	// 2. AND THE OPEN GATE STILL RETURNS nil, or every `unless` in the repository stops working.
	if !strings.Contains(rest, "\treturn nil\n}") {
		t.Fatal("`doUnless` has no `return nil` for the open gate, so the action never runs")
	}
	// 3. THE SENTINEL EXISTS AND IS AN ERROR, because the signature says error.
	var sentinel bool
	for _, decl := range []string{
		"var errUnlessAlready = errors.New(",
	} {
		if strings.Contains(src, decl) {
			sentinel = true
		}
	}
	if !sentinel {
		t.Fatal("`errUnlessAlready` is not declared with errors.New, so errors.Is cannot match it")
	}
}

// AND THE DISPATCHER TURNS THAT SENTINEL INTO A PLAIN RETURN RATHER THAN A FAILED STAGE.
//
// A skip is not a failure: the step asked to act only when something was absent, and it was present.
// Reading `errUnlessAlready` as an error would fail the stage on every correctly-skipped step.
func TestTheDispatcherReadsTheUnlessSentinelAsASkip(t *testing.T) {
	body, err := os.ReadFile("executor.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	// THE ARM IS SPELLED `when_not`, which is what `unless:` compiles to in the vocabulary. The
	// test looks for the arm that HANDLES it rather than for the word "unless", because the two are
	// deliberately different: `unless:` is the user-facing key and `when_not:` is the gate.
	i := strings.Index(src, `case s.Has("when_not"), s.Has("when_not_any"):`)
	if i < 0 {
		t.Fatal("the dispatcher has no `unless`/`when_not` arm")
	}
	rest := src[i:]
	if end := strings.Index(rest, "case s.Has(\"until\")"); end > 0 {
		rest = rest[:end]
	}
	if !strings.Contains(rest, "errors.Is(err, errUnlessAlready)") {
		t.Fatal("the dispatcher does not check for errUnlessAlready, so a skipped step becomes a " +
			"failed stage or runs its action - both were measured")
	}
	if !strings.Contains(rest, "return nil") {
		t.Fatal("the dispatcher does not return nil for the skip, so a correctly-skipped step fails")
	}
}
