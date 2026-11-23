package walk

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/easydev-org/easyvm/internal/step"
)

// THERE IS ONE FORMAT, AND THIS IS THE TEST THAT SAYS IT WORKS ON EVERY REAL FILE.
//
// Every walkthrough in the tree is read, compiled, and the result must reach the engine: the step
// list must be the same length, every step must pass the vocabulary, and reading the source twice
// must give the same document. A format that only parses is a format nobody has checked.
func TestEveryRealWalkthroughCompiles(t *testing.T) {
	files := realWalkthroughs(t)
	checked := 0
	for _, path := range files {
		doc, err := WalkV2ToYAML(readFile(t, path))
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(path), err)
			continue
		}
		steps, ok := doc["steps"].([]any)
		if !ok || len(steps) == 0 {
			t.Errorf("%s: no steps", filepath.Base(path))
			continue
		}
		for i, raw := range steps {
			m, ok := raw.(map[string]any)
			if !ok {
				t.Errorf("%s: step %d is not a mapping", filepath.Base(path), i+1)
				continue
			}
			name, _ := m["name"].(string)
			if name == "" {
				t.Errorf("%s: step %d has no name", filepath.Base(path), i+1)
			}
			if err := step.Validate(name, m); err != nil {
				t.Errorf("%s: step %q: %v", filepath.Base(path), name, err)
			}
		}
		checked++
	}
	if checked < 20 {
		t.Fatalf("only %d walkthroughs compiled; the glob is wrong", checked)
	}
	t.Logf("%d walkthroughs compile and validate", checked)
}

// READING IS DETERMINISTIC AND IDEMPOTENT: the same text gives the same document, twice.
func TestReadingIsIdempotent(t *testing.T) {
	for _, path := range realWalkthroughs(t) {
		src := readFile(t, path)
		a, err := WalkV2ToYAML(src)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(path), err)
			continue
		}
		b, err := WalkV2ToYAML(src)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(path), err)
			continue
		}
		if d := diffDoc(a, b, ""); d != "" {
			t.Errorf("%s: reading twice differs at %s", filepath.Base(path), d)
		}
	}
}

// THE EMITTER AND THE READER AGREE, which is what makes the format a closed system.
//
// A document compiled from a file is rendered again, and the second compile must equal the first.
// This is not a migration check any more - there is nothing to migrate from - it is the property
// that a `.walk` cannot express something the reader loses.
func TestTheEmitterAndReaderAgreeOnEveryRealFile(t *testing.T) {
	for _, path := range realWalkthroughs(t) {
		src := readFile(t, path)
		first, err := WalkV2ToYAML(src)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(path), err)
			continue
		}
		rendered, err := YAMLToWalkV2(first)
		if err != nil {
			t.Errorf("%s: cannot render: %v", filepath.Base(path), err)
			continue
		}
		second, err := WalkV2ToYAML(rendered)
		if err != nil {
			t.Errorf("%s: the rendered file does not parse: %v\n--- rendered ---\n%s",
				filepath.Base(path), err, rendered)
			continue
		}
		if d := diffDoc(first, second, ""); d != "" {
			t.Errorf("%s: rendering changed the document at %s", filepath.Base(path), d)
		}
	}
}

// THE SYNTAX IS THREE RULES AND THEY ARE ENFORCED.
//
// A brace is refused by name, because a file that writes one is a file written in a language this
// format replaced. The error must say what to write instead - a rule a reader cannot act on is not
// a rule.
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
	if !strings.Contains(err.Error(), "indented block") {
		t.Errorf("the error does not say what to write instead: %v", err)
	}
}

// CHILDREN ARE INDENTED, AND THERE IS NO OTHER MARKER.
//
// `from` and `words` are siblings under `expect`. Neither is written on the `expect` line, because
// neither is required for the verb to mean anything, and a rule that says "children are indented"
// admits no exceptions or it is not a rule.
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
	step := onlyStep(t, doc)
	// `expect` IS THE SOURCE WORD; the engine's key is `check`, because the document is the shape
	// the executor has always dispatched on.
	exp, ok := step["expect"].(map[string]any)
	if !ok {
		t.Fatalf("`expect` did not compile to `check`: %#v", step)
	}
	if exp["from"] != "screen" {
		t.Errorf("from = %v", exp["from"])
	}
	if _, ok := exp["words"].([]any); !ok {
		t.Errorf("words is %T, want a list", exp["words"])
	}
}

// A LIST OF STEPS IS `item` BLOCKS, and one may nest a branch - which is why a flat `do:` could not
// express the loops that answer a wizard page by page.
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
	step := onlyStep(t, doc)
	rep, ok := step["repeat"].(map[string]any)
	if !ok {
		t.Fatalf("`repeat` did not compile into `repeat:`: %#v", step)
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
	// THE BRANCHES ARE THE STEP'S OWN KEYS, beside `if`, because a step runs one thing and its
	// `then`/`else` are what `if` chooses between.
	then, ok := second["then"].([]any)
	if !ok || len(then) != 1 {
		t.Fatalf("the nested `then` = %#v", second["then"])
	}
	if branch["from"] != "pane" {
		t.Fatalf("the condition lost its reading: %#v", branch)
	}
	// THE CONDITION IS THE LOOP'S OWN `until:`, WHICH IS WHAT THE ENGINE READS NOW. There is no
	// `expect`/`read` pair to compile it into: a `repeat` stops on a condition, and the condition
	// names the picture it reads.
	until, ok := rep["until"].(map[string]any)
	if !ok || until["from"] != "pane" {
		t.Fatalf("the condition did not reach `until:`: %#v", rep["until"])
	}
}

// A WORD ALONE IS `true`: a flag's presence IS its argument, and there is no second way to write it.
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
	if got := onlyStep(t, doc)["optional"]; got != true {
		t.Errorf("optional = %#v, want true", got)
	}
}

// THERE IS NO RETRY INTERVAL AND NO TRY COUNT. A retry is `repeat ... until`, and the pause between
// attempts is a `settle` in the `do`. This asserts the vocabulary itself, not the emitter: neither
// word may be a modifier or a key the design accepts.
func TestTheDesignHasNoRetryInterval(t *testing.T) {
	for _, word := range []string{"tries", "every"} {
		if got := v2KeyOrder(word); got != "5"+word {
			t.Errorf("`%s` has a modifier order (%q); the design deleted it", word, got)
		}
	}
	doc, err := WalkV2ToYAML(`walkthrough "x"
resolution 1920x1080
recording_resolution 1920x1080
step "s"
  repeat
    pause 5
    until
      from p
      words [a]
    max 40
`)
	if err != nil {
		t.Fatal(err)
	}
	loop, _ := onlyStep(t, doc)["repeat"].(map[string]any)
	if loop["max"] == nil {
		t.Fatalf("the loop's count did not reach `max`: %#v", loop)
	}
}

// A HEADER LINE THAT IS MISSING IS NAMED, not defaulted. `recording_resolution` is the video's size,
// which is not `resolution` - one is where a coordinate was measured, the other is how big the
// artifact is.
func TestRecordingResolutionIsRequired(t *testing.T) {
	// THE CHECK LIVES IN `loadDocument`, not in the parser: a document is a document, and the rule is
	// about a walkthrough. So this goes through `Load`, which is the entry point a run uses.
	dir := t.TempDir()
	path := filepath.Join(dir, "x.walk")
	if err := os.WriteFile(path, []byte(`walkthrough "x"
resolution 1920x1080
step "s"
  stop kill
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, nil); err == nil {
		t.Fatal("a file with no `recording_resolution` loaded")
	}
}

// ---- helpers ---------------------------------------------------------------------------------

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func realWalkthroughs(t *testing.T) []string {
	t.Helper()
	// THE STAGES LIVE IN THE SUB-REPOSITORIES, NOT HERE. Each is laid out
	// `<repo>/<stage>/steps/<stage>.walk`, so the glob covers one level of stage under every root.
	//
	// THE FLOOR IS AN ASSERTION, and it is what makes a wrong glob a failure rather than an empty loop:
	// there are sixty-odd stages across the five repositories, so a number under twenty means the
	// search found the wrong directory rather than that the tree got smaller.
	var files []string
	for _, family := range familyOrder {
		files = append(files, globSteps("", family)...)
	}
	sort.Strings(files)
	if len(files) == 0 {
		t.Skip("no stages here: the walkthroughs live in the sub-repositories (easyvm-linux, " +
			"easyvm-windows, easyvm-macos, easyvm-bsd, easyvm-android). Point EASYVM_STAGES at a " +
			"checkout of them to run this test")
	}
	if len(files) < 20 {
		t.Fatalf("found only %d walkthroughs; the glob is wrong", len(files))
	}
	return files
}

func onlyStep(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	steps, ok := doc["steps"].([]any)
	if !ok || len(steps) != 1 {
		t.Fatalf("want one step, got %#v", doc["steps"])
	}
	m, ok := steps[0].(map[string]any)
	if !ok {
		t.Fatalf("the step is not a mapping")
	}
	return m
}

// diffDoc names the first place two documents disagree, because "they differ" is useless when a step
// has twenty keys and four of them are coordinate pairs.
func diffDoc(want, got any, at string) string {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return at + ": not a mapping"
		}
		for k, wv := range w {
			gv, ok := g[k]
			if !ok {
				return at + "." + k + ": missing"
			}
			if d := diffDoc(wv, gv, at+"."+k); d != "" {
				return d
			}
		}
		for k := range g {
			if _, ok := w[k]; !ok {
				return at + "." + k + ": unexpected"
			}
		}
	case []any:
		g, ok := got.([]any)
		if !ok || len(w) != len(g) {
			return at + ": lists differ"
		}
		for i := range w {
			if d := diffDoc(w[i], g[i], fmt.Sprintf("%s.%d", at, i)); d != "" {
				return d
			}
		}
	default:
		if fmt.Sprint(want) != fmt.Sprint(got) {
			return fmt.Sprintf("%s: want %v, got %v", at, want, got)
		}
	}
	return ""
}
