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

// A STEP THAT IS SILENTLY SKIPPED LOOKS EXACTLY LIKE A STEP THAT PASSED.
//
// `if: inputs.push != false` was true only when the input EXISTS, and on a `push` event it is
// empty - so `” != false` evaluated false, the login and the push were both skipped, and the job
// reported SUCCESS having published nothing. Measured on run 43013438028: build-push-action's own
// resolved inputs said `push: false`, and ghcr.io's `latest` tag still carried an older commit's
// binary. The walkthrough workflow then failed with `easyvm: unknown command "pull"` for a command
// that is in the repository, which is what a stale image looks like from the outside.
//
// THE SHAPE OF THE BUG: `inputs.X != <value>` is a test that degrades to false when the input is
// absent, and absent is the normal case for every event that is not workflow_dispatch. The
// condition that means what it says names the event first.
func TestNoConditionDependsOnAnAbsentDispatchInput(t *testing.T) {
	files, err := os.ReadDir("../../.github/workflows")
	if err != nil {
		t.Skipf("no workflows directory here (%v)", err)
	}
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".yml") && !strings.HasSuffix(f.Name(), ".yaml") {
			continue
		}
		body, err := os.ReadFile("../../.github/workflows/" + f.Name())
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(body), "\n")
		// A CONDITION MAY SPAN LINES (`if: |` with a multi-line expression), so a block is
		// collected before it is judged. Checking line by line reported a failure for a condition
		// whose `github.event_name` was on the line above - the test was right about the rule and
		// wrong about where a condition ends.
		for i := 0; i < len(lines); i++ {
			trimmed := strings.TrimSpace(lines[i])
			if strings.HasPrefix(trimmed, "#") || !strings.HasPrefix(trimmed, "if:") {
				continue
			}
			// Collect this `if:` and any continuation lines that are more indented and are not a
			// new YAML key.
			block := trimmed
			baseIndent := len(lines[i]) - len(strings.TrimLeft(lines[i], " \t"))
			for j := i + 1; j < len(lines); j++ {
				next := lines[j]
				if strings.TrimSpace(next) == "" {
					block += "\n"
					continue
				}
				indent := len(next) - len(strings.TrimLeft(next, " \t"))
				if indent <= baseIndent {
					break
				}
				block += "\n" + strings.TrimSpace(next)
			}
			if !strings.Contains(block, "inputs.") {
				continue
			}
			if !strings.Contains(block, "!=") && !strings.Contains(block, "==") &&
				!strings.Contains(block, "contains(") {
				continue
			}
			// `${{ }}` inside `with:` is the same expression, so both spellings are judged.
			if strings.Contains(block, "github.event_name") {
				continue
			}
			t.Errorf("%s:%d has an `if:` that tests an `inputs.` value without naming the event:\n"+
				"\t%s\n"+
				"A dispatch input is EMPTY on every other event, so `inputs.x != false` is "+
				"false and the step is skipped - while the job still reports success. Write "+
				"`github.event_name != 'workflow_dispatch' || inputs.x`.", f.Name(), i+1, block)
		}
	}
}
