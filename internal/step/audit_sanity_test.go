package step

import "testing"

// THE COUNTER MUST SEE THE FILES, not report zero because it looked at nothing. MEASURED: with the
// `.yaml` files gone, `countKeyUse` counted `.yaml` only and would have gone green about an empty set.
func TestTheKeyCounterSeesTheWalkFiles(t *testing.T) {
	use := countKeyUse(t)
	// THE 2.0 WORDS, which is what the tree now holds: `repeat` replaced `loop`, and `expect`
	// replaced the top-level `check`. A counter that found none of these is reading nothing.
	for _, k := range []string{"settle", "press", "repeat", "from", "rect"} {
		if use[k] == 0 {
			t.Errorf("the counter found no `%s` anywhere; it is not reading the `.walk` files", k)
		}
	}
}
