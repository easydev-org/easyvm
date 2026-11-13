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
	// THE TARGET IS A virtio DEVICE, WHICH IS NOW SAID BY THE `-device` RATHER THAN BY `if=virtio`:
	// `bootindex` cannot go on a `-drive` ("Block format 'qcow2' does not support the option
	// 'bootindex'"), so the drive is `if=none,id=target` and the device names it.
	if !strings.Contains(args, "virtio-blk-pci,drive=target") {
		t.Fatalf("the target must be virtio; got %q", args)
	}
	if !strings.Contains(args, "if=none") {
		t.Fatalf("the target drive must be `if=none` for its device to attach; got %q", args)
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
	order := []string{filepath.Base(iso), filepath.Base(answer), "id=target", filepath.Base(driver)}
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

// BSD IS A DIFFERENT OPERATING SYSTEM ON THE SAME MACHINE.
//
// GhostBSD boots the way every Linux here does - UEFI firmware, a virtio disk, std VGA - because
// lib/ssh_step.py built it with the same `uefi.shape_with_uefi(shape, work, kind)` call every Linux
// distribution uses. So `os: bsd` is its own NAME and not its own machine: the name appears in logs
// and in a file, and nothing else about the guest changes.
//
// THE FIRST CI RUN OF THE THIRTEEN LINUX/BSD LEGS IS WHY THIS EXISTS AT ALL: those files had no
// `boot:` step, because in the Python implementation lib/ssh_step.py started the machine before
// running them. Reproducing that as a file means the file has to say `os:` - and `os: bsd` was
// refused by ParseOS, so a file that named it correctly would still not boot.
func TestBSDIsAcceptedAndBootsLikeLinux(t *testing.T) {
	os_, err := ParseOS("bsd")
	if err != nil {
		t.Fatalf("`os: bsd` must be accepted: %v", err)
	}
	if !os_.IsLinuxLike() {
		t.Fatal("bsd must be linux-like: same firmware, same disk bus, same video adapter")
	}
	if os_.IsMacOS() {
		t.Fatal("bsd is not macOS; it must not get OpenCore, the SMC or the Apple CPU flags")
	}

	disk, iso, answer, driver, _, _, _, _ := fixture(t)
	_ = answer
	_ = driver
	installed := Boot{OS: BSD, Disk: disk}
	if err := installed.Validate(); err != nil {
		t.Fatal(err)
	}
	// THE STORAGE IS ONE VIRTIO DISK AND THE SAME DISCARD RULE AS LINUX.
	args := strings.Join(installed.StorageArgs(), " ")
	if !strings.Contains(args, "virtio-blk-pci,drive=target") || !strings.Contains(args, "discard=unmap") {
		t.Fatalf("a booted BSD disk must be one virtio disk with discard on:\n%s", args)
	}
	// AND AN INSTALLER ISO TURNS DISCARD OFF, the same rule as every other family.
	installing := Boot{OS: BSD, Disk: disk, ISO: iso}
	if strings.Contains(strings.Join(installing.StorageArgs(), " "), "discard=unmap") {
		t.Fatal("an installing BSD boot must not discard")
	}
	// AND IT GETS NO APPLE OR WINDOWS PARTS.
	if len(installed.SMCArgs("")) != 0 || len(installed.ExtraArgs()) != 0 {
		t.Fatal("bsd must not get the SMC device or the macOS ICH9 arguments")
	}
}

// KVM IS NAMED, NOT NEGOTIATED.
//
// `-machine q35` WITH NO `-accel` LETS QEMU CHOOSE, and qemu's default is `tcg` - pure software
// emulation. It does not fail and it does not visibly warn: the guest boots, everything takes fifty
// times as long, and on macOS the twenty CPU flags it needs are refused one by one:
//
//	qemu-system-x86_64: warning: TCG doesn't support requested feature:
//	  CPUID[80000007h].EDX.invtsc [bit 8]
//
// `+invtsc` is the flag whose absence deadlocks macOS at `[EB|LOG:HANDOFF TO XNU]` with every vCPU at
// 100%, which internal/machine's own comment records. On TCG that is not a risk, it is the outcome.
//
// THESE TESTS ARE ABOUT THE TWO HALVES OF THE GUARANTEE: the command line names the accelerator, and
// the check behind it says what is wrong when the device is not usable - because "qemu exited 1" is a
// message about neither.
func TestTheAcceleratorIsInTheCommandLine(t *testing.T) {
	body, err := os.ReadFile("../daemon/driver.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	if !strings.Contains(src, "accel=kvm") {
		t.Fatal("the qemu command line does not name `accel=kvm`. Without it qemu defaults to TCG: the " +
			"guest boots, runs at a fiftieth of the speed, and macOS cannot be given `+invtsc` - the " +
			"flag whose absence deadlocks it at HANDOFF TO XNU")
	}
	if !strings.Contains(src, "machine.CheckKVM()") {
		t.Fatal("CheckKVM exists and nothing calls it, so a reader gets no sentence about /dev/kvm " +
			"when a boot is slow or qemu exits")
	}
	// AND IT MUST NOT BE A GATE. A check that can refuse a guest qemu would have started is worse than
	// none: `KVM_GET_API_VERSION` passed a pointer where the ioctl takes no argument and reported
	// "invalid argument" on a machine whose qemu had just accepted `-accel kvm`.
	if strings.Contains(src, "if err := machine.CheckKVM(); err != nil {\n\t\treturn err") {
		t.Fatal("CheckKVM is a gate again. Qemu is the authority on whether it can start: `-accel kvm` " +
			"makes it refuse loudly, and this check must only explain")
	}
}

// AND THE CHECK REPORTS SOMETHING THIS MACHINE DOES OR DOES NOT HAVE, rather than passing by being
// untested. It must not PANIC and must not claim an answer it did not measure.
func TestCheckKVMAnswersForThisMachine(t *testing.T) {
	err := CheckKVM()
	if err == nil {
		t.Log("this machine can accelerate guests")
		return
	}
	// A machine without KVM must get a message naming the device and the consequence.
	for _, want := range []string{"/dev/kvm"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal must name %q; got %v", want, err)
		}
	}
	t.Logf("no KVM here, as expected for a test runner: %v", err)
}

// THE RECOVERY DEVICE NEEDS A USB CONTROLLER, AND ITS ABSENCE KILLED THE FIRST REAL OPENCORE BOOT.
//
// `usb-storage,bus=xhci.0` NAMES A BUS NOTHING CREATED unless a `qemu-xhci` device is added, and qemu
// refuses at startup:
//
//	-device usb-storage,bus=xhci.0,drive=rec: Bus 'xhci.0' not found
//
// THEN IT EXITS, so the QMP socket never appears and the boot step reports a socket error - a
// hypothesis about qmp.Registry, pointing a reader away from the machine that never started.
//
// THIS TEST IS ABOUT THE ORDER AS WELL AS THE PRESENCE: the controller must be added BEFORE the device
// that names its bus, because qemu resolves `bus=` as it parses.
func TestTheRecoveryDeviceHasAController(t *testing.T) {
	disk, _, _, _, recovery, rom, vars, bootImg := fixture(t)
	b := Boot{OS: MacOS, Disk: disk, Recovery: recovery,
		OpenCoreROM: rom, OpenCoreVars: vars, BootImage: bootImg}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	args := b.StorageArgs()
	joined := strings.Join(args, " ")

	controller := -1
	for i, a := range args {
		if strings.HasPrefix(a, "qemu-xhci") {
			controller = i
		}
	}
	if controller < 0 {
		t.Fatalf("no qemu-xhci device, so `bus=xhci.0` names a bus that does not exist and qemu "+
			"refuses to start:\n%s", joined)
	}
	// THE CONTROLLER MUST BE DECLARED BEFORE THE DEVICE THAT USES IT.
	usb := strings.Index(joined, "usb-storage")
	if usb < 0 {
		t.Fatalf("the recovery image is not attached as usb-storage:\n%s", joined)
	}
	// Compare positions in the joined string, which is where qemu parses them.
	if at := strings.Index(joined, "qemu-xhci"); at > usb {
		t.Fatalf("`usb-storage,bus=xhci.0` comes before the controller it names:\n%s", joined)
	}

	// AND WITHOUT A RECOVERY IMAGE THERE IS NEITHER THE DEVICE NOR THE CONTROLLER: an installed macOS
	// disk does not install, and an extra USB disk is one more row in Disk Utility's sidebar.
	installed := Boot{OS: MacOS, Disk: disk, OpenCoreROM: rom, OpenCoreVars: vars, BootImage: bootImg}
	body := strings.Join(installed.StorageArgs(), " ")
	if strings.Contains(body, "qemu-xhci") || strings.Contains(body, "usb-storage") {
		t.Fatalf("a macOS boot with no recovery image attached USB devices anyway:\n%s", body)
	}
}

// THE GUEST AGENT'S PORT AND THE SCREEN SIZE, BOTH OF WHICH WERE MISSING FROM THE COMMAND LINE.
//
// TWO FAILURES THAT LOOKED LIKE GUEST PROBLEMS AND WERE ARGUMENTS:
//
//	prove the guest agent is running: the command exited 3
//	  THREE IS `systemctl is-active`'s "inactive". The agent was installed and could not start because
//	  `/dev/virtio-ports/org.qemu.guest_agent.0` is created by a `virtserialport` on a `virtio-serial`
//	  bus, and neither device was in the command line.
//
//	wait for the Console result: `region_at` is [1920, 1080] but this frame is 1280x800
//	  EVERY WALKTHROUGH HERE WAS MEASURED AT 1920x1080, and `-vga std` brings a guest up at qemu's own
//	  default 1280x800. Only the `-device VGA` form takes `xres`/`yres`.
//
// BOTH ARE ASSERTED ON THE DRIVER'S SOURCE, because they are assembled there rather than by
// internal/machine - and a test that only ran the machine would need qemu and a guest for each.
func TestTheGuestAgentPortAndTheScreenSizeAreInTheCommandLine(t *testing.T) {
	body, err := os.ReadFile("../daemon/driver.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)

	// THE AGENT'S PORT, BOTH TRANSPORTS.
	if !strings.Contains(src, "virtio-serial") ||
		!strings.Contains(src, "virtserialport,chardev=qga0,name=org.qemu.guest_agent.0") {
		t.Fatal("the guest agent's virtio-serial port is not in the command line, so a guest that " +
			"installs qemu-guest-agent cannot start it - `systemctl is-active` returns 3 and the " +
			"step reports a stopped agent")
	}
	if !strings.Contains(src, "isa-serial,chardev=qga0") {
		t.Fatal("macOS does not get `isa-serial` for its agent. Apple's own agent never loads under " +
			"QEMU and the third-party one probes /dev/cu.serial1, which is the FIRST isa-serial device")
	}
	if !strings.Contains(src, "server=on,wait=off") {
		t.Fatal("the agent's chardev does not use `server=on,wait=off`, so qemu would block waiting " +
			"for a host to connect before the guest can start")
	}

	// THE SCREEN SIZE, WHICH `-vga std` CANNOT CARRY.
	if !strings.Contains(src, "xres=1920,yres=1080") {
		t.Fatal("the video adapter does not set xres/yres. Every walkthrough here was measured at " +
			"1920x1080 and a guest on qemu's default 1280x800 puts every coordinate on a different " +
			"screen - measured as `region_at is [1920, 1080] but this frame is 1280x800`")
	}
	if !strings.Contains(src, "vgamem_mb=32") {
		t.Fatal("no vgamem_mb. The default is 8 MiB and 1920x1080 at 32bpp is 7.91 MiB before double " +
			"buffering, so the adapter cannot hold a frame - and the symptom is a BLACK screen from " +
			"the first capture, with no error")
	}
	// AND macOS STILL GETS `-vga none`, because its adapter is vmware-svga from ExtraArgs.
	if !strings.Contains(src, `"-vga", "none"`) {
		t.Fatal("the `-vga none` branch is gone; macOS would get a second adapter alongside its " +
			"vmware-svga device")
	}
}

// EVERY MEDIUM IS IN THE BOOT ORDER, AND THE ONE THAT INSTALLS IS FIRST.
//
// MEASURED ON UBUNTU-INSTALL: `console.log` grew to 24 KB - the firmware had booted something - while
// every frame was `drop=+0.0 ratio=0.0x sat=+0.00` for fifteen minutes. A BLACK MACHINE.
//
// The comment in `StorageArgs` said "the firmware boots the first thing it can", which assumed OVMF
// would scan the CD-ROM and find its El Torito image by itself. A FRESH OVMF_VARS HAS NO SAVED BOOT
// ENTRY, so that scan is the firmware's own choice rather than a request - and the note on the macOS
// path already recorded the lesson for its own disk:
//
//	"a disk with no bootindex is not in the boot order at all" - the firmware then reports it could
//	not load Boot0001, six runs in a row
//
// IT WAS APPLIED ONLY WHERE macOS NEEDED IT. This asserts it for a Linux installer and a Windows one,
// because the reason is about OVMF rather than about a guest.
func TestEveryMediumThatCanBootIsInTheBootOrder(t *testing.T) {
	for _, tc := range []struct {
		os   OS
		name string
	}{
		{Linux, "linux"},
		{Windows, "windows"},
	} {
		disk, iso, _, _, _, _, _, _ := fixture(t)
		b := Boot{OS: tc.os, ISO: iso, Disk: disk}
		if err := b.Validate(); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		args := strings.Join(b.StorageArgs(), " ")

		// THE INSTALLER IS bootindex=0, because it is what has to run.
		if !strings.Contains(args, "bootindex=0") {
			t.Fatalf("%s: nothing is bootindex=0, so the firmware is left to find a boot medium by "+
				"scanning - and a fresh OVMF_VARS has nothing saved:\n%s", tc.name, args)
		}
		// AND THE TARGET IS NEXT, not implicitly first by being a disk.
		if !strings.Contains(args, "bootindex=1") {
			t.Fatalf("%s: the target disk is not bootindex=1, so it could outrank the installer:\n%s",
				tc.name, args)
		}
	}
}

// `bootindex` IS A DEVICE PROPERTY AND CANNOT BE PASSED ON A `-drive`.
//
// MEASURED, AND IT IS THE FIRST THING TO REMEMBER ABOUT THIS FILE:
//
//	qemu-system-x86_64: -drive file=...,if=virtio,format=qcow2,bootindex=0,discard=unmap:
//	Block format 'qcow2' does not support the option 'bootindex'
//
// AND THE STEP THAT FAILED SAID SOMETHING ELSE ENTIRELY:
//
//	boot the disk: qmp .../drive.sock.qmp: connect: connection refused (waited 30s)
//
// qemu exits on a bad argument BEFORE it creates the QMP socket, so EVERY argument mistake in this
// file presents as "the socket never appeared" - which reads as a stale socket or a second client, and
// is neither. The daemon's own `qemu ...` log line is the only place the real complaint is written.
//
// THE SHAPE THAT WORKS, and the macOS branch already used it: the drive is `if=none,id=X` and a
// `-device` names X and carries the index.
func TestBootindexIsOnADeviceNotADrive(t *testing.T) {
	disk, iso, answer, driver, _, _, _, _ := fixture(t)
	for _, b := range []Boot{
		{OS: Linux, ISO: iso, Disk: disk},
		{OS: Windows, ISO: iso, Disk: disk, AnswerCD: answer, DriverCD: driver},
		{OS: BSD, Disk: disk},
	} {
		args := b.StorageArgs()
		for i, a := range args {
			// THE VALUE OF `-drive` IS THE ARGUMENT AFTER THE FLAG.
			if a == "-drive" && i+1 < len(args) && strings.Contains(args[i+1], "bootindex") {
				t.Fatalf("a `-drive` carries bootindex, and qemu refuses to start with "+
					"\"Block format 'qcow2' does not support the option 'bootindex'\" - the process "+
					"then exits before its QMP socket exists:\n%s", args[i+1])
			}
		}
		// AND THE REVERSE: the index exists somewhere, on a -device.
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "bootindex=") {
			t.Fatalf("%s: no bootindex anywhere, so the firmware is left to scan", b.OS)
		}
		if !strings.Contains(joined, "-device virtio-blk-pci,drive=target,bootindex=") &&
			!strings.Contains(joined, "virtio-blk-pci,drive=target,bootindex=") {
			// A boot DISK with no installer still needs the index on its device.
			t.Fatalf("%s: the target's bootindex is not on its `-device`:\n%s", b.OS, joined)
		}
	}
}

// q35's IDE CONTROLLER HAS EXACTLY ONE UNIT, AND windows-install ATTACHES THREE CDs.
//
//	qemu-system-x86_64: -device ide-cd,drive=media1,bootindex=1:
//	Can't create IDE unit 1, bus supports only 1 units
//
// MEASURED ON windows-install, WHICH IS THE ONLY STAGE WITH ALL THREE: the installer, the answer CD
// carrying the virtio drivers, and a driver CD for the installed system. Only the INSTALLER has to be
// on a bus the firmware boots from.
//
// SO THE INSTALLER TAKES THE ONE IDE UNIT and the rest go on USB - a bus with as many ports as there
// are devices, and the same `qemu-xhci` the macOS branch already creates for its recovery disk.
func TestAtMostOneMediumIsOnTheIDEBus(t *testing.T) {
	disk, iso, answer, driver, _, _, _, _ := fixture(t)
	b := Boot{OS: Windows, ISO: iso, AnswerCD: answer, DriverCD: driver, Disk: disk}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	args := b.StorageArgs()
	joined := strings.Join(args, " ")

	ide := strings.Count(joined, "ide-cd")
	if ide > 1 {
		t.Fatalf("%d devices on the IDE bus and q35 supports ONE; qemu refuses with \"Can't create "+
			"IDE unit 1\" and then exits before its QMP socket exists:\n%s", ide, joined)
	}
	// AND THE ONE THAT IS THERE IS THE INSTALLER, because the firmware has to boot it.
	if !strings.Contains(joined, "ide-cd,drive=media0,bootindex=0") {
		t.Fatalf("the installer is not the single IDE device, so it may not be bootable:\n%s", joined)
	}
	// AND THE OTHER TWO ARE ON USB, attached to a controller that exists.
	if strings.Count(joined, "usb-storage,bus=xhci.0") != 2 {
		t.Fatalf("the answer and driver CDs are not both on USB:\n%s", joined)
	}
	if strings.Count(joined, "qemu-xhci,id=xhci") != 1 {
		t.Fatalf("the xhci controller is created %d times; `usb-storage,bus=xhci.0` names a bus "+
			"that does not exist unless something creates it, and two controllers would collide on "+
			"the id:\n%s", strings.Count(joined, "qemu-xhci,id=xhci"), joined)
	}
	// AND THE XHCI COMES FIRST, because qemu resolves `bus=xhci.0` as it parses.
	xhci := strings.Index(joined, "qemu-xhci,id=xhci")
	usb := strings.Index(joined, "usb-storage,bus=xhci.0")
	if xhci < 0 || usb < xhci {
		t.Fatalf("a `usb-storage` names `bus=xhci.0` before the controller is defined, which qemu "+
			"refuses while parsing:\n%s", joined)
	}
}

// THE MACOS ADAPTER IS BARE `vmware-svga`, AND ASKING FOR 256 MB OF FRAMEBUFFER BROKE THE GUEST.
//
// THE MEASUREMENT IS TWO RUNS ON ADJACENT COMMITS, SAME WALKTHROUGH, SAME MACHINE:
//
//	22eeda3   bare vmware-svga        1803 frames captured,   3 lost to a failed screendump
//	5ba0c18   vgamem_mb=256             92 frames captured, 264 lost to a failed screendump
//
// THREE QUARTERS OF THE RECORDING WENT MISSING, and the 92 frames that survived are BLACK at mean
// luminance 0.6. The recovery system never drew anything, so `wait for the recovery menu` read
// `Ul ag` - OCR finding letters in noise - for its whole thirty-minute budget.
//
// THE PYTHON IMPLEMENTATION DOES SET `vgamem_mb=256`, WHICH IS WHY THIS LOOKED RIGHT, AND THE
// DIFFERENCE IS WHICH SYSTEM IS RUNNING: an INSTALLED macOS has VMwareGfx loaded and can use the
// memory. THE RECOVERY SYSTEM HAS NO SUCH DRIVER, so the request does not help it and instead
// disturbs whatever mode the firmware had established.
//
// THE 640x480 IS NOT A MEMORY PROBLEM. OpenCore's GOP leaves the guest at that mode before macOS
// loads a display driver, and no adapter argument changes when that happens.
func TestTheMacOSAdapterIsBareVmwareSVGA(t *testing.T) {
	body, err := os.ReadFile("boot.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	if strings.Contains(src, `"vmware-svga,vgamem_mb=256"`) {
		t.Fatal("the macOS adapter asks for vgamem_mb=256. Measured: 1803 frames and 3 losses " +
			"with a bare `vmware-svga` against 92 frames and 264 losses with the argument - three " +
			"quarters of the recording lost and the rest black. The recovery system has no " +
			"VMwareGfx driver to use that memory with")
	}
	if !strings.Contains(src, `"-device", "vmware-svga"`) {
		t.Fatal("the macOS adapter is no longer `vmware-svga`; virtio-vga left a 7 MB framebuffer " +
			"with `No Kext Loaded`, so the wallpaper layer could not be drawn")
	}
}
