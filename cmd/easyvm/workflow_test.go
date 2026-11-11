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
		path := "../../.github/workflows/" + f.Name()
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			// ANY `inputs.` COMPARISON HAS TO SAY WHICH EVENT IT IS TALKING ABOUT. Without that,
			// the comparison is against an empty string and the step is skipped on every push.
			if !strings.Contains(trimmed, "inputs.") {
				continue
			}
			if !strings.Contains(trimmed, "!=") && !strings.Contains(trimmed, "==") {
				continue
			}
			// `${{ }}` inside `with:` is the same expression, so both spellings are checked.
			if strings.Contains(trimmed, "github.event_name") {
				continue
			}
			t.Errorf("%s:%d compares an `inputs.` value without naming the event:\n\t%s\n"+
				"A dispatch input is EMPTY on every other event, so `inputs.x != false` is "+
				"false and the step is skipped - while the job still reports success. Write "+
				"`github.event_name != 'workflow_dispatch' || inputs.x`.", f.Name(), i+1, trimmed)
		}
	}
}

// A `container:` JOB RUNS STEPS WITH /bin/sh, SO BASH-ONLY SHELL FEATURES DIE THERE.
//
// run 43018660638 died on the third line of its polling loop:
//
//	/__w/_temp/c1bb5a9d-7830-40ee-8058-6be476d5f9b4.sh: 3: SECONDS: parameter not set
//
// `SECONDS` is a bash variable that dash does not define. The step had already printed nothing,
// while the daemon it was watching was up and starting a guest - so the failure looked like a
// broken walkthrough and was a broken loop.
//
// `set -o pipefail` IS NOT POSIX EITHER. Dash accepts `set -euo pipefail` as `set -e -u -o pipefail`
// and then ignores it, which is worse than failing: the step looks protected and is not.
func TestWorkflowShellIsPosix(t *testing.T) {
	files, err := os.ReadDir("../../.github/workflows")
	if err != nil {
		t.Skipf("no workflows directory here (%v)", err)
	}
	// Names that only bash provides, checked because the failure is a runtime error in the middle
	// of a step rather than a parse error at the top.
	bashOnly := []string{"$SECONDS", "${SECONDS}", "pipefail"}
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".yml") && !strings.HasSuffix(f.Name(), ".yaml") {
			continue
		}
		body, err := os.ReadFile("../../.github/workflows/" + f.Name())
		if err != nil {
			t.Fatal(err)
		}
		// ONLY A JOB THAT DECLARES `container:` RUNS ITS STEPS UNDER /bin/sh. A job on the runner
		// itself gets bash, where `pipefail` and `SECONDS` are both fine - and image.yml is such a
		// job, so refusing them there would be a rule about the wrong thing.
		if !strings.Contains(string(body), "container:") {
			continue
		}
		for i, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(line)
			// COMMENTS ARE WHERE THESE FILES EXPLAIN THE BUG, so they are exempt by definition.
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			for _, bad := range bashOnly {
				if !strings.Contains(trimmed, bad) {
					continue
				}
				t.Errorf("%s:%d uses %q, which /bin/sh in a `container:` job does not have:\n\t%s\n"+
					"Count iterations, or count with `date`; do not count with a bash variable.",
					f.Name(), i+1, bad, trimmed)
			}
		}
	}
}
