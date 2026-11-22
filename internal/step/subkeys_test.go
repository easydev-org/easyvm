package step

import (
	"strings"
	"testing"
)

// A SUBCKEY WITH NO OWNER IS A STEP WITH NO ACTION, AND IT USED TO LOAD.
//
// THIS IS THE HOLE THE SPLIT CLOSES. `Keys` held `mean`, `words`, `mode`, `rect` and forty others
// beside `click` and `boot`, so every one of them was accepted at step level. A file that wrote
//
//   - mean: 40
//
// loaded, and the dispatcher answered "no implementation for this step's keys" - a true sentence
// about a mistake made two lines earlier.
func TestASubkeyWithNoOwnerIsRefused(t *testing.T) {
	for _, spec := range []map[string]any{
		{"mean": float64(40)},
		{"words": []any{"next"}},
		{"mode": "text"},
		{"max": float64(5)},
		{"then": []any{map[string]any{"settle": 1}}},
		{"reference": "shot.png"},
	} {
		err := Validate("probe", spec)
		if err == nil {
			t.Errorf("%v was accepted; a sub-key with no key to read it is a step that does "+
				"nothing, and the failure belongs here rather than in the dispatcher", spec)
		}
	}
}

// AND A SUBCKEY WITH ITS OWNER IS ACCEPTED, because that is how the tree writes real steps.
//
// `rect:` beside `when_not:` is the case measured in `macos/15/steps/ssh.walk`: the GATE reads the
// region and the click that follows does not use it. `max:` beside `loop:` is the same shape inside
// a mapping. Both must load.
func TestASubkeyBeneathItsOwnerIsAccepted(t *testing.T) {
	for _, c := range []struct {
		what string
		spec map[string]any
	}{
		{"a gate reading a region", map[string]any{
			"click": []any{703, 336}, "when_not": []any{"allow"}, "rect": []any{1, 2, 3, 4},
			"rect_at": []any{1920, 1080}, "scale": float64(2),
		}},
		{"an until reading from the console", map[string]any{
			"until": "DVD-ROM", "from": "console", "blind_after": float64(60),
		}},
		{"a loop with a ceiling", map[string]any{
			"repeat": map[string]any{"max": float64(3), "do": []any{}},
		}},
		{"a check comparing a reference", map[string]any{
			"expect": map[string]any{"from": "shot", "rect": []any{0, 0, 1, 1},
				"rect_at": []any{1920, 1080}, "mode": "like", "reference": "x.png"},
		}},
	} {
		if err := Validate("probe", c.spec); err != nil {
			t.Errorf("%s was refused: %v", c.what, err)
		}
	}
}

// THE TWO TABLES DO NOT OVERLAP. A key in both is a key whose role nobody decided, and `Validate`
// reads whichever one it finds first - so the answer would depend on map iteration.
func TestNoKeyIsInBothTables(t *testing.T) {
	for k := range Keys {
		if _, ok := Subkeys[k]; ok {
			t.Errorf("`%s:` is in Keys AND Subkeys; a validator cannot say which rule it follows", k)
		}
	}
}

// AND A SUBCKEY'S KIND IS STILL CHECKED, because its owner hands it the same `checkKind` a step key
// gets. The split moved the NAME, not the shape.
func TestASubkeysKindIsChecked(t *testing.T) {
	// `mode:` must be a string; a mapping here is a file that meant something else.
	err := Validate("probe", map[string]any{
		"expect": map[string]any{"from": "shot"}, "mode": map[string]any{"x": 1},
	})
	if err == nil {
		t.Fatal("`mode:` was accepted as a mapping")
	}
	if !strings.Contains(err.Error(), "mode") {
		t.Fatalf("the failure does not name the key: %v", err)
	}
}
