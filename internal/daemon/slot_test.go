package daemon

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/easydev-org/easyvm/internal/guest"
	"github.com/easydev-org/easyvm/internal/run"
)

// THE SLOT IS THE ONE PLACE "MAY A MACHINE START" IS DECIDED, AND IT DECIDES IT ATOMICALLY.
//
// THIS IS THE TEST THE OLD MODEL COULD NOT PASS, because there was no single operation to test: the
// check was `if d.proc != nil` in one place, `if d.machine == nil` in another, and `if d.sandbox` in a
// third, with a gap between each check and the store that answered it.
//
// MEASURED CONSEQUENCE, twice: `create` on a daemon already running a `run`'s machine built a second
// driver, overwrote the closer, and left the first qemu unreachable holding /dev/kvm until the
// container died. And two boots arriving together could both see an idle driver and both start qemu
// against one disk.
//
// SO THE INVARIANT IS: OF N CONCURRENT RESERVATIONS, EXACTLY ONE WINS. The rest get a typed refusal -
// `ErrMachineStarting`, `ErrMachineClosing` or `*ErrMachineRunning` - each of which tells the caller
// something it can act on, rather than a sentence it has to parse.
func TestOnlyOneReservationWins(t *testing.T) {
	d := New()

	const n = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	var refusals []error
	start := make(chan struct{})

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			owner := ownerRun
			if i%2 == 0 {
				owner = ownerCreate
			}
			if _, err := d.reserve(owner); err == nil {
				mu.Lock()
				won++
				mu.Unlock()
				return
			} else {
				mu.Lock()
				refusals = append(refusals, err)
				mu.Unlock()
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if won != 1 {
		t.Fatalf("%d of %d concurrent reservations won, and a daemon holds ONE machine. Two winners "+
			"is two qemu processes on one disk", won, n)
	}
	if len(refusals) != n-1 {
		t.Fatalf("%d refusals for %d losers", len(refusals), n-1)
	}
	// AND EVERY REFUSAL IS A TYPED ONE, because a caller has to tell "retry" from "destroy something".
	for _, err := range refusals {
		var running *ErrMachineRunning
		if !errors.Is(err, ErrMachineStarting) && !errors.Is(err, ErrMachineClosing) &&
			!errors.As(err, &running) {
			t.Fatalf("a loser got %v, which is not ErrMachineStarting, ErrMachineClosing or "+
				"*ErrMachineRunning - so a caller cannot tell 'retry' from 'give up'", err)
		}
	}

	// AND THE WINNER RESERVED RATHER THAN STARTED: the phase says `starting`, so nothing has a driver
	// yet and nothing can act through a machine that does not exist.
	if slot, phase := d.slot(); phase != slotStarting || slot == nil {
		t.Fatalf("after one reservation the phase is %s, wanted starting", phase)
	}
}

// A REFUSAL NAMES THE MACHINE THAT HOLDS THE SLOT.
//
// "a machine is already running" is not actionable: the reader asks WHICH one, and how it got there.
// MEASURED as a leg that failed with `a run is already in progress (step "installation runs")` for
// twenty minutes on a machine that had been killed - the message named a step in a run that no longer
// existed, and said nothing about the qemu still holding /dev/kvm.
func TestARefusalNamesTheHolder(t *testing.T) {
	d := New()
	slot, err := d.reserve(ownerCreate)
	if err != nil {
		t.Fatal(err)
	}
	// PUBLISHED AS RUNNING, which is the state a second `run` or `create` meets.
	if err := d.publish(slot, &fakeOSDriver{os: "macos"}, func() {}); err != nil {
		t.Fatal(err)
	}

	_, err = d.reserve(ownerRun)
	var running *ErrMachineRunning
	if !errors.As(err, &running) {
		t.Fatalf("the second reservation returned %v, wanted *ErrMachineRunning", err)
	}
	if running.OS != "macos" {
		t.Errorf("the refusal says the guest is %q, and the driver reported macos", running.OS)
	}
	if running.Owner != ownerCreate {
		t.Errorf("the refusal says the holder is %v, and a `create` reserved it", running.Owner)
	}
	msg := err.Error()
	for _, want := range []string{"macos", "create", "destroy"} {
		if !contains(msg, want) {
			t.Errorf("the refusal %q does not mention %q; a reader needs the guest, the holder and "+
				"the way out", msg, want)
		}
	}
}

// `absent` IS PUBLISHED ONLY AFTER THE TEARDOWN HAS FINISHED.
//
// THIS IS THE WHOLE POINT OF THE `stopping` PHASE, AND IT IS INVISIBLE IF YOU ONLY TEST THE END STATE.
// The obvious teardown is
//
//	d.machine = nil
//	closeFn()          // and now take your time
//
// which publishes "there is no machine" while a disk is still being closed and an overlay is still
// being verified. A boot arriving in that window starts qemu against a disk the first machine is
// still writing - and the failure surfaces much later, as a corrupt image or as qemu refusing
// `drive.sock.qmp` because the previous client has not let go.
//
// SO THE TEST IS ON WHAT A CONCURRENT CALLER SEES DURING THE CLOSE, not on what is left afterwards.
func TestDestroyIsNotVisibleUntilTheTeardownFinishes(t *testing.T) {
	d := New()
	slot, err := d.reserve(ownerCreate)
	if err != nil {
		t.Fatal(err)
	}

	closing := make(chan struct{})
	release := make(chan struct{})
	if err := d.publish(slot, &fakeOSDriver{os: "linux"}, func() {
		close(closing)
		<-release // the teardown is slow, on purpose
	}); err != nil {
		t.Fatal(err)
	}

	// A DESTROY RUNNING IN THE BACKGROUND, as the real one does.
	done := make(chan Response, 1)
	go func() { done <- d.destroySandbox() }()
	<-closing

	// WHILE IT IS CLOSING, THE SLOT IS STILL TAKEN - and the answer is "retry", not "build one".
	if _, err := d.reserve(ownerRun); !errors.Is(err, ErrMachineClosing) {
		t.Fatalf("during the teardown a reservation returned %v, wanted ErrMachineClosing. A "+
			"`machine == nil` test here would have started a second qemu on a disk the first one "+
			"is still closing", err)
	}
	// AND THE MACHINE IS NOT HANDED OUT TO A STEP either.
	if err := d.withMachine(func(run.Driver) error { return nil }); !errors.Is(err, ErrNoMachine) {
		t.Fatalf("during the teardown `withMachine` returned %v, wanted ErrNoMachine", err)
	}

	close(release)
	select {
	case resp := <-done:
		if !resp.OK {
			t.Fatalf("destroy failed: %s", resp.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("destroy did not finish")
	}

	// AND AFTERWARDS THE SLOT IS FREE, which is what makes the next leg possible.
	if _, phase := d.slot(); phase != slotAbsent {
		t.Fatalf("after a completed destroy the phase is %s, wanted absent", phase)
	}
	if _, err := d.reserve(ownerRun); err != nil {
		t.Fatalf("a reservation after the destroy returned %v, wanted success", err)
	}
}

// A RESERVATION TAKEN AWAY WHILE THE MACHINE WAS BEING BUILT IS NOT INSTALLED.
//
// A `stop` or a `destroy` can arrive between `reserve` and `publish` - the gap in which a driver is
// being constructed and a disk may be created. Without this check the freshly built machine is
// installed into a slot that no longer belongs to it, and nothing will ever close it: it holds
// /dev/kvm and its tap until the container dies.
func TestAMachineBuiltIntoACancelledReservationIsRefused(t *testing.T) {
	d := New()
	slot, err := d.reserve(ownerRun)
	if err != nil {
		t.Fatal(err)
	}
	// A `destroy` of a machine that never published - the operator changed their mind.
	d.release(slot)

	err = d.publish(slot, &fakeOSDriver{os: "windows"}, func() {})
	if !errors.Is(err, ErrSlotLost) {
		t.Fatalf("publishing into a released slot returned %v, wanted ErrSlotLost", err)
	}
	if _, phase := d.slot(); phase != slotAbsent {
		t.Fatalf("the refused publish left the phase at %s", phase)
	}
}

// fakeOSDriver is the smallest driver that can answer "which guest are you", for the refusal messages.
//
// IT IMPLEMENTS ONLY WHAT THE SLOT ASKS FOR, and the slot asks for one thing: the family, so a refusal
// can say "a create sandbox (`macos`)" rather than "something". Everything else panics, so a test that
// accidentally drives a machine fails loudly instead of quietly passing.
type fakeOSDriver struct {
	os string
	// running is what `IsRunning` reports, for the abandoned-run path.
	running bool
}

type fakeOSMachine struct {
	os      string
	running bool
}

func (m fakeOSMachine) Boot(run.BootSpec) error { return nil }
func (m fakeOSMachine) Stop(string) error       { return nil }
func (m fakeOSMachine) Reboot() error           { return nil }
func (m fakeOSMachine) IsRunning() bool         { return m.running }
func (m fakeOSMachine) ReleaseDiff() error      { return nil }
func (m fakeOSMachine) OS() string              { return m.os }

func (d *fakeOSDriver) Frame() (run.Frame, error)                  { return nil, errors.New("fake") }
func (d *fakeOSDriver) Click(int, int) error                       { return errors.New("fake") }
func (d *fakeOSDriver) DoubleClick(int, int) error                 { return errors.New("fake") }
func (d *fakeOSDriver) Key(string) error                           { return errors.New("fake") }
func (d *fakeOSDriver) Stroke([]string, string) error              { return errors.New("fake") }
func (d *fakeOSDriver) Type(string, time.Duration) ([]rune, error) { return nil, errors.New("fake") }
func (d *fakeOSDriver) Guest() *guest.Client                       { return nil }
func (d *fakeOSDriver) Log(string)                                 {}
func (d *fakeOSDriver) Machine() run.Machine {
	return fakeOSMachine{os: d.os, running: d.running}
}
