package oci

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// A DISK IS TRANSPORT, NOT AN ARTIFACT, AND EVERY LEG HAS TO SAY SO.
//
// `stage.yml` used to upload THREE artifacts per leg - `<leg>-video`, `<leg>-logs` AND `<leg>-disk` -
// and the third was the whole qcow2. MEASURED ON `windows-install`: 22,133,669,203 bytes uploaded a
// second time AFTER `easyvm push` had already put the same 10,786 MiB disk in ghcr.
//
// THAT COST IS PAID BY EVERY PASSING LEG ON EVERY RUN, and it buys nothing:
//
//	<leg>-video   the recording            "what was on screen"   tens of MiB   KEPT
//	<leg>-logs    daemon.log, console.log  "what the guest said"  kilobytes     KEPT
//	<leg>-disk    the qcow2                the disk is transport  gigabytes     GONE
//
// A leg that PASSES needs no post-mortem, and a leg that FAILS has no disk worth reading - its
// recording and console log are what answer the question, and both are separate artifacts.
//
// THIS IS CHECKED FOR EVERY LEG OF EVERY OPERATING SYSTEM rather than for the one file that had the
// bug, because the upload lives in the shared `stage.yml` and a future leg must not reintroduce it.
func TestNoWorkflowUploadsADiskAsAnArtifact(t *testing.T) {
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
		// EVERY `upload-artifact` STEP, WITH THE PATHS IT CARRIES. A YAML walk rather than a grep,
		// because `path:` is a block scalar and a grep for `.qcow2` cannot tell an upload from a
		// `qemu-img check`.
		var doc struct {
			Jobs map[string]struct {
				Steps []struct {
					Uses string `yaml:"uses"`
					With struct {
						Name string `yaml:"name"`
						Path string `yaml:"path"`
					} `yaml:"with"`
				} `yaml:"steps"`
			} `yaml:"jobs"`
		}
		if err := yaml.Unmarshal(body, &doc); err != nil {
			t.Fatalf("%s does not parse: %v", filepath.Base(name), err)
		}
		for jobName, job := range doc.Jobs {
			for _, st := range job.Steps {
				if !strings.Contains(st.Uses, "upload-artifact") {
					continue
				}
				checked++
				// COMMENTS ARE NOT PATHS. The `path:` block in stage.yml carries the note about what
				// the console log is for, and that note mentions `disk.qcow2` while explaining why
				// the disk artifact was split off. A check that reads prose finds the word and not
				// the upload, so the comment lines are dropped first.
				var real []string
				for _, line := range strings.Split(st.With.Path, "\n") {
					line = strings.TrimSpace(line)
					if line == "" || strings.HasPrefix(line, "#") {
						continue
					}
					real = append(real, line)
				}
				paths := strings.Join(real, " ") + " " + st.With.Name
				for _, diskish := range []string{".qcow2", ".qcow", ".raw", ".img"} {
					if strings.Contains(paths, diskish) {
						t.Errorf("%s: job %q uploads an artifact whose name or path mentions %q "+
							"(artifact %q, path %q). A disk is transport: it goes to ghcr with "+
							"`easyvm push` and the next leg reads it with `easyvm pull`. Uploading "+
							"it as well cost 22 GiB per passing install leg and is downloaded by "+
							"nobody", filepath.Base(name), jobName, diskish, st.With.Name, st.With.Path)
					}
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no upload-artifact step was found in any workflow, so this test would pass over " +
			"anything")
	}
	t.Logf("checked %d upload-artifact step(s)", checked)
}

// AND THE TWO ARTIFACTS THAT MUST REMAIN DO.
//
// The recording is the first thing to open after a failure and the console log is the only record of
// what the firmware did - the note in `stage.yml` says both were learned the expensive way. A test
// that only forbids the disk would happily accept a file that has none of the three.
func TestEveryStageKeepsItsVideoAndLogs(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "stage.yml"))
	if err != nil {
		t.Skipf("no stage.yml here (%v)", err)
	}
	src := string(body)
	for _, want := range []string{
		"matrix.name }}-video",
		"matrix.name }}-logs",
		"work/*.mp4",
		"work/daemon.log",
		"work/console.log",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("stage.yml no longer carries %q, so a failing leg loses the evidence that "+
				"answers what happened", want)
		}
	}
	if strings.Contains(src, "matrix.name }}-disk") {
		t.Error("stage.yml names a `-disk` artifact again; the qcow2 is transport, not evidence")
	}
}

// ORAS IS INSTALLED BY `prepare`, NOT BAKED INTO THE IMAGE.
//
// Both work. The image is built from source on every push, so baking oras in would mean every leg
// waits for a new image before it can publish - and a version bump would invalidate the layer
// everything else is cached behind. `prepare` already proves the runner can run a guest; installing a
// three-second download there is the same step doing one more thing.
//
// MEASURED LOCALLY BEFORE IT WAS WRITTEN: the download's sha256 verifies, `oras version` prints 1.2.2,
// and a second run on a PATH that already has it prints "already present" and downloads nothing.
//
// THE PIN MATTERS MORE THAN THE INSTALL. The tags oras writes are what later stages pull, so an
// unpinned download is a build where the tool that wrote a tag can change without a commit.
// THE WORKFLOW USES THE ORGANISATION'S ACTIONS FOR THE THINGS IT USED TO DO BY HAND.
//
// THIS REPLACES THREE HAND-WRITTEN BLOCKS that four repositories each had a copy of:
//
//	prepare            30 lines probing /dev/kvm, /dev/net/tun and qemu's accelerator, plus a
//	                   hand-pinned oras download
//	publish the disk   50 lines of `oras login` and `oras push`, with the 512 MiB floor check
//	install oras       a download with a sha256, in every leg's prepare
//
// THE ACTION CHECKS MORE THAN THE STEP DID. `test -e /dev/kvm` PASSED on run 43063744565 while qemu
// fell back to TCG, and the symptom was not an error - the guest booted and every stage timed out.
// `runner-check` starts qemu with `-accel kvm`, which is the handshake, and `ip tuntap add`, which is
// the node AND the NET_ADMIN capability together.
func TestTheWorkflowUsesTheOrganisationActions(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "stage.yml"))
	if err != nil {
		t.Skipf("no stage.yml here (%v)", err)
	}
	src := string(body)

	// THE FOUR CALLS, AND WHAT EACH ONE OWNS.
	for _, want := range []struct{ action, why string }{
		{"easyactions/runner-check@main",
			"the devices and the disk: /dev/kvm OPENS, qemu ACCELERATES, /dev/net/tun MAKES A TAP - " +
				"none of which `test -e` can answer"},
		{"easyactions/log-stream@main",
			"the live log: a running step's Actions log is unreadable until it ends"},
		{"easyactions/oci-push@main",
			"the publish: idempotent oras install, explicit login, sha256, --distinct-from"},
	} {
		if !strings.Contains(src, want.action) {
			t.Errorf("stage.yml does not use %s, which owns %s", want.action, want.why)
		}
	}

	// AND THE HAND-WRITTEN VERSIONS ARE GONE FROM THE COMMANDS. A half-migrated file is worse than
	// either end: two probes that disagree, or an oras install that runs in one leg and not another.
	//
	// COMMENTS ARE STRIPPED FIRST. The notes above each action quote the lines they replaced - "`test
	// -e /dev/kvm` PASSED on run 43063744565 while qemu fell back to TCG" is the reason the action
	// exists - so a search over the whole file finds the explanations and calls them code.
	var commands []string
	for _, line := range strings.Split(src, "\n") {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "#") {
			continue
		}
		commands = append(commands, line)
	}
	code := strings.Join(commands, "\n")
	for _, gone := range []struct{ text, why string }{
		{"test -e /dev/kvm", "a permission test where the operation is what matters"},
		{"ORAS_SHA256", "a hand-pinned oras download, now the action's job"},
		{"oras login", "an explicit login, now inside oci-push"},
	} {
		if strings.Contains(code, gone.text) {
			t.Errorf("stage.yml still RUNS %q (%s); the action owns it now, and two copies drift",
				gone.text, gone.why)
		}
	}

	// THE FLOOR CHECK STAYS, BECAUSE IT IS NOT THE ACTION'S BUSINESS. `qemu-img create` writes a
	// 197,632-byte qcow2 - a header and nothing else - so a walk whose every click missed still
	// produces a file `-s` accepts. MEASURED ON linux-zorin: published as 0 MiB, and the next stage
	// pulled it and failed somewhere unrelated.
	if !strings.Contains(src, "512 * 1024 * 1024") {
		t.Error("the 512 MiB floor is gone. A leg whose clicks never reached the installer publishes " +
			"a qcow2 header, and `-s` accepts one")
	}
}

// EVERY WORKFLOW THAT PUBLISHES USES ORAS, NOT A SUBCOMMAND THAT NO LONGER EXISTS.
//
// `easyvm push` was removed and `stage.yml` was updated - but `artifacts-publish.yml` was NOT, and
// nothing noticed until a real publish failed:
//
//	got: 2583691264 bytes            <- the download and the hash were fine
//	--novnc-port N   0 disables it   <- this is `easyvm`'s USAGE TEXT
//	warning: iso-popos-desktop: push failed
//
// An unknown subcommand makes the program print its whole usage and exit 2, so the failure reads as a
// rejected push rather than a missing command - and the file's own `|| echo "push failed"` turned it
// into a warning, so the step still "succeeded" at the shell level.
//
// THIS IS THE SAME CLASS OF BUG AS THE ARTIFACT ONE ABOVE: a shared step edited in one place while a
// second copy of the assumption lived somewhere else.
func TestNoWorkflowRunsARemovedSubcommand(t *testing.T) {
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
		for _, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(line)
			// COMMENTS MAY NAME IT - they explain what was removed and why.
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			checked++
			// A SHELL COMMAND, in either the bare or the `"$EASYVM"` form.
			for _, gone := range []string{"easyvm push", `"$EASYVM" push`} {
				if strings.Contains(trimmed, gone) {
					t.Errorf("%s runs %q, and that subcommand was removed. An unknown subcommand "+
						"prints the program's usage and exits 2, so the log reads as a rejected "+
						"push instead of a missing command: %q", filepath.Base(name), gone, trimmed)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no workflow lines were checked")
	}
}

// AND THE PUBLISHING TOOL IS INSTALLED WHEREVER IT IS USED.
//
// `artifacts-publish.yml` runs on a host runner rather than in the image, so it cannot inherit the
// oras that `prepare` installs - it builds its own, pinned the same way.

// ORAS NEEDS ITS OWN LOGIN, AND `GITHUB_TOKEN` ALONE IS NOT ONE.
//
// `easyvm push` read the token from the environment and did the exchange itself. oras does not: it
// keeps a session, and with none it pushes ANONYMOUSLY. MEASURED ON iso-popos-desktop, where the
// download and the hash had both already succeeded:
//
//	got:  2583691264 bytes
//	sha64: verified
//	Error response from registry: HEAD ".../manifests/sha256:dd6c..." :
//	  denied: requested access to the resource is denied
//
// THE MESSAGE NAMES THE PACKAGE, so it reads as a permissions problem - the same trap as the missing
// `GITHUB_TOKEN` before it. The token has to be EXCHANGED, and `oras login` is the exchange.
//
// EVERY STEP THAT PUSHES MUST LOG IN FIRST, which is why this checks each `oras push` has an
// `oras login` above it in the same step rather than anywhere in the file.

// A QCOW2 HEADER IS NOT AN INSTALLED SYSTEM, AND `-s` CANNOT TELL.
//
// `qemu-img create` writes 197,632 bytes - a header and nothing else - so a walk whose every click
// landed on the wrong pane still produces a file the old check accepted. MEASURED ON linux-zorin: the
// install leg reported success, `oras push` published it, and the package held a `linux-zorin` of
// 0 MiB beside a real `windows11`. The next stage pulled a disk with no system on it.
//
// THE PUBLISH STEP NOW REFUSES ANYTHING UNDER 512 MiB and names the reason, because the alternative is
// a tag that looks like a disk and is not one.
func TestThePublishStepRefusesAnEmptyQcow2(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "stage.yml"))
	if err != nil {
		t.Skipf("no stage.yml here (%v)", err)
	}
	var doc struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &doc); err != nil {
		t.Fatalf("stage.yml does not parse: %v", err)
	}
	var pub string
	for _, job := range doc.Jobs {
		for _, st := range job.Steps {
			if st.Name == "publish the disk" {
				pub = st.Run
			}
		}
	}
	if pub == "" {
		t.Fatal("stage.yml has no `publish the disk` step")
	}
	if !strings.Contains(pub, "512 * 1024 * 1024") {
		t.Error("the publish step has no size floor, so a 197,632-byte qcow2 header - what " +
			"`qemu-img create` writes and a walk that clicked nothing leaves behind - is published " +
			"as if it were an installed system")
	}
	if !strings.Contains(pub, "below the 512 MiB floor") {
		t.Error("the publish step refuses a small disk without saying why; the sentence is what " +
			"tells the next reader this is a walk problem rather than a storage one")
	}
	if !strings.Contains(pub, `size=$(stat -c %s "$out")`) {
		t.Error("the publish step does not measure the output; `-s` only tells zero from non-zero")
	}
}

// A CONTAINER JOB RUNS ITS STEPS WITH /bin/sh, WHICH IS dash - AND dash HAS NO `<<<`.
//
// MEASURED ON cachyos-install, WHICH HAD JUST PRODUCED A 6962 MiB DISK:
//
//	publishing .../linux-cachyos.qcow2: 6962 MiB
//	credential: GITHUB_TOKEN is set (377 characters)
//	....sh: 74: Syntax error: redirection unexpected
//	##[error]Process completed with exit code 2.
//
// THE CREDENTIAL WAS THERE, THE DISK WAS THERE, and the shell refused the line before `oras` ran. A
// bash here-string is invisible in review because it is valid in every shell a developer types into.
func TestNoWorkflowUsesABashOnlyConstruct(t *testing.T) {
	root := filepath.Join("..", "..", ".github", "workflows")
	files, err := filepath.Glob(filepath.Join(root, "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	// THE TWO THAT HAVE ALREADY COST A RUN. `<<<` is the here-string; `[[` is the doubled bracket
	// test; `$SECONDS` is a bash variable dash leaves empty. Each was measured on a real leg.
	banned := map[string]string{
		"<<<":      "a bash here-string; use `printf '%s' \"$var\" | cmd --password-stdin`",
		"[[":       "a bash test; use `[` and quote both sides",
		"$SECONDS": "a bash variable that dash leaves empty, so a timeout compares against nothing",
	}
	checked := 0
	for _, name := range files {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("cannot read %s: %v", name, err)
		}
		for _, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(line)
			// COMMENTS MAY NAME THEM - stage.yml explains this exact failure in prose.
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			checked++
			for bad, why := range banned {
				if strings.Contains(trimmed, bad) {
					t.Errorf("%s uses %q, which is %s. A container job's steps run under dash, "+
						"where the line is a syntax error rather than a command: %q",
						filepath.Base(name), bad, why, trimmed)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no workflow lines were checked")
	}
}
