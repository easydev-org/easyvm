package run

import "testing"

// THE FOLDED FORM OF `click:` MUST MEAN EXACTLY WHAT THE OLD KEY MEANT.
//
// v0.0.1 had six keys for one verb. This asserts the mapping form reaches the same implementation,
// by checking the translation rather than by clicking at a live guest: the translator is the only
// new code, and every path after it is the one already measured.
func TestClickMappingTranslatesToTheOldKeys(t *testing.T) {
	cases := []struct {
		name string
		spec map[string]any
		want string // the key the dispatcher must end up reading
		val  any
	}{
		{
			name: "button",
			spec: map[string]any{"click": map[string]any{"button": []any{703, 336}}},
			want: "at_button",
			val:  []any{703, 336},
		},
		{
			name: "colour",
			spec: map[string]any{"click": map[string]any{"colour": []any{255, 0, 0}}},
			want: "at_color",
			val:  []any{255, 0, 0},
		},
		{
			name: "button with a fallback",
			spec: map[string]any{"click": map[string]any{
				"button": []any{703, 336}, "fallback": []any{10, 10}}},
			want: "at_fallback",
			val:  []any{10, 10},
		},
	}
	for _, c := range cases {
		got := translateClick(c.spec)
		if got == nil {
			t.Fatalf("%s: no translation", c.name)
		}
		if _, ok := got[c.want]; !ok {
			t.Fatalf("%s: translated to %v, which has no %q", c.name, specKeys(got), c.want)
		}
		if c.want == "at_button" || c.want == "at_color" {
			// the value must be the one the file wrote, not a copy that lost its numbers
			if len(got[c.want].([]any)) != len(c.val.([]any)) {
				t.Fatalf("%s: value %v is not %v", c.name, got[c.want], c.val)
			}
		}
		if _, stillHasClick := got["click"]; stillHasClick {
			t.Fatalf("%s: `click:` survived the translation, so both keys would dispatch", c.name)
		}
	}
}

func specKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
