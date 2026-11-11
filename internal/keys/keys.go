// Package keys turns text into qemu key events.
//
// THE TABLE LIVES HERE AND NOWHERE ELSE. The Python implementation had its only copy inside
// windows/11/ssh.py, and the macOS stage needed the same table - so a second copy was about to
// exist, and a second place for ":" to mean the wrong thing. The failure that produces is a colon
// typed as a semicolon, which reads in a log as a shell syntax error rather than as a keyboard
// bug, and it costs a stage's whole run before anyone suspects the input layer.
package keys

import "fmt"

// Named maps characters whose qcode is not the character itself.
var Named = map[rune]string{
	' ': "spc", '-': "minus", '=': "equal", '[': "bracket_left",
	']': "bracket_right", '\\': "backslash", ';': "semicolon", '\'': "apostrophe",
	',': "comma", '.': "dot", '/': "slash", '`': "grave_accent",
}

// Shifted maps a shifted character to the qcode of the UNSHIFTED key. The shift is sent as a
// modifier with it, because qemu has no "colon" key - only "shift" and "semicolon" held together.
var Shifted = map[rune]rune{
	'!': '1', '@': '2', '#': '3', '$': '4', '%': '5', '^': '6', '&': '7',
	'*': '8', '(': '9', ')': '0', '_': '-', '+': '=', '{': '[',
	'}': ']', '|': '\\', ':': ';', '"': '\'', '<': ',', '>': '.', '?': '/', '~': '`',
}

// Stroke is one keystroke: either a single key, or a key held with modifiers.
type Stroke struct {
	// Modifiers are qcodes pressed before Key and released after, in order.
	Modifiers []string
	// Key is the qcode of the key itself.
	Key string
}

// ForText returns every keystroke for text.
//
// NEWLINES AND TABS ARE REFUSED BY NAME. qemu cannot produce either as a character, and a caller
// that passes one has a command that needs splitting - a decision about the command, not a retry.
// The Windows stage learned this as "cannot map newline" after typing nothing at all, which read
// as a guest that ignored input.
func ForText(text string) ([]Stroke, error) {
	out := make([]Stroke, 0, len(text))
	for _, ch := range text {
		switch ch {
		case '\r', '\n', '\t':
			return nil, fmt.Errorf("cannot type %q; split the command and send it as separate "+
				"lines, because qemu has no key for it", ch)
		}
		if ch >= 'A' && ch <= 'Z' {
			out = append(out, Stroke{Modifiers: []string{"shift"}, Key: string(ch - 'A' + 'a')})
			continue
		}
		if base, ok := Shifted[ch]; ok {
			code, err := Qcode(base)
			if err != nil {
				return nil, err
			}
			out = append(out, Stroke{Modifiers: []string{"shift"}, Key: code})
			continue
		}
		code, err := Qcode(ch)
		if err != nil {
			return nil, err
		}
		out = append(out, Stroke{Key: code})
	}
	return out, nil
}

// Qcode is the qcode for one UNshifted character.
//
// It raises rather than guessing: an unshifted character with no qcode is a gap in the table, and
// typing something else silently is worse than stopping.
func Qcode(ch rune) (string, error) {
	if (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'z') {
		return string(ch), nil
	}
	if ch >= 'A' && ch <= 'Z' {
		return "", fmt.Errorf("%q needs shift; ForText returns it as a stroke and Qcode does not",
			ch)
	}
	if name, ok := Named[ch]; ok {
		return name, nil
	}
	return "", fmt.Errorf("no qcode for %q", ch)
}

// NamedKeys are the keys a walkthrough may name in `key:` that are not single characters.
//
// A YAML step says `key: ret` or `key: ctrl+alt+t`, so the vocabulary is QEMU's qcode names for
// the non-printing keys plus the modifier prefixes. Anything else is refused, because a
// misspelled key is a step that sends nothing and reports success.
var NamedKeys = map[string]bool{
	"ret": true, "esc": true, "tab": true, "spc": true, "backspace": true,
	"delete": true, "insert": true, "home": true, "end": true,
	"pgup": true, "pgdn": true, "up": true, "down": true, "left": true, "right": true,
	"caps_lock": true, "num_lock": true, "scroll_lock": true, "print": true,
	"pause": true, "menu": true, "sysrq": true, "f1": true, "f2": true, "f3": true,
	"f4": true, "f5": true, "f6": true, "f7": true, "f8": true,
	"f9": true, "f10": true, "f11": true, "f12": true,
	"shift": true, "ctrl": true, "alt": true, "meta_l": true, "meta_r": true,
}

// Modifiers are the names that may prefix a `+` combo.
var Modifiers = map[string]bool{
	"shift": true, "ctrl": true, "alt": true, "meta_l": true, "meta_r": true,
}

// ParseCombo reads a `key:` value into strokes.
//
//	"ret"          one stroke
//	"ctrl+alt+t"   three keys held together
//	"a"            the letter
//
// A COMBO IS ONE STROKE, NOT A SEQUENCE. `ctrl+alt+t` means the three are held at once, which is
// what opens a terminal; sending them one after another types three keys and does nothing.
func ParseCombo(value string) ([]Stroke, error) {
	if value == "" {
		return nil, fmt.Errorf("empty key")
	}
	parts := splitOnPlus(value)
	if len(parts) == 1 {
		// A single name, or a single character.
		if NamedKeys[parts[0]] {
			return []Stroke{{Key: parts[0]}}, nil
		}
		runes := []rune(parts[0])
		if len(runes) == 1 {
			return ForText(parts[0])
		}
		return nil, fmt.Errorf("%q is not a qcode name; known non-printing keys are in "+
			"keys.NamedKeys and a combination is written ctrl+alt+t", value)
	}
	stroke := Stroke{}
	for i, part := range parts {
		last := i == len(parts)-1
		if last {
			if part == "" {
				return nil, fmt.Errorf("%q ends with '+', so it names no key to press", value)
			}
			if NamedKeys[part] {
				stroke.Key = part
				continue
			}
			runes := []rune(part)
			if len(runes) != 1 {
				return nil, fmt.Errorf("%q: the last part of a combination must be one key", value)
			}
			code, err := Qcode(runes[0])
			if err != nil {
				// An uppercase letter in a combo is the shifted form of the lowercase key, and
				// the shift is already a modifier in the combination.
				return nil, fmt.Errorf("%q: %w", value, err)
			}
			stroke.Key = code
			continue
		}
		if !Modifiers[part] {
			return nil, fmt.Errorf("%q: %q is not a modifier; a combination may hold shift, "+
				"ctrl, alt, meta_l or meta_r", value, part)
		}
		stroke.Modifiers = append(stroke.Modifiers, part)
	}
	if stroke.Key == "" {
		return nil, fmt.Errorf("%q names only modifiers", value)
	}
	return []Stroke{stroke}, nil
}

// splitOnPlus splits a combination on '+'.
//
// A TRAILING '+' IS KEPT AS AN EMPTY PART, so `ctrl+` yields ["ctrl", ""] and ParseCombo refuses
// it as a combination naming only modifiers. The earlier version silently dropped the empty part,
// which made `ctrl+` parse as a valid single-key stroke - a step that held ctrl and pressed
// nothing, reported success, and left the reader looking at a screen that never changed.
func splitOnPlus(value string) []string {
	var out []string
	current := ""
	for _, r := range value {
		if r == '+' {
			out = append(out, current)
			current = ""
			continue
		}
		current += string(r)
	}
	out = append(out, current)
	return out
}
