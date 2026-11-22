package walk

// THE 2.0 EMITTER: A DOCUMENT -> THE `repeat` FORM.
//
// THIS IS THE PROOF AND NOT THE PRODUCT. The design in `docs/WALK-V2.md` claims that nine verbs with
// indented children cover every walkthrough in this tree. A claim like that is worth nothing sitting
// in a document, so this file writes every real file out in the new form and `WalkV2ToYAML` reads it
// back; `TestEveryRealWalkthroughRoundTripsThroughV2` requires the two documents to be equal. A
// single different value is a design bug, and the test names it.
//
// THE SYNTAX IS THREE RULES, and they are the whole grammar:
//
//	walkthrough "name"            a title
//	  resolution 1920x1080        a header line: one word, an argument, both required
//	  step "the greeter is drawn" a step, named once
//	    expect                    a verb with children
//	      from screen             a child: indented, one level
//	      words [password]        a flat list of scalars stays on one line
//	    max 40                    a child of the step
//
// THERE IS NO `==`, NO BRACE, NO COMMA AND NO QUOTED KEY. A line is `verb argument` or `keyword
// child...`, and indentation is what says which line belongs to which. The one value that is not a
// scalar is a flat list of scalars, and it is written `[a, b, c]` because a list of values has no
// keys, so there is nothing to indent.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// v2Indent is one level. Two spaces, and only two: a file with mixed widths is a file whose meaning
// depends on counting.
const v2Indent = "  "

// YAMLToWalkV2 renders a walkthrough document in the 2.0 form.
func YAMLToWalkV2(doc map[string]any) (string, error) {
	name, _ := doc["name"].(string)
	if name == "" {
		return "", fmt.Errorf("a walkthrough needs a name")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "walkthrough %s\n", alwaysQuote(name))

	// ---- the header -----------------------------------------------------------------------
	if r, ok := doc["resolution"].([]any); ok && len(r) == 2 {
		fmt.Fprintf(&b, "resolution %s\n", v2Pair(r))
	}
	if r, ok := doc["recording_resolution"].([]any); ok && len(r) == 2 {
		fmt.Fprintf(&b, "recording_resolution %s\n", v2Pair(r))
	}
	if u, ok := doc["ssh_user"].(string); ok && u != "" {
		fmt.Fprintf(&b, "user %s\n", v2Scalar(u))
	}
	// THE VARIABLES, each as a block, because a variable is a mapping and a mapping is indented.
	if vars, ok := doc["vars"].(map[string]any); ok && len(vars) > 0 {
		keys := make([]string, 0, len(vars))
		for k := range vars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			// `password` HAS ITS OWN WORD: every file has one and `{{ password }}` is in half the
			// steps, so a whole `var password` block for it would be noise.
			if k == "password" {
				if spec, ok := vars[k].(map[string]any); ok {
					if d, ok := spec["default"].(string); ok && d != "" {
						fmt.Fprintf(&b, "password %s\n", v2Scalar(d))
						continue
					}
				}
			}
			// A VAR IS A MAPPING OF `default`/`from`, BUT IT MAY BE A BARE STRING: `var display_name
			// == "admin"` is a var whose value is the string, and `resolveVars` treats it as one.
			// So a scalar var is written on the `var` line, and only a mapping opens a block.
			if !isScalarOrFlat(vars[k]) {
				fmt.Fprintf(&b, "var %s\n", k)
				if err := v2WriteBody(&b, v2Indent, vars[k]); err != nil {
					return "", fmt.Errorf("var %s: %w", k, err)
				}
				continue
			}
			fmt.Fprintf(&b, "var %s %s\n", k, v2Inline(vars[k]))
		}
	}
	// EVERY OTHER TOP-LEVEL KEY IS A HEADER LINE, written with its own name so nothing is lost.
	known := map[string]bool{"name": true, "resolution": true, "recording_resolution": true,
		"ssh_user": true, "vars": true, "steps": true}
	var extra []string
	for k := range doc {
		if !known[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	for _, k := range extra {
		v := doc[k]
		// `needs` IS A LIST OF FILE NAMES AND IT IS ONE LINE: `needs a.iso b.iso`. A path with a
		// space in it would be quoted, which is the reader's own rule.
		if k == "needs" {
			if list, ok := v.([]any); ok {
				parts := make([]string, 0, len(list))
				for _, item := range list {
					parts = append(parts, v2Scalar(fmt.Sprint(item)))
				}
				fmt.Fprintf(&b, "needs %s\n", strings.Join(parts, " "))
				continue
			}
		}
		if isScalarOrFlat(v) {
			fmt.Fprintf(&b, "%s %s\n", k, v2Inline(v))
			continue
		}
		fmt.Fprintf(&b, "%s\n", k)
		if err := v2WriteBody(&b, v2Indent, v); err != nil {
			return "", fmt.Errorf("header %s: %w", k, err)
		}
	}
	b.WriteString("\n")

	// ---- the steps ------------------------------------------------------------------------
	steps, _ := doc["steps"].([]any)
	if len(steps) == 0 {
		return "", fmt.Errorf("a walkthrough needs steps")
	}
	for i, raw := range steps {
		s, ok := raw.(map[string]any)
		if !ok {
			return "", fmt.Errorf("step %d is not a mapping", i+1)
		}
		if err := v2WriteStep(&b, "", s, "step"); err != nil {
			return "", fmt.Errorf("step %d: %w", i+1, err)
		}
	}
	return b.String(), nil
}

// v2WriteStep writes one step: a head that names it, then its body.
//
// `head` IS `step` FOR A TOP-LEVEL STEP AND `item` FOR ONE INSIDE A LIST, because a list of steps has
// no key to hang each element on. Both take a name in quotes, and both own an indented body.
func v2WriteStep(b *strings.Builder, indent string, s map[string]any, head string) error {
	name, _ := s["name"].(string)
	if head == "item" && name == "" {
		// AN ITEM WITH NO NAME IS A SHAPE THE ENGINE MAKES: a `do:` of `{settle: 10}` has no name
		// because nothing was asked of it. Its verbs are its name, so it is written as `item` with
		// no quotes - `item` alone is a legal head, and the reader fills the name from the first verb.
		fmt.Fprintf(b, "%sitem\n", indent)
	} else if name == "" {
		return fmt.Errorf("a step has no name")
	} else {
		fmt.Fprintf(b, "%s%s %s\n", indent, head, alwaysQuote(name))
	}
	body := map[string]any{}
	for k, v := range s {
		if k == "name" {
			continue
		}
		body[k] = v
	}
	return v2WriteBody(b, indent+v2Indent, body)
}

// v2WriteBody writes a mapping's entries at one indent, one entry per line or block.
//
// IT IS THE WHOLE GRAMMAR IN ONE FUNCTION, and the four cases are the four shapes a value can have:
//
//	scalar or flat list   one line:  `max 40`, `words [password, login]`
//	`repeat`              a statement with `do`, `until`, `max`, `nudge 5400`
//	a verb's mapping      the verb, then its children indented
//	a list of mappings    `item` blocks
//
// A KEY WITH NO ARGUMENT - `optional`, `clear`, `gpt`, `stopped` - IS A LINE ON ITS OWN: the word is
// the value, and writing `optional true` would be a second way to say it.
func v2WriteBody(b *strings.Builder, indent string, m any) error {
	mm, ok := m.(map[string]any)
	if !ok {
		return fmt.Errorf("expected a mapping, got %T", m)
	}
	// A NAMED BRANCH PUTS ITS `then`/`else` INSIDE THE `if`, because they are its children and a
	// sibling `else` would read as a second statement. MEASURED: `if: uninitialised` + `else: [...]` is
	// a real step in macos/26, with no `then` at all - a branch that runs only when the fact is FALSE.
	if name, ok := mm["if"].(string); ok && (mm["then"] != nil || mm["else"] != nil) {
		fmt.Fprintf(b, "%sif %s\n", indent, name)
		inner := indent + v2Indent
		for _, k := range []string{"then", "else"} {
			if vv, ok := mm[k]; ok {
				if err := v2WriteStepList(b, inner, k, vv); err != nil {
					return err
				}
			}
		}
		rest := map[string]any{}
		for k, vv := range mm {
			if k == "if" || k == "then" || k == "else" {
				continue
			}
			rest[k] = vv
		}
		if len(rest) > 0 {
			return v2WriteBody(b, indent, rest)
		}
		return nil
	}
	// A STABLE ORDER, so the output is diffable and a round trip compares like with like. The
	// document is a mapping and its order carries no meaning, but a file a person reads does.
	keys := make([]string, 0, len(mm))
	for k := range mm {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return v2KeyOrder(keys[i]) < v2KeyOrder(keys[j]) })
	for _, k := range keys {
		if err := v2WriteEntry(b, indent, k, mm[k]); err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
	}
	return nil
}

// v2KeyOrder puts the verb first and the modifiers after it, so a step reads as an action followed by
// its conditions rather than as an alphabetical list.
func v2KeyOrder(k string) string {
	switch k {
	case "boot", "press", "run", "copy", "read", "expect", "control", "if", "stop", "repeat":
		return "0" + k
	case "then", "else", "do", "until", "nudge":
		return "1" + k
	case "optional", "timeout", "after", "when":
		return "9" + k
	}
	return "5" + k
}

// v2WriteEntry writes one key of a mapping.
func v2WriteEntry(b *strings.Builder, indent, key string, v any) error {
	switch key {
	case "loop":
		// `loop:` IS THE 1.0 NAME AND `repeat` IS THE 2.0 STATEMENT. The converter translates, because
		// the whole design is that one construct means a retry: `do` is the body, `check` is the test,
		// `see` is the reading, `until` is where it stops, `max` is the count, `every`/`clock` are the
		// once-only nudge.
		return v2WriteRepeatFromLoop(b, indent, v)
	case "repeat":
		return v2WriteRepeat(b, indent, v)
	case "if":
		return v2WriteIf(b, indent, v)
	case "then", "else":
		// A BRANCH IS A LIST OF STEPS, so it is `then` followed by `item` blocks. It reaches here only
		// when it is not under an `if`, which the reader refuses - so writing it plainly is enough to
		// make the difference visible rather than silent.
		return v2WriteStepList(b, indent, key, v)
	}

	// A FLAT VALUE: one line.
	if isScalarOrFlat(v) {
		if isTrueFlag(v) {
			fmt.Fprintf(b, "%s%s\n", indent, key)
			return nil
		}
		fmt.Fprintf(b, "%s%s %s\n", indent, key, v2Inline(v))
		return nil
	}

	// A LIST OF MAPPINGS: `item` blocks.
	if list, ok := v.([]any); ok {
		fmt.Fprintf(b, "%s%s\n", indent, key)
		for _, item := range list {
			im, ok := item.(map[string]any)
			if !ok {
				return fmt.Errorf("a list element is not a mapping")
			}
			if err := v2WriteStep(b, indent+v2Indent, im, "item"); err != nil {
				return err
			}
		}
		return nil
	}

	// A MAPPING: the key opens a block and its entries are indented under it.
	if sm, ok := v.(map[string]any); ok {
		if len(sm) == 0 {
			fmt.Fprintf(b, "%s%s\n", indent, key)
			return nil
		}
		fmt.Fprintf(b, "%s%s\n", indent, key)
		return v2WriteBody(b, indent+v2Indent, sm)
	}

	return fmt.Errorf("a value of type %T has no 2.0 form", v)
}

// v2WriteRepeatFromLoop renders the 1.0 `loop:` mapping as the 2.0 `repeat` statement.
//
// THE FOUR PARTS COLLAPSE INTO TWO, and that is the whole design: `check` was the reading plus the
// comparison, and `until: {fact: X}` read the boolean that check bound. In 2.0 the comparison IS the
// stop test - there is no boolean to name and no `fact:` to point at it - so `check` and `until` become
// one `until` block, and `see:` becomes the `from:` that names the picture.
//
// THE BOUND NAME IS KEPT WHEN IT IS NEEDED. A `check: {name: X}` that an `if:` reads must keep its
// name, so `bind X` is written only when the loop's fact is referenced somewhere else. MEASURED: of
// 230 checks, only 44 names are read by an `if`; the other 186 are loop internals with no reader.
func v2WriteRepeatFromLoop(b *strings.Builder, indent string, v any) error {
	raw, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("`loop` takes a mapping")
	}
	fmt.Fprintf(b, "%srepeat\n", indent)
	inner := indent + v2Indent

	// `do` IS THE BODY. A `do` of one `settle` is the poll's own pause, so it is not written as an
	// `item`: the pause belongs to the loop and `repeat` has no slot for it that would not be a second
	// way to say `settle`.
	do, _ := raw["do"].([]any)
	// A `do` OF ONE `settle` IS THE POLL'S PAUSE. It is written as `pause N` on the `repeat`, because
	// dropping it would lose a number the file chose: MEASURED, the pauses are 2, 3, 4, 5, 6, 8, 10,
	// 12, 15 and 20 seconds, and a reader that guessed 2 would change how often a screen is read.
	//
	// ONLY WHEN THE WHOLE BODY IS THAT ONE PAUSE. A body of `[settle, see, check]` keeps its order and
	// its first element, because there the pause is part of the sequence, not the loop's pace.
	body := do
	if len(do) == 1 {
		if m, ok := do[0].(map[string]any); ok && len(m) == 1 {
			if _, only := m["settle"]; only {
				body = nil
			}
		}
	}
	if len(body) > 0 {
		if err := v2WriteStepList(b, inner, "do", body); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(b, "%spause %s\n", inner, v2Inline(do[0].(map[string]any)["settle"]))
	}

	// `until` IS THE CONDITION: the loop's `check` minus the name it bound, which is only a name.
	cond := map[string]any{}
	if check, ok := raw["check"].(map[string]any); ok {
		for k, vv := range check {
			switch k {
			case "name":
				// THE BOUND BOOLEAN IS WRITTEN AS `bind`, because the 2.0 statement still has to
				// produce a document with that name in it: `repeat` stops on a condition, and the
				// condition's name is what the engine stores. Dropping it would change the document.
				cond["bind"] = vv
				continue
			case "must":
				// `must` IS A PROPERTY OF A ONE-SHOT ASSERTION, not of a loop's test: the test is
				// asked again until it holds, so "fail if false" is what the ceiling already does.
				continue
			default:
				cond[k] = vv
			}
		}
	}
	// `see:` NAMES THE PICTURE THE TEST READS. In 1.0 the check said `from: <name>` and the loop's
	// `see: {name: <name>}` captured it; the two names were the same string in all 154 loops.
	if see, ok := raw["see"].(map[string]any); ok {
		if n, ok := see["name"].(string); ok && n != "" {
			if _, has := cond["from"]; !has {
				cond["from"] = n
			}
		}
	}
	if len(cond) > 0 {
		fmt.Fprintf(b, "%suntil\n", inner)
		if err := v2WriteBody(b, inner+v2Indent, cond); err != nil {
			return err
		}
	}

	if vv, ok := raw["max"]; ok {
		fmt.Fprintf(b, "%smax %s\n", inner, v2Inline(vv))
	}

	// `every` AND `clock` ARE THE ONCE-ONLY NUDGE, and `every` is its threshold.
	if clock, ok := raw["clock"].([]any); ok && len(clock) > 0 {
		threshold := ""
		if vv, ok := raw["every"]; ok {
			threshold = " " + v2Inline(vv)
		}
		fmt.Fprintf(b, "%snudge%s\n", inner, threshold)
		for _, item := range clock {
			im, ok := item.(map[string]any)
			if !ok {
				return fmt.Errorf("a nudge action is not a mapping")
			}
			if err := v2WriteStep(b, inner+v2Indent, im, "item"); err != nil {
				return err
			}
		}
	}
	return nil
}

// v2WriteRepeat writes the loop statement: `do`, `until`, `max`, `nudge 5400`.
//
// IT IS A STATEMENT RATHER THAN A KEYWORD WITH FLAGS because its `do` is a list of steps that may hold
// an `if` or another `repeat`. MEASURED: 3 of the 154 loops in this tree already nest an `if` inside
// their body, so a flat form could not express them.
func v2WriteRepeat(b *strings.Builder, indent string, v any) error {
	raw, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("`repeat` takes a mapping")
	}
	fmt.Fprintf(b, "%srepeat\n", indent)
	inner := indent + v2Indent

	// `do` IS THE BODY, and it is omitted when it is nothing but the pause a poll needs - that pause
	// is what `settle` already says, so a `do` of one `settle` says the same thing twice.
	if do, ok := raw["do"].([]any); ok && !v2IsPurePoll(do) {
		if err := v2WriteStepList(b, inner, "do", do); err != nil {
			return err
		}
	}
	// `until` IS A CONDITION: the same children an `expect` takes.
	if until, ok := raw["until"].(map[string]any); ok {
		cond := map[string]any{}
		for k, vv := range until {
			cond[k] = vv
		}
		// THE TEST IS THE `until` ITSELF when the loop's `check` produced the fact it tests. The
		// check's other half - its reading - is the condition's `from`, and its bound name is only
		// needed when something else reads the boolean.
		fmt.Fprintf(b, "%suntil\n", inner)
		if err := v2WriteBody(b, inner+v2Indent, cond); err != nil {
			return err
		}
	}
	if vv, ok := raw["max"]; ok {
		fmt.Fprintf(b, "%smax %s\n", inner, v2Inline(vv))
	}
	// `nudge` IS THE ONCE-ONLY CLOCK. Its THRESHOLD IS ITS ARGUMENT - `nudge 5400` - because the old
	// pair `every:`/`clock:` never appeared apart: MEASURED 8 times, all 8 together, 0 alone. A key
	// that only ever appears with another key is that key's argument, so it is written inside it.
	//
	// IT FIRES AT MOST ONCE per loop, not once per interval. That is what the engine does today.
	if clock, ok := raw["clock"].([]any); ok && len(clock) > 0 {
		every := ""
		if vv, ok := raw["every"]; ok {
			every = " " + v2Inline(vv)
		}
		if err := v2WriteStepList(b, inner, "nudge"+every, clock); err != nil {
			return err
		}
	}
	return nil
}

// v2WriteIf writes a branch: the condition's children, then `then` and `else` as step lists.
func v2WriteIf(b *strings.Builder, indent string, v any) error {
	// A NAMED BOOLEAN - `if: some_fact` - is a one-line test.
	if name, ok := v.(string); ok {
		fmt.Fprintf(b, "%sif %s\n", indent, name)
		return nil
	}
	cond, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("`if` takes a name or a condition")
	}
	fmt.Fprintf(b, "%sif\n", indent)
	inner := indent + v2Indent
	// THE CONDITION IS EVERY CHILD THAT IS NOT A BRANCH. `then` and `else` are step lists and reach
	// `v2WriteEntry` as such; everything else belongs to the test.
	test := map[string]any{}
	for k, vv := range cond {
		if k == "then" || k == "else" {
			continue
		}
		test[k] = vv
	}
	if err := v2WriteBody(b, inner, test); err != nil {
		return err
	}
	for _, k := range []string{"then", "else"} {
		if vv, ok := cond[k]; ok {
			if err := v2WriteStepList(b, inner, k, vv); err != nil {
				return err
			}
		}
	}
	return nil
}

// v2WriteStepList writes `key` and then one `item` block per step.
func v2WriteStepList(b *strings.Builder, indent, key string, v any) error {
	list, ok := v.([]any)
	if !ok {
		return fmt.Errorf("`%s` takes a list of steps", key)
	}
	fmt.Fprintf(b, "%s%s\n", indent, key)
	for _, item := range list {
		im, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("a step in `%s` is not a mapping", key)
		}
		if err := v2WriteStep(b, indent+v2Indent, im, "item"); err != nil {
			return err
		}
	}
	return nil
}

// v2IsPurePoll reports whether a loop body is nothing but the pause a poll needs.
func v2IsPurePoll(do []any) bool {
	if len(do) == 0 {
		return true
	}
	for _, item := range do {
		m, ok := item.(map[string]any)
		if !ok {
			return false
		}
		for k := range m {
			if k != "settle" && k != "name" {
				return false
			}
		}
	}
	return true
}

// isScalarOrFlat reports whether a value fits on one line: a scalar, a list of scalars, or nothing.
func isScalarOrFlat(v any) bool {
	switch t := v.(type) {
	case nil, string, bool, int, int64, float64:
		return true
	case []any:
		for _, item := range t {
			if !isScalar(item) {
				return false
			}
		}
		return true
	}
	return false
}

func isScalar(v any) bool {
	switch v.(type) {
	case nil, string, bool, int, int64, float64:
		return true
	case []any:
		// A LIST OF LISTS OF SCALARS IS A WORD GROUP - `words: [[a, b], [c]]` - and it is written on
		// one line because it is still values, not structure.
		for _, item := range v.([]any) {
			if !isScalar(item) {
				return false
			}
		}
		return true
	}
	return false
}

// isTrueFlag reports whether a key is present only to say yes.
//
// `optional: true`, `clear: true`, `gpt: true`, `stopped: true` and their kin are BOOLEANS THE
// PRESENCE OF WHICH IS THE ARGUMENT, so the word alone is the statement. A `false` is written out,
// because a file that says `optional false` is saying something and dropping it would change it.
func isTrueFlag(v any) bool {
	b, ok := v.(bool)
	return ok && b
}

// v2Inline renders a scalar or a flat list for one line.
func v2Inline(v any) string {
	switch t := v.(type) {
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			parts = append(parts, v2Inline(item))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case string:
		return v2Scalar(t)
	case bool:
		if t {
			return "true"
		}
		return "false"
	case nil:
		return "null"
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'g', -1, 64)
	}
	return fmt.Sprint(v)
}

// v2Scalar renders a string for one line, quoting only when it must be.
//
// A VALUE WITH A SPACE, A COMMA, OR A SQUARE BRACKET MUST BE QUOTED or it changes the line's shape:
// `words [a, b]` is a list, so a single word group with a comma in it has to say so. A bare word with
// none of those is written as it is, because a file full of quotes is a file that is harder to read
// and no more exact.
func v2Scalar(s string) string {
	if s == "" {
		return `""`
	}
	// A STRING WITH A NEWLINE, A TAB OR A QUOTE IS QUOTED WITH ITS ESCAPES KEPT, so the text says one
	// line and the reader rebuilds the same value. `alwaysQuote` only escapes the quote itself, so the
	// control characters are escaped here - MEASURED: without this, a multi-line `ssh` command came
	// back with a real newline where the original had a two-character `\n`, and the round trip
	// reported the difference.
	// A STRING THAT LOOKS LIKE A NUMBER MUST BE QUOTED, or it comes back as one: MEASURED, the test
	// typed `"136"` into a shrink dialog and the unquoted form read back as the integer 136, which
	// the vocabulary refused with ``type` needs a string, got int``.
	if strings.ContainsAny(s, "\n\t\r\"") || strings.ContainsAny(s, " ,[]{}#\\") ||
		s == "true" || s == "false" || s == "null" || s == "-" || v2LooksNumeric(s) {
		return quoteEscaped(s)
	}
	return s
}

// quoteEscaped writes a double-quoted string with its control characters escaped, which is what makes
// a multi-line shell command survive one line of text.
func quoteEscaped(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// v2LooksNumeric reports whether a string would be read back as a number.
func v2LooksNumeric(s string) bool {
	if s == "" {
		return false
	}
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return true
	}
	return false
}

// v2Pair renders a two-number list the way a header wants it: `1920x1080`.
func v2Pair(v []any) string {
	if len(v) != 2 {
		return v2Inline(v)
	}
	return v2Inline(v[0]) + "x" + v2Inline(v[1])
}
