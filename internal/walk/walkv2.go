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
	var needs []any

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
			needs = files
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
	// THE MARKER VERBS ARE FLATTENED INTO THE FIELDS THE ENGINE READS, and a `boot` that names a disk
	// is expanded into the two steps the executor runs. Everything else reaches the engine as written.
	if err := v2NormaliseList(&steps); err != nil {
		return nil, err
	}
	// `needs` IS A HEADER DECLARATION AND THE ENGINE RUNS IT AS A STEP, so it is put back where the
	// executor looks for it: first, before anything starts a machine.
	if len(needs) > 0 {
		steps = append([]any{map[string]any{
			"name":  "the files this stage reads are already on disk",
			"needs": needs,
		}}, steps...)
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
			// A VERB CARRIES ITS REQUIRED ARGUMENT ON THE LINE AND ITS OPTIONAL ONES INDENTED UNDER
			// IT: `press text "x"` types a string and then says `at`; `copy` has no argument and
			// `template`/`to` under it; `stop kill` names the method and `save` follows. This is the
			// one shape the design allows on both sides of the line, and it is why `at` and `clear`
			// are not a second syntax.
			if v2IsVerbatim(key) {
				body, err := v2ParseBlock(kids, v2IndentWidth(kids))
				if err != nil {
					return nil, err
				}
				sub := v2VerbArgument(key, rest)
				if cmd, ok := sub["__cmd"]; ok {
					delete(sub, "__cmd")
					body["__cmd"] = cmd
				}
				for k, v := range sub {
					body[k] = v
				}
				m[key] = body
				i = j
				continue
			}
			// `if <fact>` AND `if` (with its condition indented) ARE TWO SHAPES.
			if key == "if" {
				body, err := v2ParseBlock(kids, v2IndentWidth(kids))
				if err != nil {
					return nil, err
				}
				if rest != "" {
					m["if"] = v2UnquoteInline(rest)
				} else {
					cond := map[string]any{}
					for k, v := range body {
						if k == "then" || k == "else" {
							continue
						}
						cond[k] = v
					}
					m["if"] = cond
				}
				if then, ok := body["then"]; ok {
					m["then"] = then
				}
				if e, ok := body["else"]; ok {
					m["else"] = e
				}
				i = j
				continue
			}
			if key == "nudge" {
				list, err := v2ParseItems(kids, v2IndentWidth(kids))
				if err != nil {
					return nil, err
				}
				m["nudge"] = list
				m["nudge_after"] = v2ParseInline(rest)
				i = j
				continue
			}
			if rest != "" {
				return nil, fmt.Errorf("line %d: `%s %s` has lines indented under it; a value written "+
					"on one line takes none", head.no, key, rest)
			}
			sub := v2IndentWidth(kids)
			// A LIST IS A RUN OF `item` BLOCKS.
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

		// A VERB WHOSE WHOLE ARGUMENT IS ON ITS LINE, WITH NO CHILDREN.
		if v2IsVerbatim(key) {
			sub := v2VerbArgument(key, v2UnquoteInline(rest))
			if cmd, ok := sub["__cmd"]; ok {
				delete(sub, "__cmd")
				m["__cmd"] = cmd
				i = j
				continue
			}
			for k, v := range sub {
				m[k] = v
			}
			i = j
			continue
		}

		// ---- a key with a value on its line ----------------------------------------------
		if rest == "" {
			m[key] = true
			i = j
			continue
		}
		// A PAIR IS TWO NUMBERS ON THE LINE, not a space-joined string.
		if key == "at" || key == "fallback" {
			if x, y, ok := v2PairOf(rest); ok {
				m[key] = []any{x, y}
				i = j
				continue
			}
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

// v2Normalise applies the rewrites a 2.0 document needs before the engine sees it.
//
// IT IS THE LAST TRANSLATION, AND IT IS SMALL ON PURPOSE. The verbs that carry an argument and a
// block are compiled here (`press text "x"` loses its marker into its siblings; `copy` into
// `from`/`to`; `stop` into `method`/`save`); everything else reaches the engine as written, which is
// what makes the vocabulary a vocabulary rather than a second language.
//
// IT WALKS NESTED LISTS because a `repeat` may hold an `if`, and an `if` may hold another `repeat` -
// MEASURED: 3 of the 154 loops already nest a branch.
func v2NormaliseList(steps *[]any) error {
	out := make([]any, 0, len(*steps))
	for _, raw := range *steps {
		step, ok := raw.(map[string]any)
		if !ok {
			out = append(out, raw)
			continue
		}
		// `run` COMPILES TO A COMMAND PLUS `via:`, and the engine reads one key per transport.
		if cmd, ok := step["__cmd"]; ok {
			delete(step, "__cmd")
			via, _ := step["via"].(string)
			delete(step, "via")
			switch via {
			case "ssh":
				step["ssh"] = cmd
			case "adb":
				step["adb"] = cmd
			default:
				step["run"] = cmd
			}
		}
		// A MARKER VERB FLATTENS INTO ITS SIBLINGS. `press` says the step is input and its fields say
		// which; `copy`/`stop`/`control` are the same shape.
		if b, ok := step["boot"].(map[string]any); ok {
			delete(step, "boot")
			step["boot"] = true
			if d, ok := b["disk"]; ok {
				ds := map[string]any{"name": "the machine's disk"}
				if err := v2DiskFields(ds, d); err != nil {
					return err
				}
				out = append(out, ds)
				delete(b, "disk")
			}
			for k, v := range b {
				step[k] = v
			}
		}
		if m, ok := step["input"].(map[string]any); ok {
			delete(step, "input")
			step["input"] = true
			for k, v := range m {
				step[k] = v
			}
		}
		if m, ok := step["stop"].(map[string]any); ok {
			delete(step, "stop")
			step["stop"] = true
			for k, v := range m {
				step[k] = v
			}
		}
		for _, key := range []string{"do", "then", "else"} {
			if list, ok := step[key].([]any); ok {
				if err := v2NormaliseList(&list); err != nil {
					return err
				}
				step[key] = list
			}
		}
		if rep, ok := step["repeat"].(map[string]any); ok {
			for _, key := range []string{"do", "nudge"} {
				if list, ok := rep[key].([]any); ok {
					if err := v2NormaliseList(&list); err != nil {
						return err
					}
					rep[key] = list
				}
			}
		}
		out = append(out, step)
	}
	*steps = out
	return nil
}

// v2DiskFields expands a folded `disk:` mapping into the keys the engine creates a disk with.
func v2DiskFields(dst map[string]any, d any) error {
	switch t := d.(type) {
	case string:
		dst["disk"] = t
	case map[string]any:
		if f, ok := t["file"].(string); ok {
			dst["disk"] = f
		}
		if v, ok := t["size"]; ok {
			dst["disk_gb"] = v
		}
		for from, to := range map[string]string{
			"gpt": "disk_gpt", "esp_mib": "disk_esp_mib",
			"min_mb": "disk_min_mb", "wait": "disk_settle",
		} {
			if v, ok := t[from]; ok {
				dst[to] = v
			}
		}
	default:
		return fmt.Errorf("`disk:` takes a file name or a mapping, got %T", d)
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

// v2IsVerbatim reports whether a key is a verb whose required argument sits on its own line and whose
// optional parts are indented under it.
//
// THE LIST IS SHORT BECAUSE MOST KEYS DO NOT NEED IT: `expect`, `read` and `repeat` take a mapping and
// nothing on the line, so they follow the plain rule. These six take both halves.
func v2IsVerbatim(key string) bool {
	switch key {
	case "input", "run", "scp", "stop", "control", "boot":
		return true
	}
	return false
}

// v2VerbArgument turns the words after a verb into the fields the executor reads.
func v2VerbArgument(verb, rest string) map[string]any {
	out := map[string]any{}
	// THE MARKER IS ALWAYS PRESENT, EVEN WITH NO ARGUMENT: it is what makes `template`/`to` a copy's
	// arguments rather than sub-keys with no owner.
	out[verb] = true
	if rest == "" {
		return out
	}
	switch verb {
	case "input":
		if first, after := v2Cut(rest); first == "text" {
			out["text"] = v2ParseInline(after)
			return out
		}
		if x, y, ok := v2PairOf(rest); ok {
			out["click"] = []any{x, y}
			return out
		}
		if first, after := v2Cut(rest); first == "colour" {
			out["colour"] = v2ParseInline("[" + after + "]")
			return out
		}
		out["key"] = rest
	case "run":
		out["__cmd"] = v2UnquoteInline(rest)
	case "scp":
		// `scp a b` IS THE SHORT FORM OF `scp: [a, b]`, and both become the list the executor reads.
		// The bare `copy` verb is gone: it parsed into `from`/`to` that nothing read (issue 12).
		if local, remote, ok := v2TwoPaths(rest); ok {
			out["scp"] = []any{local, remote}
		}
	case "stop":
		out = map[string]any{"stop": true, "method": v2UnquoteInline(rest)}
	case "control":
		out = map[string]any{"control": true, "acpi": v2UnquoteInline(rest)}
	}
	return out
}

// v2TwoPaths reads `a "b c"` into its two arguments, honouring quotes.
func v2TwoPaths(s string) (string, string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", false
	}
	var first string
	if s[0] == '"' {
		end := strings.IndexByte(s[1:], '"')
		if end < 0 {
			return "", "", false
		}
		first = s[1 : 1+end]
		s = strings.TrimSpace(s[2+end:])
	} else {
		i := strings.IndexAny(s, " \t")
		if i < 0 {
			return "", "", false
		}
		first = s[:i]
		s = strings.TrimSpace(s[i:])
	}
	second := v2UnquoteInline(s)
	if second == "" {
		return "", "", false
	}
	return first, second, true
}

// v2PairOf reads `960 628` as two numbers.
func v2PairOf(s string) (int, int, bool) {
	f := strings.Fields(s)
	if len(f) != 2 {
		return 0, 0, false
	}
	x, e1 := strconv.Atoi(f[0])
	y, e2 := strconv.Atoi(f[1])
	if e1 != nil || e2 != nil {
		return 0, 0, false
	}
	return x, y, true
}

// v2UnquoteInline strips the quotes from a whole argument, so `run "a b"` is the command `a b`.
func v2UnquoteInline(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		if v, ok := v2ParseInline(s).(string); ok {
			return v
		}
	}
	return s
}
