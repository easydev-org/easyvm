// Package run executes a walkthrough.
//
// A STEP DOES ONE THING. That rule is from the Python implementation and it was earned: a step that
// clicked and typed and then waited could not be re-run from the middle, could not be counted
// honestly, and when it failed the log could not say which of the three had gone wrong.
package run

import (
	"fmt"
	"strings"
	"time"

	"github.com/easydev-org/easyvm/internal/guest"
)

// Driver is everything a step may act through. A step reaches the machine only through this, which
// is what makes a walkthrough testable without a guest.
type Driver interface {
	// Frame captures the screen, for a step that reads it or a failure that needs a picture.
	Frame() (Frame, error)
	// Click presses and releases at a point.
	Click(x, y int) error
	// DoubleClick presses twice with the gap that makes it a double-click.
	DoubleClick(x, y int) error
	// Key sends one qemu qcode.
	Key(qcode string) error
	// Stroke sends keys held together, for shift'd characters and ctrl combos.
	Stroke(modifiers []string, key string) error
	// Type types text at the current focus.
	Type(text string, perChar time.Duration) (unsupported []rune, err error)
	// Guest is the ssh connection, when the step needs one.
	Guest() *guest.Client
	// Machine is the running instance, for a step that starts or stops it.
	Machine() Machine
	// Log writes one line, tagged with the step.
	Log(msg string)
}

// Frame is a captured screen.
type Frame interface {
	// Path is where the PPM is, for a post-mortem.
	Path() string
	// Size is the frame's own dimensions, never a constant.
	Size() (int, int)
	// Words are the text read from it, when a caller asked for text.
	Words() []string
}

// Machine is the part of a running guest a step can change.
type Machine interface {
	// Boot starts the machine this step describes.
	//
	// IT TAKES THE DESCRIPTION RATHER THAN READING ONE. A driver that consulted the step's own
	// fields would need to know which of `iso:`, `recovery:` and `os:` meant what - and that
	// mapping is the thing this call exists to make single-sourced.
	Boot(spec BootSpec) error
	// Stop shuts down by a NAMED method, or reports that it cannot.
	//
	// `grace` IS THE STEP'S OWN `timeout:`, AND IT USED TO BE IGNORED. The daemon waited a hard-coded
	// five minutes for a guest to power off, so a stage whose file said `timeout: 600` still saw
	// "the guest did not power off within 5m0s" - MEASURED on bsd/freebsd/steps/ssh.yaml, whose guest
	// had just written xfce and a display manager and was shutting down correctly, only slowly. The
	// kill that followed left a filesystem that needed a replay, and the next stage's disk had
	// `/usr/local/bin/sudo` missing entirely because of it.
	//
	// A ZERO GRACE MEANS THE DEFAULT, so a caller that does not care keeps the old behaviour.
	Stop(method string, grace time.Duration) error
	// Reboot resets and waits for it to come back.
	Reboot() error
	// IsRunning reports whether qemu is still there.
	IsRunning() bool
	// PressPowerButton asks the guest to shut down through ACPI, the way a physical power button
	// does. It SENDS NOTHING ELSE: the wait is `wait_until: stopped`, so the file can see both halves.
	PressPowerButton() error
	// ResetBoot resets the machine through QMP - the same request the ACPI reset pin makes, without
	// needing a guest-side handler.
	ResetMachine() error
	// GuestAgent runs one `qemu-guest-agent` command and returns its output. The commands are the
	// protocol's own: ping, info, shutdown, exec, file-read, file-write, set-time, interfaces.
	GuestAgent(command string, args map[string]any) (string, error)
	// ReleaseDiff deletes the throwaway diff the machine ran on and proves the base it read
	// through was not written to. It is idempotent: a run that flattened already released it.
	//
	// IT IS SEPARATE FROM Stop BECAUSE THE TWO HAVE DIFFERENT OWNERS. `shutdown:` stops the
	// machine; `compress:` is the step that reads the diff and then releases it. A `shutdown:`
	// that deleted the diff first would leave the next step with nothing to flatten - which is
	// the bug the Python implementation hit on its first real `compress:` run.
	ReleaseDiff() error
}

// Outcome is what a step produced.
//
// A FAILED STEP IS NOT IN `Answered`. That distinction is from the Python implementation and it
// matters: a run that reports "13 of 14 answered" must not have counted the one that clicked and
// then failed its own check, or the transcript disagrees with the verdict.
type Outcome struct {
	// Answered is true when the step ran and its own check, if it had one, passed.
	Answered bool
	// Optional marks a step whose failure does not fail the stage. Only for a check whose
	// NEGATIVE result is information - never for the claim the stage exists to make.
	Optional bool
	// Err is why it failed.
	Err error
}

// StepError is a failure with the sentence a reader needs.
type StepError struct {
	Step string
	// What says what went wrong, in the imperative where possible.
	What string
	// Screen is the text on screen at the time, when there was one.
	Screen string
}

func (e *StepError) Error() string {
	msg := fmt.Sprintf("%s: %s", e.Step, e.What)
	if e.Screen != "" {
		msg += "\n  last screen: " + e.Screen
	}
	return msg
}

// fail builds a StepError.
func fail(step, format string, args ...any) error {
	return &StepError{Step: step, What: fmt.Sprintf(format, args...)}
}

// failWithScreen is the same, with the screen appended - and the screen is what turns "did not
// appear" into a diagnosis.
func failWithScreen(step string, d Driver, format string, args ...any) error {
	e := &StepError{Step: step, What: fmt.Sprintf(format, args...)}
	if f, err := d.Frame(); err == nil {
		if words := f.Words(); len(words) > 0 {
			text := strings.Join(words, " ")
			if len(text) > 200 {
				text = text[:200]
			}
			e.Screen = text
		}
		if p := f.Path(); p != "" {
			e.Screen += fmt.Sprintf("\n  frame: %s", p)
		}
	}
	return e
}

// BootSpec is what a `boot:` step says, in the shape the driver needs.
//
// THE FIELDS ARE PATHS AND NAMES, NOT DECISIONS. Which firmware to load, whether to attach a
// recovery image and whether discard is safe are decided by internal/machine from what is here -
// because those depend on each other, and a caller that decided them separately would get one of
// them wrong.
type BootSpec struct {
	// OS is windows, linux, macos, bsd or android.
	OS string
	// Disk is the target, and for a disk boot it is also what boots.
	Disk string
	// ISO is an installer medium. Windows, Linux and Android; macOS uses Recovery.
	ISO string
	// AnswerCD and DriverCD are the two extra media Windows Setup reads.
	AnswerCD string
	DriverCD string
	// Recovery is macOS's recovery image, attached as removable media.
	Recovery string
	// Incoming is a `snapshot:` file to restore from instead of booting.
	//
	// IT IS A SEPARATE FIELD FROM Disk BECAUSE THE TWO ANSWER DIFFERENT QUESTIONS. Disk says what
	// the guest's storage IS; Incoming says the guest's memory is already decided and qemu must load
	// it rather than run a boot loader. A resume without the disk finds nothing, and a disk boot with
	// an Incoming is a resume - so both must be given together, and Validate enforces it.
	Incoming string
	// FreshDisk says the target was CREATED BY THIS RUN, so nothing behind it needs protecting.
	//
	// SEPARATE FROM `Recovery` AND `ISO` ON PURPOSE - see `machine.Boot.FreshDisk` for the two defects
	// that conflating them produced. A `disk_gb:` step sets this; a `disk:` step that names a file
	// from somewhere does not.
	FreshDisk bool
	// OpenCoreROM, OpenCoreVars and BootImage are the macOS loader, built from the recovery image.
	OpenCoreROM  string
	OpenCoreVars string
	BootImage    string
	// CPUs and MemMB are the machine's size.
	CPUs  int
	MemMB int
	// Kernel and Initrd are the guest's kernel, for a microvm: there is no firmware to read a boot
	// sector, so the host supplies them.
	Kernel string
	Initrd string
	// MicroVM asks for the smallest machine that can boot a modern Linux kernel: NO FIRMWARE, no
	// emulated devices the guest does not need, and a direct kernel hand-off.
	//
	// IT EXISTS BECAUSE MOST OF WHAT THIS REPOSITORY DOES NEEDS A FULL MACHINE AND ALMOST NOTHING
	// ELSE DOES. A UEFI boot, a virtio disk, a VGA adapter and a USB tablet are all required to
	// drive a DISTRIBUTION INSTALLER through its GUI; a walkthrough that only has to prove "the
	// vocabulary drives a guest and the guest answers" pays for every one of them and needs none.
	// MEASURED on the kubuntu install: 4 GiB of RAM, 128 GiB of disk, and forty minutes, to run
	// thirty-eight steps.
	//
	// A MICROVM IS THE SAME ENGINE AND A SMALLER MACHINE, not a second path: the daemon still owns
	// one qemu, the dispatcher still reads the screen, and `see:`/`want:` still decide. Only the
	// qemu arguments differ.
	MicroVM bool
	// MicroVMDisplay is serial, terminal or desktop: whether a microvm gets a VGA adapter at all.
	MicroVMDisplay string
	// KernelAppend is the guest's OWN kernel command line.
	//
	// WITHOUT IT A MICROVM CAN ONLY RUN ALPINE'S DISKLESS NETBOOT. The engine always sent
	//
	//	console=ttyS0 modules=loop,squashfs,sd-mod modloop=/dev/vda
	//
	// MEASURED: a guest whose root is an EXT4 disk on /dev/vda mounted it, then had `modloop=/dev/vda`
	// mounted over it, then dropped to `initramfs emergency recovery shell launched` - and the walk
	// sat in "the console will say login" until its timeout. The default is right for one shape and
	// wrong for every other.
	KernelAppend string
}
