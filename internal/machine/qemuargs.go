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
// TWO KINDS OF EMPTY, AND THEY ARE NOT THE SAME. This is the whole difficulty of the drop rule:
//
//	file={{iso}},media=cdrom     a drive whose path is EMPTY IS NOT A DRIVE - drop the pair
//	id=target{{discard}}         `discard` is a SUFFIX; empty means "no discard", keep the pair
//
// THE DIFFERENCE IS WHAT THE PLACEHOLDER REPLACES. When it IS the value of a `key=` field - it begins
// the field and stands alone - an empty answer means the field does not apply, and qemu's own answer
// to `file=` is a device with no medium that it then refuses to boot. When it is APPENDED to a value
// (`target{{discard}}`) an empty answer is a fragment that is not there.
//
// MEASURED on ubuntu-install, run 45148972712: the ISO pull had not run, `{{iso}}` was empty, and the
// argument reached qemu as `-drive file=,media=cdrom,...`. The guest then had a CD-ROM with no
// medium, the firmware found no bootable device, and the run sat on `BdsDxe: No bootable option`
// for forty minutes instead of failing at its first step.
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
		if val == "" {
			// IS THIS PLACEHOLDER A WHOLE FIELD VALUE? It is when what precededs it ends in `=`
			// (or it starts the argument) and what follows is `,` or the end.
			before := out[:i]
			after := out[i+j+2:]
			wholeField := (before == "" || strings.HasSuffix(before, "=")) &&
				(after == "" || strings.HasPrefix(after, ","))
			if wholeField {
				return "", true
			}
		}
		out = out[:i] + val + out[i+j+2:]
	}
	return out, false
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
