package daemon

import (
	"fmt"

	"github.com/easydev-org/easyvm/internal/protocol"
)

// xyOf reads the point a `click` or `double` request names, AND REFUSES A MISSING ONE BY NAME.
//
// `{"xx": 100, "yy": 200}` DECODED TO A ZERO X AND Y AND CLICKED (0, 0). The struct had `int` fields,
// so a key that was not there and a coordinate of zero were the same value - and `(0, 0)` is the
// top-left pixel, which a walkthrough may legitimately name. `protocol.Request.X/Y` are `*int` now, so
// "absent" has its own value and this is where it is turned into an error.
//
// IT IS ALSO WHERE AN OUT-OF-FRAME COORDINATE IS REFUSED RATHER THAN CLAMPED, because both are "the
// number this request carries cannot name a pixel" and both should be answered before a guest is
// touched. MEASURED, the two failures this replaces:
//
//	POST /api/click {"xx":100,"yy":200}   -> 200 {"ok":true}   (clicked the corner)
//	POST /api/click {"x":99999,"y":1}     -> 200 {"ok":true}   (clamped to the edge)
//
// THE FRAME IS THE MACHINE'S OWN. A coordinate is a measurement of a screen, so the only frame that can
// say whether it fits is the one the guest is drawing at - `d.frameSize()`.
func xyOf(command string, req protocol.Request) (int, int, error) {
	if req.X == nil || req.Y == nil {
		var missing []string
		if req.X == nil {
			missing = append(missing, `"x"`)
		}
		if req.Y == nil {
			missing = append(missing, `"y"`)
		}
		return 0, 0, fmt.Errorf("%s needs both \"x\" and \"y\": %v did not arrive, so this request "+
			"names no point", command, missing)
	}
	return *req.X, *req.Y, nil
}

// frameSize is the framebuffer the machine is drawing at, or zero when there is none to ask.
//
// A SCREENSHOT IS THE MEASUREMENT, not a constant: the resolution is a property of the adapter the boot
// gave the guest, and a microvm draws 720x400 where a full machine draws 1920x1080.
func (d *Daemon) frameSize() (int, int) {
	d.mu.Lock()
	drv := d.ex
	d.mu.Unlock()
	if drv == nil || drv.Driver == nil {
		return 0, 0
	}
	f, err := drv.Driver.Frame()
	if err != nil {
		return 0, 0
	}
	return f.Size()
}
