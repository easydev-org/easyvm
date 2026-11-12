package screen

import (
	"os"
	"testing"

	_ "image/png"
)

// THE REAL FRAME, NOT A SYNTHETIC ONE.
func TestTheActualMXBootMenuFrame(t *testing.T) {
	for _, p := range []string{"testdata/mx_grub.png"} {
		f, err := os.Open(p)
		if err != nil {
			t.Skipf("no %s: %v", p, err)
		}
		img, err := Load(f.Name())
		f.Close()
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		ok, st := IsLinuxDesktop(img)
		t.Logf("%s: desktop=%v  %s", p, ok, st.Explain())
		if ok {
			t.Errorf("the real MX boot menu measured as a desktop: %s", st.Explain())
		}
	}
}
