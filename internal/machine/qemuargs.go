package machine

// THE RAW COMMAND LINE A SHAPE ASKS FOR, RENDERED.
//
// THIS FILE REPLACES SIX FUNCTIONS AND A BOOL. `VGA`, `StorageArgs`' two branches, `AudioArgs`' two,
// `SMCArgs`, `KernelArgs`, `microvm`'s six conditionals, `virtioBlk` and `VirtioNet` all answered
// "what does this machine look like" - and every one of those answers is a fact about a guest, which
// belongs in a shape file rather than in Go.
//
// WHAT IS LEFT HERE IS ONE SUBSTITUTION AND ONE DROP RULE:
//
//	{{name}}          replaced with the value the run has for it
//	a pair with an empty placeholder  DROPPED, which is how an optional medium disappears
//
// The drop rule is the reason an optional device needs no `if` anywhere: `{{iso}}` is empty for a boot
// with no installer, so the whole `-drive file={{iso}}...` pair goes away.

import (
	"strings"
)

// RenderValues are the substitutions a shape's `qemu.args` may use.
//
// EVERY KEY IS SOMETHING THE RUN DECIDES: a path it was given, or a fragment that depends on what the
// boot attaches. A shape cannot invent one, and an unknown placeholder is an error rather than an
// empty string - a typo that silently removed a device would be a machine that boots and cannot be
// reached.
type RenderValues map[string]string

// renderPair substitutes every placeholder in one argument pair.
//
// AN EMPTY PLACEHOLDER DROPS THE PAIR ONLY WHEN IT IS THE WHOLE ARGUMENT. That distinction is the
// difference between two kinds of placeholder and it has to be made here, because both are written
// the same way:
//
//	["-drive", "file={{iso}},media=cdrom"]   no ISO -> the whole medium is absent, DROP the pair
//	["-drive", "file={{disk}}...,id=target{{discard}}"]   no discard -> keep the pair, drop the
//	                                                       fragment
//
// SO: an argument that IS a placeholder and resolves to empty drops the pair; a placeholder INSIDE a
// longer string resolves to nothing and the rest of the string stands. `id=target` is a valid id, and
// `file=` is not a valid drive - which is exactly the difference the two cases need.
func renderPair(pair []string, v RenderValues) ([]string, bool) {
	out := make([]string, 0, len(pair))
	for _, a := range pair {
		rendered, drop := substituteArg(a, v)
		if drop {
			return nil, false
		}
		out = append(out, rendered)
	}
	return out, true
}

// substituteArg renders one argument, and says whether the PAIR should be dropped.
//
// drop IS TRUE ONLY WHEN THE WHOLE ARGUMENT WAS ONE PLACEHOLDER AND IT IS EMPTY. A placeholder in the
// middle of a longer string contributes nothing, which is how `id=target{{discard}}` keeps working.
func substituteArg(a string, v RenderValues) (string, bool) {
	out := a
	for {
		i := strings.Index(out, "{{")
		if i < 0 {
			break
		}
		j := strings.Index(out[i:], "}}")
		if j < 0 {
			break
		}
		name := out[i+2 : i+j]
		val, known := v[name]
		if !known {
			break
		}
		out = out[:i] + val + out[i+j+2:]
	}
	// THE WHOLE ARGUMENT WAS THE PLACEHOLDER, AND IT CAME OUT EMPTY.
	if out == "" && strings.HasPrefix(strings.TrimSpace(a), "{{") && strings.HasSuffix(strings.TrimSpace(a), "}}") {
		return "", true
	}
	return out, false
}


// RenderArgs renders a profile's `args` or `kernel_args`.
func RenderArgs(pairs [][]string, v RenderValues) []string {
	var out []string
	for _, pair := range pairs {
		if got, ok := renderPair(pair, v); ok {
			out = append(out, got...)
		}
	}
	return out
}

// CheckPlaceholders refuses a shape that names a value this build does not have.
//
// IT RUNS AT LOAD TIME, not at a boot: a profile with `{{recoveryy}}` would otherwise be a device that
// quietly never appeared, and the guest would boot without the medium its stage needed.
func (s Shape) CheckPlaceholders(allowed []string) error {
	known := map[string]bool{}
	for _, k := range allowed {
		known[k] = true
	}
	check := func(where, text string) error {
		for {
			i := strings.Index(text, "{{")
			if i < 0 {
				return nil
			}
			j := strings.Index(text[i:], "}}")
			if j < 0 {
				return nil
			}
			name := text[i+2 : i+j]
			if !known[name] {
				return &UnknownPlaceholder{Shape: s.Name, Where: where, Name: name}
			}
			text = text[i+j+2:]
		}
	}
	for profName, prof := range s.QEMU {
		if err := check("qemu.profiles."+profName+".machine", prof.Machine); err != nil {
			return err
		}
		for i, pair := range prof.Args {
			for _, a := range pair {
				if err := check("qemu.profiles."+profName+".args", a); err != nil {
					return err
				}
			}
			_ = i
		}
		for _, pair := range prof.KernelArgs {
			for _, a := range pair {
				if err := check("qemu.profiles."+profName+".kernel_args", a); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// UnknownPlaceholder is a shape that names a substitution this build does not fill.
type UnknownPlaceholder struct {
	Shape, Where, Name string
}

func (e *UnknownPlaceholder) Error() string {
	return "shape \"" + e.Shape + "\": " + e.Where + " uses {{" + e.Name + "}}, which this build " +
		"does not fill - so the argument would be left literal and the device would never appear"
}

// KnownPlaceholders is every substitution the daemon fills, for a shape's load-time check.
//
// IT IS A LIST RATHER THAN A MAP because it is also what a reader of `QEMUProfile` is told, and the two
// must be the same set.
var KnownPlaceholders = []string{
	"disk", "iso", "recovery", "answer", "driver", "dir", "work",
	"kernel", "initrd", "append", "discard",
}

// RenderList renders a FLAT argument list, which is what a boot step writes after `args:` is
// flattened. `RenderArgs` takes pairs because a shape file writes them as pairs; a step's list has
// already been flattened by the parser, so the drop rule is applied per element.
func RenderList(flat []string, v RenderValues) []string {
	if len(flat) == 0 {
		return nil
	}
	// IT IS RENDERED AS PAIRS, because every qemu flag takes a value and the drop rule is about a
	// PAIR: an empty `{{iso}}` in `file={{iso}},media=cdrom` removes the flag AND its argument, never
	// one of them.
	var out []string
	for i := 0; i+1 < len(flat); i += 2 {
		if got, ok := renderPair(flat[i:i+2], v); ok {
			out = append(out, got...)
		}
	}
	return out
}
