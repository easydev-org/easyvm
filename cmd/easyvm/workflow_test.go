package main

import (
	"os"
	"strings"
	"testing"
)

// NO `grep -q` IN A WORKFLOW, BECAUSE IT TURNS A SUCCESS INTO EXIT 141.
//
// `grep -q` closes the pipe as soon as it matches. The writer - a program still producing output -
// then takes SIGPIPE and dies. Under `set -euo pipefail`, which every step in these workflows uses,
// that becomes a failed step for a check that SUCCEEDED.
//
// IT COST TWO RUNS. The first was the binary's own CPU assertion (run 43000165288), the second was
// the same assertion against the built image (run 43001461321), which failed AFTER the image had
// been built, pushed and run - every other step in the job was green.
//
// THE FIX IS ALWAYS THE SAME SHAPE: capture the output, then search it.
//
//	versions=$(./dist/easyvm versions)
//	printf '%s\n' "$versions" | grep 'Skylake-Client-v4'
//
// A grep that reads a FILE is fine and is not what this checks for: `grep -q foo /etc/x` cannot
// SIGPIPE a writer, because there is none.
func TestNoGrepQuietInWorkflows(t *testing.T) {
	files, err := os.ReadDir("../../.github/workflows")
	if err != nil {
		t.Skipf("no workflows directory here (%v)", err)
	}
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".yml") && !strings.HasSuffix(f.Name(), ".yaml") {
			continue
		}
		path := "../../.github/workflows/" + f.Name()
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(line)
			// COMMENTS ARE WHERE THIS FILE EXPLAINS THE PROBLEM, so they are not the problem.
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			if !strings.Contains(line, "grep -q") {
				continue
			}
			// A grep with a file argument cannot SIGPIPE; only a pipe can.
			if !strings.Contains(line, "|") {
				continue
			}
			t.Errorf("%s:%d pipes into `grep -q`:\n\t%s\n"+
				"grep -q closes the pipe on its first match, the writer takes SIGPIPE, and "+
				"`set -euo pipefail` reports a failed step for a check that succeeded. Capture "+
				"the output first, then search it.", f.Name(), i+1, trimmed)
		}
	}
}
