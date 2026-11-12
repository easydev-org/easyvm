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
