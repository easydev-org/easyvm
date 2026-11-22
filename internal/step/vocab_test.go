package step

import (
	"sort"
	"strings"
	"testing"
)

// ONE TABLE, AND THESE ARE THE PROPERTIES IT MUST KEEP.
//
// The four tables this replaces (Subkeys, Consumes, nestedOwners, Roles) had to agree with each
// other, and all three ways of disagreeing happened during the migration: a consumed key that was
// not declared, a subkey with no owner, and a key in one table and not another. With one table the
// questions are different, and there are fewer of them.

// A VERB'S FIELDS MUST NOT COLLIDE WITH ANOTHER VERB'S, unless the two really mean the same thing.
//
// `timeout` is shared and lives in `Shared`. A field that means something different on two verbs is a
// field with two meanings, which is the defect the whole design exists to remove.
func TestNoFieldMeansTwoThings(t *testing.T) {
	seen := map[string][]string{}
	for name, v := range Verbs {
		for field := range v.Fields {
			seen[field] = append(seen[field], name)
		}
	}
	for field, verbs := range seen {
		if len(verbs) < 2 {
			continue
		}
		if _, shared := Shared[field]; shared {
			continue
		}
		sort.Strings(verbs)
		t.Errorf("`%s:` is a field of %s. If it means the same thing on both it belongs in "+
			"`Shared`; if it does not, one of them is using the wrong word",
			field, strings.Join(verbs, " and "))
	}
}

// EVERY VERB NEEDS A `Required` FIELD UNLESS ANY OF ITS OWN IS ENOUGH.
//
// It is what makes the error a person reads say "press needs a coordinate" rather than "no
// implementation for this step's keys".
func TestEveryVerbSaysWhatItNeeds(t *testing.T) {
	for name, v := range Verbs {
		if v.Summary == "" {
			t.Errorf("`%s:` has no summary, so a log line cannot say what it did", name)
		}
		if v.Required == "" {
			continue
		}
		if _, ok := v.Fields[v.Required]; ok {
			continue
		}
		if _, ok := Shared[v.Required]; !ok {
			t.Errorf("`%s:` requires `%s:`, which is neither its field nor a shared word",
				name, v.Required)
		}
	}
}

// A FIELD'S KIND IS CHECKED WHERE IT IS DECLARED, so a wrong shape is refused by the verb that owns
// it rather than by a second table.
func TestAFieldsKindIsChecked(t *testing.T) {
	if err := Validate("probe", map[string]any{
		"press": true,
		"click": "not a pair",
	}); err == nil {
		t.Fatal("a click with a non-coordinate was accepted")
	}
	if err := Validate("probe", map[string]any{
		"press": true,
		"click": []any{1, 2},
	}); err != nil {
		t.Fatalf("a valid click was refused: %v", err)
	}
}

// A FIELD FROM ANOTHER VERB IS NAMED, because "unknown key" is a useless sentence when the file
// wrote a key the vocabulary HAS, for a different verb.
func TestAFieldFromAnotherVerbIsNamed(t *testing.T) {
	err := Validate("probe", map[string]any{
		"run":  "echo hi",
		"text": "hello",
	})
	if err == nil {
		t.Fatal("a `text:` on a `run:` step was accepted")
	}
	if !strings.Contains(err.Error(), "press") {
		t.Errorf("the error does not say which verb owns `text:`: %v", err)
	}
}

// TWO VERBS IN ONE STEP IS REFUSED BY NAME, because the dispatcher would run the first and drop the
// rest.
func TestTwoVerbsInOneStepIsRefused(t *testing.T) {
	err := Validate("probe", map[string]any{
		"boot":  true,
		"press": true,
		"click": []any{1, 2},
	})
	if err == nil {
		t.Fatal("a step with two verbs was accepted")
	}
	if !strings.Contains(err.Error(), "does one thing") {
		t.Errorf("the error does not say why: %v", err)
	}
}

// NO KEY IS IN TWO PLACES, because "which table wins" is the question this design removed.
func TestNoKeyIsDeclaredTwice(t *testing.T) {
	where := map[string]string{}
	note := func(k, table string) {
		if prev, ok := where[k]; ok {
			t.Errorf("`%s:` is declared in both %s and %s", k, prev, table)
			return
		}
		where[k] = table
	}
	for k := range Shared {
		note(k, "Shared")
	}
	for name, v := range Verbs {
		if _, ok := Shared[name]; ok {
			t.Errorf("`%s:` is a verb and a shared field", name)
		}
		for f := range v.Fields {
			if _, shared := Shared[f]; shared {
				t.Errorf("`%s:` is a field of `%s:` and also a shared field", f, name)
			}
		}
	}
}

// EVERY DECLARED KEY IS REACHABLE: a verb, its field, a shared word, or an alias. A key that is in
// none of those is a key nothing can write and nothing can read.
func TestEveryDeclaredKeyIsReachable(t *testing.T) {
	reachable := func(k string) bool {
		if _, ok := Shared[k]; ok {
			return true
		}
		if _, ok := Aliases[k]; ok {
			return true
		}
		if _, ok := Verbs[k]; ok {
			return true
		}
		for _, v := range Verbs {
			if _, ok := v.Fields[k]; ok {
				return true
			}
		}
		return false
	}
	for k := range Roles {
		if !reachable(k) {
			t.Errorf("`%s:` has a role but nothing declares it", k)
		}
	}
}

// AND THE OTHER DIRECTION: every key a verb declares has a role, so `ActionsIn`-style questions have
// an answer for all of them.
func TestEveryDeclaredKeyHasARole(t *testing.T) {
	check := func(k string) {
		if _, ok := Roles[k]; !ok {
			t.Errorf("`%s:` is declared and has no role", k)
		}
	}
	for k := range Shared {
		check(k)
	}
	for k := range Aliases {
		check(k)
	}
	for name, v := range Verbs {
		check(name)
		for f := range v.Fields {
			check(f)
		}
	}
}
