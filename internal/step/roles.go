package step

import (
	"fmt"
	"strings"
)

// A STEP IS ONE ACTION AND ITS ARGUMENTS, AND UNTIL THIS FILE NOTHING SAID SO.
//
// The vocabulary has always been a FLAT key space: `boot`, `os`, `iso`, `memory_mb` and `timeout` are
// five names at the same level, and what made `os:` an argument of `boot:` rather than an action of
// its own was the ORDER OF THE CASES IN `runOne`. Measured consequences on the tree this was written
// against:
//
//	63 steps wrote `click:` or `double:` BESIDE `check:`, and `doCheck` never reads either - the
//	dispatcher reaches `case s.Has("check")` first and returns, so the click NEVER RAN and the step
//	reported the verdict it was supposed to reach by clicking
//
//	54 steps wrote `click:` beside `type:`, which DOES work - but only because `doType` happens to
//	read `click:` as its own argument. Same shape in the file, opposite outcome, decided by whether
//	somebody remembered an `if s.Has(...)` inside a `do*` body
//
// So "which key is the action" was never declared; it was implied by function order. This file
// declares it, and `Validate` refuses a step that names two actions the chosen one does not consume.
//
// THE RULE IS "AT MOST ONE ACTION", NOT "EXACTLY ONE", because two shapes have no action at all and
// are deliberate:
//
//	{needs: [...]}            a gate on the stage - "these files must be on disk". Measured 18 uses.
//	{settle: 30}              "let the screen draw". Measured 6 uses.
//
// Both are gated by verbs (`needs:`, `settle:`) rather than being absent, so "no action" is itself a
// statement a file makes on purpose.

// Role is what a key IS in a step, which is not the same question as its Kind.
//
//	Kind   says what SHAPE the value has  (`[x, y]`, a string, a list)
//	Role   says what the key DOES        (the action, an argument, a gate, a modifier)
//
// `click` is the action in `{click: [1, 2]}` and an ARGUMENT in `{type: "hi", click: [1, 2]}`. One
// key, two roles, and the role depends on which action is in the step - so Role is declared per key
// as its DEFAULT, and an action may additionally CONSUME other keys as arguments. See Consumes.
type Role int

const (
	// RoleUnknown is a key this file has not classified. It is not an error to be here - the
	// vocabulary is large and some keys are sub-keys of a mapping - but a key that is a step key
	// MUST be classified, and `TestEveryStepKeyHasARole` is what enforces it.
	RoleUnknown Role = iota

	// RoleAction is a key that STARTS WORK when it is present. A step may name at most one of
	// these that no other named action consumes.
	RoleAction

	// RoleArgument is a key that is only meaningful beside some action - `os`, `iso`, `memory_mb`.
	// It does not start anything; it describes what the action works on.
	RoleArgument

	// RoleModifier is a key that applies to whatever the step does: `timeout`, `optional`,
	// `settle`, `requires`. These ride with any action.
	RoleModifier

	// RoleGate is a conditional wrapper: `when`, `when_not`, `when_not_any`, `needs`, `requires`.
	// Like an action it changes control flow, but it is not the work - it decides whether the work
	// happens, and the dispatcher re-enters itself with the gate removed.
	RoleGate

	// RoleSubkey is a name that is meaningful INSIDE another key's mapping and is not a step key
	// at all - `mode` inside `check:`, `from` inside `see:`. It is declared in this package because
	// one table of names is easier to audit than two.
	RoleSubkey
)

func (r Role) String() string {
	switch r {
	case RoleAction:
		return "action"
	case RoleArgument:
		return "argument"
	case RoleModifier:
		return "modifier"
	case RoleGate:
		return "gate"
	case RoleSubkey:
		return "subkey"
	}
	return "unknown"
}

// CheckOneAction refuses a step that names two actions, unless the earlier one consumes the later.
//
// THE MESSAGE NAMES THE LOST KEY, because that is the whole point: a file that wrote
// `double:` beside `check:` looks correct, loads today, and runs the check without the click. The
// reader of the error has to learn which of the two was doing nothing.
// has reports whether a mapping carries this key at all, including a false value.
func has(mapping map[string]any, key string) bool {
	_, ok := mapping[key]
	return ok
}

// CheckOptionalDoesNotAct refuses `optional:` on a step that presses, clicks or types.
//
// `optional:` MEANS "THIS MISS IS NOT FATAL", AND IT WAS BEING READ AS "THIS STEP MAY DO NOTHING".
// Those are different, and the gap between them cost a real run.
//
// MEASURED ON THE POD, `linux/fedora-workstation/steps/ssh.walk`: the greeter check read the same
// region five times and answered false every time - correctly, that disk has no greeter - and then
//
//	greeter - wake the screen    key esc
//	greeter - the user tile      click (960, 488)
//	greeter - the password box   click (936, 628), type "admin"
//	greeter - submit             key ret
//
// ALL RAN ANYWAY. They were `optional`, so their failures were swallowed, and their ACTIONS went
// into the GNOME Initial Setup window that was actually on screen - Escape, a click, five
// characters and a Return, typed into a wizard nobody had asked them to touch. The wizard's own step
// afterwards still read "welcome to fedora" and passed, which is why the damage only surfaced two
// steps later as "the qmp connection is closed".
//
// SO THE TWO KINDS OF STEP ARE TOLD APART. A step that only LOOKS - `check:`, `see:`, `desktop:`,
// `until:` - may be optional: a screen that does not show the thing is a fact worth recording, not an
// action worth taking. A step that ACTS - clicks, types, presses - must be gated by `when:`/`unless:`
// or not be written at all. A conditional action is a gate AROUND an action; `optional` is a shrug
// after one, and a shrug cannot un-press a key.
//
// `when:`/`unless:` ARE NOT REFUSED HERE, because a gated action is exactly the shape being asked
// for - the gate decides whether the action runs, so its absence is not a miss.
func CheckOptionalDoesNotAct(stepName string, mapping map[string]any) error {
	if !has(mapping, "optional") {
		return nil
	}
	if has(mapping, "when") || has(mapping, "when_not") || has(mapping, "when_not_any") || has(mapping, "unless") {
		return nil
	}
	// ONLY THE KEYS THAT PUT SOMETHING INTO THE GUEST ARE REFUSED.
	//
	// `loop:` IS AN ACTION IN `Roles` BECAUSE IT RUNS SOMETHING, but its `do:` may be nothing but
	// `settle:` - waiting for a pane that may never come is a legitimate optional step, and refusing
	// it would delete eleven files' worth of screens that are simply allowed to be absent. The keys
	// below are the ones that cannot be undone: a click lands somewhere, a key is pressed, a command
	// runs. `loop:`'s own body is checked when it runs, by the same dispatcher.
	// EVERY KEY THAT ACTS IS ON THIS LIST, INCLUDING THE ONES THAT REACH THE GUEST OVER A CHANNEL.
	//
	// I FIRST LEFT `ssh:`, `run:` AND `scp:` OUT, ARGUING THEY WERE SAFER THAN A KEYSTROKE: they reach
	// a guest the stage already owns, so a failure is "a fact about that command". THAT ARGUMENT IS
	// WRONG, and the same run proves it. `systemctl enable sshd.socket` failing because the unit does
	// not exist, swallowed by `optional`, leaves the stage reporting success on a machine where the
	// socket is still not enabled - and the NEXT step then waits ten minutes for a port that this one
	// silently failed to arrange. A swallowed command failure is the same lie as a swallowed click:
	// the stage moves on believing something it never checked.
	//
	// THE ANSWER TO "THIS COMMAND MAY LEGITIMATELY FAIL" IS NOT `optional` - it is one of:
	//
	//	wait until the condition holds, then run it and let it fail;
	//	write the tolerance INTO the command (`... || true`), where the log shows it;
	//	split it, so the part that can fail is a `check:` that may be optional.
	//
	// A step that is `optional` AND acts is a step whose failure has nowhere to go.
	presses := map[string]bool{
		"click": true, "double": true, "key": true, "press": true, "type": true,
		"run": true, "ssh": true, "scp": true, "script": true, "qga": true, "adb": true,
	}
	for key := range mapping {
		if !presses[key] {
			continue
		}
		return fmt.Errorf("step %q is `optional` AND does `%s:` - `optional` swallows a failure, "+
			"it does not stop the action, so this %s happens whether or not the screen asked for it. "+
			"Gate it with `when:` (or `unless:`), or delete it: a conditional action is a gate around "+
			"an action, and `optional` is a shrug after one. Measured: four `optional` greeter steps "+
			"typed Escape, a click, a password and a Return into a GNOME wizard that was on screen "+
			"because the greeter was not.",
			stepName, key, key)
	}
	return nil
}

func quoteAll(names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = "`" + n + ":`"
	}
	return strings.Join(out, ", ")
}

func plural(n int) string {
	if n == 1 {
		return "runs"
	}
	return "run"
}

