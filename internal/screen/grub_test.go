package screen

import (
	"image"
	"image/color"
	"testing"
)

// A BOOT MENU IS NOT A DESKTOP, AND IT WAS BEING MEASURED AS ONE.
//
// MEASURED ON mx-install, WHICH NEVER REACHED ITS INSTALLER AT ALL. Frame 40 of its recording shows
// MX's GRUB menu:
//
//	MX-25.3 Xfce x64 (September 20, 2026) (sysvinit)
//	MX-25.3 Xfce x64 (September 20, 2026) (systemd)
//	Check integrity of the live media
//	Language - Keyboard - Timezone        <- THE CURSOR LANDS HERE BY DEFAULT
//	>>> Advanced Options <<<
//	The highlighted entry will start in 24 seconds.
//
// `desktop: linux` PASSED ON THAT SCREEN - `drop=+61.9 (need 15)`, a dark body with a lighter top
// strip - so the walk believed a desktop was up, pressed coordinates onto a character-cell menu, and
// every later step read `UWage - Keyboard - Timezone`.
//
// THE FIX IS NOT "WAIT LONGER": it is that a boot menu and a panel look alike to three aggregate
// statistics and not alike to any photograph of one. This test pins the distinction with a synthetic
// frame that has the boot menu's shape - a dark body, lighter text rows, no full-width single-colour
// strip - and asserts the desktop test refuses it.
func TestABootMenuIsNotADesktop(t *testing.T) {
	// A BOOT MENU'S SHAPE: one dark field with a few light text rows in the middle, and NO strip of
	// one colour along either edge. That last part is what the panel test actually depends on.
	const w, h = 640, 480
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	body := color.RGBA{R: 42, G: 38, B: 36, A: 255}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, body)
		}
	}
	// text rows, in the middle third, light on dark - the shape of a menu's entries
	light := color.RGBA{R: 230, G: 228, B: 222, A: 255}
	for _, row := range []int{150, 180, 210, 240, 270, 300} {
		for y := row; y < row+10 && y < h; y++ {
			for x := 120; x < w-120; x++ {
				img.SetRGBA(x, y, light)
			}
		}
	}

	ok, st := IsLinuxDesktop(img)
	if ok {
		t.Fatalf("a boot menu measured as a desktop: %s.\nTHE COST OF THIS: mx-install passed its "+
			"desktop gate on GRUB's menu, pressed every coordinate onto a character-cell screen, and "+
			"spent the run reading `UWage - Keyboard - Timezone`", st.Explain())
	}
}
