package run

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
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
	expectAt := strings.Index(src, `case s.Has("expect_any") || s.Has("expect"):`)
	if runAt < 0 {
		t.Fatal("no `case s.Has(\"run\")` in the dispatcher")
	}
	if expectAt < 0 {
		t.Fatal("no `case s.Has(\"expect_any\")` in the dispatcher")
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
	if !strings.Contains(exSrc, `case hasKey(s, "do"):`) {
		t.Fatal("the dispatcher has no `do` branch, so the step reports `no implementation for this " +
			"step's keys` - measured on macos-ssh")
	}
}
