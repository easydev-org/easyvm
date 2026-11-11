package run

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
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
