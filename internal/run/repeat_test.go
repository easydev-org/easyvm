package run

import (
	"os"
	"strings"
	"testing"

	"github.com/easydev-org/easyvm/internal/walk"
)

// THE NUMBER CONDITION IS THE ONE PLACE THIS REPOSITORY DOES ARITHMETIC ON A SCREEN, so it is
// tested against the readings that actually happened rather than against invented ones.
//
// THE TWO NUMBERS BELOW ARE FROM THE PYTHON IMPLEMENTATION'S OWN NOTES:
//
//	Apple Inc. VirtIO Block Media   268.4 MB     <- the boot image, which must NEVER be selected
//	...                             137.44 GB     <- a correctly selected target
//
// Erasing the boot image installs macOS into it and does not fail for fifty minutes - so a
// condition that accepted "268.4 MB" as a candidate would be the most expensive bug in the stage.
func TestCapacitiesReadsUnits(t *testing.T) {
	cases := []struct {
		reading string
		want    []float64
		why     string
	}{
		{"Capacity: 137.44 GB", []float64{137.44}, "a target disk"},
		{"main 268.4 MB total", []float64{268.4 / 1024}, "a boot image, converted to GB"},
		{"2.88 GB", []float64{2.88}, "the recovery volume"},
		{"nothing readable here", nil, "a pane that has not painted yet"},
		{"1.5 GB and 128.00 GB", []float64{1.5, 128.0}, "several capacities on one pane"},
	}
	for _, tc := range cases {
		got := capacities(wordsOf2(tc.reading))
		if len(got) != len(tc.want) {
			t.Fatalf("%q: read %v, want %v (%s)", tc.reading, got, tc.want, tc.why)
		}
		for i := range got {
			if diff := got[i] - tc.want[i]; diff > 0.001 || diff < -0.001 {
				t.Fatalf("%q: read %v, want %v (%s)", tc.reading, got, tc.want, tc.why)
			}
		}
	}
}

func wordsOf2(s string) []string {
	// a reading is already a list of words; splitting on spaces is what readScreen produces
	var out []string
	field := ""
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' {
			if field != "" {
				out = append(out, field)
				field = ""
			}
			continue
		}
		field += string(r)
	}
	if field != "" {
		out = append(out, field)
	}
	return out
}

// THE RANGE IS THE DECISION, AND IT IS WHAT SEPARATES A DISK FROM A RECOVERY VOLUME.
//
// A 128 GiB disk is in 60..200; the 2.88 GB recovery volume and a 268 MB boot image are not. That is
// the whole content of the step, and it is now readable in the file instead of being a constant in
// Go.
func TestTheRangeTellsADiskFromARecoveryVolume(t *testing.T) {
	inRange := [2]float64{60, 200}

	for _, tc := range []struct {
		reading string
		want    bool
		why     string
	}{
		{"Capacity: 137.44 GB", true, "a target disk"},
		{"Capacity: 128.00 GB", true, "another target size"},
		{"2.88 GB", false, "the recovery volume"},
		{"main 268.4 MB", false, "a boot image - selecting this installs macOS into it"},
		{"", false, "an unreadable pane"},
	} {
		cond := condition{NumberBetween: &inRange, Unit: "gb"}
		got, _ := cond.matches(wordsOf2(tc.reading), capacities(wordsOf2(tc.reading)))
		if got != tc.want {
			t.Fatalf("%q matched=%v, want %v (%s)", tc.reading, got, tc.want, tc.why)
		}
	}
}

// A WORDS CONDITION IS SATISFIED BY PUNCTUATION-STRIPPED MATCHING, because the same claim is spelled
// differently by different panes. The Python implementation lost a stage to "Remote Login: On"
// against a matcher for the three separate words.
func TestWordsConditionsIgnorePunctuation(t *testing.T) {
	cond := condition{Words: []string{"remote", "login", "on"}}
	got, _ := cond.matches(wordsOf2("Remote Login: On"), nil)
	if !got {
		t.Fatal("a words condition must match through punctuation; the pane writes a colon and " +
			"the file writes three words")
	}

	cond = condition{Words: []string{"disk", "utility"}}
	if ok, _ := cond.matches(wordsOf2("Disk Utility"), nil); !ok {
		t.Fatal("case must not matter")
	}
	if ok, _ := cond.matches(wordsOf2("Macintosh HD"), nil); ok {
		t.Fatal("unrelated words must not match")
	}
}

// THE LARGEST READING IS A CANDIDATE, BUT ANY READING IN RANGE IS A MATCH.
//
// A detail pane shows the device and its partitions, so the disk itself is the largest number - but
// what makes this correct is the RANGE, not the ordering. A loop that only looked at the largest
// would miss a pane whose device row has not painted yet.
func TestAnyReadingInRangeMatches(t *testing.T) {
	inRange := [2]float64{60, 200}
	cond := condition{NumberBetween: &inRange, Unit: "gb"}
	ok, why := cond.matches(nil, []float64{2.88, 128.0})
	if !ok {
		t.Fatalf("128 GB among a recovery volume must match; got %q", why)
	}
}

// A LOOP WITH NO STOP CONDITION MUST BE REFUSED BEFORE ITS FIRST ACTION.
//
// Eight presses of Up is eight clicks into a screen nobody measured, and the Python implementation's
// note says why that matters: erasing the wrong disk installs macOS into the recovery volume and the
// failure does not surface for fifty minutes.
func TestALoopWithoutAConditionIsRefused(t *testing.T) {
	ex := &Executor{Work: t.TempDir()}
	_, err := ex.parseRepeatUntil(stepWith(t, map[string]any{
		"repeat_until": map[string]any{
			"actions": []any{map[string]any{"key": "up"}},
		},
	}))
	if err == nil {
		t.Fatal("a loop with no until_any must be refused")
	}
}

// AND AN UNKNOWN KEY IN AN ACTION IS REFUSED HERE, not after eight presses.
func TestAnActionWithATypoIsRefused(t *testing.T) {
	ex := &Executor{Work: t.TempDir()}
	_, err := ex.parseRepeatUntil(stepWith(t, map[string]any{
		"repeat_until": map[string]any{
			"actions":   []any{map[string]any{"pess": "up"}},
			"until_any": []any{map[string]any{"words": []any{"done"}}},
		},
	}))
	if err == nil {
		t.Fatal("an action with an unknown key must be refused before the loop runs")
	}
}

// THE ATTEMPT CEILING EXISTS, because a loop that can press a key a hundred times is one that will.
func TestTheAttemptCeilingIsEnforced(t *testing.T) {
	ex := &Executor{Work: t.TempDir()}
	_, err := ex.parseRepeatUntil(stepWith(t, map[string]any{
		"repeat_until": map[string]any{
			"attempts":  500,
			"actions":   []any{map[string]any{"key": "up"}},
			"until_any": []any{map[string]any{"words": []any{"done"}}},
		},
	}))
	if err == nil {
		t.Fatal("an attempt count over the ceiling must be refused")
	}
}

// stepWith builds a Step for the parser tests.
func stepWith(t *testing.T, spec map[string]any) walk.Step {
	t.Helper()
	spec["name"] = "probe"
	return walk.Step{Index: 1, Name: "probe", Spec: spec}
}

// `unless_any:` IS THE ALTERNATIVES FORM OF `unless:`, AND IT EXISTS BECAUSE OCR DOES NOT READ EVERY
// WORD ON A PANE EQUALLY WELL.
//
// MEASURED ON opensuse-tumbleweed-install: the double-click on the Installer icon WORKED, YaST drew, and
// the check failed on its own vocabulary:
//
//	open yast2: did not advance: wanted "language" or "welcome", read
//	"1 i 1 , im | a | es (o) Wesn Home} installation) Wpgrade) mm co ..."
//
// `Welcome` came back from tesseract as `Wesn Home`; `installation` and `upgrade` came back correctly.
// A CHECK THAT ASKS FOR A WORD THE READER CANNOT PRODUCE ALWAYS FAILS, and the fix is to ask for a word
// it demonstrably does produce - not to loosen the match, which would let a wrong pane pass.
//
// `unless:` STAYS ONE WORD-SET WITH AND MEANING: a gate that required ANY of two words and one that
// required BOTH were spelled the same way, and that is a trap. Two keys, two meanings.
func TestUnlessAnyTakesTheAlternatives(t *testing.T) {
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
	if !strings.Contains(rest, `wordSetsOf(s, "unless_any")`) {
		t.Fatal("`doUnless` does not read `unless_any`, so a gate with alternatives is refused")
	}
	// AND THE SINGULAR KEY STILL WORKS, unchanged.
	if !strings.Contains(rest, `wordsOf(s, "unless")`) {
		t.Fatal("`unless` no longer works on its own")
	}
	// AND IT IS REGISTERED, or every file that uses it is refused as an unknown key - which is exactly
	// what happened for one build after the reader was written.
	keys, err := os.ReadFile("../step/keys.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(keys), `"unless_any": {Name: "unless_any", Kind: KindWordSets,`) {
		t.Fatal("`unless_any` is not declared in the step vocabulary, so `easyvm check` refuses every " +
			"file that uses it with `an unknown key used to mean a step that did nothing`")
	}
}

// A BARE WORD-SET INSIDE A LOOP MEANS `words:`, THE SAME AS IT DOES ON A STEP.
//
// ONE KEY, TWO SPELLINGS WAS A TRAP I FELL INTO. A step's `until_any:` is a list of word-sets:
//
//	until_any:
//	- [restore, time, machine]
//	- [safari]
//
// and inside `repeat_until:` the entries had to be mappings, because a loop's condition can also be
// `number_between:`, `changed:` or `desktop:`. Writing the list form in a loop - which is what every
// other `until_any:` in the repository looks like - failed with
//
//	until_any[0] is not a mapping
//
// on a GhostBSD run, four minutes in, saying nothing about what was actually wrong. THE LIST FORM IS
// NOW READ AS `words:` SO THE TWO AGREE.
func TestABareWordSetIsReadAsWordsInALoop(t *testing.T) {
	ex := &Executor{Work: t.TempDir()}
	loop, err := ex.parseRepeatUntil(stepWith(t, map[string]any{
		"repeat_until": map[string]any{
			"actions":   []any{map[string]any{"key": "3"}},
			"until_any": []any{[]any{"detailed", "help"}, []any{"list", "of", "commands"}},
		},
	}))
	if err != nil {
		t.Fatalf("a bare word-set must be accepted in `repeat_until.until_any`: %v", err)
	}
	if len(loop.Conditions) != 2 {
		t.Fatalf("%d conditions, want 2", len(loop.Conditions))
	}
	// AND THE WORDS SURVIVE IN ORDER, because they are matched as a phrase.
	want := []string{"detailed", "help"}
	for i, w := range want {
		if loop.Conditions[0].Words[i] != w {
			t.Fatalf("condition 0 is %v, want %v", loop.Conditions[0].Words, want)
		}
	}
	// THE MAPPING FORM STILL WORKS, because the richer conditions need it.
	loop2, err := ex.parseRepeatUntil(stepWith(t, map[string]any{
		"repeat_until": map[string]any{
			"actions":   []any{map[string]any{"key": "up"}},
			"until_any": []any{map[string]any{"number_between": []any{60, 200}, "unit": "gb"}},
		},
	}))
	if err != nil {
		t.Fatalf("the mapping form must still be accepted: %v", err)
	}
	if loop2.Conditions[0].NumberBetween == nil {
		t.Fatal("number_between was dropped from the mapping form")
	}
}
