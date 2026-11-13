package run

import (
	"fmt"

	"github.com/easydev-org/easyvm/internal/walk"
)

// A FILE HAS TWO HALVES, AND THEY WERE TANGLED INTO ONE LIST.
//
// THE PROBLEM THIS SOLVES, MEASURED. `create` - the mode that starts a machine and leaves it running -
// kept only the `boot:` steps from a file:
//
//	install.yaml                     create kept
//	1 start recording                -
//	2 create the empty disk          -        <- NO DISK
//	3 fetch the installer image      -        <- NO ISO
//	4 boot the installer             yes      <- and nothing to boot from
//	5 wait for the live desktop      -
//
// so a sandbox could only ever start a disk that already existed. A machine that boots an installer
// needs its disk and its installer PREPARED FIRST, and those are one operation with the boot.
//
// THE TWO HALVES ARE DIFFERENT KINDS OF THING:
//
//	machine:   HOW THIS GUEST IS BUILT AND STARTED. The disk it writes, the media it reads, the
//	           adapter and firmware it gets, and how much memory. It is data about a machine.
//	steps:     WHAT IS DONE TO THE GUEST once it is up. Clicking panes, reading panes, typing,
//	           waiting. It is a program against a running machine.
//
// A BAKE RUNS BOTH. A SANDBOX RUNS `machine:` AND STOPS, because its whole purpose is to hand back a
// machine you can then drive yourself - and driving it is exactly what the steps do, so running them
// would be doing the caller's work.
//
//	machine:
//	  disk: {path: disk.qcow2, gb: 128}
//	  fetch: {kind: linux_iso, release: ubuntu}
//	  boot: {os: linux, iso: installer_iso}
//	  memory_mb: 4096
//	  cpus: 4
//
// A FILE WITHOUT `machine:` IS UNCHANGED. The 52 walkthroughs in this repository keep their steps, and
// the first `disk:`/`fetch:`/`boot:` steps they contain are read as the machine's definition - so
// nothing has to be rewritten for the split to exist, and a file can migrate one key at a time.

// MachineSpec is the `machine:` block: how this guest is built and started.
type MachineSpec struct {
	// Disk is the disk this machine writes. Nil means the disk comes from outside (`--boot-disk`).
	Disk *DiskSpec
	// Fetch is the asset the boot needs prepared first: an installer ISO, a recovery image, the
	// OpenCore build from a recovery image.
	Fetch []FetchSpec
	// Boot is the step that starts qemu.
	Boot *walk.Step
	// MemoryMB and CPUs override the machine's shape when set.
	MemoryMB int
	CPUs     int
}

// DiskSpec is one `machine.disk:` entry.
type DiskSpec struct {
	Path string
	GB   int
}

// FetchSpec is one `machine.fetch:` entry.
//
// IT IS A LIST RATHER THAN ONE ENTRY because macOS needs three: the recovery image, the OpenCore
// archive, and the boot image built from both. They are one PREPARATION with three downloads, and
// keeping them together is what lets `create` know that a macOS machine cannot start until all three
// have landed.
type FetchSpec struct {
	Kind     string
	Release  string
	Template string
	Recovery string
}

// machineOf reads the machine half of a file.
//
// IT PREFERS `machine:` AND FALLS BACK TO THE STEPS, which is what makes the split compatible: a file
// that has not migrated keeps working, and the steps it loses from the front of its list are exactly
// the ones the machine block describes.
func machineOf(f *walk.File) (MachineSpec, []walk.Step, error) {
	if raw, ok := f.Document["machine"].(map[string]any); ok {
		return machineFromBlock(raw, f)
	}
	return machineFromSteps(f)
}

// machineFromBlock reads an explicit `machine:` block.
func machineFromBlock(raw map[string]any, f *walk.File) (MachineSpec, []walk.Step, error) {
	var m MachineSpec

	if disk, ok := raw["disk"].(map[string]any); ok {
		path, _ := disk["path"].(string)
		if path == "" {
			return m, nil, fmt.Errorf("%s: `machine.disk:` needs a `path:`", f.Path)
		}
		gb, _ := toFloat(disk["gb"])
		m.Disk = &DiskSpec{Path: path, GB: int(gb)}
	}
	if gb, ok := toFloat(raw["disk_gb"]); ok {
		// the shorthand: `machine: {disk_gb: 128}` writes disk.qcow2
		m.Disk = &DiskSpec{Path: "disk.qcow2", GB: int(gb)}
	}

	// FETCH IS ONE OR MANY, because macOS has three and every other OS has one.
	// IN A `machine:` BLOCK THE THREE ARE ONE LIST, because they are one preparation and the key
	// names only say WHY each file exists. The kind inside decides which key is emitted back.
	fetchRaw := raw["fetch"]
	if fetchRaw == nil {
		fetchRaw = raw["asset"]
	}
	if fetchRaw == nil {
		fetchRaw = raw["prepare"]
	}
	switch v := fetchRaw.(type) {
	case map[string]any:
		m.Fetch = append(m.Fetch, fetchFrom(v))
	case []any:
		for _, item := range v {
			if one, ok := item.(map[string]any); ok {
				m.Fetch = append(m.Fetch, fetchFrom(one))
			}
		}
	}

	// `machine.boot:` IS NOT ITSELF A BOOT STEP - IT IS THE BOOT STEP'S ARGUMENTS.
	//
	//	A file says:            machine: {boot: {os: linux, iso: installer_iso}}
	//	A boot STEP says:                    {boot: true, os: linux, iso: installer_iso}
	//
	// SO THE `boot:` KEY IS ADDED HERE, and its value is the truth `runOne` dispatches on. The first
	// version copied the arguments without it, so the step looked like a step that named `os` and
	// `iso` and nothing else - the dispatcher had no branch for it, and a synthesized machine ended
	// at a "no implementation for this step's keys" instead of a boot.
	if boot, ok := raw["boot"].(map[string]any); ok {
		spec := make(map[string]any, len(boot)+2)
		spec["name"] = "boot the machine"
		spec["boot"] = true
		for k, v := range boot {
			spec[k] = v
		}
		step := walk.Step{Name: "boot the machine", Spec: spec}
		m.Boot = &step
	}
	if m.Boot == nil {
		return m, nil, fmt.Errorf("%s: `machine:` needs a `boot:` - a machine block with no boot "+
			"describes something that never starts", f.Path)
	}

	if mb, ok := toFloat(raw["memory_mb"]); ok {
		m.MemoryMB = int(mb)
	}
	if n, ok := toFloat(raw["cpus"]); ok {
		m.CPUs = int(n)
	}

	// THE STEPS ARE THE WHOLE LIST, because a `machine:` block means the file already separated them.
	return m, f.Steps, nil
}

func fetchFrom(raw map[string]any) FetchSpec {
	spec := FetchSpec{}
	spec.Kind, _ = raw["kind"].(string)
	if spec.Kind == "" {
		for _, k := range []string{"asset", "build", "generate", "fetch"} {
			if v, ok := raw[k].(string); ok && v != "" {
				spec.Kind = v
				break
			}
		}
	}
	spec.Release, _ = raw["release"].(string)
	spec.Template, _ = raw["template"].(string)
	spec.Recovery, _ = raw["recovery"].(string)
	return spec
}

// machineFromSteps reads the machine out of an un-migrated file.
//
// THE PREFIX IS THE RULE AND IT IS A MEASURED ONE: every walkthrough in this repository opens with its
// preparation - `record:`, `disk:`, `fetch:`, `boot:` - and never returns to it. So the machine is the
// LEADING RUN of those steps, and the first step that is not one of them ends it. Everything after is
// what a person does to the guest.
func machineFromSteps(f *walk.File) (MachineSpec, []walk.Step, error) {
	var m MachineSpec
	i := 0
	for ; i < len(f.Steps); i++ {
		s := f.Steps[i]
		switch {
		case hasAny(s, "record"):
			// A RECORDING IS PART OF RUNNING A WALK, NOT PART OF BUILDING A MACHINE.
			//
			// It stays at the front of the file because it has to start before the boot to cover it,
			// and `machineFromSteps` skips it - the loop's own `i++` advances, so this branch does
			// not touch `i`. THE FIRST VERSION DID, WHICH SKIPPED THE NEXT STEP ENTIRELY: the disk
			// came out nil because `create the empty disk` was stepped over.
		case hasAny(s, "disk"):
			path := s.Get("disk")
			gb, _ := s.Float("disk_gb", 0)
			m.Disk = &DiskSpec{Path: path, GB: int(gb)}
		case stepHasAny(s, "asset", "build", "generate"):
			// ALL THREE ARE PREPARATION: a machine cannot start until its media exists, whether that
			// media was downloaded, assembled or rendered.
			m.Fetch = append(m.Fetch, FetchSpec{
				Kind:     firstOf(s, "asset", "build", "generate"),
				Release:  s.Get("release"),
				Template: s.Get("template"),
				Recovery: s.Get("recovery"),
			})
		case s.Has("boot") || s.Has("boot_from"):
			step := s
			// THE SIZE IS READ WHEREVER IT IS WRITTEN, because a walkthrough that has no `machine:`
			// block states it on its `boot:` step - and `create` must see the same machine `run`
			// would start, not a default-shaped one.
			if mb, ok := toFloat(step.Spec["memory_mb"]); ok {
				m.MemoryMB = int(mb)
			}
			if n, ok := toFloat(step.Spec["cpus"]); ok {
				m.CPUs = int(n)
			}
			m.Boot = &step
			i++
			goto done
		default:
			goto done
		}
	}
done:
	if m.Boot == nil {
		return m, nil, fmt.Errorf("%s: no `boot:` step and no `machine:` block, so there is no "+
			"machine to start", f.Path)
	}
	return m, f.Steps[i:], nil
}

// MachineHalf is `machineOf` for a caller that only wants the definition - the daemon's `create`, which
// runs this half and stops.
func MachineHalf(f *walk.File) (MachineSpec, []walk.Step, error) { return machineOf(f) }

// machineSteps turns a MachineSpec back into the steps that build and start it.
//
// IT IS THE INVERSE OF `machineFromSteps`, AND IT EXISTS BECAUSE THE EXECUTOR RUNS STEPS. A `machine:`
// block is data; the daemon that drives qemu knows how to run a `disk:` step and a `boot:` step, not
// how to read a struct. Converting one to the other here keeps that knowledge in one place instead of
// giving the executor a second way to start a machine.
func MachineSteps(m MachineSpec) []walk.Step {
	var out []walk.Step
	if m.Disk != nil {
		out = append(out, walk.Step{Name: "create the machine's disk", Spec: map[string]any{
			"name": "create the machine's disk", "disk": m.Disk.Path,
			"disk_gb": float64(m.Disk.GB),
		}})
	}
	for _, f := range m.Fetch {
		// THE KEY THE FILE USED IS NOT PRESERVED HERE, and it does not need to be: `doFetch` reads
		// whichever of the three is present, so emitting `asset:` for everything is the same
		// assembly with one spelling. A `machine:` block that wants to say `build:` says it in its
		// own `fetch:` list and keeps that name through `fetchFrom`.
		key := "asset"
		if f.Kind == "opencore" {
			key = "build"
		} else if f.Kind == "answer_iso" {
			key = "generate"
		}
		spec := map[string]any{"name": "prepare " + f.Kind, key: f.Kind}
		if f.Release != "" {
			spec["release"] = f.Release
		}
		if f.Template != "" {
			spec["template"] = f.Template
		}
		if f.Recovery != "" {
			spec["recovery"] = f.Recovery
		}
		out = append(out, walk.Step{Name: "fetch " + f.Kind, Spec: spec})
	}
	if m.Boot != nil {
		boot := *m.Boot
		// THE MACHINE'S SIZE RIDES ON THE BOOT STEP, because the boot step is the only thing that
		// reaches qemu. `memory_mb:` and `cpus:` used to be READ AND DROPPED here: `machineFromBlock`
		// stored them on the MachineSpec, nothing copied them onto a step, and `doBoot` never asked -
		// so EVERY GUEST RAN WITH THE 4096 MiB DEFAULT whatever the file said.
		//
		// MEASURED ON ghostbsd, which is the guest that needs the answer: its installer copies a
		// 1909 MiB uzip image into a SWAP-BASED MEMDISK, the copy stops with
		//
		//	cannot receive new filesystem stream: out of space 114.024s, 27 MB/s
		//	Enter full pathname of shell or RETURN for /rescue/sh:
		//
		// and the walker, still waiting for an X-driver menu that could never appear, reported
		// `choose the X driver` for sixty attempts.
		//
		// IT IS COPIED RATHER THAN READ FROM THE FILE LATER because `create` narrows the file to its
		// machine steps: a `machine:` block's own keys are gone by then, and the step is what is left.
		if m.MemoryMB > 0 {
			boot.Spec["memory_mb"] = float64(m.MemoryMB)
		}
		if m.CPUs > 0 {
			boot.Spec["cpus"] = float64(m.CPUs)
		}
		out = append(out, boot)
	}
	return out
}

// stepHasAny reports whether the step carries any of these keys.
func stepHasAny(s walk.Step, keys ...string) bool {
	for _, k := range keys {
		if _, ok := s.Spec[k]; ok {
			return true
		}
	}
	return false
}

// firstOf returns the value of the first of these keys the step carries.
func firstOf(s walk.Step, keys ...string) string {
	for _, k := range keys {
		if v := s.Get(k); v != "" {
			return v
		}
	}
	return ""
}

// hasAny reports whether the step carries this key at all, including a false or empty value.
//
// `Has` is the vocabulary's own question and it is the right one almost everywhere; here the answer
// has to be "is this key present", because `record: false` is a step and a machine block copied from
// a file may carry an empty `recovery:`.
func hasAny(s walk.Step, key string) bool {
	_, ok := s.Spec[key]
	return ok
}
