package daemon

import (
	"testing"
)

// THE DRIVER A WALK RUNS THROUGH MUST REPORT FACTS, BECAUSE `wait_until:` ASSERTS IT DOES.
//
// `internal/run`'s `doWaitUntil` does
//
//	if facts, ok := e.Driver.(interface{ Facts() (map[string]string, error) }); !ok {
//		return fail(s.Name, "this driver cannot report the facts a `wait_until:` waits on")
//	}
//
// and `Daemon` HAS `Facts` - the snapshot `/api/state` serves - but `*Driver` never forwarded it.
//
// MEASURED on `windows/11/steps/install.walk` step 19 of 20, the step that waits for the installed
// guest to power off before the disk is published. Nineteen steps had worked; this one could not,
// and the failure named the driver rather than the missing method.
//
// THIS TEST IS THE ASSERTION ITSELF, checked at compile time: a `*Driver` that loses `Facts` stops
// satisfying the interface and this file stops building, which is a better failure than a green run
// that cannot finish.
func TestDriverSatisfiesTheInterfaceWaitUntilAsksFor(t *testing.T) {
	var _ interface {
		Facts() (map[string]string, error)
	} = (*Driver)(nil)
}
