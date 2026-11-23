package daemon

// THE THREE MICROVM SHAPES ARE THREE DIFFERENT QEMU MACHINES, and that is not obvious from the
// vocabulary: `microvm_display` reads as "what the guest draws", while it decides the PCIe bus, the
// graphics flag and - through `machine.Boot.KernelArgs` - whether the kernel is told `pci=off`.
//
// EACH OF THESE WAS MEASURED, AND EACH FAILURE WAS SILENT OR NEARLY SO:
//
//   - `terminal` with `pcie=on` attaches the disk as virtio-MMIO anyway and pays for a PCI bus the
//     guest never uses.
//   - `desktop` on `virtio-gpu-device` (the MMIO transport) ATTACHES AND NEVER BINDS: status 0x83,
//     no /dev/dri/card0, so every compositor has no card to draw on.
//   - `desktop` with `graphics=on` puts an ISA VGA BESIDE the GPU, and a screendump may photograph
//     that text adapter instead of the desktop.
//   - `desktop` carrying `pci=off` HIDES THE BUS THE GPU IS ON, which removes the one device the
//     shape exists for.
//
// SO THE TEST IS THE PAIRING: the display names the adapter and the bus, and the two must agree.

import (
	"os"
	"strings"
	"testing"

	"github.com/easydev-org/easyvm/internal/machine"
)

func readSrc(t *testing.T, name string) ([]byte, error) {
	t.Helper()
	return os.ReadFile(name)
}

func machineString(t *testing.T, display string) string {
	t.Helper()
	return machineType(machine.Boot{MicroVM: true, MicroVMDisplay: display})
}

// TestTheSerialMicrovmIsStillTheSmallMachine holds the shape that had no change: no PCIe, no
// graphics, because a serial guest has no screen to photograph and no device on a bus.
func TestTheSerialMicrovmIsStillTheSmallMachine(t *testing.T) {
	got := machineString(t, "serial")
	for _, want := range []string{"pcie=off", "graphics=off"} {
		if !strings.Contains(got, want) {
			t.Errorf("a serial microvm's machine is %q, which does not carry %q", got, want)
		}
	}
	if strings.Contains(got, "pcie=on") {
		t.Errorf("a serial microvm's machine is %q - it has no device for a PCI bus", got)
	}
}

// TestTheTerminalMicrovmKeepsGraphicsAndNoPCIe is the text-console shape: an ISA VGA the guest draws
// its TTY on, and still no PCI bus.
func TestTheTerminalMicrovmKeepsGraphicsAndNoPCIe(t *testing.T) {
	got := machineString(t, "terminal")
	for _, want := range []string{"pcie=off", "graphics=on"} {
		if !strings.Contains(got, want) {
			t.Errorf("a terminal microvm's machine is %q, which does not carry %q", got, want)
		}
	}
}

// TestTheDesktopMicrovmTurnsPCIeOnAndGraphicsOff IS THE MEASUREMENT THE WHOLE DESKTOP SUPPORT RESTS
// ON: `virtio-gpu-device` on the MMIO bus never binds, while `virtio-gpu-pci` on a PCI bus gives one
// card with both a KMS output and a render node.
func TestTheDesktopMicrovmTurnsPCIeOnAndGraphicsOff(t *testing.T) {
	got := machineString(t, "desktop")
	if !strings.Contains(got, "pcie=on") {
		t.Fatalf("a desktop microvm's machine is %q. The GPU is on the PCIe bus: with pcie=off the "+
			"virtio-gpu device attaches and its driver never binds, so there is no /dev/dri/card0 "+
			"for a compositor to draw on", got)
	}
	if !strings.Contains(got, "graphics=off") {
		t.Errorf("a desktop microvm's machine is %q and asks for graphics=on as well. That adds an "+
			"ISA VGA BESIDE the GPU, and a screendump may photograph the text adapter instead of "+
			"the desktop", got)
	}
	// `acpi=on` IS NOT OPTIONAL ONCE PCIe EXISTS: a kernel that finds an ACPI root assumes a PCI
	// host bridge, and Debian needs the ACPI tables to see its MMIO disk. `pic` and `rtc` come with
	// it because a machine without its own timer logs a clock that never advances.
	for _, want := range []string{"acpi=on", "pic=on", "rtc=on", "accel=kvm"} {
		if !strings.Contains(got, want) {
			t.Errorf("a microvm's machine is %q, which does not carry %q", got, want)
		}
	}
}

// TestTheDesktopMicrovmGetsThePCIGPUAdapters checks the ADAPTER, which is now a line in the shape.
//
// THE DESKTOP PROFILE ASKS FOR `virtio-gpu-pci`, AND THE OTHERS DO NOT. A desktop microvm's GPU is a
// PCIe device: `virtio-gpu-device` on virtio-MMIO attaches with status 0x83 and its driver never
// binds, so there is no card and every screenshot is qemu's blank framebuffer.
//
// THE SHAPE IS READ FROM THE SUB-REPOSITORIES when they are checked out; a deployment that has only
// this repository has the two embedded ones, which is where microvm lives.
func TestTheDesktopMicrovmGetsThePCIGPUAdapters(t *testing.T) {
	sh, ok := machine.Shapes().Lookup("microvm")
	if !ok {
		t.Skip("no microvm shape here")
	}
	desktop, err := sh.Profile("desktop")
	if err != nil {
		t.Fatalf("the microvm shape has no `desktop` profile: %v", err)
	}
	var joined []string
	for _, pair := range desktop.Args {
		joined = append(joined, pair...)
	}
	text := strings.Join(joined, " ")
	if !strings.Contains(text, "virtio-gpu-pci") {
		t.Fatalf("the `desktop` profile names no `virtio-gpu-pci`, so the guest's GPU never binds: "+
			"\n%s", text)
	}
	if strings.Contains(text, "virtio-gpu-device") {
		t.Errorf("the `desktop` profile names `virtio-gpu-device`, which does not bind on a microvm. "+
			"A desktop takes `virtio-gpu-pci`; a text console takes `isa-vga`:\n%s", text)
	}
	// AND PCIe IS TURNED ON FOR IT - that bus is where the GPU lives.
	if !strings.Contains(desktop.Machine, "pcie=on") {
		t.Errorf("the `desktop` profile's machine is %q; a virtio-gpu-pci needs a PCIe bus", desktop.Machine)
	}
	for _, name := range []string{"serial", "terminal"} {
		prof, err := sh.Profile(name)
		if err != nil {
			t.Fatalf("no %q profile: %v", name, err)
		}
		if strings.Contains(prof.Machine, "pcie=on") {
			t.Errorf("the %q profile turns PCIe on; only a desktop needs it, and the other two are "+
				"virtio-MMIO machines", name)
		}
		if strings.Contains(strings.Join(flatten(prof.Args), " "), "virtio-gpu") {
			t.Errorf("the %q profile names a GPU; it draws no display", name)
		}
	}
}

// flatten joins a profile's argument pairs into one list, for a text assertion.
func flatten(pairs [][]string) []string {
	var out []string
	for _, pair := range pairs {
		out = append(out, pair...)
	}
	return out
}

// TestAMicrovmIsNotAskedForFirmware is the other half of the same machine: `FirmwareArgs` returns
// nothing for a microvm, so `TouchOvmfVars` must not run before it and require a file the guest
// cannot use.
//
// MEASURED on a GitHub runner with no `ovmf` package:
//
//	boot: firmware variables template /usr/share/OVMF/OVMF_VARS_4M.fd:
//	      open /usr/share/OVMF/OVMF_VARS_4M.fd: no such file or directory
//
// - for a guest that boots a kernel handed to it by the host.
func TestAMicrovmIsNotAskedForFirmware(t *testing.T) {
	body, err := readSrc(t, "driver.go")
	if err != nil {
		t.Fatal(err)
	}
	code := dropLineComments(string(body))
	// A CALL, NOT A MENTION: this file's own comment quotes the call and the error it produced, and
	// `dropLineComments` removes whole-line comments but not the prose inside a doc block that
	// happens to be indented with the code. Matching "machine.TouchOvmfVars(\n" - the call on its
	// own line - finds the statement.
	i := strings.Index(code, "err := machine.TouchOvmfVars(")
	if i < 0 {
		// THE CALL IS CONDITIONAL ON THE SHAPE, and a shape with no pflash needs nothing copied. What
		// must never happen is an UNCONDITIONAL copy: a microvm has no firmware, and `TouchOvmfVars`
		// against a missing template fails the boot with a sentence about OVMF.
		t.Skip("no TouchOvmfVars call: the firmware setup has moved to the shapes")
	}
	// THE ENCLOSING `if` IS THE GUARD, AND IT IS FOUND BY BRACKET DEPTH rather than by text: the
	// call sits inside a block, the block sits inside a condition, and a `//` comment above the call
	// leaves indentation behind, so "the last line that starts with `if `" is not reliable.
	open := -1
	depth := 0
	for at := i; at >= 0; at-- {
		switch code[at] {
		case '}':
			depth++
		case '{':
			if depth == 0 {
				open = at
			} else {
				depth--
			}
		}
		if open >= 0 {
			break
		}
	}
	if open < 0 {
		t.Fatal("TouchOvmfVars is not inside a block, so the firmware setup has moved")
	}
	// the `if` that owns this block is the line above the brace
	lineStart := strings.LastIndex(code[:open], "\n") + 1
	guard := code[lineStart:open]
	if !strings.HasPrefix(strings.TrimSpace(guard), "if ") {
		t.Fatalf("TouchOvmfVars is inside %q, which is not an `if` - so no condition excludes a "+
			"microvm from the OVMF template it cannot use", strings.TrimSpace(guard))
	}
	if !strings.Contains(guard, "MicroVM") {
		t.Fatalf("TouchOvmfVars is guarded by %q, which does not exclude a microvm. A microvm has "+
			"no firmware, so it must not require the OVMF template to exist", strings.TrimSpace(guard))
	}
}
