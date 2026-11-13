package run

import (
	"strings"
	"testing"

	"github.com/easydev-org/easyvm/internal/walk"
)

// A CHOICE ABOUT ARITHMETIC IS A CONDITION LIKE ANY OTHER, AND IT BINDS A BOOLEAN.
//
// MEASURED NEED, FROM THE PYTHON IMPLEMENTATION'S OWN NOTE: the macOS installer shows several disks
// and only one is the target. Selecting it is "click a row, read its capacity, keep it if the number
// is in this range, otherwise step to the next" - a decision about a NUMBER, which a word list cannot
// express. `says: [200]` would match any reading containing those digits, including `1,200 GB`.
func TestASizeRangeBindsABoolean(t *testing.T) {
	cases := []struct {
		reading string
		lo, hi  float64
		want    bool
	}{
		{"Apple Inc. Virtio Block Media 128.0 GB", 60, 200, true},
		{"Apple Inc. Virtio Block Media 268.4 MB", 60, 200, false}, // the boot image, in MB
		{"Apple Inc. Virtio Block Media 0.15 TB", 60, 200, true},   // 153.6 GB
		{"Apple Inc. Virtio Block Media 64.0 GB", 60, 200, true},
		{"Apple Inc. Virtio Block Media 59.9 GB", 60, 200, false},
	}
	for _, c := range cases {
		ex, _ := newExec(&fakeDriver{})
		ex.seen = map[string][]string{"disk": wordsOfText(c.reading)}
		step := walk.Step{Name: "size", Spec: map[string]any{
			"name": "size",
			"check": map[string]any{
				"name":  "ok",
				"from":  "disk",
				"mode":  "size_between",
				"range": []any{c.lo, c.hi},
			},
		}}
		if err := ex.doCheck(step); err != nil {
			t.Fatalf("%q: %v", c.reading, err)
		}
		if got := ex.facts["ok"]; got != c.want {
			t.Errorf("%q in %.0f..%.0f GB -> %v, want %v", c.reading, c.lo, c.hi, got, c.want)
		}
	}
}

// and a branch acts on it, in both directions.
func TestASizeBranchTakesTheRightSide(t *testing.T) {
	for _, tc := range []struct {
		reading  string
		wantDown bool
	}{
		{"Virtio Block Media 128.0 GB", false},
		{"Virtio Block Media 268.4 MB", true},
	} {
		d := &fakeDriver{}
		ex, _ := newExec(d)
		ex.seen = map[string][]string{"disk": wordsOfText(tc.reading)}
		if err := ex.doCheck(walk.Step{Name: "size", Spec: map[string]any{
			"name": "size",
			"check": map[string]any{"name": "ok", "from": "disk", "mode": "size_between",
				"range": []any{60.0, 200.0}},
		}}); err != nil {
			t.Fatal(err)
		}
		if err := ex.doIf(walk.Step{Name: "take?", Spec: map[string]any{
			"if":   "ok",
			"then": []any{map[string]any{"key": "ret"}},
			"else": []any{map[string]any{"key": "down"}},
		}}); err != nil {
			t.Fatal(err)
		}
		var keys []string
		for _, s := range d.strokes {
			keys = append(keys, s[1])
		}
		hasDown := len(keys) == 1 && keys[0] == "down"
		if hasDown != tc.wantDown {
			t.Errorf("%q: keys = %v, want down=%v", tc.reading, keys, tc.wantDown)
		}
	}
}

// wordsOfText splits a reading into the words tesseract would have produced.
func wordsOfText(s string) []string {
	var out []string
	for _, w := range strings.Fields(s) {
		out = append(out, w)
	}
	return out
}

// EVERY MODE IS A NAMED COMPARISON, AND EACH ONE IS TESTED ON A READING A REAL PANE WOULD PRODUCE.
//
// THE MODES ARE THE EXTENSION POINT. Before this, each kind of comparison was its own top-level key -
// `says`, `says_any`, `size_between`, `number_between` - so a fifth kind meant a fifth word in the
// vocabulary, and nothing could say "exactly one of these" in a way a reader could scan. `mode:` makes
// the kind a VALUE.
func TestEveryCheckModeComparesWhatItSays(t *testing.T) {
	cases := []struct {
		mode, reading string
		args          map[string]any
		want          bool
	}{
		// whole words, all of them, any order
		{"says", "Welcome to Ubuntu install", map[string]any{"words": []any{"welcome", "ubuntu"}}, true},
		{"says", "Welcome to Ubuntu install", map[string]any{"words": []any{"welcome", "fedora"}}, false},
		// any one set
		{"says_any", "Languages choose yours", map[string]any{
			"words": []any{[]any{"welcome"}, []any{"languages"}}}, true},
		{"says_any", "nothing here", map[string]any{
			"words": []any{[]any{"welcome"}, []any{"languages"}}}, false},
		// a capacity in GB, with units converted
		{"size_between", "Virtio Block Media 128.0 GB", map[string]any{
			"range": []any{60.0, 200.0}}, true},
		{"size_between", "Virtio Block Media 268.4 MB", map[string]any{
			"range": []any{60.0, 200.0}}, false},
		{"size_between", "Virtio Block Media 0.15 TB", map[string]any{
			"range": []any{60.0, 200.0}}, true},
		// any number, whatever it is about
		{"number_between", "installing 42 percent", map[string]any{
			"range": []any{40.0, 50.0}}, true},
		{"number_between", "installing 42 percent", map[string]any{
			"range": []any{60.0, 90.0}}, false},
		// the whole reading, normalised
		{"equals", "Disk Utility", map[string]any{"text": "disk utility"}, true},
		{"equals", "Disk Utility Extra", map[string]any{"text": "disk utility"}, false},
		// a substring, punctuation-insensitive
		{"contains", "Erase process is complete", map[string]any{"text": "process is complete"}, true},
		{"contains", "Erase process is complete", map[string]any{"text": "not there"}, false},
		// a regular expression over the normalised reading
		{"matches", "Copying 41 of 100 files", map[string]any{`pattern`: `copying \d+ of \d+`}, true},
		{"matches", "Copying files", map[string]any{`pattern`: `copying \d+ of \d+`}, false},
	}
	for _, c := range cases {
		ex, _ := newExec(&fakeDriver{})
		ex.seen = map[string][]string{"screen": wordsOfText(c.reading)}
		check := map[string]any{"name": "ok", "from": "screen", "mode": c.mode}
		for k, v := range c.args {
			check[k] = v
		}
		step := walk.Step{Name: c.mode, Spec: map[string]any{"name": c.mode, "check": check}}
		if err := ex.doCheck(step); err != nil {
			t.Fatalf("%s %q: %v", c.mode, c.reading, err)
		}
		if got := ex.facts["ok"]; got != c.want {
			t.Errorf("%s on %q -> %v, want %v", c.mode, c.reading, got, c.want)
		}
	}
}

// AN UNKNOWN MODE NAMES WHAT EXISTS, because a typo here chooses a comparison nobody wrote.
func TestAnUnknownModeIsRefusedAndListsTheModes(t *testing.T) {
	ex, _ := newExec(&fakeDriver{})
	ex.seen = map[string][]string{"screen": {"x"}}
	err := ex.doCheck(walk.Step{Name: "s", Spec: map[string]any{
		"name":  "s",
		"check": map[string]any{"name": "ok", "from": "screen", "mode": "says_everything"},
	}})
	if err == nil {
		t.Fatal("an unknown mode must be refused")
	}
	for _, mode := range checkModeNames() {
		if !strings.Contains(err.Error(), mode) {
			t.Errorf("the refusal does not list the mode %q; a reader cannot fix the file from it: %v",
				mode, err)
		}
	}
}
