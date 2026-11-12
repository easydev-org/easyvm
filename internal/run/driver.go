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
	Stop(method string) error
	// Reboot resets and waits for it to come back.
	Reboot() error
	// IsRunning reports whether qemu is still there.
	IsRunning() bool
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
	// OS is windows, linux or macos.
	OS string
	// Disk is the target, and for a disk boot it is also what boots.
	Disk string
	// ISO is an installer medium. Windows and Linux only.
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
	// OpenCoreROM, OpenCoreVars and BootImage are the macOS loader, built from the recovery image.
	OpenCoreROM  string
	OpenCoreVars string
	BootImage    string
	// CPUs and MemMB are the machine's size.
	CPUs  int
	MemMB int
}
