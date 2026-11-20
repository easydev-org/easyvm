package run

import (
	"testing"
	"time"

	"github.com/easydev-org/easyvm/internal/walk"
)

// A `boot:` STEP'S `timeout:` IS THE BUDGET THE WAIT AFTER IT GETS.
//
// MEASURED AS A DEFECT BEFORE IT WAS A FIX: fifty-three `boot:` steps in this tree write
// `timeout == 900` (or 240, or 1800), and `doBoot` READ NONE OF THEM. It starts qemu and returns,
// and the waiting belongs to the next step - `untilConsole` at a hard-coded 180 seconds. So a file
// that said "this guest may take fifteen minutes" was talking to a step that never looked, and the
// wait that mattered was budgeted at three.
//
// THE FAILURE THIS PRODUCES IS NAMED AFTER THE WRONG STEP. The log reads
//
//	status: failed step 4/8 "the console will say ..."
//
// for a guest whose BOOT was slower than a number the file had already declared.
// step is the smallest thing the budget reads: a name and whatever keys the test is about.
func stepOf(spec map[string]any) walk.Step {
	spec["name"] = "probe"
	return walk.Step{Index: 1, Name: "probe", Spec: spec}
}

func TestABootsTimeoutBecomesTheBudgetOfTheWaitAfterIt(t *testing.T) {
	ex := &Executor{}
	if ex.budget(stepOf(map[string]any{"until": "login"}), "timeout", 180) != 180*time.Second {
		t.Fatal("with no boot, the fallback must be the wait's own default")
	}

	// a boot that declared fifteen minutes
	ex.bootBudget = 900 * time.Second
	ex.bootBudgetSet = true
	if got := ex.budget(stepOf(map[string]any{"until": "login"}), "timeout", 180); got != 900*time.Second {
		t.Fatalf("the wait after a 900-second boot got %s; it must inherit the boot's budget", got)
	}

	// AND A STEP THAT NAMES ITS OWN STILL WINS. A per-step budget is more specific than the one a
	// boot declared, so a file that wants a short wait inside a long boot can still say so.
	own := stepOf(map[string]any{"until": "login", "timeout": float64(30)})
	if got := ex.budget(own, "timeout", 180); got != 30*time.Second {
		t.Fatalf("a step with its own timeout: got %s, want 30s - the step must win", got)
	}
}

// AND AN OLD RUN BEHAVES EXACTLY AS IT DID. `bootBudgetSet` separates "a boot said 0" from "there
// was no boot", so a tree that never writes the key is untouched.
func TestABootWithNoTimeoutChangesNothing(t *testing.T) {
	ex := &Executor{}
	ex.bootBudget = 0
	ex.bootBudgetSet = false
	if got := ex.budget(stepOf(map[string]any{"until": "x"}), "timeout", 180); got != 180*time.Second {
		t.Fatalf("got %s, want the unchanged 180s default", got)
	}
}
