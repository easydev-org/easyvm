package machine

import "testing"

// THE POINTER KIND IS READ FROM THE ARGUMENTS, because the QMP socket has one client (this daemon)
// and `query-input` is therefore not available.
func TestPointerIsAbsoluteReadsTheArguments(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"usb-tablet on an xhci bus", []string{
			"-device", "qemu-xhci,id=xhci", "-device", "usb-tablet,bus=xhci.0"}, true},
		{"virtio tablet", []string{"-device", "virtio-tablet-pci"}, true},
		// MEASURED ON ToaruOS AND HURD: no tablet, so an absolute event reaches nothing.
		{"a PS/2 mouse only, which q35 provides", []string{
			"-machine", "q35,accel=kvm", "-device", "VGA,xres=1024,yres=768"}, false},
		{"a usb mouse is RELATIVE too", []string{"-device", "usb-mouse,bus=xhci.0"}, false},
		{"no devices at all", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := PointerIsAbsolute(c.args); got != c.want {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}
