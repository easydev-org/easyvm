package walk

// YAML TO `.walk`, SO A CONVERSION CAN BE PROVED RATHER THAN TYPED.
//
// SIXTEEN FILES AND 6675 LINES is not something to hand-write: every coordinate is a measurement, and
// a transcription error in one of them is a walkthrough that clicks the wrong pane forty minutes in.
// This emitter reads the document the YAML decoded to and writes the sentence form, so the two are the
// same data by construction - and `YAMLToWalk` then `WalkToYAML` is asserted to give back the original
// document in a test, which is the property that makes the whole migration trustworthy.
//
// THE OUTPUT IS DELIBERATELY MIXED. Where a phrase says it better - `click "next" at 1777 1059`,
// `wait 8s`, `make a disk "disk.qcow2" of 128 GiB` - it writes the phrase. Where nothing does, it
// writes `step "name"` with `key == value` lines, which is exact and still readable. A person editing
// the file later can change either form; the loader takes both.

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// YAMLToWalk renders a walkthrough document as the sentence form.
//
// IT KEEPS THE COMMENTS OUT, because YAML decoding drops them: a `.walk` produced from a `.yaml` is
// the STEPS, not the prose around them. That is a real loss and it is why the migration writes the
// `.walk` beside the `.yaml` first - the review is the two files side by side - and only then deletes
// the original.
func YAMLToWalk(doc map[string]any) (string, error) {
	var b strings.Builder

	name, _ := doc["name"].(string)
	if name == "" {
		return "", fmt.Errorf("a walkthrough needs a `name:`")
	}
	fmt.Fprintf(&b, "walkthrough %s\n", quote(name))

	// ---- the header, in the order a person reads it ------------------------------------
	if r, ok := doc["resolution"].([]any); ok && len(r) == 2 {
		fmt.Fprintf(&b, "  resolution %vx%v\n", r[0], r[1])
	}
	// `ssh_user` IS WRITTEN WHENEVER IT IS PRESENT, even when it equals the default: the document
	// SAID it, and a conversion that drops a key the file wrote is not a conversion.
	if u, ok := doc["ssh_user"].(string); ok && u != "" {
		fmt.Fprintf(&b, "  user %s\n", u)
	}
	if vars, ok := doc["vars"].(map[string]any); ok {
		// `password` HAS ITS OWN HEADER WORD, because every file has one and `{{ password }}` is in
		// half the steps.
		if p, ok := vars["password"].(map[string]any); ok {
			if d, ok := p["default"].(string); ok && d != "" {
				fmt.Fprintf(&b, "  password %s\n", bareOrQuote(d))
			}
		}
	}
	// every other var is written exactly, because the vocabulary around them is not stable enough to
	// invent phrases for
	if vars, ok := doc["vars"].(map[string]any); ok && len(vars) > 0 {
		keys := make([]string, 0, len(vars))
		for k := range vars {
			if k == "password" {
				continue
			}
			keys = append(keys, k)
		}
		if len(keys) > 0 {
			sort.Strings(keys)
			for _, k := range keys {
				v, err := yamlValue(vars[k])
				if err != nil {
					return "", err
				}
				fmt.Fprintf(&b, "  var %s == %s\n", k, v)
			}
		}
	}
	// header keys with no phrase of their own
	// EVERY REMAINING TOP-LEVEL KEY IS WRITTEN EXACTLY. A converter that knew only the keys it had
	// phrases for would drop `boot_poll:` and silently change a file - and a document key nobody
	// expected is exactly what a round-trip test finds.
	known := map[string]bool{"name": true, "resolution": true, "ssh_user": true, "vars": true,
		"steps": true}
	var extra []string
	for k := range doc {
		if !known[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	for _, k := range extra {
		v, ok := doc[k]
		if !ok {
			continue
		}
		yv, err := yamlValue(v)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "  header %s == %s\n", k, yv)
	}
	b.WriteString("\n")

	// ---- the steps ----------------------------------------------------------------------
	steps, _ := doc["steps"].([]any)
	if len(steps) == 0 {
		return "", fmt.Errorf("a walkthrough needs `steps:`")
	}
	for i, raw := range steps {
		s, ok := raw.(map[string]any)
		if !ok {
			return "", fmt.Errorf("step %d is not a mapping", i+1)
		}
		if err := writeWalkStep(&b, s, "  "); err != nil {
			return "", fmt.Errorf("step %d: %w", i+1, err)
		}
	}
	return b.String(), nil
}

// writeWalkStep renders one step at the given indent.
func writeWalkStep(b *strings.Builder, s map[string]any, indent string) error {
	name, _ := s["name"].(string)
	if name == "" {
		return fmt.Errorf("a step has no name")
	}

	// THE SHORTCUTS FIRST, and each is only taken when the step is EXACTLY that shape - anything else
	// falls through to `step "name"`, which is always correct.
	if line, ok := shortcut(s, name); ok {
		// THE NAME IS PART OF THE STEP, so a phrase whose sentence is not the step's name carries the
		// real one: `click "next" at 1777 1059, named "next from welcome"`. That is what lets the
		// phrases fire on real files, where a step's name is a description rather than a label.
		if stepNameFrom(line) != name {
			// THE CLAUSE IS ALWAYS QUOTED, even for a one-word name: `named accessibility` would be
			// read as a bare token by anything that splits on spaces, and the parser's own `unquote`
			// requires the quotes anyway.
			line += `, named "` + strings.ReplaceAll(name, `"`, `\"`) + `"`
		}
		fmt.Fprintf(b, "%s%s\n", indent, line)
		return nil
	}

	fmt.Fprintf(b, "%sstep %s\n", indent, quote(name))
	keys := make([]string, 0, len(s))
	for k := range s {
		if k == "name" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		yv, err := yamlValue(s[k])
		if err != nil {
			return fmt.Errorf("`%s`: %w", k, err)
		}
		fmt.Fprintf(b, "%s  %s == %s\n", indent, k, yv)
	}
	return nil
}

// shortcut writes a step as a sentence when the step is exactly that sentence's shape.
//
// IT IS ORDERED BY HOW MUCH THE PHRASE BUYS: a click with a settle is the commonest step in every file
// here, and `click "next" at 1777 1059` is what a person would say. A longer step has more than one
// key and falls through to the exact form, where nothing can be lost.
func shortcut(s map[string]any, name string) (string, bool) {
	hasOnly := func(keys ...string) bool {
		if len(s) != len(keys)+1 { // + name
			return false
		}
		for _, k := range keys {
			if _, ok := s[k]; !ok {
				return false
			}
		}
		return true
	}
	// `click "next" at 1777 1059`
	if pt, ok := s["click"]; ok {
		base := []string{"click"}
		for _, extra := range [][]string{{"settle"}, {"settle", "optional"}} {
			if hasOnly(append(base, extra...)...) {
				x, y, ok := pairOfAny(pt)
				if !ok {
					return "", false
				}
				line := fmt.Sprintf("click %s at %d %d", quote(labelOf(name)), x, y)
				if v, ok := numOf(s["settle"]); ok {
					line += fmt.Sprintf(", waiting %gs", v)
				}
				if _, ok := s["optional"]; ok {
					line += ", optionally"
				}
				return line, true
			}
		}
	}
	// `press Return`
	if k, ok := s["key"]; ok {
		if hasOnly("key") || hasOnly("key", "settle") {
			line := fmt.Sprintf("press %s", keyName(fmt.Sprint(k)))
			if v, ok := numOf(s["settle"]); ok {
				line += fmt.Sprintf(", waiting %gs", v)
			}
			return line, true
		}
	}
	// `settle 8s` on its own. THE VERB LEADS, because that is what the parser reads - a phrase whose
	// first word is the step's name would be parsed as a verb nobody has.
	if v, ok := s["settle"]; ok && hasOnly("settle") {
		if n, ok := numOf(v); ok {
			return fmt.Sprintf("settle %gs", n), true
		}
	}
	// `needs a.iso b.iso`. THE FILES ARE BARE, because a medium's name is a fact about the disk and
	// not a value the log echoes - but a name with a path separator or a space would be ambiguous in
	// a whitespace-separated list, so those fall through to the exact form.
	if v, ok := s["needs"]; ok && hasOnly("needs") {
		if list, ok := v.([]any); ok && len(list) > 0 {
			parts := make([]string, 0, len(list))
			plain := true
			for _, item := range list {
				name := fmt.Sprint(item)
				if strings.ContainsAny(name, " \t") {
					plain = false
					break
				}
				parts = append(parts, name)
			}
			if plain {
				return "needs " + strings.Join(parts, " "), true
			}
		}
	}
	return "", false
}

// labelOf takes the control's name out of a step's own name, for a click's phrase.
//
// `click "next" at ...` READS AS A SENTENCE ONLY WHEN THE LABEL IS SHORT. A step named "next from
// welcome" is a description, not a control, and quoting the whole thing would be noise - so the label
// is the first word when the name is one or two words, and the name otherwise, which is what a person
// writes anyway.
func labelOf(name string) string {
	fields := strings.Fields(name)
	if len(fields) <= 2 {
		return fields[0]
	}
	return name
}

// keyName maps a qemu qcode back to the word a person writes.
func keyName(qcode string) string {
	switch qcode {
	case "ret":
		return "Return"
	case "esc":
		return "Escape"
	}
	return qcode
}

// yamlValue renders a value the way a `==` line takes it: YAML, on ONE LINE.
//
// THE MARSHALLER'S BLOCK STYLE IS NOT WHAT A `==` LINE CAN HOLD. A mapping marshals as
//
//	default: "128"
//	from: args.disk_gb
//
// which is two lines, and the second one landed in the `step` body as a key with no `==`. So a value
// is rendered with yaml.v3's FLOW STYLE - `{default: "128", from: args.disk_gb}` - which is YAML the
// decoder reads back to the identical mapping.
func yamlValue(v any) (string, error) {
	var b strings.Builder
	enc := yaml.NewEncoder(&b)
	// A NARROW WIDTH KEEPS IT ON ONE LINE: the marshaller wraps at 80 columns by default, which would
	// break a long list across lines and produce the same problem as block style.
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	_ = enc.Close()
	out := strings.TrimSpace(b.String())
	if !strings.Contains(out, "\n") {
		return out, nil
	}
	// FALL BACK TO FLOW STYLE FOR ANYTHING MULTI-LINE, which is what a nested mapping and a long list
	// both produce.
	return flowValue(v), nil
}

// flowValue renders any value as ONE line of YAML flow style.
//
// IT IS WRITTEN BY HAND RATHER THAN VIA A MARSHALLER OPTION because yaml.v3 has no "always flow"
// switch: `yaml.Marshal` chooses block style for mappings and long sequences, and the choice is not
// configurable. The shapes this has to carry are exactly the ones a walkthrough uses - mapping, list,
// string, number, bool - so the recursion is four cases and no parsing.
func flowValue(v any) string {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+": "+flowValue(t[k]))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			parts = append(parts, flowValue(item))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case string:
		return flowScalar(t)
	case bool:
		if t {
			return "true"
		}
		return "false"
	case nil:
		return "null"
	}
	if n, ok := numOf(v); ok {
		if n == float64(int64(n)) {
			return fmt.Sprintf("%d", int64(n))
		}
		return fmt.Sprintf("%v", n)
	}
	return flowScalar(fmt.Sprint(v))
}

// flowScalar quotes a string when YAML would otherwise read it as something else.
func flowScalar(s string) string {
	if s == "" {
		return `""`
	}
	// A VALUE THAT WOULD DECODE AS A NUMBER, A BOOL OR NULL MUST BE QUOTED, or `"128"` comes back as
	// the integer 128 and the round trip changes the document.
	needsQuote := strings.ContainsAny(s, ":{}[],#&*!|>'\"%@` \t\n") ||
		s == "true" || s == "false" || s == "null" || s == "~" ||
		(len(s) > 0 && (s[0] == '-' || s[0] == '?' || (s[0] >= '0' && s[0] <= '9')))
	if !needsQuote {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s) + `"`
}

func quote(s string) string {
	if strings.ContainsAny(s, " \t:#\"") || s == "" {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}

func bareOrQuote(s string) string { return quote(s) }

func pairOfAny(v any) (int, int, bool) {
	list, ok := v.([]any)
	if !ok || len(list) != 2 {
		return 0, 0, false
	}
	x, okx := numOf(list[0])
	y, oky := numOf(list[1])
	if !okx || !oky {
		return 0, 0, false
	}
	return int(x), int(y), true
}

func numOf(v any) (float64, bool) {
	switch t := v.(type) {
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case float64:
		return t, true
	}
	return 0, false
}

// stepNameFrom is the name a phrase produces: the line itself, minus any trailing clause.
//
// IT IS HOW THE EMITTER CHECKS ITS OWN SHORTCUT - a phrase is only used when the name it would produce
// is the name the step already had, because a conversion that renames a step changes the transcript.
// The clauses a phrase appends are `, waiting Ns` and `, optionally`, so those are what is trimmed.
func stepNameFrom(line string) string {
	line = strings.TrimSuffix(line, ", optionally")
	if i := strings.LastIndex(line, ", named "); i >= 0 {
		line = line[:i]
	}
	if i := strings.LastIndex(line, ", waiting "); i >= 0 {
		line = line[:i]
	}
	return line
}
