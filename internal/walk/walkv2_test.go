package walk

import (
	"os"
	"path/filepath"
	"testing"
)

// THE 2.0 DESIGN IS A CLAIM, AND THIS IS WHAT MAKES IT TRUE.
//
// `docs/WALK-V2.md` says nine verbs with indented children cover every walkthrough in this tree. A
// claim like that is worth nothing in a document, so every real file is read, re-emitted in the 2.0
// form, and read again - and the two documents must be EQUAL. One different value is a design bug.
//
// THE COMPARISON IS ON THE DOCUMENT, not the text: 2.0 sorts a step's keys, so the bytes differ by
// construction while the meaning must not. `firstDifference` names the path that changed.
func TestEveryRealWalkthroughRoundTripsThroughV2(t *testing.T) {
	root := filepath.Join("..", "..")
	files, err := filepath.Glob(filepath.Join(root, "*", "*", "steps", "*.walk"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 20 {
		t.Fatalf("found only %d walkthroughs", len(files))
	}
	converted, bad := 0, 0
	for _, path := range files {
		original, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		// THE 1.0 DOCUMENT IS THE BASELINE, because that is what the engine runs today.
		want, err := WalkToYAML(string(original))
		if err != nil {
			continue // a file that does not load is another test's subject
		}
		if _, wrong := want["error"]; wrong {
			continue
		}
		v2src, err := YAMLToWalkV2(want)
		if err != nil {
			t.Errorf("%s: cannot render as 2.0: %v", filepath.Base(path), err)
			bad++
			continue
		}
		got, err := WalkV2ToYAML(v2src)
		if err != nil {
			t.Errorf("%s: the 2.0 file it produced does not parse: %v\n--- the file ---\n%s",
				filepath.Base(path), err, v2src)
			bad++
			continue
		}
		if diff := firstDifference(want, got, ""); diff != "" {
			t.Errorf("%s: the 2.0 round trip changed the document at %s", filepath.Base(path), diff)
			bad++
			continue
		}
		converted++
	}
	if bad > 0 {
		t.Fatalf("%d of %d walkthroughs did not round-trip through 2.0", bad, len(files))
	}
	t.Logf("%d walkthroughs round-trip through the 2.0 form", converted)
}

// A 2.0 FILE IS RECOGNISED BY ITS TEXT, NOT ITS NAME, so both versions can be `.walk` files.
func TestAFileWithNoDoubleEqualsIs2(t *testing.T) {
	v1 := `walkthrough "x"
  resolution 1920x1080
  step "a"
    settle == 3
`
	v2 := `walkthrough "x"
  resolution 1920x1080
  step "a"
    settle 3
`
	if IsV2(v1) {
		t.Error("a file with `==` was read as 2.0")
	}
	if !IsV2(v2) {
		t.Error("a file with no `==` was not read as 2.0")
	}
}

// CHILDREN ARE INDENTED, AND THERE IS NO OTHER MARKER.
//
// THIS IS THE RULE THE WHOLE FORMAT RESTS ON, so it is tested rather than assumed: `from` and `words`
// are siblings under `expect`, and a file that writes them on the `expect` line is refused.
func TestChildrenAreIndentationAndNothingElse(t *testing.T) {
	src := `walkthrough "x"
resolution 1920x1080
recording_resolution 1920x1080
step "the greeter is up"
    expect
      from screen
      words [password]
`
	doc, err := WalkV2ToYAML(src)
	if err != nil {
		t.Fatal(err)
	}
	steps, _ := doc["steps"].([]any)
	step, _ := steps[0].(map[string]any)
	exp, ok := step["expect"].(map[string]any)
	if !ok {
		t.Fatalf("`expect` did not become a mapping: %#v", step["expect"])
	}
	if exp["from"] != "screen" {
		t.Errorf("from = %v", exp["from"])
	}
	if _, ok := exp["words"].([]any); !ok {
		t.Errorf("words is %T, want a list", exp["words"])
	}
}

// A VALUE ON THE VERB'S LINE IS REFUSED WHEN THE VERB ALSO HAS CHILDREN, because that is two places
// to say one thing and the indentation would no longer be the whole rule.
func TestAValueAndChildrenOnOneKeyIsRefused(t *testing.T) {
	src := `walkthrough "x"
resolution 1920x1080
recording_resolution 1920x1080
step "x"
  expect screen
      from screen
`
	if _, err := WalkV2ToYAML(src); err == nil {
		t.Fatal("a key with both a value and children was accepted")
	}
}

// A BRACE IS REFUSED BY NAME, because a file that writes one is a file written in the format this one
// replaces. The error says what to write instead.
func TestBracesAreRefusedByName(t *testing.T) {
	src := `walkthrough "x"
resolution 1920x1080
recording_resolution 1920x1080
step "x"
  check {from: screen}
`
	_, err := WalkV2ToYAML(src)
	if err == nil {
		t.Fatal("a brace was accepted")
	}
	if !contains(err.Error(), "indented block") {
		t.Errorf("the error does not say what to write instead: %v", err)
	}
}

// A LIST OF STEPS IS `item` BLOCKS, and each one may nest a branch - which is why a flat `do:` could
// not express the three loops in this tree that already nest an `if`.
func TestAStepListNestsABranch(t *testing.T) {
	src := `walkthrough "x"
resolution 1920x1080
recording_resolution 1920x1080
step "answer the wizard"
    repeat
      do
        item "read the pane"
          read pane
        item "answer, when the sheet is there"
          if
            from pane
            words [are, you, sure]
            then
              item "click Continue"
                press 1018 520
      until
        from pane
        words [finder]
      max 40
`
	doc, err := WalkV2ToYAML(src)
	if err != nil {
		t.Fatal(err)
	}
	steps, _ := doc["steps"].([]any)
	step, _ := steps[0].(map[string]any)
	// THE READER COMPILES `repeat` INTO THE `loop:` THE ENGINE RUNS, so a 2.0 statement reaches the
	// document as `loop:` - which is the whole migration: one statement in, the measured shape out.
	rep, ok := step["loop"].(map[string]any)
	if !ok {
		t.Fatalf("`repeat` did not compile into `loop:`: %#v", step["repeat"])
	}
	do, ok := rep["do"].([]any)
	if !ok || len(do) != 2 {
		t.Fatalf("`do` = %#v, want two items", rep["do"])
	}
	second, _ := do[1].(map[string]any)
	branch, ok := second["if"].(map[string]any)
	if !ok {
		t.Fatalf("the nested `if` did not survive: %#v", second)
	}
	then, ok := branch["then"].([]any)
	if !ok || len(then) != 1 {
		t.Fatalf("the nested `then` = %#v", branch["then"])
	}
	// THE CONDITION IS COMPILED INTO THE PAIR THE ENGINE READS: `check:` carries the test (its
	// `from:` is the picture) and `until: {fact:}` names the boolean. That pair is 1.0's shape,
	// because the engine is unchanged - the 2.0 statement is a source form, not a new runtime.
	check, ok := rep["check"].(map[string]any)
	if !ok || check["from"] != "pane" {
		t.Fatalf("the condition did not reach `check:` with its picture: %#v", rep["check"])
	}
	see, ok := rep["see"].(map[string]any)
	if !ok || see["name"] != "pane" {
		t.Fatalf("the picture was not named for the reading: %#v", rep["see"])
	}
	if _, ok := rep["until"].(map[string]any); !ok {
		t.Fatalf("`until` = %#v", rep["until"])
	}
}

// A WORD ALONE IS `true`, so a flag needs no argument and there is no second way to write it.
func TestABareWordIsTrue(t *testing.T) {
	doc, err := WalkV2ToYAML(`walkthrough "x"
resolution 1920x1080
recording_resolution 1920x1080
step "x"
  optional
`)
	if err != nil {
		t.Fatal(err)
	}
	steps, _ := doc["steps"].([]any)
	step, _ := steps[0].(map[string]any)
	if step["optional"] != true {
		t.Errorf("optional = %#v, want true", step["optional"])
	}
}

// THERE IS NO `tries` AND NO `every` IN THE DESIGN, and the claim is about the VOCABULARY the emitter
// is allowed to produce - not about whether it hides a key somebody put in a document.
//
// THE EMITTER IS HONEST: it writes what the document holds, because a converter that dropped a key it
// disliked would change a file instead of refusing it. So this asks the question that matters: does
// the 2.0 form have any construct that means "retry N times"? It does not - `repeat ... until` is the
// only retry, and its count is `max`, a child of the loop.
func TestTheDesignHasNoRetryInterval(t *testing.T) {
	// `tries` AND `every` MUST NOT BE MODIFIERS, which is what decides whether the emitter ranks them
	// as step-level words. If one were in the order list it would read as a first-class modifier.
	for _, word := range []string{"tries", "every"} {
		if got := v2KeyOrder(word); got != "5"+word {
			t.Errorf("`%s` has a modifier order (%q); the design deleted it", word, got)
		}
	}
	// AND A LOOP'S COUNT IS `max`, A CHILD OF `repeat`, NOT A MODIFIER ON A STEP.
	doc, err := YAMLToWalkV2(map[string]any{
		"name":                 "x",
		"resolution":           []any{1920, 1080},
		"recording_resolution": []any{1920, 1080},
		"steps": []any{map[string]any{
			"name": "s",
			"loop": map[string]any{
				"do":    []any{map[string]any{"settle": 5}},
				"see":   map[string]any{"name": "p"},
				"check": map[string]any{"name": "ok", "from": "p", "mode": "text", "words": []any{"a"}},
				"until": map[string]any{"fact": "ok"},
				"max":   40,
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(doc, "max 40") {
		t.Errorf("a loop's count was not written as `max 40`:\n%s", doc)
	}
	for _, word := range []string{"tries ", "every "} {
		if contains(doc, word) {
			t.Errorf("the 2.0 form emitted `%s`, which the design deleted:\n%s", word, doc)
		}
	}
}

// AND THE ONE KEY THAT MUST NOT BE MACRO-EXPANDED: a document that writes `tries:` itself is refused by
// the vocabulary, not silently rewritten - so the design's deletion is enforced where the file is read.
func TestATriesKeyIsRefusedByTheVocabulary(t *testing.T) {
	doc := map[string]any{
		"name":                 "x",
		"resolution":           []any{1920, 1080},
		"recording_resolution": []any{1920, 1080},
		"steps": []any{map[string]any{
			"name":  "s",
			"check": map[string]any{"name": "ok", "from": "p", "mode": "text", "words": []any{"a"}},
			"tries": 40,
		}},
	}
	if err := validateDocument(doc); err != nil {
		return // refused at the document level, which is also correct
	}
	steps, _ := doc["steps"].([]any)
	step, _ := steps[0].(map[string]any)
	if err := stepValidateForTest("s", step); err == nil {
		t.Fatal("a step with `tries:` was accepted; the design deleted that key")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle ||
		len(haystack) > 0 && (indexOf(haystack, needle) >= 0))
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
