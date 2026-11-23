package machine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain LOADS THE SHAPES THE BINARY SHIPS WITH, ONCE, FOR EVERY TEST IN THIS PACKAGE.
//
// IT IS NOT A CONVENIENCE. `Boot.shape()` resolves a name through the registry, so a package whose
// tests ran without loading one would have every builder return the zero shape: no firmware, no
// devices, no adapter - and the tests would pass by asserting that an empty machine is empty.
//
// AND IT PARSES EVERY EMBEDDED FILE ON EVERY RUN, which is how a malformed shape is caught by `go
// test` rather than by the first guest that tries to boot.
//
// THE FOUR MACHINES THIS BINARY DOES NOT SHIP ARE LOADED FROM THE SUB-REPOSITORIES WHEN THEY ARE
// BESIDE IT. `windows`, `macos`, `bsd` and `android` are owned by the repositories that run them and
// reach a daemon through `stage.yml`'s `shapes_dir:`; the tests below assert things about those
// machines, so they need the real files rather than a hand-written stand-in that could agree with the
// code and disagree with the machine. A checkout without them keeps only the two embedded shapes, and
// the tests that need the others fail rather than pass over an empty machine - which is the honest
// outcome, because a shape that is not here cannot be checked.
func TestMain(m *testing.M) {
	MustLoadEmbedded()
	loadSubRepositoryShapes()
	os.Exit(m.Run())
}

// loadSubRepositoryShapes registers the four machines the sub-repositories own.
//
// A FAILURE IS REPORTED ON STDERR AND NOT FATAL: this runs before any test, and a repository that is
// simply not checked out is the ordinary case for a container build. What must NOT happen is a
// silently zero shape, so the tests below that name `windows` or `macos` assert on their own results
// rather than trusting that a load happened.
func loadSubRepositoryShapes() {
	var bases []string
	if env := os.Getenv("EASYVM_STAGES"); env != "" {
		bases = strings.Split(env, string(os.PathListSeparator))
	} else {
		// The test binary runs in `internal/machine`, so the repository root is two levels up and
		// its siblings are three.
		bases = []string{"../..", "..", "../../.."}
	}
	for _, base := range bases {
		for _, repo := range []string{
			"easyvm-windows", "easyvm-macos", "easyvm-bsd", "easyvm-android",
			"sd-windows", "sd-macos", "sd-bsd", "sd-android",
		} {
			dir := filepath.Join(base, repo, "shapes")
			if _, err := os.Stat(dir); err != nil {
				continue
			}
			// `LoadDir` registers what it finds; a name that is already taken is an error, so a
			// second copy of the same machine fails here rather than silently replacing the first.
			if err := LoadShapes(dir); err != nil {
				// One machine already registered is the normal case when two bases are searched -
				// the same repository is reachable through `..` and `../..`. Only a parse failure is
				// worth reporting, and `LoadShapes` reports both with the file's name.
				if !strings.Contains(err.Error(), "two shapes are named") {
					os.Stderr.WriteString("machine: " + dir + ": " + err.Error() + "\n")
				}
			}
		}
	}
}
