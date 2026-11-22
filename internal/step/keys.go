// Package step defines the vocabulary a walkthrough YAML file may use.
//
// THIS FILE IS THE CONTRACT. The Python implementation this replaces accepted 85 distinct step
// keys, and every one of them was added because a specific CI run needed it. A key that is
// accepted here but does nothing is worse than a key that is rejected: the file loads, the step
// reports success, and the run proves nothing. So every key below is listed once, with the reason
// it exists, and the validator refuses anything not on the list.
package step

import (
	"fmt"
	"sort"
	"strings"
)

// Key is one accepted field name in a step mapping.
type Key struct {
	Name string
	// Summary is what the key DOES, in the imperative, because a reader looking at a log needs to
	// know what a step claimed to do.
	Summary string
	// Kind says how the value is read and checked.
	Kind Kind
	// Since names the CI run or the measurement that put this key here, when there is one. A key
	// nobody can explain the origin of is a key nobody dares remove.
	Since string
}

// Kind is how a key's value is interpreted. It drives validation, not execution: the executor
// reads the same mapping, but a typo like `until: "Continue"` meaning a phrase versus
// `until: 30` meaning seconds has to be caught before a guest is started.
type Kind int

const (
	// KindString is free text.
	KindString Kind = iota
	// KindBool is true/false; YAML 1.1 quoting quirks make `on` a bool, which has bitten this
	// project before, so booleans are accepted only as real YAML booleans.
	KindBool
	// KindInt is a whole number of seconds, MiB, or a count.
	KindInt
	// KindFloat is seconds with a fractional part.
	KindFloat
	// KindPair is [x, y].
	KindPair
	// KindPairOrMapping is [x, y] OR a mapping that says how the point is named.
	//
	// IT EXISTS FOR `press:` AND FOR NOTHING ELSE, and it is the fold that removes five step keys:
	// `{button: [x, y]}`, `{button: [x, y], fallback: [x, y]}` and `{colour: [r, g, b]}` used to be
	// `at_button:`, `at_fallback:` and `at_color:`. The value form is what makes ONE verb cover them.
	KindPairOrMapping
	// KindStringOrMapping is a name, OR a mapping that adds facts to it.
	//
	// IT EXISTS FOR `disk:`: a line either names a file or creates one, and the five keys that used
	// to say "and 128 GiB, GPT, at least 4000 MiB, settled after 300 s" are fields of that mapping.
	KindStringOrMapping
	// KindWordsOrNone is a word list, or `{none: <word-sets>}` for the negative form.
	//
	// IT EXISTS SO ONE KEY CARRIES BOTH POLARITIES OF THE SAME QUESTION, and so `when_not_any:` (0
	// uses in the tree, and the alternatives form of a negative gate) has a home that does not need
	// its own name.
	KindWordsOrNone
	// KindRect is [x0, y0, x1, y1].
	KindRect
	// KindWords is a phrase, or a list of words that must all be present.
	KindWords
	// KindWordSets is a list of phrases, any one of which satisfies the key.
	KindWordSets
	// KindMapping is a nested mapping (the `do:` list, `boot:` options).
	KindMapping
	// KindList is a sequence of mappings.
	KindList
	// KindScript is a multi-line body handed to a shell ON THE GUEST. Not our code: the disk
	// under test is what runs it, which is why it stays text.
	KindScript
	// KindColor is [r, g, b] - a colour, not a rectangle. `at_color: [40, 200, 64]` was declared
	// `KindRect` and nothing read the declaration, so the two disagreed since the key was written.
	KindColor
	// KindShot is a frame bound under a name: `see: {name: shot}` or `see: shot`.
	//
	// IT ACCEPTS BOTH SPELLINGS because the `.walk` converter writes the short one and a YAML file
	// may write either. What it REFUSES is anything else - a `read:` carrying parameters is the old
	// model, and `refuseLegacySee` names each one and where it belongs.
	KindShot
	// `KindRecord` WAS HERE, FOR `record:` - a file name to start a recording or `false` to stop it.
	// It went with the key: the recording is a property of the run now, so no key's value means
	// "start" or "stop" and no kind is needed to describe one.
	//
	// THE ENUM IS NOT NUMBERED, so removing a member renumbers the rest and nothing depends on the
	// numbers. A `Kind` is only ever compared to another `Kind`, never to an integer or stored.
	// KindTemplated is a number OR a `{{ name }}` placeholder, and the placeholder wins because the
	// loader substitutes variables after this map is read. `timeout: '{{ boot_timeout }}'` is what
	// ubuntu and windows ssh legs write.
	KindTemplated
	// KindAny is a key with more than one accepted shape. IT IS NOW EMPTY, AND THAT IS THE FINDING.
	//
	// IT HELD TEN KEYS AND EVERY ONE WAS A DECLARATION THAT HAD DRIFTED, NOT A KEY THAT NEEDED IT.
	// Measured on the loaded tree - the shape `Validate` actually sees, after variables are
	// substituted and after the older names are read under their current ones:
	//
	//	boot      bool:55                       one shape (its `os:`/`iso:` are SIBLING keys)
	//	disk      string:31                     one shape
	//	script    map{body,name,shell}:16       one shape
	//	rect      []any[4]:2                    one shape
	//	desktop   string:21                     one shape
	//	timeout   int/float64/string:482/6/26   one shape once the {{ }} template is resolved
	//	record    bool:49 string:51             TWO, and both are statements - see KindRecord
	//
	// SO THE TWO KEYS THAT REALLY DO CARRY TWO FORMS HAVE THEIR OWN KINDS AND ARE NOT HERE:
	// `KindRecord` names its pair (a file name or false) and `KindTemplated` names the template. This
	// kind stays in the list because a future key may need it, but a key landing here is a DECISION
	// and `TestTheStructuralKeysAreTheOnesWeThinkTheyAre` fails until somebody records it.
	KindAny
)

func (k Kind) String() string {
	switch k {
	case KindString:
		return "string"
	case KindBool:
		return "bool"
	case KindInt:
		return "int"
	case KindFloat:
		return "float"
	case KindPair:
		return "[x, y]"
	case KindRect:
		return "[x0, y0, x1, y1]"
	case KindWords:
		return "words"
	case KindWordSets:
		return "[words, ...]"
	case KindMapping:
		return "mapping"
	case KindList:
		return "[mapping, ...]"
	case KindScript:
		return "script"
	case KindColor:
		return "[r, g, b]"
	case KindTemplated:
		return "a number or a {{ template }}"
	case KindAny:
		return "any"
	}
	return "?"
}

// Validate checks a step mapping against the vocabulary.
//
// A KEY THAT IS UNKNOWN FAILS THE FILE, and that is the whole point of this list: the Python
// implementation kept 85 keys alive by accepting whatever the executor happened to read, so a
// misspelled key was a step that did nothing and reported success. Two such key names are in this
// project's history already (`family:` then `os:`, `eject` spelled two ways).
// THERE IS NO DEPRECATED LIST. A NAME IS EITHER IN THE VOCABULARY OR IT IS REFUSED.
//
// THERE WAS ONE, and it existed so a renamed key could be refused with a sentence rather than a parse
// error. IT WAS REMOVED BECAUSE THE FILES ARE THE ONLY CALLER. Every walkthrough in this repository
// is renamed in the same commit as the vocabulary, so a stale name is not a user to be guided - it is
// a file that was not updated, and "unknown key" is the correct answer for it. A compatibility table
// with no external consumer is a second place the vocabulary is written down, and the two drift.
// THE BLIND-SLEEP RULE WAS RIGHT FOR ONE COMMIT AND IS NOW WRONG, AND SAYING SO IS THE POINT.
//
// `settle:` used to be `time.Sleep` and did not poll `ShouldStop`, so a long pause beside an action
// was a wall: `easyvm stop` sets a flag and nothing reads it until the pause ends. This validator
// therefore refused anything over ten seconds, and it did its job by failing all 24 linux files at
// once - because the migration in the previous commit had converted every `wait_after:` to `settle:`,
// including the twenty-second ones.
//
// THE FIX WAS IN `settle` RATHER THAN IN THE FILES: it polls in five-second steps now, exactly as
// `doWait` does, so ANY length is interruptible and there is nothing left to refuse. A validator that
// kept the rule would be forcing files to split a pause in two to satisfy a limit whose reason no
// longer exists - which is how a validator becomes a tax.
//
// `actionKeys` USED TO LIVE HERE AND WAS READ BY NOTHING - a second, stale list of "the keys that do
// something" beside the dispatcher's own switch order. It is `Roles` in roles.go now, which `Validate`
// actually enforces and which a test keeps honest against `runOne`.

// Validate refuses a key the vocabulary does not have, and a shape its Kind does not allow.
//
// IT IS THE SAME THREE QUESTIONS FOR A VERB AND FOR AN ACTION, because they are the same kind of
// thing: one field list, one set of shared words, one shape check. There is no lenient path - a step
// that writes a key its action does not read is refused, whichever table the action came from.
func Validate(stepName string, mapping map[string]any) error {
	action, err := actionOf(stepName, mapping)
	if err != nil {
		return err
	}
	if err := CheckOptionalDoesNotAct(stepName, mapping); err != nil {
		return err
	}
	fields := fieldSetOf(action)
	// THE `machine_` PREFIX IS RESERVED FOR THE MACHINE'S OWN LIVE FACTS. A `check:` that bound a
	// name starting with it would be shadowed by the live value, or would shadow it - and either way
	// the file would read a boolean nobody can predict from the source.
	if action == "check" {
		for _, k := range []string{"as", "name"} {
			if v, ok := mapping[k].(string); ok && strings.HasPrefix(v, "machine_") {
				return fmt.Errorf("step %q binds the boolean %q, and `machine_` is reserved for the "+
					"machine's own facts (%s). Name it something else", stepName, v, MachineFactNames())
			}
		}
	}
	for name, value := range mapping {
		if name == "name" || name == action {
			continue
		}
		if key, ok := fields[name]; ok {
			if err := checkKind(key, value); err != nil {
				return fmt.Errorf("step %q: %w", stepName, err)
			}
			continue
		}
		if key, ok := Shared[name]; ok {
			if err := checkKind(key, value); err != nil {
				return fmt.Errorf("step %q: %w", stepName, err)
			}
			continue
		}
		// A KEY FROM ANOTHER ACTION IS NAMED, because the sentence a person needs is "`text:`
		// belongs to `input:`", not "unknown key".
		if owner := ownerOf(name); owner != "" {
			return fmt.Errorf("step %q is a `%s:` step and writes `%s:`, which belongs to `%s:` - "+
				"and a step does one thing", stepName, action, name, owner)
		}
		return fmt.Errorf("step %q has unknown key %q; an unknown key used to mean a step that did "+
			"nothing", stepName, name)
	}
	return nil
}

// fieldSetOf is every key the action reads: its own fields, plus - when it is one of the nine verbs -
// nothing else. `Shared` is consulted separately so a field cannot shadow a modifier.
func fieldSetOf(action string) map[string]Key {
	if v, ok := Verbs[action]; ok {
		return v.Fields
	}
	if a, ok := Actions[action]; ok {
		return a.Fields
	}
	return nil
}

// actionOf finds the one thing a step does, from either table.
func actionOf(stepName string, mapping map[string]any) (string, error) {
	var found []string
	for name := range mapping {
		if _, ok := Verbs[name]; ok {
			found = append(found, name)
			continue
		}
		if _, ok := Actions[name]; ok {
			found = append(found, name)
		}
	}
	if len(found) == 0 {
		// A STEP WHOSE KEYS ARE ALL FIELDS OF ONE VERB IS THAT VERB WITHOUT ITS MARKER. The compiler
		// writes `input 960 628` as `{input: true, click: [...]}`, but a step that carries only the
		// shape - `{at_color, color_region}` - is the same action and must load.
		for verb, v := range Verbs {
			hit := false
			for name := range mapping {
				if _, ok := v.Fields[name]; ok {
					hit = true
					break
				}
			}
			if hit {
				return verb, nil
			}
		}
		// A WAIT IS THE LAST RESORT, because a step that only pauses does nothing else.
		for name := range mapping {
			if _, ok := Shared[name]; ok && name != "name" {
				return "wait", nil
			}
		}
		var written []string
		for name := range mapping {
			if name != "name" {
				written = append(written, name)
			}
		}
		sort.Strings(written)
		return "", fmt.Errorf("step %q names no action, and no key it writes is one; it says %s, "+
			"and a step is one of %s", stepName, quoted(written), quotedNames(true))
	}
	if len(found) > 1 {
		sort.Slice(found, func(i, j int) bool { return DispatchOrder(found[i]) < DispatchOrder(found[j]) })
		return "", fmt.Errorf("step %q names %d actions (%s); a step does one thing, and the "+
			"dispatcher would run `%s:` and drop the rest", stepName, len(found), quoted(found), found[0])
	}
	return found[0], nil
}

// ownerOf names the action a field belongs to, for the failure message.
func ownerOf(field string) string {
	var owners []string
	for name, v := range Verbs {
		if _, ok := v.Fields[field]; ok {
			owners = append(owners, name)
		}
	}
	for name, a := range Actions {
		if _, ok := a.Fields[field]; ok {
			owners = append(owners, name)
		}
	}
	sort.Strings(owners)
	if len(owners) == 0 {
		return ""
	}
	return strings.Join(owners, " or ")
}

// quoted renders names as `a:` and `b:` for a failure message.
func quoted(names []string) string {
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = "`" + n + ":`"
	}
	switch len(parts) {
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " and " + parts[1]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

// quotedNames lists every action name, verbs first, for a failure message.
func quotedNames(withVerbs bool) string {
	var names []string
	for n := range Verbs {
		names = append(names, n)
	}
	if !withVerbs {
		for n := range Actions {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// Declares reports whether the vocabulary has this key, in any of its tables. It is the question a
// caller outside this package asks when it wants to know whether a name is supported.
func Declares(name string) bool {
	_, ok := keyOf(name)
	return ok
}

// keyOf finds a declared key wherever it lives. It is the one lookup the tests and the validator
// share, so a key cannot be "in the vocabulary" for one and not the other.
func keyOf(name string) (Key, bool) {
	if k, ok := Shared[name]; ok {
		return k, true
	}
	if a, ok := Actions[name]; ok {
		return Key{Name: a.Name, Kind: a.Kind, Summary: a.Summary}, true
	}
	for _, a := range Actions {
		if k, ok := a.Fields[name]; ok {
			return k, true
		}
	}
	if v, ok := Verbs[name]; ok {
		return Key{Name: v.Name, Kind: KindMapping, Summary: v.Summary}, true
	}
	for _, v := range Verbs {
		if k, ok := v.Fields[name]; ok {
			return k, true
		}
	}
	return Key{}, false
}
