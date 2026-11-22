package walk

import "testing"

// A STEP WHOSE NAME CONTAINS " at " MUST STILL EMIT A LINE THAT PARSES.
//
// MEASURED on `linux/fedora-workstation/steps/ssh.walk`: the step is named
// `point the map at Los Angeles, when the wizard is showing one`, and it clicks (640, 690). The
// emitter's click phrase put the label before the coordinates, the parser split at the FIRST " at ",
// and reading the emitter's own output failed with `click: "Los" is not an x coordinate`. The fix is
// to fall through to the exact `step` form for such a name, which no reader can misread.
func TestAClickNamedWithAtStillRoundTrips(t *testing.T) {
	src := `walkthrough "labels"
  resolution 1920x1080
  recording_resolution 1920x1080

  step "point the map at Los Angeles, when the wizard is showing one"
    click == [640, 690]
    settle == 3
`
	doc, err := WalkToYAML(src)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	out, err := YAMLToWalk(doc)
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	back, err := WalkToYAML(out)
	if err != nil {
		t.Fatalf("the emitted .walk does not parse: %v\n--- emitted ---\n%s", err, out)
	}
	steps, _ := back["steps"].([]any)
	if len(steps) != 1 {
		t.Fatalf("got %d steps back, want 1", len(steps))
	}
	s, _ := steps[0].(map[string]any)
	pt, ok := s["click"].([]any)
	if !ok || len(pt) != 2 {
		t.Fatalf("the click did not survive the round trip: %v", s["click"])
	}
	if pt[0] != 640 || pt[1] != 690 {
		t.Errorf("the coordinates came back as %v, want [640 690]", pt)
	}
}
