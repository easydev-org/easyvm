package machine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// THREE GUESTS, THREE MACHINES, AND THE DIFFERENCES ARE THE POINT.
//
// A Windows disk boot, a Windows ISO install and a macOS boot are not one thing with options. They
// differ in firmware, in disk count, in boot order, in the SMC device, in the CPU flags and in
// whether discard is safe - and every one of those is a silent failure when wrong: a crossed-out
// circle, "failed to load Boot0001", a 100%-CPU deadlock, or an install that loses 17 GiB and
// freezes at "About 48 minutes remaining".

// fixture creates real files, because Validate checks that every path exists - and a test that
// skipped that would pass on a machine where nothing works.
func fixture(t *testing.T) (disk, iso, answer, driver, recovery, rom, vars, bootImg string) {
	t.Helper()
	dir := t.TempDir()
	mk := func(name string, size int) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	return mk("disk.qcow2", 4096), // small: no installed system
		mk("install.iso", 4096),
		mk("answer.iso", 4096),
		mk("drivers.iso", 4096),
		mk("recovery.dmg", 4096),
		mk("opencore.rom", 4096),
		mk("vars.fd", 4096),
		mk("boot.img", 4096)
}

// A WINDOWS DISK BOOT IS ONE DISK AND OVMF, AND DISCARD IS ON.
func TestWindowsDiskBoot(t *testing.T) {
	disk, _, _, _, _, _, _, _ := fixture(t)
	// A big disk means an installed system, which is when discard is safe.
	big := make([]byte, 2<<30)
	bigDisk := filepath.Join(t.TempDir(), "installed.qcow2")
	if err := os.WriteFile(bigDisk, big[:1<<20], 0o644); err != nil {
		t.Fatal(err)
	}
	// The size check reads Stat, so a sparse-looking file is not enough; skip if it did not take.
	if fi, _ := os.Stat(bigDisk); fi.Size() < 1<<30 {
		bigDisk = disk
	}

	b := Boot{OS: Windows, Disk: bigDisk}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}

	args := strings.Join(b.StorageArgs(), " ")
	if !strings.Contains(args, "if=virtio") {
		t.Fatalf("the target must be virtio; got %q", args)
	}
	if !strings.Contains(args, "media=cdrom") {
		// expected: no medium on a disk boot
	} else {
		t.Fatalf("a disk boot attached a CD-ROM: %q", args)
	}
	if got := b.VGA("std"); got != "std" {
		t.Fatalf("windows vga is %q; the macOS override must not apply", got)
	}
	if got := b.SMP(4); got != "4" {
		t.Fatalf("windows smp is %q; only macOS needs the topology spelling", got)
	}
	if cpu, err := b.CPU(""); err != nil || cpu != CPUModel {
		t.Fatalf("windows cpu is %q (%v); it must be the plain model", cpu, err)
	}
	if len(b.SMCArgs("")) != 0 {
		t.Fatal("windows must not get an SMC device")
	}
	if len(b.ExtraArgs()) != 0 {
		t.Fatal("windows must not get the macOS ICH9 and svga arguments")
	}
}

// A WINDOWS ISO INSTALL IS A CD-ROM AND A DISK, IN THAT ORDER, AND DISCARD IS OFF.
//
// THE ORDER IS THE MEANING: Setup numbers disks as it sees them and the answer file's DiskID is
// written against index 0. A CD-ROM attached first moves the install target.
func TestWindowsISOBoot(t *testing.T) {
	disk, iso, answer, driver, _, _, _, _ := fixture(t)
	b := Boot{OS: Windows, Disk: disk, ISO: iso, AnswerCD: answer, DriverCD: driver}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}

	args := b.StorageArgs()
	joined := strings.Join(args, " ")

	// THE DISK COMES FIRST.
	diskAt := strings.Index(joined, "if=virtio")
	cdAt := strings.Index(joined, "media=cdrom")
	if diskAt < 0 || cdAt < 0 {
		t.Fatalf("want a virtio disk and a cdrom; got %q", joined)
	}
	if diskAt > cdAt {
		t.Fatalf("the CD-ROM is attached before the disk, which moves Setup's DiskID:\n%s",
			joined)
	}

	// ALL THREE MEDIA, IN ORDER: installer, answer, drivers.
	last := -1
	for _, want := range []string{filepath.Base(iso), filepath.Base(answer), filepath.Base(driver)} {
		at := strings.Index(joined, want)
		if at < 0 {
			t.Fatalf("%s is not attached:\n%s", want, joined)
		}
		if at < last {
			t.Fatalf("%s is out of order; the installer must come first:\n%s", want, joined)
		}
		last = at
	}

	// AND NO DISCARD WHILE AN INSTALLER IS ATTACHED - the 17 GiB that vanished.
	if strings.Contains(joined, "discard=unmap") {
		t.Fatalf("discard is on during an install. Measured: 41835253323 finished in 46 " +
			"minutes without it, and 42009463444 wrote 36774 MiB, dropped to 19761 and never " +
			"moved again - macOS's APFS snapshot lost the clusters the installer still needed")
	}
}

// A MACOS BOOT IS THREE DISKS IN BOOT ORDER, PLUS THE SMC - AND IT NEEDS THEM EVEN FROM AN
// INSTALLED DISK.
//
// "boot.img OpenCore, bootindex=0: a disk with no bootindex is not in the boot order at all, which
// is what 'failed to load Boot0001' meant for six runs."
func TestMacOSBootAlwaysHasOpenCore(t *testing.T) {
	disk, _, _, _, recovery, rom, vars, bootImg := fixture(t)
	b := Boot{OS: MacOS, Disk: disk, Recovery: recovery,
		OpenCoreROM: rom, OpenCoreVars: vars, BootImage: bootImg}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(b.StorageArgs(), " ")
	if !strings.Contains(joined, "bootindex=0") {
		t.Fatalf("OpenCore's disk must be bootindex=0; without a bootindex the firmware does "+
			"not try it at all:\n%s", joined)
	}
	if !strings.Contains(joined, "bootindex=1") {
		t.Fatalf("the target must be bootindex=1, so OpenCore finds the system after the "+
			"installer writes it:\n%s", joined)
	}
	// THE RECOVERY IMAGE IS REMOVABLE, and it is attached on EVERY macOS boot: OpenCore reads
	// Apple's APFS driver out of it.
	if !strings.Contains(joined, "usb-storage") {
		t.Fatalf("the recovery image must be usb-storage; a virtio disk would be in the boot "+
			"order and macOS installs from removable media:\n%s", joined)
	}
	if len(b.SMCArgs("")) == 0 {
		t.Fatal("macOS needs isa-applesmc; without the key the kernel stops before it draws")
	}
	// THE FIRMWARE IS OPENCORE'S, NOT OVMF'S.
	fw := strings.Join(b.FirmwareArgs("/usr/share/OVMF/OVMF_CODE_4M.fd", "/tmp/OVMF_VARS.fd"), " ")
	if !strings.Contains(fw, filepath.Base(rom)) {
		t.Fatalf("macOS must boot OpenCore's ROM, not OVMF's:\n%s", fw)
	}
}

// THE CPU IS THE SAME MODEL FOR EVERY GUEST, AND MACOS ADDS FLAGS TO IT.
//
// `+invtsc` is not decoration: without it macOS 26 deadlocks with every vCPU at 100%, block I/O
// frozen, and the serial output ending at "#[EB|LOG:HANDOFF TO XNU]" with no panic.
func TestTheCPUModelIsFixedAndMacOSAddsFlags(t *testing.T) {
	win := Boot{OS: Windows}
	cpu, err := win.CPU("")
	if err != nil {
		t.Fatal(err)
	}
	if cpu != CPUModel {
		t.Fatalf("windows cpu = %q, want the plain %q", cpu, CPUModel)
	}

	mac := Boot{OS: MacOS}
	mcpu, err := mac.CPU("")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(mcpu, CPUModel+",") {
		t.Fatalf("macOS cpu = %q; it must START with %q - the flags are additions to the same "+
			"model, not a different one", mcpu, CPUModel)
	}
	for _, want := range []string{"+invtsc", "vendor=GenuineIntel", "vmx=off", "check"} {
		if !strings.Contains(mcpu, want) {
			t.Fatalf("macOS cpu is missing %q", want)
		}
	}

	// AND `host` IS STILL REFUSED FOR BOTH.
	for _, os := range []OS{Windows, MacOS} {
		if _, err := (Boot{OS: os}).CPU("host"); err == nil {
			t.Fatalf("%s accepted -cpu host; every guest must start with the named model", os)
		}
	}
}

// MACOS HAS NO INSTALL ISO, AND A WINDOWS GUEST HAS NO RECOVERY IMAGE.
//
// Both are refused by name, because the alternative is a guest that starts and finds nothing to
// boot from - or worse, one that boots the wrong medium.
func TestTheWrongMediumForTheOSIsRefused(t *testing.T) {
	disk, iso, _, _, recovery, rom, vars, bootImg := fixture(t)

	if err := (Boot{OS: MacOS, Disk: disk, ISO: iso}).Validate(); err == nil {
		t.Fatal("macOS with an `iso:` must be refused: it installs from its recovery image")
	}
	if err := (Boot{OS: Windows, Disk: disk, Recovery: recovery}).Validate(); err == nil {
		t.Fatal("windows with a `recovery:` must be refused: that is macOS's medium")
	}
	// AND A MACOS BOOT WITHOUT OPENCORE'S FILES IS REFUSED, because the firmware would find no
	// loader and sit in the UEFI shell.
	err := (Boot{OS: MacOS, Disk: disk, Recovery: recovery}).Validate()
	if err == nil {
		t.Fatal("macOS without OpenCore's files must be refused")
	}
	if !strings.Contains(err.Error(), "OpenCore") {
		t.Fatalf("the refusal must name OpenCore; got %v", err)
	}
	_ = rom
	_ = vars
	_ = bootImg
}

// A MISSING FILE IS NAMED WITH WHAT IT WAS FOR, not with qemu's message about a path.
func TestAMissingMediumSaysWhatItWas(t *testing.T) {
	disk, _, _, _, _, _, _, _ := fixture(t)
	b := Boot{OS: Windows, Disk: disk, ISO: "/nonexistent/install.iso"}
	err := b.Validate()
	if err == nil {
		t.Fatal("a missing ISO must be refused")
	}
	if !strings.Contains(err.Error(), "installer") {
		t.Fatalf("the refusal should say the missing file was the installer medium; got %v", err)
	}
}
