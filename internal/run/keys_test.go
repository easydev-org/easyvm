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
