package run

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/easydev-org/easyvm/internal/screen"
	"github.com/easydev-org/easyvm/internal/walk"
)

// `desktop:` CAN BE DECIDED BY A REFERENCE IMAGE, AND THE MEASUREMENT IS TWO ORDERS OF MAGNITUDE
// WIDER THAN THE THRESHOLDS IT REPLACES.
//
// MEASURED on one ubuntu recording, each frame compared with the desktop reference the file already
// ships:
//
//	 30s   similarity 0.0004   the boot splash
//	 90s   similarity 0.0041   still splashing
//	120s   similarity 0.9909   the desktop
//	1600s  similarity 0.9767   the desktop, an hour later
//
// "NOT A DESKTOP" tops out at 0.0043 and "a desktop" bottoms out at 0.9669, so a 0.90 floor separates
// them with 0.87 of headroom. The six family thresholds `desktop:` used before are numbers within a
// few percent of each other, which is why `windows` sat on its own OOBE frame for two hours.
func TestDesktopCanBeDecidedByAReferenceImage(t *testing.T) {
	dir := t.TempDir()

	// A reference that is mostly one colour with a distinct strip: the shape of a desktop.
	ref := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	for y := 0; y < 1080; y++ {
		for x := 0; x < 1920; x++ {
			if y > 1000 {
				ref.Set(x, y, color.RGBA{0x30, 0x30, 0x30, 0xff})
			} else {
				ref.Set(x, y, color.RGBA{0x20, 0x60, 0xa0, 0xff})
			}
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "steps"), 0o755); err != nil {
		t.Fatal(err)
	}
	refPath := filepath.Join(dir, "steps", "desktop.png")
	writePNG(t, refPath, ref)

	// A frame that IS that desktop.
	same := filepath.Join(dir, "same.png")
	writePNG(t, same, ref)

	// A frame that is NOT: one flat colour, which is what a boot splash looks like.
	splash := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	for i := range splash.Pix {
		splash.Pix[i] = 0x10
	}
	splashPath := filepath.Join(dir, "splash.png")
	writePNG(t, splashPath, splash)

	steps := func(ref string) []walk.Step {
		return []walk.Step{{Index: 1, Name: "wait for the desktop", Spec: map[string]any{
			"name": "wait for the desktop", "desktop": "windows",
			"reference": ref, "timeout": float64(2),
		}}}
	}
	_ = steps

	// THE DESKTOP FRAME PASSES.
	d := &oneFrameDriver{path: same}
	ex, _ := newExec(d, map[string]any{"name": "x"})
	ex.Work = dir
	ex.File = &walk.File{Path: filepath.Join(dir, "steps", "install.walk")}
	err := ex.doDesktop(walk.Step{Index: 1, Name: "wait for the desktop", Spec: map[string]any{
		"name": "wait for the desktop", "desktop": "windows",
		"reference": refPath, "timeout": float64(2),
	}})
	for _, l := range d.log {
		t.Logf("log: %s", l)
	}
	if err != nil {
		t.Fatalf("the frame IS the reference and the step failed: %v", err)
	}

	// AND THE SPLASH DOES NOT, within its budget.
	d2 := &oneFrameDriver{path: splashPath}
	ex2, _ := newExec(d2, map[string]any{"name": "x"})
	ex2.Work = dir
	ex2.File = &walk.File{Path: filepath.Join(dir, "steps", "install.walk")}
	err = ex2.doDesktop(walk.Step{Index: 1, Name: "wait for the desktop", Spec: map[string]any{
		"name": "wait for the desktop", "desktop": "windows",
		"reference": refPath, "timeout": float64(2),
	}})
	if err == nil {
		t.Fatal("a flat splash frame passed a reference comparison against a desktop")
	}
}

// oneFrameDriver returns the same frame every time, so `desktop:`'s poll loop keeps seeing it.
type oneFrameDriver struct {
	fakeDriver
	path string
}

func (d *oneFrameDriver) Frame() (Frame, error) { return stuckFrame{path: d.path}, nil }

type stuckFrame struct{ path string }

func (f stuckFrame) Path() string     { return f.path }
func (f stuckFrame) Size() (int, int) { return 1920, 1080 }
func (f stuckFrame) Words() []string  { return nil }

func writePNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

var _ = screen.Compare
