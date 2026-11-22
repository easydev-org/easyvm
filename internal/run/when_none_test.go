package run

import "testing"

// `when: {none: ...}` MUST READ EXACTLY AS `when_not_any:` DID.
//
// The folded form exists so one key carries both polarities of one question. If the two spellings
// read the list differently - a flat list meaning "all of these" in one and "any of these" in the
// other - then folding it would silently change what a file means.
func TestWhenNoneReadsLikeWhenNotAny(t *testing.T) {
	cases := []struct {
		name string
		value any
		want [][]string
	}{
		{
			name: "one flat set",
			value: []any{"continue"},
			want: [][]string{{"continue"}},
		},
		{
			name: "one inner list is one set",
			value: []any{[]any{"back", "later"}},
			want: [][]string{{"back", "later"}},
		},
		{
			name: "several sets",
			value: []any{[]any{"continue"}, []any{"back", "later"}, "not now"},
			want: [][]string{{"continue"}, {"back", "later"}, {"not now"}},
		},
	}
	for _, c := range cases {
		got := setsOfAny(c.value)
		if len(got) != len(c.want) {
			t.Fatalf("%s: got %v, want %v", c.name, got, c.want)
		}
		for i := range got {
			if len(got[i]) != len(c.want[i]) {
				t.Fatalf("%s: set %d is %v, want %v", c.name, i, got[i], c.want[i])
			}
			for j := range got[i] {
				if got[i][j] != c.want[i][j] {
					t.Fatalf("%s: set %d word %d is %q, want %q", c.name, i, j, got[i][j], c.want[i][j])
				}
			}
		}
	}
}

// A value that is not a list reads as no alternatives, so `doUnless` falls through to its own
// message rather than gating on an empty set (which would make every step run).
func TestWhenNoneRefusesANonList(t *testing.T) {
	if got := setsOfAny("not a list"); got != nil {
		t.Fatalf("a string must not become alternatives, got %v", got)
	}
}
