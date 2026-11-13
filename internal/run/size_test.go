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
		{"Apple Inc. Virtio Block Media 0.15 TB", 60, 200, true},  // 153.6 GB
		{"Apple Inc. Virtio Block Media 64.0 GB", 60, 200, true},
		{"Apple Inc. Virtio Block Media 59.9 GB", 60, 200, false},
	}
	for _, c := range cases {
		ex, _ := newExec(&fakeDriver{})
		ex.seen = map[string][]string{"disk": wordsOfText(c.reading)}
		step := walk.Step{Name: "size", Spec: map[string]any{
			"name": "size",
			"check": map[string]any{
				"name":         "ok",
				"from":         "disk",
				"size_between": []any{c.lo, c.hi},
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
			"check": map[string]any{"name": "ok", "from": "disk",
				"size_between": []any{60.0, 200.0}},
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
