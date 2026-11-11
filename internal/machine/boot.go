package machine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// A GUEST HAS TWO INDEPENDENT FACTS, AND CONFLATING THEM WAS A MISTAKE I MADE AND CORRECTED.
//
//   OS      windows | linux | macos   decides the firmware, the CPU flags, the SMC device, and
//                                    the shape of the storage bus.
//   MEDIUM  none | iso | recovery      decides whether an installer is attached.
//
// My first version had `source: disk | iso | macos_recovery`, which mixed the two: it implied macOS
// was a kind of medium. It is not. macOS ALWAYS boots through OpenCore - an installed macOS disk and
// a recovery image differ only in whether the recovery image is also attached.
//
// Measured, from macos/15/machine.py's own drives():
//
//	boot.img   OpenCore,  bootindex=0   the firmware must try this first; a disk with no
//	                                   bootindex is not in the boot order at all, which is what
//	                                   "failed to load Boot0001" meant for six runs
//	disk       the target, bootindex=1  after the installer writes a system, OpenCore finds it here
//	recovery   as USB                   macOS installs from REMOVABLE media; the disk it installs TO
//	                                   is not the disk it boots FROM
//
// SO THE THREE-WAY SOURCE IS GONE. `os:` says which machine, and the presence of `iso:` or
// `recovery:` says whether an installer is attached.

// OS decides the machine's shape.
type OS string

const (
	Windows OS = "windows"
	Linux   OS = "linux"
	MacOS   OS = "macos"
)

// IsMacOS reports whether this guest boots through OpenCore.
//
// IT IS THE ONE QUESTION WITH A FAN-OUT: firmware, boot order, SMC, CPU flags, video adapter and the
// SMP topology all turn on it, and each of those is a silent failure when wrong.
func (o OS) IsMacOS() bool { return o == MacOS }

// ParseOS refuses anything that is not a machine this build can start.
func ParseOS(s string) (OS, error) {
	switch OS(s) {
	case Windows, Linux, MacOS:
		return OS(s), nil
	case "":
		return "", fmt.Errorf("a boot step must name `os:` - windows, linux or macos")
	case "android":
		return "", fmt.Errorf("`os: android` is not implemented yet: it boots with -kernel and " +
			"-initrd rather than firmware, and a half-implemented version would fail inside the " +
			"guest rather than here")
	}
	return "", fmt.Errorf("%q is not an os this build starts; the three are windows, linux and "+
		"macos", s)
}

// Boot is what one `boot:` step says.
type Boot struct {
	// OS is which machine this is.
	OS OS
	// Disk is the target, and for a disk boot it is also what boots.
	Disk string
	// ISO is an installer medium. Windows and Linux boot it directly; macOS has no install ISO and
	// uses Recovery instead.
	ISO string
	// Recovery is macOS's recovery image, attached as removable media.
	Recovery string
	// AnswerCD and DriverCD are the two extra media Windows Setup reads: the answer file and the
	// drivers. They exist because Setup finds neither on the install ISO.
	AnswerCD string
	DriverCD string
	// OpenCoreROM and OpenCoreVars are the macOS loader's firmware, built from the recovery image.
	OpenCoreROM  string
	OpenCoreVars string
	// BootImage is OpenCore's own disk image, the one the firmware boots first.
	BootImage string
}

// Validate refuses a boot that cannot work, before qemu is started.
//
// EVERY PATH IS CHECKED HERE RATHER THAN LEFT TO QEMU. qemu's message for a missing `-drive` names
// the path and not the step that forgot to produce it, so a stage that never ran its `fetch:` looks
// like a machine that will not start.
func (b Boot) Validate() error {
	if b.OS == "" {
		return fmt.Errorf("a boot step must name `os:`")
	}
	if b.Disk == "" {
		return fmt.Errorf("booting needs a target disk: a `disk:` step must come first, or pass " +
			"--boot-disk")
	}
	if err := requireFile(b.Disk, "the target disk"); err != nil {
		return err
	}

	if b.ISO != "" {
		if b.OS.IsMacOS() {
			return fmt.Errorf("macOS has no install ISO; it installs from its recovery image. " +
				"Use `recovery:` rather than `iso:`")
		}
		if err := requireFile(b.ISO, "the installer medium"); err != nil {
			return err
		}
	}
	for why, path := range map[string]string{
		"the answer file CD": b.AnswerCD,
		"the driver CD":      b.DriverCD,
	} {
		if path != "" {
			if err := requireFile(path, why); err != nil {
				return err
			}
		}
	}

	if b.Recovery != "" && !b.OS.IsMacOS() {
		return fmt.Errorf("`recovery:` is macOS's install medium; this guest is %s", b.OS)
	}
	if b.OS.IsMacOS() {
		// OPENCORE IS NOT OPTIONAL FOR macOS, EVEN FROM AN INSTALLED DISK. The three files below
		// are what the firmware boots: without them qemu starts, the firmware finds no loader, and
		// the guest sits in the UEFI shell.
		if b.Recovery == "" {
			return fmt.Errorf("a macOS guest needs `recovery:`: OpenCore is built from it - the " +
				"APFS driver comes out of that image - and it is also the install medium when the " +
				"disk has no system yet")
		}
		if err := requireFile(b.Recovery, "the recovery image"); err != nil {
			return err
		}
		for why, path := range map[string]string{
			"OpenCore's firmware ROM": b.OpenCoreROM,
			"OpenCore's variables":    b.OpenCoreVars,
			"OpenCore's boot image":   b.BootImage,
		} {
			if path == "" {
				return fmt.Errorf("%s is missing; a macOS stage must run the step that builds "+
					"OpenCore from the recovery image before booting", why)
			}
			if err := requireFile(path, why); err != nil {
				return err
			}
		}
	}
	return nil
}

func requireFile(path, why string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("no %s at %s: %w", why, path, err)
	}
	if fi.IsDir() {
		return fmt.Errorf("%s is a directory at %s; it must be a file", why, path)
	}
	return nil
}

// --- the command line ------------------------------------------------------------------

// StorageArgs is the disk and the media, in the order the guest reads them.
//
// THE ORDER IS THE MODULE'S JOB BECAUSE IT IS PART OF THE MEANING. Windows Setup numbers disks in
// the order it sees them and the answer file's DiskID is written against index 0, so a CD-ROM
// attached before the disk moves the install target. That is why this is one function and not a
// list a caller assembles.
//
// AND `discard=unmap` IS NOT ALWAYS RIGHT. It is what lets a guest's TRIM free clusters in the
// qcow2, which keeps a published disk small - but during an INSTALL it is destructive. Measured:
//
//	41835253323   no discard,  128G   finished in 46 minutes
//	42009463444   discard,     128G   wrote 36774 MiB, dropped to 19761, never moved again
//
// The 17 GiB that vanished was the installer's own staging data. macOS writes about 36 GiB, takes an
// APFS snapshot, discards its staging volume and switches root; with discard on, every TRIM is
// honoured immediately and the clusters the SNAPSHOT STILL REFERS TO are deallocated under it. The
// progress bar froze at "About 48 minutes remaining".
//
// So discard is off while an installer is attached, and on otherwise.
func (b Boot) StorageArgs() []string {
	// A DISK BOOT OF A MACOS GUEST STILL ATTACHES THE RECOVERY IMAGE - OpenCore uses it for the
	// APFS driver - so "an installer is attached" is what decides discard, not the medium alone.
	installing := b.ISO != "" || (b.OS.IsMacOS() && b.Recovery != "" && !b.hasSystemYet())
	_ = installing // the recovery image is always attached on macOS; see below.

	args := []string{}

	if b.OS.IsMacOS() {
		// OPENCORE'S OWN DISK, FIRST AND WITH bootindex=0. "A disk with no bootindex is not in the
		// boot order at all" - the firmware then reports it could not load Boot0001, six runs in a
		// row.
		args = append(args,
			"-drive", "file="+b.BootImage+",format=raw,if=none,id=oc",
			"-device", "virtio-blk-pci,drive=oc,bootindex=0",
		)
		// THE TARGET, WITH discard ONLY WHEN NOTHING IS INSTALLING.
		target := "file=" + b.Disk + ",if=none,format=qcow2,id=target"
		if !b.installing() {
			target = "file=" + b.Disk + ",if=none,format=qcow2,discard=unmap,id=target"
		}
		args = append(args,
			"-drive", target,
			"-device", "virtio-blk-pci,drive=target,bootindex=1",
		)
		// AND THE RECOVERY IMAGE AS REMOVABLE MEDIA. macOS installs from USB; the disk it installs
		// TO is not the disk it boots FROM. `usb-storage` has no bootindex property and does not
		// need one - the firmware reaches it only after OpenCore hands over.
		args = append(args,
			"-drive", "file="+b.Recovery+",media=disk,if=none,id=rec,readonly=on",
			"-device", "usb-storage,bus=xhci.0,drive=rec",
		)
		return args
	}

	// WINDOWS AND LINUX: THE DISK FIRST, AT INDEX 0.
	//
	// `discard=unmap` IS ON WHEN NOTHING IS INSTALLING, for the same reason and with the opposite
	// sign: after a system is on the disk, its TRIMs are information the qcow2 can use to stay
	// small.
	disk := "file=" + b.Disk + ",if=virtio,format=qcow2"
	if b.ISO == "" {
		disk += ",discard=unmap"
	}
	args = append(args, "-drive", disk)

	// THE MEDIA, IN THE ORDER SETUP EXPECTS. The install ISO first so the firmware finds it, then
	// the answer file, then the drivers - which is the order the Python implementation measured and
	// the reason the disk stays at index 0.
	for _, m := range []struct{ path, why string }{
		{b.ISO, "the installer"},
		{b.AnswerCD, "the answer file"},
		{b.DriverCD, "the drivers"},
	} {
		if m.path == "" {
			continue
		}
		args = append(args, "-drive", "file="+m.path+",media=cdrom,readonly=on")
	}
	return args
}

// hasSystemYet reports whether the target carries an installed system.
//
// IT IS A SIZE CHECK AND NOT A GUESS. An installed macOS disk is many gigabytes; a freshly created
// qcow2 is a few hundred kilobytes of header. The distinction decides whether the recovery image is
// an installer or just OpenCore's APFS driver source, and the two want opposite `discard` settings.
func (b Boot) hasSystemYet() bool {
	fi, err := os.Stat(b.Disk)
	if err != nil {
		return false
	}
	return fi.Size() > 1<<30
}

// installing reports whether a medium is writing to the disk, which is when discard must be off.
func (b Boot) installing() bool {
	if b.OS.IsMacOS() {
		// A macOS disk with no system is being installed onto, and the recovery image is the
		// installer. One with a system is only being booted, and the recovery image is then just
		// where OpenCore reads the APFS driver from.
		return !b.hasSystemYet()
	}
	return b.ISO != ""
}

// FirmwareArgs is the pflash pair.
//
// THE MACOS LOADER IS FIRMWARE, NOT A CD. OpenCore is a pflash ROM plus a variable store exactly
// like OVMF, which is why it appears here rather than in the storage section - and why a macOS guest
// given OVMF's files in these slots boots to a crossed-out circle.
func (b Boot) FirmwareArgs(ovmfCode, ovmfVars string) []string {
	if b.OS.IsMacOS() {
		return []string{
			"-drive", "if=pflash,format=raw,readonly=on,file=" + b.OpenCoreROM,
			"-drive", "if=pflash,format=raw,file=" + b.OpenCoreVars,
		}
	}
	return []string{
		"-drive", "if=pflash,format=raw,readonly=on,file=" + ovmfCode,
		"-drive", "if=pflash,format=raw,file=" + ovmfVars,
	}
}

// ExtraArgs is everything else the guest needs, and each line is a measured fact.
//
// THE MACOS LIST IS LONG BECAUSE THE GUEST IS FUSSY. Every entry is from macos/15/machine.py, where
// each was added after a specific failure:
//
//	ICH9-LPC.disable_s3 / disable_s4    ICH9's sleep states hang this guest at a black screen
//	acpi-pci-hotplug-with-bridge=off    macOS mishandles the PCIe hotplug bridge on ICH9
//	-rtc base=utc                       macOS reads the clock from the RTC and expects UTC
//	-smbios type=2                      the baseboard record macOS's platform check reads
//	vmware-svga                         the adapter macOS's own driver recognises; virtio-vga left
//	                                    a 7 MB framebuffer with "No Kext Loaded", so the wallpaper
//	                                    layer could not be drawn
func (b Boot) ExtraArgs() []string {
	if !b.OS.IsMacOS() {
		return nil
	}
	return []string{
		"-global", "ICH9-LPC.disable_s3=1",
		"-global", "ICH9-LPC.disable_s4=1",
		"-global", "ICH9-LPC.acpi-pci-hotplug-with-bridge-support=off",
		"-rtc", "base=utc",
		"-smbios", "type=2",
		"-device", "vmware-svga",
	}
}

// SMCArgs is the Apple SMC device and its key.
//
// THE KEY IS NOT A SECRET AND IS NOT OPTIONAL. macOS reads it from the SMC to decide whether the
// firmware is Apple's, and without it the kernel stops before it draws anything. Every hackintosh
// carries the same string.
func (b Boot) SMCArgs(osk string) []string {
	if !b.OS.IsMacOS() {
		return nil
	}
	if osk == "" {
		osk = OSK
	}
	return []string{"-device", "isa-applesmc,osk=" + osk}
}

// CPU is the processor. The base model is the same for every guest; macOS adds flags.
//
// THE FLAGS EACH FIX A SILENT FAILURE, from macos/15/machine.py:
//
//	vendor=GenuineIntel     without it the kernel stops
//	vmx=off                 nested virtualisation confuses it
//	vmware-cpuid-freq=on    the TSC frequency comes from this
//	-pdpe1gb                1 GB pages are broken under this guest
//	+invtsc                 WITHOUT IT macOS 26 DEADLOCKS: every vCPU at 100%, block I/O frozen,
//	                        the installer's ETA still saying 20 minutes, and the serial output
//	                        ending at "#[EB|LOG:HANDOFF TO XNU]" with no panic and no error
//
// THE BASE IS STILL Skylake-Client-v4. The flags are additions to the model every guest here uses,
// not a different model - which is what makes a macOS disk and a Windows disk comparable at all.
func (b Boot) CPU(base string) (string, error) {
	cpu, err := ResolveCPU(base)
	if err != nil {
		return "", err
	}
	if !b.OS.IsMacOS() {
		return cpu, nil
	}
	return cpu + ",vendor=GenuineIntel,vmx=off,vmware-cpuid-freq=on,-pdpe1gb,+invtsc," +
		"+ssse3,+sse4.2,+popcnt,+avx,+avx2,+aes,+fma,+bmi1,+bmi2,+smep," +
		"+xsave,+xsaveopt,+xgetbv1,+movbe,+rdrand,check", nil
}

// SMP is the processor topology.
//
// A BARE COUNT IS NOT WHAT macOS UNDERSTANDS. dockur's configureSmp() spells out sockets, dies, cores
// and threads, and the guest reads that structure.
func (b Boot) SMP(cpus int) string {
	if cpus <= 0 {
		cpus = DefaultCPUs
	}
	if !b.OS.IsMacOS() {
		return fmt.Sprint(cpus)
	}
	return fmt.Sprintf("%d,sockets=1,dies=1,cores=%d,threads=1", cpus, cpus)
}

// VGA is the video adapter, and macOS needs `none` so the adapter can be a PCIe -device.
//
// ON q35, `-vga` CREATES AN ISA VGA DEVICE ON THE LEGACY BUS and macOS expects the PCIe one, which
// is why the macOS list has `vmware-svga` as a device and this returns none.
func (b Boot) VGA(defaultVGA string) string {
	if b.OS.IsMacOS() {
		return "none"
	}
	return defaultVGA
}

// OSK is Apple's SMC key, obfuscated the way every hackintosh does it.
const OSK = "ourhardworkbythesewordsguardedpleasedontsteal(c)AppleComputerInc"

// TouchOvmfVars copies the variables template into place if it is not already there.
//
// IT IS COPIED, NOT REFERENCED, because qemu WRITES to the variables file and the template belongs
// to the image. A run that pointed qemu at the template would mutate it, and every later guest would
// start from a store somebody else's firmware had already changed.
func TouchOvmfVars(template, dest string) error {
	if _, err := os.Stat(dest); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	b, err := os.ReadFile(template)
	if err != nil {
		return fmt.Errorf("firmware variables template %s: %w", template, err)
	}
	return os.WriteFile(dest, b, 0o644)
}

// MediaList renders the media for a log line, with the reason each is there. A boot line that lists
// three ISO paths without saying which is which is one a reader cannot check.
func (b Boot) MediaList() string {
	var parts []string
	for _, m := range []struct{ path, why string }{
		{b.ISO, "the installer"},
		{b.AnswerCD, "the answer file"},
		{b.DriverCD, "the drivers"},
		{b.Recovery, "the recovery image"},
	} {
		if m.path != "" {
			parts = append(parts, fmt.Sprintf("%s (%s)", filepath.Base(m.path), m.why))
		}
	}
	return strings.Join(parts, ", ")
}
