package walk

// WHERE THE STAGE FILES LIVE IS NOT THIS REPOSITORY ANY MORE.
//
// EVERY WALKTHROUGH BELONGS TO THE REPOSITORY THAT RUNS IT - `easyvm-linux`, `easyvm-windows`,
// `easyvm-macos`, `easyvm-bsd`, `easyvm-android` - because a stage is data about a machine, and the
// machine's own repository is what CI checks out when it runs that stage. The engine used to carry a
// COPY of all 62 of them so `internal/walk` could assert things about their shape, and that copy was
// the thing that drifted: a fix landed in the engine's copy and the sub-repository still had the bug,
// or the other way round, and the only way to tell was to diff them by hand.
//
// SO THE TESTS READ THE SUB-REPOSITORIES. There is one lookup and every test uses it, so a checkout
// that puts them somewhere other than beside this one says so once:
//
//	EASYVM_STAGES=/src/easyvm-linux:/src/easyvm-windows:... go test ./internal/walk
//
// A CHECKOUT WITHOUT THEM SKIPS, AND SAYS WHY. Skipping is the honest answer for a machine that has
// only this repository - what these tests assert is a property of the STAGES, and there are none to
// look at. It is not a silent pass: the message names what was missing and the variable that would
// have pointed at it.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoForFamily is the repository name a family's stages live in.
var repoForFamily = map[string]string{
	"linux":   "easyvm-linux",
	"windows": "easyvm-windows",
	"macos":   "easyvm-macos",
	"bsd":     "easyvm-bsd",
	"android": "easyvm-android",
	"microvm": "easyvm-microvm",
}

// familyOrder is the order tests walk the families in, so output is stable.
var familyOrder = []string{"linux", "windows", "macos", "bsd", "android", "microvm"}

// stageSearchBases returns the directories to look for a staged repository IN.
//
// THE ORDER IS THE EXPLICIT VARIABLE, THEN THE SIBLING CHECKOUTS. A sibling is how these are laid out
// on the machine that develops them and how a CI job that checks several repositories out at once
// would put them; EASYVM_STAGES is how anything else does.
func stageSearchBases() []string {
	if env := os.Getenv("EASYVM_STAGES"); env != "" {
		var out []string
		for _, one := range strings.Split(env, string(os.PathListSeparator)) {
			if one != "" {
				out = append(out, one)
			}
		}
		return out
	}
	// THE TEST RUNS IN `internal/walk`, so the repository root is two levels up and its SIBLINGS are
	// three. All three are tried because a checkout can be a plain repository, a worktree under some
	// temporary root, or a directory whose parent holds the sub-repositories - and the cost of trying
	// an extra path is one `stat`.
	return []string{"../..", "..", "../../.."}
}

// stageFamilyRoot returns the directory that holds a family's stages, and whether one was found.
//
// TWO NAMES ARE ACCEPTED FOR THE SAME DIRECTORY. The repositories are `easyvm-linux` and so on, and a
// developer's checkout of them is often `sd-linux`; both are the same tree, and refusing one of them
// would make the tests fail for a reason that has nothing to do with a stage.
func stageFamilyRoot(family string) (string, bool) {
	names := []string{repoForFamily[family]}
	if names[0] == "" {
		names[0] = "easyvm-" + family
	}
	names = append(names, "sd-"+family, family)

	for _, base := range stageSearchBases() {
		for _, name := range names {
			dir := filepath.Join(base, name)
			if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
				// A FAMILY DIRECTORY MUST HOLD STAGES, not just exist. `linux` is also the name of an
				// unrelated checkout in some trees, and an empty match would make every test below
				// silently check nothing.
				if m, _ := filepath.Glob(filepath.Join(dir, "*", "steps", "*.walk")); len(m) > 0 {
					return dir, true
				}
				if m, _ := filepath.Glob(filepath.Join(dir, "*", "steps", "*.yaml")); len(m) > 0 {
					return dir, true
				}
			}
		}
	}
	return "", false
}

// skipWithoutStages ends a test when the family it reads is not checked out.
func skipWithoutStages(t *testing.T, family string) {
	t.Helper()
	if _, ok := stageFamilyRoot(family); !ok {
		t.Skipf("no %s stages here: the walkthroughs live in %s, not in the engine. "+
			"Point EASYVM_STAGES at a checkout of the sub-repositories to run this test",
			family, repoForFamily[family])
	}
}

// skipWithoutAnyStages is the same for a test that walks every family.
func skipWithoutAnyStages(t *testing.T) {
	t.Helper()
	for _, family := range familyOrder {
		if _, ok := stageFamilyRoot(family); ok {
			return
		}
	}
	t.Skip("no stages here: the walkthroughs live in the sub-repositories " +
		"(easyvm-linux, easyvm-windows, easyvm-macos, easyvm-bsd, easyvm-android, easyvm-microvm)")
}

// firstStageRoot is a root a test uses only for `filepath.Rel` - to print a path relative to
// something rather than an absolute one. Any family that exists will do.
func firstStageRoot() (string, bool) {
	for _, family := range familyOrder {
		if root, ok := stageFamilyRoot(family); ok {
			// One level up, so a printed path starts at the repository name rather than the stage.
			return filepath.Dir(root), true
		}
	}
	return "", false
}

// macosStage is the path of a macOS stage, or a skip when the macOS repository is not checked out.
func macosStage(t *testing.T, version, stage string) string {
	t.Helper()
	dir, ok := stageFamilyRoot("macos")
	if !ok {
		t.Skip("no macOS stages here: they live in easyvm-macos, not in the engine")
	}
	for _, ext := range []string{".walk", ".yaml"} {
		p := filepath.Join(dir, version, "steps", stage+ext)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skipf("easyvm-macos has no %s stage for version %s", stage, version)
	return ""
}

// walkPathForAny resolves a path that may be written with either extension.
//
// THE FILE NAME COMES FROM A WORKFLOW, which names a stage and not a file: `15/steps/setup.walk` and
// `15/steps/setup.yaml` are the same stage in two formats, and a tree mid-migration has both.
func walkPathForAny(path string) string {
	base := strings.TrimSuffix(strings.TrimSuffix(path, ".yaml"), ".walk")
	for _, ext := range []string{".walk", ".yaml"} {
		if _, err := os.Stat(base + ext); err == nil {
			return base + ext
		}
	}
	return base + ".yaml"
}
