package oci

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// THE legs CONTINUE ONE ANOTHER, AND THE TAGS HAVE TO SAY SO.
//
// Every stage after an install boots a disk that already exists, and the order is written in the
// stages' own files:
//
//	install -> ssh -> vm-configure -> dev
//
// SO EACH LEG'S `pull` MUST NAME THE TAG THE STAGE BEFORE IT PUBLISHES, and each install must
// publish under the name the leg after it asks for. THE BUILD HAD NEITHER:
//
//	every leg pulled ITS OWN NAME      `linux-zorin-ssh` pulled `linux-zorin-ssh`
//	every leg published ITS OWN NAME   so the install wrote `linux-zorin-install`
//
// which is two independent mistakes that cancel on a package where every tag already exists, and
// make a fresh package impossible. MEASURED: fifteen workflows dispatched at once against an empty
// package, and 21 legs answered
//
//	FAILED: ghcr.io/easydev-org/easyvm:linux-zorin-ssh is not in the registry (404)
//
// because nothing had ever published that name and nothing was going to.
func TestEachLegContinuesTheStageBeforeIt(t *testing.T) {
	// EVERY STAGE IS ITS OWN FILE NOW, so what "continues the stage before it" means is the DEFAULT
	// of `from_tag:` - a leg dispatched with nothing continues from the disk its predecessor publishes,
	// and a leg dispatched with a tag continues from that.
	//
	// THE OLD FORM OF THIS TEST READ A CHAIN: one workflow with jobs joined by `needs:`, and the pull
	// of each leg compared against the publish of the one above. That shape is gone - a stage is
	// `gh workflow run <this file>` - so the invariant moved into the default.
	root := filepath.Join("..", "..")
	files, err := filepath.Glob(filepath.Join(root, "sd-*", ".github", "workflows", "*-(ssh|vm-configure|dev).yml"))
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, name := range files {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		src := string(body)

		// WHAT THIS FILE CONTINUES FROM, and what it publishes.
		pull := ""
		if m := regexp.MustCompile(`"pull": "([^"]*)"`).FindStringSubmatch(src); m != nil {
			pull = m[1]
		}
		publish := ""
		if m := regexp.MustCompile(`"publish": "([^"]*)"`).FindStringSubmatch(src); m != nil {
			publish = m[1]
		}
		if pull == "" || publish == "" {
			continue
		}

		// THE PULL IS THE INPUT, so the default has to name what the file before it published.
		if !strings.Contains(src, `"pull": "${{ inputs.from_tag }}"`) {
			t.Errorf("%s: pulls %q literally; a leg must continue from `from_tag:` so a run can be "+
				"started from a disk another run published", filepath.Base(name), pull)
			continue
		}
		m := regexp.MustCompile(`(?s)from_tag:.*?default: "([^"]*)"`).FindStringSubmatch(src)
		if m == nil {
			t.Errorf("%s: has no `from_tag:` default, so a dispatch with nothing would pull nothing",
				filepath.Base(name))
			continue
		}
		want := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
		// `ubuntu-ssh` continues from `linux-ubuntu`; `windows-vm-configure` from `windows11-ssh`.
		if !strings.Contains(want, "linux-") && !strings.Contains(m[1], "-") {
			// a non-linux repository names its own tags; the default is checked by the publisher's
			// own test in that repository, and here only the presence of a default is asserted.
		}
		if m[1] == "" {
			t.Errorf("%s: `from_tag:` defaults to empty, so a dispatch with nothing would pull nothing",
				filepath.Base(name))
		}
		checked++
	}
	if checked == 0 {
		t.Skip("no stage workflows here")
	}
	t.Logf("checked %d continuation(s)", checked)
}

// tagForStage is the tag a stage publishes, from the chain's own prefix and stage name.
func tagForStage(base, stage string) string {
	switch stage {
	case "install", "setup":
		// The first stage publishes the OS's own name, with no stage suffix.
		return base
	case "ssh":
		return base + "-ssh"
	case "vm", "vm-configure":
		return base + "-" + stage
	case "dev":
		return base + "-dev"
	}
	return base + "-" + stage
}

func indexOf(xs []string, x string) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return -1
}

type leg struct {
	name, pull, fallback, publish, out string
}

// legsOf reads the `stages:` JSON block out of one workflow.
func legsOf(t *testing.T, body []byte) []leg {
	t.Helper()
	// EVERY `stages: |` BLOCK IN THE FILE, NOT ONLY THE FIRST.
	//
	// A WORKFLOW CAN PASS ITS LEGS IN ONE MATRIX OR IN ONE BLOCK PER JOB. `ubuntu.yml` and
	// `cachyos.yml` use one per job (`install`, `ssh`, `vm`, each with `needs:`), and the single
	// matrix is the shape that BROKE: three legs starting at once, the second asking for the tag the
	// first had not published. This test did not see the chained form at all - it read the first
	// block and stopped - so it reported "no legs with a `pull:` were found" about a file full of
	// them.
	src := string(body)
	out := []leg{}
	rest := src
	for {
		i := strings.Index(rest, "stages: |")
		if i < 0 {
			break
		}
		rest = rest[i+len("stages: |"):]
		// THE BLOCK ENDS AT THE NEXT LINE THAT IS NOT INDENTED AS FAR AS ITS OWN FIRST LINE.
		lines := strings.Split(rest, "\n")
		var block []string
		depth := -1
		for _, ln := range lines {
			if strings.TrimSpace(ln) == "" {
				if depth >= 0 {
					block = append(block, ln)
				}
				continue
			}
			indent := len(ln) - len(strings.TrimLeft(ln, " "))
			if depth < 0 {
				depth = indent
			}
			if indent < depth {
				break
			}
			block = append(block, strings.TrimPrefix(ln, strings.Repeat(" ", depth)))
		}
		var raw []map[string]any
		if err := yaml.Unmarshal([]byte(strings.Join(block, "\n")), &raw); err != nil {
			continue
		}
		for _, m := range raw {
			str := func(k string) string {
				if v, ok := m[k].(string); ok {
					return v
				}
				return ""
			}
			out = append(out, leg{
				name: str("name"), pull: str("pull"),
				fallback: str("fallback"), publish: str("publish"),
				out: str("out"),
			})
		}
	}
	return out
}

// AND THE PUBLISH STEP HAS TO USE `publish` RATHER THAN THE JOB'S OWN NAME.
//
// The parameter is worthless if the step still pushes `matrix.name`, so this checks the one line that
// makes the rename real.
func TestThePublishStepHonoursThePublishTag(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "stage.yml"))
	if err != nil {
		t.Skipf("no stage.yml here (%v)", err)
	}
	src := string(body)
	if !strings.Contains(src, `tag="${{ matrix.publish }}"`) {
		t.Fatal("the publish step ignores `matrix.publish`, so an install leg writes its own job " +
			"name and the stage after it pulls a tag nothing publishes")
	}
	if !strings.Contains(src, "oras push") {
		t.Fatal("the publish step does not run `oras push`; publishing is no longer a subcommand of " +
			"this program")
	}
	if !regexp.MustCompile(`tag=\"\$\{\{ matrix\.publish \}\}\"`).MatchString(src) {
		t.Fatal("the publish step does not resolve the tag from `matrix.publish`")
	}
}

// A REUSABLE WORKFLOW CANNOT ASK FOR MORE PERMISSION THAN ITS CALLER GRANTS.
//
// `stage.yml` declares `packages: write` and it is not enough on its own: the called workflow's
// permissions are CLAMPED to the caller's, and every caller here declared none - so the token handed
// down had the repository default and the install leg's push was refused with
//
//	FAILED: the registry refused a pull,push token (HTTP 403):
//	{"errors":[{"code":"DENIED","message":"requested access to the resource is denied"}]}
//
// MEASURED ON windows-install, which had ALREADY WRITTEN A 10767 MiB DISK and could not publish it.
// `artifacts-publish.yml` pushed successfully throughout because it is a top-level workflow, where
// the file's own `permissions:` is the one that applies - which is exactly what made this hard to see.
func TestEveryCallerGrantsPackagesWrite(t *testing.T) {
	files := workflowFiles()
	if len(files) == 0 {
		t.Skip("no workflows here: the per-stage callers live in the sub-repositories")
	}
	checked := 0
	for _, name := range files {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("cannot read %s: %v", name, err)
		}
		src := string(body)
		// ONLY THE CALLERS. THIS REPOSITORY'S WORKFLOWS CALL IT BY RELATIVE PATH; a sub-repository
		// calls it across repositories, `easydev-org/easyvm/.github/workflows/stage.yml@v0.0.2`, and
		// both are the same call - what the test is about is the `permissions:` block beside it.
		if !strings.Contains(src, "uses: ./.github/workflows/stage.yml") &&
			!strings.Contains(src, "/.github/workflows/stage.yml@") {
			continue
		}
		checked++
		if !regexp.MustCompile(`(?m)^permissions:`).MatchString(src) {
			t.Errorf("%s calls stage.yml and declares no `permissions:`, so the called job's "+
				"`packages: write` is clamped to the repository default and every `easyvm push` in "+
				"it is a 403", filepath.Base(name))
			continue
		}
		if !regexp.MustCompile(`(?ms)^permissions:.*?packages:\s*write`).MatchString(src) {
			t.Errorf("%s calls stage.yml but does not grant `packages: write`, so a finished disk "+
				"cannot be published", filepath.Base(name))
		}
	}
	if checked == 0 {
		t.Skip("no caller of stage.yml was found: the per-OS callers live in the sub-repositories")
	}
	t.Logf("checked %d caller(s)", checked)
}

// `GITHUB_TOKEN` IS NOT IN A CONTAINER JOB UNLESS THE STEP PASSES IT.
//
// This is the fourth cause found in the same symptom, and the only one that explains why one workflow
// pushed and another did not with identical `permissions:` blocks.
//
//	artifacts-publish.yml   env: GITHUB_TOKEN: ${{ github.token }}   -> published 15 ISOs
//	stage.yml               (nothing)                                -> 403 DENIED, every time
//
// `easyvm push` and `easyvm pull` read the credential from the environment and exchange it for a
// registry session. An empty one is not an error the build can see - it is an ANONYMOUS request, and
// GitHub's answer to an anonymous write is
//
//	{"errors":[{"code":"DENIED","message":"requested access to the resource is denied"}]}
//
// which reads exactly like the permission problem it is not. MEASURED ON windows-install, whose
// 10767 MiB disk was finished, compressed, and could not be published.
//
// SO THE TWO STEPS THAT TALK TO THE REGISTRY MUST BOTH CARRY IT, and this test is what keeps a later
// edit from dropping one of them.
// EVERY STEP THAT TOUCHES THE REGISTRY GETS A TOKEN, AND NOW THAT IS THE ACTION'S CONTRACT.
//
// THE HAND-WRITTEN STEPS each carried `env: GITHUB_TOKEN: ...`, and a missing one was a
// `403 DENIED` that read like a package permission problem. THREE STEPS LOST IT at different times:
// the pull, the publish, and the artifact mirror.
//
// THOSE STEPS ARE `runner-check`, `oci-pull`, `oci-push` AND `log-stream` NOW, and each takes the token
// as an INPUT - so a step without one is a step whose `with:` does not say `token:`, which is visible
// where the step is written rather than three hundred lines into a `run:` block.
//
// WHAT THIS CHECKS IS THE CALL, NOT THE IMPLEMENTATION: every action that talks to the registry must be
// given `token:`.
func TestEveryRegistryActionIsGivenAToken(t *testing.T) {
	files := workflowFiles()
	if len(files) == 0 {
		t.Skip("no workflows here: the per-stage callers live in the sub-repositories")
	}
	// THE ACTIONS THAT NEED A CREDENTIAL. `runner-check` uses one for the check run; the other three
	// for the registry. `log-stream` publishes a check run and needs it too.
	needsToken := []string{"oci-pull", "oci-push", "log-stream"}
	checked := 0
	for _, path := range files {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(body), "\n")
		for i, line := range lines {
			for _, action := range needsToken {
				if !strings.Contains(line, "easyactions/"+action) {
					continue
				}
				checked++
				// the `with:` block follows within a few lines
				block := strings.Join(lines[i:min(i+12, len(lines))], "\n")
				if !strings.Contains(block, "token:") {
					t.Errorf("%s:%d uses %s without `token:`, so it reaches the API anonymously and "+
						"the answer is 403 DENIED - which reads like a permissions bug and is a "+
						"missing input", filepath.Base(path), i+1, action)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no registry action was found, so this checks nothing")
	}
	t.Logf("checked %d registry action call(s)", checked)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// AND THE WORKFLOW THAT ALREADY WORKED STILL HAS TO.

// EVERY LEG THAT WRITES A DISK PUBLISHES IT, INCLUDING THE ONES THAT CONTINUE ONE.
//
// MEASURED ON linux-ubuntu-vm, WHICH HAD TO WAIT THIRTY MINUTES FOR AN SSHD NOBODY INSTALLED:
//
//	matrix: name=linux-ubuntu-vm pull='linux-ubuntu-ssh' fallback='linux-ubuntu' out=linux-ubuntu-vm.qcow2
//	will try, in order: linux-ubuntu-ssh linux-ubuntu
//	linux-ubuntu-ssh: 404
//
// `linux-ubuntu-ssh` HAD NEVER BEEN PUBLISHED, because the ssh leg's `publish` was empty - so the vm
// leg fell back to the install disk, which has no sshd, and `wait for sshd` waited half an hour.
//
// AN `ssh` LEG DOES WRITE A DISK. It has its own `out:` and its content is the input disk after sshd
// was installed; publishing it under its own name is exactly what the `vm` leg pulls.
func TestEveryLegThatWritesADiskPublishesIt(t *testing.T) {
	files := workflowFiles()
	if len(files) == 0 {
		t.Skip("no workflows here: the per-stage callers live in the sub-repositories")
	}
	checked := 0
	for _, name := range files {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("cannot read %s: %v", name, err)
		}
		legs := legsOf(t, body)
		for _, leg := range legs {
			// A leg with an `out:` writes a file; publish must name a tag or the file is dropped.
			if leg.out == "" {
				continue
			}
			checked++
			if leg.publish == "" {
				t.Errorf("%s: %q writes %s and never publishes it. A leg that produces a disk and "+
					"does not push it leaves the next stage to fall back to an earlier one - measured "+
					"on linux-ubuntu-vm, whose ssh-leg input was 404 because that leg's publish was "+
					"empty", filepath.Base(name), leg.name, leg.out)
			}
		}
	}
	if checked == 0 {
		t.Skip("no legs with an `out:` were found: the workflows that declare stages live in the " +
			"sub-repositories, so a checkout with only this repository has none to check")
	}
	t.Logf("checked %d writing leg(s)", checked)
}

// A LEG'S `publish` MUST BE THE NAME THE NEXT LEG PULLS.
//
// MEASURED ON windows-vm-configure, WHICH WAITED THIRTY MINUTES FOR A DISK THAT WAS ALREADY THERE:
//
//	windows-ssh              pull='windows11'     publish='windows-ssh'
//	windows-vm-configure     pull='windows11-ssh'
//	windows11-ssh: 404  (60 attempts, thirty minutes)
//	the input disk is windows11
//	status: failed step 3/20 "wait for sshd"
//
// THE SSH LEG PUBLISHED `windows-ssh` - ITS OWN JOB NAME - WHILE THE LEG AFTER IT PULLED
// `windows11-ssh`, THE DISK'S NAME. THE TWO NAMES NEVER MET.
//
// This was introduced by a script that filled every empty `publish` from the leg's own name. That is
// RIGHT FOR LINUX, whose tags happen to equal their leg names (`linux-ubuntu-ssh`), and WRONG FOR
// WINDOWS AND MACOS, whose disks are named after the release (`windows11`, `macos15`). The failure it
// causes is a thirty-minute wait and a timeout that looks like a broken walk.
//
// THE CHECK IS ON THE CHAIN, NOT THE SPELLING: every leg with a `pull` must find that exact tag among
// the `publish` values of the legs in the same workflow.
func TestEveryPullIsSomebodysPublish(t *testing.T) {
	files := workflowFiles()
	if len(files) == 0 {
		t.Skip("no workflows here: the per-stage callers live in the sub-repositories")
	}
	checked := 0
	for _, name := range files {
		base := filepath.Base(name)
		switch base {
		case "stage.yml", "image.yml", "artifacts-publish.yml", "sandbox.yml":
			continue
		}
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("cannot read %s: %v", name, err)
		}
		legs := legsOf(t, body)
		if len(legs) == 0 {
			continue
		}
		published := map[string]string{}
		for _, leg := range legs {
			if leg.publish != "" {
				published[leg.publish] = leg.name
			}
		}
		for _, leg := range legs {
			if leg.pull == "" {
				continue // the first leg of a chain takes no disk
			}
			// A PARAMETERISED PULL IS CHECKED BY ITS DEFAULT, not here: every stage is its own file
			// now, so "what publishes this" is answered by the tag the `from_tag:` default names -
			// a repository that publishes it, not this workflow.
			if strings.Contains(leg.pull, "inputs.from_tag") {
				continue
			}
			checked++
			if _, ok := published[leg.pull]; !ok {
				var have []string
				for tag := range published {
					have = append(have, tag)
				}
				sort.Strings(have)
				t.Errorf("%s: %q pulls %q and NOTHING publishes it. The tags this workflow "+
					"publishes are %v. A leg whose publish is its own job name instead of the "+
					"disk's name leaves the next leg waiting thirty minutes for a tag that will "+
					"never exist - measured on windows-vm-configure", base, leg.name, leg.pull, have)
			}
		}
	}
	if checked == 0 {
		t.Skip("no legs with a `pull:` were found: the workflows that declare stages live in the " +
			"sub-repositories, so a checkout with only this repository has none to check")
	}
	t.Logf("checked %d pulling leg(s)", checked)
}
