package machine

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/easydev-org/easyvm/internal/machine/shapes"
	"gopkg.in/yaml.v3"
)

// A NEW OPERATING SYSTEM IS A FILE, AND THIS IS THE TEST THAT SAYS SO.
//
// THE CLAIM BEING PINNED IS NOT "the loader works" - it is that NOTHING IN GO HAS TO CHANGE. A
// `chromeos` guest that no constant, no `switch` and no builder in this package has ever heard of is
// started by writing one YAML file into a shapes directory, and the argument list that comes out is
// the one the file describes.
//
// IF THIS TEST EVER NEEDS A CODE CHANGE TO PASS, THE DESIGN HAS REGRESSED: the machine shapes were
// supposed to move out of the engine, and a shape that needs a Go constant is still in it.
func TestANewOSIsAFileAndNoCodeChanges(t *testing.T) {
	dir := t.TempDir()
	// A MACHINE THIS BUILD HAS NEVER SEEN, with every field set to something non-default so the
	// assertions below cannot pass by accident.
	chromeos := `name: chromeos
description: "a guest this build was not compiled for"
firmware:
  kind: ovmf
  code: "{{.Code}}"
  vars: "{{.Vars}}"
cpu:
  suffix: ",+sse4.2,+aes"
smp:
  format: plain
video:
  kind: virtio
audio: hda
smc: false
install_medium: iso
extra_args:
  - ["-global", "some-device.some-property=1"]
media:
  iso: true
  recovery: false
`
	path := filepath.Join(dir, "chromeos.yaml")
	if err := os.WriteFile(path, []byte(chromeos), 0o644); err != nil {
		t.Fatal(err)
	}

	// A REGISTRY OF ITS OWN, which is also how a walkthrough repository would add a machine without
	// touching the ones this build ships.
	reg := NewRegistry()
	if err := reg.LoadDir(dir); err != nil {
		t.Fatalf("loading a shape for an operating system this build has no constant for: %v", err)
	}

	shape, ok := reg.Lookup("chromeos")
	if !ok {
		t.Fatal("the shape was not registered; a name that is not a Go constant is the whole point")
	}

	// AND IT IS A REAL MACHINE, not a record that parsed: every builder reads it.
	b := Boot{OS: "chromeos", Shape: shape, Disk: "/w/disk.qcow2"}

	if got := b.FirmwareArgs("/usr/share/OVMF/OVMF_CODE_4M.fd", "/w/vars.fd"); len(got) != 4 {
		t.Fatalf("firmware = %v; an ovmf shape is two -drive pairs", got)
	}
	cpu, err := b.CPU("")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(cpu, ",+sse4.2,+aes") {
		t.Fatalf("cpu = %q; the shape's suffix must be appended verbatim - that is how every qemu CPU "+
			"flag is spelled", cpu)
	}
	if got := b.VGA("std"); got != AndroidVGA {
		t.Fatalf("VGA = %q, want %q; `video.kind: virtio` is virtio-vga whatever came before the comma",
			got, AndroidVGA)
	}
	if got := b.SMP(4); got != "4" {
		t.Fatalf("SMP = %q, want a bare count", got)
	}
	if args := b.ExtraArgs(); len(args) != 2 || args[0] != "-global" {
		t.Fatalf("ExtraArgs = %v; the shape's pairs are appended verbatim", args)
	}
	if args := b.SMCArgs(""); len(args) != 0 {
		t.Fatalf("SMCArgs = %v; `smc: false` means no Apple SMC", args)
	}
	if b.installing() != false {
		t.Fatal("installing() = true with no ISO attached; `install_medium: iso` means the ISO is what " +
			"marks an install, and there is none")
	}
}

// AND THE SAME FILE, POINTED AT THE DAEMON, IS WHAT A REPOSITORY CAN SHIP.
//
// `--shapes DIR` REPLACES the compiled-in shapes rather than adding to them, which is the property a
// walkthrough repository needs: its own directory names exactly the machines its walkthroughs use, and
// a name it did not write is a refusal rather than a machine this build happened to carry.
func TestShapesDirReplacesTheCompiledInOnes(t *testing.T) {
	dir := t.TempDir()
	// ONE MACHINE, AND NOT THE SIX THIS BUILD SHIPS WITH.
	one := `name: only
description: "the only machine this directory declares"
firmware: {kind: none}
audio: none
video: {kind: absent}
install_medium: none
media: {iso: false, iso_reason: "a machine with no firmware cannot boot an ISO", recovery: false}
`
	if err := os.WriteFile(filepath.Join(dir, "only.yaml"), []byte(one), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	if err := reg.LoadDir(dir); err != nil {
		t.Fatal(err)
	}
	if reg.Len() != 1 {
		t.Fatalf("the directory declares %d shape(s), want 1", reg.Len())
	}
	if _, ok := reg.Lookup("only"); !ok {
		t.Fatal("`only` is not registered")
	}
	if _, ok := reg.Lookup("linux"); ok {
		t.Fatal("`linux` is registered too, so loading a directory ADDS to the compiled-in shapes " +
			"rather than replacing them - and a walkthrough repository's own directory must name " +
			"exactly the machines its files use")
	}
}

// A SHAPE FILE THAT DOES NOT PARSE IS AN ERROR AT LOAD, NOT AT BOOT.
//
// MEASURED SHAPE OF THE FAILURE THIS PREVENTS: a shape whose `extra_args` entry lost its value
// produces a qemu command line whose `-device` has no argument, and qemu's error names the DEVICE -
// sending a reader to look for a device bug in an argument list nobody can check by eye.
func TestAMalformedShapeIsRefusedAtLoad(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "odd extra_args",
			body: "name: x\nfirmware: {kind: none}\naudio: none\nextra_args:\n  - [\"-device\"]\n" +
				"media: {iso: false, iso_reason: \"none\", recovery: false}\n",
			want: "extra_args[0]",
		},
		{
			name: "unknown video",
			body: "name: x\nfirmware: {kind: none}\naudio: none\nvideo: {kind: s3-trio}\n" +
				"media: {iso: false, iso_reason: \"none\", recovery: false}\n",
			want: "not an adapter this build knows",
		},
		{
			name: "iso refused without a reason",
			body: "name: x\nfirmware: {kind: none}\naudio: none\nmedia: {iso: false, recovery: false}\n",
			want: "iso_reason",
		},
		{
			name: "opencore naming pflash paths",
			body: "name: x\nfirmware: {kind: opencore, code: /a, vars: /b}\naudio: none\n" +
				"media: {iso: false, iso_reason: \"none\", recovery: false}\n",
			want: "must not name",
		},
		{
			name: "no name",
			body: "firmware: {kind: none}\naudio: none\n" +
				"media: {iso: false, iso_reason: \"none\", recovery: false}\n",
			want: "must have a `name:`",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reg := NewRegistry()
			err := reg.LoadBytes("x.yaml", []byte(c.body))
			if err == nil {
				t.Fatalf("this shape was accepted, and the failure it produces would arrive as a "+
					"qemu error naming a device nobody can find: %s", c.body)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("the refusal says %q; a reader needs it to mention %q", err, c.want)
			}
		})
	}
}

// A DUPLICATE NAME IS REFUSED RATHER THAN THE LAST ONE WINNING.
//
// TWO FILES CLAIMING `os: macos` is a configuration whose behaviour depends on the order a directory
// happens to be read in - and a machine that changes shape when a file is renamed is not a machine
// anybody can reason about.
func TestTwoShapesCannotShareAName(t *testing.T) {
	reg := NewRegistry()
	one := []byte("name: dup\nfirmware: {kind: none}\naudio: none\n" +
		"media: {iso: false, iso_reason: \"none\", recovery: false}\n")
	if err := reg.LoadBytes("a.yaml", one); err != nil {
		t.Fatal(err)
	}
	err := reg.LoadBytes("b.yaml", one)
	if err == nil {
		t.Fatal("a second shape with the same name was accepted")
	}
	if !strings.Contains(err.Error(), "two shapes are named") {
		t.Fatalf("the refusal says %q, and it must say that two files claimed one name", err)
	}
}

// THIS BUILD SHIPS TWO SHAPES, AND THEY ARE THE TWO THAT NEED NOTHING FROM A REPOSITORY.
//
// A MACHINE SHAPE BELONGS TO THE REPOSITORY THAT RUNS IT. `easyvm-windows` owns `windows.yaml`,
// `easyvm-macos` owns `macos.yaml`, and so on - each passes `shapes_dir:` to `stage.yml`, which hands
// the daemon `--shapes`. There is no copy of those four here, so a change to what a Windows guest IS
// happens in one place.
//
// THESE TWO STAY EMBEDDED BECAUSE THE IMAGE HAS NO SOURCE TREE. `linux` and `microvm` are the machines
// a bare image can start with nothing mounted beside it: firmware and a boot sector for the first, a
// kernel and an initramfs for the second, and neither needs a file from a repository. Without them a
// container daemon would start no guest at all until somebody mounted a directory.
//
// A MISSING ONE IS NOT A MISSING TEST: `ParseOS` refuses an `os:` it has no shape for, so a build that
// lost `linux.yaml` would refuse every Linux walkthrough with a message about the build rather than
// about the file.
func TestTheShippedShapesCoverTheTwoMachinesThatNeedNoRepository(t *testing.T) {
	// THE EMBEDDED FILES, NOT THE REGISTRY. `TestMain` also registers the four machines the
	// sub-repositories own, when those are checked out beside this one - so the registry is the wrong
	// place to ask "what does this BINARY carry". `shapes.Files` is what `go:embed` compiled in.
	want := map[string]bool{"linux.yaml": true, "microvm.yaml": true}
	entries, err := fs.ReadDir(shapes.Files, ".")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
			got[e.Name()] = true
		}
	}
	for name := range want {
		if !got[name] {
			t.Errorf("this build embeds no %s, so a container can start no guest of that shape "+
				"until a shapes directory is mounted", name)
		}
	}
	for name := range got {
		if !want[name] {
			t.Errorf("this build embeds %s. The other four machines are owned by easyvm-windows, "+
				"easyvm-macos, easyvm-bsd and easyvm-android, which pass them with `--shapes`; a copy "+
				"here would be a second definition of the same machine", name)
		}
	}
	// AND EACH ONE PARSES AND REGISTERS, which is what makes the file worth embedding.
	for name := range want {
		raw, err := fs.ReadFile(shapes.Files, name)
		if err != nil {
			t.Fatal(err)
		}
		var f ShapeFile
		if err := yaml.Unmarshal(raw, &f); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := f.Validate(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}
// THE TWO EMBEDDED SHAPES ARE THE SUB-REPOSITORIES' OWN, BYTE FOR BYTE.
//
// THIS IS THE INVARIANT THAT MAKES "ONE DEFINITION" TRUE. `easyvm-linux` owns `linux.yaml` and
// `easyvm-microvm` owns `microvm.yaml`; this build embeds a copy of each because a container has no
// source tree and must be able to start these two with nothing mounted. A copy that is not checked
// against the original IS a second definition, and it drifts - a device added in the repository and
// not here would mean a guest that behaves differently in CI than in the image.
//
// A CHECKOUT WITHOUT THE SUB-REPOSITORIES SKIPS, and says so. That is the ordinary case for a
// container build, and inventing a stricter answer would fail every image build for a reason that has
// nothing to do with the image.
func TestTheEmbeddedShapesAreTheSubRepositoriesOwn(t *testing.T) {
	owners := map[string]string{
		"linux.yaml":   "easyvm-linux",
		"microvm.yaml": "easyvm-microvm",
	}
	var bases []string
	if env := os.Getenv("EASYVM_STAGES"); env != "" {
		bases = strings.Split(env, string(os.PathListSeparator))
	} else {
		bases = []string{"../..", "..", "../../.."}
	}

	checked := 0
	for file, repo := range owners {
		embedded, err := fs.ReadFile(shapes.Files, file)
		if err != nil {
			t.Fatalf("%s is not embedded: %v", file, err)
		}
		var found string
		for _, base := range bases {
			for _, name := range []string{repo, "sd-" + strings.TrimPrefix(repo, "easyvm-")} {
				p := filepath.Join(base, name, "shapes", file)
				if _, err := os.Stat(p); err == nil {
					found = p
					break
				}
			}
			if found != "" {
				break
			}
		}
		if found == "" {
			continue // this checkout does not have the sub-repositories
		}
		checked++
		theirs, err := os.ReadFile(found)
		if err != nil {
			t.Fatal(err)
		}
		if string(embedded) != string(theirs) {
			t.Errorf("%s differs from %s. The sub-repository is the definition and this copy is what "+
				"an image ships; two different answers to \"what is this machine\" is the drift this "+
				"test exists to catch. Copy the repository's file over the embedded one", file, found)
		}
	}
	if checked == 0 {
		t.Skip("no sub-repositories here: the embedded shapes cannot be compared against their owners")
	}
	t.Logf("checked %d embedded shape(s) against the repositories that own them", checked)
}
