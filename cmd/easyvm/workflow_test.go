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

// A DUPLICATE KEY IS A WORKFLOW GITHUB REFUSES TO DISPATCH, AND NOTHING LOCAL NOTICED.
//
// Measured, on the first attempt to dispatch the new matrix:
//
//	could not create workflow dispatch event: HTTP 422: Invalid Argument - failed to parse
//	workflow: (Line: 178, Col: 5): 'timeout-minutes' is already defined
//
// The file was valid YAML - a YAML parser keeps the LAST value for a repeated key without a word -
// so `python -c 'yaml.safe_load(...)'` printed nothing and every Go test passed. The workflow simply
// could not be triggered, which is the one failure a test suite should never be able to miss.
//
// THE CHECK IS A LINE SCAN RATHER THAN A PARSER, because it has to see both keys and Go has no
// YAML dependency here. Indentation defines the block: two keys at the same indent inside one
// mapping are the duplicate GitHub rejects.
func TestNoDuplicateKeysInWorkflows(t *testing.T) {
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
		// stack of (indent, keys seen at that indent) for the CURRENT mapping.
		type level struct {
			indent int
			keys   map[string]int // key -> first line
		}
		var stack []level
		for i, raw := range strings.Split(string(body), "\n") {
			line := strings.TrimRight(raw, " \t\r")
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") || trimmed == "---" {
				continue
			}
			if strings.HasPrefix(trimmed, "- ") {
				// A SEQUENCE ITEM STARTS A NEW MAPPING at its own indent.
				stack = nil
				indent := len(line) - len(strings.TrimLeft(line, " "))
				rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
				if k, ok := keyOf(rest); ok {
					stack = append(stack, level{indent: indent + 2,
						keys: map[string]int{k: i + 1}})
				}
				continue
			}
			indent := len(line) - len(strings.TrimLeft(line, " "))
			k, ok := keyOf(trimmed)
			if !ok {
				continue
			}
			// POP deeper levels, then find or create the level at this indent.
			for len(stack) > 0 && stack[len(stack)-1].indent > indent {
				stack = stack[:len(stack)-1]
			}
			if len(stack) == 0 || stack[len(stack)-1].indent < indent {
				stack = append(stack, level{indent: indent, keys: map[string]int{}})
			}
			top := &stack[len(stack)-1]
			if first, seen := top.keys[k]; seen {
				t.Errorf("%s:%d repeats the key %q, first defined at line %d. GitHub REFUSES TO "+
					"PARSE such a workflow - `Invalid Argument ... is already defined` - so it "+
					"cannot be dispatched at all, and YAML parsers keep the last value silently.",
					f.Name(), i+1, k, first)
			}
			top.keys[k] = i + 1
		}
	}
}

// keyOf returns the mapping key on a line, if it is one.
func keyOf(line string) (string, bool) {
	// A key is `name:` with nothing before the colon but the name itself - no space, no quote.
	idx := strings.Index(line, ":")
	if idx <= 0 {
		return "", false
	}
	k := line[:idx]
	if strings.ContainsAny(k, " \t\"'{[(") {
		return "", false
	}
	return k, true
}
