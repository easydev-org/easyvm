package run

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/easydev-org/easyvm/internal/walk"
)

// A FILE HAS TWO HALVES: HOW THE MACHINE IS BUILT, AND WHAT IS DONE TO IT.
//
// THE PROBLEM THIS SOLVES, MEASURED WITH `create` - the mode that starts a machine and leaves it
// running:
//
//	create → boot: booting needs a target disk: a `disk:` step must come first
//
// because the old code kept ONLY the `boot:` steps from a file. A machine that boots an installer
// needs its disk and its ISO PREPARED FIRST:
//
//	install.yaml                     old create kept
//	1 start recording                -
//	2 create the empty disk          -        <- NO DISK
//	3 fetch the installer image      -        <- NO ISO
//	4 boot the installer             yes      <- and nothing to boot from
//	5 wait for the live desktop      -
//
// SO THE MACHINE HALF IS THE PREPARATION AND THE BOOT TOGETHER: a disk is created FOR a boot, an ISO
// is fetched FOR it, and they are one operation.
func TestTheMachineHalfIsThePreparationAndTheBoot(t *testing.T) {
	// AN UN-MIGRATED FILE: the machine is the LEADING RUN of preparation steps, and everything after
	// the boot is what a person does to the guest.
	doc, err := loadFixture(t, map[string]any{
		"name": "probe",
		"steps": []any{
			map[string]any{"name": "start recording", "record": "x.mp4"},
			map[string]any{"name": "create the empty disk", "disk": "disk.qcow2", "disk_gb": 128},
			map[string]any{"name": "the installer image", "asset": "linux_iso", "release": "ubuntu"},
			map[string]any{"name": "boot the installer", "boot": true, "os": "linux", "iso": "installer_iso"},
			map[string]any{"name": "wait for the desktop", "desktop": "linux"},
			map[string]any{"name": "click Next", "click": []any{10, 20}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	machine, rest, err := MachineHalf(doc)
	if err != nil {
		t.Fatalf("the file describes a machine: %v", err)
	}
	if machine.Disk == nil || machine.Disk.Path != "disk.qcow2" || machine.Disk.GB != 128 {
		t.Fatalf("the disk half came out %+v; the `disk:` step must be part of the machine", machine.Disk)
	}
	if len(machine.Fetch) != 1 || machine.Fetch[0].Kind != "linux_iso" {
		t.Fatalf("the fetch half came out %+v; the ISO is fetched FOR the boot", machine.Fetch)
	}
	if machine.Boot == nil {
		t.Fatal("no boot step")
	}

	// AND THE REST IS WHAT A PERSON DOES, starting at the first step that is not preparation.
	if len(rest) != 2 {
		t.Fatalf("%d step(s) after the machine, want 2 (the desktop wait and the click); a run "+
			"that dropped these would click nothing, and one that kept the preparation would "+
			"create a second disk", len(rest))
	}
	if rest[0].Name != "wait for the desktop" {
		t.Fatalf("the remaining steps start at %q, want the desktop wait", rest[0].Name)
	}

	// A SANDBOX RUNS THE MACHINE HALF, SO IT MUST BE RUNNABLE - that is the whole fix: `create` gets
	// three steps instead of one, and the first two are what a boot needs.
	steps := MachineSteps(machine)
	if len(steps) != 3 {
		t.Fatalf("the machine half is %d step(s), want 3 (disk, fetch, boot)", len(steps))
	}
}

// AN EXPLICIT `machine:` BLOCK IS PREFERRED, AND A FILE WITH ONE HAS NO PREPARATION IN ITS STEPS.
func TestAnExplicitMachineBlockIsUsed(t *testing.T) {
	doc, err := loadFixture(t, map[string]any{
		"name": "probe",
		"machine": map[string]any{
			"disk":  map[string]any{"path": "target.qcow2", "gb": 128},
			"fetch": map[string]any{"kind": "recovery_macos", "release": "15"},
			"boot":  map[string]any{"os": "macos", "recovery": "recovery_macos"},
		},
		"steps": []any{
			map[string]any{"name": "wait for the recovery menu", "until": []any{"disk", "utility"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	machine, rest, err := MachineHalf(doc)
	if err != nil {
		t.Fatalf("the machine block is valid: %v", err)
	}
	if machine.Disk == nil || machine.Disk.Path != "target.qcow2" {
		t.Fatalf("disk = %+v, want target.qcow2", machine.Disk)
	}
	if machine.Boot == nil || machine.Boot.Get("os") != "macos" {
		t.Fatalf("boot = %+v", machine.Boot)
	}
	// NO STEPS ARE CONSUMED, because the file already separated them.
	if len(rest) != 1 {
		t.Fatalf("%d step(s) remain, want 1; a `machine:` block means the file already split "+
			"itself, so nothing is taken from the step list", len(rest))
	}
}

// COMING FROM AN ISO OR A RECOVERY IMAGE IS THE SAME SHAPE, WHICH IS THE POINT OF THE SPLIT.
//
// The boot names a medium and the fetch prepares it; what differs is only which fetch and which
// medium, and neither is the step list's business.
func TestAnISOAndARecoveryAreTheSameShape(t *testing.T) {
	cases := []struct {
		name  string
		fetch map[string]any
		boot  map[string]any
	}{
		{"an installer ISO", map[string]any{"kind": "linux_iso", "release": "ubuntu"},
			map[string]any{"os": "linux", "iso": "installer_iso"}},
		{"a recovery image", map[string]any{"kind": "recovery_macos", "release": "15"},
			map[string]any{"os": "macos", "recovery": "recovery_macos"}},
	}
	for _, c := range cases {
		doc, err := loadFixture(t, map[string]any{
			"name":    c.name,
			"machine": map[string]any{"disk_gb": 128, "fetch": c.fetch, "boot": c.boot},
			"steps":   []any{map[string]any{"name": "wait", "wait": 1}},
		})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		machine, _, err := MachineHalf(doc)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		steps := MachineSteps(machine)
		// disk, fetch, boot - in that order, because the medium must exist before the boot names it.
		if len(steps) != 3 {
			t.Fatalf("%s: %d machine step(s), want 3", c.name, len(steps))
		}
		// `hasAny` RATHER THAN `Has`: a synthesized `disk_gb: 0` is present but empty, and `Has`
		// asks whether the key carries a value. The order is what this asserts.
		if !stepHasKey(steps[0], "disk") || !stepHasKey(steps[1], "asset") || !stepHasKey(steps[2], "boot") {
			t.Fatalf("%s: the machine half is out of order: %v (keys: %v / %v / %v)", c.name,
				[]string{steps[0].Name, steps[1].Name, steps[2].Name},
				keysOf(steps[0]), keysOf(steps[1]), keysOf(steps[2]))
		}
	}
}

// A FILE WITH NO BOOT IS A REFUSAL THAT SAYS SO.
func TestAFileWithNoMachineIsRefused(t *testing.T) {
	doc, err := loadFixture(t, map[string]any{
		"name":  "probe",
		"steps": []any{map[string]any{"name": "click", "click": []any{1, 2}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := MachineHalf(doc); err == nil {
		t.Fatal("a file with no boot must be refused; `create` would otherwise start nothing and " +
			"report success")
	}
}

// loadFixture writes YAML to a temp file and loads it through the REAL loader, so a fixture cannot
// describe something the parser would refuse.
func loadFixture(t *testing.T, doc map[string]any) (*walk.File, error) {
	t.Helper()
	body, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "w.yaml")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return walk.Load(path, nil)
}

// hasKey reports whether the step's own mapping carries this key at all.
func stepHasKey(s walk.Step, key string) bool {
	_, ok := s.Spec[key]
	return ok
}

// keysOf lists a step's own keys, so a failure says what was actually there.
func keysOf(s walk.Step) []string {
	out := make([]string, 0, len(s.Spec))
	for k := range s.Spec {
		out = append(out, k)
	}
	return out
}

// THE GUEST'S SIZE HAS TO REACH QEMU, AND FOR ONE RELEASE IT DID NOT.
//
// `memory_mb:` and `cpus:` were read off a `machine:` block into `MachineSpec` and then dropped:
// `MachineSteps` emitted the disk, the fetches and the boot, and neither key was on the boot step, so
// `doBoot` had nothing to pass and qemu got `machine.DefaultMemMB` whatever the file said.
//
// THE GUEST THAT MEASURED IT was ghostbsd. Its installer copies a 1909 MiB system image into a
// SWAP-BASED MEMDISK, and at 4096 MiB the copy stops:
//
//	cannot receive new filesystem stream: out of space 114.024s, 27 MB/s
//	Enter full pathname of shell or RETURN for /rescue/sh:
//
// The walk then waited sixty times for an X-driver menu that could never appear.
//
// SO THIS TEST IS ABOUT THE CABLE, NOT THE VALUE: whatever a file writes must be on the step the
// executor runs, in both spellings - the `machine:` block and a plain `boot:` step.
func TestTheGuestSizeReachesTheBootStep(t *testing.T) {
	for _, tc := range []struct {
		name string
		doc  map[string]any
	}{
		{"machine block", map[string]any{
			"name": "sized",
			"machine": map[string]any{
				"disk_gb":   128,
				"memory_mb": 8192,
				"cpus":      4,
				"boot":      map[string]any{"os": "bsd", "iso": "installer_iso"},
			},
			// A `machine:` file states its own steps; the block is the machine, not the walk.
			"steps": []any{
				map[string]any{"name": "wait", "wait": 5},
			},
		}},
		{"boot step", map[string]any{
			"name": "sized",
			"steps": []any{
				map[string]any{"name": "disk", "disk": "disk.qcow2", "disk_gb": 128},
				map[string]any{"name": "boot it", "boot": true, "os": "bsd",
					"memory_mb": 8192, "cpus": 4},
				map[string]any{"name": "wait", "wait": 5},
			},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := loadFixture(t, tc.doc)
			if err != nil {
				t.Fatal(err)
			}
			spec, _, err := machineOf(doc)
			if err != nil {
				t.Fatal(err)
			}
			if spec.MemoryMB != 8192 {
				t.Errorf("the machine is %d MiB, and the file says 8192 - a size that stops at the "+
					"struct is a size qemu never sees", spec.MemoryMB)
			}

			// AND THE STEPS ARE WHAT THE EXECUTOR ACTUALLY RUNS, so the number has to survive the
			// round trip. `create` narrows a file to exactly this list.
			var boot *walk.Step
			for i, s := range MachineSteps(spec) {
				if s.Has("boot") {
					boot = &MachineSteps(spec)[i]
				}
			}
			if boot == nil {
				t.Fatal("the machine's steps contain no boot, so nothing would start")
			}
			if got, _ := boot.Float("memory_mb", 0); int(got) != 8192 {
				t.Errorf("the boot step says memory_mb=%v, and the file says 8192 - qemu reads the "+
					"step, so this is the number that decides the guest", got)
			}
			if got, _ := boot.Float("cpus", 0); int(got) != 4 {
				t.Errorf("the boot step says cpus=%v, and the file says 4", got)
			}
		})
	}
}
