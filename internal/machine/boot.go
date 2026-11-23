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
	// Android IS A LINUX GUEST WITH A KERNEL HANDED TO IT BY THE HOST.
	//
	// Android-x86 IS NOT A DISK WITH A BOOTLOADER WHEN IT IS BEING INSTALLED. Its own initrd finds the
	// system image, mounts it, and mounts a writable ext4 for /data - so the INSTALLER is started with
	// `-kernel` and `-initrd` out of the ISO's own directory, and the ISO as a cdrom, exactly the way
	// easydiskbake's lib/vm_session.py does it. The INSTALLED disk then boots through UEFI and GRUB
	// like any other UEFI system, and `-kernel` is absent on that path - its absence is what makes the
	// launch a proof that the disk can start itself.
	Android OS = "android"

	// BSD IS A DIFFERENT OPERATING SYSTEM AND THE SAME MACHINE.
	//
	// GhostBSD boots the way every Linux here does - UEFI firmware, a virtio disk, std VGA at the
	// resolution the coordinates were measured at - because that is what lib/ssh_step.py built for
	// it with the same `uefi.shape_with_uefi(shape, work, kind)` call every Linux distribution uses.
	// So it is its own NAME and not its own machine: the name appears in logs and in a file's
	// `os:`, and nothing else about the guest changes.
	BSD OS = "bsd"
)

// IsLinuxLike reports whether this guest boots UEFI with a virtio disk.
//
// IT EXISTS SO `StorageArgs` DOES NOT HAVE TO LIST THEM: a `switch` over three cases where two share
// every field is a place for the third to be forgotten when a fourth arrives.
//
// ANDROID IS ONE OF THEM. An INSTALLED Android-x86 disk is started by UEFI reading the ESP's GRUB,
// exactly like a Linux guest: same firmware, same virtio-blk target, same std VGA. The `-kernel`
// path it also supports belongs to the INSTALLER and is decided by whether `Kernel` is set, which is
// a different question from this one.
func (o OS) IsLinuxLike() bool { return o == Linux || o == BSD || o == Android }

// IsMacOS reports whether this guest boots through OpenCore.
//
// IT IS THE ONE QUESTION WITH A FAN-OUT: firmware, boot order, SMC, CPU flags, video adapter and the
// SMP topology all turn on it, and each of those is a silent failure when wrong.
func (o OS) IsMacOS() bool { return o == MacOS }

// shape returns the machine this boot is for, resolving the name if nobody filled it in.
//
// THE FALLBACK EXISTS FOR TWO REASONS, AND NEITHER IS CONVENIENCE.
//
//	A `Boot` VALUE IS USED IN PLACES THAT NEVER SAW THE DAEMON - a test, a `check:` that asks "what
//	would this guest be given", a tool that renders a command line for a person to read. Requiring
//	every one of them to call `Resolve` first would put the same three lines in each, and one of them
//	would eventually pass the wrong name.
//
//	AND THE ZERO VALUE HAS TO MEAN SOMETHING. A struct whose builders all read a field that only the
//	daemon fills is a struct that silently produces an argument list with no firmware and no devices
//	when somebody forgets. Resolving on read makes the zero value correct instead of empty.
//
// A SHAPE THAT WAS FILLED IN IS USED AS IT IS, so `--shapes` and a test that wants a shape of its own
// both keep working: only an EMPTY shape is resolved from the name.
func (b Boot) shape() Shape {
	if b.Shape.Name != "" {
		return b.Shape
	}
	if sh, ok := defaultShapes.Lookup(string(b.OS)); ok {
		return sh
	}
	return Shape{}
}

// DefaultShapes is the registry this process starts with, loaded once from the shapes directory.
//
// IT IS A PACKAGE-LEVEL VALUE RATHER THAN A PARAMETER because every caller that asks "may this build
// start a chromeos guest" is answering a question about the BUILD, not about a particular boot. A
// second registry is possible - `LoadDir` on a different directory - and is how a test drives a shape
// of its own.
var defaultShapes = NewRegistry()

// Shapes returns the registry `ParseOS` and `Resolve` consult.
func Shapes() *Registry { return defaultShapes }

// LoadShapes reads a directory of shape files into the default registry, replacing nothing.
//
// CALLED ONCE, BY THE DAEMON, BEFORE ANY GUEST IS STARTED. A directory that does not parse is an
// error rather than a warning: a shape file is configuration for a machine that will be started, and
// finding out it is malformed at the `boot:` step is finding out forty minutes into a stage.
func LoadShapes(dir string) error { return defaultShapes.LoadDir(dir) }

// ParseOS refuses anything this build has no shape for.
//
// THE LIST IS NO LONGER IN THIS FUNCTION. It was a `switch` over five constants, so adding a sixth
// machine meant editing the parser, and the error message named the five either way. It now asks the
// registry, and the error names what THIS build actually has - which is the only list that is true.
func ParseOS(s string) (OS, error) {
	if s == "" {
		return "", fmt.Errorf("a boot step must name `os:` - this build has %s",
			strings.Join(defaultShapes.Names(), ", "))
	}
	if _, ok := defaultShapes.Lookup(s); !ok {
		return "", fmt.Errorf("%q is not an os this build starts; it has %s",
			s, strings.Join(defaultShapes.Names(), ", "))
	}
	return OS(s), nil
}

// Resolve returns the machine a name means, refusing one this build has no shape for.
//
// THIS IS THE ONE PLACE A NAME BECOMES A MACHINE. `Boot.Shape` is filled from it, so every builder in
// this file reads a field rather than switching on an operating system.
func Resolve(name string) (Shape, error) {
	sh, ok := defaultShapes.Lookup(name)
	if !ok {
		return Shape{}, fmt.Errorf("%q is not an os this build starts; it has %s",
			name, strings.Join(defaultShapes.Names(), ", "))
	}
	return sh, nil
}

// Boot is what one `boot:` step says.
type Boot struct {
	// OS is which machine this is - the NAME a walkthrough's `os:` says.
	OS OS
	// Shape is the MACHINE that name resolves to: firmware, CPU flags, devices, media rules.
	//
	// IT IS FILLED BY `Resolve` FROM A SHAPE FILE, and it is what every builder below reads. There is
	// no `switch` on the operating system anywhere in this package any more: the fifteen branches
	// that used to answer "which firmware, which CPU flags, which devices" were facts about a machine,
	// and facts belong in a file somebody can read and change.
	Shape Shape
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
	// Work is the run's working directory, which is what `{{work}}` in a shape's `firmware.dir:`
	// stands for and the base a relative one is joined onto.
	Work string
	// FirmwareDir is the directory a `kind: folder` firmware lives in.
	//
	// THE ENGINE ONLY EVER PASSES THIS THROUGH. `{{dir}}` in the shape's `firmware.args` and
	// `firmware.disks` is replaced with it, and what the files inside are called is the shape's
	// business - which is what lets a firmware this build has never heard of boot.
	FirmwareDir string
	// MicroVM asks for the smallest machine that boots a modern Linux kernel: qemu's `microvm`
	// machine type, no PCIe, no IDE, no USB, no VGA.
	//
	// IT IS SET FROM THE `os:` THE FILE WROTE, and it is kept as a field rather than derived on every
	// read because it decides the MACHINE TYPE - `-machine microvm` rather than `q35` - which is a
	// different question from any single device.
	//
	// THE SHAPE MUST AGREE: `os: microvm` resolves to a shape whose firmware is `none`, and a boot
	// that sets one without the other is refused in `Validate`.
	MicroVM bool
	// MicroVMDisplay is serial, terminal or desktop - whether the microvm has a VGA adapter.
	//
	//	serial     graphics=off: serial only. `see:` reads nothing and a recording is blank.
	//	           pcie=off, and `pci=off` on the kernel line.
	//	terminal   graphics=on:  a VGA adapter the guest draws its TTY on. A recording of a
	//	                         headless guest is a PICTURE of its console. pcie=off.
	//	desktop    pcie=on and graphics=off: a `virtio-gpu-pci` device, which is the ONLY way a
	//	           compositor gets a KMS output and a render node on one card. It is also the one
	//	           shape that does NOT carry `pci=off`, because that would hide the bus the GPU is
	//	           attached to.
	//
	// The three are the same qemu machine type; what separates them is the adapter and the PCI bus,
	// and - for `desktop` - what the guest runs.
	MicroVMDisplay string
	// Kernel and Initrd are handed to qemu directly, which is how a microvm starts: there is no
	// firmware to read a boot sector, so the host supplies the kernel the guest runs.
	Kernel string
	Initrd string
	// KernelAppend is the guest's own boot line. Empty means the Alpine default, which is what a
	// microvm here boots.
	KernelAppend string

	// Incoming is a migration stream to restore instead of booting. See the -incoming arguments in
	// Boot.StorageArgs' caller: it is not a device, it is the machine's starting state.
	Incoming string

	// FreshDisk says this run CREATED the target and nothing behind it needs protecting.
	//
	// THIS IS A SEPARATE FACT FROM `installing()`, AND CONFLATING THEM WAS TWO DEFECTS. A `boot:` step
	// that creates its own disk (a `disk_gb:` step just ran) writes to the disk itself; a step that
	// opens a disk from somewhere - a published artifact, another stage's output, a disk a person
	// wants to keep - must NOT lose its writes.
	//
	// `installing()` used to answer both questions with "is an installer medium attached", so:
	//
	//	an installed macOS disk booted WITHOUT `recovery:` got a throwaway overlay, and RUPTURED on
	//	destroy - measured in easydev-org/easyvm#3 as a Setup Assistant that restarted from the
	//	country list every time, with 38.40 GiB unchanged on the base disk;
	//
	//	and `Discardable()` was its exact negation, so the configurations "writes persist" and
	//	"TRIM reaches the disk" could never both be true - the same issue measured 26 GiB of freed
	//	blocks in a published disk that no amount of compression could remove.
	//
	// SET BY THE EXECUTOR WHEN IT RUNS A `disk_gb:` STEP, because that is the only thing that knows
	// the disk was made here.
	FreshDisk bool

	// THERE IS NO `-kernel` HERE, AND ITS ABSENCE IS A DECISION RATHER THAN AN OMISSION.
	//
	// Android-x86 CAN be started by handing qemu its kernel and initrd, and this build did for a
	// while. Two things make that the wrong way to install it:
	//
	//	THE ISO IS ALREADY A BOOTABLE MEDIUM UNDER BOTH FIRMWARES. Measured from its own El Torito
	//	catalog: a `0x00` entry for the x86 BIOS image and a `0xEF` entry for `boot/grub/efi.img`.
	//	Firmware finds both by itself, which is what every other installer in this repository relies
	//	on - and a guest handed a kernel by the host is a guest whose own boot chain was never run.
	//
	//	THE INSTALLER DOES ITS OWN PARTITIONING. It offers cfdisk and cgdisk, and cgdisk writes the
	//	GPT and the ESP the installed system needs. Driving that from the host means reproducing a
	//	partition table that the installer was going to make anyway.
	//
	// So `os: android` boots the ISO the way Windows and Linux boot theirs, and the only platform
	// fact it adds is the video adapter - see Boot.VGA.
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
	// `os: microvm` AND `as a microvm` ARE THE SAME FACT, AND THE PARSER WRITES ONE FROM THE OTHER.
	//
	// A MICROVM IS AN OPERATING SYSTEM IN THIS VOCABULARY, not a modifier on linux: it wants no
	// firmware at all, a `virtio-blk-device` rather than a `-pci` one, a different network device,
	// and a kernel the host hands qemu directly. Those are a different MACHINE, and the shape file
	// `shapes/microvm.yaml` is where they live.
	//
	// SO THE HALVES MUST AGREE. A boot that set `MicroVM` on a shape whose firmware is pflash would
	// ask for `-machine microvm` and then hand qemu a firmware it cannot use; the shape without the
	// flag would describe devices a microvm has no bus for. Both produce a qemu error naming a device
	// nobody added, which is why this is refused here where the sentence can say which half is wrong.
	if noFirmware := b.shape().Firmware == FirmwareNone; noFirmware != b.MicroVM {
		return fmt.Errorf("`os: %s` resolves to a shape with `firmware.kind: %s`, but this boot %s "+
			"the microvm machine type. `as a microvm` names the operating system `microvm`, whose "+
			"shape has no firmware; setting one half without the other asks qemu for a machine whose "+
			"devices were never described", b.OS, b.shape().Firmware,
			map[bool]string{true: "asks for", false: "does not ask for"}[b.MicroVM])
	}
	if b.Disk == "" {
		return fmt.Errorf("booting needs a target disk: a `disk:` step must come first, or pass " +
			"--boot-disk")
	}
	if err := requireFile(b.Disk, "the target disk"); err != nil {
		return err
	}

	if b.ISO != "" {
		// THE SHAPE SAYS WHETHER THIS GUEST MAY BE GIVEN AN INSTALL ISO, AND ITS REFUSAL CARRIES THE
		// REASON. macOS has no install ISO at all - it installs from its recovery image - and a stage
		// that attached one would watch the guest sit in the UEFI shell with nothing said.
		if !b.shape().Media.ISO {
			return fmt.Errorf("%s", b.shape().Media.ISOReason)
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
			// AND THE TWO EXTRA DISCS ARE WINDOWS SETUP'S, which is a fact the shape carries: a Linux
			// installer given an answer-file CD would find nothing on it and the mistake would surface
			// as a wizard that never advanced.
			if !b.shape().Media.AnswerAndDriver {
				return fmt.Errorf("%s is Windows Setup's, and this guest is %q. The medium would be "+
					"attached and read by nobody, which surfaces later as a wizard that never "+
					"advances rather than as this sentence", why, b.OS)
			}
			if err := requireFile(path, why); err != nil {
				return err
			}
		}
	}

	if b.Recovery != "" && !b.shape().Media.Recovery {
		return fmt.Errorf("`recovery:` is macOS's install medium; this guest is %s", b.OS)
	}

	// A `kind: folder` FIRMWARE IS CHECKED BY WHAT ITS OWN ARGUMENTS NAME.
	//
	// THIS BUILD DOES NOT KNOW WHICH FILE IS A ROM OR A VARIABLE STORE - that is the point - so it
	// cannot name them. What it can do is render the shape's arguments and confirm that every
	// `file=` they mention exists, which catches the failure this check exists for: a stage that
	// never ran its `fetch:`, so the firmware directory is empty and qemu would start, find no
	// loader, and sit in the UEFI shell.
	if b.shape().Firmware == FirmwareFolder {
		if b.Recovery != "" {
			if err := requireFile(b.Recovery, "the recovery image"); err != nil {
				return err
			}
		}
		for _, pair := range b.shape().FirmwareDisks {
			if err := requireFirmwareFiles(pair, b.firmwareDir(), "firmware.disks"); err != nil {
				return err
			}
		}
		if err := requireFirmwareFiles(b.shape().FirmwareArgs, b.firmwareDir(), "firmware.args"); err != nil {
			return err
		}
	}

	if b.shape().Firmware == FirmwareOpenCore {
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

	if b.shape().Firmware == FirmwareFolder {
		// THE FIRMWARE'S OWN BOOT DISKS COME FIRST, before the guest's target. A loader that boots
		// from a disk of its own has to be `bootindex=0`, or the firmware reports it cannot load
		// Boot0001 and the guest never starts.
		for _, pair := range b.shape().FirmwareDisks {
			args = append(args, renderFirmwareArgs(pair, b.firmwareDir())...)
		}
		// THE BUS AND THE POINTER COME BEFORE ANY DISK THAT NAMES THEM. qemu resolves `bus=` as it
		// parses, so a `usb-storage,bus=xhci.0` written before the controller is
		// `Bus 'xhci.0' not found` and qemu exits - which reads as a qmp socket error, not a device.
		args = append(args, b.shape().DeviceArgs...)
		args = append(args, b.targetDiskArgs()...)
		// AND THE RECOVERY IMAGE, IF ONE IS ATTACHED, in the order the shape wrote.
		//
		// THE CONTROLLER IS IN `media.recovery_args` - every pair of the list, not just the ones that
		// name the image - because a `usb-storage,bus=xhci.0` device needs its bus created first.
		if b.Recovery != "" {
			for _, pair := range b.shape().FirmwareRecoveryPairs() {
				for _, a := range pair {
					args = append(args, strings.ReplaceAll(
						strings.ReplaceAll(a, "{{dir}}", b.firmwareDir()),
						"{{recovery}}", b.Recovery))
				}
			}
		}
		return args
	}

	if b.shape().Firmware == FirmwareOpenCore {
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
		// PS/2 and every pointer or key it receives arrives over USB.
		//
		// THE TABLET IS ON THIS BUS - `usb-tablet,bus=xhci.0`, below and again for a disk boot. THIS
		// COMMENT USED TO SAY the build gave the tablet "its own `virtio-tablet-pci`, which macOS
		// drives", AND THAT DEVICE HAS NEVER BEEN IN THE ARGUMENT VECTOR: `grep virtio-tablet-pci`
		// finds this sentence, a test's failure message, and nothing that qemu is ever given.
		//
		// IT IS WORTH CORRECTING RATHER THAN DELETING, because the sentence described a real design
		// and the difference explains a real failure. A `virtio-tablet-pci` would meet q35's `vmmouse`
		// on equal terms; a `usb-tablet` does not, and on a guest where Linux binds `vmmouse` the
		// absolute events reach that instead:
		//
		//	click (965, 981): pointer to (965, 981): Input handler not found for event type abs
		//
		// The fix for that is `-machine q35,...,vmport=off` (see the daemon's argument list), not a
		// different tablet - the tablet the walkthroughs were measured against is this one.
		//
		// `usb-storage` has no bootindex and does not need one: the firmware reaches it only
		// after OpenCore hands over.
		// THE POINTER IS ATTACHED WHETHER OR NOT THERE IS A RECOVERY IMAGE, AND THAT WAS THE BUG.
		//
		// `usb-tablet,bus=xhci.0` USED TO LIVE INSIDE `if b.Recovery != ""`, so a macOS guest booted
		// FROM AN INSTALLED DISK - every `ssh`, `vm-configure` and `dev` leg - had NO POINTER AT ALL.
		// The first click of such a walk died in qemu rather than on the screen:
		//
		//	click (965, 981): pointer to (965, 981): Input handler not found for event type abs
		//
		// MEASURED ON THE DISK THIS REPOSITORY PUBLISHED AS `macos15`, pulled back and driven: the
		// machine booted to its Setup Assistant - the screen was right - and the walk could not touch
		// it. The device list had NO POINTER IN IT:
		//
		//	isa-serial  virtio-blk-pci(oc)  virtio-blk-pci(target)  vmware-svga  virtio-net-pci
		//
		// AND A MAC HAS NO PS/2, so a pointer that is not on USB is a pointer that does not exist.
		// `vmport=off` in the daemon's `-machine` does not help either: it removes a competing
		// HANDLER, and there was no device for the handler to belong to.
		//
		// SO THE CONTROLLER AND THE TABLET COME FIRST AND UNCONDITIONALLY, and the recovery image is
		// what is conditional - it is one more device ON the bus that is already there.
		args = append(args, "-device", "qemu-xhci,id=xhci")
		if b.Recovery != "" {
			args = append(args,
				"-drive", "file="+b.Recovery+",media=disk,if=none,id=rec,readonly=on",
				"-device", "usb-storage,bus=xhci.0,drive=rec",
			)
		}
		args = append(args, "-device", "usb-tablet,bus=xhci.0")
		// AND THE KEYBOARD THE COMMENT ABOVE SAYS THIS GUEST NEEDS. `lib/vm.py` gives this bus a
		// `usb-tablet` AND a `usb-kbd`, "because a macOS guest has no PS/2 and every pointer or key
		// it receives arrives over USB" - and only the tablet was ever added here. The walks have
		// been typing into macOS over USB anyway, which is why this was not caught: qemu's HID
		// keyboard falls back to the PS/2 port for keycodes, and the firmware's own menus are the
		// one place that fallback does not reach.
		args = append(args, "-device", "usb-kbd,bus=xhci.0")
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
	//
	// EVERY MEDIUM CARRIES A `bootindex`, AND THAT IS NOT DECORATION. The note ten lines up - "a disk
	// with no bootindex is not in the boot order at all" - was learned on the macOS path and applied
	// only there. The comment here said "the firmware boots the first thing it can", which assumed OVMF
	// would scan the CD-ROM and find a bootable El Torito image.
	//
	// MEASURED ON UBUNTU-INSTALL: `console.log` was 24 KB of firmware output and the screen was
	// `drop=+0.0 ratio=0.0x sat=+0.00` for fifteen minutes - A BLACK MACHINE. A fresh OVMF_VARS has no
	// saved boot entry, and an implicit scan is the firmware's choice rather than a request.
	//
	// THE ORDER IS THE ARGUMENT ORDER, made explicit: whatever is installing gets 0, the target disk
	// gets 1, and anything else gets 2 or more so it cannot win.
	nextIndex := 0
	nextID := 0
	// A CD ABOARD A USB BUS, FOR THE ONES THAT ARE NOT THE BOOT MEDIUM.
	//
	// q35's IDE CONTROLLER HAS EXACTLY ONE UNIT, AND qemu SAYS SO PLAINLY:
	//
	//	-device ide-cd,drive=media1,bootindex=1: Can't create IDE unit 1, bus supports only 1 units
	//
	// Measured on windows-install, which attaches THREE CDs - the installer, the answer CD with the
	// virtio drivers, and the driver CD for the installed system. Only the installer needs to be on a
	// bus the firmware boots from, so it takes the IDE unit and the others go on USB, which is a bus
	// with as many ports as there are devices.
	//
	// THE XHCI CONTROLLER IS CREATED ONCE, on first use, and it is the same `qemu-xhci,id=xhci` the
	// macOS branch needs for its recovery disk.
	xhciAdded := false
	appendUSBCD := func(path string) {
		if path == "" {
			return
		}
		if !xhciAdded {
			args = append(args, "-device", "qemu-xhci,id=xhci")
			xhciAdded = true
		}
		id := fmt.Sprintf("media%d", nextID)
		nextID++
		args = append(args,
			"-drive", fmt.Sprintf("file=%s,media=cdrom,readonly=on,if=none,id=%s", path, id),
			"-device", fmt.Sprintf("usb-storage,bus=xhci.0,drive=%s", id),
		)
	}
	// `bootindex` IS A DEVICE PROPERTY AND CANNOT GO ON A `-drive`, WHICH IS MEASURED RATHER THAN
	// ASSUMED - I put it there first and qemu refused to start:
	//
	//	qemu-system-x86_64: -drive file=...,if=virtio,format=qcow2,bootindex=0,discard=unmap:
	//	Block format 'qcow2' does not support the option 'bootindex'
	//
	// AND THE SYMPTOM WAS NOT THAT MESSAGE. It was
	//
	//	boot the disk: qmp .../drive.sock.qmp: connect: connection refused (waited 30s)
	//
	// because qemu exits before it creates the socket. A boot step reporting a socket error is the
	// shape of EVERY qemu argument mistake, which is why the daemon's own log line - the one above -
	// has to be read rather than the step's.
	//
	// SO A MEDIUM IS TWO ARGUMENTS: `-drive if=none,id=X` and a `-device` that names X and carries
	// the index. That is the same form the macOS branch uses for OpenCore, and the same reason.
	//
	// A CD-ROM IS `ide-cd` ON q35 - the machine has an IDE controller for it and no SATA bus unless one
	// is asked for, and the installer is a CD-R.
	appendVirtioDisk := func(path string) {
		if path == "" {
			return
		}
		id := fmt.Sprintf("media%d", nextID)
		nextID++
		// NO `bootindex`: the host hands the kernel over, so nothing on this bus is booted from. It
		// is a BLOCK DEVICE the guest reads `modloop=` out of.
		//
		// MEASURED, and the reason it matters: giving it one collided with the target disk's index 0
		//
		//	-device virtio-blk-device,drive=target,bootindex=0: The bootindex 0 has already been used
		//
		// - qemu refused at startup and the step reported `qmp ... connection refused`.
		args = append(args,
			"-drive", fmt.Sprintf("file=%s,if=none,format=raw,readonly=on,id=%s", path, id),
			"-device", fmt.Sprintf("virtio-blk-device,drive=%s", id),
		)
	}
	appendMedia := func(path string) {
		if path == "" {
			return
		}
		idx := nextIndex
		nextIndex++
		id := fmt.Sprintf("media%d", nextID)
		nextID++
		args = append(args,
			"-drive", fmt.Sprintf("file=%s,media=cdrom,readonly=on,if=none,id=%s", path, id),
			"-device", fmt.Sprintf("ide-cd,drive=%s,bootindex=%d", id, idx),
		)
	}

	// 1. THE INSTALLER, SO THE FIRMWARE BOOTS IT - and the ONLY one on the IDE bus.
	//
	// A MICROVM HAS NO FIRMWARE AND NO IDE, so its medium is a virtio-blk disk at the next index.
	// That is not a workaround: an Alpine microvm boots a kernel the host hands it and reads its
	// `modloop=` squashfs out of this image, so what it needs is a BLOCK DEVICE, not a bootable
	// CD-ROM - which is exactly what a virtio disk is.
	if b.MicroVM {
		appendMedia = appendVirtioDisk
	}
	appendMedia(b.ISO)

	// 2. THE ANSWER CD. Windows Setup scans removable media for Autounattend.xml and
	//    $WinPEDriver$ at its root, which is why the virtio drivers also live here.
	appendUSBCD(b.AnswerCD)

	// 3. THE TARGET DISK, AT INDEX 0.
	//
	// `discard=unmap` IS ON WHEN NOTHING IS INSTALLING, for the same reason and with the opposite
	// sign as macOS: after a system is on the disk, its TRIMs are information the qcow2 can use to
	// stay small.
	// A MICROVM'S TARGET IS INDEX 0, BECAUSE NOTHING ELSE IS IN THE BOOT ORDER: the host hands the
	// kernel over, so no medium carries a `bootindex` and the counter is still 0. Using it there
	// collided the disk with itself -
	//
	//	-device virtio-blk-device,drive=target,bootindex=0: The bootindex 0 has already been used
	//
	// - and every other machine keeps the counter, where the installer is 0 and the target is 1 so
	// the firmware cannot outrank the medium it was told to boot.
	//
	// AND A MICROVM'S TARGET IS INDEX 1 RATHER THAN 0, measured the hard way: qemu takes index 0 for
	// the kernel it was handed, so the disk answering 0 is
	//
	//	-device virtio-blk-device,drive=target,bootindex=0: The bootindex 0 has already been used
	//
	// - and the same command line has NO other `bootindex` in it, which is what made this look like a
	// duplicate-device bug for three attempts.
	diskIndex := nextIndex
	if b.MicroVM {
		diskIndex = 1
	} else {
		nextIndex++
	}
	disk := fmt.Sprintf("file=%s,if=none,format=qcow2,id=target", b.Disk)
	if b.Discardable() {
		disk += ",discard=unmap"
	}
	args = append(args,
		"-drive", disk,
		// A MICROVM HAS NO PCI BUS, so its disk is the virtio-mmio device. `virtio-blk-device` and
		// `virtio-blk-pci` are the same driver to the guest, and the mmio form is what a machine with
		// no PCIe can attach.
		"-device", fmt.Sprintf("%s,drive=target,bootindex=%d", b.virtioBlk(), diskIndex),
	)

	// 4. THE DRIVER CD, FOR THE SYSTEM THAT WILL RUN ON THIS DISK.
	appendUSBCD(b.DriverCD)

	// 5. THE POINTER, WHICH BELONGS ON THE SAME CONTROLLER AS EVERYTHING ELSE HERE.
	//
	// IT WAS ADDED BY THE DAEMON INSTEAD, AND THAT IS WHERE IT BROKE WINDOWS:
	//
	//	qemu-system-x86_64: -device qemu-xhci,id=xhci: Duplicate ID 'xhci' for device
	//
	// A Windows boot has an ANSWER CD and a DRIVER CD, so `appendUSBCD` above had already created
	// `qemu-xhci,id=xhci` - and the daemon then added it again for the tablet, without any way to know.
	// `xhciAdded` IS THE FACT THAT MATTERS, and it lives here: the pointer is one more thing on this
	// machine's USB bus, not a separate decision made by a caller that cannot see the bus.
	//
	// A MAC IS THE ONE EXCEPTION, and for a reason that is about macOS rather than about buses: its
	// pointing device comes through OpenCore, and the tablet is still attached so a person can drive the
	// recovery screen. `StorageArgs` has already created the controller for the recovery disk there,
	// which is the same variable.
	// A MICROVM HAS NO PCI BUS, so there is no xHCI controller for a USB tablet to hang from - and
	// measured, asking for one stops qemu at startup:
	//
	//	qemu-system-x86_64: -device qemu-xhci,id=xhci: No 'PCI' bus found for device 'qemu-xhci'
	//
	// A guest whose job is to prove the vocabulary does not need a pointer: the steps that click are
	// exercised on the full machines, where a pointer exists. What this machine proves is that the
	// vocabulary starts a guest, reads its console, and measures its screen.
	if b.MicroVM {
		return args
	}
	if !xhciAdded {
		args = append(args, "-device", "qemu-xhci,id=xhci")
		xhciAdded = true
	}
	args = append(args, "-device", "usb-tablet,bus=xhci.0")
	// AND A KEYBOARD ON THE SAME BUS, BECAUSE SOME MENUS ONLY READ USB INPUT.
	//
	// MEASURED ON RUN 44484688558 (mx), whose recording is the SAME UEFI "Select Language" panel for
	// its whole 80 minutes:
	//
	//	=== that menu is the language submenu this medium opens on     (the check PASSED - it read
	//	                                                                Language, Keyboard, Timezone)
	//	=== leave the language submenu for the real boot menu   key esc
	//	=== up to the first entry ... up again ... and once more  key up
	//	=== boot it                                             key ret
	//	=== wait for the live desktop                           80 minutes of the same frame
	//
	// Every key was sent and none arrived. This guest had `usb-tablet` and NO KEYBOARD AT ALL: q35
	// gives it PS/2, and OVMF's graphical menu does not read PS/2 - it reads EFI Simple Text Input,
	// which on this machine is USB. The menu is a device list, which is why a pointer would not have
	// helped either.
	//
	// `usb-kbd` IS WHAT macOS ALREADY NEEDS and what `lib/vm.py` gives every guest on this bus. It is
	// harmless elsewhere: a guest that already has a PS/2 keyboard just has a second one, and the
	// keycodes the daemon sends are the same either way.
	args = append(args, "-device", "usb-kbd,bus=xhci.0")
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
	// WHICH MEDIUM MARKS AN INSTALL IS A FACT ABOUT THE GUEST, so the shape says it.
	//
	// macOS's installer IS the recovery image on a USB device, so a boot that attaches it is the one
	// that writes. Everything else installs from an ISO. Conflating the two was two published defects
	// - see the note on `FreshDisk`.
	switch b.shape().InstallMedium {
	case MediumRecovery:
		return b.Recovery != ""
	case MediumNone:
		return false
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
//
// WantsOverlay ASKS WHETHER THE DISK CAME FROM SOMEWHERE, NOT WHETHER AN INSTALLER IS ATTACHED.
//
// THE OLD RULE WAS `!installing()`, AND IT LOST WRITES. MEASURED IN easydev-org/easyvm#3: a walk that
// booted an ALREADY INSTALLED macOS disk with a plain `boot: true` got `WantsOverlay() == true`, the
// daemon made `run-overlay.qcow2`, and `ReleaseDiff` deleted it on destroy - so every pane the Setup
// Assistant completed went into a throwaway file. The symptom was a wizard that started over at the
// country list on every run, with nothing in any log, and the base disk byte-for-byte unchanged.
//
// A FRESH DISK HAS NOTHING TO PROTECT, so it runs on itself; anything else runs on a diff that
// `compress:` flattens when the stage publishes.
func (b Boot) WantsOverlay() bool { return !b.FreshDisk }

// Discardable ASKS WHETHER A TRIM MAY REACH THE DISK, AND IT IS NO LONGER THE NEGATION OF THE OVERLAY.
//
// THE TWO WERE THE SAME EXPRESSION, SO THEY WERE MUTUALLY EXCLUSIVE: a disk that kept its writes could
// not be trimmed, and a disk that could be trimmed was a throwaway. MEASURED IN easydev-org/easyvm#3
// on an installed macOS 26 disk:
//
//	guest   df -h /          12 GiB used, 108 GiB available
//	host    disk size        38.4 GiB      30.01% allocated
//
// - 26 GiB of blocks the guest had already freed, which `qemu-img convert -c` cannot remove because
// to it they look like live data. Running the same disk with `discard=unmap` and deleting the guest's
// caches gave 19.7 GiB, WRITABLE, at 6% fragmentation.
//
// AN INSTALLER MUST NOT SEE discards (the 17 GiB that vanished under an APFS snapshot is measured in
// the note above `installing`), and a fresh disk being installed has nothing worth trimming yet. Every
// other boot may trim.
func (b Boot) Discardable() bool { return !b.installing() }

// FirmwareArgs is the pflash pair.
//
// THE MACOS LOADER IS FIRMWARE, NOT A CD. OpenCore is a pflash ROM plus a variable store exactly
// like OVMF, which is why it appears here rather than in the storage section - and why a macOS guest
// given OVMF's files in these slots boots to a crossed-out circle.
func (b Boot) FirmwareArgs(ovmfCode, ovmfVars string) []string {
	// THE SHAPE SAYS WHICH FIRMWARE; THE BOOT SAYS WHERE OPENCORE'S FILES ARE.
	//
	// OpenCore's two files are BUILT PER RUN from the recovery image, so they cannot be named in a
	// shape file - and OVMF's two come from the caller, because the container image carries one build
	// and an environment may point elsewhere. So the shape chooses the KIND and each branch fills in
	// the paths it owns.
	switch b.shape().Firmware {
	case FirmwareNone:
		// A MICROVM HAS NO FIRMWARE. It boots a kernel the host hands it, which is the whole point of
		// the machine type: no pflash, no boot order, no UEFI, and one fewer thing that can fail
		// before the guest is up.
		return nil
	case FirmwareOpenCore:
		return []string{
			"-drive", "if=pflash,format=raw,readonly=on,file=" + b.OpenCoreROM,
			"-drive", "if=pflash,format=raw,file=" + b.OpenCoreVars,
		}
	case FirmwareFolder:
		// THE SHAPE WROTE THESE ARGUMENTS. `{{dir}}` is the directory `fetch:` wrote, and it is the
		// only thing this function substitutes - it does not know what files are in there, which is
		// the property `FirmwareFolder` exists for.
		return renderFirmwareArgs(b.shape().FirmwareArgs, b.firmwareDir())
	}
	return []string{
		"-drive", "if=pflash,format=raw,readonly=on,file=" + ovmfCode,
		"-drive", "if=pflash,format=raw,file=" + ovmfVars,
	}
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
	// THE FLAGS ARE THE SHAPE'S, VERBATIM, and macOS's list ends in `check` so qemu REFUSES at
	// startup rather than silently ignoring a flag this host cannot provide.
	return cpu + b.shape().CPUSuffix, nil
}

// SMP is the processor topology.
//
// A BARE COUNT IS NOT WHAT macOS UNDERSTANDS. dockur's configureSmp() spells out sockets, dies, cores
// and threads, and the guest reads that structure.
func (b Boot) SMP(cpus int) string {
	if cpus <= 0 {
		cpus = DefaultCPUs
	}
	if b.shape().SMPFormat == SMPTopology {
		return fmt.Sprintf("%d,sockets=1,dies=1,cores=%d,threads=1", cpus, cpus)
	}
	return fmt.Sprint(cpus)
}


// AudioArgs is the sound card, because a guest without one reports every capture failure as its own
// fault.
//
// MEASURED, on five live guests driven by an unattended client, and this is issue
// easydev-org/easyvm#1 in its own words:
//
//	Windows 11    audio: None - GetDefaultAudioEndpoint: Element not found. (0x80070490)
//	Ubuntu 26.04  ok, because PipeWire is in the image - the guest was given a device by luck
//	macOS 15      system_profiler SPAudioDataType prints an empty `Devices:` section
//
// `0x80070490` IS `ERROR_NOT_FOUND`, which is as precise as an error gets: there is no render
// endpoint because there is no device. Every audio test on every guest was measuring the absence of
// a sound card and reporting it as a capture failure.
//
// TWO DEVICES RATHER THAN ONE, BECAUSE THE GUESTS DISAGREE ABOUT WHAT THEY CAN ENUMERATE:
//
//	HDA   what Windows, Linux and the BSDs expect, and what their drivers bind out of the box.
//	USB   what macOS needs. MEASURED: AppleHDA's controller loads against an ICH9 HDA device once
//	      OpenCore injects a matching device-id - `com.apple.driver.AppleHDAController (600.2)`,
//	      `com.apple.iokit.IOHDAFamily (600.2)`, `com.apple.iokit.IOAudioFamily (600.2)` all appear -
//	      but it never enumerates the CODEC: `IOHDACodecDevice` has no children, `AppleHDAEngine` is
//	      never instantiated, and CoreAudio still reports zero devices. qemu's `hda-duplex` does not
//	      present a vendor/product pair that AppleHDA's personalities match.
//	      `AppleUSBAudio` is in every macOS, is already loaded, and binds qemu's `usb-audio` with no
//	      injection at all.
//
// `-audiodev none` KEEPS THE CODEC, THE PCI FUNCTION AND THE GUEST-VISIBLE ENDPOINTS and discards the
// samples once they reach qemu. That is correct here and not a compromise: the container has no sound
// server - `-audiodev help` lists only alsa/oss/wav/none and there is no `/dev/snd` - and loopback
// capture reads the render stream INSIDE the guest, before the hardware is involved.
//
// `hda-duplex` RATHER THAN `hda-output`: duplex carries line-out AND line-in, so one codec gives the
// guest a speaker endpoint and a microphone endpoint.
//
// ORDERING IS LOAD-BEARING AND QEMU'S ERROR DOES NOT SAY SO: `-audiodev` must come before the device
// that names it, or qemu fails with `audiodev snd0 not found`, which reads like a missing option
// rather than a sequencing mistake. `ich9-intel-hda` creates the controller and its HDA bus;
// `hda-duplex` is a codec ON that bus, so the controller comes first.
//
// IT IS UNCONDITIONAL, AND THAT IS DELIBERATE. A `sound:` key on the walkthrough would let a file
// silently opt out, and the failure mode is the one above: a guest with no card that reports it as a
// capture bug three stages later.
func (b Boot) AudioArgs() []string {
	// THE SHAPE SAYS WHICH CARD, AND `none` IS A REAL ANSWER RATHER THAN AN ABSENCE.
	//
	// A MICROVM HAS NO PCI BUS, so `ich9-intel-hda` - the default - cannot attach, and the device it
	// cannot have is the one that stops the machine at startup.
	switch b.shape().Audio {
	case "none":
		return nil
	case "usb":
		// `usb-audio` NEEDS THE xHCI CONTROLLER, which macOS's own device list creates. On a guest
		// with neither, `bus=xhci.0` names a bus that does not exist and qemu refuses to start.
		return []string{
			"-audiodev", "none,id=snd0",
			"-device", "usb-audio,bus=xhci.0,audiodev=snd0",
		}
	}
	// `hda-duplex` RATHER THAN `hda-output`: duplex carries line-out AND line-in, so one codec gives
	// the guest a speaker endpoint and a microphone endpoint.
	//
	// ORDERING IS LOAD-BEARING AND QEMU'S ERROR DOES NOT SAY SO: `-audiodev` must come before the
	// device that names it, or qemu fails with `audiodev snd0 not found`, which reads like a missing
	// option rather than a sequencing mistake.
	return []string{
		"-audiodev", "none,id=snd0",
		"-device", "ich9-intel-hda",
		"-device", "hda-duplex,audiodev=snd0",
	}
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

// KernelArgs hands the guest its kernel directly, which is how a microvm boots.
//
// A `microvm` MACHINE HAS NO FIRMWARE, so there is no boot sector for a disk to carry and nothing to
// read an El Torito image. The kernel comes out of the medium the host already has, and qemu starts
// it - which is exactly the mechanism `os: android` used before this, and the comment in this file
// records why it was the wrong answer THERE: Android's own boot chain is what its install proves. A
// microvm is the opposite case: the walkthrough proves the VOCABULARY, not a boot loader.
func (b Boot) KernelArgs() []string {
	if b.Kernel == "" {
		return nil
	}
	args := []string{"-kernel", b.Kernel}
	if b.Initrd != "" {
		args = append(args, "-initrd", b.Initrd)
	}
	// THE APPEND IS THE GUEST'S OWN BOOT LINE, and the default is Alpine's own `syslinux.cfg`:
	//
	//	APPEND modules=loop,squashfs,sd-mod,usb-storage quiet
	//
	// plus `console=ttyS0` - a microvm has no display, so the serial line is its only output, and
	// without it the kernel writes to a VGA adapter that does not exist and the console stays
	// EMPTY, which reads as "the guest never booted".
	//
	// `modloop=` NAMES THE SQUASHFS INSIDE THE MEDIUM, and it is what makes an Alpine microvm come
	// up: MEASURED, without it the initramfs mounts nothing and drops to
	//
	//	mount: mounting /dev/vda on /sysroot failed: Invalid argument
	//	initramfs emergency recovery shell launched
	//
	// while WITH it the same machine prints `Welcome to Alpine Linux 3.21` and `localhost login:` in
	// 2.09 seconds on 512 MiB.
	// `modloop=/dev/vda` IS THE MEDIUM'S OWN SQUASHFS, at the block device the boot step attaches.
	// MEASURED, live: with it the guest prints `Welcome to Alpine Linux 3.21` and `localhost login:`
	// in 2.09 seconds on 512 MiB; without it the initramfs mounts nothing.
	cmdline := b.KernelAppend
	if cmdline == "" {
		cmdline = "console=ttyS0 modules=loop,squashfs,sd-mod modloop=/dev/vda"
	}
	// `pci=off` IS ADDED TO A MICROVM'S LINE THAT HAS NO PCI BUS, INCLUDING ONE THAT WROTE ITS OWN.
	//
	// A microvm with `pcie=off` has no PCI bus, and a kernel that finds an ACPI root - which every
	// microvm now gets, because Debian needs it to see its MMIO disk - assumes a PCI host bridge
	// exists and stops:
	//
	//	PCI: Fatal: No config space access function found
	//
	// MEASURED on the NixOS guest, which died at 0.05s with that line and nothing else. With
	// `pci=off` the same kernel prints the warning and continues to
	//
	//	EXT4-fs (vda): mounted filesystem ... r/w with ordered data mode
	//	echo[464]: NIXOS-MICROVM-READY
	//	nixos-microvm login: root (automatic login)
	//
	// The statement is TRUE of this machine in every case, so it is prepended rather than left to
	// each walkthrough: a file that adds its own `console=` should not also have to know that the
	// absence of a PCI bus has to be declared.
	//
	// A DESKTOP MICROVM HAS A PCIe BUS ON PURPOSE (see `machineType`), so it must NOT carry
	// `pci=off`: that would hide the bus the virtio-gpu is attached to and the GPU would vanish.
	if !(b.MicroVM && b.MicroVMDisplay == "desktop") && !strings.Contains(cmdline, "pci=off") {
		cmdline = "pci=off " + cmdline
	}
	args = append(args, "-append", cmdline)
	return args
}

// virtioBlk is the disk device name this machine can attach.
//
// `virtio-blk-pci` NEEDS A PCI BUS, and a microvm has none - measured: the machine refuses at
// startup with `-device virtio-blk-pci: no bus found`, which arrives as `qmp ... connect: connection
// refused` because qemu exits before creating the socket. The mmio form is the same driver to the
// guest.
func (b Boot) virtioBlk() string {
	if b.MicroVM {
		return "virtio-blk-device"
	}
	return "virtio-blk-pci"
}

// VirtioNet is the network device name this machine can attach.
func (b Boot) VirtioNet() string {
	if b.MicroVM {
		return "virtio-net-device"
	}
	return "virtio-net-pci"
}

// renderFirmwareArgs replaces `{{dir}}` with the firmware directory.
//
// IT IS THE WHOLE OF WHAT THIS BUILD KNOWS ABOUT A `kind: folder` FIRMWARE: a placeholder and a
// path. Everything else - which files, how many, which is read-only - is in the shape file.
func renderFirmwareArgs(pair []string, dir string) []string {
	out := make([]string, len(pair))
	for i, a := range pair {
		out[i] = strings.ReplaceAll(a, "{{dir}}", dir)
	}
	return out
}

// targetDiskArgs is the guest's own disk, in the form this build uses for every guest.
//
// IT IS SPLIT OUT SO THE TWO FIRMWARE PATHS SHARE ONE SPELLING. `FirmwareOpenCore` and
// `FirmwareFolder` both attach a target after their own disks, and two copies of this argument pair
// would be two chances to spell `discard` differently - which is the argument that cost three
// quarters of a macOS recording when it was on during an install.
func (b Boot) targetDiskArgs() []string {
	target := "file=" + b.Disk + ",if=none,format=qcow2,id=target"
	if b.Discardable() {
		target = "file=" + b.Disk + ",if=none,format=qcow2,discard=unmap,id=target"
	}
	index := 1
	if b.shape().Firmware == FirmwareFolder {
		// THE SHAPE WRITES ITS OWN `bootindex` VALUES, so the target is the number AFTER the highest
		// one it declared. Counting the PAIRS was wrong: an OpenCore disk is one drive plus one
		// device, and both pairs together are one index. MEASURED: counting them produced
		// `bootindex=2` where the firmware's own disk is 0, and the guest then reported it could not
		// load Boot0001.
		index = 1 + b.shape().highestFirmwareBootIndex()
	}
	return []string{
		"-drive", target,
		"-device", fmt.Sprintf("virtio-blk-pci,drive=target,bootindex=%d", index),
	}
}

// firmwareDir is where a `kind: folder` firmware lives.
//
// `{{work}}` IN A SHAPE'S `firmware.dir:` IS THE WORK DIRECTORY, and it is the only placeholder this
// level substitutes - the argument lists below use `{{dir}}`, which is the same directory spelled
// inside the arguments. A shape that names a subdirectory instead (`firmware/opencore`) gets that
// joined onto the work directory, so a firmware can live somewhere specific without the shape having
// to know an absolute path it cannot see.
func (b Boot) firmwareDir() string {
	dir := b.shape().FirmwareDir
	if strings.Contains(dir, "{{work}}") {
		return strings.ReplaceAll(dir, "{{work}}", b.Work)
	}
	if dir == "" || filepath.IsAbs(dir) {
		return dir
	}
	return filepath.Join(b.Work, dir)
}

// requireFirmwareFiles checks that every `file=` an argument names is on disk.
//
// IT IS THE ONLY CHECK THIS BUILD CAN MAKE ABOUT A FIRMWARE IT DOES NOT KNOW: the paths come from the
// shape, so a missing one is either a stage that never fetched or a shape that names the wrong file -
// and both are better as a sentence here than as qemu starting and finding no loader.
//
// ONLY `-drive`-SHAPED ARGUMENTS ARE CHECKED, which is what `file=` appears in. A `-device` that
// mentions a path (none does) would be this build guessing, and guessing is what this whole mechanism
// exists to avoid.
func requireFirmwareFiles(pair []string, dir, where string) error {
	for _, a := range pair {
		for _, part := range strings.Split(a, ",") {
			if !strings.HasPrefix(part, "file=") {
				continue
			}
			path := strings.ReplaceAll(strings.TrimPrefix(part, "file="), "{{dir}}", dir)
			if err := requireFile(path, "the firmware file `"+filepath.Base(path)+"` named by "+
				where); err != nil {
				return fmt.Errorf("%w. A shape's `firmware.dir:` is where the `fetch:` step writes, so "+
					"an empty directory means that step did not run", err)
			}
		}
	}
	return nil
}

// KernelAppendline is the guest's boot line, with the defaults and the one flag this build must add.
//
// IT IS THE ONE PIECE OF `KernelArgs` THAT IS NOT THE SHAPE'S ARGUMENTS, and it stays in Go for a
// reason that is about the KERNEL rather than about the machine: `pci=off` is prepended when the
// machine has no PCIe bus, because a kernel that finds an ACPI root assumes a PCI host bridge exists
// and stops with `PCI: Fatal: No config space access function found`. MEASURED on NixOS, which died
// at 0.05s without it and boots with it.
//
// A DESKTOP MICROVM HAS A PCIe BUS ON PURPOSE, so it must NOT carry `pci=off`: that would hide the
// bus its virtio-gpu is attached to and the GPU would vanish.
func (b Boot) KernelAppendline(machineString string) string {
	cmdline := b.KernelAppend
	if cmdline == "" {
		// ALPINE'S OWN `syslinux.cfg` LINE plus `console=ttyS0`: a microvm's serial line is its only
		// output, and without it the kernel writes to an adapter that does not exist and the console
		// stays EMPTY - which reads as "the guest never booted".
		cmdline = "console=ttyS0 modules=loop,squashfs,sd-mod modloop=/dev/vda"
	}
	// `pci=off` IS FOR A MACHINE WITH NO PCIe BUS, which the profile's own `-machine` string states.
	if !strings.Contains(machineString, "pcie=off") {
		return cmdline
	}
	if strings.Contains(cmdline, "pci=off") {
		return cmdline
	}
	return "pci=off " + cmdline
}

// DiscardFragment is `,discard=unmap` when a trim may reach the disk, and empty otherwise.
//
// IT IS A PLACEHOLDER RATHER THAN A CONDITIONAL IN THE SHAPE because the SHAPE cannot know: whether
// a trim is safe depends on whether an installer is attached to THIS boot, which is a fact about the
// run. During a macOS install `discard` is destructive - measured: 42009463444 wrote 36774 MiB,
// dropped to 19761 and froze at "About 48 minutes remaining", because qemu honoured every TRIM for
// clusters the APFS snapshot still referred to.
func (b Boot) DiscardFragment() string {
	if b.Discardable() {
		return ",discard=unmap"
	}
	return ""
}

// AudioDevArg and AudioCardArg are the two halves of the sound card, as placeholders.
//
// THEY ARE SEPARATE BECAUSE A SHAPE'S `qemu.args` IS A LIST OF PAIRS, and `-audiodev X` / `-device Y`
// is two pairs. Returning one string that a caller splits would put a qemu argument grammar in a
// string, which is what the pair rule exists to avoid.
//
// EITHER BOTH ARE NON-EMPTY OR NEITHER IS, so the drop rule removes the two pairs together and a
// guest never gets a device that names an audiodev qemu was not given.
func (b Boot) AudioDevArg() string {
	if b.audioCard() == "" {
		return ""
	}
	return "none,id=snd0"
}

// AudioCardArg is the device, or empty when this machine cannot have one.
func (b Boot) AudioCardArg() string { return b.audioCard() }

// audioCard is the one place the sound-card decision lives.
//
// `none` IS A REAL ANSWER RATHER THAN AN ABSENCE: a microvm has no PCI bus, so `ich9-intel-hda` -
// the default - cannot attach, and the device it cannot have is the one that stops the machine at
// startup. A DESKTOP microvm is the exception, because it turns PCIe on for its virtio-gpu.
func (b Boot) audioCard() string {
	switch b.shape().Audio {
	case "none":
		// A DESKTOP MICROVM HAS A PCIe BUS ON PURPOSE, so the card attaches. The headless forms do
		// not, and asking them for one is `No 'PCI' bus found for device 'ich9-intel-hda'` at
		// startup - qemu exits and the step reports a qmp socket error.
		if b.MicroVM && b.MicroVMDisplay == "desktop" {
			return "ich9-intel-hda"
		}
		return ""
	case "usb":
		// `usb-audio` NEEDS THE xHCI CONTROLLER, which macOS's own device list creates. On a guest
		// with neither, `bus=xhci.0` names a bus that does not exist and qemu refuses to start.
		return "usb-audio,bus=xhci.0,audiodev=snd0"
	}
	// `hda-duplex` RATHER THAN `hda-output`: duplex carries line-out AND line-in, so one codec gives
	// the guest a speaker endpoint and a microphone endpoint.
	return "hda-duplex,audiodev=snd0"
}




// ProfileName is which of a shape's `qemu.profiles` this boot runs.
//
// IT IS THE DISPLAY MODE FOR A MICROVM, because that is the axis a microvm's command line actually
// varies on: `serial` has no adapter and no PCIe, `terminal` an ISA VGA, `desktop` a virtio-gpu on a
// PCIe bus it turns on. Every other guest has one configuration, so this is empty and the shape's
// `qemu.default` answers.
func (b Boot) ProfileName() string {
	if b.MicroVM {
		if b.MicroVMDisplay == "" {
			return "terminal"
		}
		return b.MicroVMDisplay
	}
	// A RECOVERY IMAGE IS WHAT MAKES A BOOT AN INSTALL for a machine whose install medium is one -
	// which is macOS and nothing else here, and the shape says so rather than this function. That is
	// the same fact `installing()` reads, stated once: the profile that attaches a recovery image is
	// the profile that installs, and the profile that does not is an installed system whose freed
	// blocks may reach the file.
	if b.shape().InstallMedium == MediumRecovery {
		if b.Recovery != "" {
			return "install"
		}
		return "disk"
	}
	return ""
}
