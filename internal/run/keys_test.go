package run

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/easydev-org/easyvm/internal/step"
)

// EVERY KEY CLAIMED HERE MUST HAVE A BRANCH IN THE DISPATCHER.
//
// This is the check the Python implementation never had: a key accepted by the vocabulary and never
// handled by the executor is a step that does nothing and reports success. Rather than trusting the
// two lists to stay in step, the claim is verified against the code.
func TestEveryExecutableKeyHasADispatchBranch(t *testing.T) {
	const src = "executor.go"
	file, err := parser.ParseFile(token.NewFileSet(), src, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}

	// Collect the string literals compared in runOne's switch: `s.Has("boot")` and friends.
	handled := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "runOne" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			// TWO SHAPES ARE BRANCHES, AND THEY ARE DIFFERENT QUESTIONS.
			//
			//	s.Has("key")      present AND truthy - right for `boot:` and every ordinary key
			//	hasKey(s, "key")  present at all - right where `false` is an ACTION, which is
			//	                  `record: false` stopping a recording
			//
			// The test has to know both, or adding the second shape reads as a missing branch -
			// which is exactly what it reported for `record` after the fix.
			var lit *ast.BasicLit
			switch fun := call.Fun.(type) {
			case *ast.SelectorExpr:
				if fun.Sel.Name != "Has" || len(call.Args) != 1 {
					return true
				}
				lit, ok = call.Args[0].(*ast.BasicLit)
			case *ast.Ident:
				if fun.Name != "hasKey" || len(call.Args) != 2 {
					return true
				}
				lit, ok = call.Args[1].(*ast.BasicLit)
			default:
				return true
			}
			if !ok || lit.Kind != token.STRING {
				return true
			}
			handled[unquote(lit.Value)] = true
			return true
		})
	}
	if len(handled) == 0 {
		t.Fatalf("no dispatch branches found in %s; the test is looking for the wrong shape", src)
	}

	for _, key := range ExecutableKeys() {
		if !handled[key] {
			t.Errorf("%q is claimed executable but runOne has no branch for it - "+
				"that is a step that reports success and does nothing", key)
		}
	}
	// AND THE OTHER DIRECTION: a branch for a key not claimed would let `run` refuse a file it
	// could actually run.
	claimed := map[string]bool{}
	for _, key := range ExecutableKeys() {
		claimed[key] = true
	}
	for key := range handled {
		if !claimed[key] {
			t.Errorf("runOne handles %q but ExecutableKeys does not list it, so `run` would "+
				"refuse a file this build can run", key)
		}
	}
}

func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

var _ = os.Getenv

// A `run:` THAT ALSO CARRIES `expect_any:` MUST STILL TYPE THE COMMAND.
//
// MEASURED ON manjaro-ssh, step 9 of 23 - and the evidence is the ABSENCE of a log line:
//
//	=== install openssh
//	status: failed step 9/23 "install openssh"
//	error:   install openssh: did not advance: wanted "pacman" or "sudo", read "_ io"
//
// NO `running N line(s), M character(s)` LINE, WHICH `doRun` ALWAYS PRINTS. The step has both keys, and
// the executor's switch tested `expect_any` BEFORE `run`, so it went to `doExpectAny` and THE TYPEWRITER
// NEVER RAN. The step read the screen, found nothing, and reported "did not advance" - precisely what a
// step that did nothing would report.
//
// THE FIX IS NOT AN ORDERING IN THE SWITCH, because a step's keys are not a priority list - they are a
// description of ONE action plus its check. `doRun` performs the check itself, after the typing it is
// about, so no ordering can separate them again.
func TestARunStepStillTypesWhenItAlsoExpects(t *testing.T) {
	body, err := os.ReadFile("text.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	i := strings.Index(src, "func (e *Executor) doRun(")
	if i < 0 {
		t.Fatal("no doRun")
	}
	rest := src[i:]
	if end := strings.Index(rest[1:], "\nfunc "); end > 0 {
		rest = rest[:end+1]
	}

	// THE TYPING IS THE ACTION AND MUST NOT BE CONDITIONAL ON ANYTHING BUT THE BODY.
	if !strings.Contains(rest, "e.Driver.Type(line, gap)") {
		t.Fatal("`doRun` no longer types the command")
	}
	// AND THE CHECK, WHEN THE STEP HAS ONE, RUNS AFTER IT - not instead of it.
	if !strings.Contains(rest, `s.Has("expect")`) || !strings.Contains(rest, "e.doExpectAny(s)") {
		t.Fatal("`doRun` ignores a step's `expect:`/`expect_any:`, so the check would have to be " +
			"dispatched separately - and the dispatcher checks `expect_any` BEFORE `run`, which is " +
			"how the typing was skipped entirely")
	}
	// AND THE CHECK IS AFTER THE LAST KEYSTROKE, not before the first.
	typeAt := strings.Index(rest, "e.Driver.Type(line, gap)")
	expectAt := strings.Index(rest, "e.doExpectAny(s)")
	if expectAt < typeAt {
		t.Fatal("`doRun` checks the screen before typing")
	}
}

// `run` MUST BE MATCHED BEFORE `expect`, OR THE TYPING IS SILENTLY SKIPPED.
//
// A step that types a command and checks the screen carries BOTH keys. `doExpectAny` only READS the
// screen, so if it is reached first the typewriter never runs and the check reports "did not advance" -
// which is what a step that did nothing would report.
//
// MEASURED ON manjaro's `install openssh`, TWICE, ON TWO DIFFERENT IMAGES:
//
//	=== install openssh
//	error:   install openssh: did not advance: wanted "pacman" or "sudo", read "_ io"
//
// AND THE TRACE HAD NO `running N line(s)` LINE, which `doRun` prints unconditionally. The first fix
// was inside `doRun` - perform the check after the typing - and it could not work, because `run` never
// reached `doRun`. THAT IS WHY THE SAME FAILURE CAME BACK.
//
// The check belongs to the action, so the ACTION is dispatched first.
func TestRunIsDispatchedBeforeExpect(t *testing.T) {
	body, err := os.ReadFile("executor.go")
	if err != nil {
		t.Fatal(err)
	}
	src := stripCommentsForTest(string(body))
	runAt := strings.Index(src, `case s.Has("run"):`)
	if runAt < 0 {
		t.Fatal("no `case s.Has(\"run\")` in the dispatcher")
	}
	expectAt := strings.Index(src, `case s.Has("expect_any") || s.Has("expect"):`)
	if expectAt < 0 {
		// THE BUG THIS TEST WAS WRITTEN FOR CANNOT RECUR: `expect:`/`expect_any:` no longer have a
		// dispatch branch at all. They are an ARGUMENT of `run:`, checked by `doRun` after the
		// typing, so there is no order to get wrong. If a branch comes back, the check below still
		// applies - a step that both types and checks must reach `doRun` first.
		return
	}
	if expectAt < runAt {
		t.Fatal("`expect_any` is dispatched BEFORE `run`, so a step that both types and checks never " +
			"types - measured on manjaro's `install openssh`, twice, with no `running N line(s)` line " +
			"in the trace to say so")
	}
}

// `do:` IS DECLARED AND MUST BE DISPATCHED, BECAUSE A DECLARED KEY WITH NO BRANCH IS A STEP THAT DOES
// NOTHING AND REPORTS SUCCESS.
//
// MEASURED ON macos-ssh, STEP 10 OF 33:
//
//	error:   dismiss the overlays macOS puts up on a first login: no implementation for this step's keys
//
// THE VOCABULARY CAUGHT IT, which is what it is for - but only at RUNTIME, on a guest, thirty minutes
// into a stage. `TestEveryExecutableKeyHasADispatchBranch` should have caught it before the push, and
// the reason it did not is that `do` is a LIST key whose value is a set of actions rather than an
// action of its own, so it looked like a container.
//
// IT IS AN ACTION: the step exists to perform those clicks. `doActions` runs each as a step.
func TestDoRunsItsActions(t *testing.T) {
	body, err := os.ReadFile("actions.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	i := strings.Index(src, "func (e *Executor) doActions(")
	if i < 0 {
		t.Fatal("no doActions - a `do:` step has nothing to run it")
	}
	rest := src[i:]
	if end := strings.Index(rest[1:], "\nfunc "); end > 0 {
		rest = rest[:end+1]
	}
	code := stripCommentsForTest(rest)
	// EACH ACTION GOES BACK THROUGH THE DISPATCHER, so `at:`, `key:`, `until:` and the rest all work.
	if !strings.Contains(code, "e.runOne(sub)") {
		t.Fatal("`doActions` does not dispatch its actions, so each one would need its own copy of " +
			"every action's logic")
	}
	// AND THE SUB-STEP CARRIES THE PARENT'S NAME, so a failure says which step it was part of.
	if !strings.Contains(code, "s.Name") {
		t.Fatal("`doActions` loses the parent's name, so a failure would name an anonymous sub-step")
	}

	// AND THE DISPATCHER REACHES IT.
	ex, err := os.ReadFile("executor.go")
	if err != nil {
		t.Fatal(err)
	}
	exSrc := stripCommentsForTest(string(ex))
	if !strings.Contains(exSrc, `case s.Has("do"):`) {
		t.Fatal("the dispatcher has no `do` branch, so the step reports `no implementation for this " +
			"step's keys` - measured on macos-ssh")
	}
}

// THE PANE MODEL WAS DECLARED AND NEVER DISPATCHED, AND THIS IS THE GUARD AGAINST IT COMING BACK.
//
// MEASURED: `macos/15/steps/sequoia.yaml` writes `when:` 16 times and `do:` 8 times. `runOne` had no
// branch for either, so every one of those steps fell to the switch's last arm and returned
// `no implementation for this step's keys` — the macOS Setup Assistant walk could not run at all, and
// nothing said so because nothing ran it.
//
// A KEY THAT LOADS AND THEN FAILS AT RUN TIME IS WORSE THAN A REFUSED KEY: the file is accepted, the
// stage starts, and the failure arrives twenty minutes in on the wrong step. So these two are named
// here explicitly rather than trusting the general test above to notice a third time.
func TestThePaneModelKeysAreDispatched(t *testing.T) {
	for _, key := range []string{"when", "do"} {
		found := false
		for _, k := range ExecutableKeys() {
			if k == key {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("`%s:` is not in ExecutableKeys, so `run` would refuse a file that writes it - "+
				"and before this was added, the executor answered `no implementation for this "+
				"step's keys` for the 24 steps of macOS's setup walk", key)
		}
	}
}

// AND THE KEYS THAT WERE ONLY EVER PART OF ITS STATE MACHINE ARE GONE.
//
// `once:`, `end:` and `stuck_after:` were the rest of the pane model: a pane answered at most once, a
// file's end condition, and a stuck timer. Each was declared, documented, and never implemented.
// Deleting them is the point of "no redundancy" — a vocabulary that keeps a word nobody can honour is
// a vocabulary a reader has to check against the source rather than trust.
//
// `UNTIL_ANY:` WAS IN THIS LIST AND SHOULD NEVER HAVE BEEN. The commit that deleted it said it "had
// no dispatcher branch", and `doUntil` had read it all along:
//
//	for _, set := range wordSetsOf(s, "until_any") {
//		alternatives = append(alternatives, set)
//	}
//
// so `until:` and `until_any:` were the two spellings the same function documents - one word-set,
// or a list of alternatives - and deleting the name made a legal file unloadable while the code that
// implemented it stayed. `macos/26/steps/setup.yaml` writes it once, on the step whose comment says
// "the Setup Assistant is up, OR THIS STAGE FAILS".
//
// IT IS PUT BACK, AND THE TEST THAT DEMANDED IT STAY DELETED IS THE THING THAT WAS WRONG. A test is a
// claim about the code; when the claim and the code disagree, the code is not automatically the one
// that is broken.
func TestTheDeadPaneStateMachineKeysAreGone(t *testing.T) {
	for _, key := range []string{"once", "end", "stuck_after"} {
		if step.Declares(key) {
			t.Errorf("`%s:` is declared again. It was part of the pane state machine, it had no "+
				"dispatcher branch, and no file uses it", key)
		}
	}
	// AND `until_any:` IS THE ONE THAT IS NOT DEAD: `doUntil` reads it beside `until:`.
	if !step.Declares("until_any") {
		t.Error("`until_any:` is not declared, and `doUntil` reads it: a file that spells the " +
			"alternatives form is refused while the executor implements it")
	}
}

// `clear:` MUST ACTUALLY CLEAR, BECAUSE THE VOCABULARY HAS ADVERTISED IT SINCE IT WAS WRITTEN.
//
// MEASURED on AnduinOS' account form: the username box arrives holding a `vm`, the walk says
// `input text "vmuser" at ... clear true`, and the disk was built with `vmvmuser`. Nothing cleared it,
// because `doType` never read the key - it only clicked and typed. Every later `ssh:` then failed with
// `unable to authenticate, attempted methods [none password]` on a machine whose sshd was active, and
// that message names nothing.
//
// THE CHECK IS ON THE CODE because the failure is silent: a step that ignores `clear:` still types, still
// reports the right character count, and still passes. Only the account on the disk is wrong.
func TestATypeStepClearsBeforeTyping(t *testing.T) {
	body, err := os.ReadFile("executor.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	i := strings.Index(src, "func (e *Executor) doType(")
	if i < 0 {
		t.Fatal("no doType")
	}
	rest := src[i:]
	if end := strings.Index(rest[1:], "\nfunc "); end > 0 {
		rest = rest[:end+1]
	}

	clearAt := strings.Index(rest, `s.Bool("clear")`)
	if clearAt < 0 {
		t.Fatal("`doType` does not read `clear:`, so a pre-filled field is appended to - which is how " +
			"an AnduinOS install produced the account `vmvmuser` while the walk asked for `vmuser`")
	}
	// The select-all and the delete are both required: a select with no delete replaces nothing.
	if !strings.Contains(rest, `"a"`) {
		t.Fatal("`clear:` does not select all")
	}
	if !strings.Contains(rest, `"backspace"`) {
		t.Fatal("`clear:` selects but does not delete, so the old text is still there")
	}
	// AND IT RUNS BEFORE THE TYPING, or it clears the value that was just written.
	typeAt := strings.Index(rest, "e.Driver.Type(")
	if typeAt < 0 {
		t.Fatal("`doType` no longer types")
	}
	if clearAt > typeAt {
		t.Fatal("`clear:` runs after the typing")
	}
}
