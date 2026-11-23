package run

// AN `until` THAT IS FALSE MUST NOT END THE LOOP, AND IT DID.
//
// `expectOnce` wraps a loop's condition as a private `check:`, and a plain `check:` binds a boolean
// WITHOUT failing when it is false - which is right for a step an `if:` reads. The loop then read
// "no error" as "the condition held", so EVERY `repeat until` was satisfied on its first attempt.
//
// MEASURED on freebsd install, run 45074859661:
//
//	wait for the partitioning menu [until]: __until__ = false
//	    (none of "guided" or "manual" in "FreeBSD Installer -ase-dbqigbase ...")
//	repeat: satisfied after 1 attempt(s)
//
// The walk pressed six keys into a menu that was not up, bsdinstall took the ZFS path instead of
// automatic UFS, and the published disk was 0 MiB - refused by the 512 MiB floor three hours later.
//
// THE TEST USES A FRAME THAT CANNOT PASS. `stubFrame` is a black PNG, so `mean_between [1, 255]` is
// false on every attempt. A loop that ends on the first attempt is the bug; one that runs to its
// ceiling and reports the failure is correct.

import (
	"strings"
	"testing"

	"github.com/easydev-org/easyvm/internal/walk"
)

func TestAnUntilThatIsFalseKeepsLooping(t *testing.T) {
	d := &fakeDriver{}
	ex, _ := newExec(d)
	ex.shots = map[string]string{"screen": stubFrame{}.Path()}

	cond := map[string]any{
		"source":   "screen",
		"mode":     "mean_between",
		"range":    []any{1, 255},
		"rect":     []any{0, 0, 100, 100},
		"rect_at":  []any{1920, 1080},
		"name":     "__until__",
	}
	// The frame is black, so the condition is FALSE every time. Three attempts, no pause.
	err := ex.expectOnce(walk.Step{Name: "x"}, cond)
	if err == nil {
		t.Fatal("expectOnce returned nil for a condition that is false. A loop reading that as " +
			"\"held\" stops on its first attempt and then acts on a screen that was never up")
	}
	if !strings.Contains(err.Error(), "must be true") {
		t.Errorf("the error does not say the condition was false: %v", err)
	}
}

// AND A CONDITION THAT HOLDS MUST NOT REPORT A FAILURE, or every loop would run to its ceiling.
func TestAnUntilThatHoldsReturnsNoError(t *testing.T) {
	d := &fakeDriver{}
	ex, _ := newExec(d)
	ex.shots = map[string]string{"screen": stubFrame{}.Path()}

	// A black frame's mean is 0, so this range covers it and the condition is TRUE.
	cond := map[string]any{
		"source":  "screen",
		"mode":    "mean_between",
		"range":   []any{0, 255},
		"rect":    []any{0, 0, 100, 100},
		"rect_at": []any{1920, 1080},
	}
	if err := ex.expectOnce(walk.Step{Name: "x"}, cond); err != nil {
		t.Fatalf("a condition that holds reported an error: %v", err)
	}
}

// A MACHINE FACT THAT IS TRUE MUST END THE LOOP, EVEN THOUGH IT ALSO CARRIES A REASON STRING.
//
// `machineFact` returns `(ok, why, err)` and `why` is the sentence a FALSE answer fails with. The
// loop turned `why` into an error whether or not `ok` was true, so:
//
//	repeat until machine_ssh
//	  machine_ssh: ssh_ready="true" present=true     (sixty times)
//	  the repeat ran 60 attempt(s) and the condition never held
//
// MEASURED on freebsd ssh, booted from this image and this disk in the cluster's own lab pod, while
// `/api/state` reported `ssh_ready: true` and a direct `guest.Probe` answered in 70ms. Every
// `repeat until machine_stopped` and `machine_ssh` in every repository was affected.
func TestAMachineFactThatHoldsEndsTheLoop(t *testing.T) {
	d := &fakeDriver{facts: map[string]string{"ssh_ready": "true"}}
	ex, steps := newExec(d, map[string]any{
		"name": "wait for sshd",
		"repeat": map[string]any{
			"until": "machine_ssh",
			"max":   3,
			"pause": 0,
		},
	})
	res, err := ex.Run(steps)
	if err != nil {
		t.Fatalf("the loop failed on a fact that holds: %v", err)
	}
	if res.Err != nil {
		t.Fatalf("the loop failed on a fact that holds: %v", res.Err)
	}
}

// AND A FACT THAT IS FALSE STILL FAILS, WITH ITS REASON - the fix must not turn the loop into one
// that always succeeds.
func TestAMachineFactThatIsFalseFailsWithItsReason(t *testing.T) {
	d := &fakeDriver{facts: map[string]string{"ssh_ready": "false"}}
	ex, steps := newExec(d, map[string]any{
		"name": "wait for sshd",
		"repeat": map[string]any{
			"until": "machine_ssh",
			"max":   2,
			"pause": 0,
		},
	})
	res, err := ex.Run(steps)
	if err == nil {
		err = res.Err
	}
	if err == nil {
		t.Fatal("the loop succeeded on a fact that is false")
	}
	if !strings.Contains(err.Error(), "does not accept") {
		t.Errorf("the failure does not carry the fact's own reason: %v", err)
	}
}
