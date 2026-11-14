package run

import (
	"errors"
	"os/exec"
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

// A DIFF IS MEASURED BY WHAT IT FLATTENS TO, NOT BY ITS OWN BLOCKS.
//
// MEASURED ON A DISK PULLED FROM ghcr AND PUT THROUGH THE ssh WALKTHROUGH LOCALLY:
//
//	=== publish the disk
//	error: the disk is 359 MiB, below the 512 MiB floor for an installed system
//
// on a disk holding a complete Ubuntu system. The stage runs on an overlay, so `Disk()` returns the
// DIFF - and the diff of "boot, install openssh, shut down" is 359 MiB while the disk it sits on is
// 10.1 GiB:
//
//	qemu-img measure -O qcow2 run-overlay.qcow2
//	required size:        10620305408      <- 10.1 GiB, what the flatten produces
//	fully allocated size: 137460187136     <- 128 GiB, the virtual size
//
// The floor exists to catch an install that never happened; measuring the diff catches "this stage
// changed little", which is what a later stage is SUPPOSED to do.
//
// THE TEST DOES NOT NEED A BIG DIFF. What it checks is WHICH MEASUREMENT IS USED: a file with a
// backing chain must be measured by `qemu-img measure`, and one without must use `stat`.
func TestADiffIsMeasuredByItsFlattenedSize(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.qcow2")
	diff := filepath.Join(dir, "diff.qcow2")
	plain := filepath.Join(dir, "plain.qcow2")

	run(t, "qemu-img", "create", "-f", "qcow2", base, "1G")
	run(t, "qemu-img", "create", "-f", "qcow2", "-b", base, "-F", "qcow2", diff)
	run(t, "qemu-img", "create", "-f", "qcow2", plain, "1G")

	// A DIFF IS MEASURED BY WHAT IT WOULD FLATTEN TO.
	measured, how := installedBytes(diff)
	if !contains(how, "flattened") {
		t.Fatalf("a file with a backing chain was measured by %q; it must use the flattened size, "+
			"or a stage that installs 300 MiB of packages is refused as an empty disk", how)
	}
	// AND THE NUMBER IS A PROPERTY OF THE WHOLE IMAGE, NOT OF THE DIFF.
	//
	// IT IS NOT ASSERTED TO BE BIG: a 1 GiB base containing nothing compresses to a few hundred
	// kilobytes, and that is the honest answer - which is why this test checks the METHOD rather than
	// a threshold. The 10.1 GiB figure in the note above came from a disk with a system on it.
	if measured <= 0 {
		t.Fatalf("the diff measured %d bytes by %q", measured, how)
	}
	if diffOnly, _ := installedBytesPlain(diff); measured == diffOnly {
		t.Logf("the flattened size (%d) happens to equal the diff's own blocks (%d); the method is "+
			"what this test is about", measured, diffOnly)
	}

	// A PLAIN FILE STILL USES `stat`: cheaper, and it needs no qemu-img.
	if _, plainHow := installedBytes(plain); !contains(plainHow, "blocks") {
		t.Fatalf("a file with no backing chain was measured by %q, and it should use its own blocks",
			plainHow)
	}
}

// installedBytesPlain is the `stat` measurement, so the test can say which one it compared.
func installedBytesPlain(path string) (int64, string) { return allocatedBytes(path), "blocks" }

// run fails the test if a command fails.
func run(t *testing.T, name string, args ...string) {
	t.Helper()
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v (%s)", name, args, err, out)
	}
}
