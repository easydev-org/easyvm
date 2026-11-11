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

// `matrix` DOES NOT EXIST IN A JOB-LEVEL `if:`, AND USING IT MAKES THE WORKFLOW UNDISPATCHABLE.
//
// FIVE RUNS WERE CREATED WITH ZERO JOBS AND A FAILURE CONCLUSION, and nothing in the repository
// could see why. The only place the reason appeared was the dispatch API:
//
//	HTTP 422: Invalid Argument - failed to parse workflow: (Line: 109, Col: 9): Unrecognized
//	named-value: 'matrix'. Located at position 133 within expression
//
// `matrix` is defined inside `jobs.<id>.steps` and nowhere else, so a job-level condition that
// reads it makes the WHOLE FILE unparseable: `workflow_dispatch` is refused and every push that
// touches the file leaves a red run behind.
//
// THE RULE IS NARROWER THAN "ANY JOB-LEVEL KEY", AND THAT IS THE FINDING. `timeout-minutes:
// ${{ matrix.timeout }}` is LEGAL at job level - GitHub resolves `matrix` there - and so is every
// step's `if:` and `run:`. What is NOT legal is the job's own `if:`, which is evaluated BEFORE the
// matrix exists; the dispatch error names that expression precisely.
//
// A first version of this test refused every `matrix.` reference outside the step list and reported
// the legal `timeout-minutes` line - a test that is right about a class of bug and wrong about its
// boundary is worse than a missing one, because it teaches the next reader to ignore it.
func TestMatrixIsNotUsedInJobLevelConditions(t *testing.T) {
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

		// THE REGION THAT MATTERS IS BETWEEN A JOB'S NAME AND ITS `steps:`. Everything after
		// `steps:` is a step, where `matrix` is legal - including a step's own `if:`, which sits at
		// the SAME indent as a job key and is why an indent-only rule reported a false positive.
		inSteps := false
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") || trimmed == "" {
				continue
			}
			if strings.HasPrefix(trimmed, "steps:") {
				inSteps = true
				continue
			}
			// A job key at four spaces ends the step list of the previous job.
			indent := len(line) - len(strings.TrimLeft(line, " "))
			if indent <= 4 && !strings.HasPrefix(trimmed, "-") {
				inSteps = false
			}
			if inSteps || !strings.HasPrefix(trimmed, "if:") {
				continue
			}
			// A JOB-LEVEL `if:`, which may span lines with `if: |`.
			block := trimmed
			for j := i + 1; j < len(lines); j++ {
				next := lines[j]
				if strings.TrimSpace(next) == "" {
					break
				}
				nind := len(next) - len(strings.TrimLeft(next, " "))
				if nind <= indent {
					break
				}
				block += " " + strings.TrimSpace(next)
			}
			if !strings.Contains(block, "matrix.") {
				continue
			}
			t.Errorf("%s:%d uses `matrix` in a JOB-LEVEL `if:`:\n\t%s\n"+
				"`matrix` is resolved only for a job's keys that run after the matrix is built, "+
				"and the job's own `if:` is evaluated before it. GitHub rejects the WHOLE FILE: "+
				"`failed to parse workflow: Unrecognized named-value: 'matrix'`, so the workflow "+
				"cannot be dispatched and every push leaves a red run with zero jobs.",
				f.Name(), i+1, strings.TrimSpace(block))
		}
	}
}

// EACH OPERATING SYSTEM HAS ITS OWN WORKFLOW, AND ITS OWN CONCURRENCY GROUP.
//
// A SINGLE `walkthrough.yml` WITH ALL 21 LEGS UNDER ONE `concurrency:` GROUP MEANT THAT DISPATCHING A
// LINUX FIX CANCELLED A RUNNING WINDOWS INSTALL. That is measured, not hypothetical: forty minutes of
// a Windows install were thrown away so a Linux stage could be re-run.
//
// THE GROUP EXISTS TO STOP TWO RUNS FIGHTING OVER ONE MACHINE, AND A MATRIX LEG IS NOT ONE MACHINE -
// every leg gets its own runner with its own /dev/kvm. So the group belongs to an operating system:
// re-running Linux is a better measurement of Linux and says nothing about Windows.
//
// AND THERE MUST BE ONE `stage.yml` AND NOT TWENTY-SIX COPIES. easydiskbake has 26 workflows with the
// same container stanza in 23 of them - 73 duplicated lines naming the same three devices. The
// mechanism is a callable workflow here; each OS file declares only its own matrix.
func TestEachOperatingSystemHasItsOwnGroupAndOneSharedMechanism(t *testing.T) {
	dir := "../../.github/workflows"
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("no workflows directory here (%v)", err)
	}

	// THE SHARED MECHANISM EXISTS AND IS CALLABLE.
	stage, err := os.ReadFile(dir + "/stage.yml")
	if err != nil {
		t.Fatalf("no .github/workflows/stage.yml: the container stanza, the prepare probe and the "+
			"daemon steps would have to be copied into every OS workflow - which is the 73-line "+
			"duplication this split exists to avoid: %v", err)
	}
	if !strings.Contains(string(stage), "workflow_call:") {
		t.Fatal("stage.yml is not callable; it must be `on: workflow_call:` so the OS workflows can " +
			"share it")
	}
	// AND THE DEVICES ARE NAMED THERE ONCE, WHICH IS THE WHOLE POINT - COUNTED OVER INSTRUCTIONS,
	// because the comment at the top QUOTES the line it is explaining.
	deviceLines := 0
	for _, line := range strings.Split(string(stage), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.Contains(trimmed, "--device /dev/kvm") {
			deviceLines++
		}
	}
	if deviceLines != 1 {
		t.Fatalf("stage.yml names the device options on %d instruction lines, and it must be one: a "+
			"second copy is the duplication this file exists to prevent", deviceLines)
	}

	groups := map[string]string{}
	for _, f := range files {
		name := f.Name()
		if !strings.HasSuffix(name, ".yml") || name == "stage.yml" || name == "image.yml" {
			continue
		}
		body, err := os.ReadFile(dir + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		if !strings.Contains(text, "uses: ./.github/workflows/stage.yml") {
			continue // not a stage wrapper
		}
		// A CONCURRENCY GROUP PER FILE, AND NO TWO THE SAME.
		var group string
		for _, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "group: ") &&
				!strings.HasPrefix(strings.TrimSpace(line), "group: ${{") {
				group = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "group: "))
			}
		}
		if group == "" {
			t.Errorf("%s has no explicit concurrency group; two OS workflows sharing one group means "+
				"dispatching one cancels the other", name)
			continue
		}
		if other, dup := groups[group]; dup {
			t.Errorf("%s and %s share the concurrency group %q; dispatching one would cancel the "+
				"other, which is the failure this split fixed", other, name, group)
		}
		groups[group] = name
	}
	// ONE WORKFLOW PER DISTRIBUTION AND PER OPERATING SYSTEM, WHICH IS THE NAME A READER LOOKS FOR.
	//
	// easydiskbake names its workflows after the distribution - `ubuntu.yml`, `ghostbsd-mate.yml` - and
	// a single `linux.yml` holding twelve of them undoes the property that makes the split worth
	// anything: `concurrency:` is per workflow, so dispatching `ubuntu` must not touch `ghostbsd`.
	if len(groups) < 15 {
		t.Fatalf("only %d stage workflows have their own group (%v); this repository migrates "+
			"windows, macos, twelve Linux distributions and one BSD, each with its own name",
			len(groups), groups)
	}
	// AND NONE OF THEM IS AN UMBRELLA NAME. `linux` and `bsd` are groups of distributions, not
	// distributions: a file called `linux` would hold twelve stages under one concurrency group, which
	// is the arrangement this replaced.
	for _, umbrella := range []string{"linux.yml", "bsd.yml", "walkthrough.yml"} {
		if _, err := os.Stat(dir + "/" + umbrella); err == nil {
			t.Errorf(".github/workflows/%s exists. A workflow is named after what it runs - a "+
				"distribution or an operating system - so that dispatching one cannot cancel another, "+
				"and so that a failed run's name says which guest broke", umbrella)
		}
	}
}
