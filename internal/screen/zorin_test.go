package screen

import "testing"

// A REAL DESKTOP STILL PASSES, so the stricter margin is not the test going blind.
//
// This frame is from the lab pod: Zorin's XFCE session after the mode switch to 1920x1080, with its
// panel along the top. It is the frame the mx gate was WRONGLY passing while this one - an actual
// desktop - is what the test exists to accept.
func TestTheActualZorinDesktopFrame(t *testing.T) {
	img, err := Load("testdata/zorin_desktop.png")
	if err != nil {
		t.Skipf("%v", err)
	}
	ok, st := IsLinuxDesktop(img)
	t.Logf("desktop=%v  %s", ok, st.Explain())
	if !ok {
		t.Fatalf("a real XFCE desktop was refused: %s", st.Explain())
	}
}
