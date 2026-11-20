package walk

// THE `.walk` FORMAT: A WALKTHROUGH WRITTEN AS SENTENCES, CONVERTED TO THE SAME DOCUMENT.
//
// IT IS NOT A SECOND ENGINE, and that is the whole point of this file: it reads lines and produces
// the `map[string]any` that `yaml.Unmarshal` would have produced, so everything after that point -
// validation, the dispatcher, the measurements, the recordings - is untouched. A `.walk` file cannot
// express anything a `.yaml` file cannot, which is the property that stops the two drifting.
//
// THE SHAPE, and indentation is the only syntax:
//
//	walkthrough "kubuntu install"
//	  resolution 1920x1080
//	  needs kubuntu.iso
//	  click "next" at 1777 1059
//	  when the screen says "welcome":
//	    click "next" at 1777 1059
//
// THREE THINGS A LINE CAN BE, and each has one rule:
//
//	a header line    `resolution`, `user`, `password`, `recording` - at the top, before any step
//	a step           a verb and its arguments; its NAME is the line itself
//	a child          indented under a step, and becomes a key OF that step
//
// `docs/WALK-FORMAT.md` is the reference a person reads.

import (
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// WalkToYAML converts a `.walk` document into the mapping the YAML decoder would produce.
//
// IT IS EXPORTED SO THE CONVERSION CAN BE TESTED AS A VALUE rather than only through a machine. The
// test that matters is "every real YAML walkthrough converts to `.walk` and back byte-identically",
// and that needs this function, not a run.
func WalkToYAML(src string) (map[string]any, error) {
	lines := scanWalkLines(src)
	if len(lines) == 0 {
		return nil, fmt.Errorf("the file has no lines; a walkthrough starts with `walkthrough \"name\"`")
	}
	if lines[0].indent != 0 {
		return nil, fmt.Errorf("line %d: the title must not be indented", lines[0].no)
	}
	name, err := parseTitle(lines[0])
	if err != nil {
		return nil, fmt.Errorf("line %d: %w", lines[0].no, err)
	}

	doc := map[string]any{"name": name}
	vars := map[string]any{}
	var steps []any
	var recording string

	i := 1
	// ---- the header -------------------------------------------------------------------------
	//
	// A HEADER LINE MAY APPEAR ANYWHERE BEFORE THE FIRST STEP, and that is a correction: the first
	// version stopped at the first line that was not a header word, so a file that wrote
	// `needs mint.iso` and then `recording "x.mp4"` emitted a step whose KEY was `recording:` -
	// a key the vocabulary does not have, in a document that then failed to load. MEASURED on a
	// real conversion.
	//
	// SO THE LOOP CONTINUES UNTIL A LINE THAT IS DEFINITELY A STEP, and a header word written after
	// a step has begun is caught by the vocabulary rather than by this loop.
	//
	// A HEADER WORD IS RECOGNISED WHEREVER IT APPEARS, not only in a leading run: a file that writes
	// `needs mint.iso` and then `recording "x.mp4"` has not started a step called "recording", it has
	// declared a recording. So the scan TAKES the header lines it finds and leaves the rest for the
	// step parser, which is why this is a filtered pass rather than a prefix.
	var stepLines []line
	for ; i < len(lines); i++ {
		key, value, isHeader, err := parseHeader(lines[i])
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lines[i].no, err)
		}
		if !isHeader {
			stepLines = append(stepLines, lines[i])
			continue
		}
		switch key {
		case "recording":
			recording = value.(string)
		case "var":
			vp := value.(varPair)
			vars[vp.name] = vp.spec["__exact"]
		case "header":
			vp := value.(varPair)
			doc[vp.name] = vp.spec["__exact"]
		case "vars":
			vars[value.(varPair).name] = value.(varPair).spec
		default:
			doc[key] = value
		}
	}
	if len(vars) > 0 {
		doc["vars"] = vars
	}
	// A RECORDING IS A STEP. It is written as a header because it starts before anything else and
	// reads as a property of the run, but the YAML has it as the first step and the executor starts
	// it there.
	// `recording` IS STILL ACCEPTED AND NO LONGER DOES ANYTHING: a recording starts with the run
	// now, so a header that named the file was a step that said "begin" to a recorder already going.
	// The name is derived from the walkthrough, which is more than the header ever managed -
	// `install.mp4` and `ubuntu-install.mp4` both existed and nothing read either.
	_ = recording

	// ---- the steps ------------------------------------------------------------------------
	//
	// THE RESOLUTION IS HANDED TO EACH STEP, because a reading of "the whole frame" has to be a real
	// rectangle and the size it is measured at lives in the header. Carrying it on the step is the
	// smallest way to get it there without a global.
	res := []any{1920, 1080}
	if r, ok := doc["resolution"].([]any); ok {
		res = r
	}
	for i := 0; i < len(stepLines); {
		step, next, err := parseStepWith(stepLines, i, res)
		if err != nil {
			return nil, err
		}
		steps = append(steps, step)
		i = next
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("the file has no steps; a header with nothing after it proves nothing")
	}
	doc["steps"] = steps
	return doc, nil
}

// line is one non-blank, non-comment source line.
type line struct {
	no     int
	indent int
	text   string
}

// scanWalkLines drops blanks and comments and records each line's indentation and number.
//
// THE NUMBER IS KEPT because a `.walk` failure that says "bad indent" without a line is a file a
// person has to bisect.
func scanWalkLines(src string) []line {
	var out []line
	for n, raw := range strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n") {
		text := strings.TrimRight(raw, " \t")
		trimmed := strings.TrimSpace(text)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		out = append(out, line{no: n + 1, indent: len(text) - len(strings.TrimLeft(text, " \t")), text: trimmed})
	}
	return out
}

// varPair is a variable's name and its YAML spec, carried out of parseHeader as one value.
type varPair struct {
	name string
	spec map[string]any
}

// parseHeader recognises a header line, and says so with `isHeader` rather than by guessing from
// indentation: `resolution` at the top is a header word, and so is a step called "resolution" only
// if it is written with a verb.
func parseHeader(l line) (key string, value any, isHeader bool, err error) {
	verb, rest := cutVerb(l.text)
	switch verb {
	case "resolution":
		w, h, err := parseSize(rest)
		if err != nil {
			return "", nil, false, fmt.Errorf("resolution: %w", err)
		}
		return "resolution", []any{w, h}, true, nil
	case "recording_resolution":
		// `recording_resolution 1280x800` - THE VIDEO'S SIZE, which is a fact about the ARTIFACT
		// rather than about the coordinates. It is a header because it describes the whole run, and
		// it is required because a default would silently letterbox a 720x400 guest into 1920x1080.
		w, h, err := parseSize(rest)
		if err != nil {
			return "", nil, false, fmt.Errorf("recording_resolution: %w", err)
		}
		return "recording_resolution", []any{w, h}, true, nil
	case "user":
		// BARE OR QUOTED: a single word needs no quotes, and `user vmuser` reads better than
		// `user "vmuser"` for a value with no spaces.
		v, err := bareOrQuoted(rest)
		if err != nil {
			return "", nil, false, fmt.Errorf("user: %w", err)
		}
		return "ssh_user", v, true, nil
	case "password":
		// BARE OR QUOTED: a single word needs no quotes, and `user vmuser` reads better than
		// `user "vmuser"` for a value with no spaces.
		v, err := bareOrQuoted(rest)
		if err != nil {
			return "", nil, false, fmt.Errorf("password: %w", err)
		}
		return "vars", varPair{"password", map[string]any{
			"from": "args.password", "default": v,
		}}, true, nil
	case "var", "header":
		// `var name == <yaml>` AND `header name == <yaml>` - a key written exactly, because the
		// vocabulary around vars has no phrase for most of them and the conversion must be exact.
		key, value, ok := strings.Cut(rest, "==")
		if !ok {
			return "", nil, false, fmt.Errorf("`%s` needs `name == <yaml>`", verb)
		}
		var v any
		if err := yaml.Unmarshal([]byte(strings.TrimSpace(value)), &v); err != nil {
			return "", nil, false, fmt.Errorf("`%s %s`: %v", verb, strings.TrimSpace(key), err)
		}
		return verb, varPair{strings.TrimSpace(key), map[string]any{"__exact": v}}, true, nil
	case "recording":
		// BARE OR QUOTED: a single word needs no quotes, and `user vmuser` reads better than
		// `user "vmuser"` for a value with no spaces.
		v, err := bareOrQuoted(rest)
		if err != nil {
			return "", nil, false, fmt.Errorf("recording: %w", err)
		}
		return "recording", v, true, nil
	}
	return "", nil, false, nil
}

// parseStep reads one step and everything indented under it. It returns the step and the index of the
// first line that is not part of it.
func parseStepWith(lines []line, i int, res []any) (map[string]any, int, error) {
	step, next, err := parseStep(lines, i)
	if err != nil {
		return nil, 0, err
	}
	// `see:` CARRIES NO RECTANGLE ANY MORE, so the header's resolution is not pushed into it. A
	// region belongs to the `check:` that asks the question, and that `check:` says its own `rect:`
	// and `rect_at:` - which is what lets one capture answer questions about different parts of it.
	//
	// `res` IS STILL PASSED because the measurement lines below need it, and dropping it here would
	// move the knowledge of the frame's size into a second place.
	_ = res
	return step, next, err
}

func parseStep(lines []line, i int) (map[string]any, int, error) {
	head := lines[i]
	verb, rest := cutVerb(head.text)

	// CHILDREN ARE EVERY FOLLOWING LINE INDENTED MORE THAN THIS ONE.
	var children []line
	next := i + 1
	for next < len(lines) && lines[next].indent > head.indent {
		children = append(children, lines[next])
		next++
	}

	var step map[string]any
	var err error
	switch {
	case verb == "when" || verb == "unless":
		step, err = parseGate(head, rest, children)
	case verb == "repeat":
		step, err = parseRepeat(head, rest, children)
	case verb == "decide":
		step, err = parseDecideBlock(head, rest, children)
	case verb == "read":
		step, err = parseReadBlock(head, rest, children)
	case verb == "step":
		step, err = parseExact(head, rest, children)
	case len(children) > 0:
		// A STEP THAT OWNS CHILDREN IS A GATE OR A LOOP; anything else with an indented block is a
		// file whose author expected something this format does not do, and saying so is better than
		// silently running the head and dropping the block.
		return nil, 0, fmt.Errorf("line %d: %q opens a block, and only `when`, `unless`, "+
			"`read`, `decide`, `step` and `repeat` take one", head.no, head.text)
	default:
		step, err = parseStatement(head)
	}
	if err != nil {
		return nil, 0, err
	}
	// THE `named` CLAUSE IS `parseStatement`'S JOB NOW, so this only fills a default: a block head
	// (`when ...:`) has no phrase of its own.
	if _, ok := step["name"]; !ok {
		// A BLOCK HEAD'S NAME MUST NOT END WITH `:` - the colon is the format's syntax, and a name
		// carrying it makes the YAML decoder read `name: decide "x" is all of:` as a nested mapping:
		//
		//	yaml: line 29: mapping values are not allowed in this context
		//
		// MEASURED on the first microvm walkthrough that used a `decide` block.
		step["name"] = strings.TrimSuffix(head.text, ":")
	}
	return step, next, nil
}

// parseGate builds a step whose `when:`/`when_not:` gate is the head and whose action is its block.
//
//	tap the power button, unless the screen says "installed"
//	  click "next" at 1777 1059
func parseGate(head line, rest string, children []line) (map[string]any, error) {
	if len(children) != 1 {
		return nil, fmt.Errorf("line %d: a gate needs exactly one action under it, got %d",
			head.no, len(children))
	}
	action, err := parseStatement(children[0])
	if err != nil {
		return nil, err
	}
	words, err := quotedWords(rest, "the screen says")
	if err != nil {
		return nil, fmt.Errorf("line %d: %w", head.no, err)
	}
	key := "when"
	if strings.Fields(head.text)[0] == "unless" {
		key = "when_not"
	}
	action[key] = words
	action["name"] = action["__default_name"]
	delete(action, "__default_name")
	if action["name"] == "" || action["name"] == nil {
		action["name"] = head.text
	}
	return action, nil
}

// parseRepeat builds a `loop:` from `repeat up to N times:` and its block.
func parseRepeat(head line, rest string, children []line) (map[string]any, error) {
	max, err := parseRepeatMax(rest)
	if err != nil {
		return nil, fmt.Errorf("line %d: %w", head.no, err)
	}
	loop := map[string]any{"max": max}
	var action []any
	for _, c := range children {
		sub, err := parseStatement(c)
		if err != nil {
			return nil, err
		}
		// THE FOUR PARTS OF A LOOP ARE RECOGNISED BY THE KEY THEY PRODUCE, because `loop:` names
		// them: `do:`, `ocr:`, `check:`, `until:`.
		switch {
		case sub["__until"] != nil:
			loop["until"] = map[string]any{"fact": sub["__until"]}
		case sub["ocr"] != nil:
			loop["ocr"] = sub["ocr"]
		case sub["check"] != nil:
			loop["check"] = sub["check"]
		default:
			delete(sub, "name")
			action = append(action, sub)
		}
	}
	if len(action) == 0 {
		return nil, fmt.Errorf("line %d: `repeat` has no action, so it spins on the clock",
			head.no)
	}
	loop["do"] = action
	return map[string]any{"name": head.text, "loop": loop}, nil
}

// parseStatement turns one sentence into a step mapping.
//
// THE VERB IS THE FIRST WORD AND IT IS THE YAML KEY, so a statement that needs no argument parsing
// is passed through unchanged - which is what keeps a key added to the vocabulary working here with
// no change to this file. The verbs below are the ones whose ARGUMENTS have a shape worth reading:
// a coordinate pair, a colour, a number of seconds. Everything else is `verb: rest`.
func parseStatement(l line) (map[string]any, error) {
	verb, rest := cutVerb(l.text)
	step := map[string]any{"name": l.text}

	// `, named "X"` IS THE STEP'S REAL NAME, and it is read HERE rather than in `parseStep` so that a
	// step inside a `do:` list - which never passes through `parseStep` - gets it too. MEASURED: a
	// click in a list kept the phrase's own name and the round trip renamed the step.
	// THE `, named "X"` CLAUSE IS CUT FROM THE TEXT BEFORE ANYTHING PARSES IT, and applied to the step
	// afterwards. A phrase builds a name of its own - `click "next" at ...` - so the real name has to
	// win, and the clause must not reach `parsePoint`, whose fields are numbers.
	named, hasNamed := "", false
	if i := strings.LastIndex(l.text, ", named "); i >= 0 {
		if n, err := unquote(l.text[i+len(", named "):]); err == nil {
			// THE NAME MAY CONTAIN ESCAPED QUOTES - a step's name is a sentence and some quote:
			// `give the installer its last few seconds, because "ready to use" is not "flushed"`.
			named, hasNamed = yamlUnescape(n), true
			l.text = l.text[:i]
		}
	}
	verb, rest = cutVerb(l.text)
	step = map[string]any{"name": l.text}

	out, err := parseVerbed(l, verb, rest, step)
	if err != nil {
		return nil, err
	}
	if hasNamed && out != nil {
		out["name"] = named
	}
	return out, nil
}

// parseVerbed is the switch, split out so the `named` clause is applied in ONE place.
func parseVerbed(l line, verb, rest string, step map[string]any) (map[string]any, error) {
	switch verb {
	// ---- loop bookkeeping, which is not a step -------------------------------------------
	case "until":
		fact, err := unquote(rest)
		if err != nil {
			return nil, fmt.Errorf("line %d: `until` needs a quoted fact name: %w", l.no, err)
		}
		return map[string]any{"__until": fact, "name": l.text}, nil

	// ---- the machine ---------------------------------------------------------------------
	case "make":
		return parseMakeDisk(l, rest, step)
	case "boot":
		return parseBoot(l, rest, step)
	case "stop":
		return parseStop(l, rest, step)
	case "acpi":
		// `acpi power` / `acpi reset`
		step["acpi"] = strings.TrimSpace(rest)
		return step, nil
	case "qga":
		// `qga shutdown`, `qga ping`, `qga info`, ... - the protocol's own command names.
		step["qga"] = strings.TrimSpace(rest)
		return step, nil
	case "wait until":
		// `wait until stopped`
		step["wait_until"] = strings.TrimSpace(rest)
		return step, nil

	// ---- input ---------------------------------------------------------------------------
	case "click", "double":
		// THE COORDINATES ARE THE FIRST TWO NUMBERS AFTER `at`, and everything after a comma is a
		// clause: `click "next" at 1777 1059, waiting 8s, named "next from welcome"`.
		body := rest
		if i := strings.Index(body, ", waiting "); i >= 0 {
			body = body[:i]
		}
		if i := strings.Index(body, ", named "); i >= 0 {
			body = body[:i]
		}
		x, y, err := parsePoint(body)
		if err != nil {
			return nil, fmt.Errorf("line %d: %s: %w", l.no, verb, err)
		}
		step[verb] = []any{x, y}
		// AND THE CLAUSES ARE PART OF THE STEP. A phrase that dropped `waiting` or `optionally`
		// would convert the file into a different one.
		if i := strings.Index(rest, ", waiting "); i >= 0 {
			tail := rest[i+len(", waiting "):]
			if j := strings.Index(tail, ", "); j >= 0 {
				tail = tail[:j]
			}
			if secs, err := parseSeconds(tail); err == nil {
				step["settle"] = secs
			}
		}
		if strings.Contains(rest, ", optionally") {
			step["optional"] = true
		}
		return step, nil
	case "press":
		// `press Return` / `press ctrl+alt+t`
		if rest == "" {
			return nil, fmt.Errorf("line %d: `press` needs a key", l.no)
		}
		// THE CLAUSES ARE CUT FIRST: `press Return, waiting 10s, named "welcome - Install is
		// preselected"` - the key is `Return` and everything after the comma is not.
		value := rest
		if i := strings.LastIndex(value, ", named "); i >= 0 {
			value = value[:i]
		}
		if i := strings.LastIndex(value, ", waiting "); i >= 0 {
			value = value[:i]
		}
		step["key"] = keyQcode(value)
		// `, waiting Ns` IS THE STEP'S SETTLE, not decoration - a phrase that dropped it would change
		// the document.
		if i := strings.LastIndex(rest, ", waiting "); i >= 0 {
			tail := rest[i+len(", waiting "):]
			if j := strings.LastIndex(tail, ", named "); j >= 0 {
				tail = tail[:j]
			}
			if secs, err := parseSeconds(tail); err == nil {
				step["settle"] = secs
			}
		}
		return step, nil
	case "type":
		return parseType(l, rest, step)
	case "run":
		step["run"] = rest
		return step, nil
	case "ssh":
		step["ssh"] = rest
		return step, nil

	// ---- a wait --------------------------------------------------------------------------
	case "settle":
		// `, named "X"` AND `, waiting Ns` ARE CLAUSES ON THE PHRASE, so they are cut before the number
		// is read: `settle 20s, named "let the menu draw"`.
		value := rest
		if i := strings.LastIndex(value, ", named "); i >= 0 {
			value = value[:i]
		}
		if i := strings.LastIndex(value, ", waiting "); i >= 0 {
			value = value[:i]
		}
		secs, err := parseSeconds(value)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", l.no, err)
		}
		step["settle"] = secs
		return step, nil

	// ---- a reading, and the capture it is named for --------------------------------------
	//
	// `read` AND `see` ARE THE SAME LINE, and only one of them was a verb here.
	//
	// `see: {name: shot}` IS THE STEP THAT CAPTURES, and the sentence form for it is
	// `see the screen as "shot"` - which is what `linux/alpine-microvm/steps/measure.walk` and
	// `reading-example.walk` write. With no `case "see"` the line fell through to the fallback and
	// became `see: "the whole screen as \"page"` - ONE STRING, quotes and all, where a mapping was
	// expected. The walk still LOADED, the frame was named `the whole screen as "page`, and every
	// `check: {from: page}` in the file referred to a name nothing had bound.
	//
	// MEASURED AS A SILENT FAILURE, which is the worst kind: `easyvm check` printed "7 step(s)" and
	// nothing else, and the three questions after it would each have answered "names a reading no
	// ocr: or see: step has made" on a real run.
	case "read", "see":
		return parseRead(l, rest, step)

	// ---- the serial console, which is all a microvm has --------------------------------
	case "wait":
		// `wait for a desktop, waiting up to 5400s`
		if strings.Contains(rest, "desktop") {
			step["desktop"] = "linux"
			for _, fam := range []string{"linux/", "windows", "macos", "android"} {
				if i := strings.Index(rest, fam); i >= 0 {
					word := strings.FieldsFunc(rest[i:], func(r rune) bool {
						return r == ',' || r == ' ' || r == '.'
					})[0]
					step["desktop"] = word
					break
				}
			}
			if secs, err := parseTimeoutClause(rest); err == nil && secs > 0 {
				step["timeout"] = secs
			}
			return step, nil
		}
		// `wait for the console to say "X", waiting up to 180s`
		if strings.Contains(rest, "console") {
			words, err := quotedWords(rest, "say")
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", l.no, err)
			}
			step["until"] = words[0]
			step["from"] = "console"
			if secs, err := parseTimeoutClause(rest); err == nil && secs > 0 {
				step["timeout"] = secs
			}
			return step, nil
		}
		secs, err := parseSeconds(rest)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", l.no, err)
		}
		step["settle"] = secs
		return step, nil
	case "the":
		// `the console will say "localhost login"` - an assertion about the serial line.
		if strings.HasPrefix(rest, "console will say") {
			words, err := quotedWords(rest, "say")
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", l.no, err)
			}
			step["until"] = words[0]
			step["from"] = "console"
			step["timeout"] = 180
			return step, nil
		}

	// ---- a declaration -------------------------------------------------------------------
	case "needs":
		// `, named "X"` IS A CLAUSE ON THE PHRASE, not a file name: `needs ubuntu.iso, named "the
		// media this stage boots from is already on disk"`.
		value := rest
		if i := strings.LastIndex(value, ", named "); i >= 0 {
			value = value[:i]
		}
		var files []any
		for _, f := range strings.Fields(value) {
			files = append(files, strings.Trim(f, `"`))
		}
		if len(files) == 0 {
			return nil, fmt.Errorf("line %d: `needs` names no files, so it would pass by checking "+
				"nothing", l.no)
		}
		step["needs"] = files
		return step, nil
	}
	// A VERB THIS FILE DOES NOT PARSE BUT THE VOCABULARY HAS is passed through with its argument as
	// a string, so `record "x.mp4"` and `screenshot "s.png"` need no case here. An unknown verb is
	// caught by the vocabulary's own validator, which names every key it accepts.
	if rest == "" {
		step[verb] = true
		return step, nil
	}
	step[verb] = strings.Trim(rest, `"`)
	return step, nil
}

// parseMakeDisk reads `make a disk "alpine.qcow2" of 1 GiB`.
func parseMakeDisk(l line, rest string, step map[string]any) (map[string]any, error) {
	// `a disk` is prose that carries no meaning; the path and the size do.
	r := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rest), "a disk"))
	name, after, err := firstQuote(r)
	if err != nil {
		return nil, fmt.Errorf("line %d: `make a disk` needs the file name in quotes: %w", l.no, err)
	}
	step["disk"] = name
	gb, err := parseGiB(after)
	if err != nil {
		return nil, fmt.Errorf("line %d: %w", l.no, err)
	}
	step["disk_gb"] = gb
	return step, nil
}

// parseBoot reads `boot the alpine medium "alpine.iso" as a microvm, waiting up to 300s`.
func parseBoot(l line, rest string, step map[string]any) (map[string]any, error) {
	step["boot"] = true
	// A MICROVM TAKES A KERNEL AND AN INITRAMFS INSTEAD OF A MEDIUM, because there is no firmware to
	// read a boot sector. Its two inputs are read first, and the medium is only looked for when they
	// are absent - otherwise the kernel's own name would be taken as the ISO.
	microvm := strings.Contains(rest, "microvm")
	if microvm {
		step["microvm"] = true
		if i := strings.Index(rest, "kernel "); i >= 0 {
			if name, _, err := firstQuote(rest[i:]); err == nil {
				step["kernel"] = name
			}
		}
		if i := strings.Index(rest, "initramfs "); i >= 0 {
			if name, _, err := firstQuote(rest[i:]); err == nil {
				step["initrd"] = name
			}
		}
		// `medium "x.iso"` ATTACHES THE DISTRIBUTION IMAGE AS A BLOCK DEVICE, which is what an Alpine
		// microvm reads its `modloop=` squashfs out of. A kernel and an initramfs alone boot the
		// kernel and then stop: measured, the guest reached
		// `initramfs emergency recovery shell launched` because there was nothing to mount.
		// `with "console=tty0 root=/dev/vda rw"` IS THE GUEST'S OWN BOOT LINE.
		//
		// The engine's default is Alpine's diskless one, and it mounts `modloop=/dev/vda` OVER the
		// root disk - MEASURED: the guest mounted its ext4 root, then lost it, then dropped to
		// `initramfs emergency recovery shell launched`. So a file with a real root filesystem has
		// to be able to say so.
		if i := strings.Index(rest, " with \""); i >= 0 {
			if line, _, err := firstQuote(rest[i+len(" with "):]); err == nil {
				step["kernel_append"] = line
			}
		}
		// `with a desktop` / `with a console` / `without a display` NAMES THE ADAPTER.
		//
		// THE THREE SHAPES ARE NOT COSMETIC. `terminal` is a text-mode adapter, and an X server on it
		// answers `(EE) no screens found` - MEASURED. `desktop` asks for `virtio-gpu-device`, which is
		// on the virtio bus (so a machine with no PCIe can attach it) and provides DRM/KMS, so X
		// draws AND qemu's screendump records what it drew.
		switch {
		case strings.Contains(rest, "with a desktop"):
			step["microvm_display"] = "desktop"
		case strings.Contains(rest, "without a display"):
			step["microvm_display"] = "serial"
		case strings.Contains(rest, "with a console"):
			step["microvm_display"] = "terminal"
		}
		if i := strings.Index(rest, "medium "); i >= 0 {
			if name, _, err := firstQuote(rest[i:]); err == nil {
				step["iso"] = name
			}
		}
	}
	// THE MEDIUM IS THE FIRST QUOTED THING, and `as a microvm` and the timeout are the two clauses
	// that follow it in any order.
	if _, exists := step["kernel"]; !exists {
		if name, after, err := firstQuote(rest); err == nil {
			step["iso"] = name
			rest = after
		}
	}
	if secs, err := parseTimeoutClause(rest); err == nil && secs > 0 {
		step["timeout"] = secs
	}

	if i := strings.Index(rest, "as a "); i >= 0 {
		osName := strings.Fields(rest[i+5:])[0]
		osName = strings.Trim(osName, ",.")
		if osName == "microvm" {
			osName = "linux"
		}
		step["os"] = osName
	}
	if _, ok := step["os"]; !ok {
		step["os"] = "linux"
	}
	return step, nil
}

// parseStop reads `stop the machine by killing it, waiting up to 60s`.
func parseStop(l line, rest string, step map[string]any) (map[string]any, error) {
	method := "kill"
	for _, m := range []string{"kill", "ssh", "acpi", "guest_agent"} {
		if strings.Contains(rest, m) {
			method = m
			break
		}
	}
	step["shutdown"] = method
	if secs, err := parseTimeoutClause(rest); err == nil && secs > 0 {
		step["timeout"] = secs
	}
	return step, nil
}

// parseType reads `type "admin" into the password field` and `type "admin"`.
func parseType(l line, rest string, step map[string]any) (map[string]any, error) {
	value, after, err := firstQuote(rest)
	if err != nil {
		// A VAR REFERENCE: `type the password` means `{{ password }}`.
		word := strings.TrimSpace(rest)
		word = strings.TrimPrefix(word, "the ")
		if word == "" {
			return nil, fmt.Errorf("line %d: `type` needs text or a variable name", l.no)
		}
		step["type"] = "{{ " + strings.Fields(word)[0] + " }}"
		return step, nil
	}
	step["type"] = value
	// `clear` appears when the file says the field may already hold something.
	if strings.Contains(after, "clearing") || strings.Contains(after, "clear") {
		step["clear"] = true
	}
	return step, nil
}

// parseRead reads `read the screen as "parts"` and `read the console as "console"`.
//
// THE AREA IS THE WHOLE FRAME when the phrase names none, and "the whole frame" is
// `[0, 0, width, height]` AT THE FILE'S OWN RESOLUTION - not a placeholder. A rect of
// `[full, 0, 0, 0]` is the shape the first version produced, and the validator refused it as "too
// small to read", which is the correct verdict: a reading of nothing is not a reading.
func parseRead(l line, rest string, step map[string]any) (map[string]any, error) {
	name, _, err := firstQuote(rest)
	if err != nil {
		return nil, fmt.Errorf("line %d: `read ... as \"name\"` needs a quoted name: %w", l.no, err)
	}
	// THE AREA IS THE WHOLE FRAME when the phrase names none, and "the whole frame" is a REAL RECT at
	// the file's own resolution - not a placeholder. The first version emitted `[full, 0, 0, 0]` and
	// the validator refused it as "too small to read", which is right: a reading of nothing is not a
	// reading. The resolution is on the walk being built, so it is passed in.
	// `read the bottom 4.5% of the screen as "panel"` names an AREA, and that is a fraction the
	// vocabulary already understands. When it names none the rect is left absent, and `parseStepWith`
	// fills it with the header's own resolution - which is why this function does not have to know
	// how big the frame is.
	// `read ... as "name"` CAPTURES A FRAME AND NAMES IT. The area it names is the frame's, and a
	// question about a smaller region belongs to the `check:` that asks it - so a `read` with an area
	// still captures the whole frame.
	//
	// `measuring` IS STILL READ and is no longer emitted: the numbers it listed are measurements, and
	// a measurement is a `check: {mode: mean_between}` now. `parseMeasured` handles that line.
	step["see"] = map[string]any{"name": name}
	return step, nil
}

// `parseDecide` WAS HERE, AND `decide` WENT WITH `want:`.
//
// A `decide "name" is:` block parsed into a `want:` step, and `want:` is deleted - zero walkthroughs
// used it while `check:` was used 29 times. The only two files that ever wrote `decide` were the
// microvm measurement examples, which are about the vocabulary proving itself rather than about a
// guest, and they build their steps directly.

// ---- small parsers -----------------------------------------------------------------------

// cutVerb splits "verb rest" into the verb and everything after it.
func cutVerb(text string) (verb, rest string) {
	if i := strings.IndexAny(text, " \t"); i >= 0 {
		return text[:i], strings.TrimSpace(text[i+1:])
	}
	return text, ""
}

// parseTitle reads `walkthrough "name"`.
func parseTitle(l line) (string, error) {
	verb, rest := cutVerb(l.text)
	if verb != "walkthrough" {
		return "", fmt.Errorf("a walkthrough starts with `walkthrough \"name\"`; got %q", l.text)
	}
	name, err := unquote(rest)
	if err != nil {
		return "", fmt.Errorf("the walkthrough's name must be quoted: %w", err)
	}
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("the walkthrough has an empty name, so every log line about it is " +
			"anonymous")
	}
	return name, nil
}

// parseSize reads `1920x1080`.
func parseSize(s string) (int, int, error) {
	parts := strings.Split(strings.TrimSpace(s), "x")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("%q is not a size; write it as 1920x1080", s)
	}
	w, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("%q is not a size: %v", s, err)
	}
	h, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, fmt.Errorf("%q is not a size: %v", s, err)
	}
	return w, h, nil
}

// parseSeconds reads `8s`, `8`, `1.5s`.
//
// THE UNIT IS ALLOWED AND NOT REQUIRED, which is the opposite of the format's rule for other units
// and is deliberate: `settle:` in the YAML is seconds, so `wait 8s` and `wait 8` mean the same thing
// and both read correctly out loud.
func parseSeconds(s string) (float64, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "s")
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number of seconds", s)
	}
	return v, nil
}

// parseRepeatMax reads `up to 30 times`.
func parseRepeatMax(s string) (float64, error) {
	fields := strings.Fields(s)
	for i, f := range fields {
		if f == "times" || f == "time" {
			if i == 0 {
				break
			}
			v, err := strconv.ParseFloat(fields[i-1], 64)
			if err != nil {
				return 0, fmt.Errorf("%q is not a number of attempts", fields[i-1])
			}
			return v, nil
		}
	}
	return 0, fmt.Errorf("%q does not say how many times; write `repeat up to 30 times:`", s)
}

// quotedWords reads the quoted strings out of a phrase, ignoring the words before `stopAt`.
func quotedWords(s, stopAt string) ([]any, error) {
	if stopAt != "" {
		if i := strings.Index(s, stopAt); i >= 0 {
			s = s[i+len(stopAt):]
		}
	}
	var out []any
	i := 0
	for i < len(s) {
		if s[i] == '"' {
			j := strings.IndexByte(s[i+1:], '"')
			if j < 0 {
				return nil, fmt.Errorf("an opening quote has no closing quote")
			}
			out = append(out, s[i+1:i+1+j])
			i += j + 2
			continue
		}
		i++
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no quoted words: a gate needs the words the screen will say, in " +
			"quotes")
	}
	return out, nil
}

// unquote strips the quotes from a value that must be quoted.
func unquote(s string) (string, error) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return "", fmt.Errorf("%q is not quoted; a value the log echoes is written in quotes", s)
	}
	return s[1 : len(s)-1], nil
}

// ---- the small parsers the statements use ------------------------------------------------

// parsePoint reads `1777 1059` into two integers.
func parsePoint(rest string) (int, int, error) {
	// `click "next" at 1777 1059` - the coordinates are after `at`, and the words before it are the
	// control's name, which the step name already carries.
	if i := strings.Index(rest, " at "); i >= 0 {
		rest = rest[i+4:]
	}
	fields := strings.Fields(strings.ReplaceAll(rest, ",", " "))
	if len(fields) < 2 {
		return 0, 0, fmt.Errorf("%q is not a coordinate; write `at 1777 1059`", rest)
	}
	x, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, 0, fmt.Errorf("%q is not an x coordinate", fields[0])
	}
	y, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, fmt.Errorf("%q is not a y coordinate", fields[1])
	}
	return x, y, nil
}

// keyQcode maps the words a person writes to the qemu qcodes the vocabulary takes.
//
// IT IS A TABLE RATHER THAN A PASSTHROUGH because `press Return` and `press ret` are the same
// instruction and only one of them reads like English.
func keyQcode(word string) string {
	switch strings.ToLower(strings.TrimSpace(word)) {
	case "return", "enter":
		return "ret"
	case "escape", "esc":
		return "esc"
	case "space":
		return "space"
	case "tab":
		return "tab"
	case "backspace":
		return "backspace"
	case "delete":
		return "delete"
	case "up", "down", "left", "right":
		return strings.ToLower(word)
	}
	return strings.TrimSpace(word)
}

// firstQuote returns the first quoted string and the text after its closing quote.
func firstQuote(s string) (value, after string, err error) {
	i := strings.IndexByte(s, '"')
	if i < 0 {
		return "", s, fmt.Errorf("no quoted value in %q", s)
	}
	rest := s[i+1:]
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		return "", s, fmt.Errorf("an opening quote has no closing quote in %q", s)
	}
	return rest[:j], rest[j+1:], nil
}

// parseGiB reads `of 1 GiB` and `of 128 GiB`.
//
// THE UNIT IS HONOURED, because a disk written `1 GiB` and one written `1 MiB` are different files
// and a silent coercion to GiB would make a microvm's 1 GiB a 1 GiB it did not ask for. `disk_gb` is
// the vocabulary's key, so MiB is converted and a fractional GiB is kept.
func parseGiB(s string) (float64, error) {
	fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(s), "of "))
	if len(fields) < 1 {
		return 0, fmt.Errorf("no size: write `of 1 GiB`")
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a size", fields[0])
	}
	unit := "gib"
	if len(fields) > 1 {
		unit = strings.ToLower(fields[1])
	}
	switch unit {
	case "gib", "gb", "g":
		return v, nil
	case "mib", "mb", "m":
		return v / 1024, nil
	}
	return 0, fmt.Errorf("%q is not a unit I know; write GiB or MiB", unit)
}

// parseTimeoutClause reads `waiting up to 300s`.
func parseTimeoutClause(s string) (float64, error) {
	i := strings.Index(s, "up to ")
	if i < 0 {
		return 0, fmt.Errorf("no timeout")
	}
	rest := s[i+6:]
	f := strings.Fields(rest)
	if len(f) == 0 {
		return 0, fmt.Errorf("no number after `up to`")
	}
	return parseSeconds(strings.TrimSuffix(f[0], ","))
}

// walkVerbFor reports whether the format has a verb that produces this key, and which.
//
// IT IS THE COVERAGE TEST'S QUESTION: "does a person writing `.walk` have a way to say this?" A key
// with no verb is a key the format does not cover, and the test names it rather than letting the
// format quietly support less than the vocabulary.
func walkVerbFor(key string) (string, bool) {
	verbs := map[string]string{
		"needs": "needs", "disk": "make a disk",
		"disk_gb": "make a disk", "boot": "boot", "os": "boot ... as a", "iso": "boot",
		"microvm": "boot ... as a microvm", "timeout": "waiting up to",
		"click": "click", "double": "double", "key": "press", "type": "type", "run": "run",
		"ssh": "ssh", "settle": "settle", "ocr": "read",
		"when": "when", "when_not": "unless", "loop": "repeat",
		"shutdown": "stop", "acpi": "acpi", "qga": "qga", "wait_until": "wait until",
		"check": "decide", "screenshot": "screenshot", "compress": "compress",
		"scp":   "scp",
		"write": "write", "reboot": "reboot",
		"require_ssh": "require_ssh", "see": "read", "fetch": "fetch", "esp_fallback": "esp_fallback",
		"adb": "adb", "at_color": "at_color", "at_button": "at_button",
		// THE REST OF THE VOCABULARY, each with the phrase a file writes. A key whose verb is a bare
		// passthrough is still covered: `desktop linux` produces `desktop: linux`.
		"at_bottom": "at_bottom", "at_fallback": "at_fallback", "clear": "type ... clearing",
		"type_gap": "type_gap", "terminal_wait": "terminal_wait", "desktop": "desktop",
		"disk_settle": "disk_settle", "disk_min_mb": "disk_min_mb", "disk_gpt": "disk gpt",
		"disk_esp_mib": "disk esp", "memory_mb": "with memory", "answer_cd": "answer_cd",
		"driver_cd": "driver_cd", "recovery": "recovery", "release": "release",
		"template": "template", "ssh_probe": "ssh_probe", "blind_after": "blind_after",
		"if": "if", "then": "if ... then", "do": "repeat", "esp_from": "esp_from",
		"at_color_tolerance": "at_color", "colour": "decide", "text": "decide",
		"want_all": "decide", "poll": "poll", "optional": "optionally",
		"settle_after": "settle", "url": "fetch", "kind": "fetch",
		"modifier":   "type",
		"keep_alive": "desktop", "metadata": "fetch", "adb_connect": "adb",
		"vga": "boot", "cpus": "with cpu", "at_button_fallback": "at_button",
		// THE READING'S OWN SUBKEYS, which appear as keys of a `read`/`decide` block rather than as
		// step verbs - `rect 0 0 100 100` inside `read the screen as "x"`.
		"rect": "read ... within", "rect_at": "read ... measured at", "scale": "read ... scaled",
		"when_not_any": "unless any of", "skip_when_unfinished": "if finished",
		"color_region": "at_color within", "color_tolerance": "at_color tolerance",
		"else": "if ... else", "ssh_after_reboot": "require_ssh after a reboot",
		"color": "decide", "all": "decide", "any": "decide", "not": "decide",
		"must": "decide ... must", "like": "contains", "fuzzy": "contains ... fuzzy",
		"ratio": "contains ... similar", "contains": "contains", "contains_any": "contains any",
		"not_contains": "contains none", "matches": "matches", "equals": "equals",
		"between": "between", "less_than": "less than", "greater_than": "greater than",
		"at_least": "at least", "at_most": "at most", "minus": "minus",
		"adb_privileged": "adb with root",
	}
	v, ok := verbs[key]
	return v, ok
}

// bareOrQuoted accepts a quoted string or a single bare word.
//
// THE RULE IS "quotes when a value the log echoes has spaces in it", which is what makes
// `user vmuser` and `recording "mint install.mp4"` both read correctly.
func bareOrQuoted(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("no value")
	}
	if strings.HasPrefix(s, "\"") {
		return unquote(s)
	}
	if strings.ContainsAny(s, " \t") {
		return "", fmt.Errorf("%q has a space in it, so it needs quotes", s)
	}
	return s, nil
}

// parseDecideBlock builds a `want:` from `decide "name" is any of:` and its comparison lines.
//
// THE COMPARISONS ARE THE BLOCK, because a decision with no comparisons is a decision that is always
// true - which is the one thing a `want:` must not be.
//
//	decide "that menu names mint" is any of:
//	  grub contains "start linux mint"
//	  grub contains "linux mint"
//
// becomes
//
//	want:
//	  name: that menu names mint
//	  any:
//	    - text: [{from: grub, contains: [start, linux, mint]}]
func parseDecideBlock(head line, rest string, children []line) (map[string]any, error) {
	name, after, err := firstQuote(rest)
	if err != nil {
		return nil, fmt.Errorf("line %d: `decide \"name\"` needs a quoted name: %w", head.no, err)
	}
	want := map[string]any{"name": name}
	// `is any of:` / `is all of:` / `is:` choose the quantifier.
	quantifier := "all"
	if strings.Contains(after, "any of") {
		quantifier = "any"
	} else if strings.Contains(after, "not") {
		quantifier = "not"
	}
	if len(children) == 0 {
		return nil, fmt.Errorf("line %d: `decide` has no comparisons under it, so it would always "+
			"be true", head.no)
	}
	var comparisons []any
	for _, c := range children {
		cmp, err := parseComparison(c)
		if err != nil {
			return nil, err
		}
		comparisons = append(comparisons, cmp)
	}
	if quantifier == "not" {
		want["not"] = comparisons[0]
	} else {
		want[quantifier] = comparisons
	}
	// THE NAME CARRIES NO TRAILING COLON: it is the block's syntax, and a name holding it makes the
	// YAML decoder read `name: decide "x" is all of:` as a mapping.
	return map[string]any{"name": strings.TrimSuffix(head.text, ":"), "want": want}, nil
}

// parseComparison reads one line of a `decide` block: `<reading> <relation> <value>`.
//
//	grub contains "start linux mint"     -> text: [{from: grub, contains: [...]}]
//	panel.spread is less than 42         -> panel.spread: {less_than: 42}
//	panel.flat_rows is at least 3        -> panel.flat_rows: {at_least: 3}
func parseComparison(c line) (map[string]any, error) {
	if name, after, err := firstQuote(""); err == nil {
		_ = name
		_ = after
	}
	// `X is similar to "phrase" within 0.85` - THE FUZZY ONE, and it is a ratio over the whole
	// cropped region because that is the only unit where a ratio is reliable: a lost glyph scores
	// 0.90 and up, a wrong crop scores 0.35.
	if i := strings.Index(c.text, " is similar to "); i >= 0 {
		reading := strings.TrimSpace(c.text[:i])
		arg := strings.TrimSpace(c.text[i+len(" is similar to "):])
		phrase, after, err := firstQuote(arg)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", c.no, err)
		}
		ratio := 0.85
		if j := strings.Index(after, "within "); j >= 0 {
			if v, err := strconv.ParseFloat(strings.Fields(after[j+7:])[0], 64); err == nil {
				ratio = v
			}
		}
		return map[string]any{"text": []any{map[string]any{
			"from": reading, "like": phrase, "ratio": ratio,
		}}}, nil
	}
	// A TEXT COMPARISON names a reading, a verb, and a quoted phrase.
	for _, verb := range []string{"contains", "matches", "equals"} {
		if i := strings.Index(c.text, " "+verb+" "); i >= 0 {
			reading := strings.TrimSpace(c.text[:i])
			arg := strings.TrimSpace(c.text[i+len(verb)+2:])
			if verb == "contains" {
				// THE PHRASE IS A WORD LIST: `contains "start linux mint"` is every one of those
				// words, which is what the YAML's `contains:` takes.
				phrase, err := unquote(arg)
				if err != nil {
					return nil, fmt.Errorf("line %d: %w", c.no, err)
				}
				return map[string]any{"text": []any{map[string]any{
					"from":     reading,
					"contains": strings.Fields(phrase),
				}}}, nil
			}
			phrase, err := unquote(arg)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", c.no, err)
			}
			return map[string]any{"text": []any{map[string]any{
				"from": reading, verb: phrase,
			}}}, nil
		}
	}
	// A NUMERIC COMPARISON names a reading, a relation and a number.
	relations := []struct{ phrase, key string }{
		{"is less than", "less_than"}, {"is greater than", "greater_than"},
		{"is at least", "at_least"}, {"is at most", "at_most"},
		{"is between", "between"}, {"is", "equals"},
	}
	for _, r := range relations {
		if i := strings.Index(c.text, " "+r.phrase+" "); i >= 0 {
			// `panel mean` IS `panel.mean`: a reading and the number measured from it are one name
			// with a dot, which is what `want:` compares.
			reading := dottedName(c.text[:i])
			value := strings.TrimSpace(c.text[i+len(r.phrase)+2:])
			if r.key == "between" {
				// `between 20 and 60` - the word "and" is how a person says a range.
				fields := strings.Fields(strings.ReplaceAll(value, " and ", " "))
				if len(fields) != 2 {
					return nil, fmt.Errorf("line %d: `is between` takes two numbers: %q", c.no, value)
				}
				lo, err1 := strconv.ParseFloat(fields[0], 64)
				hi, err2 := strconv.ParseFloat(fields[1], 64)
				if err1 != nil || err2 != nil {
					return nil, fmt.Errorf("line %d: `is between` takes two numbers: %q", c.no, value)
				}
				return map[string]any{reading: map[string]any{"between": []any{lo, hi}}}, nil
			}
			v, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return nil, fmt.Errorf("line %d: %q is not a number", c.no, value)
			}
			return map[string]any{reading: map[string]any{r.key: v}}, nil
		}
	}
	return nil, fmt.Errorf("line %d: %q is not a comparison. Write `<reading> contains \"words\"` "+
		"or `<reading> is less than 42`", c.no, c.text)
}

// parseReadBlock builds a `see:` that names WHAT TO MEASURE, written as an indented list:
//
//	read the bottom 4.5% of the screen as "panel"
//	  measuring mean spread sat flat_rows
//
// becomes
//
//	see: {name: panel, rect: "bottom 4.5%", of: [mean, spread, sat, flat_rows]}
//
// `of:` IS THE LIST OF NUMBERS, and it is the one thing a person has to say because the measurement
// is a CHOICE: a panel is asked whether it is flat, a launcher whether it is saturated, and a window
// whether it is bright. Naming them is what keeps a verdict readable.
func parseReadBlock(head line, rest string, children []line) (map[string]any, error) {
	name, _, err := firstQuote(rest)
	if err != nil {
		return nil, fmt.Errorf("line %d: `read ... as \"name\"` needs a quoted name: %w", head.no, err)
	}
	// A `read` WITH NO BLOCK IS A TEXT READING, which is the common case: `read the screen as "page"`
	// means "the words on it". A block is how a file asks for NUMBERS instead, and that choice is
	// what `measuring` spells out.
	if len(children) == 0 {
		return parseRead(head, rest, map[string]any{"name": head.text})
	}
	see := map[string]any{"name": name}
	// THE AREA, if the phrase names one. `the bottom 4.5% of the screen` is a fraction the vocabulary
	// already understands, so it is passed through rather than converted here.
	if i := strings.Index(rest, " of the "); i >= 0 {
		if j := strings.LastIndex(rest, " as "); j > i {
			// THE AREA IS THE PHRASE BEFORE `of the`, with a leading `the` dropped: "the middle"
			// is `middle`, and "the bottom 4.5%" is `bottom 4.5%`.
			// `rest` IS EVERYTHING AFTER THE VERB, so the area is its own prefix - no offset needed.
			area := strings.TrimSpace(rest[:i])
			area = strings.TrimPrefix(area, "the ")
			see["rect"] = strings.TrimSpace(area)
		}
	}
	// `measuring mean spread sat` - one line, whitespace separated.
	var of []any
	for _, c := range children {
		verb, list := cutVerb(c.text)
		if verb != "measuring" && verb != "measuring:" {
			return nil, fmt.Errorf("line %d: a `read` block takes `measuring <numbers>`, not %q",
				c.no, c.text)
		}
		for _, w := range strings.Fields(strings.TrimSuffix(list, ":")) {
			of = append(of, w)
		}
	}
	if len(of) == 0 {
		return nil, fmt.Errorf("line %d: `read ... measuring` names no measurement, so the reading "+
			"would bind nothing", head.no)
	}
	see["of"] = of
	return map[string]any{"name": head.text, "see": see}, nil
}

// dottedName turns `panel mean` into `panel.mean`, and leaves a single word alone.
//
// A PERSON WRITES `panel mean is between 20 and 60`, because a reading and the number measured from it
// are one thought. The vocabulary names it with a dot - `panel.mean` - because that is what `want:`
// compares, so the phrase is joined here rather than in the file.
func dottedName(s string) string {
	fields := strings.Fields(strings.TrimSpace(s))
	switch len(fields) {
	case 1:
		return fields[0]
	case 2:
		return fields[0] + "." + fields[1]
	}
	return strings.Join(fields, ".")
}

// parseExact is the ESCAPE HATCH, and it exists so that a converted file is EXACT rather than nearly
// exact.
//
//	step "the greeter - the password box"
//	  click == [936, 628]
//	  settle == 3
//	  optional == true
//
// THE RIGHT-HAND SIDE IS YAML, so any value the vocabulary takes can be written - a nested mapping, a
// list of lists, a number, a boolean. That is what makes a mechanical conversion possible: a converter
// emits the natural phrasing where it knows one and `key == value` where it does not, and the
// resulting document is the same one the YAML produced.
//
// IT IS NOT THE PREFERRED FORM. A file written by hand should say `click "next" at 1777 1059`; this is
// for the cases the phrases do not cover, and for the conversion itself.
func parseExact(head line, rest string, children []line) (map[string]any, error) {
	// A QUOTED NAME MAY CONTAIN ESCAPED QUOTES, because a step's name is a sentence and some of them
	// quote: `give the installer its last few seconds, because "ready to use" is not "flushed"`.
	// `unquote` strips the outer pair and leaves the escapes; YAML is what turns `\"` back into `"`,
	// so the name goes through it.
	name, err := bareOrQuoted(rest)
	if err != nil {
		return nil, fmt.Errorf("line %d: `step` needs a name: %w", head.no, err)
	}
	if strings.Contains(name, `\"`) {
		var decoded string
		if err := yaml.Unmarshal([]byte(`"`+name+`"`), &decoded); err == nil {
			name = decoded
		}
	}
	step := map[string]any{"name": name}
	for _, c := range children {
		key, value, ok := strings.Cut(c.text, "==")
		if !ok {
			return nil, fmt.Errorf("line %d: a `step` body is `key == value`, not %q", c.no, c.text)
		}
		key = strings.TrimSpace(key)
		if _, exists := step[key]; exists {
			return nil, fmt.Errorf("line %d: `%s` is written twice in one step", c.no, key)
		}
		var v any
		if err := yaml.Unmarshal([]byte(strings.TrimSpace(value)), &v); err != nil {
			return nil, fmt.Errorf("line %d: `%s == %s` is not a YAML value: %v", c.no, key,
				strings.TrimSpace(value), err)
		}
		step[key] = v
	}
	return step, nil
}

// yamlUnescape turns `\"` back into `"` in a quoted name.
//
// A STEP'S NAME IS A SENTENCE, and a sentence may quote another: `because "ready to use" is not
// "flushed"`. The emitter escapes those quotes to keep the line parseable, so the parser has to undo
// it - otherwise the name comes back with four extra backslashes in it.
func yamlUnescape(s string) string {
	if !strings.Contains(s, `\"`) {
		return s
	}
	var decoded string
	if err := yaml.Unmarshal([]byte(`"`+s+`"`), &decoded); err == nil {
		return decoded
	}
	return strings.ReplaceAll(s, `\"`, `"`)
}
