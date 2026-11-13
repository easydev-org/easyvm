package daemon

import (
	_ "embed"
	"net/http"
)

// THE PAGE IS SERVED BY THE DAEMON AND IS ONE FILE, WITH NO BUILD STEP.
//
// A SEPARATE WEB PROJECT WOULD BE A SECOND THING TO KEEP IN STEP with the API it calls, and this
// repository has exactly one consumer of that API: this page. It is embedded rather than served from
// a directory so the binary carries it - a daemon deployed to a pod has no source tree beside it.
//
// WHAT IT DOES, IN THE ORDER A PERSON NEEDS IT:
//
//	see     a PNG polled once a second from /screen.png
//	click   a click on the image maps to guest pixels, because the image IS the frame
//	type    a field that sends /api/type, at the pace the walkthroughs use
//	key     the handful of keys a walkthrough actually presses, plus a combo box
//	state   the daemon's own status line, so a failure is visible without a terminal
//
// THE IMAGE IS NOT A VNC CANVAS ON PURPOSE. qemu already serves VNC on 127.0.0.1:5900 and this page
// could proxy it, but a VNC client needs a websocket bridge and a second protocol; a screenshot a
// second plus click mapping is enough to drive a walk, and it is the same QMP path the walkthroughs
// use - so what a person sees here is exactly what `until:` sees.
//
//go:embed web/index.html
var pageHTML []byte

func (d *Daemon) handlePage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(pageHTML)
}
