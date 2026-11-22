package step

import (
	"fmt"
	"math"
	"strings"
)

// checkKind reports whether a value has the shape its key declares.
//
// `Validate` USED TO CHECK ONLY THAT THE KEY EXISTS, and `Kind` existed but drove nothing - the comment
// above it said "it drives validation", and nothing read it. MEASURED: all three of these passed
// `easyvm check` and would have reached a guest:
//
//	click: "not a point"        a string where [x, y] is required
//	click: [1, 2, 3]            three numbers where two are
//	click: [1.5, 2.5]           a fraction where an integer pixel is
//
// The first would fail inside `parsePoint` with a message about coordinates at the moment the step
// ran, which is a machine already booted and forty minutes spent. The second and third would be
// silently truncated by `int(f)` and click somewhere nobody asked for - the worst kind, because
// nothing reports it.
//
// IT IS A SHAPE CHECK, NOT A RANGE CHECK: whether a coordinate fits the frame is
// `TestEveryCoordinateFitsTheFrameItDeclares`, which reads the file's own `resolution:`.
func checkKind(key Key, value any) error {
	switch key.Kind {
	case KindString, KindScript:
		if _, ok := value.(string); !ok {
			return expect(key, "a string", value)
		}
	case KindBool:
		// YAML 1.1 makes `on` a bool, and this project has been bitten by that, so a bool is a bool.
		if _, ok := value.(bool); !ok {
			return expect(key, "true or false", value)
		}
	case KindInt:
		if _, ok := asInt(value); !ok {
			return expect(key, "a whole number", value)
		}
	case KindFloat:
		if _, ok := asFloat(value); !ok {
			return expect(key, "a number", value)
		}
	case KindPair:
		return checkNumbers(key, value, 2, "[x, y]")
	case KindWordsOrNone:
		if m, ok := value.(map[string]any); ok {
			none, has := m["none"]
			if !has {
				return fmt.Errorf("`%s:` as a mapping needs `none:` - `when: {none: [[a], [b, c]]}`", key.Name)
			}
			for k := range m {
				if k != "none" {
					return fmt.Errorf("`%s:` has no field `%s:`; the mapping form is `{none: <word-sets>}`", key.Name, k)
				}
			}
			return checkKind(Key{Name: key.Name, Kind: KindWordSets}, none)
		}
		return checkKind(Key{Name: key.Name, Kind: KindWords}, value)
	case KindStringOrMapping:
		if m, ok := value.(map[string]any); ok {
			// THE FIELDS ARE NAMED AND CHECKED, because a mapping that accepts anything is a second
			// vocabulary: `disk: {size: 128}` without `file:` would be accepted here and then refused
			// by the executor, which is the "loads and does nothing" shape this package exists to stop.
			allowed := map[string]bool{
				"file": true, "size": true, "gpt": true,
				"esp_mib": true, "min_mb": true, "wait": true,
			}
			for k := range m {
				if !allowed[k] {
					return fmt.Errorf("`%s:` has no field `%s:`; the ones it has are "+
						"file, size, gpt, esp_mib, min_mb, wait", key.Name, k)
				}
			}
			if _, hasFile := m["file"]; !hasFile {
				return fmt.Errorf("`%s:` as a mapping needs `file:`", key.Name)
			}
			return nil
		}
		if _, ok := value.(string); !ok {
			return expect(key, "a file name or a mapping", value)
		}
		return nil
	case KindPairOrMapping:
		if _, ok := value.(map[string]any); ok {
			return nil
		}
		return checkNumbers(key, value, 2, "[x, y] or {button: [x, y]} / {colour: [r, g, b]}")
	case KindRect:
		return checkNumbers(key, value, 4, "[x0, y0, x1, y1]")
	case KindWords:
		// A single string is one phrase; a list is words that must all be present.
		switch v := value.(type) {
		case string:
			return nil
		case []any:
			for _, item := range v {
				if _, ok := item.(string); !ok {
					return expect(key, "a string or a list of strings", value)
				}
			}
			return nil
		}
		return expect(key, "a string or a list of strings", value)
	case KindWordSets:
		list, ok := value.([]any)
		if !ok {
			return expect(key, "a list of word lists", value)
		}
		for _, item := range list {
			switch inner := item.(type) {
			case string:
				continue
			case []any:
				for _, w := range inner {
					if _, ok := w.(string); !ok {
						return expect(key, "a list of word lists", value)
					}
				}
			default:
				return expect(key, "a list of word lists", value)
			}
		}
	case KindColor:
		return checkNumbers(key, value, 3, "[r, g, b]")
	case KindShot:
		// `see: {name: shot}` OR `see: shot`. Nothing else: a mapping with parameters is the old
		// model and the executor names each parameter it refuses.
		switch v := value.(type) {
		case string:
			if strings.TrimSpace(v) == "" {
				return expect(key, "a frame name", value)
			}
			return nil
		case map[string]any:
			for k := range v {
				if k != "name" {
					return fmt.Errorf("`see:` has no key %q: it captures a frame and names it. "+
						"Every other setting is a QUESTION and belongs on the `check:` that asks it", k)
				}
			}
			if name, _ := v["name"].(string); strings.TrimSpace(name) == "" {
				return expect(key, "a frame name", value)
			}
			return nil
		}
		return expect(key, "a frame name", value)
	case KindAny:
		// A KEY THAT REACHES HERE OWES A CHECK OF ITS OWN, because `KindAny` means "the executor
		// decides" and an unvalidated key is what this package exists to stop.
		if key.Name == "scp" {
			return checkSCP(key, value)
		}
		return nil
	case KindTemplated:
		// A `{{ }}` PLACEHOLDER IS RESOLVED AFTER THIS MAP IS READ, so the value here may still be
		// the template text. Anything else must be a number.
		if str, ok := value.(string); ok && strings.Contains(str, "{{") {
			return nil
		}
		if _, ok := asFloat(value); !ok {
			return expect(key, "a number or a {{ template }}", value)
		}
	case KindMapping:
		if _, ok := value.(map[string]any); !ok {
			return expect(key, "a mapping", value)
		}
	case KindList:
		if _, ok := value.([]any); !ok {
			return expect(key, "a list", value)
		}
	}
	return nil
}

// checkNumbers requires a list of exactly n integers.
//
// A FRACTION IS REFUSED RATHER THAN TRUNCATED. `int(1.5)` is 1, so a `click: [1.5, 2.5]` would land on
// pixel (1, 2) and report nothing - and a coordinate is a measurement, so a half of one means the
// number came from somewhere that was not a screen.
func checkNumbers(key Key, value any, n int, shape string) error {
	list, ok := value.([]any)
	if !ok {
		return expect(key, shape, value)
	}
	if len(list) != n {
		return fmt.Errorf("`%s` is %s - %d number(s) - not the %d it was given",
			key.Name, shape, n, len(list))
	}
	for i, item := range list {
		v, ok := asFloat(item)
		if !ok {
			return expect(key, shape, value)
		}
		if v != math.Trunc(v) {
			return fmt.Errorf("`%s` is %s and entry %d is %v: a coordinate is a whole pixel, so a "+
				"fraction means the number was not read off a screen", key.Name, shape, i+1, item)
		}
	}
	return nil
}

func expect(key Key, want string, got any) error {
	return fmt.Errorf("`%s` needs %s, got %T (%v)", key.Name, want, got, got)
}

// asInt and asFloat accept every numeric shape a YAML decoder and the `.walk` converter produce.
func asInt(v any) (int, bool) {
	switch t := v.(type) {
	case int:
		return t, true
	case int64:
		return int(t), true
	case float64:
		if t != math.Trunc(t) {
			return 0, false
		}
		return int(t), true
	}
	return 0, false
}

func asFloat(v any) (float64, bool) {
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

// checkSCP accepts the two forms `scp:` has, and nothing else.
//
//	[x, y]                          a list of exactly two names
//	{template: KIND, to: REMOTE}    a bootstrap file to render and one destination
func checkSCP(key Key, value any) error {
	switch v := value.(type) {
	case []any:
		if len(v) != 2 {
			return expect(key, "[local-file, remote-path] - 2 names", value)
		}
		for _, item := range v {
			if str, ok := item.(string); !ok || strings.TrimSpace(str) == "" {
				return expect(key, "[local-file, remote-path] - 2 names", value)
			}
		}
		return nil
	case map[string]any:
		kind, _ := v["template"].(string)
		to, _ := v["to"].(string)
		if strings.TrimSpace(kind) == "" || strings.TrimSpace(to) == "" {
			return expect(key, "{template: KIND, to: REMOTE} - both are required", value)
		}
		for k := range v {
			if k != "template" && k != "to" {
				return fmt.Errorf("`scp:` has no key %q; the mapping form is {template, to}", k)
			}
		}
		switch kind {
		case "launcher", "macos_agent_plist", "windows_task":
			return nil
		}
		return fmt.Errorf("`scp: {template: %s}` is not a file this build writes; the kinds are "+
			"launcher, macos_agent_plist, windows_task", kind)
	}
	return expect(key, "[local-file, remote-path] or {template, to}", value)
}
