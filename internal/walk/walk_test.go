package walk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The real walkthroughs live beside the Python implementation this replaces. Loading them here is
// the only check that matters for the vocabulary: a key the files use and this package does not
// know would be refused, so the test failing means the port is incomplete - which is exactly what
// a reader needs to know, and better than a hand-written fixture that agrees with itself.
const realFiles = "../.."

// TestLoadsEveryRealWalkthrough MEASURES THE PORT RATHER THAN ASSERTING IT IS FINISHED.
//
// THE FLOOR IS THE COUNT OF FILES THIS REPOSITORY SHIPS, and it exists to catch a regression: a
// key that stops parsing takes a file out of the set, and the number is the only thing that says
// so. It is not a target to lower.
//
// THE WINDOWS AND MACOS FILES ARE NOW HERE, which is what moved this from "28 of 54 borrowed from
// the Python repository" to "10 of 10 in this one". None of them uses a `python:` step any more:
// the extension each one named became a key in the vocabulary.
// THE FLOOR IS A REGRESSION GUARD, NOT A TARGET: it is the number of walkthroughs that load today.
// It went from 10 to 40 when the generator converted the upstream files whose vocabulary already
// matched, and it may only go up.
const portedFloor = 40

func TestLoadsEveryRealWalkthrough(t *testing.T) {
	if _, err := os.Stat(realFiles); err != nil {
		t.Skipf("the Python implementation is not beside this checkout (%v); this test needs its "+
			"YAML files and skips rather than passing vacuously", err)
	}
	var found int
	var loaded int
	var pythonBlocked []string
	var awaiting []string
	var unexpected []string

	err := filepath.Walk(realFiles, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() || !strings.Contains(path, string(filepath.Separator)+"steps"+string(filepath.Separator)) {
			return nil
		}
		// THE `.walk` IS THE FILE THIS PACKAGE IS ANSWERABLE FOR, AND THE `.yaml` BESIDE IT IS THE
		// FILE IT REPLACED.
		//
		// THIS USED TO WALK `.yaml` AND SKIP ANY THAT HAD A `.walk`, which was right while the
		// migration was a handful of hand-written files and became wrong the moment a generator could
		// convert the rest: 31 files gained a `.walk` in one commit, every `.yaml` was skipped for
		// having one, and nothing was loaded at all. The measurement said "0 of 10" about a tree that
		// had 47 loadable files in it.
		switch {
		case strings.HasSuffix(path, ".walk"):
		case strings.HasSuffix(path, ".yaml"):
			if _, err := os.Stat(strings.TrimSuffix(path, ".yaml") + ".walk"); err == nil {
				return nil // superseded; the `.walk` above is counted instead
			}
		default:
			return nil
		}
		found++
		f, loadErr := Load(path, nil)
		if loadErr == nil {
			if len(f.Steps) == 0 {
				unexpected = append(unexpected, path+": loaded with no steps")
				return nil
			}
			loaded++
			return nil
		}
		// THE ONLY ACCEPTABLE FAILURE IS A NAMED, UNPORTED EXTENSION. Anything else is a
		// vocabulary gap, which would mean a real file uses a key this package does not know.
		msg := loadErr.Error()
		if strings.Contains(msg, "uses \"python") || strings.Contains(msg, "uses \"python_screen") {
			pythonBlocked = append(pythonBlocked, path)
			return nil
		}
		// AN UPSTREAM `.yaml` THAT HAS NOT BEEN CONVERTED is a KNOWN, NAMED extension of the same
		// kind: it writes keys this vocabulary removed (`require:`, `record:`, `ocr:`), and each is
		// converted in its own time. The list is printed so the gap is visible rather than counted as
		// a pass - a file that still fails AFTER its `.walk` exists is a real defect and is reported.
		if strings.HasSuffix(path, ".yaml") && isUnmigratedKeys(msg) {
			awaiting = append(awaiting, path)
			return nil
		}
		unexpected = append(unexpected, msg)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == 0 {
		t.Skip("no walkthrough files found")
	}

	t.Logf("loaded %d of %d walkthroughs; %d blocked on the unported `python:` steps; "+
		"%d upstream `.yaml` files awaiting conversion", loaded, found, len(pythonBlocked),
		len(awaiting))

	if len(unexpected) > 0 {
		t.Fatalf("%d walkthrough(s) failed for a reason that is NOT a known extension - a "+
			"vocabulary gap, which is the one thing this package must not have:\n  %s",
			len(unexpected), strings.Join(unexpected, "\n  "))
	}
	if loaded < portedFloor {
		t.Fatalf("only %d walkthroughs load, down from %d: something that used to parse now does "+
			"not", loaded, portedFloor)
	}
	// AND EVERY BLOCKED FILE MUST BE EXPLAINED BY THE MIGRATION LIST, so the gap stays a plan
	// rather than becoming a number nobody looks at.
	if len(pythonBlocked) > 0 && len(Extensions) == 0 {
		t.Fatal("files are blocked on python: steps but extensions.go lists none to port")
	}
}

// A FILE THAT NAMES A PYTHON EXTENSION IS REFUSED, not silently run without it.
//
// The Go implementation has no Python. A `python:` step that was quietly skipped would report
// success for work nobody did - and these steps are the ones that drive a GUI through a stage's
// own code, so the work is not small.
//
// IT IS REFUSED AS AN UNKNOWN KEY. There used to be a `Deprecated` table that named `python:` and
// explained why it was gone; THE TABLE IS DELETED, because the walkthroughs are the only caller and
// every name in this repository is renamed in the same commit as the vocabulary. A compatibility
// entry with no external consumer is a second copy of the vocabulary that drifts from the first.
func TestPythonStepsAreRefusedWithASentence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.yaml")
	body := `name: a file with a python step
recording_resolution: [1920, 1080]
steps:
  - name: enable remote login
    python: ssh_panes:enable_remote_login
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path, nil)
	if err == nil {
		t.Fatal("a python: step must be refused; there is no interpreter in this implementation")
	}
	if !strings.Contains(err.Error(), "python") {
		t.Fatalf("the refusal must say which key is the problem; got %v", err)
	}
	// THE SENTENCE CHANGED WITH THE VOCABULARY: a step whose keys are all unknown names no verb, and
	// the refusal says so while naming the key that made it unreadable.
	if !strings.Contains(err.Error(), "names no action") && !strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("the refusal must say the step has no readable action; got %v", err)
	}
}

// AN UNKNOWN KEY IS A REFUSAL, and the message says what the key would have done wrong.
//
// 85 keys were kept alive in the Python implementation by an executor that read whatever it found.
// A misspelled key was therefore a step that did nothing and reported success, which is the one
// failure mode this repository has paid for most often.
func TestUnknownKeysAreRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.yaml")
	body := `name: a typo
recording_resolution: [1920, 1080]
steps:
  - name: click the button
    until_ms: 5000
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path, nil)
	if err == nil {
		t.Fatal("an unknown key must be refused")
	}
	if !strings.Contains(err.Error(), "until_ms") {
		t.Fatalf("the refusal must name the key; got %v", err)
	}
}

// A STEP WITH NO NAME IS REFUSED, because the log is how a failure is read and a nameless step
// gives it nothing to say.
func TestUnnamedStepIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.yaml")
	body := "name: nameless\nsteps:\n  - at: [10, 10]\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, nil); err == nil {
		t.Fatal("a step with no name must be refused")
	}
}

// `overlay false` IS A FLAG WHOSE DEFAULT IS ON, AND THE 2.0 READER HAS TO KEEP IT.
//
// TWO THINGS WENT WRONG BEFORE THIS TEST EXISTED, and both are what it now holds:
//
//  1. `.walk` IS NOT YAML. `v2Cut` splits a line on whitespace, so `overlay: false` - the YAML habit -
//     arrives as the KEY `overlay:` with an empty value, and validation answers `unknown key
//     "overlay:"`. The line is `overlay false`.
//
//  2. `boot` IS A MARKER: its children are LIFTED to sit beside it, so by the time the engine looks,
//     `overlay` is a top-level key and a reader that searched a nested `boot:` map found nothing -
//     the flag would have been silently ignored and the diff kept.
//
// `Has` cannot read it either: it folds "absent" and "false" together, and this flag's default is ON.
func TestOverlayIsAFlagWhoseDefaultIsOn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.walk")
	body := `walkthrough "overlay flag"
resolution 1920x1080
recording_resolution 1920x1080
step "boot with no key"
  boot
    machine "q35"
    args [["-drive", "file=disk.qcow2"]]
step "boot with the flag off"
  boot
    machine "q35"
    overlay false
    args [["-drive", "file=disk.qcow2"]]
step "boot with the flag on"
  boot
    machine "q35"
    overlay true
    args [["-drive", "file=disk.qcow2"]]
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Load(path, nil)
	if err != nil {
		t.Fatalf("a `boot` with `overlay` was refused: %v", err)
	}

	if _, present := f.Steps[0].Bool("overlay"); present {
		t.Fatal("a boot with no `overlay` line reported the key as present; the default is a diff " +
			"and it must come from the ABSENCE of the line, not from its value")
	}
	if on, present := f.Steps[1].Bool("overlay"); !present || on {
		t.Fatalf("`overlay false` must arrive as present-and-false; got present=%v value=%v", present, on)
	}
	if on, present := f.Steps[2].Bool("overlay"); !present || !on {
		t.Fatalf("`overlay true` must arrive as present-and-true; got present=%v value=%v", present, on)
	}
}

// A MISSING VAR IS AN ERROR AND NAMES WHAT WAS DEFINED.
//
// A blank substituted into a timeout was one number in one step and a different number in another
// - two components disagreeing from one empty string - so the resolution refuses instead.
func TestMissingVarIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.yaml")
	body := `name: unresolved
recording_resolution: [1920, 1080]
vars:
  user:
    value: vmuser
steps:
  - name: ssh in
    ssh: echo {{ password_is_not_declared }}
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path, nil)
	if err == nil {
		t.Fatal("an undeclared var must be refused")
	}
	if !strings.Contains(err.Error(), "password_is_not_declared") {
		t.Fatalf("the error must name the missing var; got %v", err)
	}
	// AND IT MUST LIST WHAT IS AVAILABLE, so the reader does not have to open the file.
	if !strings.Contains(err.Error(), "user") {
		t.Fatalf("the error should list the defined vars; got %v", err)
	}
}

// A VAR THAT NAMES A COMMAND-LINE ARGUMENT TAKES THE ARGUMENT, and falls back to its default.
func TestArgsOverrideVars(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.yaml")
	body := `name: args
recording_resolution: [1920, 1080]
vars:
  user:
    from: args.user
    default: fallback
steps:
  - name: ssh in
    ssh: whoami
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Load(path, map[string]string{"args.user": "vmuser"})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Vars["user"]; got != "vmuser" {
		t.Fatalf("user = %v, want the command-line value", got)
	}
	f2, err := Load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := f2.Vars["user"]; got != "fallback" {
		t.Fatalf("user = %v, want the default", got)
	}
}

// A DUPLICATE KEY IS AN ERROR. yaml.v3 reports it, and the Python implementation's `safe_load`
// kept the last one silently - which let a workflow with two `outputs:` blocks pass every local
// check and be refused by the runner.
func TestDuplicateKeysAreRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.yaml")
	body := `name: dup
recording_resolution: [1920, 1080]
ssh_port: 2222
ssh_port: 2223
steps:
  - name: one
    ssh: true
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, nil); err == nil {
		t.Fatal("a duplicate key must be refused, because the runner refuses the file it appears in")
	}
}

// A STEP THAT CARRIES `ocr:` AND `expect:` TOGETHER IS REFUSED, BECAUSE THE SECOND HALF NEVER RUNS.
//
// THE DISPATCHER'S FIRST MATCHING BRANCH WINS: `case s.Has("ocr")` calls `doOCR` and returns, so a
// `expect:` in the same step is read from the file, accepted by the vocabulary, and silently skipped.
// MEASURED on bsd/ghostbsd/steps/gbi.yaml, whose step read a screen and decided from it:
//
//	is the X driver menu the thing on screen: 37 word(s) -> ocr/screen.txt      <- the reading
//	                                                                             <- and no `expect:`
//
// and the next step failed with `if: menu` names a boolean no `expect:` step has made - a true
// statement about a file that looks like it made one.
//
// THE RULE IS THE ONE THE WHOLE DSL RESTS ON: one step does one thing. A reading and a decision are
// two, and a vocabulary that accepts them together is a vocabulary that lets a file say something it
// does not mean.
// A STEP THAT READS AND DECIDES IN ONE MAPPING IS REFUSED, because the dispatcher runs the first
// branch it matches and returns - so the second half would never run, in a file that loaded.
//
// `read:` IS THE READING NOW (`ocr:` was the same operation restricted to a word list), and the rule
// is unchanged: one step does one thing.
func TestAReadingStepCannotAlsoCheck(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.yaml")
	body := `name: probe
recording_resolution: [1920, 1080]
steps:
- name: read and decide
  read: {name: screen, of: [text], rect: [0, 0, 100, 100], rect_at: [1920, 1080], wait: 0}
  expect: {name: done, from: screen, mode: says_any, words: [[ok]]}
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path, nil)
	if err == nil {
		t.Fatal("a step with both `read:` and `expect:` loaded, and the `expect:` would never run - the " +
			"dispatcher takes the reading branch and returns. Split it into two steps.")
	}
	if !strings.Contains(err.Error(), "one thing") && !strings.Contains(err.Error(), "expect") {
		t.Fatalf("the error should say why: %v", err)
	}
}

// isUnmigratedKeys reports whether a load failure is a key this vocabulary REMOVED, which is what an
// upstream `.yaml` that has not been converted still writes.
//
// IT IS A NAMED LIST, NOT A PATTERN: "any unknown key" would make this test pass on a typo, and the
// whole point of `TestLoadsEveryRealWalkthrough` is that a vocabulary gap cannot hide.
func isUnmigratedKeys(msg string) bool {
	for _, key := range []string{
		`unknown key "require"`, `unknown key "record"`, `unknown key "ocr"`,
		`unknown key "reboot"`, `unknown key "wait_after"`, `unknown key "family"`,
		`no longer a key`, `unknown key "region"`, `unknown key "region_at"`,
	} {
		if strings.Contains(msg, key) {
			return true
		}
	}
	return false
}
