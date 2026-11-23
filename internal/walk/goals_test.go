package walk

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// A KEY THAT HID A DECISION IS A KEY THAT MADE THE FILE LIE.
//
// This is the check for the class of mistake that produced `dismiss:`, `copy_in:`, `select_disk:`
// and `apple_account:` - each was a single key whose implementation contained a loop, a click
// sequence or a tolerance, so the file said one thing and the Go said three. The pattern is not
// "this key is long": it is "a key whose name is a GOAL rather than an ACTION".
//
// THE LIST IS THE ACCEPTED VOCABULARY, and it is here rather than in internal/step because this is
// about what the FILES may say, not about what the executor can do.
var allowedActionKeys = map[string]string{
	// --- pure atoms ---
	"click":      "one click",
	"double":     "one double-click",
	"at_window":  "one click, placed relative to a window edge",
	"key":        "one keystroke",
	"type":       "typing at the current focus",
	"wait":       "waiting, with nothing clicked",
	"screenshot": "one capture",
	"unless":     "a gate on the step it is written on",

	// --- the machine ---
	"boot":     "start the machine this file owns",
	"reboot":   "reset it",
	"shutdown": "stop it by a NAMED method",
	"disk":     "name the file boot: starts",
	"record":   "start or stop the recording",
	"compress": "flatten the overlay into an artifact",

	// --- channels into the guest ---
	"ssh":         "one command, one exit status",
	"require_ssh": "wait for sshd",
	"scp":         "one copy, size-checked on both sides",
	"script":      "one body, copied in and run by path",

	// --- measurements on the screen ---
	"until":      "wait for words",
	"until_any":  "wait for any of several word-sets",
	"expect":     "require words after the action",
	"expect_any": "require any of several word-sets",
	"desktop":    "one test of the frame",
	"wait_for":   "one test of a control with no readable text",

	// --- the host's share ---
	"share_serve": "start smbd",
	"share_mount": "the guest mounts it",

	// --- host-side files ---
	"write": "write one file on the host",
	"fetch": "produce one asset",

	// --- the loop ---
	"repeat_until": "actions, then a screen test, up to a ceiling",
}

// NO KEY MAY BE A GOAL. These four were removed because each one was a goal whose body was several
// actions or a decision - and the file could not say which.
var forbiddenGoalKeys = map[string]string{
	"dismiss": "expand into a `do:` list: three Quit clicks four seconds apart, then the " +
		"VoiceOver chord. It also opened its own QMP connection, which is the framework's " +
		"business and not a step's",
	"select_disk":   "it was a LOOP and a RANGE. Use `repeat_until:` with `number_between:`",
	"apple_account": "it was four clicks with their timings. Write them as a `do:` list",
	"copy_in": "it was a copy plus a size check. `scp:` now checks the remote size, so every " +
		"copy gets it",
}

// TestNoStepKeyIsAGoal walks the real files and refuses a key that is a goal rather than an action.
func TestNoStepKeyIsAGoal(t *testing.T) {
	root := filepath.Join("..", "..")
	// `globSteps` RATHER THAN A `.yaml` LIST, so a tree mid-migration is read in the format each file
	// is being migrated TO - a `.walk` wins over the `.yaml` it replaced.
	var files []string
	files = append(files, globSteps(root, "windows")...)
	files = append(files, globSteps(root, "macos")...)
	if len(files) == 0 {
		t.Skip("no walkthroughs here")
	}

	for _, path := range files {
		f, err := Load(path, nil)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for _, s := range f.Steps {
			for key := range s.Spec {
				if why, gone := forbiddenGoalKeys[key]; gone {
					t.Errorf("%s: step %q uses %q, which is a GOAL rather than an action: %s",
						filepath.Base(path), s.Name, key, why)
				}
			}
		}
	}
}

// AND THE ATOMS A GOAL WAS EXPANDED INTO MUST BE THERE.
//
// The dismissal became a `do:` list, and a `do:` list that lost one of its clicks would still look
// like a dismissal. Three clicks four seconds apart is the measured number: a click that arrives
// before the dialog is drawn lands on wallpaper and looks identical in a log to one that dismissed
// it (run 42162859846).
func TestTheDismissalKeepsItsMeasuredShape(t *testing.T) {
	// `ssh` IS THE ONE THAT DRIVES THIS DISMISSAL. `sequoia.yaml` had the same shape and is gone: it was
	// an orphan - no workflow dispatched it - and its wizard was already covered by `ssh`'s own loop.
	for _, name := range []string{"ssh.walk"} {
		path := filepath.Join("..", "..", "macos", "15", "steps", name)
		f, err := Load(path, nil)
		if err != nil {
			continue // the file is not here
		}
		var found bool
		for _, s := range f.Steps {
			// THE ONE THAT WAS A PRIMITIVE, not every step whose name starts with "dismiss":
			// "dismiss anything over the login window" is a single `key: esc` and should stay one.
			if !strings.Contains(strings.ToLower(s.Name), "overlays") {
				continue
			}
			found = true
			actions, ok := s.Spec["do"].([]any)
			if !ok {
				t.Fatalf("%s: %q is not a `do:` list any more", name, s.Name)
			}
			var clicks, keys int
			for _, a := range actions {
				m, ok := a.(map[string]any)
				if !ok {
					continue
				}
				if _, ok := m["click"]; ok {
					clicks++
					// FOUR SECONDS, NOT TWO. Two was measured short on a loaded guest.
					settle, ok := toFloat(m["settle"])
					if !ok {
						settle, ok = toFloat(m["wait_after"])
					}
					if !ok || settle < 4 {
						t.Fatalf("%s: a Quit click waits %v seconds after it; the measured gap is 4 "+
							"(run 42162859846)", name, settle)
					}
				}
				if _, ok := m["key"]; ok {
					keys++
				}
			}
			if clicks != 3 {
				t.Fatalf("%s: %d Quit clicks, want 3 - one arriving before the dialog is drawn "+
					"lands on wallpaper and looks like a dismissal", name, clicks)
			}
			if keys != 3 {
				t.Fatalf("%s: %d keys, want 3 (esc, meta_l+q, esc)", name, keys)
			}
		}
		if found {
			return
		}
	}
	t.Log("no dismissal step found; the file may have moved")
}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case float64:
		return t, true
	}
	return 0, false
}

// EVERY `boot:` MUST HAVE A DISK BEFORE IT, OR `--boot-disk` SUPPLIED BY THE CALLER.
//
// `internal/machine.Boot.Validate` refuses a boot with no target, and the message is good:
//
//	booting needs a target disk: a `disk:` step must come first, or pass --boot-disk
//
// BUT IT ARRIVES AFTER EVERY HOST-SIDE STEP HAS RUN, which for the two install stages means after
// the answer CD is built and after a 7.7 GiB installer is resolved. Run 43044525517 spent three
// minutes on exactly that: `build the answer CD` passed, `name the installer` passed, and then
// `boot the installer` refused because no step had created the disk.
//
// THE RULE IS STRUCTURAL AND CHEAP TO CHECK: a boot step's file must either create the disk above it
// (`disk_gb:`), name an existing one (`disk:`), or DECLARE that its disk arrives as `--boot-disk`
// with a top-level `boot_disk: from_caller`. The declaration exists because six of the nine files
// genuinely continue from another stage's output - and without it, "boots with no disk above it" is
// indistinguishable from the file that forgot to create one.
func TestEveryBootHasADiskBeforeIt(t *testing.T) {
	root := filepath.Join("..", "..")
	// `globSteps` RATHER THAN A `.yaml` LIST, so a tree mid-migration is read in the format each file
	// is being migrated TO - a `.walk` wins over the `.yaml` it replaced.
	var files []string
	files = append(files, globSteps(root, "windows")...)
	files = append(files, globSteps(root, "macos")...)
	if len(files) == 0 {
		t.Skip("no walkthroughs here")
	}

	for _, path := range files {
		f, err := Load(path, nil)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if f.BootDiskFromCaller {
			continue
		}
		var sawDisk bool
		for _, s := range f.Steps {
			// A DISK STEP IS EITHER `disk_gb:` (creates one) OR `disk:` (names one).
			if s.Has("disk_gb") || s.Has("disk") {
				sawDisk = true
			}
			if !s.Has("boot") {
				continue
			}
			if sawDisk {
				continue
			}
			t.Errorf("%s: step %q boots with no disk above it. A `disk:` step names an existing "+
				"image and `disk_gb:` creates one; without either, the boot refuses with "+
				"\"booting needs a target disk\" - AFTER every host-side step before it has run, "+
				"which is minutes of work for a structural mistake", filepath.Base(path), s.Name)
		}
	}
}

// EVERY WALKTHROUGH IN THE TREE MUST LOAD, WITH NO `python:` STEP LEFT.
//
// THE MIGRATION'S COMPLETENESS IS A FILE QUESTION, AND IT IS THE ONLY PART OF IT THAT CAN BE ANSWERED
// WITHOUT A CI RUN. The constraint for this port is Go and YAML only: a `python:` step names a module
// in the distribution's own directory, and the Go build has no way to run it - so a file containing
// one is a file that cannot run at all, whatever a CI job says.
//
// IT FOUND FIVE, AND THEN THREE MORE: after the thirteen `bootstrap_files:launcher` steps became
// `write: launcher`, ghostbsd still had `panes:install_icon`, `wait_for_live_desktop` and
// `bootstrap_files:smbd_log`. Each needed a different answer, and none of them was "leave it".
func TestEveryWalkthroughLoads(t *testing.T) {
	root := filepath.Join("..", "..")
	var files []string
	patterns := globStepsAny(root)
	for _, pat := range patterns {
		matches, err := filepath.Glob(pat)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, matches...)
	}
	if len(files) == 0 {
		t.Skip("no walkthroughs here")
	}

	for _, path := range files {
		rel, _ := filepath.Rel(root, path)
		if _, err := Load(path, nil); err != nil {
			t.Errorf("%s does not load: %v", rel, err)
		}
	}
	// AND THE COUNT IS AN ASSERTION, so a tree that lost files cannot pass by having fewer to check.
	//
	// THE FLOOR MOVED WHEN THE MERGE BROUGHT THE UPSTREAM `.yaml` FILES BACK: a `.walk` now WINS over
	// the `.yaml` it replaced, so the number found is the number of STAGES rather than the number of
	// files on disk. Eighteen of them are `.walk`; the rest are `.yaml` stages with no replacement yet,
	// which `TestLoadsEveryRealWalkthrough` names as awaiting conversion.
	if len(files) < 20 {
		t.Errorf("only %d walkthroughs found, and this tree has at least twenty stages. A glob "+
			"that stopped matching is a green test about nothing", len(files))
	}
}

// A FILE THAT CLICKS OR WAITS MUST START ITS OWN MACHINE.
//
// THIS IS THE FAILURE THAT COST THIRTEEN LEGS AND TWO macOS LEGS, and it has ONE shape: a file whose
// steps act on a guest, and whose `boot:` step does not exist because the Python driver started qemu
// before running the walkthrough.
//
// The two symptoms are different and both were measured on run 43052807693:
//
//	`run:`/`key:`/`at:` first   open the terminal: qmp .../drive.sock.qmp: dial unix ...
//	                            connect: no such file or directory
//	`require_ssh:` first        wait for sshd: nothing accepted ssh on 172.30.0.1:2222
//	                            within 15m0s: dial tcp ...: i/o timeout
//
// A REFUSED CONNECTION AND A TIMEOUT ARE DIFFERENT DIAGNOSES - the first means the bridge answered
// and nothing listened, the second means the packet went to a guest that was never started - and
// neither names the missing step.
//
// THE RULE: a file that has no `boot:` must ALSO have no step that needs a guest. A file that only
// fetches, or only inspects the host, is fine; a file that clicks needs a machine.
func TestAFileThatNeedsAGuestStartsOne(t *testing.T) {
	root := filepath.Join("..", "..")
	var files []string
	files = append(files, globStepsAny(root)...)
	if len(files) == 0 {
		t.Skip("no walkthroughs here")
	}

	// A STEP THAT TOUCHES THE GUEST. `fetch:`, `write:` and `disk_gb:` are HOST work and need no
	// machine; everything here does.
	needsGuest := map[string]bool{
		"click": true, "at_button": true, "at_window": true, "at_bottom": true, "double": true,
		"key": true, "type": true, "run": true, "ssh": true, "scp": true, "script": true,
		"require_ssh": true, "desktop": true, "until": true, "until_any": true,
		"until_console": true, "screenshot": true, "share_mount": true, "shutdown": true,
		"reboot": true, "record": true, "compress": true, "disk_settle": true,
	}

	for _, path := range files {
		f, err := Load(path, nil)
		if err != nil {
			continue // TestEveryWalkthroughLoads reports the load failure
		}
		var boots bool
		for _, s := range f.Steps {
			if s.Has("boot") {
				boots = true
			}
		}
		if boots {
			continue
		}
		for _, s := range f.Steps {
			for key := range s.Spec {
				if !needsGuest[key] {
					continue
				}
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s: step %q uses %q and the file has NO `boot:` step. In the Python "+
					"implementation the stage's own script started qemu before running the "+
					"walkthrough, so the file never said it. Reproduced as YAML, the machine has "+
					"no owner: the daemon is started, the run is accepted, and the first step "+
					"that needs a guest reports `drive.sock.qmp: no such file` or an `i/o "+
					"timeout` - neither of which names the missing step", rel, s.Name, key)
				break
			}
		}
	}
}

// NO WALKTHROUGH READS A STEP NAME TWICE.
//
// IT IS A MISTAKE THIS PORT HAS NOW MADE AND CAUGHT ONCE, IN THE HARDEST WAY TO NOTICE: a scripted edit
// of eleven Linux files INSERMED a replacement block without removing the original, so
// `linux/manjaro/steps/ssh.yaml` had `allow password authentication` at step 14 AND step 16.
//
// THE FILE STILL LOADED. The second run would have succeeded - the step is idempotent - and the cost
// would have been a duplicate line in every log and a reader wondering which one mattered.
//
// THE NAME IS A STEP'S IDENTITY in `--start-at`, `--stop-before` and in the log, so a duplicate is
// ambiguous for three things at once.
func TestNoStepNameRepeats(t *testing.T) {
	root := filepath.Join("..", "..")
	var files []string
	files = append(files, globStepsAny(root)...)
	if len(files) == 0 {
		t.Skip("no walkthroughs here")
	}

	for _, path := range files {
		f, err := Load(path, nil)
		if err != nil {
			continue // TestEveryWalkthroughLoads reports it
		}
		seen := map[string]int{}
		for i, s := range f.Steps {
			if s.Name == "" {
				continue
			}
			if first, dup := seen[s.Name]; dup {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s: step name %q appears at %d and again at %d. The name is a step's "+
					"identity in --start-at, --stop-before and in the log, so a duplicate is "+
					"ambiguous for all three", rel, s.Name, first, i+1)
			}
			seen[s.Name] = i + 1
		}
	}
}

// A SCRIPTED REWRITE DELETED TEN FILES' WORTH OF STEPS, AND NOTHING NOTICED.
//
// `re.sub(r'  - name: open the terminal\n(?:    .*\n)+', NEW, s, flags=re.S)` LOOKS HARMLESS and is
// catastrophic: `re.S` makes `.` match newlines, so `(?:    .*\n)+` runs past the end of the step and
// swallows everything indented after it. Measured here: ubuntu went from 14 steps to 6, manjaro from
// 21 to 6, and eight more lost their entire install-and-enable sequence.
//
// THE FILES STILL LOADED. A walkthrough with six steps is a valid walkthrough, so `easyvm check`
// passed - which is why the first thing to fail was a CI leg, twenty minutes later.
//
// THIS TEST GUARDS THE SHAPE RATHER THAN THE COUNT: every file whose name says it installs sshd must
// have the steps that DO it. A count would need updating on every edit; the presence of the actions is
// the fact that matters.
func TestTheLinuxSSHFilesStillInstallAndEnableSSHD(t *testing.T) {
	root := filepath.Join("..", "..")
	files := globBoth(root, "linux", "ssh")
	if len(files) == 0 {
		t.Skip("no Linux ssh walkthroughs here")
	}

	for _, path := range files {
		f, err := Load(path, nil)
		if err != nil {
			continue // TestEveryWalkthroughLoads reports it
		}
		rel, _ := filepath.Rel(root, path)
		var installs, enables, waits bool
		for _, s := range f.Steps {
			body := strings.ToLower(s.Get("run") + " " + s.Get("ssh"))
			// THE INSTALL IS ONE OF FIVE PACKAGE MANAGERS OR A DECLARATION, and nixos needs the last
			// one: `services.openssh.enable = true` in configuration.nix IS the install there, and
			// `nixos-rebuild` is what makes it so. Requiring a package-manager verb refused the one
			// distribution whose package manager does not have one.
			if strings.Contains(body, "openssh") &&
				(strings.Contains(body, "apt-get") || strings.Contains(body, "pacman") ||
					strings.Contains(body, "dnf") || strings.Contains(body, "zypper") ||
					strings.Contains(body, "pkg ") || strings.Contains(body, "pkg_add") ||
					strings.Contains(body, "services.openssh")) {
				installs = true
			}
			if strings.Contains(body, "enable") && strings.Contains(body, "ssh") {
				enables = true
			}
			// A WAIT FOR sshd IS A `repeat` WHOSE UNTIL IS THE MACHINE'S OWN ssh FACT, because
			// `require_ssh:` is gone: the daemon probes ssh on its own ticker and the file repeats
			// on that answer.
			if lp, ok := s.Spec["repeat"].(map[string]any); ok {
				if u, ok := lp["until"].(string); ok && strings.Contains(u, "ssh") {
					waits = true
				}
			}
		}
		if !installs {
			t.Errorf("%s has no step that installs openssh. A scripted rewrite can delete a step "+
				"without breaking the YAML, and a six-step walkthrough that only logs in is still a "+
				"valid file", rel)
		}
		if !enables {
			t.Errorf("%s has no step that enables or starts sshd, so the disk it produces has no "+
				"remote login and every later stage fails on `wait for sshd`", rel)
		}
		if !waits {
			t.Errorf("%s never waits for sshd, so it reports success without proving the thing it "+
				"exists to do", rel)
		}
	}
}

// `findstr -C:` IS NOT A THING, AND IT COST THIS PROJECT STEP 24 OF 29, TWICE.
//
// findstr spells its literal-string option `/C:`. A DASH IS NOT ACCEPTED AS A SWITCH, so `-C:"LOOKING"`
// is a search for the literal characters `-C:LOOKING` - a pattern that can never match what the command
// was piped from. The step then reports
//
//	error:   prove sshd is listening: the command exited 1
//	answered 23 step(s)
//
// and the note above it blamed "no line matched" and REORDERED THE TWO FILTERS, which cannot help: a
// pattern that never matches does not start matching when it is applied second.
//
// THE COMMENT IS NOT THE DEFENCE. This test reads every walkthrough for a `findstr` whose switch is
// spelled with a dash, because the mistake looks correct in prose and reads correctly in a diff.
func TestNoFindstrSwitchIsSpelledWithADash(t *testing.T) {
	root := filepath.Join("..", "..")
	var checked int
	{
		files := globStepsAny(root)
		for _, path := range files {
			rel, _ := filepath.Rel(root, path)
			// THE BODIES, NOT THE FILE. Comments in this project QUOTE the failing command on purpose -
			// that is how a reader learns what went wrong - so a check over raw text reports the
			// EXPLANATION as the defect. Measured: this test went red on the paragraph that documents
			// the `-C:` bug it exists to prevent.
			doc, err := Load(path, nil)
			if err != nil {
				continue
			}
			for _, step := range doc.Steps {
				body := step.Get("ssh") + "\n" + step.Get("run") + "\n" + step.Get("script")
				if !strings.Contains(body, "findstr") {
					continue
				}
				checked++
				// THE BAD FORMS. `/C:` is correct and is not matched here; `-C:`, `-R`, `-I`, `-V` and
				// the rest all are.
				for _, bad := range []string{"findstr -C:", "findstr -R ", "findstr -I ", "findstr -V ",
					"findstr -S ", "findstr -N", "findstr -L"} {
					if strings.Contains(body, bad) {
						t.Errorf("%s: the step %q uses %q. findstr takes FORWARD-SLASH switches; a "+
							"dash makes the switch part of the search string, so the filter can never "+
							"match and the step reports `the command exited 1` as if the guest were "+
							"wrong", rel, step.Name, bad)
					}
				}
			}
		}
	}
	if checked == 0 {
		t.Log("no walkthrough step uses findstr; nothing to check")
	}
}

// EVERY `winget install` IN THIS PROJECT MUST READ ITS EXIT CODE THE SAME WAY, BECAUSE THE WRONG WAY
// LOOKS CORRECT AND CANNOT WORK.
//
// `$LASTEXITCODE` IN POWERSHELL IS AN int32 CARRYING AN HRESULT. winget's "already installed, nothing
// to upgrade" is 0x8A15002B, which arrives as -1978335189; -1978335189 & 0xFF is 0x2B, 43, the number a
// POSIX shell reports and the number `lib/steps.py` logs. So `$rc -eq 43` NEVER MATCHES - it did not,
// three attempts in a row - and `exit $LASTEXITCODE` reports an installed package as a failure:
//
//	script install-buildtools.ps1 (powershell) -> 2316632107
//	Found an existing package already installed. Trying to upgrade the installed package...
//	No available upgrade found.
//
// 2316632107 IS ALSO 0x8A15002B. THE MESSAGE SAYS THE PACKAGE IS MISSING AND IT IS PRESENT, which is
// why this cost several runs of guessing at the index, at the package id, and at the desktop gate.
//
// ONE STEP LACKED THE MASK while the six after it had it, so the rule lived in six places and its
// duplicate did not. This test requires it in ALL of them.
func TestEveryWingetInstallMasksItsExitCode(t *testing.T) {
	root := filepath.Join("..", "..")
	files := globSteps(root, "windows")
	if len(files) == 0 {
		t.Skip("no Windows walkthroughs here")
	}
	var installs int
	for _, path := range files {
		rel, _ := filepath.Rel(root, path)
		// PARSED, NOT SLICED. A text split on `- name:` picks up the file's own header comment and the
		// step AFTER the one that matched, and both reported failures that were not there.
		doc, err := Load(path, nil)
		if err != nil {
			continue // TestEveryWalkthroughLoads reports it
		}
		for _, step := range doc.Steps {
			body := step.Get("script") + step.Get("run") + step.Get("type")
			if !strings.Contains(body, "winget install") {
				continue
			}
			installs++
			// THE MASK, WHICHEVER FORM THE STEP USES. A `script:` step can BRANCH on it; a `type:`
			// step can only PRINT it, because what a keyboard types into a PowerShell window has no
			// exit status to check - so `-band 0xFF` is the requirement in both cases and means
			// "reported the low byte" in the second.
			if !strings.Contains(body, "-band 0xFF") {
				t.Errorf("%s: the step %q runs `winget install` without masking the exit status. "+
					"`$LASTEXITCODE` carries an HRESULT, so 0x8A15002B - \"already installed, nothing "+
					"to upgrade\" - arrives as -1978335189 and never equals 43; the step then reports "+
					"an installed package as a failure", rel, step.Name)
			}
			// AND A TYPED STEP CANNOT DECIDE, SO IT MUST AT LEAST SAY. `$LASTEXITCODE` read two
			// statements later is the wrong value - that cost dev.yaml a step - so the report is on
			// the same line, joined by `;`.
			if strings.HasPrefix(body, "winget install") && !strings.Contains(body, "WINGET_LOW=") {
				t.Errorf("%s: the step %q types `winget install` and never prints the exit status. "+
					"A typed command has no status to check, so the screen is the only place the "+
					"outcome exists, and without it the next failure is a bare timeout", rel, step.Name)
			}
			if strings.Contains(body, "exit $LASTEXITCODE") {
				t.Errorf("%s: the step %q ends with a bare `exit $LASTEXITCODE`, which passes "+
					"APPINSTALLER_CLI_ERROR_UPDATE_NOT_APPLICABLE to the runner as a failure",
					rel, step.Name)
			}
		}
	}
	if installs == 0 {
		t.Skip("no `winget install` steps here")
	}
	t.Logf("checked %d winget install step(s)", installs)
}

// A `scp:` SOURCE MUST BE A FILE THAT SOME EARLIER STEP ACTUALLY PRODUCES, AT THAT PATH.
//
// MEASURED ON WINDOWS-VM-CONFIGURE, STEP 5 OF 20:
//
//	copy the driver in: no file to copy at guest-agent/vioserial/vioser.inf
//	(work="/__w/easyvm/easyvm/work"): stat guest-agent/vioserial/vioser.inf: no such file or directory
//
// `fetch: guest_agent` EXTRACTS TWO MEMBERS FROM THE virtio-win ISO:
//
//	guest-agent/qemu-ga-x86_64.msi   ->  <work>/qemu-ga-x86_64.msi
//	vioserial/w10/amd64/vioser.inf   ->  <work>/vioserial/vioser.inf
//
// SO `guest-agent/` IS A PATH INSIDE THE ISO AND `vioserial/` IS THE PATH ON THE HOST. The file composed
// them - `guest-agent/vioserial/vioser.inf` - and a path that exists in neither place reads in a log as
// "the fetch did not run" when the fetch ran perfectly.
//
// THE TEST DERIVES THE HOST PATHS FROM THE SAME TABLES THE FETCHER USES, so a renamed output breaks here
// rather than inside a guest twenty minutes into a stage.
func TestEverySCPSourceIsProducedByAnEarlierStep(t *testing.T) {
	root := filepath.Join("..", "..")
	files := globStepsAny(root)
	if len(files) == 0 {
		t.Skip("no walkthroughs here")
	}

	// THE HOST NAMES, taken from the code that writes them. A `fetch:` puts its outputs in the work
	// directory at these paths; a `write:` names its own file; AND A WORKFLOW PULLS SOME OF THEM
	// BEFORE THE STAGE STARTS, which is the third source and the one this test was missing.
	//
	// THE SECOND HALF OF THAT CHANGE IS THE POINT: a walk no longer downloads its own media. Every
	// installer ISO, the macOS recovery image, the OpenCore firmware, the Xcode .xip and the dev
	// package tarballs are named in a workflow's `assets:` and placed in the work directory by
	// `pull-artifact.sh` before `easyvm run` is called. So a `scp:` source that no `fetch:` produces
	// is still a source that exists - and this test asserts it by reading the same workflows.
	produced := workflowAssets(t)
	produced["qemu-ga-x86_64.msi"] = true
	produced["vioserial/vioser.inf"] = true
	produced["vioserial/vioser.sys"] = true
	produced["vioserial/vioser.cat"] = true
	produced["bootstrap-launcher.ps1"] = true
	produced["bootstrap-launcher.sh"] = true
	produced["bootstrap-install.ps1"] = true
	produced["org.vm.bootstrap.plist"] = true
	produced["demo.sh"] = true

	var checked int
	for _, path := range files {
		rel, _ := filepath.Rel(root, path)
		doc, err := Load(path, nil)
		if err != nil {
			continue
		}
		for _, step := range doc.Steps {
			raw, ok := step.Spec["scp"].([]any)
			if !ok || len(raw) != 2 {
				continue
			}
			src, _ := raw[0].(string)
			if src == "" || filepath.IsAbs(src) {
				continue
			}
			checked++
			if produced[src] {
				continue
			}
			// A FILE BESIDE THE WALKTHROUGH IS A SOURCE. That is what replaced `script:`: a body too
			// long to inline lives in `steps/<walk>/<name>`, and `scp:` finds it there.
			if _, err := os.Stat(filepath.Join(filepath.Dir(path), src)); err == nil {
				continue
			}
			// A SOURCE PRODUCED BY `fetch: iso` VARIES BY DISTRIBUTION, so a file whose own earlier
			// steps name it is accepted: the fetch writes wherever that kind says, and this test cannot
			// know every installer's name.
			namedEarlier := false
			for _, prev := range doc.Steps {
				if prev.Name == step.Name {
					break
				}
				if strings.Contains(prev.Get("fetch"), "iso") {
					namedEarlier = true
					break
				}
			}
			if namedEarlier {
				continue
			}
			t.Errorf("%s: the step %q copies %q, which no `fetch:` or `write:` in this build produces "+
				"at that path. A path that exists in neither the ISO nor the work directory reads in "+
				"a log as \"the fetch did not run\"", rel, step.Name, src)
		}
	}
	if checked == 0 {
		t.Skip("no scp steps here")
	}
	t.Logf("checked %d scp source(s)", checked)
}

// A WINDOWS `ssh:` COMMAND IS RUN BY cmd.exe, WHERE SINGLE QUOTES ARE NOT QUOTING.
//
// MEASURED ON windows-ssh, STEP 24 OF 29, TWICE:
//
//	ssh: cmd /c 'netstat -ano | findstr -C:"LISTENING" | findstr -C:":22"'   -> 1
//	ssh: cmd /c 'netstat -ano | findstr /C:"LISTENING" | findstr /C:":22 "'  -> 1
//	  ''netstat' is not recognized as an internal or external command,
//
// THE SECOND RUN FIXED THE REAL findstr DEFECT (`/C:` not `-C:`) AND CHANGED NOTHING, because the whole
// chain was still inside `cmd /c '...'` and cmd has no single-quote quoting: `'netstat'` became a command
// NAME with quotes in it. The tool was never invoked.
//
// THE LESSON IS NOT "QUOTE DIFFERENTLY" - it is that the guest's ssh shell is ALREADY cmd.exe, so a
// POSIX wrapper is a layer that can only be wrong. A `ssh:` body for a Windows guest must not start with
// `cmd /c '` or contain a POSIX single-quoted argument.
func TestWindowsSSHStepsDoNotUsePOSIXQuoting(t *testing.T) {
	root := filepath.Join("..", "..")
	files := globSteps(root, "windows")
	if len(files) == 0 {
		t.Skip("no Windows walkthroughs here")
	}
	var checked int
	for _, path := range files {
		rel, _ := filepath.Rel(root, path)
		doc, err := Load(path, nil)
		if err != nil {
			continue
		}
		for _, step := range doc.Steps {
			body := step.Get("ssh")
			if body == "" {
				continue
			}
			checked++
			// `cmd /c '...'` - THE EXACT SHAPE THAT FAILED, and any `'...'` argument inside it.
			if strings.HasPrefix(strings.TrimSpace(body), "cmd /c '") {
				t.Errorf("%s: the step %q runs `cmd /c '<...>'`, and cmd.exe has no single-quote "+
					"quoting - the first word becomes a command name with quotes in it, so the tool "+
					"is never found", rel, step.Name)
			}
			// AND A SHELL USED ONLY TO QUOTE IS NOT NEEDED: sshd on Windows starts cmd.exe already.
			if strings.Contains(body, "cmd /c '") || strings.Contains(body, "cmd /c \"'") {
				t.Errorf("%s: the step %q wraps a command in `cmd /c '...'`. The guest's ssh shell "+
					"IS cmd.exe, so the wrapper adds a quoting layer that can only be wrong", rel, step.Name)
			}
		}
	}
	if checked == 0 {
		t.Skip("no Windows ssh steps here")
	}
	t.Logf("checked %d Windows ssh step(s)", checked)
}

// AN INSTALLER THAT TAKES OVER THE SCREEN HAS NO PANEL, SO `desktop: linux` CANNOT SEE IT.
//
// MEASURED ON ubuntu-install, FIFTEEN MINUTES OF:
//
//	no linux desktop within 15m0s; drop=+0.0 (need 15) ratio=0.0x (need 2) sat=+0.00 on a 1920x1080 frame
//
// AND `console.log` - which this project only started keeping in the artifact that run - SHOWED:
//
//	BdsDxe: starting Boot0002 "UEFI QEMU DVD-ROM QM00011"
//	GNU GRUB version 2.14
//	*Try or Install Ubuntu Server
//
// THE FIRMWARE BOOTED THE DVD AND THE INSTALLER TOOK OVER. `IsLinuxDesktop` measures a strip of colour
// along an edge - a panel - and subiquity draws character cells across the whole screen and no panel at
// all. The test was asking a question the guest could not answer, which is the FOURTH time this session
// that a screen check measured the wrong thing.
//
// THE TWO KINDS OF INSTALLER NEED TWO KINDS OF WAIT, AND THIS TEST IS THE DISTINCTION:
//
//	an installer that comes up by itself     `until_any:` on its own first-screen words
//	an installer that is an ICON on a desktop `desktop: linux`, because the next step double-clicks it
//
// THE REFERENCE IMPLEMENTATION ALREADY SAID SO and I did not read it: `DesktopWhen(all_of=('welcome',
// 'ubuntu'))` reads TEXT, with a 5400-second budget rather than the 900 I had guessed.
func TestAnInstallerIsWaitedForTheWayItAppears(t *testing.T) {
	root := filepath.Join("..", "..")
	files := globBoth(root, "linux", "install")
	if len(files) == 0 {
		t.Skip("no Linux install walkthroughs here")
	}

	var checked int
	for _, path := range files {
		rel, _ := filepath.Rel(root, path)
		doc, err := Load(path, nil)
		if err != nil {
			continue
		}
		// A FORM-ONLY INSTALLER HAS NO SESSION TO DRAW, AND SAYING SO IS THE POINT.
		//
		// MEASURED on openSUSE Leap 16.1: its Agama installer is a WEB UI that the live session opens
		// straight into - `Select a mode`, then an `Installation` summary of five form sections. There
		// is no panel at any point, and the two frames before the chooser measure:
		//
		//	a solid teal splash    body 154.7  top 154.7  bottom 154.7  drop 0.0 / 0.0
		//	the product chooser    body 254.1  top 236.4  bottom 247.2  drop 17.7 / 6.9
		//
		// `IsLinuxDesktop` wants `drop > 15`, so `desktop: linux` would spend its whole budget on both
		// - an honest measurement of a medium that does not have what it measures.
		//
		// SO THE EXEMPTION IS DECLARED IN THE FILE, in `notes:`, and the test only honours it for a
		// file that says the words `form-only installer`. A blanket list of names here would be
		// "some files do not count"; this is a file stating why it is not that shape.
		if strings.Contains(doc.Notes, "form-only installer") {
			checked++
			continue
		}
		waitAt := -1
		for i, s := range doc.Steps {
			name := strings.ToLower(s.Name)
			if strings.Contains(name, "wait for the") &&
				(strings.Contains(name, "desktop") || strings.Contains(name, "installer")) {
				waitAt = i
				break
			}
		}
		if waitAt < 0 {
			t.Errorf("%s has no step that waits for its live session to draw", rel)
			continue
		}
		checked++
		wait := doc.Steps[waitAt]

		// THE WAIT MEASURES A PICTURE, NOT A THRESHOLD FAMILY.
		//
		// It used to be `desktop: linux` or `desktop: linux/<name>`, which chose one of six
		// family-wide threshold sets in `internal/screen`. Those are gone: MEASURED on one ubuntu
		// recording, every frame compared with the desktop reference the file already shipped gave
		//
		//	 30s   similarity 0.0004   the boot splash
		//	 90s   similarity 0.0041   still splashing
		//	120s   similarity 0.9909   the desktop
		//	1600s  similarity 0.9767   the desktop, an hour later
		//
		// - two orders of magnitude, where the thresholds that windows failed on were three numbers
		// within a few percent of each other. So the wait is a loop over `check: {mode: like}` with a
		// `reference:`, and THIS ASSERTION IS THAT THE LOOP ASKS THE PICTURE RATHER THAN WORDS.
		//
		// WHAT IT STILL REFUSES: a wait on OCR. A screen that says "welcome" is not a session, and
		// the mistake this was written for was a loop that matched a word on the boot splash.
		if !wait.Has("repeat") {
			t.Errorf("%s: %s does not wait with a `repeat:`", rel, wait.Name)
		}
		rep, _ := wait.Spec["repeat"].(map[string]any)
		until, _ := rep["until"].(map[string]any)
		if until == nil || until["mode"] != "like" || until["reference"] == nil {
			t.Errorf("%s: %s waits on %v rather than on `check: {mode: like, reference: ...}`. "+
				"These are desktop images and the next step clicks a control on the session, so the "+
				"wait must ask whether that session's PICTURE is up", rel, wait.Name, until)
		}
		// `must: true` IS A FIELD OF THE `check:` INSIDE `until:`, not of `until:` itself.
		var cond map[string]any
		if until != nil {
			if c, ok := until["check"].(map[string]any); ok {
				cond = c
			} else {
				cond = until
			}
		}
		if cond != nil && cond["must"] != true {
			t.Errorf("%s: %s ends its loop on a check that binds a boolean instead of judging: "+
				"without `must: true` a wrong picture is a silent pass", rel, wait.Name)
		}
		// AND THE BUDGET IS THE INSTALL'S. A live medium unpacks itself from a multi-gigabyte ISO
		// before its desktop exists; the loop's `max:` is that budget in five-second attempts.
		if got, _ := rep["max"].(int); got < 400 {
			t.Errorf("%s: %s allows %v attempt(s); a live medium unpacks itself before its desktop "+
				"exists and the reference budget is 5400s, which is 1080 attempts", rel, wait.Name, got)
		}
	}
	if checked == 0 {
		t.Skip("no install waits here")
	}
	t.Logf("checked %d installer wait(s)", checked)
}

// A FILE EDIT MUST NOT ASSUME THE LAST LINE IS THE CLOSING BRACE.
//
// MEASURED ON nixos-vm, STEP 11 OF 22:
//
//	error:   prove the name: the command exited 1
//
// THE CAUSE WAS ONE STEP EARLIER, in `declare the machine's name`:
//
//	sed -i "/networking.hostName/d" /etc/nixos/configuration.nix
//	sed -i "\$ d"                     /etc/nixos/configuration.nix
//	printf "  networking.hostName = \"%s\";\n}\n" ... >> /etc/nixos/configuration.nix
//
// `$ d` DELETES THE LAST LINE AND ASSUMES IT IS `}`. NixOS's own default configuration.nix ends with
// the brace followed by blank lines and the comment block `nixos-generate-config` leaves, so the line
// deleted was a COMMENT and the file ended up with TWO closing braces - a file that no longer parses,
// which `nixos-rebuild` refused with a syntax error. The step after it then reported a bare
// `exited 1`.
//
// AND `python3` WOULD HAVE BEEN THE WRONG REPLACEMENT: it is not part of a NixOS minimal
// installation, so a fix written in Python trades a parse error for `command not found`. `awk` is in
// the base system.
func TestTheNixOSConfigurationEditDoesNotAssumeItsShape(t *testing.T) {
	root := filepath.Join("..", "..")
	path := filepath.Join(root, "linux", "nixos", "steps", "vm-configure.walk")
	doc, err := Load(path, nil)
	if err != nil {
		t.Skipf("no nixos vm-configure here (%v)", err)
	}
	var edit string
	for _, s := range doc.Steps {
		if strings.Contains(s.Name, "declare the machine") {
			edit = s.Get("ssh")
			break
		}
	}
	if edit == "" {
		t.Fatal("nixos has no step that declares the machine's name")
	}
	// THE SHAPE ASSUMPTION ITSELF.
	if strings.Contains(edit, "sed -i \"$ d\"") || strings.Contains(edit, `sed -i "\$ d"`) {
		t.Fatal("the step deletes configuration.nix's LAST LINE, which is a comment on a default " +
			"NixOS install - the result is two closing braces and a file nixos-rebuild refuses")
	}
	// AND NO PYTHON, because a NixOS minimal installation has none.
	if strings.Contains(edit, "python3") {
		t.Fatal("the step runs python3, which a NixOS minimal installation does not carry")
	}
	// AND IT FINDS THE BRACE RATHER THAN ASSUMING ITS POSITION.
	if !strings.Contains(edit, "awk") || !strings.Contains(edit, `$0 == "}"`) {
		t.Fatal("the step does not locate the closing brace by content")
	}
}

// A NON-INTERACTIVE ssh COMMAND DOES NOT GET A LOGIN SHELL'S PATH, AND `mount` LIVES IN /sbin.
//
// MEASURED ON cachyos-vm, STEP 16 OF 23:
//
//	prove the share is readable and writable: the command exited 127
//
// 127 IS "COMMAND NOT FOUND", and the commands in that step are `mount`, `grep`, `ls`, `printf`,
// `cat` and `test` - every one of which is on the disk. `mount` IS IN /sbin, WHICH IS NOT ON THE PATH
// OF A NON-INTERACTIVE SHELL on most of these distributions: a login shell sources /etc/profile and
// adds it, and `ssh host command` does not.
//
// A STEP THAT REPORTS 127 HAS NO FURTHER DIAGNOSIS TO GIVE - the shell never ran anything - which is
// why the fix belongs beside the command rather than in an error message.
func TestTheShareProofSetsThePathItNeeds(t *testing.T) {
	root := filepath.Join("..", "..")
	files := globBoth(root, "linux", "vm-configure")
	if len(files) == 0 {
		t.Skip("no Linux vm-configure walkthroughs here")
	}
	var checked int
	for _, path := range files {
		rel, _ := filepath.Rel(root, path)
		doc, err := Load(path, nil)
		if err != nil {
			continue
		}
		for _, s := range doc.Steps {
			body := s.Get("ssh")
			if !strings.Contains(body, "what is mounted") {
				continue
			}
			checked++
			// THE COMMENTS ARE STRIPPED FIRST: this step's own paragraph QUOTES the `mount | grep`
			// line it used to begin with, so a check over raw text finds a `mount` ABOVE the PATH line
			// that in fact sits below it - the same trap three other tests in this project hit.
			code := stripHashComments(body)
			// `mount` MUST BE NAMED IN A WAY THAT FINDS IT, AND THERE ARE TWO WAYS.
			//
			// THE FIRST WAS `PATH="$PATH:/sbin:..."`, AND IT IS WRONG ON ANY GUEST WHOSE LOGIN SHELL IS
			// NOT POSIX - measured on cachyos, where fish answers
			//
			//	Unsupported use of '='. In fish, please use 'set PATH ...'
			//
			// and the whole script exits 127. THE SECOND IS `/sbin/mount` BY PATH, which works in every
			// shell because it needs no variable at all.
			hasAbsolute := strings.Contains(code, "/sbin/mount")
			pathAt := strings.Index(code, `PATH="$PATH:/sbin`)
			mountAt := strings.Index(code, "mount |")
			if !hasAbsolute {
				if pathAt < 0 {
					t.Errorf("%s: %q calls `mount` without extending PATH AND without naming "+
						"`/sbin/mount`, so a guest whose PATH lacks /sbin reports 127, \"command not "+
						"found\", with no output at all", rel, s.Name)
					continue
				}
				// THE VARIABLE FORM MUST COME FIRST, and it must not be the `VAR=value` spelling that
				// fish refuses.
				if mountAt >= 0 && pathAt > mountAt {
					t.Errorf("%s: %q sets PATH after it calls `mount`, which is too late", rel, s.Name)
				}
				if strings.Contains(code, `PATH="$PATH:`) && !strings.Contains(code, "export PATH=") {
					t.Errorf("%s: %q assigns PATH with `VAR=value`, which fish refuses as a syntax "+
						"error - and cachyos logs in with fish", rel, s.Name)
				}
			}
		}
	}
	if checked == 0 {
		t.Skip("no share proofs here")
	}
	t.Logf("checked %d share proof(s)", checked)
}

// stripHashComments removes `#` comment lines from a shell body, so a check about a SCRIPT reads the
// script rather than the prose that explains it.
func stripHashComments(body string) string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// A DROP-IN IS A FILE NOTHING READS UNLESS THE MAIN CONFIG INCLUDES THE DIRECTORY.
//
// MEASURED ON endeavour-ssh, STEP 11 OF 13, WITH THE DROP-IN WRITTEN AND `sshd` RESTARTED:
//
//	wait for sshd: the port is open but no shell answers: ssh: handshake failed: ssh: unable to
//	authenticate, attempted methods [none password], no supported methods remain
//
// THE DROP-IN WAS THERE AND CORRECT. Arch's own /etc/ssh/sshd_config has NO
// `Include /etc/ssh/sshd_config.d/*.conf`, so /etc/ssh/sshd_config.d/99-vm.conf was a file the daemon
// never opened - and it therefore authenticated exactly as it had before.
//
// SO THE LINE IS SET IN THE MAIN FILE TOO, by REPLACING the commented `#PasswordAuthentication yes` the
// package ships rather than appending a second line beside it - `^#?\s*PasswordAuthentication` matches
// both forms, so the result is one uncommented line.
func TestThePasswordSettingReachesTheMainConfig(t *testing.T) {
	root := filepath.Join("..", "..")
	files := globBoth(root, "linux", "ssh")
	if len(files) == 0 {
		t.Skip("no Linux ssh walkthroughs here")
	}
	var checked int
	for _, path := range files {
		rel, _ := filepath.Rel(root, path)
		doc, err := Load(path, nil)
		if err != nil {
			continue
		}
		for _, s := range doc.Steps {
			body := s.Get("run")
			if !strings.Contains(body, "PasswordAuthentication") {
				continue
			}
			checked++
			// NIXOS DECLARES ITS SSHD IN configuration.nix AND HAS NO /etc/ssh/sshd_config TO EDIT -
			// the file is generated from the store at activation. Its step is correct as written.
			if strings.Contains(body, "services.openssh") {
				continue
			}
			// THE MAIN FILE IS WRITTEN, not only the drop-in directory.
			if !strings.Contains(body, "/etc/ssh/sshd_config ") &&
				!strings.Contains(body, "/etc/ssh/sshd_config\n") &&
				!strings.Contains(body, "/etc/ssh/sshd_config\\") {
				t.Errorf("%s: %q writes the setting but not /etc/ssh/sshd_config itself, and an image "+
					"whose main config has no `Include` reads only that file", rel, s.Name)
				continue
			}
			// AND IT REPLACES THE COMMENTED LINE, or the file ends up with two settings and the
			// FIRST one wins in sshd's own reading order - the package's, which says no.
			if !strings.Contains(body, "sed -i") {
				t.Errorf("%s: %q appends without replacing the commented `#PasswordAuthentication` "+
					"line, so an image that has one ends up with both", rel, s.Name)
			}
		}
	}
	if checked == 0 {
		t.Skip("no password-authentication steps here")
	}
	t.Logf("checked %d password step(s)", checked)
}

// A `run:` BODY IS TYPED LINE BY LINE, SO A MULTI-LINE SHELL SCRIPT IS NOT ONE COMMAND.
//
// `doRun` TYPES EACH LINE AND PRESSES RETURN AFTER IT. A body like
//
//	echo pw | sudo -S sh -c '
//	  mkdir -p /etc/ssh
//	  # a comment
//	  sed -i ...
//	' 2>&1 | tail -3
//
// is therefore submitted ONE LINE AT A TIME: the shell receives an unterminated quote on line 1, runs
// it, and then executes every following line - INCLUDING THE COMMENT LINES - as its own command.
//
// MEASURED ON endeavour-ssh, STEP 11 OF 13, on the run where the typing was first proven to happen:
//
//	=== allow password authentication
//	  running 19 line(s), 1020 character(s): echo admin | sudo -S sh -c ' ; ...
//	  wait for sshd: ... ssh: unable to authenticate, attempted methods [none password]
//
// THE LOG LINE THAT PROVED THE TYPING ALSO PROVED THE BUG. Nineteen lines, 1020 characters, and none of
// it ran as one script - so the drop-in and the main config were never written.
//
// `ssh:` MAY BE MULTI-LINE, because it is sent as one command over one channel. `run:` MAY NOT.
func TestARunBodyIsOneLine(t *testing.T) {
	root := filepath.Join("..", "..")
	files := globStepsAny(root)
	if len(files) == 0 {
		t.Skip("no walkthroughs here")
	}
	var checked int
	for _, path := range files {
		rel, _ := filepath.Rel(root, path)
		doc, err := Load(path, nil)
		if err != nil {
			continue
		}
		for _, s := range doc.Steps {
			body := s.Get("run")
			if body == "" {
				continue
			}
			checked++
			if n := len(strings.Split(strings.TrimRight(body, "\n"), "\n")); n > 1 {
				t.Errorf("%s: the step %q has a %d-line `run:` body. `run:` TYPES EACH LINE AND PRESSES "+
					"RETURN, so a multi-line shell script is submitted one line at a time - an "+
					"unterminated quote, then every later line as its own command, comments included. "+
					"JOIN IT WITH `;`, or use `ssh:` if it genuinely must be a script", rel, s.Name, n)
			}
		}
	}
	if checked == 0 {
		t.Skip("no run steps here")
	}
	t.Logf("checked %d run step(s)", checked)
}

// A WAIT FOR AN INSTALLER MUST NAME A WORD THE INSTALLER ALONE DRAWS.
//
// MEASURED TWICE ON popos-install. The wait was
//
//	until_any: [[language], [welcome], ["full","name"]]
//
// and it PASSED on a screen whose only content was the desktop's top bar:
//
//	last read: Wskspaces Applications M11 12 3:49 AM u = (c) O (c) i : : F J ...
//
// EVERY PANE AFTER IT WAS THEN CLICKED ONTO A DESKTOP. `at_button:` found a control fill anyway - the
// language page's Select button is y=775..806 on the reference machine and the match here was
// y=795..826, twenty pixels lower on something else entirely - so the run pressed the same point four
// times and the account pane then waited ninety seconds on a screen with no account pane in it.
//
// A LONE `language` IS NOT A MARKER. Any live desktop with a language indicator satisfies it, and a
// fill test cannot tell a button from wallpaper. The marker must be a word-pair only the installer
// draws: this file's own `clean install` footer.
func TestAnInstallerWaitNamesAnInstallerWord(t *testing.T) {
	root := filepath.Join("..", "..")
	// WORDS THAT EVERY LIVE DESKTOP HAS, so a wait on one of them can pass with no installer at all.
	generic := map[string]bool{
		"language": true, "welcome": true, "install": true, "settings": true,
		"applications": true, "workspaces": true, "activities": true,
	}
	files := globBoth(root, "linux", "install")
	if len(files) == 0 {
		t.Skip("no Linux install walkthroughs here")
	}
	var checked int
	for _, path := range files {
		rel, _ := filepath.Rel(root, path)
		doc, err := Load(path, nil)
		if err != nil {
			continue
		}
		for _, s := range doc.Steps {
			if !strings.Contains(strings.ToLower(s.Name), "installer") {
				continue
			}
			sets, _ := s.Spec["until_any"].([]any)
			if len(sets) == 0 {
				continue
			}
			checked++
			// AT LEAST ONE WORD-SET MUST BE FREE OF GENERIC WORDS, and a single generic word alone
			// in its set is what passed over a bare desktop.
			specific := false
			for _, raw := range sets {
				words, ok := raw.([]any)
				if !ok || len(words) < 2 {
					continue // a one-word set is the shape being refused
				}
				allGeneric := true
				for _, w := range words {
					if !generic[strings.ToLower(fmt.Sprint(w))] {
						allGeneric = false
					}
				}
				if !allGeneric {
					specific = true
				}
			}
			if !specific {
				t.Errorf("%s: %q waits on %v - every set is either ONE WORD or made only of words "+
					"every live desktop has, so it can pass over a bare desktop with no installer "+
					"on screen. Measured: `language` alone matched `Workspaces Applications ... 3:49 AM`",
					rel, s.Name, sets)
			}
		}
	}
	if checked == 0 {
		t.Skip("no installer waits here")
	}
	t.Logf("checked %d installer wait(s)", checked)
}

// AN INSTALL LEG THAT LEAVES NO DISK HAS PRODUCED NOTHING FOR THE STAGE AFTER IT.
//
// EVERY install LEG IS DECLARED IN ITS WORKFLOW WITH `"out": "linux-<distro>.qcow2"`, and `--out` is
// where a `compress:` WRITES - it is not a filename the engine invents. WITH NO `compress:` STEP IN THE
// FILE, NOTHING WAS WRITTEN, and the measurement is unambiguous:
//
//	linux-zorin-install-disk        518 bytes
//	linux-zorin-vm-disk      10,497,859,093 bytes
//
// THE FIRST IS AN EMPTY ZIP; THE SECOND IS A DISK. And `zorin-install` had just PASSED - every pane of
// the installer driven correctly - and then produced nothing, so the leg proved the installer works and
// left no artifact.
//
// A `compress:` NAMES `{{ out }}`, WHICH MEANS THE FILE MUST DECLARE THAT VARIABLE. `easyvm check`
// refused all thirteen files when the step was first added, which is the vocabulary doing its job: an
// undeclared variable is a path the engine cannot resolve.
func TestAnInstallLegPublishesItsDisk(t *testing.T) {
	root := filepath.Join("..", "..")
	var files []string
	for _, pat := range []string{
		filepath.Join(root, "linux", "*", "steps", "install.walk"),
		filepath.Join(root, "bsd", "*", "steps", "install.walk"),
	} {
		got, err := filepath.Glob(pat)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, got...)
	}
	if len(files) == 0 {
		t.Skip("no install walkthroughs here")
	}
	var checked int
	for _, path := range files {
		rel, _ := filepath.Rel(root, path)
		doc, err := Load(path, nil)
		if err != nil {
			continue
		}
		checked++
		// A STEP THAT WRITES THE ARTIFACT.
		publishes := false
		for _, s := range doc.Steps {
			if strings.Contains(s.Get("save"), "out") || strings.Contains(s.Get("compress"), "out") {
				publishes = true
			}
		}
		if !publishes {
			t.Errorf("%s has no `save:` on its stop step, so `--out` writes nothing and the workflow's "+
				"`\"out\"` file is never created - measured on zorin-install, whose `-disk` artifact "+
				"was 518 bytes while its `-vm-disk` was 10 GiB", rel)
		}
		// AND THE VARIABLE IT NAMES IS DECLARED.
		if _, ok := doc.Vars["out"]; !ok {
			t.Errorf("%s has a `save:` naming `{{ out }}` but declares no `out` variable, so the "+
				"engine cannot resolve the path", rel)
		}
	}
	if checked == 0 {
		t.Skip("no install walkthroughs loaded")
	}
	t.Logf("checked %d install walkthrough(s)", checked)
}

// A `save:` BEFORE A `stop:` CANNOT WORK, BECAUSE QEMU HOLDS THE DISK OPEN.
//
// MEASURED ON zorin-install, WHICH ANSWERED ALL 21 STEPS AND FAILED ON THE LAST ONE:
//
//	publish the disk: qemu-img convert: exit status 1
//	(qemu-img: Could not open '/__w/easyvm/easyvm/work/disk.qcow2': Failed to get shared "write" lock
//
// THE INSTALL WAS COMPLETE. `compress:` READS THE FILE, and qemu holds a write lock on it until the
// machine is stopped - so the disk a passing install produced could not be published, and a `-disk`
// artifact of 518 bytes was the visible result.
//
// AND THE SHUTDOWN IS `acpi`, NOT `ssh`: AN INSTALL LEG HAS NO ssh AT ALL, because the guest it drives
// is an installer. `shutdown: ssh` would fail with "no connection, and there is none".
func TestAnInstallStopsTheMachineBeforePublishingTheDisk(t *testing.T) {
	root := filepath.Join("..", "..")
	var files []string
	for _, pat := range []string{
		filepath.Join(root, "linux", "*", "steps", "install.walk"),
		filepath.Join(root, "bsd", "*", "steps", "install.walk"),
	} {
		got, err := filepath.Glob(pat)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, got...)
	}
	if len(files) == 0 {
		t.Skip("no install walkthroughs here")
	}
	var checked int
	for _, path := range files {
		rel, _ := filepath.Rel(root, path)
		doc, err := Load(path, nil)
		if err != nil {
			continue
		}
		checked++
		shutdownAt, compressAt := -1, -1
		for i, s := range doc.Steps {
			// `stop:` IS THE ONE VERB THAT STOPS THE MACHINE; `save:` is the verb that publishes the
			// disk. They may be one step or two, and either is fine as long as the stop comes first.
			if s.Has("stop") || s.Has("shutdown") {
				shutdownAt = i
				if s.Has("save") || s.Has("compress") {
					compressAt = i
				}
			}
			if s.Has("save") || s.Has("compress") {
				if compressAt < 0 {
					compressAt = i
				}
			}
		}
		if compressAt < 0 {
			t.Errorf("%s has no `save:` on its stop step, so `--out` writes nothing", rel)
			continue
		}
		if shutdownAt < 0 {
			t.Errorf("%s publishes its disk without stopping the machine first - qemu holds a write "+
				"lock and `qemu-img convert` answers \"Failed to get shared write lock\"", rel)
			continue
		}
		if shutdownAt > compressAt {
			t.Errorf("%s stops the machine AFTER compressing the disk, which is backwards", rel)
		}
		// AND IT IS `kill`, WHICH IS THE ONLY METHOD A LIVE INSTALLER ANSWERS.
		//
		// MEASURED ON zorin-install, WHICH PASSED ALL 21 STEPS AND THEN PRODUCED A 0 MiB DISK:
		//
		//	stopping with method "acpi"
		//	pressing the power button
		//	the guest did not power off within 5m0s; killing it
		//	compressing with zstd
		//	linux-zorin.qcow2: 0 MiB
		//
		// A LIVE INSTALLER DOES NOT ANSWER THE POWER BUTTON. `acpi` waited the full five minutes and
		// then killed anyway, and the kill raced `qemu-img convert` - which read a disk qemu was
		// still releasing and wrote ZERO BYTES.
		//
		// `kill` IS NOT A COMPROMISE: THE INSTALLER HAS ALREADY FINISHED, because the step above waits
		// for the install to complete. The journal replay it costs is paid by the next boot, which is
		// the stage after this one.
		shutdown := stopMethod(doc.Steps[shutdownAt])
		if shutdown != "kill" {
			t.Errorf("%s uses `shutdown: %s`. An install leg must use `kill`: there is no ssh to ask "+
				"with, a live installer ignores ACPI, and waiting for a button nothing presses ends "+
				"with the same kill five minutes later - after which `qemu-img convert` can read a "+
				"disk qemu is still releasing", rel, shutdown)
		}
	}
	if checked == 0 {
		t.Skip("no install walkthroughs loaded")
	}
	t.Logf("checked %d install walkthrough(s)", checked)
}

// A WAIT FOR "THE INSTALLER FINISHED" MUST NOT BE SATISFIED BY A WORD ON AN EARLIER PANE.
//
// MEASURED ON cachyos-install, WHICH PUBLISHED A 0 MiB DISK WITH EVERY STEP GREEN:
//
//	=== the installer finishes
//	  saw "安装成功" (1s)
//	=== stop the machine   stopping with method "kill"
//	=== publish the disk   linux-cachyos.qcow2: 0 MiB
//
// ONE SECOND IS THE TELL: a live install cannot complete in one second. The frame at that moment
// shows the installer at `initialize pacman ... ranking mirrors ... 11%`, and the match came from a
// ONE-WORD CJK ALTERNATIVE that tesseract found somewhere in the installer's own Chinese-labelled UI.
//
// THE SHAPE IS THE BUG, not the language: a `until_any:` entry with a single short word is satisfied
// by any screen containing that word. Completion pages are recognised by a PHRASE.
func TestNoCompletionWaitIsASingleShortWord(t *testing.T) {
	root := filepath.Join("..", "..")
	files := globStepsAny(root)
	checked := 0
	for _, path := range files {
		doc, err := Load(path, nil)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(root, path)
		for _, s := range doc.Steps {
			for _, key := range []string{"until_any", "expect_any", "unless_any"} {
				sets, _ := s.Spec[key].([]any)
				for _, raw := range sets {
					words, ok := raw.([]any)
					if !ok || len(words) != 1 {
						continue
					}
					one := strings.TrimSpace(fmt.Sprint(words[0]))
					// A CJK word of four characters or fewer is the measured offender; the same
					// reasoning applies to any one-glyph alternative in any script, so the check is
					// on the SHAPE rather than on the language.
					if len([]rune(one)) > 4 {
						continue
					}
					checked++
					t.Errorf("%s: %q has a one-word %s entry %q. A single short word is satisfied by "+
						"any pane that contains it - measured on cachyos, where `安装成功` matched at 1s "+
						"while the installer was at 11%% and the published disk was 0 MiB. Use a phrase "+
						"the finished installer says and no earlier pane does",
						rel, s.Name, key, one)
				}
			}
		}
	}
	t.Logf("checked %d one-word alternatives across the walkthroughs", checked)
}

// `record:` CARRIED A `poll:` ONCE, AND NEITHER EXISTS NOW.
//
// The test that lived here refused a `record:` step with a `poll:`, because `doRecord` never read it
// and the sampler ticks once a second - so a file saying `poll: 20` described a cadence nobody used.
// The recording is a property of the run now, so there is no step to misconfigure.
// ONLY THE FIRST `run:` IN A TERMINAL OPENS ONE.
//
// `terminal_wait: N` PRESSES ctrl+alt+t AND WAITS N SECONDS. On a walkthrough with six `run:` steps
// that is SIX NEW TERMINALS - measured on ubuntu-ssh, whose recording shows seven windows stacked,
// each command typed into a fresh one while the previous was still running.
//
// THE COST IS NOT COSMETIC. `apt-get install openssh-server` needs minutes; the next step opened a
// second terminal thirty seconds later and typed into that, so the install's own output was never
// seen and `systemctl is-active ssh` answered `inactive` while apt was still working.
func TestOnlyOneTerminalWaitPerFile(t *testing.T) {
	root := filepath.Join("..", "..")
	files := globStepsAny(root)
	checked := 0
	for _, path := range files {
		doc, err := Load(path, nil)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(root, path)
		opens := 0
		for _, s := range doc.Steps {
			if !s.Has("run") || !s.Has("terminal_wait") {
				continue
			}
			opens++
			checked++
			if opens > 1 {
				t.Errorf("%s: %q is the %dth `run:` with `terminal_wait`, and each one presses "+
					"ctrl+alt+t. One opens the terminal; the rest must type into it or a command "+
					"that takes minutes has its next step typed into a new window",
					rel, s.Name, opens)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no `run:` steps with `terminal_wait` were found")
	}
	t.Logf("checked %d terminal-opening step(s)", checked)
}

// A TYPED WINDOWS COMMAND MUST NOT CARRY A BACKSLASH OR A PIPE.
//
// `run:` TYPES THE LINE ONE CHARACTER AT A TIME, and both of these are lost on the way:
//
//	measured 1   `echo admin | sudo -S` arrived as `ecadmn`          (the pipe vanished)
//	measured 2   `C:\ProgramData\ssh\sshd_config` arrived broken      (the backslash broke the path)
//	measured 3   `-replace '^#?\s*Password...'` arrived as `^#?artio` (JS regex escapes)
//
// EACH ONE LEAVES POWERSHELL OR BASH WAITING: `>>` is the continuation prompt, and the step's timeout
// then reports a service that never started.
//
// `$env:ProgramData` and `Join-Path` reach the same directory with no separator characters; `*` and
// `?` are literal in `-Pattern` rather than regex syntax. A file that needs a backslash should use a
// `type:` with a slower `type_gap`, or `ssh:` once sshd exists.
func TestTypedWindowsCommandsCarryNoBackslashOrPipe(t *testing.T) {
	root := filepath.Join("..", "..")
	files := globSteps(root, "windows")
	checked := 0
	for _, path := range files {
		doc, err := Load(path, nil)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(root, path)
		for _, s := range doc.Steps {
			body := s.Get("run")
			if body == "" {
				continue
			}
			checked++
			if strings.Contains(body, `\`) {
				t.Errorf("%s: %q has a backslash in a typed `run:`. Measured: "+
					"`C:\\ProgramData\\ssh\\sshd_config` arrived with the separator lost and PowerShell "+
					"answered with its `>>` continuation prompt. Use `$env:ProgramData` and `Join-Path`",
					rel, s.Name)
			}
			if strings.Contains(body, "|") {
				t.Errorf("%s: %q has a pipe in a typed `run:`. Measured: `echo admin | sudo` arrived "+
					"as `ecadmn` - the pipe is what a typed line loses first", rel, s.Name)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no typed windows `run:` steps were found")
	}
	t.Logf("checked %d typed windows command(s)", checked)
}

// A LEG DECLARED WITH `out:` MUST HAVE A STEP THAT WRITES IT.
//
// MEASURED ON linux-ubuntu-ssh, WHICH REACHED success WITH EVERY PROOF PASSING:
//
//	=== prove the kernel over ssh
//	    Linux vmuser-Standard-PC-Q35-ICH9-2009 7.0.0-43-generic ... x86_64 GNU/Linux
//	publish the disk:  no disk to publish at .../linux-ubuntu-ssh.qcow2
//
// THE WALK HAD INSTALLED SSHD AND PROVED IT OVER A REAL SSH SESSION, and the file its workflow asked
// for was never written - because `compile:` is a STEP, not a property of the leg, and the ssh
// walkthrough had no `compress:`. So `publish` reported `no disk to publish`, `linux-ubuntu-ssh` was
// never pushed, and the vm leg spent thirty minutes on the fallback install disk.
//
// ELEVEN OF THE TWELVE LINUX SSH WALKTHROUGHS WERE MISSING IT. Only `ubuntu` gained one here; the
// other eleven and ghostbsd were fixed in the same pass.
func TestAnSSHLegWritesTheDiskItsWorkflowPublishes(t *testing.T) {
	root := filepath.Join("..", "..")
	// EVERY LEG THAT DECLARES AN `out:` WRITES IT, NOT JUST `ssh`. The same omission was found
	// again on windows-vm-configure and macos-vm-configure - both published a file neither wrote -
	// and on the `dev` walkthroughs. `install` and every linux `vm-configure` already had it.
	files := globStepsAny(root)
	checked := 0
	for _, path := range files {
		doc, err := Load(path, nil)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(root, path)
		// ONLY A WALKTHROUGH THAT PRODUCES A DISK IS CHECKED. A `record:`-only file or an
		// assistant file writes none, and demanding `compress:` from those would be wrong.
		produces := false
		for _, s := range doc.Steps {
			if s.Has("save") || s.Has("compress") {
				produces = true
				break
			}
		}
		if !produces {
			// an ssh, vm-configure or dev walkthrough is a disk leg; install always flattens
			base := filepath.Base(path)
			dir := filepath.Base(filepath.Dir(filepath.Dir(path)))
			_ = dir
			switch base {
			// `gbi.yaml` IS GHOSTBSD'S INSTALL WALKTHROUGH - its workflow names that file, not
			// `install.yaml`, so the first version of this check never looked at the one leg that
			// was publishing nothing. `install.yaml` remains for the other BSD and Linux files.
			case "ssh.yaml", "vm-configure.yaml", "dev.yaml", "install.yaml", "gbi.yaml":
				checked++
				t.Errorf("%s has no `save:` on its stop step, so its workflow publishes a disk that is "+
					"never written. Measured twice: linux-ubuntu-ssh's every proof passed and "+
					"`publish the disk` reported `no disk to publish at .../linux-ubuntu-ssh.qcow2`, "+
					"and windows-vm-configure said the same for windows11-vm-configure.qcow2 after "+
					"a SUCCESSFUL mount", rel)
			}
			continue
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no ssh walkthroughs were found")
	}
	t.Logf("checked %d ssh walkthrough(s)", checked)
}

// A TYPED COMMAND THAT USES `$` NEEDS A SLOWER `type_gap` THAN THE DEFAULT.
//
// MEASURED ON windows-ssh, FRAME 550 OF ITS RECORDING:
//
//	PS> powershell -Command "Set-Service sshd -StartupType Automatic; (Get-Service sshd).StartType"
//	Automatic                                    <- no `$`, worked
//
//	PS> powershell -Command "$f = Join-Path $env:ProgramData 'ssh/sshd_config'; Add-Content $f ...
//	                      $f                       <- THE `$` IS GONE, and so is `$env:`
//
// `$` IS SHIFT+4. The default `type_gap` of 0.02s releases the shift before the 4 is delivered, and
// the character arrives as `4`, `@` or nothing. `type:` steps have used 0.30s for this reason since
// the port; `run:` inherited a much faster default and nobody had typed a `$` in one until now.
func TestATypedDollarNeedsASlowTypeGap(t *testing.T) {
	root := filepath.Join("..", "..")
	files := globStepsAny(root)
	checked := 0
	for _, path := range files {
		doc, err := Load(path, nil)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(root, path)
		for _, s := range doc.Steps {
			body := s.Get("run")
			if !strings.Contains(body, "$") {
				continue
			}
			checked++
			gap, _ := s.Float("type_gap", 0.02)
			if gap < 0.10 {
				t.Errorf("%s: %q types a `$` with type_gap %.2fs. Measured on windows-ssh: `$f` arrived "+
					"as `f` and `$env:` as `env:` because the shift is released before the 4 is "+
					"delivered. Use at least 0.15", rel, s.Name, gap)
			}
		}
	}
	if checked == 0 {
		t.Skip("no typed command uses a `$` here")
	}
	t.Logf("checked %d typed command(s) with a `$`", checked)
}

// A `run:` AFTER POWERSHELL IS ALREADY OPEN MUST NOT SPAWN POWERSHELL AGAIN.
//
// MEASURED ON windows-ssh, FRAME 600, IN THE ADMINISTRATOR POWERSHELL WINDOW:
//
//	PS C:\Users\vmuser> powershell -Command "$f = Join-Path $env:ProgramData 'ssh/sshd_config';
//	                                          Add-Content $f 'PasswordAuthentication yes'; ..."
//	= : The term '=' is not recognized as the name of a cmdlet, function, script file...
//	+ = Join-Path C:\ProgramData 'ssh/sshd_config'; Add-Content  'Password ...
//
// THE OUTER POWERSHELL EXPANDED `$f` AND `$env:ProgramData` BEFORE THE INNER ONE EVER SAW THE LINE.
// `$f` was empty because the outer session had no such variable, so the inner PowerShell was handed
// `= Join-Path C:\ProgramData ...; Add-Content  'Password...'` - and `Add-Content` with no value
// parameter went INTERACTIVE:
//
//	cmdlet Add-Content at command pipeline position 1
//	Supply values for the following parameters:
//	Value[0]: powershell -Command "Start-Service sshd; (Get-Service sshd).Status"
//	Value[1]: powershell -Command "New-NetFirewallRule ..."
//
// so the NEXT TWO STEPS BECAME ARGUMENTS TO A PROMPT. All four sshd steps were lost, and the failure
// was reported as "nothing accepted ssh within 10m" - a timeout, not a mangled command.
//
// THE FILE OPENS POWERSHELL ONCE (step "type powershell", then Enter) and every later `run:` that is
// PowerShell should be a plain command. This test is about the nesting, not about `$`: a `$` is fine
// in a `run:` with a slow enough `type_gap`, and several Linux files legitimately use one.
func TestNoRunNestsPowerShellInsidePowerShell(t *testing.T) {
	root := filepath.Join("..", "..")
	files := globSteps(root, "windows")
	checked, opened := 0, 0
	for _, path := range files {
		doc, err := Load(path, nil)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(root, path)
		for _, s := range doc.Steps {
			if strings.Contains(strings.ToLower(s.Get("type")), "powershell") {
				opened++
			}
			body := s.Get("run")
			if body == "" {
				continue
			}
			checked++
			if strings.Contains(strings.ToLower(body), "powershell -command") {
				t.Errorf("%s: %q spawns `powershell -Command` from a `run:`. Measured on "+
					"windows-ssh: the outer PowerShell expanded `$f` and `$env:ProgramData` first, "+
					"`Add-Content` lost its value and went interactive, and the next two steps were "+
					"consumed as its parameter values", rel, s.Name)
			}
		}
	}
	if checked == 0 {
		t.Skip("no windows `run:` steps here")
	}
	t.Logf("checked %d typed command(s); %d PowerShell launch(es)", checked, opened)
}

// AN INSTALL LEG THAT PUBLISHES A DISK MUST BOOT THAT DISK BEFORE PUBLISHING IT.
//
// MEASURED ON windows-ssh, ON A DISK `windows-install` HAD JUST PUBLISHED:
//
//	pulled ghcr.io/easydev-org/easyvm:windows11 (10890 MiB)
//	failed step 4/31 "wait for the desktop"
//	drop=+1.6 (need 15) ratio=0.0x (need 2)
//
// and the extracted frames are PURE BLACK at mean luminance 1.32 from thirty seconds to eight
// hundred, carrying nothing but the firmware's own `BdsDxe: loading ...` lines. The machine never
// left the firmware: the disk installed and could not be started again.
//
// THE INSTALLER'S DESKTOP PROVES THE INSTALLER RAN, NOT THAT THE DISK BOOTS. A reboot between them
// is the only thing that tells the two apart, and putting it in the install leg means a bad disk
// fails where it was made, with its own recording, instead of fifteen minutes into the next stage.
func TestTheInstallingLegThatWasMeasuredUnbootableRebootsItsDisk(t *testing.T) {
	root := filepath.Join("..", "..")
	// WINDOWS ONLY, BECAUSE THAT IS THE ONE THAT WAS MEASURED. `windows-install` published a
	// 10890 MiB `windows11` that booted to nothing but the TianoCore logo - the ssh leg that pulled
	// it spent fifteen minutes on `wait for the desktop` with `ratio=0.0x`, and every extracted
	// frame was black at mean luminance 1.32.
	//
	// THE LINUX INSTALL LEGS ARE NOT CHANGED BY THIS. `linux-ubuntu` installs, publishes, and its
	// own ssh leg boots the result - the cycle is proven there, so demanding a second boot of twelve
	// healthy legs would add twenty minutes each to prove something already measured.
	//
	// WHEN A LINUX LEG FAILS THIS WAY, ADD IT HERE WITH ITS OWN MEASUREMENT.
	cases := []struct{ path string }{
		{walkPathFor(root, "windows/11/steps/install.yaml")},
	}
	for _, c := range cases {
		doc, err := Load(c.path, nil)
		if err != nil {
			t.Fatalf("%s: %v", c.path, err)
		}
		booted := 0
		for _, s := range doc.Steps {
			if s.Has("boot") {
				booted++
			}
		}
		if booted < 2 {
			t.Errorf("%s boots %d time(s) and publishes a disk. One boot is the INSTALLER; a disk "+
				"that installs and cannot start again is indistinguishable from a good one until "+
				"the next stage spends its timeout on a firmware logo - measured on windows-ssh "+
				"against a 10890 MiB `windows11`, whose every frame was black", c.path, booted)
		}
	}
}

// EVERY DISK THIS REPOSITORY CREATES IS 128 GiB, BECAUSE THAT IS THE SIZE THE PROJECT FIXED.
//
// MEASURED: twelve Linux walkthroughs carried `disk_gb: 64` while Windows carried 128. Nothing
// failed because of it - an install fits in 64 GiB - which is exactly why it survived: a value that
// is wrong but not fatal is never reported. The requirement is one size for every disk so that a
// published artifact is comparable across distributions and so a guest never runs out of space part
// way through an install that then looks like an installer bug.
//
// THIS IS A CONSTRAINT ON THE FILES, NOT A DEFAULT IN THE CODE. A walkthrough that says 64 asked for
// 64, and the answer is that it may not.
func TestEveryCreatedDiskIs128GiB(t *testing.T) {
	root := filepath.Join("..", "..")
	files := globStepsAny(root)
	checked := 0
	for _, path := range files {
		doc, err := Load(path, nil)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(root, path)
		// A MICROVM WALKTHROUGH IS EXEMPT, AND THE REASON IS THE SAME ONE THAT MADE THE RULE: the size
		// exists so a DISTRIBUTION INSTALL never runs out of space. A microvm installs nothing - it
		// proves the vocabulary drives a guest - so 1 GiB is the whole point, and demanding 128 would
		// make the smallest machine in the repository pay for a rule about the largest.
		microvm := false
		for _, s := range doc.Steps {
			if v, ok := s.Spec["microvm"].(bool); ok && v {
				microvm = true
			}
		}
		if microvm {
			continue
		}
		for _, s := range doc.Steps {
			if !s.Has("disk_gb") {
				continue
			}
			checked++
			gb, _ := s.Float("disk_gb", 0)
			if gb != 128 {
				t.Errorf("%s: %q creates a %.0f GiB disk. Every disk in this repository is 128 GiB - "+
					"measured: twelve Linux walkthroughs said 64 while Windows said 128, and nothing "+
					"failed because an install fits in 64, which is why it survived", rel, s.Name, gb)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no `disk_gb:` step was found")
	}
	t.Logf("checked %d disk(s)", checked)
}

// THE MACOS RECOVERY GATE MUST NOT REQUIRE THE ONE ROW THAT NEVER READS.
//
// THE RECOVERY SYSTEM DRAWS AT 640x480 - OpenCore's GOP leaves the guest in that mode before macOS
// loads a display driver - so `Disk Utility` is EIGHT PIXELS TALL. MEASURED ON THE REAL FRAME AT
// SEVERAL MAGNIFICATIONS:
//
//	scale 2   restore, time machine
//	scale 3   restore, safari
//	scale 4   restore
//	scale 6   restore, time machine, reinstall, safari
//	scale 8   restore, time machine, safari
//
// `disk utility` DOES NOT READ AT 2x, 3x, 4x, 6x, 8x, 10x OR 12x. A gate whose condition is only
// `[disk, utility]` therefore waits out its whole budget on a menu that is plainly on screen, which
// it did for thirty minutes on every attempt.
//
// THE GATE IS NOW A `repeat:` WITH ITS THREE PARTS WRITTEN DOWN - a wait, an `ocr:` reading, and a
// condition - so the rectangle and its enlargement are beside the reading rather than inside the
// condition that uses them.
func TestTheMacOSRecoveryGateAcceptsReadableRows(t *testing.T) {
	path := walkPathFor(filepath.Join("..", ".."), "macos/15/steps/setup.yaml")
	doc, err := Load(path, nil)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	var gate *Step
	for i := range doc.Steps {
		if doc.Steps[i].Name == "wait for the recovery menu" {
			gate = &doc.Steps[i]
			break
		}
	}
	if gate == nil {
		t.Fatal("no `wait for the recovery menu` step")
	}
	loop, ok := gate.Spec["repeat"].(map[string]any)
	if !ok {
		t.Fatal("the recovery gate is not a `repeat:`. The three parts - the wait, the `ocr:` reading " +
			"and the condition - have to be visible, because the reading's region and enlargement " +
			"are what make it work at 640x480")
	}
	// THE ENLARGEMENT BELONGS TO THE QUESTION, and the question is the loop's own `until:`. A
	// `read:` captures a frame and names it and has no `scale:`; a file that needs a 640x480 row to
	// be legible says `scale: 6` where it asks about that row.
	until, ok := loop["until"].(map[string]any)
	if !ok {
		t.Fatal("the loop has no `until:` - a condition with no comparison decides nothing")
	}
	if scale, _ := toFloat(until["scale"]); scale < 6 {
		t.Errorf("the condition crops at scale %.0f. Measured on the real 640x480 frame: at scale 6 "+
			"four rows read, at scale 2 only two", scale)
	}
	if _, has := until["source"]; !has {
		t.Fatal("the loop's condition names no picture - it would read the screen itself")
	}
	if _, ok := loop["until"].(map[string]any); !ok {
		t.Fatal("the loop has no `until:`")
	}
	// `until:` TESTS A BOOLEAN AND NOTHING ELSE - the comparison belongs to `expect:`.
	// THE CONDITION CARRIES THE COMPARISON ITSELF: a `repeat`'s `until` IS the test, so there is
	// no separate `expect` whose boolean the loop then reads.
	if mode, _ := until["mode"].(string); mode != "says_any" && mode != "text" && mode != "" {
		t.Errorf("the condition's mode is %q, want says_any or text - the recovery menu is four "+
			"labelled rows and any one of them is enough", mode)
	}
	sets, _ := until["words"].([]any)
	if len(sets) < 3 {
		t.Errorf("the condition accepts %d row(s). The menu has four labelled rows and two hits is "+
			"the Python implementation's own rule, because one row can be cut off by a redraw; "+
			"with fewer than three alternatives a single bad read fails the step", len(sets))
	}
}

// EVERY `region:` SAYS WHAT SIZE IT WAS MEASURED AT, AND EVERY READING NAMES ONE.
//
// A RECTANGLE IS PIXELS ON ONE PARTICULAR SCREEN, and this repository's guests draw at four different
// sizes during a single boot:
//
//	UEFI shell            1280x800
//	macOS recovery         640x480      <- the menu is 8 px tall at this size
//	mx's live session     1024x768
//	an installed system   1920x1080
//
// WITHOUT `region_at:` THERE IS NO WAY TO KNOW WHICH ONE A FILE MEANT, and both ways of guessing were
// measured and both were wrong:
//
//	nixos:  `region_at` is [1920, 1080] but this frame is 1280x800     <- hard failure, and the
//	                                                                     pane had simply not drawn
//	mx:     the framebuffer is 1024x768 but this region was measured at
//	        1920x1080; not reading it                                  <- read nothing, 30 times
//
// SO THE NUMBER IS REQUIRED. It is also what makes the scaling honest: the log says
//
//	reading the same place scaled to [253, 88, 466, 248]
//
// and a reader can check that against the frame.
func TestEveryRegionSaysWhatSizeItWasMeasuredAt(t *testing.T) {
	root := filepath.Join("..", "..")
	files := globStepsAny(root)
	checked := 0
	for _, path := range files {
		doc, err := Load(path, nil)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(root, path)
		for _, s := range doc.Steps {
			cropKey := "rect"
			if !s.Has(cropKey) {
				cropKey = "region" // the old spelling, so a half-migrated tree is still checked
			}
			if !s.Has(cropKey) {
				continue
			}
			checked++
			if !s.Has("rect_at") && !s.Has("region_at") {
				t.Errorf("%s: %q crops `region:` and does not say `region_at:`. The crop is pixels "+
					"on one screen, and this guest draws at 1280x800 in firmware, 640x480 in macOS "+
					"recovery and 1024x768 in mx's live session - a number with no size beside it "+
					"cannot be scaled, and guessing it wrong reads the wallpaper", rel, s.Name)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no cropped step was found, so this checks nothing")
	}
	t.Logf("checked %d cropped step(s)", checked)
}

// AN `ocr:` STEP NAMES ITS REGION AND THE SIZE THAT REGION WAS MEASURED AT.
//
// THE TWO ARE ONE DECISION AND THE VOCABULARY REQUIRES BOTH. A reading is a rectangle of pixels on a
// screen of one size; `name:` is what the condition reaches it by, `region:` is where, `region_at:`
// is on what. Leaving any of the three out makes the reading either unreachable or unscalable, and
// both were measured as gates that expired on a menu that was plainly on screen.
func TestEveryOCRStepNamesItsRegionAndSize(t *testing.T) {
	root := filepath.Join("..", "..")
	files := globStepsAny(root)
	checked := 0
	for _, path := range files {
		doc, err := Load(path, nil)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(root, path)
		var check func(any, string)
		check = func(raw any, where string) {
			m, ok := raw.(map[string]any)
			if !ok {
				return
			}
			if spec, ok := m["ocr"].(map[string]any); ok {
				checked++
				// `rect:` AND `rect_at:` ARE THE NAMES NOW - `region:`/`region_at:` meant the same
				// thing under two spellings, 245 uses between them, and one region should have one
				// name. The old spelling is still accepted here so a half-migrated tree is checked.
				for _, key := range []string{"name", "rect", "rect_at", "wait"} {
					if _, ok := spec[key]; !ok {
						if key == "rect" {
							if _, old := spec["region"]; old {
								continue
							}
						}
						if key == "rect_at" {
							if _, old := spec["region_at"]; old {
								continue
							}
						}
						t.Errorf("%s: %s has an `ocr:` with no %q. Every reading needs a name to be "+
							"asked about, a region to read, the size that region was measured at, "+
							"and a `wait:` that says how long it paused first", rel, where, key)
					}
				}
			}
			// a loop carries its reading under `ocr:` too
			if loop, ok := m["repeat"].(map[string]any); ok {
				check(loop, where+" (loop)")
			}
			for _, sub := range []string{"do", "actions"} {
				if list, ok := m[sub].([]any); ok {
					for _, item := range list {
						check(item, where+"."+sub)
					}
				}
			}
			if ru, ok := m["repeat_until"].(map[string]any); ok {
				check(ru, where+" (repeat_until)")
			}
		}
		for _, s := range doc.Steps {
			check(s.Spec, s.Name)
		}
	}
	t.Logf("checked %d `ocr:` reading(s)", checked)
}

// `repeat:` IS GONE, AND IT WAS NEVER ALIVE.
//
// FOURTEEN OF THEM ACROSS EIGHT FILES, a vocabulary entry describing "(count, settle) for a footer
// button pressed the same way N times", AND NO DISPATCHER BRANCH THAT READ IT. So
//
//   - name: walk the first-login pages to the end
//     at: [1464, 194]
//     repeat:
//   - 10
//   - 4
//
// PRESSED NOTHING AT ALL. The vocabulary test that requires every executable key to have a branch did
// not catch it because `repeat:` was never claimed as executable - it was only DECLARED, and a
// declared key with no branch is exactly the "step that reports success and does nothing" the
// vocabulary exists to refuse.
//
// A LOOP IS `repeat:`. A FIXED NUMBER OF PRESSES IS A `do:` LIST. Both are visible in the file, which
// `repeat: [10, 4]` never was - it could not even say WHICH presses it meant.
func TestThereIsNoLoopKey(t *testing.T) {
	root := filepath.Join("..", "..")
	files := globStepsAny(root)
	checked := 0
	for _, path := range files {
		doc, err := Load(path, nil)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(root, path)
		var walk func(any, string)
		walk = func(raw any, where string) {
			m, ok := raw.(map[string]any)
			if !ok {
				return
			}
			// `loop` WAS THE 1.0 NAME. `repeat` is the one statement now, and a document that still
			// holds `loop:` is a document compiled by the engine the migration replaced.
			if _, ok := m["loop"]; ok {
				t.Errorf("%s: %s still has `loop:`; the one loop is `repeat:`", rel, where)
			}
			for k, v := range m {
				switch t := v.(type) {
				case []any:
					for _, item := range t {
						walk(item, where+"."+k)
					}
				case map[string]any:
					walk(t, where+"."+k)
				}
			}
		}
		for _, s := range doc.Steps {
			checked++
			walk(s.Spec, s.Name)
		}
	}
	t.Logf("checked %d step(s)", checked)
}

// A RETRY IS A `repeat:`, NOT A `timeout:` ON A CONDITION.
//
// 43 STEPS USED TO HIDE A RETRY IN THE EXECUTOR: `until_any:` plus `timeout: 1800` plus `poll: 10`
// was a `for` loop in Go, so the file said WHAT it looked for and never HOW MANY TIMES it would look,
// nor that a failed read was swallowed and retried, nor that `keep_alive:` clicked the guest between
// attempts.
//
// THE LOOP IS EXPLICIT NOW, AND EVERY ATTEMPT IS VISIBLE:
//
//   - name: the recovery menu is up
//     loop:
//     max: 180                      <- how many times
//     do: [{wait: 10}]              <- what it waits, and any nudge, between attempts
//     ocr: {name: recovery, ...}    <- what it reads (a TEXT variable)
//     expect: {name: up, from: recovery, says_any: [...]}   <- the comparison (a BOOLEAN)
//     until: {fact: up}             <- the test, and nothing else
//
// SO `timeout:` ON A CONDITION IS REFUSED, because accepting it would silently restore the loop.
func TestAConditionDoesNotCarryATimeout(t *testing.T) {
	root := filepath.Join("..", "..")
	files := globStepsAny(root)
	checked := 0
	for _, path := range files {
		doc, err := Load(path, nil)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(root, path)
		var walk func(any, string)
		walk = func(raw any, where string) {
			m, ok := raw.(map[string]any)
			if !ok {
				return
			}
			cond := false
			for _, k := range []string{"until", "until_any", "expect", "expect_any", "unless", "unless_any"} {
				if _, ok := m[k]; ok {
					cond = true
				}
			}
			if cond {
				checked++
				// `until: {from: console}` IS THE ONE CONDITION THAT MAY CARRY `timeout:` AND
				// `poll:`, BECAUSE ITS SOURCE IS A FILE THAT GROWS. There is no "read it once" that
				// means anything for a serial log, so the poll is the operation rather than a hidden
				// retry around it - and the step says `from: console`, so a reader sees why.
				console := false
				if v, ok := m["from"]; ok {
					if str, ok := v.(string); ok && str == "console" {
						console = true
					}
				}
				for _, k := range []string{"timeout", "poll", "keep_alive"} {
					if console {
						break
					}
					if _, ok := m[k]; ok {
						t.Errorf("%s: %s has a condition with %q. A retry is a `repeat:` with "+
							"`max:`, `do:`, `ocr:`, `expect:` and `until: {fact: ...}` - a `timeout:` "+
							"on a condition is a hidden loop in the executor, where the file cannot "+
							"say how many times it looks", rel, where, k)
					}
				}
			}
			for k, v := range m {
				switch t := v.(type) {
				case []any:
					for _, item := range t {
						walk(item, where+"."+k)
					}
				case map[string]any:
					walk(t, where+"."+k)
				}
			}
		}
		for _, s := range doc.Steps {
			walk(s.Spec, s.Name)
		}
	}
	t.Logf("checked %d condition step(s)", checked)
}

// EVERY NAME A STEP HANDS TO THE ASSET LAYER MUST BE A KIND THAT EXISTS.
//
// MEASURED ON CI, ON `macos/15/steps/dev.yaml`, at the fourth stage of a chain that had already spent
// three hours baking a disk:
//
//	fetch the packages on the host: "dev_packages" is not an asset this build knows; the kinds are
//	  guest_agent_windows, guest_agent_macos, xcode, packages, virtio_win, answer_iso, windows_iso,
//	  recovery_macos, opencore, linux_iso
//
// The error names the right answer in the same sentence, which means the check that should have caught
// it did not exist: a walkthrough is DATA, so the Go compiler never sees the string, and the name only
// fails when a run reaches that step.
//
// THE TEST ASKS THE SOURCE OF TRUTH - the `Kind` constants in internal/asset - and then refuses any
// `asset:` value in any walkthrough that is not one of them. A file that names a PATH instead of a KIND
// is `boot:`'s `iso:` (which goes through `assetPath` and may be a path) and is not checked here.
func TestEveryAssetNameIsAKind(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "asset", "asset.go"))
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, m := range regexp.MustCompile(`(\w+)\s+Kind\s*=\s*"([a-z_]+)"`).FindAllStringSubmatch(string(src), -1) {
		kinds[m[2]] = true
	}
	if len(kinds) == 0 {
		t.Fatal("no asset kinds were found in internal/asset/asset.go, so this checks nothing")
	}

	// `**` is not expanded by filepath.Glob; walk the tree instead. BOTH FORMATS ARE READ: `.walk` is
	// the format on disk now, and the lines inside it write `fetch == opencore`.
	var files []string
	err = filepath.Walk(filepath.Join("..", ".."), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.Contains(path, "/steps/") {
			return nil
		}
		if !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".walk") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	checked := 0
	for _, path := range files {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(line)
			// `fetch:` IS THE OLD YAML SPELLING AND `fetch ==` THE `.walk` ONE; both are read so this
			// guardian sees whichever format a file is in.
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			// THREE SPELLINGS: `fetch: x` and `fetch == x` are the two older forms, and `fetch x` is
			// the 2.0 one - a key and its value on one line, which is what the tree holds now.
			var value string
			switch {
			case strings.HasPrefix(trimmed, "fetch:"), strings.HasPrefix(trimmed, "asset:"):
				value = strings.TrimSpace(strings.SplitN(trimmed, ":", 2)[1])
			case strings.HasPrefix(trimmed, "fetch =="), strings.HasPrefix(trimmed, "asset =="):
				value = strings.TrimSpace(strings.SplitN(trimmed, "==", 2)[1])
			case strings.HasPrefix(trimmed, "fetch "), strings.HasPrefix(trimmed, "asset "):
				value = strings.TrimSpace(strings.SplitN(trimmed, " ", 2)[1])
			default:
				continue
			}
			name := strings.Trim(strings.TrimSpace(value), `"'`)
			checked++
			// `guest_agent` IS A NAME THE ASSET LAYER RESOLVES, NOT A KIND IT STORES, and the
			// distinction is the whole reason this list has an exception.
			//
			// `internal/asset.Fetch` opens with
			//
			//	if kind == "guest_agent" {
			//		switch spec.Family {
			//		case "macos": kind = GuestAgentMacOS
			//		default:      kind = GuestAgentWindows
			//
			// so a file that says `asset: guest_agent` beside `family: macos` is asking for a real
			// asset and the suffix is resolved for it. The alternative - making every file repeat
			// `guest_agent_macos` - is the vocabulary leak that branch was written to remove.
			//
			// MEASURED: without this exception the test failed on macos/15/steps/vm-configure.yaml:41
			// and windows/11/steps/vm-configure.yaml:42, both of which are correct files.
			if name == "guest_agent" {
				continue
			}
			if !kinds[name] {
				t.Errorf("%s:%d names `asset: %s`, which is not a kind in internal/asset. The kinds are: %s",
					path, i+1, name, strings.Join(sortedKeys(kinds), ", "))
			}
		}
	}
	if checked == 0 {
		t.Fatal("no `fetch:` or `asset:` step was found in any walkthrough, so this checks nothing")
	}
	t.Logf("checked %d `asset:` name(s) against %d kind(s)", checked, len(kinds))
}

// sortedKeys is a stable ordering for the failure message.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// workflowAssets is every filename a workflow's `assets:` places in the work directory.
//
// IT IS THE THIRD PRODUCER OF A FILE A `scp:` CAN COPY, beside `fetch:` and `write:`, and it exists
// because THE WALKS NO LONGER DOWNLOAD ANYTHING. Every medium is pulled before `easyvm run` is called:
//
//	"assets": ["iso-ubuntu-desktop:ubuntu.iso"]                         an installer image
//	"assets": ["macos-asset-boot:boot.img", "macos-asset-rom:macos.rom"] OpenCore's firmware
//	"assets": ["macos-asset-xcode-26.3:Xcode_26.3_Universal.xip"]        the Xcode .xip
//
// The right-hand side of each pair is the name the walk reads, which is why this returns the NAMES and
// not the tags: a test about a `scp:` source cares what is on disk, not where it came from.
func workflowAssets(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	files, err := filepath.Glob(filepath.Join("..", "..", ".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatalf("cannot list workflows: %v", err)
	}
	list := regexp.MustCompile(`"assets":\s*\[([^\]]*)\]`)
	pair := regexp.MustCompile(`"[A-Za-z0-9._-]+:([A-Za-z0-9._/-]+)"`)
	for _, name := range files {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("cannot read %s: %v", name, err)
		}
		for _, m := range list.FindAllStringSubmatch(string(body), -1) {
			for _, e := range pair.FindAllStringSubmatch(m[1], -1) {
				out[e[1]] = true
			}
		}
	}
	return out
}

// EVERY STAGE THAT BOOTS FROM A FILE BEGINS BY CHECKING THAT FILE IS THERE.
//
// THE MEDIA ARE PULLED BEFORE `easyvm run` IS CALLED NOW - that move took an 18-minute download out of
// the middle of a stage (measured on run 43847000071, where it consumed the whole `wait for sshd`
// budget and reported a machine that never answered). It ALSO MOVED THE DIAGNOSIS: without this step a
// missing file fails at the first `boot:`, and that message names the boot rather than the pull:
//
//	boot: no the installer medium at /work/ubuntu.iso: stat: no such file or directory
//
// A `require:` step at the top names what is actually wrong, and it fails in the first second instead
// of after a live desktop and a screen recording.
//
// THE LIST IS DERIVED FROM THE WORKFLOWS, not written twice. A stage whose leg declares `assets:`
// must name those same files in its first step, so adding an asset to a workflow without adding it
// here is caught - and so is the reverse, which is the failure that would otherwise be silent until a
// guest could not boot.
func TestEveryStageRequiresTheAssetsItsLegPulls(t *testing.T) {
	root := filepath.Join("..", "..")
	assets := workflowAssetsByStage(t)
	if len(assets) == 0 {
		t.Fatal("no workflow declares `assets:`, so this test would pass by checking nothing")
	}

	// WHICH FILE A STAGE NAME RESOLVES TO. The workflow's stage name is `linux-ubuntu-install`, and
	// its file is `linux/ubuntu/steps/install.yaml`; both are in the same JSON object, so the pair is
	// read rather than guessed from the name.
	checked := 0
	for stage, files := range assets {
		if len(files) == 0 {
			continue
		}
		// THE WORKFLOW NAMES THE STAGE, and the file it resolves to may be either format: a `.walk`
		// wins over the `.yaml` it replaced.
		path := walkPathFor(root, stage)
		doc, err := Load(path, nil)
		if err != nil {
			// AN UPSTREAM `.yaml` THAT HAS NOT BEEN CONVERTED YET is skipped rather than failed: it
			// still writes `require:` and the old `read:` shape, which this vocabulary no longer
			// accepts. The list of those files is `*.yaml` WITHOUT a `.walk` beside it, and
			// `TestEveryWalkthroughLoads` reports them separately so the count stays honest.
			if strings.HasSuffix(path, ".yaml") && fileExistsNoWalk(root, stage) {
				continue
			}
			t.Errorf("%s: %v", stage, err)
			continue
		}
		if len(doc.Steps) == 0 {
			t.Errorf("%s: no steps", stage)
			continue
		}
		first := doc.Steps[0]
		// `needs:` IS THE NAME; `require:` WAS ONE LETTER FROM `requires:`, WHICH ASSERTS A
		// CAPABILITY RATHER THAN A FILE. Both are accepted here so an unmigrated file still passes
		// its own guardian test, and the vocabulary is what stops the old spelling being written.
		raw, ok := first.Spec["needs"]
		if !ok {
			raw, ok = first.Spec["require"]
		}
		if !ok {
			t.Errorf("%s: the first step is %q and it is not a `needs:`. The leg pulls %v, so a "+
				"missing one of those must fail here rather than at a `boot:` that names the medium "+
				"instead of the pull", stage, first.Name, files)
			continue
		}
		list, _ := raw.([]any)
		named := map[string]bool{}
		for _, item := range list {
			s, _ := item.(string)
			if i := strings.IndexByte(s, ':'); i >= 0 {
				s = s[:i]
			}
			named[s] = true
		}
		for _, f := range files {
			if !named[f] {
				t.Errorf("%s: the leg pulls %q and the first step's `require:` does not name it. A "+
					"file the workflow places and the stage never checks is one a truncated pull can "+
					"leave behind", stage, f)
			}
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no stage with assets was checked")
	}
	t.Logf("checked %d stage(s)", checked)
}

// workflowAssetsByStage maps each stage's FILE PATH to the filenames its leg pulls.
func workflowAssetsByStage(t *testing.T) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	files, err := filepath.Glob(filepath.Join("..", "..", ".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatalf("cannot list workflows: %v", err)
	}
	// `"file": "<path>"` and `"assets": [ ... ]` live in the same JSON object, so they are matched as
	// one span: everything from a `"file":` to the closing bracket of its `"assets":`.
	// THE SPAN MUST NOT CROSS A `}`: without that a stage with no `assets:` inherits the NEXT
	// stage's, which is how this test first reported nine Linux ssh legs as pulling an installer ISO
	// they never boot from.
	block := regexp.MustCompile(`"file":\s*"([^"]+)"[^}]*?"assets":\s*\[([^\]]*)\]`)
	pair := regexp.MustCompile(`"[A-Za-z0-9._-]+:([A-Za-z0-9._/-]+)"`)
	for _, name := range files {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("cannot read %s: %v", name, err)
		}
		for _, m := range block.FindAllStringSubmatch(string(body), -1) {
			var names []string
			for _, e := range pair.FindAllStringSubmatch(m[2], -1) {
				names = append(names, e[1])
			}
			if len(names) > 0 {
				out[m[1]] = names
			}
		}
	}
	return out
}

// globSteps finds a tree's walkthroughs in EITHER format.
//
// THE MIGRATION MADE THIS NECESSARY AND IT IS ALSO WHAT MAKES IT SAFE TO DO IN PIECES: a tree can hold
// `.yaml` files, `.walk` files, or both, and every guardian test has to see all of them. A glob that
// still said `*.yaml` would find nothing after the migration and go green about nothing - which is the
// failure `TestEveryWalkthroughLoads` reported as "only 36 walkthroughs found".
func globSteps(root, family string) []string {
	// A `.walk` WINS OVER THE `.yaml` IT REPLACED, so a tree mid-migration is tested in the format the
	// file is being migrated TO.
	//
	// THIS IS A TEMPORARY RULE AND IT IS NAMED AS ONE: the upstream `.yaml` files came back with the
	// merge and still use `ocr:` and the old `read:` shape, which this vocabulary no longer accepts.
	// They are kept until they are converted, and `TestEveryWalkthroughLoads` must not fail on a file
	// whose replacement is already there.
	seen := map[string]bool{}
	var out []string
	for _, ext := range []string{"*.walk", "*.yaml"} {
		m, err := filepath.Glob(filepath.Join(root, family, "*", "steps", ext))
		if err != nil {
			continue
		}
		for _, one := range m {
			stage := strings.TrimSuffix(filepath.Base(one), filepath.Ext(one))
			if seen[stage] {
				continue
			}
			seen[stage] = true
			out = append(out, one)
		}
	}
	sort.Strings(out)
	return out
}

// globStepsAny is `globSteps` across every OS family.
func globStepsAny(root string) []string {
	var out []string
	for _, family := range []string{"windows", "macos", "linux", "bsd", "android"} {
		out = append(out, globSteps(root, family)...)
	}
	sort.Strings(out)
	return out
}

// walkPathFor is the file a test should read when it names a walkthrough by its OLD `.yaml` name.
//
// A test that says `windows/11/steps/install.yaml` is naming a STAGE, not a file, and after the
// migration the file is `install.walk`. This resolves either.
func walkPathFor(root, rel string) string {
	// A NAME THAT IS ALREADY A `.walk` IS RETURNED AS IT IS, which the first version broke: it
	// appended `.walk` unconditionally and asked for `setup.walk.yaml`.
	base := strings.TrimSuffix(strings.TrimSuffix(rel, ".yaml"), ".walk")
	for _, ext := range []string{".walk", ".yaml"} {
		full := filepath.Join(root, base+ext)
		if _, err := os.Stat(full); err == nil {
			return full
		}
	}
	return filepath.Join(root, base+".yaml")
}

// A LOOP WITHOUT `until:` RUNS FOREVER, AND NOTHING BEFORE A GUEST IS STARTED SAYS SO.
//
// MEASURED. Adding page-arrival waits to cachyos cut each wait's `expect:` block short, and the
// `until: {fact: here}` on SEVEN of them went with it. The tree still loaded, `easyvm check` still
// printed all sixty steps, and `go test ./internal/walk/` was green - because `until:` is enforced
// in `internal/run/loop.go` when the loop RUNS:
//
//	error: the location page arrives after welcome: `repeat:` must say `until: {fact: <name>}` -
//	       the boolean a `expect:` step produced
//
// - which is step 18 of 60, three minutes into an install, on a machine that was already up. A
// loop with no `until:` presses or waits until the stage's timeout, so the cost of catching this
// late is the whole budget of every loop in the file.
//
// THE RULE IS CHEAP AND TOTAL: every `repeat:` names the boolean it ends on.
func TestEveryLoopSaysWhatEndsIt(t *testing.T) {
	root := filepath.Join("..", "..")
	// `globStepsAny` RATHER THAN A `.yaml` GLOB, so a mid-migration tree is read in the format each
	// file is being migrated TO.
	files := globStepsAny(root)

	loops := 0
	for _, path := range files {
		rel, _ := filepath.Rel(root, path)
		f, err := Load(path, nil)
		if err != nil {
			continue // TestEveryWalkthroughLoads reports the reason
		}
		for _, s := range f.Steps {
			if !s.Has("repeat") {
				continue
			}
			loops++
			inner, ok := s.Spec["repeat"].(map[string]any)
			if !ok {
				t.Errorf("%s: %q: `repeat:` is not a mapping", rel, s.Name)
				continue
			}
			if _, ok := inner["until"]; !ok {
				t.Errorf("%s: %q: `repeat:` has no `until:`, so it ends only when the stage times "+
					"out. Say `until: {fact: <name>}` naming the boolean its `expect:` binds",
					rel, s.Name)
			}
		}
	}
	if loops == 0 {
		t.Fatal("no `repeat:` found anywhere, so this test checked nothing")
	}
}

// `says` MATCHES WHOLE TOKENS, SO A TWO-WORD ENTRY IN `check: words:` CAN NEVER MATCH.
//
// PORTED FROM UPSTREAM (`20944e7`), WHICH FOUND IT IN TWO OF THE FILES THIS BRANCH CONVERTED:
//
//	linux/ubuntu/steps/ssh.yaml     "log in"        in the greeter-settled wait
//	bsd/freebsd/steps/install.yaml  "console type"  in the install-finished wait
//
// `says` builds a SET OF THE READING'S TOKENS and asks whether each wanted entry is in it, so
// `Select storage` looks for one token spelled "select storage", which no OCR produces. The result is
// a wait that can never end, and it reads like a page that never arrived - cachyos spent its whole
// partitions budget on a page that was on screen.
//
// BOTH ARE FIXED IN THIS BRANCH. The migration copied the phrase faithfully, because it was in the
// YAML; RUNNING them would have failed the same way upstream's did.
//
// THE FIX IS ALWAYS "NAME ONE WORD", never "loosen the comparison": a multi-word entry is a
// phrase-matching expectation this vocabulary does not have, and inventing it would change what every
// existing single-word entry means.
func TestNoCheckNamesAPhrase(t *testing.T) {
	root := filepath.Join("..", "..")
	files := globStepsAny(root)
	if len(files) == 0 {
		t.Fatal("no walkthrough found, so this test checked nothing")
	}

	checks := 0
	visit := func(rel, name string, check map[string]any) {
		checks++
		raw, ok := check["words"].([]any)
		if !ok {
			return
		}
		// `says` takes a flat list of words; `says_any` takes a list of LISTS, one word-set each.
		for _, entry := range raw {
			switch v := entry.(type) {
			case string:
				if strings.Contains(v, " ") {
					t.Errorf("%s: %q: `check: words: %q` is a PHRASE and can never match - `says` "+
						"compares whole tokens. Name one word", rel, name, v)
				}
			case []any:
				for _, inner := range v {
					if s, ok := inner.(string); ok && strings.Contains(s, " ") {
						t.Errorf("%s: %q: `check: words:` set entry %q is a PHRASE and can never "+
							"match - `says` compares whole tokens. Name one word", rel, name, s)
					}
				}
			}
		}
	}

	for _, path := range files {
		rel, _ := filepath.Rel(root, path)
		f, err := Load(path, nil)
		if err != nil {
			continue // TestEveryWalkthroughLoads reports the reason
		}
		for _, s := range f.Steps {
			if c, ok := s.Spec["check"].(map[string]any); ok {
				visit(rel, s.Name, c)
			}
			if lp, ok := s.Spec["repeat"].(map[string]any); ok {
				if c, ok := lp["until"].(map[string]any); ok {
					visit(rel, s.Name, c)
				}
			}
		}
	}
	if checks == 0 {
		t.Fatal("no `check:` found anywhere, so this test checked nothing")
	}
	t.Logf("checked %d `check:` block(s)", checks)
}

// stepsOf turns the raw list inside a `loop: do:` or an `if: then:` into Steps, so a scan can descend
// into them. A step inside a branch is a step.
func stepsOf(raw []any) []Step {
	out := make([]Step, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			out = append(out, Step{Spec: m})
		}
	}
	return out
}

// A COORDINATE OUTSIDE THE FRAME THE FILE DECLARES WAS MEASURED ON ANOTHER SCREEN.
//
// PORTED FROM UPSTREAM (`aac5fc0`). The daemon used to find this at step 6 of 29, on a guest, with the
// machine already booted - and the message named the click rather than the arithmetic. Here it is a
// parse-time failure with the file and step in it.
//
// IT IS THE SAME CLASS OF BUG AS THE `rect_at:` ONE THIS BRANCH FIXED: a number written for one screen
// read on another. That one was a region; this one is a point.
func TestEveryCoordinateFitsTheFrameItDeclares(t *testing.T) {
	root := filepath.Join("..", "..")
	files := globStepsAny(root)

	// THE KEYS THAT NAME A POINT, and how many leading ints are a point. `at_button` is a RECTANGLE -
	// [x0, y0, x1, y1] - so its last two values are the far corner and are checked too.
	points := map[string]int{"click": 2, "at": 2, "double": 2, "at_fallback": 2, "at_color": 2}

	checked := 0
	for _, path := range files {
		rel, _ := filepath.Rel(root, path)
		f, err := Load(path, nil)
		if err != nil {
			continue // TestEveryWalkthroughLoads reports the reason
		}
		w, h := f.Resolution[0], f.Resolution[1]
		if w == 0 || h == 0 {
			continue
		}
		var scan func(steps []Step)
		scan = func(steps []Step) {
			for _, s := range steps {
				for key := range points {
					v, ok := s.Spec[key].([]any)
					if !ok || len(v) < 2 {
						continue
					}
					x, okx := v[0].(int)
					y, oky := v[1].(int)
					if !okx || !oky {
						continue
					}
					checked++
					if x >= w || y >= h || x < 0 || y < 0 {
						t.Errorf("%s: %q: `%s: [%d, %d]` is outside the %dx%d frame this file declares. "+
							"A coordinate past an edge was measured on another screen",
							rel, s.Name, key, x, y, w, h)
					}
				}
				// `at_button: [x0, y0, x1, y1]` - the SECOND pair is a corner, not a point.
				if v, ok := s.Spec["at_button"].([]any); ok && len(v) == 4 {
					for _, pair := range [][2]int{{0, 1}, {2, 3}} {
						x, okx := v[pair[0]].(int)
						y, oky := v[pair[1]].(int)
						if !okx || !oky {
							continue
						}
						if x >= w || y >= h {
							t.Errorf("%s: %q: `at_button: %v` reaches (%d, %d), outside the %dx%d "+
								"frame this file declares", rel, s.Name, v, x, y, w, h)
						}
					}
				}
				// And the steps inside a loop or a branch are steps too.
				if lp, ok := s.Spec["repeat"].(map[string]any); ok {
					if inner, ok := lp["do"].([]any); ok {
						scan(stepsOf(inner))
					}
				}
				for _, key := range []string{"then", "else"} {
					if inner, ok := s.Spec[key].([]any); ok {
						scan(stepsOf(inner))
					}
				}
			}
		}
		scan(f.Steps)
	}
	if checked == 0 {
		t.Fatal("no coordinate found anywhere, so this test checked nothing")
	}
	t.Logf("checked %d coordinate(s)", checked)
}

// A LOOP WAITING ON A WORD THAT CANNOT APPEAR IS A FAILURE WITH A DELAY.
//
// PORTED FROM UPSTREAM (`35110ae`). `max:` is a CEILING ON ATTEMPTS, not a count: a loop repeats until
// its condition holds and gives up after `max` tries. So a `zzzz`-style sentinel written to mean "do
// this a few times" is a loop that always fails, and it reads as a page that never arrived.
func TestNoLoopWaitsOnAWordThatCannotAppear(t *testing.T) {
	root := filepath.Join("..", "..")
	files := globStepsAny(root)
	if len(files) == 0 {
		t.Fatal("no walkthrough found, so this test checked nothing")
	}
	loops := 0
	for _, path := range files {
		rel, _ := filepath.Rel(root, path)
		f, err := Load(path, nil)
		if err != nil {
			continue
		}
		for _, s := range f.Steps {
			lp, ok := s.Spec["repeat"].(map[string]any)
			if !ok {
				continue
			}
			check, ok := lp["until"].(map[string]any)
			if !ok {
				continue
			}
			loops++
			raw, ok := check["words"].([]any)
			if !ok {
				continue
			}
			for _, entry := range raw {
				switch v := entry.(type) {
				case string:
					if strings.HasPrefix(strings.ToLower(v), "zzzz") {
						t.Errorf("%s: %q: the loop waits on %q, which exists only to be unmatched - "+
							"a loop whose condition can never hold is a failure with a delay", rel, s.Name, v)
					}
				case []any:
					for _, inner := range v {
						if tok, ok := inner.(string); ok && strings.HasPrefix(strings.ToLower(tok), "zzzz") {
							t.Errorf("%s: %q: the loop waits on %q, which exists only to be unmatched - "+
								"a loop whose condition can never hold is a failure with a delay",
								rel, s.Name, tok)
						}
					}
				}
			}
		}
	}
	if loops == 0 {
		t.Fatal("no loop found anywhere, so this test checked nothing")
	}
	t.Logf("checked %d loop(s)", loops)
}

// globBoth finds one stage in EITHER format, a `.walk` winning over the `.yaml` it replaced.
//
// IT EXISTS BECAUSE THE GUARDIAN TESTS NAMED THEIR SUBJECT BY FILE NAME - `steps", "install.yaml"` -
// and after the migration the file is `install.walk`. A glob that still said `.yaml` would find the
// UPSTREAM file the merge brought back and test the wrong one, or find nothing at all.
//
// THE `.yaml` BRANCH IS TEMPORARY AND NAMED AS ONE: the upstream files came back with the merge and
// still write `ocr:` and the old `read:` shape, which this vocabulary no longer accepts. Until each is
// converted, a test must read the `.walk` beside it.
func globBoth(root, family, stage string) []string {
	for _, ext := range []string{".walk", ".yaml"} {
		p := filepath.Join(root, family, "*", "steps", stage+ext)
		if m, err := filepath.Glob(p); err == nil && len(m) > 0 {
			return m
		}
	}
	return nil
}

// fileExistsNoWalk reports whether a stage has ONLY the `.yaml`, with no `.walk` beside it.
//
// IT IS THE TEST FOR "NOT MIGRATED YET", and it is deliberately narrow: a stage whose `.walk` exists
// is tested through that file, so a `.yaml` next to it is not a reason to skip anything.
func fileExistsNoWalk(root, stage string) bool {
	base := strings.TrimSuffix(strings.TrimSuffix(stage, ".yaml"), ".walk")
	if _, err := os.Stat(filepath.Join(root, base+".walk")); err == nil {
		return false
	}
	_, err := os.Stat(filepath.Join(root, base+".yaml"))
	return err == nil
}

// stopMethod is how a step stops the machine, whichever spelling the document holds.
func stopMethod(s Step) string {
	if m := s.Get("method"); m != "" {
		return m
	}
	return s.Get("shutdown")
}
