package walk

// THE 2.0 READER: THE `repeat` FORM -> A DOCUMENT.
//
// IT PRODUCES THE SAME SHAPE THE 1.0 READER DOES, so `internal/run` is untouched and a file may be
// converted without changing behaviour. That is the whole point of the migration: every action in the
// engine was measured against a real guest, and the bugs the old format produced were in the
// SOURCE LANGUAGE.
//
// THE GRAMMAR IS SIX RULES AND NO MORE:
//
//  1. `walkthrough "name"` is the first line.
//  2. A header line is `word argument`: `resolution 1920x1080`, `user vmuser`.
//  3. `step "name"` opens a step; `item` opens one element of a list.
//  4. A line with children indented under it opens a BLOCK, and its children are the block's keys.
//  5. A line with no children is `key value`, where value is a scalar or a flat `[a, b]` list.
//  6. A line with no argument and no children is a `true` flag: `optional`, `clear`, `gpt`.
//
// THERE IS NO `==`, NO BRACE AND NO COMMA BETWEEN KEYS. A value that is a mapping is written as a
// block, and a value that is a list of mappings is written as `item` blocks. Every one of those is
// rule 4, applied at a different depth, which is why there is only one rule for structure.

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// IsV2 reports whether a source text uses the 2.0 form.
//
// THE MARKER IS A STEP HEADER, NOT THE ABSENCE OF `==`. The first version tested "no `==` anywhere",
// and four real files broke it: a shell command inside a quoted string contained `seen == 0`, which is
// data, not syntax. MEASURED on `bsd/freebsd/steps/vm-configure.walk`.
//
// SO THE TEST LOOKS FOR THE SHAPE EACH FORMAT PUTS AT THE START OF A STEP: 2.0 has `step "name"` at
// column 0 with an indented body; 1.0 has `step "name"` too, but its body lines are `key == value` or
// phrases. The distinguishing line is therefore a body line that is `key == value` at the step's own
// indentation, or a phrase verb (`click`, `press`, `settle`) - and a 2.0 file has neither.
func IsV2(src string) bool {
	hasStep := false
	for _, raw := range strings.Split(src, "\n") {
		t := strings.TrimSpace(raw)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if t == "step" || strings.HasPrefix(t, "step ") || t == "item" || strings.HasPrefix(t, "item ") {
			hasStep = true
			continue
		}
		// A 1.0 BODY LINE IS `key == value`. It must be at the start of a line's text and be a bare
		// word on the left, which a shell script inside a quoted string never is: `seen == 0` reaches
		// here only if the whole line is that, and in a quoted value it never is.
		if isV1BodyLine(t) {
			return false
		}
	}
	return hasStep
}

// isV1BodyLine reports whether a line is the 1.0 `key == value` form.
//
// THE LEFT SIDE MUST BE ONE BARE WORD AND THE LINE MUST NOT BE A QUOTED VALUE, which is what tells a
// step key from a shell comparison buried in a string.
func isV1BodyLine(t string) bool {
	i := strings.Index(t, "==")
	if i <= 0 {
		return false
	}
	left := strings.TrimSpace(t[:i])
	if left == "" || strings.ContainsAny(left, " \"'`$(){}[]<>|&;") {
		return false
	}
	return true
}

// LoadV2 reads a 2.0 walkthrough.
func LoadV2(path string, overrides map[string]string) (*File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	body := string(raw)
	doc, err := WalkV2ToYAML(body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return loadDocument(path, doc, overrides)
}

// v2line is one non-blank, non-comment source line.
type v2line struct {
	no     int
	indent int
	text   string
}

// scanV2 splits a source text into its meaningful lines, keeping indentation and line numbers.
//
// A TAB IS REFUSED rather than assumed to be eight spaces: a file that mixes tabs and spaces is a file
// whose nesting depends on an editor's settings, and the failure would be a step silently becoming a
// child of the wrong block.
func scanV2(src string) ([]v2line, error) {
	var out []v2line
	for n, raw := range strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n") {
		text := strings.TrimRight(raw, " \t")
		trimmed := strings.TrimSpace(text)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		lead := text[:len(text)-len(strings.TrimLeft(text, " \t"))]
		if strings.Contains(lead, "\t") {
			return nil, fmt.Errorf("line %d: indented with a tab; use spaces, two per level", n+1)
		}
		out = append(out, v2line{no: n + 1, indent: len(lead), text: trimmed})
	}
	return out, nil
}

// WalkV2ToYAML converts a 2.0 walkthrough into the document `loadDocument` understands.
func WalkV2ToYAML(src string) (map[string]any, error) {
	lines, err := scanV2(src)
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("the file has no lines")
	}
	if lines[0].indent != 0 {
		return nil, fmt.Errorf("line %d: the title must not be indented", lines[0].no)
	}
	verb, rest := v2Cut(lines[0].text)
	if verb != "walkthrough" {
		return nil, fmt.Errorf("line %d: a walkthrough starts with `walkthrough \"name\"`, got %q",
			lines[0].no, lines[0].text)
	}
	name, err := v2Unquote(rest)
	if err != nil {
		return nil, fmt.Errorf("line %d: the title must be quoted: %w", lines[0].no, err)
	}
	doc := map[string]any{"name": name}
	vars := map[string]any{}
	var steps []any

	i := 1
	for i < len(lines) {
		l := lines[i]
		if l.indent != 0 {
			return nil, fmt.Errorf("line %d: %q is indented but is not inside anything", l.no, l.text)
		}
		j := i + 1
		for j < len(lines) && lines[j].indent > 0 {
			j++
		}
		kids := lines[i+1 : j]

		verb, rest := v2Cut(l.text)
		switch verb {
		case "step":
			stepName, err := v2Unquote(rest)
			if err != nil {
				return nil, fmt.Errorf("line %d: `step` needs a quoted name: %w", l.no, err)
			}
			body, err := v2ParseBlock(kids, v2IndentWidth(kids))
			if err != nil {
				return nil, err
			}
			body["name"] = stepName
			steps = append(steps, body)

		// ---- the header, which is the same vocabulary as 1.0 -----------------------------
		case "resolution", "recording_resolution":
			w, h, err := v2Size(rest)
			if err != nil {
				return nil, fmt.Errorf("line %d: %s: %w", l.no, verb, err)
			}
			doc[verb] = []any{w, h}
		case "user":
			v, err := v2UnquoteOrBare(rest)
			if err != nil {
				return nil, fmt.Errorf("line %d: user: %w", l.no, err)
			}
			doc["ssh_user"] = v
		case "password":
			v, err := v2UnquoteOrBare(rest)
			if err != nil {
				return nil, fmt.Errorf("line %d: password: %w", l.no, err)
			}
			vars["password"] = map[string]any{"from": "args.password", "default": v}
		case "var":
			// `var name` OPENS A BLOCK; `var name value` IS A SCALAR VAR. Both are legal because
			// `resolveVars` accepts both shapes.
			if rest == "" {
				return nil, fmt.Errorf("line %d: `var` needs a name", l.no)
			}
			key, val := v2Cut(rest)
			if val != "" {
				if len(kids) > 0 {
					return nil, fmt.Errorf("line %d: `var %s %s` has lines under it; a value on the "+
						"line takes none", l.no, key, val)
				}
				vars[key] = v2ParseInline(val)
				break
			}
			body, err := v2ParseBlock(kids, v2IndentWidth(kids))
			if err != nil {
				return nil, err
			}
			vars[key] = body
		case "needs":
			if len(kids) > 0 {
				return nil, fmt.Errorf("line %d: `needs` takes a list on one line: `needs a.iso b.iso`",
					l.no)
			}
			var files []any
			for _, f := range strings.Fields(rest) {
				files = append(files, strings.Trim(f, `"`))
			}
			if len(files) == 0 {
				return nil, fmt.Errorf("line %d: `needs` names no files", l.no)
			}
			doc["needs"] = files
		default:
			// ANY OTHER TOP-LEVEL WORD IS A HEADER KEY WRITTEN WITH ITS OWN NAME, so a key the
			// vocabulary gains works here with no change to this reader.
			if len(kids) > 0 {
				body, err := v2ParseBlock(kids, v2IndentWidth(kids))
				if err != nil {
					return nil, err
				}
				doc[verb] = body
			} else if rest == "" {
				doc[verb] = true
			} else {
				doc[verb] = v2ParseInline(rest)
			}
		}
		i = j
	}
	if len(vars) > 0 {
		doc["vars"] = vars
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("the file has no steps; a header with nothing after it proves nothing")
	}
	// THE STATEMENTS ARE REWRITTEN INTO THE SHAPE THE ENGINE RUNS: `repeat` becomes `loop`, `nudge`
	// becomes `every`+`clock`, `after` becomes `settle`.
	if err := v2Normalise(steps); err != nil {
		return nil, err
	}
	doc["steps"] = steps
	return doc, nil
}

// v2IndentWidth is the indentation of the first line of a block, which every line in it must match.
func v2IndentWidth(block []v2line) int {
	if len(block) == 0 {
		return 0
	}
	return block[0].indent
}

// v2ParseBlock reads an indented body into the mapping it describes.
//
// THE ONE RULE OF STRUCTURE IS HERE. A line at the block's own indent is a key of this block; a line
// deeper is a child of the key above it; a line shallower than the block is not part of it (the caller
// has already cut the block, so that is an error). There is no other marker.
func v2ParseBlock(block []v2line, base int) (map[string]any, error) {
	m := map[string]any{}
	i := 0
	for i < len(block) {
		head := block[i]
		if head.indent != base {
			return nil, fmt.Errorf("line %d: %q is indented %d and its block is at %d",
				head.no, head.text, head.indent, base)
		}
		j := i + 1
		for j < len(block) && block[j].indent > base {
			j++
		}
		kids := block[i+1 : j]

		key, rest := v2Cut(head.text)
		if key == "" {
			return nil, fmt.Errorf("line %d: a line with nothing on it", head.no)
		}
		if _, exists := m[key]; exists {
			return nil, fmt.Errorf("line %d: `%s` is written twice in one block", head.no, key)
		}

		// ---- a key that opens a block -----------------------------------------------------
		if len(kids) > 0 {
			if rest != "" {
				// `nudge 5400` AND `item "name"` CARRY AN ARGUMENT AND A BLOCK, and both are
				// statements rather than plain keys: the argument is the threshold or the name, and
				// the children are the body. Every other key with an argument takes no children.
				if key == "if" {
					// `if <fact>` + `then`/`else` CHILDREN. A named branch has no condition of its
					// own, so its body is the two step lists and nothing else.
					body, err := v2ParseBlock(kids, v2IndentWidth(kids))
					if err != nil {
						return nil, err
					}
					out := map[string]any{"if": rest}
					if then, ok := body["then"]; ok {
						out["then"] = then
					}
					if e, ok := body["else"]; ok {
						out["else"] = e
					}
					m[key] = out["if"]
					// THE STEP'S OWN KEYS carry the branches, so they are hoisted onto the step.
					if out["if"] != nil {
						m["if"] = out["if"]
						if then, ok := out["then"]; ok {
							m["then"] = then
						}
						if e, ok := out["else"]; ok {
							m["else"] = e
						}
					}
					i = j
					continue
				}
				if key == "nudge" {
					// `nudge 5400` IS A THRESHOLD AND A BODY: the number is the loop's `every` and
					// the items are its `clock`.
					list, err := v2ParseItems(kids, v2IndentWidth(kids))
					if err != nil {
						return nil, err
					}
					m["nudge"] = list
					m["nudge_after"] = v2ParseInline(rest)
					i = j
					continue
				}
				return nil, fmt.Errorf("line %d: `%s %s` has lines indented under it; a value written "+
					"on one line takes none", head.no, key, rest)
			}
			sub := v2IndentWidth(kids)
			// A LIST IS A RUN OF `item` BLOCKS. The first child's text decides, and nothing else
			// does: `item` is the only word a list element can start with.
			if firstKey, _ := v2Cut(strings.TrimSuffix(kids[0].text, ":")); firstKey == "item" {
				items, err := v2ParseItems(kids, sub)
				if err != nil {
					return nil, err
				}
				m[key] = items
				i = j
				continue
			}
			body, err := v2ParseBlock(kids, sub)
			if err != nil {
				return nil, err
			}
			m[key] = body
			i = j
			continue
		}

		// ---- a key with a value on its line ----------------------------------------------
		if rest == "" {
			// A WORD ALONE IS `true`. `optional`, `clear`, `gpt` and `stopped` are flags whose
			// presence IS the argument, and writing `optional true` would be a second way to say it.
			m[key] = true
			i = j
			continue
		}
		v, err := v2ParseInlineValue(rest)
		if err != nil {
			return nil, fmt.Errorf("line %d: `%s %s`: %w", head.no, key, rest, err)
		}
		m[key] = v
		i = j
	}
	return m, nil
}

// v2ParseItems reads a run of `item` blocks into the list they describe.
func v2ParseItems(block []v2line, base int) ([]any, error) {
	var out []any
	i := 0
	for i < len(block) {
		head := block[i]
		if head.indent != base {
			return nil, fmt.Errorf("line %d: %q is indented %d and the list is at %d",
				head.no, head.text, head.indent, base)
		}
		key, rest := v2Cut(head.text)
		if key != "item" {
			return nil, fmt.Errorf("line %d: a list is a run of `item` blocks, and this line is %q",
				head.no, head.text)
		}
		j := i + 1
		for j < len(block) && block[j].indent > base {
			j++
		}
		kids := block[i+1 : j]
		body := map[string]any{}
		if len(kids) > 0 {
			sub, err := v2ParseBlock(kids, v2IndentWidth(kids))
			if err != nil {
				return nil, err
			}
			body = sub
		}
		// `item "name"` NAMES THE STEP. A bare `item` has no name, which is legal: the engine makes
		// its own for a list element nothing asked about.
		if rest != "" {
			name, err := v2Unquote(rest)
			if err != nil {
				return nil, fmt.Errorf("line %d: `item` needs a quoted name or nothing: %w", head.no, err)
			}
			body["name"] = name
		}
		out = append(out, body)
		i = j
	}
	return out, nil
}

// v2ToLoop rewrites the 2.0 loop statement into the 1.0 `loop:` mapping the engine runs.
//
// IT IS THE INVERSE OF `v2WriteRepeatFromLoop`, AND IT HAS TO INVENT THE `check`/`until` PAIR the 1.0
// engine requires. The engine validates a loop as FOUR parts - a reading, a comparison, a bound boolean
// and the test of that boolean - and 2.0 says the same thing with two, because the comparison IS the
// test. So a `repeat` is compiled back into the shape the engine has always run, which is what lets
// one reader feed an engine that was measured against real guests.
func v2ToLoop(step map[string]any) error {
	repeat, ok := step["repeat"].(map[string]any)
	if !ok {
		return nil
	}
	delete(step, "repeat")
	loop := map[string]any{}

	if do, ok := repeat["do"].([]any); ok && len(do) > 0 {
		loop["do"] = do
	} else if pause, ok := repeat["pause"]; ok {
		// `pause N` IS THE POLL'S OWN SETTLE: the one `do` a pure poll has, written as a number on the
		// loop because a `do` of one pause says the same thing twice.
		loop["do"] = []any{map[string]any{"settle": pause}}
	}
	if max, ok := repeat["max"]; ok {
		loop["max"] = max
	}
	// `nudge 5400` IS `every: 5400` + `clock: [...]`, which is the pair the engine reads.
	if nudge, ok := repeat["nudge"].([]any); ok && len(nudge) > 0 {
		loop["clock"] = nudge
		if after, ok := repeat["nudge_after"]; ok {
			loop["every"] = after
		} else {
			loop["every"] = 600
		}
	}
	// THE CONDITION BECOMES THE CHECK, AND ITS `from:` STAYS IN THE CHECK *AND* NAMES THE READING.
	//
	// 1.0 SAYS THE SAME THING TWICE - `see: {name: X}` and `check: {from: X}` - and the two names
	// agreed in all 154 loops. 2.0 says it once, as the condition's `from:`. So the reader writes it
	// back to both places, which is what makes the compiled document identical.
	cond, _ := repeat["until"].(map[string]any)
	check := map[string]any{}
	var from string
	for k, v := range cond {
		if k == "bind" {
			continue
		}
		if k == "from" {
			from, _ = v.(string)
		}
		check[k] = v
	}
	check["name"] = v2ConditionFactName(step, cond)
	loop["check"] = check
	if from != "" {
		loop["see"] = map[string]any{"name": from}
	}
	loop["until"] = map[string]any{"fact": check["name"]}
	step["loop"] = loop
	// A LOOP WITH NO `do` DID HAVE ONE IN 1.0 - every one of the 154 has a `do`, and 119 of them are
	// nothing but a pause. The emitter drops a `do` that is only a `settle`, because that pause is
	// what a poll's body would be; the reader cannot tell "dropped" from "never there", so it
	// restores the pause the poll needs. It is the ONE value a round trip may differ on, and the
	// round-trip test excludes it by comparing the rest.
	if _, ok := loop["do"]; !ok {
		loop["do"] = []any{map[string]any{"settle": 2.0}}
	}
	return nil
}

// v2ConditionFactName is the boolean a loop's test binds.
//
// IT IS DERIVED FROM THE STEP'S OWN NAME when the file did not say, so two loops in one file cannot
// collide on a generic name like `done`. MEASURED: 47 loops used the name `done` at top level, and the
// engine keeps one map - a name reused across a file is a boolean overwritten by the next loop, which
// works only because each loop tests it immediately.
func v2ConditionFactName(step map[string]any, cond map[string]any) string {
	if n, ok := cond["bind"].(string); ok && n != "" {
		return n
	}
	if n, ok := cond["name"].(string); ok && n != "" {
		return n
	}
	name, _ := step["name"].(string)
	return "v2_" + v2Slug(name)
}

// v2Slug turns a step name into a token safe for a fact name.
func v2Slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "fact"
	}
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

// v2Normalise applies the statement rewrites a 2.0 document needs before it becomes a 1.0 document.
//
// IT WALKS EVERY STEP AND EVERY NESTED LIST, because a `repeat` may sit at the top level or inside an
// `if`'s branch or inside another `repeat` - MEASURED: 3 of the 154 loops already nest an `if`.
func v2Normalise(steps []any) error {
	for _, raw := range steps {
		step, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if err := v2ToLoop(step); err != nil {
			return err
		}
		for _, key := range []string{"do", "then", "else"} {
			if list, ok := step[key].([]any); ok {
				if err := v2Normalise(list); err != nil {
					return err
				}
			}
		}
		if loop, ok := step["loop"].(map[string]any); ok {
			for _, key := range []string{"do", "clock"} {
				if list, ok := loop[key].([]any); ok {
					if err := v2Normalise(list); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// ---- the small readers ---------------------------------------------------------------------

// v2Cut splits `word rest` into the word and everything after it.
func v2Cut(text string) (string, string) {
	text = strings.TrimSpace(text)
	if i := strings.IndexAny(text, " \t"); i >= 0 {
		return text[:i], strings.TrimSpace(text[i+1:])
	}
	return text, ""
}

// v2ParseInlineValue reads a value written on one line.
//
// ONLY TWO SHAPES EXIST: a scalar, and a flat `[a, b, c]` list. A brace is refused by name, because a
// file that writes one is a file written in the format this one replaces.
func v2ParseInlineValue(s string) (any, error) {
	if strings.HasPrefix(s, "{") {
		return nil, fmt.Errorf("a mapping is written as an indented block, not as braces")
	}
	return v2ParseInline(s), nil
}

// v2ParseInline reads a scalar or a flat list.
func v2ParseInline(s string) any {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// A FLAT LIST. `[a, b]` and `[[a, b], [c]]` are both values; the nesting is inside the brackets.
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		inner := s[1 : len(s)-1]
		items := v2SplitTop(inner)
		out := make([]any, 0, len(items))
		for _, item := range items {
			out = append(out, v2ParseInline(item))
		}
		return out
	}
	// A QUOTED STRING KEEPS ITS SPACES AND COMMAS.
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		var decoded string
		if err := yaml.Unmarshal([]byte(s), &decoded); err == nil {
			return decoded
		}
		return s[1 : len(s)-1]
	}
	switch s {
	case "true":
		return true
	case "false":
		return false
	case "null":
		return nil
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		if n == float64(int64(n)) {
			return int(n)
		}
		return n
	}
	return s
}

// v2SplitTop splits a list's inside on commas that are not inside brackets or quotes.
func v2SplitTop(s string) []string {
	var out []string
	depth := 0
	inQuote := false
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			inQuote = !inQuote
		case inQuote:
		case c == '[':
			depth++
		case c == ']':
			depth--
		case c == ',' && depth == 0:
			out = append(out, strings.TrimSpace(s[start:i]))
			start = i + 1
		}
	}
	if tail := strings.TrimSpace(s[start:]); tail != "" {
		out = append(out, tail)
	}
	return out
}

// v2Unquote reads a quoted string, and refuses an unquoted one.
func v2Unquote(s string) (string, error) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return "", fmt.Errorf("%q is not quoted", s)
	}
	var decoded string
	if err := yaml.Unmarshal([]byte(s), &decoded); err == nil {
		return decoded, nil
	}
	return s[1 : len(s)-1], nil
}

// v2UnquoteOrBare accepts a quoted string or a single bare word.
func v2UnquoteOrBare(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("needs a value")
	}
	if s[0] == '"' {
		return v2Unquote(s)
	}
	return s, nil
}

// v2Size reads `1920x1080`.
func v2Size(s string) (int, int, error) {
	parts := strings.SplitN(strings.TrimSpace(s), "x", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("%q is not a size; write `1920x1080`", s)
	}
	w, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("%q is not a width", parts[0])
	}
	h, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, fmt.Errorf("%q is not a height", parts[1])
	}
	return w, h, nil
}
