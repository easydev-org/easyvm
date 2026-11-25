package machine

// THE MACHINE A BOOT STEP DESCRIBES, AND NOTHING ELSE.
//
// THIS FILE USED TO BE A THOUSAND LINES OF MACHINE KNOWLEDGE: a shape registry, six firmware kinds, an
// adapter table, an audio table, a kernel-boot path, a media policy and the argument builders that
// turned all of it into qemu flags. Every one of those answered "what does this machine look like",
// which is a fact about a guest - and the file that knows what guest it wants can say so.
//
// WHAT IS LEFT IS WHAT THE HOST OWNS:
//
//	the paths the run was given          `Disk`, `ISO`, `Recovery`, `AnswerCD`, `DriverCD`, `Work`
//	the arguments the step wrote          `Machine`, `Args`
//	whether the disk is this run's own    `FreshDisk`
//
// Everything else a boot does is one of those paths substituted into one of those arguments.

import (
	"fmt"
	"os"
	"strings"
)

// OS is the name a walkthrough's `os:` used to say.
//
// IT IS NOT READ ANY MORE and is kept only for the messages that name what a machine was: a boot names
// qemu arguments now, and no code branches on this. `ParseOS` refuses anything but a known name so a
// stale file is told, rather than silently starting whatever the arguments describe.
type OS string

const (
	// Windows, Linux, MacOS, Android and BSD are the names a stage may still write.
	Windows OS = "windows"
	Linux   OS = "linux"
	MacOS   OS = "macos"
	Android OS = "android"
	BSD     OS = "bsd"
	// MicroVM is the smallest machine - no firmware, no PCIe - and it is a name here only because a
	// walkthrough's `os:` may still say it.
	MicroVM OS = "microvm"
)

// ParseOS refuses anything this build has no name for.
//
// IT IS A LIST RATHER THAN A REGISTRY because the registry is gone: what a name MEANS is the
// arguments a boot writes, and this only answers "is this a guest this build has ever heard of".
func ParseOS(s string) (OS, error) {
	switch OS(s) {
	case Windows, Linux, MacOS, Android, BSD, MicroVM:
		return OS(s), nil
	}
	return "", fmt.Errorf("%q is not a guest this build has seen; it has %s",
		s, strings.Join([]string{"windows", "linux", "macos", "bsd", "android", "microvm"}, ", "))
}

// Boot is what one `boot:` step says.
type Boot struct {
	// OS is which machine this is, for the messages that name one. NOTHING BRANCHES ON IT.
	OS OS
	// Disk is the target, and for a disk boot it is also what boots.
	Disk string
	// ISO is an installer medium, if the run was told about one. The argument that attaches it
	// names its path literally.
	ISO string
	// Recovery is macOS's recovery image, if the run was told about one.
	Recovery string
	// AnswerCD and DriverCD are Windows Setup's two extra media.
	AnswerCD string
	DriverCD string
	// Machine is the `-machine` value, verbatim.
	Machine string
	// Args are the qemu arguments the step wrote, in order, as `-flag value` pairs.
	Args []string
	// Dir is the step's `dir:`.
	Dir string
	// Work is the run's working directory.
	Work string
	// Incoming is a migration stream to restore instead of booting.
	Incoming string
	// Kernel and Initrd are the guest's kernel, for a machine the host boots directly.
	Kernel string
	Initrd string
	// KernelAppend is the guest's own kernel command line.
	KernelAppend string
	// FreshDisk says this run CREATED the target and nothing behind it needs protecting.
	//
	// IT IS SEPARATE FROM WHAT IS ATTACHED, AND CONFLATING THEM WAS TWO DEFECTS. A `boot:` step that
	// creates its own disk writes to the disk itself; a step that opens a disk from somewhere - a
	// published artifact, another stage's output - must NOT lose its writes.
	FreshDisk bool
	// NoOverlay says the guest's writes go STRAIGHT TO THE DISK, with no throwaway diff in front.
	//
	// A FILE ASKS FOR IT WITH `overlay: false` ON ITS `boot:`, and it is for the stage whose diff
	// does not fit beside the disk it starts from - see WantsOverlay for the measurement. It trades
	// the diff's two guarantees for that room: a failed run leaves its writes in the disk, and a TRIM
	// from the guest reaches the published artifact.
	NoOverlay bool
}

// Validate refuses a boot that cannot work, before qemu is started.
func (b Boot) Validate() error {
	if b.Machine == "" {
		return fmt.Errorf("booting needs `machine:` - the qemu `-machine` value, as in " +
			"`machine: \"q35,accel=kvm,vmport=off\"`")
	}
	if len(b.Args) == 0 {
		return fmt.Errorf("booting needs `args:` - the qemu arguments that describe this machine. " +
			"A boot with no arguments asks qemu for a machine with no disk and no device")
	}
	if len(b.Args)%2 != 0 {
		return fmt.Errorf("`args:` has %d element(s). Every qemu flag takes a value, so the list is "+
			"pairs: an odd count is a flag whose argument went missing", len(b.Args))
	}
	if b.Disk == "" {
		return fmt.Errorf("booting needs a target disk: a `disk:` step must come first, or pass " +
			"--boot-disk")
	}
	// EVERY PATH THE ARGUMENTS NAME IS CHECKED HERE, not by qemu: qemu's message for a missing
	// `-drive` names the path and not the step that forgot to write it, so a stage whose leg pulled
	// nothing looks like a machine that will not start.
	for _, p := range []struct{ path, why string }{
		{b.Disk, "the target disk"},
		{b.ISO, "the installer medium"},
		{b.Recovery, "the recovery image"},
		{b.AnswerCD, "the answer CD"},
		{b.DriverCD, "the driver CD"},
		{b.Kernel, "the kernel"},
		{b.Initrd, "the initramfs"},
		{b.Incoming, "the snapshot"},
	} {
		if p.path == "" {
			continue
		}
		if err := requireFile(p.path, p.why); err != nil {
			return err
		}
	}
	return nil
}

// requireFile is a path that must exist and must not be a directory.
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

// installing reports whether a medium is writing to the disk, which is when discard must be off.
//
// IT READS WHAT THE ARGUMENTS ATTACH, not a file size and not a guest's name. A size heuristic ("a big
// qcow2 already has a system") is wrong in both directions: a fresh 128G qcow2 is 196 KiB and an
// installed one is 30 GiB, but an overlay over an installed disk is a few hundred MiB and still has a
// system.
//
// MEASURED, AND THE TWO HALVES ARE OPPOSITE:
//
//	macOS install with discard    42009463444 wrote 36774 MiB, dropped to 19761, froze
//	macOS install without         41835253323 finished in 46 minutes
//	a finished disk being booted  discard is what keeps the published qcow2 small
func (b Boot) installing() bool {
	// A MEDIUM IS WHAT MAKES AN INSTALL, and the argument list is where the media are. An installer
	// is a `media=cdrom` drive or a `readonly=on` disk - the two spellings this build's own files use
	// - and neither is present on a boot that starts an installed system.
	for i := 0; i+1 < len(b.Args); i++ {
		a := b.Args[i+1]
		if !strings.Contains(a, "media=cdrom") && !strings.Contains(a, "readonly=on") {
			continue
		}
		// THE RECOVERY IMAGE IS A DEVICE TOO AND IT IS NOT AN INSTALLER WHEN THE DISK IS OURS - but a
		// `readonly=on` disk that is not the target, with a cdrom beside it or alone, is what every
		// install in these files attaches. The target is excluded by name.
		if strings.Contains(a, "file="+b.Disk+",") || strings.Contains(a, "file="+b.Disk+" ") {
			continue
		}
		return true
	}
	return false
}

// WantsOverlay reports whether the disk needs a throwaway diff in front of it.
//
// A DISK THIS RUN CREATED NEEDS NONE: there is nothing behind it to protect, and a diff would be one
// more file to flatten. Every other boot reads through a read-only base, which is what makes it safe
// for the guest to TRIM.
//
// A FILE MAY ALSO ASK FOR THE WRITES TO GO STRAIGHT TO THE DISK with `overlay: false` on its `boot:`.
// THAT IS FOR THE STAGES WHOSE DIFF DOES NOT FIT BESIDE THE DISK THEY START FROM, and the arithmetic
// is the same for all of them: `publish` reads the base AND the diff and writes a THIRD file, so the
// peak is the sum of three. MEASURED on macos 15 ssh (run 45611579655):
//
//	disk.qcow2          27.95 GiB   the pulled system, read-only
//	run-overlay.qcow2   26.29 GiB   what the guest wrote
//	qemu-img convert    42.2 GiB    where the compressed flatten ran out of room
//	                    ---------
//	                    ~96 GiB     on an 84 GiB runner
//
// and the guest's own `df` in that same run said the whole filesystem held 22 GiB. THE 26 GiB DIFF
// WAS ALMOST ENTIRELY BLOCKS THE GUEST HAD ALREADY FREED: APFS does not hand deletes back to
// `discard=unmap` while it runs, so nothing could shrink it and the third copy had nowhere to go.
//
// With no diff there is no second copy, and the peak is the disk plus its output.
//
// IT IS NOT THE DEFAULT, AND IT IS NOT FREE. A diff is what lets a failed run leave the published
// disk untouched, and what makes `discard=unmap` safe - a TRIM from a guest writing straight to a
// published artifact frees blocks the next stage expects to read. So a file that says
// `overlay: false` is saying "this stage's writes ARE the product, and nothing after it needs the
// bytes it frees".
func (b Boot) WantsOverlay() bool {
	return !b.FreshDisk && !b.NoOverlay
}

// Discardable reports whether a TRIM may reach the disk.
//
// A FRESH, NON-INSTALLING DISK MAY BE TRIMMED, and that combination is why this is not the negation of
// `WantsOverlay`: a disk with no diff whose writes go straight to the file is exactly when freed
// blocks need to leave it. MEASURED on an installed macOS 26 disk: `df -h /` said 12 GiB used and 108
// GiB available while the host saw 38.4 GiB allocated, and 26 GiB of those were blocks the guest had
// already freed and `qemu-img convert -c` cannot remove.
func (b Boot) Discardable() bool { return !b.installing() }

// PointerIsAbsolute reports whether the arguments attach a tablet, which is the only kind of pointer
// qemu lets this engine place at a COORDINATE.
//
// A PS/2 MOUSE TAKES DISPLACEMENTS, NOT POSITIONS. `input-send-event` carries two families of move:
//
//	InputEventAbs   a position, 0..32767 per axis - what usb-tablet and virtio-tablet report
//	InputEventRel   a displacement - all a PS/2 or usb-mouse pointer can be told
//
// MEASURED on ToaruOS 2.3.2 and Debian GNU/Hurd, both q35: neither guest has a driver for a tablet,
// so every `click:` was an absolute move a guest could not read plus a press wherever the pointer
// already was. The wizard rectangle was byte-identical before and after `click 1219 752`.
//
// THE FACT IS IN THE ARGUMENT LIST, WHICH THIS ENGINE WRITES ITSELF. Asking qemu is not possible -
// the QMP socket has exactly one client and it is the daemon - so `query-input` is out. What is in
// hand is the list of `-device` values a boot was started with, and a tablet is one of them.
func PointerIsAbsolute(args []string) bool {
	for _, a := range args {
		if strings.Contains(a, "usb-tablet") || strings.Contains(a, "virtio-tablet") {
			return true
		}
	}
	return false
}

// DiscardFragment is `,discard=unmap` when a trim may reach the disk, and empty otherwise.
func (b Boot) DiscardFragment() string {
	if b.Discardable() {
		return ",discard=unmap"
	}
	return ""
}

// KernelAppendline is the guest's boot line, with the one flag this build must add.
//
// `pci=off` IS PREPENDED WHEN THE MACHINE HAS NO PCIe BUS, because a kernel that finds an ACPI root
// assumes a PCI host bridge exists and stops with `PCI: Fatal: No config space access function found`.
// MEASURED on NixOS, which died at 0.05s without it and boots with it.
//
// A MACHINE WITH A PCIe BUS MUST NOT CARRY IT: that would hide the bus a virtio-gpu is attached to and
// the GPU would vanish. The machine string says which it is.
func (b Boot) KernelAppendline(machineString string) string {
	cmdline := b.KernelAppend
	if cmdline == "" {
		// ALPINE'S OWN `syslinux.cfg` LINE plus `console=ttyS0`: a serial line is the only output a
		// machine with no adapter has, and without it the kernel writes to an adapter that is not
		// there and the console stays EMPTY - which reads as "the guest never booted".
		cmdline = "console=ttyS0 modules=loop,squashfs,sd-mod modloop=/dev/vda"
	}
	if !strings.Contains(machineString, "pcie=off") {
		return cmdline
	}
	if strings.Contains(cmdline, "pci=off") {
		return cmdline
	}
	return "pci=off " + cmdline
}

// ReplaceTargetDisk returns args with every `file=` that names the target replaced by `running`.
//
// IT IS A FUNCTION ABOUT PATHS, NOT ABOUT SPELLING. A walk writes the disk it was handed, and that
// path arrives in three forms: `disk.qcow2` (the work directory's own name), an absolute path from
// `--boot-disk`, and either of those as qemu sees them from `cmd.Dir`. The overlay is the fourth, and
// it is the one the file cannot write - which is why the substitution lives here rather than in the
// file's arguments.
//
// A PAIR IS TOUCHED ONLY WHEN ITS `file=` VALUE NAMES THE TARGET. Media carry their own paths
// (`media=cdrom`, `readonly=on`) and the answer/driver CDs are different files; rewriting those
// would point an installer at a disk instead of its medium.
//
// MEASURED AS A REGRESSION WHEN THE PLACEHOLDER RENDERER WAS DELETED: the overlay was created, closed
// empty (196672 bytes = a bare qcow2 header), and the base grew by 1 MB because qemu was handed it
// directly. Every smoke walk published a disk it had written to. This is the same guarantee, spelled
// as a path comparison instead of a `{{disk}}` template.
func ReplaceTargetDisk(args []string, target, running, work string) []string {
	if target == "" || running == "" || target == running {
		return args
	}
	want := map[string]bool{}
	for _, p := range []string{target, running} {
		want[p] = true
		if rel := RelativeTo(p, work); rel != p {
			want[rel] = true
		}
	}
	// THE TARGET'S OWN SPELLINGS, so a walk that wrote `file=disk.qcow2` and one that wrote the
	// absolute path are the same case.
	tnames := map[string]bool{target: true}
	if rel := RelativeTo(target, work); rel != target {
		tnames[rel] = true
	}
	out := append([]string(nil), args...)
	for i := 0; i+1 < len(out); i++ {
		if !strings.HasPrefix(out[i], "-drive") && !strings.HasPrefix(out[i], "-device") {
			continue
		}
		field := out[i+1]
		j := strings.Index(field, "file=")
		if j < 0 {
			continue
		}
		start := j + len("file=")
		end := strings.IndexByte(field[start:], ',')
		if end < 0 {
			end = len(field) - start
		}
		got := field[start : start+end]
		if !tnames[got] {
			continue
		}
		out[i+1] = field[:start] + RelativeTo(running, work) + field[start+end:]
	}
	return out
}

// RelativeTo spells a path the way qemu sees it, because qemu runs IN the work directory.
//
// A DISK IS THE ONE PATH THAT MAY COME FROM OUTSIDE: `--boot-disk` names a published artifact a leg
// pulled, and CI passes it as an absolute path. Inside the work directory that is `disk.qcow2` to
// qemu; anywhere else it stays absolute, because a file genuinely elsewhere has to be spelled out.
func RelativeTo(path, work string) string {
	if path == "" || work == "" {
		return path
	}
	dir := strings.TrimSuffix(work, "/")
	if strings.HasPrefix(path, dir+"/") {
		return strings.TrimPrefix(path, dir+"/")
	}
	return path
}
