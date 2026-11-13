package run

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/easydev-org/easyvm/internal/guest"
)

// A MACHINE THAT HAS GONE IS NOT A DROPPED TICK.
//
// THE SAMPLER COUNTS TICKS IT COULD NOT CAPTURE and prints the count, because a video shorter than the
// run with no explanation was mistaken for a 1 fps encode of a shorter recording. MEASURED: a 20-minute
// macos-setup produced 909 frames, and one tick in four was lost to a failed screendump.
//
// BUT THE COUNT IS ONLY HONEST WHILE THE MACHINE EXISTS. `destroy` closes the machine and the sampler
// may still be running until `finishRecording` joins it - a `record:` before a `boot:`, or a walk that
// failed and left the recording up. Counting those as lost ticks reports a truncated video that is not
// truncated: the run ended, and that is not a defect in the recording.
func TestAGoneMachineIsNotCountedAsADroppedTick(t *testing.T) {
	d := &fakeFrameDriver{running: false}
	if driverHasAMachine(d) {
		t.Fatal("a driver whose machine reports IsRunning() == false was reported as having one, so " +
			"every tick after a `destroy` is counted as a lost frame")
	}
	d.running = true
	if !driverHasAMachine(d) {
		t.Fatal("a driver whose machine is running was reported as having none, so a real recording " +
			"would stop counting its own failures")
	}
	// AND A DRIVER WITH NO MACHINE AT ALL IS "GONE" TOO, rather than a nil dereference.
	//
	// `Machine()` CAN RETURN NIL - a driver that has not booted yet - and the sampler asks this on every
	// failed tick. A missing nil check here would panic inside the recording goroutine, which is the one
	// goroutine nobody is watching.
	if driverHasAMachine(&noMachineDriver{}) {
		t.Fatal("a driver whose Machine() is nil was reported as having a machine")
	}
}

// A RECORDING WITH NO FRAMES REFUSES, AND A RECORDING WITH FRAMES ENCODES.
//
// THE REFUSAL IS THE IMPORTANT HALF: the Python implementation uploaded a 303-byte PNG instead of a
// video because `encode` returned silently when the sampler had captured nothing. A video that does not
// exist is an artifact a person has to open to discover, and the stage had already reported success.
func TestEncodingRefusesWhenNoFrameWasCaptured(t *testing.T) {
	r := &recorder{}
	err := r.encode(filepath.Join(t.TempDir(), "out.mp4"), func(string) {})
	if err == nil {
		t.Fatal("`encode` accepted an empty frame list, so a stage with no recording would upload " +
			"whatever the artifact step found")
	}
	// AND THE SENTENCE NAMES THE REASON, because "encode failed" sends a reader to ffmpeg.
	want := "no frames were captured"
	if !contains(err.Error(), want) {
		t.Fatalf("the refusal is %q, and it does not say %q", err.Error(), want)
	}
}

// contains is a local helper: this package has no string utility and one caller does not justify one.
func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// --- the fakes ------------------------------------------------------------------------

// fakeFrameDriver answers only "is there a machine", which is what the sampler asks.
type fakeFrameDriver struct {
	running bool
}

func (d *fakeFrameDriver) Frame() (Frame, error) { return nil, errors.New("fake") }
func (d *fakeFrameDriver) Click(int, int) error  { return errors.New("fake") }
func (d *fakeFrameDriver) DoubleClick(int, int) error {
	return errors.New("fake")
}
func (d *fakeFrameDriver) Key(string) error              { return errors.New("fake") }
func (d *fakeFrameDriver) Stroke([]string, string) error { return errors.New("fake") }
func (d *fakeFrameDriver) Type(string, time.Duration) ([]rune, error) {
	return nil, errors.New("fake")
}
func (d *fakeFrameDriver) Guest() *guest.Client { return nil }
func (d *fakeFrameDriver) Log(string)           {}
func (d *fakeFrameDriver) Machine() Machine     { return recordingMachine{running: d.running} }

type recordingMachine struct{ running bool }

func (m recordingMachine) Boot(BootSpec) error { return nil }
func (m recordingMachine) Stop(string) error   { return nil }
func (m recordingMachine) Reboot() error       { return nil }
func (m recordingMachine) IsRunning() bool     { return m.running }
func (m recordingMachine) ReleaseDiff() error  { return nil }

// noMachineDriver has no machine at all, which is the state every driver is in before a `boot:`.
type noMachineDriver struct{ fakeFrameDriver }

func (d *noMachineDriver) Machine() Machine { return nil }
