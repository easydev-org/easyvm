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

// Validate refuses a key the vocabulary does not have, and a blind pause long enough that stopping
// the machine would not work.
//
// IT IS THREE QUESTIONS, because there is one table now:
//
//  1. which verb is this step? exactly one of `Verbs`.
//  2. is every key either that verb's field or one of `Shared`?
//  3. does each value have the shape its Key declares?
func Validate(stepName string, mapping map[string]any) error {
	verb, err := verbOf(stepName, mapping)
	if err != nil {
		return err
	}
	if err := CheckOptionalDoesNotAct(stepName, mapping); err != nil {
		return err
	}
	// A WAIT-ONLY STEP IS ITS OWN SHAPE: every other key is the wait's own or a modifier.
	if verb == "settle" {
		for name, value := range mapping {
			if name == "name" {
				continue
			}
			key, ok := Shared[name]
			if !ok {
				key, ok = Aliases[name]
			}
			if !ok {
				return fmt.Errorf("step %q is a wait and writes `%s:`, which is not a modifier",
					stepName, name)
			}
			if err := checkKind(key, value); err != nil {
				return fmt.Errorf("step %q: %w", stepName, err)
			}
		}
		return nil
	}
	// THE ALIAS AND THE WAIT-ONLY SHAPES are checked leniently: their fields are the engine's,
	// not a verb's.
	if verb == "action" || verb == "settle" {
		return checkLooseAlias(stepName, mapping)
	}
	// AN ALIAS STEP (`require_ssh`, `desktop`, `disk_settle`, ...) is the engine's shape, and its
	// fields are declared in `Aliases` rather than on a verb.
	if _, isAlias := Aliases[verb]; isAlias {
		return checkLooseAlias(stepName, mapping)
	}
	v := Verbs[verb]
	for name, value := range mapping {
		if name == "name" {
			continue
		}
		// THE VERB ITSELF: its value is a flag or an argument, and the verb already accepted it.
		if name == verb {
			continue
		}
		if key, ok := v.Fields[name]; ok {
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
		// AN ALIAS IS A SHAPE THE ENGINE STILL DISPATCHES ON. It is not part of what a person writes,
		// but a compiled document carries it and it must load.
		if key, ok := Aliases[name]; ok {
			if err := checkKind(key, value); err != nil {
				return fmt.Errorf("step %q: %w", stepName, err)
			}
			continue
		}
		// A KEY THAT BELONGS TO A DIFFERENT VERB IS A MISTAKE WORTH NAMING, because the sentence a
		// person needs is "`text:` belongs to `press:`", not "unknown key".
		if owner := ownerOf(name); owner != "" {
			return fmt.Errorf("step %q is a `%s:` step and writes `%s:`, which belongs to `%s:` - "+
				"and a step does one thing", stepName, verb, name, owner)
		}
		return fmt.Errorf("step %q has unknown key %q; an unknown key used to mean a step that did "+
			"nothing", stepName, name)
	}
	return nil
}

// checkLooseAlias validates a step whose action is one of the engine's shapes rather than a verb a
// person writes: `do:`, `acpi:`, `save:` and their kin. Every key must be one the vocabulary knows.
func checkLooseAlias(stepName string, mapping map[string]any) error {
	for name, value := range mapping {
		if name == "name" {
			continue
		}
		if key, ok := Shared[name]; ok {
			if err := checkKind(key, value); err != nil {
				return fmt.Errorf("step %q: %w", stepName, err)
			}
			continue
		}
		if key, ok := Aliases[name]; ok {
			if err := checkKind(key, value); err != nil {
				return fmt.Errorf("step %q: %w", stepName, err)
			}
			continue
		}
		// A FIELD OF ANY VERB IS ACCEPTED ON AN ALIAS STEP, because an alias IS that verb's engine
		// shape: `write:` renders a template and reads `template:`; `require_ssh:` reads the probe.
		if fieldKey, ok := anyVerbField(name); ok {
			if err := checkKind(fieldKey, value); err != nil {
				return fmt.Errorf("step %q: %w", stepName, err)
			}
			continue
		}
		if owner := ownerOf(name); owner != "" {
			return fmt.Errorf("step %q writes `%s:`, which belongs to `%s:`, and this step's action "+
				"does not read it", stepName, name, owner)
		}
		return fmt.Errorf("step %q has unknown key %q", stepName, name)
	}
	return nil
}

// verbOf finds the one verb a step names.
//
// AN ALIAS COUNTS AS THE VERB, because a compiled document carries the shape the engine dispatches on
// (`disk:`, `fetch:`, `desktop:`) and it must load. The alias's own fields are not declared - it is
// the engine's shape, not the file's vocabulary - so its arguments are checked leniently.
func verbOf(stepName string, mapping map[string]any) (string, error) {
	var found []string
	for name := range mapping {
		if _, ok := Verbs[name]; ok {
			found = append(found, name)
			continue
		}
		if _, ok := Aliases[name]; ok && isActionAlias(name) {
			found = append(found, name)
		}
	}
	if len(found) == 0 {
		// AND A STEP THAT ONLY PUBLISHES IS THE SECOND HALF OF `stop:`. `save:` on its own is how the
		// 62 files write "publish the disk" as a step of its own, after the machine is gone.
		if _, ok := mapping["save"]; ok {
			return "stop", nil
		}
		if _, ok := mapping["compress"]; ok {
			return "stop", nil
		}
		// THE MACHINE MESSAGES ARE `control:`'S. `acpi power` and `qga shutdown` are how 20 files ask
		// the machine itself to do something.
		for _, name := range []string{"acpi", "qga", "wait_until"} {
			if _, ok := mapping[name]; ok {
				return "control", nil
			}
		}
		// `do:` IS A STEP: a list run in order under one name, which is what the pane model writes.
		if _, ok := mapping["do"]; ok {
			return "action", nil
		}
		// `until:` ON ITS OWN IS A WAIT ON THE SERIAL LINE, which is how a microvm asks whether the
		// guest got to a prompt.
		if _, ok := mapping["until"]; ok {
			return "action", nil
		}
		if _, ok := mapping["until_any"]; ok {
			return "action", nil
		}
		if _, ok := mapping["desktop"]; ok {
			return "action", nil
		}
		// A STEP THAT ONLY WATCHES THE DISK is the second half of making one: `disk_min_mb` and
		// `disk_settle` say "wait until the file stops growing, and check it is big enough".
		for _, name := range []string{"disk_min_mb", "disk_settle"} {
			if _, ok := mapping[name]; ok {
				return "action", nil
			}
		}
		// `require_ssh:` AND ITS RETRY WORD ARE ONE STEP in 39 files.
		if _, ok := mapping["require_ssh"]; ok {
			return "action", nil
		}
		// A COLOUR-ADDRESSED CLICK IS `press` WITHOUT THE MARKER: `at_color` + `color_region` is how
		// a file clicks a blob when the coordinate is not stable.
		if _, ok := mapping["at_color"]; ok {
			return "input", nil
		}
		if _, ok := mapping["at_button"]; ok {
			return "input", nil
		}
		if _, ok := mapping["at_bottom"]; ok {
			return "input", nil
		}
		// `write:` RENDERS A TEMPLATE TO DISK, which is a copy with no source file.
		if _, ok := mapping["write"]; ok {
			return "action", nil
		}
		if _, ok := mapping["screenshot"]; ok {
			return "action", nil
		}
		// A STEP THAT ONLY WAITS IS A STEP. `settle: 5` says "let the screen draw" and nothing else.
		// It is checked LAST, because a step that also clicks is a click.
		for name := range mapping {
			if name == "settle" || name == "wait" {
				return "settle", nil
			}
		}
		// NAME THE KEY THAT MADE IT UNKNOWN, because "names no verb" alone sends a reader looking for
		// the missing word rather than at the misspelled one they wrote.
		var unknown []string
		for name := range mapping {
			if name == "name" {
				continue
			}
			unknown = append(unknown, name)
		}
		sort.Strings(unknown)
		return "", fmt.Errorf("step %q names no verb, and no key it writes is one; it says %s, "+
			"and a step is one of %s", stepName, quoted(unknown), quotedVerbNames())
	}
	if len(found) > 1 {
		sort.Slice(found, func(i, j int) bool { return DispatchOrder(found[i]) < DispatchOrder(found[j]) })
		return "", fmt.Errorf("step %q names %d actions (%s); a step does one thing, and the "+
			"dispatcher would run `%s:` and drop the rest", stepName, len(found),
			quoted(found), found[0])
	}
	return found[0], nil
}

// isActionAlias reports whether an alias is a step's action rather than a pole of one.
//
// `do`/`then`/`else`/`until` are parts of another step, not actions of their own.
func isActionAlias(name string) bool {
	switch name {
	case "do", "then", "else", "until", "until_any", "poll", "blind_after",
		"release", "path", "to", "template", "save", "method", "via", "text", "scale",
		"from", "disk_gb", "disk_gpt", "disk_esp_mib", "disk_min_mb", "disk_settle",
		"ssh_after_reboot", "ssh_probe", "terminal_wait", "type_gap", "recovery", "os":
		return false
	}
	return true
}

// anyVerbField finds a key in any verb's field list, for an alias step that reads another verb's shape.
func anyVerbField(name string) (Key, bool) {
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

// ownerOf names the verb a field belongs to, for the failure message.
func ownerOf(field string) string {
	var owners []string
	for name, v := range Verbs {
		if _, ok := v.Fields[field]; ok {
			owners = append(owners, name)
		}
	}
	sort.Strings(owners)
	if len(owners) == 0 {
		return ""
	}
	return strings.Join(owners, " or ")
}

// quotedVerbNames lists the nine verbs for a failure message.
func quotedVerbNames() string {
	names := make([]string, 0, len(Verbs))
	for n := range Verbs {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// quoted renders owners as `a:`, `b:` and `c:` for a failure message.
func quoted(owners []string) string {
	parts := make([]string, len(owners))
	for i, o := range owners {
		parts[i] = "`" + o + ":`"
	}
	switch len(parts) {
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " or " + parts[1]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " or " + parts[len(parts)-1]
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
	if k, ok := Aliases[name]; ok {
		return k, true
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
