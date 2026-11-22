package step

import "testing"

// THE COUNTER MUST SEE THE FILES, not report zero because it looked at nothing. MEASURED: with the
// `.yaml` files gone, `countKeyUse` counted `.yaml` only and would have gone green about an empty set.
func TestTheKeyCounterSeesTheWalkFiles(t *testing.T) {
	use := countKeyUse(t)
	for _, k := range []string{"settle", "click", "loop", "check"} {
		if use[k] == 0 {
			t.Errorf("the counter found no `%s` anywhere; it is not reading the `.walk` files", k)
		}
	}
}
