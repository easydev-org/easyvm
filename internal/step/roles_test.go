package step

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// EVERY DECLARED KEY HAS A ROLE, and the role comes from the table that declares it rather than a
// fourth list that has to agree.
func TestEveryKeyHasARole(t *testing.T) {
	for k := range allDeclared() {
		role, ok := Roles[k]
		if !ok || role == RoleUnknown {
			t.Errorf("`%s:` is declared and Roles says %v", k, role)
		}
	}
}

// AND NO ROLE IS INVENTED FOR A KEY NOTHING DECLARES.
func TestNoRoleIsDeclaredForAnUnknownKey(t *testing.T) {
	known := allDeclared()
	for name := range Roles {
		if !known[name] {
			t.Errorf("Roles names `%s:` and nothing declares it", name)
		}
	}
}

// TestDispatchOrderMatchesTheExecutor keeps the copied switch order honest.
//
// THE ORDER IS WHAT DECIDES WHICH ACTION WINS, so a table that drifts from the `switch` would name
// the wrong key in every "this will never run" error - which is worse than no check, because the
// message would send the reader to the wrong line.
func TestDispatchOrderMatchesTheExecutor(t *testing.T) {
	src, err := os.ReadFile("../run/executor.go")
	if err != nil {
		t.Skipf("executor source not readable from here: %v", err)
	}
	// Only the `runOne` body, so a `case` in some other function is not read as dispatch.
	text := string(src)
	start := strings.Index(text, "func (e *Executor) runOne(")
	if start < 0 {
		t.Fatalf("runOne not found; this test reads its `switch` and the shape changed")
	}
	end := strings.Index(text[start:], "\nfunc ")
	if end < 0 {
		end = len(text) - start
	}
	body := text[start : start+end]

	// `s.Has` IS FALSE FOR `record: false`, SO FIVE BRANCHES ASK `hasKey` INSTEAD - the key is
	// present whatever its value. Both spellings are dispatch cases, so both are collected, and the
	// union is ordered by first appearance in the source because the ORDER is what decides which
	// action wins.
	fileKey := regexp.MustCompile(`s\.Has\("([a-z_0-9]+)"\)`)
	presenceKey := regexp.MustCompile(`hasKey\(s, "([a-z_0-9]+)"\)`)
	type hit struct {
		at   int
		name string
	}
	var hits []hit
	for _, re := range []*regexp.Regexp{fileKey, presenceKey} {
		for _, m := range re.FindAllStringSubmatchIndex(body, -1) {
			hits = append(hits, hit{at: m[0], name: body[m[2]:m[3]]})
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].at < hits[j].at })
	seen := map[string]bool{}
	var inSource []string
	for _, h := range hits {
		if !seen[h.name] {
			seen[h.name] = true
			inSource = append(inSource, h.name)
		}
	}
	if len(inSource) < 30 {
		t.Fatalf("read only %d dispatch cases from executor.go; the parser broke and this test "+
			"would pass about nothing", len(inSource))
	}

	// The table and the source must agree on the RELATIVE order of the keys the table lists.
	var tableOnly []string
	inTable := map[string]bool{}
	for _, k := range dispatchOrder {
		inTable[k] = true
		if !seen[k] {
			tableOnly = append(tableOnly, k)
		}
	}
	if len(tableOnly) > 0 {
		t.Errorf("dispatchOrder lists keys that are NOT dispatch cases in runOne: %s. Either the "+
			"executor dropped the branch or the table is stale", strings.Join(tableOnly, ", "))
	}

	// Walk both lists and require the common keys in the same order.
	var common []string
	for _, k := range inSource {
		if inTable[k] {
			common = append(common, k)
		}
	}
	var tableCommon []string
	for _, k := range dispatchOrder {
		if seen[k] {
			tableCommon = append(tableCommon, k)
		}
	}
	if strings.Join(common, " ") != strings.Join(tableCommon, " ") {
		t.Errorf("dispatchOrder disagrees with runOne's case order.\n  source: %s\n  table:  %s",
			strings.Join(common, " "), strings.Join(tableCommon, " "))
	}
}

// A VERB AND ITS OWN FIELDS IS ONE ACTION; A FIELD FROM ANOTHER VERB IS TWO.
//
// It is the property `Consumes` used to encode in a second table, and it is now read from the verb's
// own field list. The cases are the measured ones.
func TestAVerbAndItsFieldsAreOneAction(t *testing.T) {
	cases := []struct {
		name string
		spec map[string]any
		ok   bool
		why  string
	}{
		{
			name: "press reads text and at",
			spec: map[string]any{"press": true, "text": "hello", "at": []any{1, 2}},
			ok:   true,
			why:  "typing into a field clicks it first - this is ONE action",
		},
		{
			name: "check does not read a click",
			spec: map[string]any{"check": map[string]any{"source": "s"}, "click": []any{1, 2}},
			ok:   false,
			why:  "`doCheck` never reads a click, and the dispatcher takes the check branch first",
		},
		{
			name: "boot and its own arguments",
			spec: map[string]any{"boot": true, "os": "linux", "iso": "x.iso"},
			ok:   true,
			why:  "`os` and `iso` are arguments of boot, not actions",
		},
		{
			name: "run does not read a key",
			spec: map[string]any{"run": "echo hi", "key": "Return"},
			ok:   false,
			why:  "the keystroke would never happen - `key` belongs to `press`",
		},
		{
			name: "a shared modifier beside an action is fine",
			spec: map[string]any{"when_not": []any{"done"}, "press": true, "click": []any{1, 2}},
			ok:   true,
			why:  "`when_not` gates; the click is still the one action",
		},
	}
	for _, c := range cases {
		err := Validate("probe", c.spec)
		if c.ok && err != nil {
			t.Errorf("%s: expected accepted, got %v\n  (%s)", c.name, err, c.why)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: expected REFUSED, got accepted\n  (%s)", c.name, c.why)
		}
	}
}

// EVERY FIELD A VERB DECLARES IS REACHABLE FROM `Roles`, so a question about "may these two keys
// share a step" has an answer for every key a file can write.
func TestEveryVerbFieldHasARole(t *testing.T) {
	for name, v := range Verbs {
		if Roles[name] != RoleAction {
			t.Errorf("`%s:` is a verb and Roles says %s", name, Roles[name])
		}
		for f := range v.Fields {
			if _, ok := Roles[f]; !ok {
				t.Errorf("`%s:` is a field of `%s:` and has no role", f, name)
			}
		}
	}
}
