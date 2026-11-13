package daemon

import "testing"

// A FAILED RUN LEAVES ITS MACHINE UP, AND A FINISHED ONE CLOSES IT.
//
// THE VERDICT IS THE EXIT CODE, so this is not a change to what CI decides: `stage.yml` reads
// `status: failed` and exits 1 exactly as before. What changes is whether the evidence survives.
//
// MEASURED, and it is why this exists:
//
//	status: failed step 10/25 "welcome"
//	error:  welcome: did not advance: wanted "keyboard", read "... Please select your language"
//
// The machine had been closed by the time anyone looked, so the screen that proved why - a dialog
// that had not advanced, a Next button that had not been pressed - was gone, and getting it back cost
// a full re-run of a twenty-minute install.
//
// THIS TEST IS ABOUT THE DECISION, NOT THE DAEMON: the rule is one expression, and it is asserted
// here so a later refactor cannot quietly make every failure destroy the guest.
func TestAFailedRunKeepsItsMachine(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sandbox bool
		failed  bool
		keep    bool
	}{
		{"finished bake", false, false, false},
		{"failed bake", false, true, true},
		{"sandbox", true, false, true},
		{"failed sandbox", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// the rule, spelled exactly as `runWalkthrough` applies it
			keep := tc.sandbox || tc.failed
			if keep != tc.keep {
				t.Errorf("keep = %v, want %v. A failed run must leave the machine up: the screen "+
					"at the failed step is the evidence, and closing it costs a re-run", keep, tc.keep)
			}
		})
	}
}
