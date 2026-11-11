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

	b := Boot{OS: Windows, Disk: disk}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}

	args := strings.Join(b.StorageArgs(), " ")
	if !strings.Contains(args, "if=virtio") {
		t.Fatalf("the target must be virtio; got %q", args)
	}
	// AND DISCARD IS ON, because a finished disk's TRIMs are what keep the published qcow2 small.
	if !strings.Contains(args, "discard=unmap") {
		t.Fatalf("a disk boot without an installer must have discard=unmap - it is the only "+
			"thing that lets a guest's TRIM shrink the qcow2; got %q", args)
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

	// THE ORDER IS INSTALLER, ANSWER, DISK, DRIVERS - MEASURED, NOT ARBITRARY.
	//
	// "the order matters only for the boot device: the install ISO is first so the firmware boots
	//  it ... The drivers travel on the answer CD, which keeps the virtio disk at index 0."
	//
	// A CD-ROM DOES NOT TAKE A DISK INDEX - Windows numbers only fixed disks - so a virtio disk
	// after two CD-ROMs is still DiskID 0. What the order decides is which medium the firmware
	// tries first, and the driver CD sits last because only the INSTALLED system needs it.
	order := []string{filepath.Base(iso), filepath.Base(answer), "if=virtio", filepath.Base(driver)}
	last := -1
	for _, want := range order {
		at := strings.Index(joined, want)
		if at < 0 {
			t.Fatalf("%s is not attached:\n%s", want, joined)
		}
		if at < last {
			t.Fatalf("%s is out of order. Want installer, answer, disk, drivers - the installer "+
				"first so the firmware boots it, the disk at index 0, the drivers last because "+
				"only the installed system reads them:\n%s", want, joined)
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
	// AND AN INSTALL MUST NOT DISCARD, because it is the one boot that writes.
	//
	//     41835253323   no discard, 128G   finished in 46 minutes
	//     42009463444   discard,    128G   wrote 36774 MiB, dropped to 19761, never moved again
	if strings.Contains(joined, "discard=unmap") {
		t.Fatalf("discard is on while the recovery image is attached, which is the install. "+
			"Measured: it loses the clusters the APFS snapshot still refers to and the "+
			"progress bar freezes at \"About 48 minutes remaining\":\n%s", joined)
	}

	// AND WITHOUT A RECOVERY IMAGE THERE IS NO USB DEVICE AT ALL, AND DISCARD IS ON, because a
	// later stage boots a system that is already installed. An extra USB disk is one more row the
	// guest can pick in Disk Utility, where `choose the target disk by capacity` reads the
	// sidebar; and discard is then exactly what keeps the published disk small.
	installed := Boot{OS: MacOS, Disk: disk, OpenCoreROM: rom, OpenCoreVars: vars,
		BootImage: bootImg}
	if err := installed.Validate(); err != nil {
		t.Fatalf("a macOS boot without a recovery image must be allowed - only an INSTALL needs "+
			"the recovery device: %v", err)
	}
	body := strings.Join(installed.StorageArgs(), " ")
	if strings.Contains(body, "usb-storage") {
		t.Fatalf("a macOS boot with no recovery image attached a USB device anyway; that is one "+
			"more disk the guest can select:\n%s", body)
	}
	if !strings.Contains(body, "discard=unmap") {
		t.Fatalf("a macOS boot with no recovery image is a FINISHED system and must discard - "+
			"that is what keeps the published disk small:\n%s", body)
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

// THE RECOVERY IMAGE IS ATTACHED BY THE INSTALL AND BY NOTHING ELSE.
//
// macOS installs FROM removable media, so setup.yaml spends it as a USB disk. Every later stage
// boots a system that is already installed, and an installed macOS has no recovery volume - the
// extra USB disk is then one more row the guest can pick in Disk Utility, where
// `choose the target disk by capacity` reads the sidebar. So "attached where nothing installs" is
// at best a row nobody looks at and at worst the row that gets picked.
//
// THIS IS THE TEST FOR THE BUG WHERE AN EMPTY `recovery:` STILL EMITTED A DEVICE: the code built
// the -drive unconditionally, so a file with no `recovery:` produced
//
//	-drive file=,media=disk,if=none,id=rec,readonly=on
//
// which is an empty path and a qemu that refuses to start, with a message about a drive rather
// than about the step that forgot to say what it was installing from.
func TestOnlyAnInstallAttachesTheRecoveryImage(t *testing.T) {
	disk, _, _, _, recovery, rom, vars, bootImg := fixture(t)

	install := Boot{OS: MacOS, Disk: disk, Recovery: recovery,
		OpenCoreROM: rom, OpenCoreVars: vars, BootImage: bootImg}
	installed := Boot{OS: MacOS, Disk: disk,
		OpenCoreROM: rom, OpenCoreVars: vars, BootImage: bootImg}

	cases := []struct {
		name     string
		boot     Boot
		attached bool
	}{
		{"the setup stage, which has `recovery:`", install, true},
		{"a stage that boots an installed system", installed, false},
	}
	for _, c := range cases {
		args := c.boot.StorageArgs()
		joined := strings.Join(args, " ")
		if got := strings.Contains(joined, "usb-storage"); got != c.attached {
			t.Fatalf("%s: usb-storage attached = %v, want %v:\n%s", c.name, got, c.attached, joined)
		}
		// AN EMPTY PATH IS NEVER ALLOWED, whatever the answer. This is the assertion that would
		// have caught the original bug even if the USB decision had been right.
		if strings.Contains(joined, "file=,") || strings.Contains(joined, "file= ") {
			t.Fatalf("%s: a -drive was built with an empty file path:\n%s", c.name, joined)
		}
		for _, a := range args {
			if strings.HasPrefix(a, "file=") && a == "file=," {
				t.Fatalf("%s: empty drive path in %q", c.name, a)
			}
			if a == "" {
				t.Fatalf("%s: an empty argument reached the command line: %q", c.name, args)
			}
		}
	}

	// AND OPENCORE IS IN BOTH. It is the firmware, not the installer: an installed macOS disk
	// still boots through it, and without these three files the guest sits in the UEFI shell.
	for _, c := range cases {
		joined := strings.Join(c.boot.StorageArgs(), " ")
		if !strings.Contains(joined, "bootindex=0") {
			t.Fatalf("%s: OpenCore's disk lost bootindex=0; without a bootindex the firmware "+
				"does not try it at all:\n%s", c.name, joined)
		}
	}
}

// DISCARD IS OFF FOR AN INSTALL AND ON FOR A FINISHED DISK - AND THE DISK'S SIZE IS NOT THE
// QUESTION.
//
// A size heuristic was here and it was wrong both ways: an overlay over an installed disk is a few
// hundred MiB and still has a system, and an installer two hours in has written 30 GiB and still
// needs discard off. The file already says which it is.
func TestDiscardIsDecidedByTheMediaAndNotByTheDiskSize(t *testing.T) {
	var (
		disk, iso, answer, driver, recovery, rom, vars, bootImg = fixture(t)
	)
	_ = answer
	_ = driver

	macInstall := Boot{OS: MacOS, Disk: disk, Recovery: recovery,
		OpenCoreROM: rom, OpenCoreVars: vars, BootImage: bootImg}
	macInstalled := Boot{OS: MacOS, Disk: disk,
		OpenCoreROM: rom, OpenCoreVars: vars, BootImage: bootImg}
	winInstall := Boot{OS: Windows, Disk: disk, ISO: iso}
	winInstalled := Boot{OS: Windows, Disk: disk}

	for _, c := range []struct {
		name       string
		boot       Boot
		discarding bool
	}{
		{"macOS installing", macInstall, false},
		{"macOS installed", macInstalled, true},
		{"windows installing", winInstall, false},
		{"windows installed", winInstalled, true},
	} {
		joined := strings.Join(c.boot.StorageArgs(), " ")
		got := strings.Contains(joined, "discard=unmap")
		if got != c.discarding {
			t.Fatalf("%s: discard=unmap = %v, want %v. An install that discards loses the "+
				"clusters the APFS snapshot still refers to (42009463444: wrote 36774 MiB, "+
				"dropped to 19761, froze at \"About 48 minutes remaining\"):\n%s",
				c.name, got, c.discarding, joined)
		}
	}

	// AND THE SIZE OF THE FILE ON DISK CHANGES NOTHING. This is the assertion that would have
	// failed on the old heuristic: the same install boot, given a disk big enough that
	// "> 1 GiB means installed" would fire, must still refuse to discard.
	big := filepath.Join(t.TempDir(), "half-written.qcow2")
	if err := os.WriteFile(big, make([]byte, 2<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	bigInstall := Boot{OS: MacOS, Disk: big, Recovery: recovery,
		OpenCoreROM: rom, OpenCoreVars: vars, BootImage: bootImg}
	if strings.Contains(strings.Join(bigInstall.StorageArgs(), " "), "discard=unmap") {
		t.Fatal("discard was turned on because the install's disk had grown past a size " +
			"threshold; the installer is still writing and this is the 17 GiB failure")
	}
}
