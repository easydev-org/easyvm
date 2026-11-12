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
func TestPrepareInstallsOrasAndSkipsWhenPresent(t *testing.T) {
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
	var prep string
	for _, job := range doc.Jobs {
		for _, st := range job.Steps {
			if st.Name == "prepare" {
				prep = st.Run
			}
		}
	}
	if prep == "" {
		t.Fatal("stage.yml has no `prepare` step, so this test checks nothing")
	}

	// IT PROBES BEFORE DOWNLOADING, so a runner or image that already has oras pays nothing.
	if !strings.Contains(prep, "if oras version") {
		t.Error("`prepare` does not check whether oras is already there. `oras version` rather than " +
			"`command -v` because a binary on PATH that cannot run is the same failure one line later")
	}
	// AND IT INSTALLS WHEN IT IS NOT, WITH A PINNED VERSION AND HASH.
	for _, want := range []string{"ORAS_VERSION=", "ORAS_SHA256=", "sha256sum -c -", "/usr/local/bin/oras"} {
		if !strings.Contains(prep, want) {
			t.Errorf("`prepare` installs oras without %q, so the tool that writes the tags later "+
				"stages pull is not a known tool", want)
		}
	}
	// AND THE IMAGE DOES NOT ALSO INSTALL IT - one place, or a version bump is two commits.
	containerfile, err := os.ReadFile(filepath.Join("..", "..", "Containerfile"))
	if err != nil {
		t.Skipf("no Containerfile here (%v)", err)
	}
	if strings.Contains(string(containerfile), "ORAS_VERSION") {
		t.Error("the Containerfile also installs oras. One place installs it: `prepare`. Two places " +
			"means a version bump is two commits and one of them gets forgotten")
	}
}
