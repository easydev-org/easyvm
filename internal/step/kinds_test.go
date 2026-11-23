package step

import "testing"

// A KEY'S KIND IS CHECKED, AND THE CHECK KNOWS WHICH SHAPES ARE LEGAL.
//
// BEFORE THIS, `Validate` asked only whether the key existed and `Kind` drove nothing - so
// `click: "not a point"`, `click: [1,2,3]` and `click: [1.5, 2.5]` all passed `easyvm check` and
// reached a guest. The third would have been silently truncated to (1, 2).
func TestEveryKindAcceptsItsShapesAndRefusesTheOthers(t *testing.T) {
	cases := []struct {
		key   string
		ok    []any
		notOK []any
	}{
		{key: "click", ok: []any{[]any{0, 0}, []any{1919, 1079}},
			notOK: []any{"not a point", []any{1, 2, 3}, []any{1.5, 2.5}}},
		{key: "at_button", ok: []any{[]any{0, 0, 10, 10}},
			notOK: []any{[]any{1, 2, 3}}},
		{key: "at_color", ok: []any{[]any{40, 200, 64}},
			notOK: []any{[]any{1, 2}, []any{1, 2, 3, 4}}},
		{key: "disk", ok: []any{"disk.qcow2", "{{ work }}/disk.qcow2"},
			notOK: []any{[]any{"disk.qcow2"}, map[string]any{"from": "x"}}},
		// A TEMPLATE IS SUBSTITUTED AFTER THIS MAP IS READ, so it is legal here and resolved later.
		{key: "timeout", ok: []any{1800, 300.0, "{{ boot_timeout }}"},
			notOK: []any{"abc", []any{1}}},
		{key: "optional", ok: []any{true, false}, notOK: []any{"true", 1}},
		{key: "needs", ok: []any{[]any{"a.iso"}}, notOK: []any{"a.iso", true}},
	}

	for _, c := range cases {
		key, known := keyOf(c.key)
		if !known {
			t.Fatalf("%q is not a key", c.key)
		}
		for _, v := range c.ok {
			if err := checkKind(key, v); err != nil {
				t.Errorf("%s: %#v was refused: %v", c.key, v, err)
			}
		}
		for _, v := range c.notOK {
			if err := checkKind(key, v); err == nil {
				t.Errorf("%s: %#v was ACCEPTED, and it is not one of its shapes", c.key, v)
			}
		}
	}
}

// `KindAny` IS FOR A KEY WHOSE TWO FORMS DIFFER IN STRUCTURE, AND EACH USE MUST BE ARGUED FOR HERE.
//
// It held ten keys once. Measured one at a time against the loaded tree, EVERY ONE of those ten was
// a declaration that had drifted from what its executor accepts - `desktop` was called a mapping and
// is a string, `at_color` was called a rectangle and is an [r,g,b] triple, `boot` was called a
// mapping and is a bool whose `os:`/`iso:` are SIBLING keys. None of them needed this kind.
//
// SO A KEY LANDING HERE IS A DECISION, and it does not get to make it silently: this test fails
// until somebody writes the reason below.
var kindAnyReasons = map[string]string{
	"esp_fallback": "a STRING naming the disk (`disk`), or a MAPPING {disk, from}. The flat form " +
		"is what the only file uses and the mapping form is what a reader should write, so both are " +
		"accepted; the two differ in structure rather than in value type.",
	"stop": "the STEP-LEVEL MARKER for stopping the machine: `true` on the marker with `method` " +
		"(and optionally `save`) beside it. The structural forms are the marker and its absence.",
	"copy": "the STEP-LEVEL MARKER for a copy action: `true` on the marker, with `from`/`to` (or " +
		"`template`) beside it. The distinct structural forms are the marker and its absence.",
	"input": "the STEP-LEVEL MARKER for an input action, whose true value says \"this step is an " +
		"input\" and whose siblings (`click`, `text`, `key`, `colour`) say WHICH. It is `true` on " +
		"that marker and absent on a step that does not press anything, so the two structural " +
		"forms are \"present as a marker\" and \"absent\", which no other Kind expresses.",
}

func TestNoKeyIsUncheckedWithoutAReason(t *testing.T) {
	var unjustified []string
	for name, key := range allDeclaredKeys() {
		if key.Kind != KindAny {
			continue
		}
		if _, ok := kindAnyReasons[name]; !ok {
			unjustified = append(unjustified, name)
		}
	}
	if len(unjustified) > 0 {
		t.Fatalf("`KindAny` is on %v with no reason recorded in kindAnyReasons. Every key it held "+
			"before was a MISTYPED DECLARATION rather than a key with two structural forms - so "+
			"measure the shapes the loader produces and write down why they cannot be one", unjustified)
	}
	// AND THE OTHER DIRECTION: a reason for a key that is no longer KindAny is a stale argument.
	for name := range kindAnyReasons {
		// A VERB'S OWN KEY IS A MAPPING OR A FLAG, not a value with two structural forms.
		if _, isVerb := Verbs[name]; isVerb {
			continue
		}
		key, ok := keyOf(name)
		if !ok {
			t.Errorf("kindAnyReasons explains `%s:`, which is not a key", name)
			continue
		}
		if key.Kind != KindAny {
			t.Errorf("kindAnyReasons explains `%s:` as multi-shaped, and its Kind is %s", name, key.Kind)
		}
	}
}
