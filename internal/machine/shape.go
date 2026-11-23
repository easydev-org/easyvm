package machine

// A SHAPE IS THE MACHINE A GUEST IS GIVEN, AS DATA.
//
// THE FIFTEEN `if b.OS.IsMacOS()` BRANCHES THIS REPLACES were all answering the same four questions -
// which firmware, which CPU flags, which devices, which media - and each answer was a fact about an
// operating system rather than a decision the engine made. A fact belongs in a file somebody can read
// and change; the engine's job is to assemble qemu's argument list from it.
//
// SO THERE IS NO `switch` LEFT ON THE OPERATING SYSTEM. `Boot` carries a Shape, and every builder
// (`FirmwareArgs`, `CPU`, `SMP`, `VGA`, `AudioArgs`, `SMCArgs`, `ExtraArgs`) reads a field. Adding a
// machine is adding a YAML file - see `shapes/` - and nothing here changes.
//
// THE ONE THING THIS CANNOT EXPRESS IS A MEASUREMENT, so every field's comment carries the one that
// made it what it is. A shape file that says `-device vmware-svga` without saying why is a file whose
// next editor will "fix" it to `vgamem_mb=256` and lose three quarters of a recording.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// FirmwareKind is how the guest is started before any operating system code runs.
type FirmwareKind string

const (
	// FirmwareOVMF is UEFI: pflash code plus a writable variables store. Every Windows, Linux and
	// BSD guest here uses it.
	FirmwareOVMF FirmwareKind = "ovmf"
	// FirmwareOpenCore is macOS's loader, which is itself a disk the firmware boots first.
	//
	// OPENCORE IS NOT OPTIONAL FOR macOS, EVEN FROM AN INSTALLED DISK: without it qemu starts, the
	// firmware finds no loader, and the guest sits in the UEFI shell.
	//
	// IT IS KEPT AS A NAME BUT NOT AS A SPECIAL CASE. A shape may still write `kind: opencore` and
	// get the layout below, which is what every existing file does; the same machine can be written
	// as `kind: folder` with its own `args:` and `disks:`, and that form is what a firmware this
	// build has never heard of uses.
	FirmwareOpenCore FirmwareKind = "opencore"
	// FirmwareFolder is FIRMWARE THE ENGINE DOES NOT KNOW.
	//
	// `fetch:` writes a DIRECTORY, and the shape says which files in it are the pflash pair and which
	// disk the firmware boots - as qemu arguments with `{{dir}}` standing for that directory. The
	// engine never learns what the files are called, what they mean, or how many of them there are.
	//
	// THE PROPERTY THIS BUYS IS THE POINT: a new firmware is a shape file and a `fetch:`, not a Go
	// branch. MEASURED by the one it was written for - OpenCore was `FirmwareOpenCore`, three fields
	// on the boot spec, two special cases in `StorageArgs` and one in `FirmwareArgs`, all of it
	// knowledge about macOS living in a package that is supposed to know only what a shape tells it.
	FirmwareFolder FirmwareKind = "folder"
	// FirmwareNone is a microvm. It has no firmware at all - the host hands qemu a kernel - which is
	// the whole point of the machine type: no pflash, no boot order, no UEFI, and one fewer thing
	// that can fail before the guest is up.
	FirmwareNone FirmwareKind = "none"
)

// SMPFormat is the grammar qemu's `-smp` accepts for this guest.
type SMPFormat string

const (
	// SMPPlain is a bare count, which every guest except macOS understands.
	SMPPlain SMPFormat = "plain"
	// SMPTopology spells out sockets, dies, cores and threads. A BARE COUNT IS NOT WHAT macOS
	// UNDERSTANDS: dockur's configureSmp() spells out the structure, and the guest reads it.
	SMPTopology SMPFormat = "topology"
)

// InstallMedium says which attached medium means "this boot writes".
//
// IT EXISTS BECAUSE THE TWO ANSWERS ARE ALREADY MEASURED AND THEY DIFFER. macOS installs FROM its
// recovery image, so a boot that attaches `recovery:` is the one that writes. Everything else installs
// from an ISO, so `iso:` is what marks an install. Conflating them was two published defects - see the
// note on `Boot.FreshDisk`.
type InstallMedium string

const (
	MediumISO      InstallMedium = "iso"
	MediumRecovery InstallMedium = "recovery"
	MediumNone     InstallMedium = "none"
)

// ShapeFile is one YAML file in a shapes directory.
//
// IT IS DELIBERATELY CLOSE TO THE SHAPE RATHER THAN TO THE FILE'S SYNTAX: a reader should be able to
// open `shapes/macos.yaml` beside this struct and see one field per key.
type ShapeFile struct {
	// Name is what a walkthrough's `os:` says. It is also the file's stem, by convention.
	Name string `yaml:"name"`
	// Description is one line for `easyvm check` and for the error that lists what this build has.
	Description string `yaml:"description"`

	Firmware FirmwareSpec `yaml:"firmware"`
	CPU      CPUSpec      `yaml:"cpu"`
	SMP      SMPSpec      `yaml:"smp"`
	Video    VideoSpec    `yaml:"video"`
	Audio    string       `yaml:"audio"`
	// SMC is Apple's System Management Controller, which only macOS reads.
	SMC bool `yaml:"smc"`
	// QGA is how the guest agent's chardev reaches the guest: `virtio-serial` (the default) or
	// `isa-serial`.
	//
	// TWO GUESTS NEED `isa-serial`, FOR DIFFERENT REASONS, AND NEITHER IS A PREFERENCE:
	//
	//	macOS      Apple's own agent never loads under qemu, and the third-party one that does probes
	//	           `/dev/cu.serial1` - the FIRST isa-serial device qemu adds, so this must be the only
	//	           one.
	//	a microvm  `virtio-serial` is a PCI device and a microvm has no PCI bus:
	//	           `-device virtio-serial: No 'PCI' bus found for device 'virtio-serial-pci'`.
	//
	// It is a SHAPE FIELD rather than a name check because both of those are facts about a machine,
	// and the daemon that assembles the argument list should read them rather than decide them.
	QGA string `yaml:"qga"`
	// InstallMedium is which attached medium marks this boot as an install.
	InstallMedium InstallMedium `yaml:"install_medium"`
	// Media is which media this guest may be given, and refused otherwise.
	Media MediaSpec `yaml:"media"`
	// QEMU is the raw command line this machine needs. See `QEMUSpec`.
	QEMU QEMUSpec `yaml:"qemu"`
}

// FirmwareSpec is what the `-drive if=pflash` lines name.
type FirmwareSpec struct {
	Kind FirmwareKind `yaml:"kind"`
	// Code and Vars are the two pflash slots for `ovmf`. They are templates because they name files
	// the DAEMON chooses: `{{.Code}}` and `{{.Vars}}` are filled from the caller's environment.
	Code string `yaml:"code"`
	Vars string `yaml:"vars"`

	// Dir is the DIRECTORY `fetch:` writes, for `kind: folder`. It is a path relative to the work
	// directory, and `{{dir}}` in `Args`/`Disks` stands for its absolute form.
	Dir string `yaml:"dir"`
	// Args are the qemu arguments that attach the firmware, one array per argument pair.
	//
	// THESE ARE THE `-drive if=pflash` LINES AND NOTHING ELSE the engine has to understand: whether a
	// firmware is two pflash slots, one, or none at all is the shape's business. A `{{dir}}` in any
	// argument is replaced with the directory's absolute path.
	Args [][]string `yaml:"args"`
	// Disks are qemu arguments for disks the FIRMWARE itself boots from, placed before the guest's
	// own target disk.
	//
	// OPENCORE IS THE REASON THIS EXISTS: it boots from a raw image of its own (`boot.img`) that has
	// to be `bootindex=0`, so the guest's target disk is second. A firmware that boots a partition
	// inside the target, or a CD, needs nothing here.
	Disks [][]string `yaml:"disks"`
}

// CPUSpec is what is appended to the resolved CPU model.
type CPUSpec struct {
	// Suffix is appended verbatim after a comma, which is how every qemu CPU flag is spelled.
	Suffix string `yaml:"suffix"`
}

// SMPSpec is how `-smp` is rendered.
type SMPSpec struct {
	Format SMPFormat `yaml:"format"`
}

// VideoSpec is the adapter, as a`kind` the daemon used to switch on.
//
// IT IS STILL HERE BECAUSE `VGA()` READS IT, and `kind` now means "which of `qemu.video` to use"
// rather than a branch in Go. See `QEMUSpec`.
type VideoSpec struct {
	// Kind is the adapter this shape wants: `vga`, `virtio`, `none` or `absent`. It names one of
	// `qemu.video`'s entries when that map is used, and is otherwise a label for a reader.
	Kind string `yaml:"kind"`
}

// QEMUSpec is THE RAW QEMU COMMAND LINE THIS MACHINE NEEDS, written by the shape.
//
// IT REPLACES SIX FUNCTIONS AND A BOOL: `VGA`, `StorageArgs`' two branches, `AudioArgs`' two,
// `SMCArgs`, `KernelArgs`, `microvm`'s six conditionals and `virtioBlk`/`VirtioNet`. Every one of
// them answered "what does this machine look like", which is a fact about a guest and belongs in a
// file somebody can read.
//
// A SHAPE MAY HOLD MORE THAN ONE PROFILE because one machine can be started in more than one
// CONFIGURATION and the difference is not a placeholder: a microvm draws on nothing, an ISA VGA, or a
// virtio-gpu depending on whether its session is serial, a text console or a desktop - and each of
// those changes the `-machine` options, the adapter AND whether PCIe exists. Writing that as
// `{{pcie}}`/`{{graphics}}`/`{{video}}` put three conditionals back in the file in a different
// spelling. A profile writes each configuration out literally instead.
type QEMUSpec struct {
	// Default is the profile a step gets when it names none. Empty means the first profile in name
	// order, which keeps a single-profile shape from having to state one.
	Default string `yaml:"default"`
	// Profiles are the configurations, keyed by the name a step selects.
	Profiles map[string]QEMUProfile `yaml:"profiles"`
}

// QEMUProfile is one configuration of one machine.
//
// THE PLACEHOLDERS ARE THE ONLY THING THE ENGINE SUBSTITUTES, and every one is a value only the run
// knows - never a choice between configurations:
//
//	{{disk}}       the target disk's path
//	{{iso}}        the installer medium, empty when none
//	{{recovery}}   the recovery image, empty when none
//	{{answer}}     Windows Setup's answer CD
//	{{driver}}     Windows Setup's driver CD
//	{{dir}}        the firmware directory (`firmware.dir`)
//	{{work}}       the run's working directory
//	{{kernel}}     the kernel a microvm boots
//	{{initrd}}     its initramfs
//	{{append}}     its kernel command line
//	{{discard}}    `,discard=unmap` when a trim may reach the disk, empty otherwise
//
// A PAIR WITH AN EMPTY PLACEHOLDER IS DROPPED, which is how `{{iso}}` and `{{recovery}}` make an
// optional medium disappear without a conditional.
type QEMUProfile struct {
	// Machine is the `-machine` value.
	Machine string `yaml:"machine"`
	// Args are the device and drive arguments, in order, one array per argument pair.
	//
	// THE ORDER IS THE FILE'S, not this build's: a bus must precede the device that names it, a
	// `-audiodev` must precede the sound card, and an OpenCore disk must be `bootindex=0` before the
	// target.
	Args [][]string `yaml:"args"`
	// KernelArgs are appended when the boot names a kernel, instead of firmware reading a boot
	// sector. `{{kernel}}`, `{{initrd}}` and `{{append}}` are substituted.
	KernelArgs [][]string `yaml:"kernel_args"`
}

// MediaSpec is which of the optional media a guest may be given.
type MediaSpec struct {
	// Recovery says macOS's recovery image is legal for this guest. Nothing else may attach one.
	Recovery bool
	// RecoveryArgs attach the recovery image, and are used only when one is given.
	//
	// `{{recovery}}` IS THE IMAGE'S PATH and `{{dir}}` the firmware directory. THE CONTROLLER IS
	// PART OF THE LIST because a `usb-storage,bus=xhci.0` device names a bus that does not exist
	// unless something creates it - qemu refuses at startup with
	//
	//	-device usb-storage,bus=xhci.0,drive=rec: Bus 'xhci.0' not found
	//
	// and exits, so the QMP socket never appears and the boot step reports a socket error rather
	// than the missing device.
	RecoveryArgs [][]string `yaml:"recovery_args"`
	// DeviceArgs are attached on EVERY boot of this guest, before its disks.
	//
	// THE BUS AND THE POINTER BELONG HERE RATHER THAN IN `recovery_args`: a macOS guest has no PS/2,
	// so its tablet must exist whether or not a recovery image is attached - and the controller must
	// exist before the device that names `bus=xhci.0`. MEASURED: with the tablet inside the
	// recovery-only list, a macOS guest booted from an installed disk had no pointer at all and its
	// first click died in qemu.
	DeviceArgs [][]string `yaml:"device_args"`
	// ISO false means an install ISO is refused with the reason from ISOReason.
	ISO       bool   `yaml:"iso"`
	ISOReason string `yaml:"iso_reason"`
	// AnswerAndDriver says Windows Setup's two extra CDs are legal here.
	AnswerAndDriver bool `yaml:"answer_and_driver"`
}

// Shape is a ShapeFile that has been checked and compiled.
//
// THE COMPILED FORM IS SEPARATE FROM THE FILE SO VALIDATION HAPPENS ONCE. A format string with the
// wrong number of verbs, or an adapter kind nothing knows how to render, is a load-time error rather
// than a qemu failure forty minutes into a stage.
type Shape struct {
	Name        string
	Description string

	Firmware      FirmwareKind
	FirmwareCode  string
	FirmwareVars  string
	// RecoveryArgs is `media.recovery_args`, flattened - each pair is two qemu arguments.
	RecoveryArgs []string
	// DeviceArgs is `media.device_args`, flattened.
	DeviceArgs []string
	// FirmwareDir, FirmwareArgs and FirmwareDisks are `kind: folder`'s compiled form: a directory
	// the `fetch:` step wrote, the arguments that attach it, and the firmware's own boot disks.
	FirmwareDir  string
	FirmwareArgs []string
	// FirmwareDisks keeps its pair structure, because each entry becomes two qemu arguments and a
	// flattening would let an odd count through.
	FirmwareDisks [][]string
	CPUSuffix     string
	SMPFormat     SMPFormat
	Video         string
	Audio         string
	SMC           bool
	QGA           string
	InstallMedium InstallMedium
	Media         MediaSpec
	// QEMU is `qemu`, with each profile checked and kept as written.
	QEMU map[string]QEMUProfile
	// QEMUDefault is the profile a step gets when it names none.
	QEMUDefault string
}

// Validate compiles a ShapeFile, refusing anything the builders could not render.
func (f ShapeFile) Validate() (Shape, error) {
	var s Shape
	if f.Name == "" {
		return s, fmt.Errorf("a shape must have a `name:`; it is what a walkthrough's `os:` says")
	}
	s.Name, s.Description = f.Name, f.Description

	switch f.Firmware.Kind {
	case FirmwareOVMF:
		// THE TEMPLATES ARE REQUIRED FOR OVMF AND IGNORED FOR THE OTHERS, and the difference is
		// stated rather than tolerated: a `none` shape carrying pflash paths is a file whose author
		// expected firmware and will not get it.
		if f.Firmware.Code == "" || f.Firmware.Vars == "" {
			return s, fmt.Errorf("shape %q: `firmware.kind: ovmf` needs `code:` and `vars:` - the two "+
				"pflash slots qemu is given", f.Name)
		}
		s.Firmware, s.FirmwareCode, s.FirmwareVars = FirmwareOVMF, f.Firmware.Code, f.Firmware.Vars
	case FirmwareOpenCore:
		// OPENCORE'S PATHS COME FROM THE BOOT, NOT FROM THE FILE: they are built per run from the
		// recovery image. A shape that named them would name a file that does not exist yet.
		if f.Firmware.Code != "" || f.Firmware.Vars != "" {
			return s, fmt.Errorf("shape %q: `firmware.kind: opencore` must not name `code:`/`vars:`; "+
				"they are built from the recovery image on each run", f.Name)
		}
		if f.Firmware.Dir != "" || len(f.Firmware.Args) > 0 || len(f.Firmware.Disks) > 0 {
			return s, fmt.Errorf("shape %q: `firmware.kind: opencore` does not take `dir:`, `args:` or "+
				"`disks:` - they are the layout this build already knows. Write `kind: folder` to "+
				"state that layout in the file instead", f.Name)
		}
		s.Firmware = FirmwareOpenCore
	case FirmwareFolder:
		// A DIRECTORY AND THE ARGUMENTS THAT ATTACH IT. Nothing about the files is checked here
		// beyond the shape of what the file says, because what they are called is exactly what this
		// build does not know.
		if f.Firmware.Dir == "" {
			return s, fmt.Errorf("shape %q: `firmware.kind: folder` needs `dir:` - the directory "+
				"`fetch:` writes, relative to the work directory", f.Name)
		}
		if len(f.Firmware.Args) == 0 {
			return s, fmt.Errorf("shape %q: `firmware.kind: folder` needs `args:` - the qemu "+
				"arguments that attach it. An empty list is a firmware nothing boots", f.Name)
		}
		for i, pair := range f.Firmware.Args {
			if len(pair) != 2 {
				return s, fmt.Errorf("shape %q: `firmware.args[%d]` has %d element(s). Each entry is "+
					"ONE argument pair, because a qemu flag without its value is an error naming the "+
					"flag rather than what was meant", f.Name, i, len(pair))
			}
		}
		for i, pair := range f.Firmware.Disks {
			if len(pair) != 2 {
				return s, fmt.Errorf("shape %q: `firmware.disks[%d]` has %d element(s); each entry is "+
					"one argument pair", f.Name, i, len(pair))
			}
		}
		s.Firmware, s.FirmwareDir = FirmwareFolder, f.Firmware.Dir
		for _, pair := range f.Firmware.Args {
			s.FirmwareArgs = append(s.FirmwareArgs, pair...)
		}
		s.FirmwareDisks = f.Firmware.Disks
	case FirmwareNone:
		if f.Firmware.Code != "" || f.Firmware.Vars != "" {
			return s, fmt.Errorf("shape %q: `firmware.kind: none` must not name `code:`/`vars:`; a "+
				"machine with no firmware has no pflash to read", f.Name)
		}
		s.Firmware = FirmwareNone
	case "":
		return s, fmt.Errorf("shape %q: `firmware.kind:` is required; one of %q, %q, %q or %q",
			f.Name, FirmwareOVMF, FirmwareOpenCore, FirmwareNone, FirmwareFolder)
	default:
		return s, fmt.Errorf("shape %q: `firmware.kind: %s` is not a firmware this build knows; one of "+
			"%q, %q, %q or %q", f.Name, f.Firmware.Kind, FirmwareOVMF, FirmwareOpenCore,
			FirmwareNone, FirmwareFolder)
	}

	for i, pair := range f.Media.DeviceArgs {
		if len(pair) != 2 {
			return s, fmt.Errorf("shape %q: `media.device_args[%d]` has %d element(s); each entry "+
				"is one argument pair", f.Name, i, len(pair))
		}
		s.DeviceArgs = append(s.DeviceArgs, pair...)
	}
	for i, pair := range f.Media.RecoveryArgs {
		if len(pair) != 2 {
			return s, fmt.Errorf("shape %q: `media.recovery_args[%d]` has %d element(s); each entry "+
				"is one argument pair", f.Name, i, len(pair))
		}
		s.RecoveryArgs = append(s.RecoveryArgs, pair...)
	}

	if len(f.QEMU.Profiles) == 0 {
		return s, fmt.Errorf("shape %q: `qemu.profiles` is empty; a shape with no command line "+
			"describes a machine nothing can start", f.Name)
	}
	for name, prof := range f.QEMU.Profiles {
		if prof.Machine == "" {
			return s, fmt.Errorf("shape %q: `qemu.profiles.%s.machine` is empty; it is the `-machine` "+
				"value and qemu has no default for it", f.Name, name)
		}
		for i, pair := range prof.Args {
			if len(pair) != 2 {
				return s, fmt.Errorf("shape %q: `qemu.profiles.%s.args[%d]` has %d element(s). Each "+
					"entry is ONE argument pair, because a qemu flag without its value is an error "+
					"naming the flag rather than what was meant", f.Name, name, i, len(pair))
			}
		}
		for i, pair := range prof.KernelArgs {
			if len(pair) != 2 {
				return s, fmt.Errorf("shape %q: `qemu.profiles.%s.kernel_args[%d]` has %d element(s); "+
					"each entry is one argument pair", f.Name, name, i, len(pair))
			}
		}
	}
	if f.QEMU.Default != "" {
		if _, ok := f.QEMU.Profiles[f.QEMU.Default]; !ok {
			return s, fmt.Errorf("shape %q: `qemu.default: %q` names no profile; the profiles are %s",
				f.Name, f.QEMU.Default, strings.Join(sortedKeysQEMU(f.QEMU.Profiles), ", "))
		}
	}
	s.QEMU, s.QEMUDefault = f.QEMU.Profiles, f.QEMU.Default

	s.CPUSuffix = f.CPU.Suffix

	switch f.SMP.Format {
	case SMPPlain, SMPTopology:
		s.SMPFormat = f.SMP.Format
	case "":
		s.SMPFormat = SMPPlain
	default:
		return s, fmt.Errorf("shape %q: `smp.format: %s` is not one this build knows; %q or %q",
			f.Name, f.SMP.Format, SMPPlain, SMPTopology)
	}

	switch f.Video.Kind {
	case "vga", "virtio", "none", "absent":
		s.Video = f.Video.Kind
	case "":
		// THE DEFAULT IS `vga`, because that is what a shape of this repository's shape means: a
		// guest given a VGA adapter at the resolution its coordinates were measured at.
		s.Video = "vga"
	default:
		return s, fmt.Errorf("shape %q: `video.kind: %s` is not an adapter this build knows; one of "+
			"\"vga\" (the -device VGA,xres= form), \"virtio\" (virtio-vga, which carries a DRM node), "+
			"\"none\" (another device supplies one) or \"absent\" (no display at all)", f.Name, f.Video.Kind)
	}

	switch f.Audio {
	case "hda", "usb", "none":
		s.Audio = f.Audio
	case "":
		s.Audio = "hda"
	default:
		return s, fmt.Errorf("shape %q: `audio: %s` is not a sound device this build knows; one of "+
			"\"hda\" (what Windows, Linux and the BSDs bind out of the box), \"usb\" (what macOS "+
			"needs) or \"none\"", f.Name, f.Audio)
	}

	switch f.InstallMedium {
	case MediumISO, MediumRecovery, MediumNone:
		s.InstallMedium = f.InstallMedium
	case "":
		s.InstallMedium = MediumISO
	default:
		return s, fmt.Errorf("shape %q: `install_medium: %s` is not one of %q, %q or %q",
			f.Name, f.InstallMedium, MediumISO, MediumRecovery, MediumNone)
	}


	s.Media = f.Media
	// A SHAPE THAT CAN TAKE AN INSTALL ISO MUST SAY SO, because the refusal for the others names a
	// reason and a reason for a guest that has no install ISO is a fact worth keeping.
	if s.Media.ISO && s.Media.ISOReason != "" {
		return s, fmt.Errorf("shape %q: `media.iso: true` and `media.iso_reason:` are contradictory; "+
			"the reason is only read when an ISO is refused", f.Name)
	}
	if !s.Media.ISO && s.Media.ISOReason == "" {
		return s, fmt.Errorf("shape %q: `media.iso: false` needs `media.iso_reason:` - a refusal that "+
			"does not say why sends a reader looking for a bug in the engine", f.Name)
	}
	s.SMC = f.SMC
	switch f.QGA {
	case "":
		s.QGA = "virtio-serial"
	case "virtio-serial", "isa-serial":
		s.QGA = f.QGA
	default:
		return s, fmt.Errorf("shape %q: `qga: %q` is not a transport this build knows; one of "+
			"\"virtio-serial\" or \"isa-serial\"", f.Name, f.QGA)
	}
	return s, nil
}

// Registry is every shape this build can start, by name.
type Registry struct {
	mu     sync.RWMutex
	shapes map[string]Shape
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{shapes: map[string]Shape{}} }

// Register adds a shape, refusing a duplicate name.
//
// A DUPLICATE IS REFUSED RATHER THAN OVERWRITTEN, because two files claiming `os: macos` is a
// configuration whose behaviour depends on directory order - and a machine that changes shape when a
// file is renamed is not a machine anybody can reason about.
func (r *Registry) Register(s Shape) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.shapes[s.Name]; ok {
		return fmt.Errorf("two shapes are named %q: %q and %q. A name is what a walkthrough's `os:` "+
			"says, so it has to choose exactly one machine", s.Name, old.Description, s.Description)
	}
	r.shapes[s.Name] = s
	return nil
}

// Lookup returns the shape for name.
func (r *Registry) Lookup(name string) (Shape, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.shapes[name]
	return s, ok
}

// Names returns every registered name, sorted, for an error message that tells a reader what they
// could have written instead.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.shapes))
	for n := range r.shapes {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Len is how many shapes this registry holds.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.shapes)
}

// LoadDir reads every `*.yaml` in dir and registers it.
//
// A FILE THAT FAILS IS AN ERROR, NOT A WARNING. A shape file is configuration for a machine that will
// be started; one that does not parse is a guest that will not boot, and finding that out at the
// `boot:` step rather than at load is finding it out forty minutes into a stage.
func (r *Registry) LoadDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("shapes directory %s: %w", dir, err)
	}
	var loaded int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if err := r.LoadFile(path); err != nil {
			return err
		}
		loaded++
	}
	if loaded == 0 {
		return fmt.Errorf("shapes directory %s holds no `*.yaml` files, so this build could start no "+
			"machine at all", dir)
	}
	return nil
}

// LoadBytes reads one shape and registers it, with `name` only for error messages.
//
// SEPARATE FROM LoadFile BECAUSE AN EMBEDDED FILE HAS NO PATH. The bytes are the same bytes; what a
// reader of an error needs is which shape failed, and the file's name answers that either way.
func (r *Registry) LoadBytes(name string, raw []byte) error {
	var f ShapeFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return fmt.Errorf("shape %s: %w", name, err)
	}
	s, err := f.Validate()
	if err != nil {
		return fmt.Errorf("shape %s: %w", name, err)
	}
	if err := r.Register(s); err != nil {
		return fmt.Errorf("shape %s: %w", name, err)
	}
	return nil
}

// LoadFile reads one shape file and registers it.
func (r *Registry) LoadFile(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var f ShapeFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return fmt.Errorf("shape file %s: %w", path, err)
	}
	s, err := f.Validate()
	if err != nil {
		return fmt.Errorf("shape file %s: %w", path, err)
	}
	if err := r.Register(s); err != nil {
		return fmt.Errorf("shape file %s: %w", path, err)
	}
	return nil
}
// FirmwareRecoveryPairs is `media.recovery_args` as the pairs it was written as.
//
// THE STORED FIELD IS FLAT so the argument builder can append it in one line, and this reconstructs
// the pairs for the caller that needs them separately - each pair's `{{recovery}}` is substituted
// before use, so a flat list would have to be substituted with two different values.
func (s Shape) FirmwareRecoveryPairs() [][]string {
	var out [][]string
	for i := 0; i+1 < len(s.RecoveryArgs); i += 2 {
		out = append(out, s.RecoveryArgs[i:i+2])
	}
	return out
}

// highestFirmwareBootIndex is the largest `bootindex=` the shape's `firmware.disks:` declare, or -1.
//
// IT IS PARSED RATHER THAN ASSUMED so a firmware with two boot disks gets 0 and 1 and the guest's
// target gets 2, without the shape having to write the target's number itself.
func (s Shape) highestFirmwareBootIndex() int {
	best := -1
	for _, pair := range s.FirmwareDisks {
		for _, a := range pair {
			i := strings.Index(a, "bootindex=")
			if i < 0 {
				continue
			}
			n, err := strconv.Atoi(a[i+len("bootindex="):])
			if err != nil {
				continue
			}
			if n > best {
				best = n
			}
		}
	}
	return best
}


// sortedKeysQEMU is a stable ordering for an error message.
func sortedKeysQEMU(m map[string]QEMUProfile) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Profile returns the named profile, or the default when the name is empty.
//
// AN UNKNOWN NAME IS AN ERROR rather than a fallback: a walk that says `microvm_display: dekstop`
// should be told so, not handed the terminal profile and left to wonder why its GPU never appeared.
func (s Shape) Profile(name string) (QEMUProfile, error) {
	if len(s.QEMU) == 0 {
		return QEMUProfile{}, fmt.Errorf("shape %q has no `qemu` section", s.Name)
	}
	if name == "" {
		name = s.QEMUDefault
	}
	if name == "" {
		// THE FIRST IN NAME ORDER, so a single-profile shape needs no `default:`.
		return s.QEMU[sortedKeysQEMU(s.QEMU)[0]], nil
	}
	prof, ok := s.QEMU[name]
	if !ok {
		return QEMUProfile{}, fmt.Errorf("shape %q has no `qemu` profile named %q; it has %s",
			s.Name, name, strings.Join(sortedKeysQEMU(s.QEMU), ", "))
	}
	return prof, nil
}
