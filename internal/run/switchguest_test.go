package run

import (
	"strings"
	"testing"
	"time"
)

// ONE WALK, TWO GUESTS: stop the first and start a second on the same daemon.
//
// THE QUESTION THIS ANSWERS: can a walkthrough show one operating system, stop it, and show another -
// so that ONE recording contains both desktops? That is what the xfce-then-openbox e2e test needs, and
// what `macos/26/steps/resume.yaml` relies on in the other direction (a guest that is already there).
//
// THE MACHINE SLOT IS THE THING THAT DECIDES. `doBoot` refuses different qemu arguments while a machine is
// RUNNING - "one daemon holds one machine" - and that check is guarded by `IsRunning()`, so a stopped
// machine is not a machine. Whether the second boot is allowed is therefore a question about whether
// `Stop` leaves the driver able to start again.
//
// IT IS TESTED HERE BECAUSE IT WAS NEVER TESTED: `fakeMachine.IsRunning` answered `started > 0` and
// `Stop` did not clear it, so every test that could have asked this question was answered "still
// running" by the fake rather than by the code.
func TestOneWalkCanStopAGuestAndStartAnother(t *testing.T) {
	d := &fakeDriver{}
	ex, steps := newExec(d,
		map[string]any{"name": "boot windows", "boot": true, "machine": "q35",
			"args": []any{[]any{"-drive", "file=win.iso"}}},
		map[string]any{"name": "stop it", "stop": "kill", "timeout": float64(60)},
		map[string]any{"name": "boot linux", "boot": true, "machine": "q35",
			"args": []any{[]any{"-drive", "file=linux.iso"}}},
	)
	ex.Work = t.TempDir()
	if _, err := ex.Run(steps); err != nil {
		t.Fatalf("a walk that stops one guest and starts another: %v", err)
	}

	if d.started != 2 {
		t.Fatalf("%d boot(s) happened and the walk asked for two. A daemon that refuses the second "+
			"cannot record a walkthrough of two operating systems", d.started)
	}
	if len(d.stopped) != 1 || d.stopped[0] != "kill" {
		t.Fatalf("stops = %v, want one kill", d.stopped)
	}
	if d.boot == nil || len(d.boot.Args) == 0 ||
		!strings.Contains(strings.Join(d.boot.Args, " "), "linux.iso") {
		t.Fatalf("the second boot asked for %+v, and the walk named linux.iso", d.boot)
	}
}

// TestASecondBootOfTheSameRunningGuestIsANoOp is the other half: a `boot:` against a machine that is
// ALREADY UP does not start a second one. Measured reasoning is in `doBoot` - a sandbox boots once and
// every later stage says "this machine must be running", which it is.
func TestASecondBootOfTheSameRunningGuestIsANoOp(t *testing.T) {
	d := &fakeDriver{}
	ex, steps := newExec(d,
		map[string]any{"name": "boot", "boot": true, "machine": "q35",
			"args": []any{[]any{"-device", "isa-vga"}}},
		map[string]any{"name": "boot again", "boot": true, "machine": "q35",
			"args": []any{[]any{"-device", "isa-vga"}}},
	)
	ex.Work = t.TempDir()
	if _, err := ex.Run(steps); err != nil {
		t.Fatal(err)
	}
	if d.started != 1 {
		t.Fatalf("%d boots for a machine that was already up; the second must be a no-op", d.started)
	}
}

// TestADifferentGuestOnARunningMachineIsRefused is the guard: the second guest must not land on the
// first one's screen, because every click after it would be aimed at a pane that is not there.
func TestADifferentGuestOnARunningMachineIsRefused(t *testing.T) {
	d := &fakeDriver{}
	ex, steps := newExec(d,
		map[string]any{"name": "boot windows", "boot": true, "machine": "q35",
			"args": []any{[]any{"-drive", "file=win.iso"}}},
		map[string]any{"name": "boot linux without stopping", "boot": true, "machine": "q35",
			"args": []any{[]any{"-drive", "file=l.iso"}}},
	)
	ex.Work = t.TempDir()
	_, err := ex.Run(steps)
	if err == nil {
		t.Fatal("a second guest was started on a running machine; every click after it would be " +
			"aimed at a screen that is not the one the file describes")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Fatalf("the refusal does not say a machine is already up: %v", err)
	}
	_ = time.Second
}

// A WALK THAT STOPS A MACHINE MUST BE ABLE TO ASK WHETHER IT WENT DOWN.
//
// MEASURED ON RUN 44461859215, macos 15 ssh, step 29 of 31:
//
//	status: failed step 29/31 "stop the machine"
//	error:   wait for the machine to stop: the guest is no longer running, so this step cannot be
//	         done. The machine exited during the step before it
//
// THE SHUTDOWN HAD WORKED. `shutdown: ssh` sent the request, the guest went down, and the next step -
// `wait_until: stopped` - is the one that confirms it. The guard against running steps on a dead guest
// refused the step whose ENTIRE PURPOSE is to observe that death.
//
// `stepNeedsAGuest` answers "does this step need a machine to be there". `wait_until:` asks ABOUT the
// machine, so the answer is no - the same reasoning that put `boot:` in the list for the opposite
// direction.
func TestAWalkCanWaitForTheMachineItJustStopped(t *testing.T) {
	d := &fakeDriver{}
	ex, steps := newExec(d,
		map[string]any{"name": "boot", "boot": true, "machine": "q35",
			"args": []any{[]any{"-device", "isa-vga"}}},
		map[string]any{"name": "stop it", "stop": "kill", "timeout": float64(60)},
		map[string]any{"name": "wait for the machine to stop", "wait_until": "stopped",
			"timeout": float64(5), "poll": float64(1)},
	)
	ex.Work = t.TempDir()
	if _, err := ex.Run(steps); err != nil {
		t.Fatalf("a walk that stops its machine and then waits for it: %v", err)
	}
	if len(d.stopped) != 1 || d.stopped[0] != "kill" {
		t.Fatalf("stops = %v, want one kill", d.stopped)
	}
}
