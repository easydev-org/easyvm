package qmp

import "testing"

// A TABLET IS TOLD A COORDINATE AND A PS/2 MOUSE IS WALKED TO ONE, AND SENDING THE WRONG KIND IS A
// CLICK THAT LANDS NOWHERE.
//
// MEASURED ON TWO GUESTS THIS ENGINE COULD NOT CLICK:
//
//	ToaruOS 2.3.2, q35, no tablet driver   `click 1219 752` left the wizard's rectangle
//	                                       byte-identical, because an `abs` event reaches
//	                                       usb-tablet/virtio-tablet and nothing else
//	Haiku R1beta5, usb-tablet              `Install Haiku` and every dropdown were dead, because
//	                                       `moveTo` routed every move through the top-left corner
//	                                       and Haiku closes a menu the pointer leaves
//
// These tests hold the two halves apart: an absolute pointer gets two events and no corner, a
// relative one gets a corner and then a displacement.
func TestMoveEventsForAbsolutePointerHaveNoCorner(t *testing.T) {
	c := &Conn{}
	c.SetAbsolutePointer(true)
	if !c.HasAbsolutePointer() {
		t.Fatal("a connection told it has a tablet must report one")
	}
}

func TestMoveEventsForRelativePointer(t *testing.T) {
	c := &Conn{}
	c.SetAbsolutePointer(false)
	if c.HasAbsolutePointer() {
		t.Fatal("a connection told it has no tablet must not report one - every move would then be " +
			"an absolute event the guest cannot read, which is the ToaruOS defect")
	}
}

// A CONNECTION THAT WAS NEVER TOLD KEEPS THE OLD BEHAVIOUR, and that is deliberate: every walk in
// this repository attaches `usb-tablet`, and a guest that has none is the exception the daemon says
// so for. A default of `false` would silently switch all of them to relative events.
func TestUntoldConnectionsBehaveLikeTablets(t *testing.T) {
	c := &Conn{}
	if !c.HasAbsolutePointer() {
		t.Fatal("a connection that was never told which pointer the guest has must default to the " +
			"tablet path, which is what every walk attaches")
	}
}
