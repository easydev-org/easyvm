package oci

import (
	"os"
	"path/filepath"
	"regexp"
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
	root := filepath.Join("..", "..", ".github", "workflows")
	files, err := filepath.Glob(filepath.Join(root, "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	// THE CHAINS, as stage names in order, keyed by the OS's own tag prefix.
	chains := map[string][]string{
		"linux-cachyos":             {"install", "ssh", "vm"},
		"linux-endeavour":           {"install", "ssh", "vm"},
		"linux-fedora-workstation":  {"install", "ssh", "vm"},
		"linux-kubuntu":             {"install", "ssh", "vm"},
		"linux-manjaro":             {"install", "ssh", "vm"},
		"linux-mint":                {"install", "ssh", "vm"},
		"linux-mx":                  {"install", "ssh", "vm"},
		"linux-nixos":               {"install", "ssh", "vm"},
		"linux-opensuse-tumbleweed": {"install", "ssh", "vm"},
		"linux-popos":               {"install", "ssh", "vm"},
		"linux-ubuntu":              {"install", "ssh", "vm"},
		"linux-zorin":               {"install", "ssh", "vm"},
		"bsd-ghostbsd-mate":         {"install", "ssh", "vm"},
		"macos15":                   {"setup", "ssh", "vm-configure", "dev"},
		"windows11":                 {"install", "ssh", "vm-configure", "dev"},
	}

	checked := 0
	for _, name := range files {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("cannot read %s: %v", name, err)
		}
		legs := legsOf(t, body)
		if len(legs) == 0 {
			continue
		}
		// WHICH CHAIN THIS FILE IS, by matching the leg names that carry a prefix.
		var base string
		var stages []string
		for candidate, chain := range chains {
			for _, leg := range legs {
				if leg.name == candidate || strings.HasPrefix(leg.name, candidate+"-") {
					base, stages = candidate, chain
					break
				}
			}
			if base != "" {
				break
			}
		}
		if base == "" {
			continue
		}
		checked++

		for _, leg := range legs {
			// THE LAST SEGMENT OF THE LEG NAME IS THE STAGE (`linux-zorin-vm` -> `vm`).
			stage := leg.name
			if i := strings.LastIndex(leg.name, "-"); i >= 0 {
				stage = leg.name[i+1:]
			}
			// `macos-setup` and `windows-install` are the first stage under a different word.
			if strings.HasSuffix(leg.name, "-setup") {
				stage = "setup"
			}
			idx := indexOf(stages, stage)
			if idx < 0 {
				continue
			}
			if idx == 0 {
				// THE FIRST STAGE CREATES THE DISK IT WRITES, so it pulls nothing and publishes the
				// name the second stage asks for.
				if leg.pull != "" {
					t.Errorf("%s: %q is the first stage and pulls %q; an install has no input - its "+
						"first step creates the disk it writes to", filepath.Base(name), leg.name, leg.pull)
				}
				if leg.publish != base {
					t.Errorf("%s: %q must publish %q - that is the tag the stage after it pulls - "+
						"and publishes %q instead", filepath.Base(name), leg.name, base, leg.publish)
				}
				continue
			}
			// EVERY LATER STAGE PULLS WHAT THE STAGE BEFORE IT PUBLISHED.
			want := tagForStage(base, stages[idx-1])
			if leg.pull != want {
				t.Errorf("%s: %q pulls %q, and the stage before it published %q",
					filepath.Base(name), leg.name, leg.pull, want)
			}
			// AND A FALLBACK, so a fresh package can still start: the legs run at the same time, so
			// the stage before may not have published yet.
			if idx >= 2 && !strings.HasPrefix(leg.fallback, tagForStage(base, stages[0])) {
				t.Errorf("%s: %q has no fallback to %q, so on a package where the previous stage "+
					"has not published yet this leg fails with a 404 instead of continuing the "+
					"installed disk", filepath.Base(name), leg.name, tagForStage(base, stages[0]))
			}
		}
	}
	if checked == 0 {
		t.Fatal("no workflow's legs were checked, so this test would pass over anything")
	}
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
	name, pull, fallback, publish string
}

// legsOf reads the `stages:` JSON block out of one workflow.
func legsOf(t *testing.T, body []byte) []leg {
	t.Helper()
	src := string(body)
	const key = "stages: |"
	i := strings.Index(src, key)
	if i < 0 {
		return nil
	}
	rest := src[i+len(key):]
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
		return nil
	}
	out := make([]leg, 0, len(raw))
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
		})
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
	if !regexp.MustCompile(`easyvm push --tag "\$tag"`).MatchString(src) {
		t.Fatal("the publish step does not push with the resolved `$tag`")
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
	root := filepath.Join("..", "..", ".github", "workflows")
	files, err := filepath.Glob(filepath.Join(root, "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, name := range files {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("cannot read %s: %v", name, err)
		}
		src := string(body)
		// ONLY THE CALLERS: a file that invokes `./.github/workflows/stage.yml`.
		if !strings.Contains(src, "uses: ./.github/workflows/stage.yml") {
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
		t.Fatal("no caller of stage.yml was found, so this test would pass over anything")
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
func TestTheStageStepsThatUseTheRegistryPassTheToken(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "stage.yml"))
	if err != nil {
		t.Skipf("no stage.yml here (%v)", err)
	}
	var doc struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string            `yaml:"name"`
				Env  map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &doc); err != nil {
		t.Fatalf("stage.yml does not parse: %v", err)
	}
	wanted := map[string]bool{"pull the input disk": false, "publish the disk": false}
	for _, job := range doc.Jobs {
		for _, st := range job.Steps {
			if _, ok := wanted[st.Name]; !ok {
				continue
			}
			wanted[st.Name] = true
			tok, ok := st.Env["GITHUB_TOKEN"]
			if !ok || !strings.Contains(tok, "github.token") {
				t.Errorf("stage.yml: %q has no `GITHUB_TOKEN` in its `env`, so `easyvm` asks the "+
					"registry anonymously and the answer to a write is 403 `DENIED` - which looks "+
					"like a permissions bug and is a missing variable", st.Name)
			}
		}
	}
	for name, found := range wanted {
		if !found {
			t.Errorf("stage.yml has no step named %q, so this test is checking nothing", name)
		}
	}
}

// AND THE WORKFLOW THAT ALREADY WORKED STILL HAS TO.
func TestThePublisherPassesTheTokenToo(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "artifacts-publish.yml"))
	if err != nil {
		t.Skipf("no artifacts-publish.yml here (%v)", err)
	}
	src := string(body)
	if strings.Count(src, "GITHUB_TOKEN:") < 2 {
		t.Fatal("artifacts-publish.yml no longer passes `GITHUB_TOKEN` to both its steps; it was " +
			"the workflow that proved the variable is what the registry wants")
	}
}
