package oci

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// NOTHING IN THIS BUILD MAY NAME ANOTHER REPOSITORY'S PACKAGE.
//
// A `GITHUB_TOKEN` IS SCOPED TO THE REPOSITORY WHOSE WORKFLOW HOLDS IT. The build used to default
// every fetch and every push to `easydev-org/easydiskbake`, and the first install leg that finished a
// real disk measured what that means:
//
//	FAILED: the registry refused a pull,push token (HTTP 403):
//	{"errors":[{"code":"DENIED","message":"requested access to the resource is denied"}]}
//
// 3927 MiB of installed opensuse-tumbleweed - a disk that booted, was configured and shut down -
// could not be published, because the destination was not this token's to write. Reading has the same
// shape, and a private package elsewhere would not be readable at all.
//
// SO BOTH DIRECTIONS ARE `easydev-org/easyvm`, and this test is what keeps a future edit from quietly
// pointing one of them back. A default is the easiest thing in a program to change and the hardest
// thing to notice.
func TestBothDirectionsUseThisRepositorysPackage(t *testing.T) {
	// THE FILES THAT CHOOSE A REPOSITORY. `asset` fetches the installer images and the guest-agent
	// archives; `opencore` fetches the four macOS firmware assets. Those are every place a tag is
	// resolved, so they are every place a default can be wrong.
	files := []string{
		"../asset/asset.go",
		"../opencore/opencore.go",
	}

	// A STRING LITERAL, NOT A COMMENT. The comment that explains this history names the other
	// repository on purpose - it is the measurement, and deleting it would delete the reason. What
	// must never appear again is a literal that the program can assign.
	literal := regexp.MustCompile(`"easydev-org/easydiskbake"`)

	for _, name := range files {
		body, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatalf("cannot read %s: %v", name, err)
		}
		src := string(body)
		if bad := literal.FindString(src); bad != "" {
			t.Fatalf("%s still contains the literal %s. A token minted for this repository cannot "+
				"read or write another repository's package, so every fetch and every push must name "+
				"`easydev-org/easyvm` - which is what artifacts-publish.yml fills", name, bad)
		}
		if !strings.Contains(src, `"easydev-org/easyvm"`) {
			t.Fatalf("%s does not default to `easydev-org/easyvm`, so it reads or writes a package "+
				"this build does not own", name)
		}
	}

	// AND THE ENVIRONMENT OVERRIDE STILL EXISTS, because a fork must be able to point at its own
	// package rather than being told it may not run.
	if !strings.Contains(string(mustRead(t, "../asset/asset.go")), "DISKS_REPOSITORY") {
		t.Fatal("`DISKS_REPOSITORY` is gone, so a fork cannot point the fetches at its own package")
	}
}

// THE WORKFLOW THAT MAKES THOSE TAGS EXIST, AND THE PERMISSION IT NEEDS.
//
// `artifacts-publish.yml` mirrors every pinned upstream input into this repository's package, so the
// build has a source artifact for each tag it fetches. Without it the first fetch of a fresh clone
// pulls nothing and the stage fails on a missing installer image with a message about the network.
//
// AND THE STAGE NEEDS `packages: write`, not `read`: `easyvm push` creates a package version, and a
// read-only token is refused with the same `DENIED` measured above.
func TestTheArtifactWorkflowAndItsPermission(t *testing.T) {
	root := "../.."

	publish := string(mustRead(t, filepath.Join(root, ".github/workflows/artifacts-publish.yml")))
	for _, want := range []string{
		// The permission without which the push is a 403.
		"packages: write",
		// THE TRANSFER IS THE ORGANISATION'S ACTION. The `oras` install and push moved out of this
		// file when the actions were written - `ORAS_VERSION=` and `oras push` used to be asserted
		// here, and asserting them now would be asserting the thing that was removed.
		"easyactions/oci-push",
		"easyactions/runner-check",
		// EVERY TAG THE BUILD FETCHES has to be in the catalogue, so one of the four groups is not
		// silently short an input.
		"iso-windows11",
		"iso-virtio-win",
		"iso-ubuntu-desktop",
		"iso-nixos-graphical",
		"macos-asset-opencore",
		"macos-asset-ovmf-vars-1920x1080",
		// The verification, because a moving upstream is the whole reason a pin exists.
		"sha256",
	} {
		if !strings.Contains(publish, want) {
			t.Fatalf("artifacts-publish.yml does not mention %q, so the package either lacks an "+
				"input or publishes one without verifying it", want)
		}
	}

	stage := string(mustRead(t, filepath.Join(root, ".github/workflows/stage.yml")))
	if !strings.Contains(stage, "packages: write") {
		t.Fatal("stage.yml declares `packages: read`, so `easyvm push` is refused with HTTP 403 " +
			"`DENIED` and an install leg's finished disk is never published")
	}
	if strings.Contains(stage, "packages: read") {
		t.Fatal("stage.yml still says `packages: read` somewhere; a read-only token cannot create " +
			"a package version")
	}
}

// AND NO WORKFLOW MAY DEFAULT TO THE OTHER REPOSITORY EITHER.
//
// The input default is what a manual dispatch uses when the field is left alone, and a default
// pointing elsewhere would make the push fail on exactly the runs a person started by hand.
func TestNoWorkflowDefaultsToAnotherRepository(t *testing.T) {
	files, err := filepath.Glob("../../.github/workflows/*.yml")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Skip("no workflows here")
	}
	for _, name := range files {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("cannot read %s: %v", name, err)
		}
		src := string(body)
		// A `default:` IS AN ASSIGNMENT the program acts on, so it is checked as one. A prose
		// mention stays allowed for the same reason the comments above are.
		if strings.Contains(src, `default: "easydev-org/easydiskbake"`) {
			t.Fatalf("%s defaults `disks_repository` to another repository, so a manual dispatch "+
				"pushes a finished disk to a package this token cannot write",
				filepath.Base(name))
		}
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}
	return body
}

// NO WORKFLOW MAY PASS A FLAG THE DAEMON DOES NOT DEFINE.
//
// MEASURED, ON ALL THREE CI WORKFLOWS AT ONCE:
//
//	flag provided but not defined: -novnc-port
//	Usage of serve: -http string ... -work string ...
//
// `--novnc-port` was deleted with the second HTTP server, and `stage.yml` and `sandbox.yml` still passed
// it - so the daemon refused to start, every install leg failed in 312 bytes of log, and there was NO
// VIDEO because a machine that never started records nothing.
//
// A TEST AND NOT A CONVENTION, because the deletion and the call sites live in different files and the
// Go compiler cannot see a shell string. It asks the SOURCE OF TRUTH - the flags `cmdServe` defines -
// and then looks for any other `-name` in the workflows' `easyvm serve` lines.
func TestEveryDaemonFlagTheWorkflowsPassIsDefined(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "cmd", "easyvm", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	// the flags are declared with fs.String/fs.Int/fs.Duration in cmdServe
	defined := map[string]bool{"work": true, "http": true}
	for _, m := range regexp.MustCompile(`fs\.(?:String|Int|Duration|Bool)\("([a-z-]+)"`).FindAllStringSubmatch(string(src), -1) {
		defined[m[1]] = true
	}

	files, err := filepath.Glob(filepath.Join("..", "..", ".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	served := 0
	for _, path := range files {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(body), "\n") {
			// comments quote the old flags on purpose, and a comment is not an invocation
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") || !strings.Contains(line, "easyvm serve") {
				continue
			}
			served++
			for _, m := range regexp.MustCompile(`(?:^|\s)--?([a-z][a-z-]*)`).FindAllStringSubmatch(line, -1) {
				if !defined[m[1]] {
					t.Errorf("%s:%d passes `--%s` to `easyvm serve`, and cmdServe does not define it. "+
						"The daemon will refuse to start and the leg will fail with a usage message "+
						"rather than a machine", filepath.Base(path), i+1, m[1])
				}
			}
		}
	}
	if served == 0 {
		t.Fatal("no `easyvm serve` invocation was found, so this checks nothing")
	}
	t.Logf("checked %d `easyvm serve` line(s) against %d defined flag(s)", served, len(defined))
}
