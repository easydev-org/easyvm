package step

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestEveryKeyHasARole fails when a key in the vocabulary has no entry in Roles, or is marked
// RoleUnknown. A key nobody classified is a key whose part in "one action and its arguments" nobody
// decided, which is the state `click:` was in for its whole life.
func TestEveryKeyHasARole(t *testing.T) {
	var missing, unknown []string
	for name := range Keys {
		role, ok := Roles[name]
		switch {
		case !ok:
			missing = append(missing, name)
		case role == RoleUnknown:
			unknown = append(unknown, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(unknown)
	if len(missing) > 0 {
		t.Errorf("these keys are in the vocabulary and NOT classified in Roles: %s. Every key is "+
			"an action, an argument, a modifier, a gate or a sub-key, and which one decides whether "+
			"two keys may share a step", strings.Join(missing, ", "))
	}
	if len(unknown) > 0 {
		t.Errorf("these keys are classified as RoleUnknown: %s", strings.Join(unknown, ", "))
	}
}

// TestNoRoleIsDeclaredForAnUnknownKey is the other direction: a Role for a name that is not in Keys
// is either a typo or a sub-key somebody added to the wrong table.
func TestNoRoleIsDeclaredForAnUnknownKey(t *testing.T) {
	var orphan []string
	for name := range Roles {
		if _, ok := Keys[name]; !ok {
			orphan = append(orphan, name)
		}
	}
	// SUB-KEYS ARE DECLARED HERE AND IN Keys ON PURPOSE - one table of names, audited once. So an
	// orphan is only a name in neither.
	for _, name := range orphan {
		if Roles[name] == RoleSubkey {
			continue
		}
		t.Errorf("Roles declares `%s:` as a %s, and it is not in Keys nor marked RoleSubkey",
			name, Roles[name])
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

// TestOneActionAndItsArguments pins the two shapes that must be accepted and the one that must not.
func TestOneActionAndItsArguments(t *testing.T) {
	cases := []struct {
		name string
		spec map[string]any
		ok   bool
		why  string
	}{
		{
			name: "type reads click as its argument",
			spec: map[string]any{"type": "hello", "click": []any{1, 2}},
			ok:   true,
			why:  "`doType` clicks the field before typing - this is ONE action",
		},
		{
			name: "check does not read click",
			spec: map[string]any{"check": map[string]any{"name": "x"}, "click": []any{1, 2}},
			ok:   false,
			why:  "`doCheck` never reads a click, and the dispatcher takes the check branch first",
		},
		{
			name: "check does not read double",
			spec: map[string]any{"check": map[string]any{"name": "x"}, "double": []any{1, 2}},
			ok:   false,
			why:  "same shape, measured on linux/mint",
		},
		{
			name: "boot and its own arguments",
			spec: map[string]any{"boot": true, "os": "linux", "iso": "x.iso"},
			ok:   true,
			why:  "`os` and `iso` are arguments of boot, not actions",
		},
		{
			name: "two unrelated actions",
			spec: map[string]any{"click": []any{1, 2}, "key": "Return"},
			ok:   false,
			why:  "`doClick` does not read `key`, so the keystroke would never happen",
		},
		{
			name: "needs alone is a gate, not a missing action",
			spec: map[string]any{"needs": []any{"x.iso"}},
			ok:   true,
			why:  "18 files gate a stage this way; there is no action and that is the statement",
		},
		{
			name: "settle alone is a wait, not a missing action",
			spec: map[string]any{"settle": 30},
			ok:   true,
			why:  "6 files say 'let the screen draw' and nothing else",
		},
		{
			name: "a gate beside an action is fine",
			spec: map[string]any{"when_not": []any{"done"}, "click": []any{1, 2}},
			ok:   true,
			why:  "`when_not` gates; the click is still the one action",
		},
	}
	for _, c := range cases {
		err := CheckOneAction("probe", c.spec)
		if c.ok && err != nil {
			t.Errorf("%s: expected accepted, got %v\n  (%s)", c.name, err, c.why)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: expected REFUSED, got accepted\n  (%s)", c.name, c.why)
		}
	}
}

// TestEveryConsumedKeyIsAStepKey keeps Consumes honest: an action may only claim to read a key that
// the vocabulary declares.
//
// THE ANSWER COMES FROM EITHER TABLE. `qga:` reads `path:` and `argv:`, which are written INSIDE its
// value and live in `Subkeys`; `ssh:` reads `user:`, which is a step key and lives in `Keys`. A test
// that looked only at `Keys` would call every nested argument a typo.
//
// A GATE MAY CONSUME TOO, AND IT IS NOT AN ACTION. `when_not:` reads the screen, so it reads `rect:`
// as well - but it is a GATE: it decides whether the step's action runs rather than being that
// action. The rule this test enforces is "the owner is a key a file may write at step level", which
// a gate is.
func TestEveryConsumedKeyIsAStepKey(t *testing.T) {
	for action, args := range Consumes {
		switch Roles[action] {
		case RoleAction, RoleGate:
		case RoleUnknown:
			// `expect:` and `until_any:` are retired or merged names kept in Consumes so the
			// argument list stays readable; they are not step keys any more and a file cannot
			// write them.
			if _, ok := Keys[action]; ok {
				t.Errorf("Consumes names `%s:` and Roles does not classify it", action)
			}
			continue
		default:
			t.Errorf("Consumes names `%s:` and Roles says it is a %s; only an action or a gate "+
				"consumes arguments", action, Roles[action])
		}
		for _, a := range args {
			if _, ok := Keys[a]; ok {
				continue
			}
			if _, ok := Subkeys[a]; ok {
				continue
			}
			t.Errorf("Consumes[%q] names `%s:`, which is in neither table - a typo here silently "+
				"stops the argument being recognised", action, a)
		}
	}
}
