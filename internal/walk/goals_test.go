package walk

import (
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
					if settle, ok := toFloat(m["settle"]); !ok || settle < 4 {
						t.Fatalf("%s: a Quit click settles %v seconds; the measured gap is 4 "+
							"(run 42162859846)", name, m["settle"])
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
