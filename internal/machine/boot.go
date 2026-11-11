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
	// BSD IS A DIFFERENT OPERATING SYSTEM AND THE SAME MACHINE.
	//
	// GhostBSD boots the way every Linux here does - UEFI firmware, a virtio disk, std VGA at the
	// resolution the coordinates were measured at - because that is what lib/ssh_step.py built for
	// it with the same `uefi.shape_with_uefi(shape, work, kind)` call every Linux distribution uses.
	// So it is its own NAME and not its own machine: the name appears in logs and in a file's
	// `os:`, and nothing else about the guest changes.
	BSD OS = "bsd"
)

// IsLinuxLike reports whether this guest boots UEFI with a virtio disk - every Linux and the BSD.
//
// IT EXISTS SO `StorageArgs` DOES NOT HAVE TO LIST THEM: a `switch` over three cases where two share
// every field is a place for the third to be forgotten when a fourth arrives.
func (o OS) IsLinuxLike() bool { return o == Linux || o == BSD }

// IsMacOS reports whether this guest boots through OpenCore.
//
// IT IS THE ONE QUESTION WITH A FAN-OUT: firmware, boot order, SMC, CPU flags, video adapter and the
// SMP topology all turn on it, and each of those is a silent failure when wrong.
func (o OS) IsMacOS() bool { return o == MacOS }

// ParseOS refuses anything that is not a machine this build can start.
func ParseOS(s string) (OS, error) {
	switch OS(s) {
	case Windows, Linux, MacOS, BSD:
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
		// OPENCORE IS NOT OPTIONAL FOR macOS, EVEN FROM AN INSTALLED DISK. Those three files are
		// what the firmware boots: without them qemu starts, the firmware finds no loader, and the
		// guest sits in the UEFI shell.
		//
		// THE RECOVERY IMAGE **IS** OPTIONAL, AND ATTACHING IT WHEN IT IS NOT NEEDED IS A BUG.
		// macOS installs FROM removable media, so only the stage that installs needs the recovery
		// image as a device. Every later stage boots a system that is already installed and has no
		// recovery volume - and an extra USB disk is one more row the guest can pick in Disk
		// Utility, where `choose the target disk by capacity` reads the sidebar. A stage that
		// attached it anyway could select the recovery volume instead of the target.
		//
		// It is still an INPUT to those stages, for one reason: opencore.build() extracts Apple's
		// APFS driver out of the image. That is a file the HOST reads, not a device the guest is
		// given - which is exactly why the two are separate fields.
		if b.Recovery != "" {
			if err := requireFile(b.Recovery, "the recovery image"); err != nil {
				return err
			}
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
// THE ORDER IS THE MODULE'S JOB BECAUSE IT IS PART OF THE MEANING. Two different things are being
// ordered and only one of them is about index numbers:
//
//	the BOOT MEDIUM     the firmware boots the first thing it can, so the installer ISO must come
//	                    before the disk
//	the DISK INDEX      Windows numbers only FIXED disks, so a virtio disk after two CD-ROMs is
//	                    still DiskID 0 and the answer file stays valid
//
// A caller that assembled this list would eventually get one of those backwards, and the failure is
// not a message: it is Setup offering to install onto the wrong disk, or a firmware screen.
//
// AND `discard=unmap` IS NOT ALWAYS RIGHT. It is what lets a guest's TRIM free clusters in the
// qcow2, which keeps a published disk small - but during an INSTALL it is destructive. Measured on
// the macOS 26 installs, which are the two runs that differ in exactly this argument:
//
//	41835253323   no discard,  128G   finished in 46 minutes
//	42009463444   discard,     128G   wrote 36774 MiB, dropped to 19761, never moved again
//
// The 17 GiB that vanished was the installer's own staging data. macOS writes about 36 GiB, takes an
// APFS snapshot, discards its staging volume and switches root; with discard on, every TRIM is
// honoured immediately and the clusters the SNAPSHOT STILL REFERS TO are deallocated under it. The
// progress bar froze at "About 48 minutes remaining".
//
// So discard is off while an installer is attached, and on otherwise. See installing() for what
// "attached" means and for why it is read from the media rather than guessed from the file.
func (b Boot) StorageArgs() []string {
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
		if b.Discardable() {
			target = "file=" + b.Disk + ",if=none,format=qcow2,discard=unmap,id=target"
		}
		args = append(args,
			"-drive", target,
			"-device", "virtio-blk-pci,drive=target,bootindex=1",
		)
		// AND THE RECOVERY IMAGE AS REMOVABLE MEDIA, ONLY WHEN THIS BOOT INSTALLS.
		//
		// macOS installs FROM removable media; the disk it installs TO is not the disk it boots
		// FROM. That is why the SETUP stage attaches it - and why every LATER stage must not:
		// those boot a system that is already installed and has no recovery volume, and an extra
		// USB disk is one more row the guest can pick in Disk Utility, where
		// `choose the target disk by capacity` reads the sidebar. Attaching it on an installed
		// disk could select the recovery volume instead of the target.
		//
		// AND IT NEEDS A CONTROLLER TO BE ATTACHED TO.
		//
		// `usb-storage,bus=xhci.0` NAMES A BUS THAT DOES NOT EXIST UNLESS SOMETHING CREATES IT, and qemu
		// refuses at startup:
		//
		//	-device usb-storage,bus=xhci.0,drive=rec: Bus 'xhci.0' not found
		//
		// - and then exits, so the QMP socket never appears and the boot step reports a socket error
		// rather than the missing device. Measured on the first macos-setup run that got past
		// OpenCore: `qmp .../drive.sock.qmp: connect: connection refused`.
		//
		// THE CONTROLLER IS UNCONDITIONAL WHEN THERE IS A RECOVERY IMAGE, and it is not only for the
		// disk: lib/vm.py gives the same bus a usb-tablet and a usb-kbd, because a macOS guest has no
		// PS/2 and every pointer or key it receives arrives over USB. This build gives the tablet its
		// own `virtio-tablet-pci`, which macOS drives; the bus here exists for the installer.
		//
		// `usb-storage` has no bootindex and does not need one: the firmware reaches it only
		// after OpenCore hands over.
		if b.Recovery != "" {
			args = append(args,
				"-device", "qemu-xhci,id=xhci",
				"-drive", "file="+b.Recovery+",media=disk,if=none,id=rec,readonly=on",
				"-device", "usb-storage,bus=xhci.0,drive=rec",
			)
		}
		return args
	}

	// WINDOWS AND LINUX: THE INSTALLER, THE ANSWER CD, THE DISK, THE DRIVERS - IN THAT ORDER.
	//
	// THAT ORDER IS MEASURED, NOT ARBITRARY, and it is NOT "the disk first". The Python
	// implementation's comment gives the reason:
	//
	//     "Three CDs at most, and the order matters only for the boot device: the install ISO is
	//      first so the firmware boots it. ... The drivers travel on the answer CD, which keeps the
	//      virtio disk at index 0 so the answer file's DiskID stays correct."
	//
	// A CD-ROM DOES NOT TAKE A DISK INDEX. Windows numbers only fixed disks, so a virtio disk after
	// two CD-ROMs is still DiskID 0 - which is why the answer file's disk reference stays valid.
	// Putting the CD-ROMs after the disk would ALSO work for DiskID and would break the other half:
	// the firmware boots the first thing it can, and the installer must be it.
	//
	// THE DRIVER CD IS LAST for its own reason: "The same drivers again, for the installed system
	// to pick up network and ballooning. Setup only reads $WinPEDriver$ during windowsPE." It has
	// to be attached for the installed system, but nothing during setup needs it in a particular
	// place - so it goes after the disk, where it cannot be mistaken for the boot medium.
	appendMedia := func(path string) {
		if path == "" {
			return
		}
		// `media=cdrom,readonly=on` IS WHAT MAKES IT A CD-ROM rather than another hard disk.
		args = append(args, "-drive", "file="+path+",media=cdrom,readonly=on")
	}

	// 1. THE INSTALLER, SO THE FIRMWARE BOOTS IT.
	appendMedia(b.ISO)

	// 2. THE ANSWER CD. Windows Setup scans removable media for Autounattend.xml and
	//    $WinPEDriver$ at its root, which is why the virtio drivers also live here.
	appendMedia(b.AnswerCD)

	// 3. THE TARGET DISK, AT INDEX 0.
	//
	// `discard=unmap` IS ON WHEN NOTHING IS INSTALLING, for the same reason and with the opposite
	// sign as macOS: after a system is on the disk, its TRIMs are information the qcow2 can use to
	// stay small.
	disk := "file=" + b.Disk + ",if=virtio,format=qcow2"
	if b.Discardable() {
		disk += ",discard=unmap"
	}
	args = append(args, "-drive", disk)

	// 4. THE DRIVER CD, FOR THE SYSTEM THAT WILL RUN ON THIS DISK.
	appendMedia(b.DriverCD)
	return args
}

// installing reports whether a medium is writing to the disk, which is when discard must be off.
//
// IT READS THE MEDIA, NOT THE FILE SIZE. A size heuristic ("a big qcow2 already has a system")
// is wrong in both directions: a 128G qcow2 that has only been created is 196 KiB and one that has
// been installed onto is 30 GiB, but an OVERLAY over an installed disk is a few hundred MiB and
// still has a system, while an installer that has just written 2 GiB into a fresh disk has neither
// finished nor become large. The file already says which it is - it names `recovery:` or `iso:`
// when a medium is installing - so that is what this asks.
//
// The two measured facts that make it matter are opposite halves of the same flag:
//
//	macOS install with discard    42009463444 wrote 36774 MiB, dropped to 19761, froze
//	macOS install without         41835253323 finished in 46 minutes
//	a finished disk being booted  discard is what keeps the published qcow2 small
//
// AND `discard=unmap` HAS A SECOND HAZARD, WHICH THE PYTHON IMPLEMENTATION ATTRIBUTED TO THIS
// FLAG AND THEN RETRACTED. lib/clean_step.py passes `discard=False` because a guest booting out of
// a zstd qcow2 must not be handed its clusters back - run 42778795463 died at
//
//	#[EB|LOG:HANDOFF TO XNU] _
//
// with no panic and nothing in the console after it. But run 42874167311 repeated the same death
// WITH `discard=False` in place, so the flag was not the cause; the note that retracts it points at
// the firmware state instead. The retraction is the part worth carrying: a plausible cause that
// survives one run and not the next is not a cause.
//
// THIS BUILD DOES NOT PASS `discard=False` FOR A COMPRESSED ARTIFACT, AND THAT IS A GAP. The
// Python implementation's answer is to open a WRITABLE OVERLAY over the published base - the base
// is read-only and never handed a TRIM - and this build has no overlay yet. Until it does, a stage
// that boots a pulled artifact discards into the artifact itself. See Boot.Discardable.
func (b Boot) installing() bool {
	if b.OS.IsMacOS() {
		// macOS's installer IS the recovery image on a USB device, so a boot that attaches it is
		// the one that writes. Every later stage boots an installed system and attaches nothing.
		return b.Recovery != ""
	}
	return b.ISO != ""
}

// WantsOverlay reports whether this boot should write into a throwaway diff over the disk.
//
// AN INSTALL WRITES A NEW SYSTEM TO A DISK THIS RUN CREATED, and there is nothing behind it to
// protect - so it runs on the disk itself, and `readonly` on a fresh qcow2 would only add a file.
//
// EVERY OTHER BOOT OPENS A DISK THAT CAME FROM SOMEWHERE: a published artifact, a working copy
// another stage made, or a disk the person wants to keep. Those run on a diff, and `compress:`
// flattens it when the stage publishes. That is what lib/steps.py does with `copy=True`, which is
// its default, and what windows/11/steps/ssh.yaml's own comment describes:
//
//	"`boot:` BUILDS THE OVERLAY, so there is no `disk_overlay:` here any more. It was needed for
//	 one commit because a file that let `boot:` make the overlay had no way to tell `compress:` to
//	 flatten it. Now `boot:` records the overlay it made, so the two are the same path: every
//	 machine runs on a diff, and publishing flattens it."
func (b Boot) WantsOverlay() bool { return !b.installing() }

// Discardable reports whether `discard=unmap` belongs on the target.
//
// TWO ANSWERS, AND BOTH ARE MEASURED: no while an installer is writing (the 17 GiB that vanished),
// yes for a finished disk (its TRIMs are what keep the published qcow2 small).
//
// The third input - whether the file is a compressed artifact being read rather than written - is
// deliberately NOT here. lib/clean_step.py blamed it for the HANDOFF TO XNU death and then found
// the same death with the flag off; see the note above installing(). It belongs to the overlay,
// which does not exist in this build yet.

// Discardable reports whether `discard=unmap` belongs on the target.
//
// TWO ANSWERS, AND BOTH ARE MEASURED: no while an installer is writing (the 17 GiB that vanished),
// yes for a finished disk (its TRIMs are what keep the published qcow2 small).
//
// The third input - whether the file is a compressed artifact being read rather than written - is
// deliberately NOT here. lib/clean_step.py blamed it for the HANDOFF TO XNU death and then found
// the same death with the flag off; see the note above installing(). It belongs to the overlay,
// which does not exist in this build yet.
func (b Boot) Discardable() bool { return !b.installing() }

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
