package walk

import (
	"fmt"
	"os"
	"path/filepath"
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
	"at":         "one click",
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
	var files []string
	for _, dir := range []string{"windows/11/steps", "macos/15/steps"} {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".yaml") {
				files = append(files, filepath.Join(root, dir, e.Name()))
			}
		}
	}
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
	for _, name := range []string{"ssh.yaml", "sequoia.yaml"} {
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
				if _, ok := m["at"]; ok {
					clicks++
					// FOUR SECONDS, NOT TWO. Two was measured short on a loaded guest.
					if settle, ok := toFloat(m["wait_after"]); !ok || settle < 4 {
						t.Fatalf("%s: a Quit click waits %v seconds after it; the measured gap is 4 "+
							"(run 42162859846)", name, m["wait_after"])
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
	var files []string
	for _, dir := range []string{"windows/11/steps", "macos/15/steps"} {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".yaml") {
				files = append(files, filepath.Join(root, dir, e.Name()))
			}
		}
	}
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
	patterns := []string{
		filepath.Join(root, "windows", "*", "steps", "*.yaml"),
		filepath.Join(root, "macos", "*", "steps", "*.yaml"),
		filepath.Join(root, "linux", "*", "steps", "*.yaml"),
		filepath.Join(root, "bsd", "*", "steps", "*.yaml"),
	}
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
	if len(files) < 50 {
		t.Errorf("only %d walkthroughs found; the tree this port migrated has 52. A glob that "+
			"stopped matching is a green test about nothing", len(files))
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
	for _, pat := range []string{
		filepath.Join(root, "windows", "*", "steps", "*.yaml"),
		filepath.Join(root, "macos", "*", "steps", "*.yaml"),
		filepath.Join(root, "linux", "*", "steps", "*.yaml"),
		filepath.Join(root, "bsd", "*", "steps", "*.yaml"),
	} {
		matches, err := filepath.Glob(pat)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, matches...)
	}
	if len(files) == 0 {
		t.Skip("no walkthroughs here")
	}

	// A STEP THAT TOUCHES THE GUEST. `fetch:`, `write:` and `disk_gb:` are HOST work and need no
	// machine; everything here does.
	needsGuest := map[string]bool{
		"at": true, "at_button": true, "at_window": true, "at_bottom": true, "double": true,
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
	for _, pat := range []string{
		filepath.Join(root, "windows", "*", "steps", "*.yaml"),
		filepath.Join(root, "macos", "*", "steps", "*.yaml"),
		filepath.Join(root, "linux", "*", "steps", "*.yaml"),
		filepath.Join(root, "bsd", "*", "steps", "*.yaml"),
	} {
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
	files, err := filepath.Glob(filepath.Join(root, "linux", "*", "steps", "ssh.yaml"))
	if err != nil {
		t.Fatal(err)
	}
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
			if s.Has("require_ssh") {
				waits = true
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
	for _, pattern := range []string{
		filepath.Join(root, "*", "*", "steps", "*.yaml"),
		filepath.Join(root, "*", "steps", "*.yaml"),
	} {
		files, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
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
	files, err := filepath.Glob(filepath.Join(root, "windows", "*", "steps", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
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
	files, err := filepath.Glob(filepath.Join(root, "*", "*", "steps", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Skip("no walkthroughs here")
	}

	// THE HOST NAMES, taken from the code that writes them. A `fetch:` puts its outputs in the work
	// directory at these paths; a `write:` names its own file.
	produced := map[string]bool{
		"qemu-ga-x86_64.msi":     true, // fetch: guest_agent / guest_agent_windows
		"vioserial/vioser.inf":   true,
		"vioserial/vioser.sys":   true,
		"vioserial/vioser.cat":   true,
		"bootstrap-launcher.ps1": true, // write: launcher, on a windows family
		"bootstrap-launcher.sh":  true, // write: launcher, on linux/macos
		"bootstrap-install.ps1":  true, // write: windows_task
		"org.vm.bootstrap.plist": true, // write: macos_agent_plist
		"org.vm.smbmount.plist":  true, // write: macos_mount_plist
		"vm-smbmount.sh":         true, // write: macos_mount_script
		"smb.conf":               true, // write: smb_conf
		"smbnetfs.conf":          true, // write: smb_conf, as its SECOND file
		"mac-guest-agent":        true, // fetch: guest_agent_macos
		// `primitives.yaml` IS NOT A STAGE ANY WORKFLOW RUNS - it is the vocabulary's own catalogue,
		// exercising `write: demo_script`, which is declared and not implemented. A source it names is
		// checked against the writer's own contract, not against a fetch.
		"demo.sh": true, // write: demo_script (implemented in primitives.yaml's own stage)
	}

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
	files, err := filepath.Glob(filepath.Join(root, "windows", "*", "steps", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
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
	files, err := filepath.Glob(filepath.Join(root, "linux", "*", "steps", "install.yaml"))
	if err != nil {
		t.Fatal(err)
	}
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

		// A PANEL, NOT A WORD. Every image here is a DESKTOP image and the step after this one clicks
		// something on that desktop, so the question is "is a live session up" - which is what a panel
		// answers and what a word does not.
		//
		// THIS WAS WRITTEN THE OTHER WAY FOR FIVE FILES WHEN `iso-ubuntu` TURNED OUT TO BE THE SERVER
		// IMAGE (2,792 MiB, "Try or Install Ubuntu Server", an installer with no panel). THE IMAGES ARE
		// DESKTOP ONES NOW - `iso-ubuntu-desktop` is 6,182 MiB, Desktop 26.04.1 exactly - so the panel
		// is the honest measurement again, and the same correction applied to nixos (minimal has no
		// desktop at all), popos, manjaro and zorin.
		if wait.Get("desktop") != "linux" {
			t.Errorf("%s: %s waits on %q rather than `desktop: linux`. These are desktop images and "+
				"the next step clicks a control on the session, so the wait must ask whether a session "+
				"is up", rel, wait.Name, wait.Get("desktop"))
		}
		// AND THE SESSION IS KEPT AWAKE. This wait can run for ninety minutes, and a live session's
		// idle timer puts a lock screen up well before that - after which nothing below can match.
		// The reference implementation says the same: "a real click, not a pointer move, because the
		// session's idle timer is what puts the lock screen up".
		if !wait.Has("keep_alive") {
			t.Errorf("%s: %s does not set `keep_alive: true`, and this wait runs long enough for "+
				"the live session to lock its screen", rel, wait.Name)
		}
		// AND THE BUDGET IS THE INSTALL'S. A live medium unpacks itself from a multi-gigabyte ISO
		// before its desktop exists; a fifteen-minute number was a guess that was wrong four times.
		if got := wait.Get("timeout"); got != "5400" {
			t.Errorf("%s: %s waits %q seconds; a live medium unpacks itself before its desktop "+
				"exists and the reference budget is 5400", rel, wait.Name, got)
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
	path := filepath.Join(root, "linux", "nixos", "steps", "vm-configure.yaml")
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
	files, err := filepath.Glob(filepath.Join(root, "linux", "*", "steps", "vm-configure.yaml"))
	if err != nil {
		t.Fatal(err)
	}
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
	files, err := filepath.Glob(filepath.Join(root, "linux", "*", "steps", "ssh.yaml"))
	if err != nil {
		t.Fatal(err)
	}
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
	var files []string
	for _, pat := range []string{
		filepath.Join(root, "linux", "*", "steps", "*.yaml"),
		filepath.Join(root, "windows", "*", "steps", "*.yaml"),
		filepath.Join(root, "bsd", "*", "steps", "*.yaml"),
		filepath.Join(root, "macos", "*", "steps", "*.yaml"),
	} {
		got, err := filepath.Glob(pat)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, got...)
	}
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
	files, err := filepath.Glob(filepath.Join(root, "linux", "*", "steps", "install.yaml"))
	if err != nil {
		t.Fatal(err)
	}
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
		filepath.Join(root, "linux", "*", "steps", "install.yaml"),
		filepath.Join(root, "bsd", "*", "steps", "install.yaml"),
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
			if strings.Contains(s.Get("compress"), "out") {
				publishes = true
			}
		}
		if !publishes {
			t.Errorf("%s has no `compress:` step, so `--out` writes nothing and the workflow's "+
				"`\"out\"` file is never created - measured on zorin-install, whose `-disk` artifact "+
				"was 518 bytes while its `-vm-disk` was 10 GiB", rel)
		}
		// AND THE VARIABLE IT NAMES IS DECLARED.
		if _, ok := doc.Vars["out"]; !ok {
			t.Errorf("%s has a `compress:` naming `{{ out }}` but declares no `out` variable, so the "+
				"engine cannot resolve the path", rel)
		}
	}
	if checked == 0 {
		t.Skip("no install walkthroughs loaded")
	}
	t.Logf("checked %d install walkthrough(s)", checked)
}

// A `compress:` BEFORE A `shutdown:` CANNOT WORK, BECAUSE QEMU HOLDS THE DISK OPEN.
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
		filepath.Join(root, "linux", "*", "steps", "install.yaml"),
		filepath.Join(root, "bsd", "*", "steps", "install.yaml"),
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
			if s.Has("shutdown") {
				shutdownAt = i
			}
			if s.Has("compress") {
				compressAt = i
			}
		}
		if compressAt < 0 {
			t.Errorf("%s has no `compress:` step, so `--out` writes nothing", rel)
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
		shutdown := doc.Steps[shutdownAt].Get("shutdown")
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
	files, err := filepath.Glob(filepath.Join(root, "*", "*", "steps", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
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

// A `poll:` ON A `record:` STEP IS DEAD CONFIGURATION, AND IT MISLEADS.
//
// `doRecord` NEVER READS `poll:`; the sampler ticks once a second in `publish.go`. But every
// walkthrough carried `record: ... poll: 20`, so a reader - and this session's own investigation -
// took 20 seconds per frame as fact and tried to explain a 909-frame video of a 20-minute run as a
// 20-second cadence rather than as a quarter of the ticks being dropped by a failed screendump.
//
// THE TICK IS ONE SECOND AND THE MISSES ARE NOW COUNTED. Neither was visible while the file said
// `poll: 20`, so the key is refused rather than ignored.
func TestARecordStepDoesNotCarryAPoll(t *testing.T) {
	root := filepath.Join("..", "..")
	files, err := filepath.Glob(filepath.Join(root, "*", "*", "steps", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, path := range files {
		doc, err := Load(path, nil)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(root, path)
		for _, s := range doc.Steps {
			if !s.Has("record") {
				continue
			}
			checked++
			if s.Has("poll") {
				t.Errorf("%s: %q is a `record:` step and carries `poll:`. The sampler ticks once a "+
					"second and `doRecord` never reads `poll:`, so the value is dead - and a reader "+
					"takes it as the frame interval, which is how a 909-frame video of a 20-minute "+
					"run was read as a 20-second cadence", rel, s.Name)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no `record:` steps were found, so this test checks nothing")
	}
	t.Logf("checked %d record step(s)", checked)
}

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
	files, err := filepath.Glob(filepath.Join(root, "*", "*", "steps", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
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
	files, err := filepath.Glob(filepath.Join(root, "windows", "*", "steps", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
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
	files, err := filepath.Glob(filepath.Join(root, "*", "*", "steps", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
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
			if s.Has("compress") {
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
				t.Errorf("%s has no `compress:` step, so its workflow publishes a disk that is "+
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
	files, err := filepath.Glob(filepath.Join(root, "*", "*", "steps", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
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
	files, err := filepath.Glob(filepath.Join(root, "windows", "*", "steps", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
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
		{"windows/11/steps/install.yaml"},
	}
	for _, c := range cases {
		doc, err := Load(filepath.Join(root, c.path), nil)
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
	files, err := filepath.Glob(filepath.Join(root, "*", "*", "steps", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, path := range files {
		doc, err := Load(path, nil)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(root, path)
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

// GHOSTBSD NEEDS THE LOADER VARIABLE, OR EVERY CLICK LANDS IN THE TOP-LEFT CORNER.
//
// THE MEASUREMENT IS THE PYTHON IMPLEMENTATION'S, AND IT IS A KERNEL FACT RATHER THAN A GUESS:
//
//	ugen0.2: <QEMU Tablet Adomax Technology Co., Ltd> at usbus0
//	uhid0:   <QEMU QEMU USB Tablet, class 0/0, rev 2.00/2.00, addr 1> on usbus0
//
// THE TABLET IS ATTACHED - TO uhid(4). It reports HID subclass 0 / protocol 0, the non-boot
// protocol; ums(4) accepts only subclass 1 / protocol 2, declines it, and uhid takes the raw
// reports without ever writing a position into the input layer. THE POINTER THEN SITS AT THE
// TOP-LEFT CORNER whatever coordinate the host sends, so every `at:` in the walkthrough presses
// the same pixel and the run produces a 0 MiB disk.
//
// ONE LOADER VARIABLE FIXES IT, AND IT HAS TO BE THE LOADER: `sysctl hw.usb.usbhid.enable=1`
// from a root shell changes the value and does nothing, because the sysctl is read when a device
// is enumerated and this one is already open by uhid.
//
//	sett hw.usb.usbhid.enable=1
//	boot
//
// The port dropped this step entirely, which means the walkthrough could never have worked even
// if the desktop had appeared.
func TestGhostBSDSetsTheLoaderVariableThatMakesClicksWork(t *testing.T) {
	root := filepath.Join("..", "..")
	path := filepath.Join(root, "bsd", "ghostbsd", "steps", "gbi.yaml")
	doc, err := Load(path, nil)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	sets := false
	for _, s := range doc.Steps {
		body := s.Get("type")
		if strings.Contains(body, "hw.usb.usbhid.enable=1") {
			sets = true
			break
		}
	}
	if !sets {
		t.Fatal("bsd/ghostbsd/steps/gbi.yaml never sets `hw.usb.usbhid.enable=1` in the loader. " +
			"Without it usb-tablet is claimed by uhid(4), ums(4) declines it, and the pointer sits " +
			"in the top-left corner - so every `at:` presses the same pixel and the install writes " +
			"nothing. It must be the LOADER, not a runtime sysctl: the value is read at enumeration")
	}
	// AND THE PROMPT IS CONFIRMED BEFORE ANYTHING IS TYPED INTO IT. Typing the command into the
	// BOOT MENU instead means its characters are read as hotkeys - the string contains a `1`,
	// which selects another boot entry.
	confirmed := false
	for _, s := range doc.Steps {
		// THE GUARD IS A `loop:` WITH AN `ocr:` AND A `check:` NOW, and it used to be
		// `repeat_until: until_any:`. Both spellings are read here because this test's question is
		// "does something confirm the prompt", not "which key spells it".
		sets, _ := s.Spec["until_any"].([]any)
		if inner, ok := s.Spec["repeat_until"].(map[string]any); ok {
			if nested, ok := inner["until_any"].([]any); ok {
				sets = nested
			}
		}
		if inner, ok := s.Spec["loop"].(map[string]any); ok {
			if chk, ok := inner["check"].(map[string]any); ok {
				if nested, ok := chk["words"].([]any); ok {
					sets = nested
				}
			}
		}
		for _, raw := range sets {
			words, _ := raw.([]any)
			var parts []string
			for _, w := range words {
				if str, ok := w.(string); ok {
					parts = append(parts, str)
				}
			}
			joined := strings.Join(parts, " ")
			if strings.Contains(joined, "detailed help") || strings.Contains(joined, "list of commands") {
				confirmed = true
			}
		}
	}
	if !confirmed {
		t.Error("nothing confirms the loader prompt is up before the setting is typed. The boot " +
			"menu reads characters as hotkeys - the command contains a `1`, which selects another " +
			"boot entry - so the guard is not a formality")
	}
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
// THE GATE IS NOW A `loop:` WITH ITS THREE PARTS WRITTEN DOWN - a wait, an `ocr:` reading, and a
// condition - so the rectangle and its enlargement are beside the reading rather than inside the
// condition that uses them.
func TestTheMacOSRecoveryGateAcceptsReadableRows(t *testing.T) {
	path := filepath.Join("..", "..", "macos", "15", "steps", "setup.yaml")
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
	loop, ok := gate.Spec["loop"].(map[string]any)
	if !ok {
		t.Fatal("the recovery gate is not a `loop:`. The three parts - the wait, the `ocr:` reading " +
			"and the condition - have to be visible, because the reading's region and enlargement " +
			"are what make it work at 640x480")
	}
	ocr, ok := loop["ocr"].(map[string]any)
	if !ok {
		t.Fatal("the loop has no `ocr:` - a condition with no reading reads the screen itself")
	}
	if scale, _ := toFloat(ocr["scale"]); scale < 6 {
		t.Errorf("the reading crops at scale %.0f. Measured on the real 640x480 frame: at scale 6 "+
			"four rows read, at scale 2 only two", scale)
	}
	until, ok := loop["until"].(map[string]any)
	if !ok {
		t.Fatal("the loop has no `until:`")
	}
	// `until:` TESTS A BOOLEAN AND NOTHING ELSE - the comparison belongs to `check:`.
	if _, ok := until["fact"]; !ok {
		t.Error("`until:` is not `{fact: ...}`. A loop's stop test must be a boolean reference: " +
			"when it also carried the word-sets it both named a reading and decided what it " +
			"meant, so \"the text was wrong\" and \"stop the loop\" were one expression")
	}
	check, ok := loop["check"].(map[string]any)
	if !ok {
		t.Fatal("the loop has no `check:` - the comparison that turns the reading into a boolean")
	}
	if mode, _ := check["mode"].(string); mode != "says_any" {
		t.Errorf("the check's mode is %q, want says_any - the recovery menu is four labelled rows "+
			"and any one of them is enough", mode)
	}
	sets, _ := check["words"].([]any)
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
	files, err := filepath.Glob(filepath.Join(root, "*", "*", "steps", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, path := range files {
		doc, err := Load(path, nil)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(root, path)
		for _, s := range doc.Steps {
			if !s.Has("region") {
				continue
			}
			checked++
			if !s.Has("region_at") {
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
	files, err := filepath.Glob(filepath.Join(root, "*", "*", "steps", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
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
				for _, key := range []string{"name", "region", "region_at", "wait"} {
					if _, ok := spec[key]; !ok {
						t.Errorf("%s: %s has an `ocr:` with no %q. Every reading needs a name to be "+
							"asked about, a region to read, the size that region was measured at, "+
							"and a `wait:` that says how long it paused first", rel, where, key)
					}
				}
			}
			// a loop carries its reading under `ocr:` too
			if loop, ok := m["loop"].(map[string]any); ok {
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
// A LOOP IS `loop:`. A FIXED NUMBER OF PRESSES IS A `do:` LIST. Both are visible in the file, which
// `repeat: [10, 4]` never was - it could not even say WHICH presses it meant.
func TestThereIsNoRepeatKey(t *testing.T) {
	root := filepath.Join("..", "..")
	files, err := filepath.Glob(filepath.Join(root, "*", "*", "steps", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
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
			if _, ok := m["repeat"]; ok {
				t.Errorf("%s: %s still has `repeat:`. It had no implementation - the presses it "+
					"described never happened - so it is removed from the vocabulary. Use `loop:` "+
					"for a loop and a `do:` list for a fixed number of presses", rel, where)
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

// A RETRY IS A `loop:`, NOT A `timeout:` ON A CONDITION.
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
//     check: {name: up, from: recovery, says_any: [...]}   <- the comparison (a BOOLEAN)
//     until: {fact: up}             <- the test, and nothing else
//
// SO `timeout:` ON A CONDITION IS REFUSED, because accepting it would silently restore the loop.
func TestAConditionDoesNotCarryATimeout(t *testing.T) {
	root := filepath.Join("..", "..")
	files, err := filepath.Glob(filepath.Join(root, "*", "*", "steps", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
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
				for _, k := range []string{"timeout", "poll", "keep_alive"} {
					if _, ok := m[k]; ok {
						t.Errorf("%s: %s has a condition with %q. A retry is a `loop:` with "+
							"`max:`, `do:`, `ocr:`, `check:` and `until: {fact: ...}` - a `timeout:` "+
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
