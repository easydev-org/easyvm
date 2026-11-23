package machine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A DISK THAT IS BEING INSTALLED ONTO MUST NOT DISCARD, AND AN INSTALLED ONE MUST.
//
// THE TWO HALVES ARE MEASURED AND THEY ARE OPPOSITE:
//
//	macOS install with discard    42009463444 wrote 36774 MiB, dropped to 19761, froze
//	macOS install without         41835253323 finished in 46 minutes
//	a finished disk being booted  discard is what keeps the published qcow2 small - an installed
//	                              macOS 26 disk showed 12 GiB used on the guest and 38.4 GiB
//	                              allocated on the host, 26 GiB of freed blocks
//
// WHAT DECIDES IS WHAT THE ARGUMENTS ATTACH, not a guest's name and not a file size. A size heuristic
// is wrong in both directions: a fresh 128G qcow2 is 196 KiB and an installed one is 30 GiB, but an
// overlay over an installed disk is a few hundred MiB and still has a system.
func TestDiscardIsDecidedByWhatIsAttached(t *testing.T) {
	for _, c := range []struct {
		name  string
		boot  Boot
		trims bool
	}{
		{
			name: "an installer is attached",
			boot: Boot{Disk: "/w/disk.qcow2", Args: []string{
				"-drive", "file=/w/installer.iso,media=cdrom,readonly=on,if=none,id=media0",
				"-device", "ide-cd,drive=media0,bootindex=0",
			}},
		},
		{
			name: "a recovery image is attached as a readonly disk",
			boot: Boot{Disk: "/w/disk.qcow2", Recovery: "/w/recovery.dmg", Args: []string{
				"-drive", "file=/w/recovery.dmg,media=disk,if=none,id=rec,readonly=on",
				"-device", "usb-storage,bus=xhci.0,drive=rec",
			}},
		},
		{
			name:  "an installed system has nothing attached",
			boot:  Boot{Disk: "/w/disk.qcow2", Args: []string{"-drive", "file=/w/disk.qcow2,if=none,format=qcow2,id=target"}},
			trims: true,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := c.boot.DiscardFragment() != ""; got != c.trims {
				t.Fatalf("discard enabled = %v, want %v. An install that discards loses the clusters "+
					"the installer's own snapshot still refers to and freezes at \"About 48 minutes "+
					"remaining\"", got, c.trims)
			}
		})
	}

	// AND THE SIZE OF THE FILE CHANGES NOTHING: the same install boot with a disk big enough that
	// "> 1 GiB means installed" would fire must still refuse to discard.
	big := filepath.Join(t.TempDir(), "half-written.qcow2")
	if err := os.WriteFile(big, make([]byte, 2<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	install := Boot{Disk: big, Args: []string{
		"-drive", "file=/w/i.iso,media=cdrom,readonly=on,if=none,id=media0",
		"-device", "ide-cd,drive=media0",
	}}
	if install.DiscardFragment() != "" {
		t.Fatal("discard was turned on because the install's disk had grown past a size threshold")
	}
}

// A FRESH DISK NEEDS NO OVERLAY, and every other boot does.
func TestAFreshDiskNeedsNoOverlay(t *testing.T) {
	if (Boot{Disk: "/d.qcow2", FreshDisk: true}).WantsOverlay() {
		t.Fatal("a fresh disk opened a throwaway diff; there is nothing behind it to protect, and " +
			"the diff is one more file to flatten")
	}
	if !(Boot{Disk: "/d.qcow2"}).WantsOverlay() {
		t.Fatal("a disk that came from somewhere has no diff, so a guest's writes would reach the " +
			"published base")
	}
}

// A BOOT WITH NO ARGUMENTS IS REFUSED, because it asks qemu for a machine with no disk.
func TestABootMustNameItsMachineAndArguments(t *testing.T) {
	dir := t.TempDir()
	disk := filepath.Join(dir, "d.qcow2")
	if err := os.WriteFile(disk, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := (Boot{Disk: disk, Args: []string{"-device", "isa-vga"}}).Validate(); err == nil {
		t.Fatal("a boot with no `machine:` was accepted")
	}
	if err := (Boot{Disk: disk, Machine: "q35"}).Validate(); err == nil {
		t.Fatal("a boot with no `args:` was accepted")
	}
	// AND AN ODD COUNT IS A FLAG WHOSE ARGUMENT WENT MISSING.
	if err := (Boot{Disk: disk, Machine: "q35",
		Args: []string{"-device", "isa-vga", "-drive"}}).Validate(); err == nil {
		t.Fatal("a boot with an odd argument count was accepted; the last flag has no value")
	}
	// AND A PATH THAT IS NOT THERE IS NAMED, not left to qemu.
	err := (Boot{Disk: disk, Machine: "q35",
		Args: []string{"-drive", "file=/nope.iso,media=cdrom"}, ISO: "/nope.iso"}).Validate()
	if err == nil || !strings.Contains(err.Error(), "installer medium") {
		t.Fatalf("a missing installer medium was not named: %v", err)
	}
}

// THE KERNEL LINE GAINS `pci=off` ONLY ON A MACHINE WITH NO PCIe BUS.
//
// MEASURED on NixOS, which died at 0.05s with `PCI: Fatal: No config space access function found` and
// boots with the flag. A DESKTOP microvm has PCIe on purpose - that is where its virtio-gpu lives - so
// it must NOT carry it, or the GPU would vanish.
func TestTheKernelLineSaysPCIeOffOnlyWhenThereIsNoBus(t *testing.T) {
	b := Boot{KernelAppend: "console=ttyS0"}
	if got := b.KernelAppendline("microvm,accel=kvm,pcie=off"); !strings.HasPrefix(got, "pci=off ") {
		t.Fatalf("a machine with `pcie=off` produced %q; a kernel that finds an ACPI root assumes a "+
			"PCI host bridge and stops", got)
	}
	if got := b.KernelAppendline("microvm,accel=kvm,pcie=on"); strings.Contains(got, "pci=off") {
		t.Fatalf("a machine with `pcie=on` produced %q; `pci=off` would hide the bus its "+
			"virtio-gpu is attached to", got)
	}
	// AND A LINE THAT ALREADY SAYS IT IS NOT DOUBLED.
	if got := b.KernelAppendline("pcie=off"); strings.Count(got, "pci=off") != 1 {
		t.Fatalf("the flag was prepended to a line that already had it: %q", got)
	}
}
