package daemon

import (
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/easydev-org/easyvm/internal/artifact"
	"github.com/easydev-org/easyvm/internal/guest"
	"github.com/easydev-org/easyvm/internal/journal"
	"github.com/easydev-org/easyvm/internal/keys"
	"github.com/easydev-org/easyvm/internal/machine"
	easyvmnet "github.com/easydev-org/easyvm/internal/net"
	"github.com/easydev-org/easyvm/internal/overlay"
	"github.com/easydev-org/easyvm/internal/qmp"
	"github.com/easydev-org/easyvm/internal/run"
	"github.com/easydev-org/easyvm/internal/screen"
	"github.com/easydev-org/easyvm/internal/walk"
)

const (
	// vncBase is qemu's own numbering: display N listens on 5900+N.
	vncBase = 5900
	// vncDisplay IS THE FALLBACK, and the display is chosen per WORK DIRECTORY.
	//
	// IT WAS A CONSTANT ON THE ASSUMPTION "ONE GUEST PER CONTAINER", and that assumption is true of
	// the deployed pod and false of every other place this runs - measured, repeatedly, in the lab:
	// a second daemon on another port answers at startup
	//
	//	qemu-system-x86_64: -vnc 127.0.0.1:0: Failed to find an available port: Address already in
	//	use
	//
	// and because qemu exits, the step that needed it reports `qmp ... connection refused`, which
	// reads as a socket problem rather than a VNC one.
	//
	// SO THE DISPLAY COMES FROM THE WORK DIRECTORY: two daemons with different work directories get
	// different displays, and the deployed pod - which has one - still gets 0.
	vncDisplay = 0
)

// qemuDriver is the one implementation of run.Driver.
//
// # THE SHAPE, AND WHY IT IS THIS SHAPE
//
// EVERY FIELD HERE IS A FACT THAT CANNOT BE DERIVED. That rule is not tidiness - it is what keeps
// the machine's state describable in one place. The version before this one had fourteen fields, and
// SEVEN OF THEM WERE COPIES OF SOMETHING ELSE:
//
//	vncPort       always 5900, or "no machine"          -> a constant plus `proc == nil`
//	netUp         implied by `proc != nil`              -> `Down()` is idempotent, so no flag
//	flattenFrom   "the same file as diff.Path"          -> the comment said so itself
//	diskPath      whatever `SetDisk` was told           -> belongs to the machine, not the driver
//	qmpConn       the registry already holds it         -> one owner, not two
//	qmpReg        A DAEMON-WIDE FACT, NOT A MACHINE'S   -> see below
//
// TWO FACTS WITH TWO HOMES IS WHERE BOTH OF TODAY'S BUGS CAME FROM. A `qmpReg` that `Boot` replaced
// while the recorder's sampler still held the old map gave qemu two QMP clients, and the second one
// waited seven minutes for a greeting qemu never sends. A `qmpConn` that `shutdown` closed while the
// sampler was mid-`Screendump` dereferenced a nil monitor and took the daemon down as PID 1.
//
// SO THIS TYPE NOW HOLDS ONLY WHAT A MACHINE OWNS. The process, its socket path and its connection
// live in ONE object (`qemuProcess`), because they are one fact - see `qemuproc.go`.
// "this process has exactly one QMP client" is a fact about the PROCESS.
//
// IT LAZILY OPENS EACH CHANNEL, and that is not laziness. A walkthrough's first step may be `record:`
// before `boot:`, so the QMP socket does not exist yet; the ssh connection cannot be made until the
// guest answers; and a `run` that opened everything up front would fail on a file whose steps are in
// a different order than the implementation expected.
type Driver struct {
	// daemon is the process that owns this driver. ONLY ONE THING IS ASKED OF IT: the QMP registry,
	// because "one client per socket" is a fact about the process rather than about a machine.
	daemon *Daemon
	file   *walk.File
	work   string
	// guestOS is the family the machine was actually STARTED as, remembered at boot.
	//
	// IT IS NOT READ FROM THE FILE, because a sandbox runs several files against one machine and the
	// file in hand may not be the one that booted it. What is true is what was passed to qemu.
	guestOS string
	// bootArgs is what the running machine was started from, so a second boot can tell "this machine
	// again" from "a different machine", which a daemon cannot hold.
	bootArgs []string
	port     int
	pass     string

	// mu guards every field below it. THE LOCK IS NEVER HELD ACROSS I/O - a dial, a migration, a
	// shutdown request are all network round trips, and holding it across one blocks `status`, the
	// console page and every step for as long as the round trip takes. Three separate deadlocks in
	// this file were that mistake.
	mu sync.Mutex
	// booting is set from the moment a boot is accepted until the machine is published or the boot
	// has failed. IT IS WHAT MAKES "ONE MACHINE" TRUE, because it closes the gap between "is there a
	// machine" and "start one" - a gap in which two boots could both see nil and both start qemu.
	booting bool
	// qemu IS THE PROCESS AND ITS ONLY OWNER. `Alive()` answers "is the machine up", and there is no
	// other answer anywhere in this file - see `qemuproc.go` for the six defects that came from
	// `d.proc != nil` disagreeing with reality.
	qemu *qemuProcess
	// qmp is the ONE connection to this machine, and the registry that owns it lives on the Daemon.
	qmp *qmp.Conn
	// disk is what this machine was started on, or will start on. It IS state: a `disk:` step
	// creates a disk and tells the boot which one, so it is chosen during the run rather than at
	// construction. What it is NOT is a second copy of the diff: `Disk()` answers "the file this
	// stage has been writing to", which is the diff when one exists - ONE answer, computed, rather
	// than a `flattenFrom` field that had to be kept equal to `diff.Path` by hand.
	disk string
	// ssh is the guest session. It is a SESSION rather than a resource: it can go stale while the
	// machine is perfectly healthy (a `reboot:` kills it), which is why it repairs itself at the
	// moment of use rather than being treated as part of the machine's lifetime. See `sshSession`.
	ssh *sshSession
	// sshTarget is the address the session dials, kept so `/api/state` can probe it.
	sshTarget guest.Target
	// diff is the throwaway overlay this run wrote into, when it made one. Its Close is what
	// proves the base was not written to, so it belongs to the driver rather than to a step.
	diff *overlay.Overlay
	// frameMu serialises screen capture. ONE FRAME AT A TIME, because three readers of one screen do
	// not need three screendumps and interleaved writes produce unreadable files.
	frameMu sync.Mutex
	// frames is the one-a-second capture every reader shares. See framecache.go for the measurement
	// that made it: the sampler and a gate fought over one QMP socket and the gate lost 166 of 180
	// attempts in silence.
	frames *frameCache
	// shareDir is the directory a `share_serve:` step published, or "" when none did. The daemon
	// serves it over HTTP at `/shared/` - there is no smbd, no port and no config file any more.
	shareDir string
	logf     func(string)
	// audit writes down every external call this driver makes, when a journal is attached. Nil in a
	// test, and every call site tolerates it.
	audit *audit
	// lib is the run's picture library. A `see:`/`screenshot:` puts its capture here and records the
	// name; a frame the recorder takes does NOT, because a library of two thousand unnamed frames is
	// the recording, not a library.
	lib *artifact.Store
}

// NewDriver builds the driver and returns a closer for the caller's defer.
//
// THE DISK IS GIVEN ONCE, HERE, AND NEVER CHANGED. It used to be a field with a `SetDisk` setter,
// because a `disk:` step could name it - but a machine has exactly one disk and it is chosen before
// qemu starts. A setter meant the answer could change under a running guest, and the field was read
// in three places that each had to remember it might.
// Facts reports the machine's measured state, for a `wait_until:` step.
//
// THIS WAS MISSING AND THAT IS A REAL DEFECT: `doWaitUntil` asserts its driver implements
//
//	interface{ Facts() (map[string]string, error) }
//
// and `Daemon` HAS `Facts` - the snapshot `/api/state` serves - but the DRIVER a walk runs through
// did not forward it. So every `wait_until: stopped` in the tree failed with
//
//	wait for the machine to stop: this driver cannot report the facts a `wait_until:` waits on
//
// MEASURED on windows/11/steps/install.walk step 19 of 20, which is the step that waits for the
// installed guest to power off before the disk is published - the last thing the leg does, after
// nineteen steps that had all worked.
//
// IT IS A FORWARD RATHER THAN A SECOND IMPLEMENTATION, because `Daemon.Facts` is already built from
// the same snapshot `/api/state` reads. Two sources for "is sshd up" is how a step comes to wait on
// one answer while the log shows another.
func (d *Driver) Facts() (map[string]string, error) {
	if d.daemon == nil {
		return nil, fmt.Errorf("this driver has no daemon, so it cannot report the machine's facts")
	}
	return d.daemon.Facts()
}

func NewDriver(owner *Daemon, f *walk.File, work, bootDisk string, port int,
	password string) (run.Driver, func(), error) {
	if err := os.MkdirAll(work, 0o755); err != nil {
		return nil, nil, err
	}
	// WHEN THE CALLER HAS NO PASSWORD, USE THE FILE'S OWN `password:`.
	//
	// THIS IS THE FIX FOR THE FAILURE THAT LOOKED LIKE A BROKEN NETWORK. `password admin1` in a
	// walk is a TEMPLATE VARIABLE (it feeds `{{ password }}`), not a field `walk.File` carries - so a
	// caller that passed no `--password` left the driver with an empty one, and every probe and
	// `ssh:` step answered "unable to authenticate ... no supported methods remain" on a guest whose
	// sshd was active and reachable. MEASURED (run 45915469545 closed it; reproduced in the pod with
	// the engine's own client): the same guest accepted `admin1` and refused `admin`.
	//
	// The resolved variable is exactly what `{{ password }}` becomes, so a file that prints it during
	// its install and a driver that dials with it can no longer disagree. A caller's `--password` still
	// wins, which is what makes an image with a different account usable.
	password = sshPassword(password, f)
	d := &Driver{
		daemon: owner,
		file:   f,
		work:   work,
		port:   port,
		pass:   password,
		disk:   bootDisk,
		logf: func(msg string) {
			fmt.Printf("[easyvm] %s\n", msg)
		},
	}
	// THE SESSION IS BUILT HERE, WITH ITS DIALER, BECAUSE NOTHING ELSE KNOWS THE TARGET.
	//
	// A `dev.yaml` runs against a machine that is already up, so the session must exist before any step
	// asks for it - and it dials lazily, on the first use, so a file whose first step is a `disk:` does
	// not pay for a connection the guest cannot accept yet.
	// THE TARGET IS KEPT, not just captured by the closure, because `/api/state` has to answer
	// "does sshd accept a connection" and a probe needs the address to ask.
	// CONNECT TO THE GUEST'S OWN 22, NOT THE HOST'S DNAT PORT.
	//
	// MEASURED on run 45823139196, whose four host-side diagnosis shots all showed, at the very
	// moment the guest reported `sshd active` and `LISTEN 0.0.0.0:22`:
	//
	//	host diag [nat 2222]:  -A EASYVM_FWD ... --dport 2222 -j DNAT --to-destination 172.30.0.2:22
	//	host diag [conntrack]: (nothing matched)          <- the SYN never becomes a tracked flow
	//	host diag [ip neigh]:  172.30.0.2 ... REACHABLE   <- layer 2 to the guest is fine
	//	host diag [ip route]:  172.30.0.2 dev qemubr0      <- the route is right
	//
	// ARP resolves and the route is correct, so the guest is reachable at layer 2/3 and the ONLY
	// thing between the host and the guest's sshd is the DNAT hop, which on this runner produces no
	// conntrack entry - the packet is dropped before the guest sees it, and the kernel answers
	// `no route to host`. anduinos and pearos happen to pass through that same hop; cachyos, kubuntu
	// and zorin do not, and nothing about the guest differs.
	//
	// SO THE HOP IS REMOVED. The guest is 172.30.0.2 on the bridge the host itself owns; dialling
	// 172.30.0.2:22 needs no DNAT, no conntrack rewrite and no published port - it is one host on a
	// bridge talking to another, which ARP already proved it can do.
	//
	// THE `port` ARGUMENT IS THE HOST'S PUBLISHED PORT (2222), AND THE GUEST DOES NOT LISTEN ON IT.
	// The guest's sshd is on 22 - every `sshd -T` in these runs says `port 22` - so the direct dial
	// is 22. `port` is kept in the signature because the callers pass it and the published DNAT
	// still exists for anything OUTSIDE the container that wants 172.30.0.1:2222; the engine's own
	// client simply no longer uses it.
	_ = port
	d.sshTarget = guest.Target{
		// BACK TO THE DNAT ADDRESS, WHICH IS WHAT THE PASSING FORK USES. fork's `target()` is
		// Host=172.30.0.1, Port=e.SSHPort (2222, the published port) - and fork passes ssh on
		// endeavour/manjaro/zorin/mx/nixos/fedora. Dialling the guest directly at 172.30.0.2:22 was
		// tried here and every CI run failed while ICMP to the same address succeeded; the DNAT hop
		// is the path that is known to work, so it is restored.
		Host:     "172.30.0.1",
		Port:     port,
		User:     f.SSHUser,
		Password: password,
		Key:      mustKeyFile(f.SSHKeyPath),
	}
	d.ssh = newSSHSession(func() (*guest.Client, error) {
		return guest.Dial(d.sshTarget, 30*time.Second)
	})
	return d, func() {
		d.shutdown()
	}, nil
}

func (d *Driver) Log(msg string) { fmt.Println(msg) }

// --- the screen -----------------------------------------------------------------------

// Frame captures the screen through QMP, WITH ONE SCREENDUrP FOR EVERY CALLER THAT WANTS ONE.
//
// THIS WAS THE ROOT CAUSE OF THE FRAMES THAT COULD NOT BE READ, AND IT IS A SHARING PROBLEM RATHER
// THAN A TIMING ONE.
//
// THREE CALLERS ASK FOR A FRAME, and each one used to run its own `screendump`:
//
//	the recording sampler     one per second, for the video
//	an `ocr:` step             every 10 s, for the condition a walk is waiting on
//	the browser page           once a second, while somebody is looking
//
// EACH WRITE IS A WHOLE FRAME, and they all landed in the same directory on the same qmp connection.
// A frame that was still being written by one caller was opened by another, which is exactly the
// `unexpected EOF` that made macos-setup's recovery gate read nothing for forty-five attempts:
//
//	screendump: frame-000294: unexpected EOF
//
// - on a file that was complete by any measure taken afterwards. The file was fine; another
// screendump was writing into the same directory at the time.
//
// SO THERE IS ONE FRAME IN FLIGHT. A caller that arrives while one is being written WAITS for it and
// takes the same result. That is not a cache with a timeout - it is the observation that three readers
// of one screen do not need three screens, and it makes the interleaving impossible instead of
// making the readers polite.
// Frame returns a capture from the shared cache, waiting for one if the cache is empty.
//
// IT USED TO TAKE A FRESH SCREENDUMP FOR EVERY CALLER, under a mutex. That made three readers of one
// screen fight: the recorder, a `check:` gate and the browser page all asked qemu, and the gate lost
// - measured on macos-setup, 14 of 180 attempts returned a frame. The cache takes one capture a
// second, which is what the recorder alone used to make, and every reader shares it.
//
// A CALLER THAT NEEDED A FRESH FRAME USES FrameAt. This one is "the newest" and is what a reader
// looks at when it does not care when the frame was taken - the browser page, a recording tick.
func (d *Driver) Frame() (run.Frame, error) {
	started := time.Now()
	f, err := d.frame()
	var ok *bool
	if err == nil {
		ok = okPtr(true)
	}
	d.auditCall(journal.CallScreendump, "", started, ok, err, "")
	return f, err
}

// frame is `Frame` without the audit, so a re-entrant call cannot record twice.
func (d *Driver) frame() (run.Frame, error) {
	cache := d.frameCache()
	if cache == nil {
		return nil, fmt.Errorf("this machine has no frame cache")
	}
	// THE FIRST CALLER MAY ARRIVE BEFORE THE FIRST TICK, and the recorder's does: its sampler starts
	// at step one, when the ticker has had no chance to run. Waiting for one capture is cheaper than
	// making every caller understand that the cache starts empty.
	if f, ok := cache.waitForFirst(3 * time.Second); ok {
		return f, nil
	}
	f, err := cache.latest()
	if err != nil {
		return nil, err
	}
	return f, nil
}

// frameCache starts the cache on first use, so a driver built by a test that never looks at the
// screen pays for no ticker.
func (d *Driver) frameCache() *frameCache {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.frames == nil {
		d.frames = newFrameCache(func() (cachedFrame, error) {
			d.frameMu.Lock()
			defer d.frameMu.Unlock()
			// A FRAME THAT FAILS IS A LOST TICK, NOT A REASON TO RECONNECT A SHARED CONNECTION.
			//
			// THE SAMPLER AND A TYPING CALLER SHARE ONE `*Conn`, and `qmpRetry`'s repair CLOSES the
			// connection it gives up on. So a sampler that took the repair path closed the socket a
			// `type:` was in the middle of using. MEASURED on the pod, on opensuse's console login:
			//
			//	login [2.3]: typing: input-send-event []+7: the qmp connection is closed
			//
			// - the first character landed, the second did not, and the guest was fine.
			//
			// A CAPTURE IS WORTH LESS THAN A KEYSTROKE. If this tick fails the recording is one frame
			// shorter and says so in its own count; the connection stays as it is, and the caller that
			// is actually SENDING something is the one that gets to decide whether it needs repair.
			// That is why this goes through `qmpSocket` and a bare `Screendump` rather than
			// `qmpRetry`: the sampler observes, it does not repair.
			// THE SAME LOCK AS EVERY OTHER COMMAND, because the transport is shared and a capture is
			// not privileged over a keystroke. What makes this safe is that the capture does NOT
			// repair: a failed tick is a lost frame, and the connection stays as it is for whoever is
			// actually sending something.
			// THE SAMPLER QUEUES LIKE EVERYBODY ELSE. It used to take `qmpMu`, fetch the shared
			// connection and screendump on it - a second path that had to respect the same
			// exclusion the commands did, and the one place where "who may close this connection"
			// was answered twice. Now it is a request to the owner goroutine, and a capture is
			// scheduled between two keystrokes rather than racing them.
			//
			// A CAPTURE IS STILL WORTH LESS THAN A KEYSTROKE: `Run` repairs a closed connection
			// because it must, but a failed dump here is a lost frame and the recording says so.
			var shot qmp.Frame
			err := d.qmpRun(func(conn *qmp.Conn) error {
				f, err := conn.Screendump(filepath.Join(d.work, "frames"))
				if err != nil {
					return err
				}
				shot = f
				return nil
			})
			if err != nil {
				return cachedFrame{}, err
			}
			return cachedFrame{path: shot.Path, w: shot.Width, h: shot.Height, at: time.Now()}, nil
		})
	}
	return d.frames
}

type frame struct {
	path string
	w, h int
}

func (f frame) Path() string     { return f.path }
func (f frame) Size() (int, int) { return f.w, f.h }
func (f frame) Words() []string  { return nil } // no OCR in this build; see the note in run.Driver

// Click presses and releases at a point.
//
// QMP'S ABSOLUTE POINTER TAKES ONE EVENT PAIR PER AXIS, and the guest's tablet scales them. That is
// why the coordinates are the file's own measured pixels rather than anything the host computes.
func (d *Driver) Click(x, y int) error {
	started := time.Now()
	err := d.click(x, y)
	var ok *bool
	if err == nil {
		ok = okPtr(true)
	}
	d.auditCall(journal.CallClick, fmt.Sprintf("%d,%d", x, y), started, ok, err, "")
	return err
}

// click is `Click` without the audit, so a double-click records ONE call rather than two.
func (d *Driver) click(x, y int) error {
	// QMP, NOT THE VNC SESSION, AND THE REASON IS THAT QEMU'S VNC SERVER TAKES ONE CLIENT.
	//
	// The pointer was routed through VNC for a while, on the evidence that a click in the browser
	// console visibly advanced the installer while `input-send-event` did nothing. THAT EVIDENCE WAS
	// MISREAD: the real defect was the DEVICE - `virtio-tablet-pci`, which FreeBSD has no driver for -
	// and once every guest got `usb-tablet` the QMP path worked on the first try.
	//
	// REACHING FOR VNC THEN PRODUCED A SECOND, WORSE BUG. The browser console holds the only client
	// slot, so the daemon's own session was refused or dropped mid-run:
	//
	//	click (1331, 785): write tcp 127.0.0.1:50058->127.0.0.1:5900: write: broken pipe
	//
	// and a walk whose pointer is a socket contention problem fails in a way that reads as a dead
	// widget. QMP has no such limit: it is the same connection the daemon already owns for status,
	// screendumps and shutdown.
	return d.qmpRetry(func(conn *qmp.Conn) error { return conn.Click(x, y) })
}

func (d *Driver) DoubleClick(x, y int) error {
	started := time.Now()
	err := d.doubleClick(x, y)
	var ok *bool
	if err == nil {
		ok = okPtr(true)
	}
	d.auditCall(journal.CallClick, fmt.Sprintf("%d,%d double", x, y), started, ok, err, "")
	return err
}

func (d *Driver) doubleClick(x, y int) error {
	if err := d.click(x, y); err != nil {
		return err
	}
	time.Sleep(120 * time.Millisecond)
	return d.click(x, y)
}

func (d *Driver) Key(qcode string) error {
	started := time.Now()
	err := d.qmpRetry(func(conn *qmp.Conn) error { return conn.Key(qcode) })
	var ok *bool
	if err == nil {
		ok = okPtr(true)
	}
	d.auditCall(journal.CallKey, qcode, started, ok, err, "")
	return err
}

func (d *Driver) Stroke(modifiers []string, key string) error {
	started := time.Now()
	err := d.qmpRetry(func(conn *qmp.Conn) error { return conn.Stroke(modifiers, key) })
	var ok *bool
	if err == nil {
		ok = okPtr(true)
	}
	detail := key
	if len(modifiers) > 0 {
		detail = strings.Join(modifiers, "+") + "+" + key
	}
	d.auditCall(journal.CallKey, detail, started, ok, err, "")
	return err
}

// Type sends text one keystroke at a time, at the pace the caller measured.
func (d *Driver) Type(text string, perChar time.Duration) ([]rune, error) {
	strokes, err := keys.ForText(text)
	if err != nil {
		// A CHARACTER WITH NO QCODE IS NOT FATAL, and the caller logs which ones were skipped.
		// Refusing the whole line because one character cannot be named is worse than typing the
		// rest and saying so - that is the contract the Python implementation's caller expected.
		var kept []keys.Stroke
		var skipped []rune
		for _, ch := range text {
			one, oneErr := keys.ForText(string(ch))
			if oneErr != nil {
				skipped = append(skipped, ch)
				continue
			}
			kept = append(kept, one...)
		}
		strokes = kept
		if err := d.sendStrokes(strokes, perChar); err != nil {
			return skipped, err
		}
		return skipped, nil
	}
	return nil, d.sendStrokes(strokes, perChar)
}

// sendStrokes types one string, and EVERY STROKE IS RETRIED ON ITS OWN CONNECTION IF IT NEEDS TO BE.
//
// THE UNIT OF RETRY IS THE STROKE, NOT THE STRING. A string is many commands, and re-running the whole
// string after a reconnect would type the first half twice - which in a password field is a wrong
// password, not a visible mistake. A stroke is one `input-send-event`: either the guest received it
// or it did not, so retrying that one is exactly "send it again".
func (d *Driver) sendStrokes(strokes []keys.Stroke, perChar time.Duration) error {
	for _, s := range strokes {
		stroke := s
		started := time.Now()
		err := d.qmpRetry(func(conn *qmp.Conn) error {
			return conn.Stroke(stroke.Modifiers, stroke.Key)
		})
		// EVERY CHARACTER IS ITS OWN ROW, because a typed password and a typed command are the two
		// things an audit of this repository has most often needed to reconstruct.
		var ok *bool
		if err == nil {
			ok = okPtr(true)
		}
		detail := stroke.Key
		if len(stroke.Modifiers) > 0 {
			detail = strings.Join(stroke.Modifiers, "+") + "+" + stroke.Key
		}
		d.auditCall(journal.CallType, detail, started, ok, err, "")
		if err != nil {
			return err
		}
		// THE KEY IS RELEASED A SECOND TIME, ON ITS OWN REQUEST, AND THAT IS WHAT STOPS THE REPEAT.
		//
		// MEASURED on a REAL zorin disk, in the pod, with this repository's own daemon and the walk's
		// own arguments. The audit recorded exactly THREE `minus` strokes for `A---B`, and the
		// guest's terminal rendered TWELVE:
		//
		//	$ A------------C---D---E---F        <- and C/D/E/F were correct, three each
		//	    ^^^^^^^^^^^^  twelve
		//
		// Three sent, twelve shown, and the extra nine fill the 600 ms until the next key - a
		// 25 Hz AUTOREPEAT. So the guest's key-up was LOST and its kernel held the key down. It is
		// not a pacing problem: the gap was 200 ms and the repeat simply ran longer. It is not the
		// PS/2 device either - `-machine q35,...,i8042=off` reproduced the same eleven dashes,
		// measured the same way, which is why this fix is here rather than in a walk.
		//
		// A BARE UP-EVENT SETS A KEY TO A STATE IT IS ALREADY IN, so sending it twice is harmless
		// when the first one arrived - and when it did not, the second one is the release. This is
		// the SAME LEVER `ReleaseModifiers` already pulls for shift at the end of a string (see its
		// note in qmp.go: "the only lever this side has over a guest's own driver"); the defect was
		// that it was pulled only at the SEAM BETWEEN LINES, so a dropped release inside a line
		// still repeated for as long as the line lasted.
		// THE GAP COMES FIRST, AND THE SECOND RELEASE AFTER IT.
		//
		// MEASURED on the same real zorin disk, twice: with the second up sent IMMEDIATELY after the
		// stroke, eleven dashes became FIVE - better, and still repeating. Two up-events back to
		// back are two bytes for the guest's queue to swallow at the same instant, so when the first
		// one is dropped the second one is dropped with it. After the gap the queue is empty, and the
		// release lands.
		if perChar > 0 {
			time.Sleep(perChar)
		}
		if err := d.qmpRetry(func(conn *qmp.Conn) error {
			return conn.ReleaseKey(stroke.Key)
		}); err != nil {
			return err
		}
	}
	// THE SHIFT THAT THE LAST CHARACTER NEEDED IS RELEASED AGAIN, BY NAME.
	//
	// A guest can drop a modifier's up-event, and the symptom is the whole NEXT line in capitals:
	// `echo SCREEN_KEPT_ON` ends in an uppercase letter, and zorin's next command arrived as
	// `PKILL _F UPDATE_NOTIFIER` / `SUDO APT_GET UPDATE`. See
	// `qmp.Conn.ReleaseModifiers` for the recording.
	//
	// It runs ONCE PER TYPED STRING rather than per character: an extra modifier event in the middle
	// of text is a way to lose a character, and the fault is at the seam between two lines.
	return d.qmpRetry(func(conn *qmp.Conn) error { return conn.ReleaseModifiers() })
}

// The guest's screen, and the default is FHD because every coordinate in this repository was
// measured on one.
//
// `-device VGA` IS THE ONLY FORM THAT TAKES `xres`/`yres`: `-vga std` cannot, and a guest given it
// comes up at qemu's own 1280x800 - which put every click on a different screen and failed
// `linux-nixos-ssh` with "`region_at` is [1920, 1080] but this frame is 1280x800".
//
// `vgamem_mb=32` IS NOT TUNING. The adapter's default is 8 MiB, and 1920x1080 at 32 bits per pixel
// is 7.91 MiB BEFORE double buffering - so the default is just too small, the adapter cannot hold a
// frame, and the symptom is a black screen from the first capture with no error at all. 32 MiB is
// four frames of headroom.
const DefaultVideoDevice = "VGA,xres=1920,yres=1080,vgamem_mb=32"

// --- the guest ------------------------------------------------------------------------

// Guest is the connection a step addresses the guest through, or nil when none could be made.
//
// THE DIALING AND THE CACHING ARE `sshSession`'S, and that is a change of ownership rather than a move.
// The cache was three interlocks in this function - look under the lock, dial outside it, look again in
// case somebody else won - and it was correct. What it could not do was notice that a cached connection
// had DIED, so a `reboot:` left the next step writing into a dead socket and the step was blamed.
//
// See the note on `sshSession` for the measured failure and for how `Do` repairs it.
func (d *Driver) Guest() *guest.Client {
	d.mu.Lock()
	sess := d.ssh
	d.mu.Unlock()
	if sess == nil {
		return nil
	}
	c, err := sess.Get()
	if err != nil {
		d.logf(fmt.Sprintf("  could not open an ssh connection: %v", err))
		return nil
	}
	return c
}

// DoGuest runs an operation on the guest, repairing a dead session once.
//
// A STEP USES THIS WHENEVER IT IS ABOUT TO TOUCH THE GUEST, because the repair needs to happen at the
// moment of use rather than at the moment somebody remembered to invalidate. `Guest()` is kept for the
// callers that need the raw client, such as the sftp subsystem.
func (d *Driver) DoGuest(fn func(*guest.Client) error) error {
	d.mu.Lock()
	sess := d.ssh
	d.mu.Unlock()
	if sess == nil {
		return errNoSSHSession
	}
	return sess.Do(fn)
}

// errNoSSHSession is what a step gets when it needs the guest and the driver was never prepared for one.
var errNoSSHSession = errors.New("no ssh session is available; a `require_ssh:` step must come first")

// dropGuest closes the cached connection, so the next use dials a new one.
//
// `reboot:` CALLS IT BECAUSE IT KNOWS THE GUEST IS RESTARTING. Nothing else has to: `sshSession.Do`
// repairs a dead session on its own, which is why the interface method that made every caller
// responsible for invalidation is gone.
func (d *Driver) dropGuest() {
	d.mu.Lock()
	sess := d.ssh
	d.mu.Unlock()
	if sess != nil {
		sess.Drop()
	}
}

// --- the machine ----------------------------------------------------------------------

func (d *Driver) Machine() run.Machine { return machineControl{d} }

// SetDisk is how a `disk:` step tells boot: what to start.
func (d *Driver) SetDisk(path string) {
	d.mu.Lock()
	d.disk = path
	d.mu.Unlock()
}

type machineControl struct{ d *Driver }

func (m machineControl) SetDisk(path string) { m.d.SetDisk(path) }

// Disk is what a `compress:` step asks for: the file this stage has been writing to.
//
// IT IS THE DIFF WHEN THERE IS ONE, AND IT IS COMPUTED RATHER THAN STORED. The version before this
// had `flattenFrom` next to `diskPath`, kept equal to `diff.Path` by hand, and released in two
// places - a copy of a fact that `diff` already held.
func (m machineControl) Disk() string {
	d := m.d
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.diff != nil {
		return d.diff.Path
	}
	return d.disk
}

// ReleaseDiff deletes the diff and proves the base was not written to.
//
// IDEMPOTENT, BECAUSE TWO THINGS CALL IT. `compress:` releases the diff after flattening, and the
// driver's own shutdown releases it if the walk failed before that step - and a failed walk is
// exactly when the base has never been re-checked. overlay.Close tolerates a file that is already
// gone, so the second call verifies without deleting.
func (m machineControl) ReleaseDiff() error {
	d := m.d
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.releaseDiffLocked()
}

func (d *Driver) releaseDiffLocked() error {
	if d.diff == nil {
		return nil
	}
	written, err := d.diff.Close()
	if written > 0 {
		d.logf(fmt.Sprintf("  released the diff (%d MiB written this run)", written>>20))
	}
	if err != nil {
		return err
	}
	// THE DIFF IS GONE, SO `Disk()` FALLS BACK TO THE DISK ON ITS OWN - there is no `flattenFrom` to
	// clear, because there is no second field to keep in step.
	d.diff = nil
	return nil
}

// Start launches qemu with the ONE command line every family shares.
func (m machineControl) Boot(spec run.BootSpec) error {
	d := m.d

	// THE LOCK IS FOR FIELDS, NOT FOR THE BOOT. IT DOES NOT SURVIVE THIS BLOCK.
	//
	// THE PREVIOUS VERSION HELD `d.mu` FROM HERE TO THE END OF THE FUNCTION, and the function starts a
	// qemu, waits for its QMP socket and waits for the guest. That made every one of those waits a
	// wait ON THE MACHINE'S LOCK, so the HTTP surface blocked behind a boot in progress:
	//
	//	goroutine 23  [sync.Mutex.Lock]   doBoot -> Boot -> qmpSocket -> d.mu.Lock   <- IT HELD IT
	//	goroutine 66  [sync.Mutex.Lock]   http / -> vncPort -> d.mu.Lock            <- waiting
	//
	// `qmpSocket` takes the lock only to LOOK at `qmp`; it does its I/O outside, on purpose. So
	// calling it from inside a locked section is a self-deadlock - Go's mutex is not reentrant - and
	// the daemon stops answering while it is booting, which reads as a hung process.
	//
	// THE RESERVATION IS WHAT MAKES "ONE MACHINE" TRUE, AND `proc != nil` COULD NOT DO IT.
	//
	// This check used to be `if d.proc != nil`, which is only set AFTER `cmd.Start()` - so the whole
	// span before it, from here through `easyvmnet.Up` and the argument list, was a window in which a
	// second boot saw an idle driver and started a second qemu. The two would then fight over
	// `drive.sock.qmp` (qemu queues the second client and never greets it) and over the tap name, and
	// the symptom was a machine that never answered rather than "two boots".
	//
	// `booting` is taken HERE, under the lock, and released on every path out of this function. It is
	// a claim on the slot, not a description of a process: the difference is exactly the window.
	d.mu.Lock()
	qemu, booting := d.qemu, d.booting
	d.mu.Unlock()
	if booting || qemu.Alive() {
		return &MachineBusy{OS: d.guestOS, Running: qemu.Alive()}
	}
	d.mu.Lock()
	d.booting = true
	d.mu.Unlock()

	// EVERY EXIT FROM HERE RELEASES THE RESERVATION, whether the boot worked or not. A failed boot
	// that kept it would wedge the driver: `proc` stays nil, `booting` stays true, and every later
	// attempt is refused by a machine that does not exist.
	ok := false
	defer func() {
		if !ok {
			d.mu.Lock()
			d.booting = false
			d.mu.Unlock()
		}
	}()

	// THERE IS NO GUEST NAME TO PARSE. A boot names qemu arguments, and the daemon passes them
	// through - so what used to be an identity check here is now nothing at all. `guestOS` is kept
	// only for the message a `MachineBusy` refusal prints, and it is whatever the caller set.
	guestOS := machine.OS(spec.OS)
	d.mu.Lock()
	d.guestOS = spec.OS
	d.mu.Unlock()
	// THE ACCELERATOR IS NAMED IN THE COMMAND LINE, AND QEMU IS THE AUTHORITY ON IT.
	//
	// `-accel kvm` IS IN THE ARGUMENTS BELOW, which makes qemu refuse to start rather than silently
	// choose TCG - the silent choice is what put every macOS leg on software emulation, where
	// `+invtsc` cannot be provided and the guest deadlocks at HANDOFF TO XNU.
	//
	// `CheckKVM()` IS DIAGNOSIS, NOT A GATE. It was a gate for one run, and its `KVM_GET_API_VERSION`
	// call passed a pointer where the ioctl takes no argument - so it reported "invalid argument" on a
	// machine whose qemu had just accepted `-accel kvm`. A CHECK THAT CAN REFUSE A GUEST QEMU WOULD
	// HAVE STARTED IS WORSE THAN NO CHECK: the authority is the program that has to work.
	//
	// So a failure here is a LOG LINE - it tells a reader why a boot was slow or why qemu exited -
	// and the boot proceeds to qemu, whose own message is the verdict.
	if err := machine.CheckKVM(); err != nil {
		d.logf("  " + err.Error())
	}

	// THE BOOT DESCRIPTION GOES THROUGH Validate BEFORE ANY PATH IS USED. A stage that never ran
	// its `fetch:` gets a sentence naming what is missing, not qemu's message about a drive.
	boot := machine.Boot{
		OS:           guestOS,
		Disk:         spec.Disk,
		ISO:          spec.ISO,
		AnswerCD:     spec.AnswerCD,
		DriverCD:     spec.DriverCD,
		Recovery:     spec.Recovery,
		Kernel:       spec.Kernel,
		Initrd:       spec.Initrd,
		KernelAppend: spec.KernelAppend,
		FreshDisk:    spec.FreshDisk,
		NoOverlay:    spec.NoOverlay,
		Incoming:     spec.Incoming,
		Machine:      spec.Machine,
		Args:         spec.Args,
		Dir:          spec.Dir,
		Work:         d.work,
	}
	if err := boot.Validate(); err != nil {
		return err
	}

	// THE DIFF IS MADE BEFORE qemu SEES THE DISK, because what qemu is given IS the diff.
	//
	// AN INSTALL WRITES THE DISK ITSELF (see machine.Boot.WantsOverlay): there is nothing behind a
	// freshly created qcow2 to protect, and a diff would be one more file to flatten. Every other
	// boot reads through a read-only base - which is what makes it safe for the guest to TRIM, and
	// what makes `compress:` a flatten rather than a copy.
	running := spec.Disk
	if boot.WantsOverlay() {
		o, err := overlay.Create(spec.Disk, d.work, "run-overlay.qcow2")
		if err != nil {
			return err
		}
		// THE DIFF IS THE ONLY RECORD. `Disk()` answers "the file this stage wrote to" by
		// asking this field, so there is no second copy to keep in step.
		d.mu.Lock()
		d.diff = o
		d.mu.Unlock()
		running = o.Path
		d.logf(fmt.Sprintf("  running on a diff over %s (read-only); %s",
			filepath.Base(o.Base), o.Path))
	}
	// THE ARGUMENTS ARE REWRITTEN HERE, AND THIS IS THE ONLY PLACE THE DIFF REACHES QEMU.
	//
	// `args` IS NOW THE FILE'S OWN WORDS, WITH NO SUBSTITUTION - that was the point of deleting the
	// placeholder language. But the diff is a fact the RUN decides, not the file: a smoke walk names
	// the disk it was handed (`file=disk.qcow2`, or an absolute path from `--boot-disk`) and the
	// engine is what puts a throwaway layer in front of it. Nothing in the file can spell that,
	// because the file does not know an overlay exists.
	//
	// MEASURED AS A REGRESSION WHEN THE RENDERER WAS DELETED, and it is worse than a broken feature:
	//
	//	before=570161664   after=571211776     the BASE grew by 1 MB
	//	run-overlay.qcow2 = 196672 bytes       the diff stayed an empty header
	//
	// qemu was handed the base, wrote to it, and the overlay was created and closed having never been
	// opened by anything. Every `smoke.walk` was affected - including the ones that had passed - and
	// the failure is silent: the run succeeds and the PUBLISHED ARTIFACT IS THE ONE IT WROTE.
	//
	// SO EACH `file=` VALUE THAT NAMES THE TARGET DISK IS REPLACED WITH THE PATH QEMU MUST OPEN.
	// The comparison is on the FILE, not on a string the file was expected to write: `spec.Disk` is
	// what was resolved (`disk.qcow2` in the work directory, or the absolute path a leg pulled), and
	// any `-drive` whose `file=` names that path is the target. Media (`media=cdrom`,
	// `readonly=on`) name OTHER paths and are left alone.
	// `boot.Disk` IS WHAT THE RUN WILL OPEN, and `spec.Disk` is what the file named. They differ
	// exactly when there is a diff, and that difference is what the arguments have to be told about.
	boot.Disk = running
	if running != spec.Disk {
		boot.Args = machine.ReplaceTargetDisk(boot.Args, spec.Disk, running, d.work)
	}

	qmpPath := filepath.Join(d.work, "drive.sock.qmp")

	// A SOCKET THAT ANSWERS IS A MACHINE THAT IS ALIVE, AND DELETING IT IS NOT A CLEANUP.
	//
	// This line used to be `_ = os.Remove(qmpPath)` and nothing more, and the underscore is what
	// made the defect invisible: removing a leftover socket and destroying a running machine's only
	// control channel were the same statement.
	//
	// MEASURED, on a work directory that already held a live `create` sandbox:
	//
	//	REFUSED: a machine is already running on this daemon: a `create` sandbox, up 3m49s
	//
	// which is right - but then the operator killed the process without a `destroy`, the slot's
	// machine was gone while its qemu was not, and the next Boot deleted the socket file of a qemu
	// that was still running. From that moment the daemon could not reach its own guest:
	//
	//	could not read the screen: qmp /work/drive.sock.qmp: connect: no such file or directory
	//	(waited 30s; is another process already holding this socket? qemu permits one client)
	//
	// and every `/api/click` returned `{"ok":true}` while nothing reached the guest, so the pointer
	// on the screen never moved and a walk looked like it was clicking a dead widget.
	//
	// THE PROBE IS THE WHOLE FIX. A unix socket that completes a connect has a listener behind it,
	// so "connects" means a qemu owns this work directory and this boot must not proceed. Only a
	// socket nobody answers is a leftover, and removing a leftover is honest.
	if err := probeUnixSocket(qmpPath, 250*time.Millisecond); err == nil {
		return fmt.Errorf("a qemu is already listening on %s: another machine owns this work "+
			"directory (find it with `ps -eo pid,args | grep %s`). One work directory holds one "+
			"machine; destroy it, or pass a different --work", qmpPath, filepath.Dir(qmpPath))
	}
	_ = os.Remove(qmpPath)

	// THE MACHINE, FROM THE FILE THAT WROTE IT.
	//
	// EVERY ARGUMENT BELOW `-machine` USED TO BE BUILT HERE, from a shape: the firmware pair, the
	// guest agent's bus, the video adapter, the disk order, the sound card, the SMC key. All of that
	// is a qemu argument, and the file that knows what machine it wants writes it - so this function
	// contributes only what the HOST owns:
	//
	//	-machine / -cpu / -smp / -m   the machine the step named, with its size
	//	-display none                 there is no local display
	//	-serial file:                 the console a step polls
	//	-qmp / -vnc                   this daemon's own sockets
	//	-incoming                     a resume
	//
	// AND NOTHING ELSE. A device the engine has never heard of needs no line of Go: it is a term in
	// the step's `args:`.
	// THE ONLY ARGUMENTS THIS FUNCTION CONTRIBUTES ARE THE HOST'S OWN.
	//
	//	-machine   the value the file wrote (it may hold placeholders)
	//	-display   there is no local display
	//	-serial    the console a step polls
	//
	// EVERYTHING ELSE - `-cpu`, `-smp`, `-m`, every device - is in the file's `args:`, because it is a
	// qemu argument and this build has no opinion about the machine beyond where its sockets go.
	// THE VARIABLES STORE IS A COPY, BECAUSE qemu WRITES TO IT.
	//
	// `OVMF_VARS` is firmware NVRAM: a guest registers its boot entries there and expects them on the
	// next boot. The image ships the PACKAGE'S TEMPLATE, which is read-only in every sense - a guest
	// that wrote into it would leave its entries for the next guest - so each run gets its own copy
	// made once.
	//
	// IT IS MADE WHEN ABSENT RATHER THAN OVERWRITTEN. A stage that reboots inside one run must keep
	// the entries the last boot registered, and a copy every boot would erase them.
	varFile := filepath.Join(d.work, "OVMF_VARS.fd")
	if _, err := os.Stat(varFile); os.IsNotExist(err) {
		if err := copyFile(ovmfVarsTemplate(d), varFile); err != nil {
			return fmt.Errorf("the UEFI variables template: %w. Every UEFI guest needs it, and the "+
				"image installs it at %s", err, ovmfVarsTemplate(d))
		}
	}

	args := []string{
		"-machine", boot.Machine,
		"-display", "none",
		"-serial", "file:" + filepath.Join(d.work, "console.log"),
	}
	// THE FILE'S ARGUMENTS ARE PASSED THROUGH EXACTLY AS IT WROTE THEM. There is no
	// substitution: a path is a path, and qemu runs in the work directory, so a walk that
	// names `disk.qcow2` is naming the file it made.
	args = append(args, boot.Args...)
	args = append(args,
		"-qmp", "unix:"+qmpPath+",server=on,wait=off",
		"-vnc", fmt.Sprintf("127.0.0.1:%d", d.vncNumber()),
	)
	if boot.Incoming != "" {
		args = append(args, "-incoming", "file:"+boot.Incoming)
	}

	// THE NETWORK BEFORE THE MACHINE, because qemu opens the tap itself: a tap that does not exist
	// is `qemu: could not configure /dev/net/tun`, which reads as a qemu problem.
	//
	// Preflight REFUSES WITH A SENTENCE when the container lacks /dev/net/tun or NET_ADMIN, and
	// that sentence is the whole reason it exists - the raw failure is
	// `RTNETLINK answers: Operation not permitted`, which reads as a broken network.
	// AND THE NETWORK PACKAGE'S MESSAGES GO INTO THIS RUN'S LOG. Without this the relay's one
	// explanation - "the udp relay's tun device: ... (the guest's UDP will not be proxied)" - went to
	// stdout, where a container's daemon log does not read it, and the guest's QUIC timing out twenty
	// minutes later was the only symptom.
	easyvmnet.SetLogf(func(format string, args ...any) {
		d.Log(fmt.Sprintf(format, args...))
	})
	if err := easyvmnet.Up(d.port, nil); err != nil {
		return err
	}

	// THE SOCKET IS RELEASED BEFORE THE PROCESS EXISTS, AND THE ORDER IS THE WHOLE THING.
	//
	// `Release` CLOSES the connection it holds for that path. Called after `cmd.Start()`, it closed the
	// connection this very function had just made - so the machine came up and the first step that
	// touched it failed with
	//
	//	press a key so the loader takes it: key "esc": input-send-event []+esc:
	//	the qmp connection is closed
	//
	// MEASURED ON CI, on windows-install, three seconds into a run whose qemu was healthy: the disk was
	// published as 512 bytes because nothing had ever driven the installer. A `Release` AFTER the socket
	// is connected is not a cleanup, it is a self-mutilation.
	//
	// THE PREVIOUS MACHINE'S SOCKET IS NOT "RELEASED" ANY MORE - THERE IS NOTHING TO RELEASE.
	//
	// This used to be a `Release` on the daemon's registry, plus a second one after the handshake,
	// because the registry was a map that outlived every machine and had to be told when a path
	// became free. The connection now belongs to the `qemuProcess` that made it and is closed by that
	// object's `watch`, so a path is free exactly when its process is gone.
	//
	// THE ONLY QUESTION LEFT IS WHETHER SOMETHING ELSE IS LISTENING, and that is a fact about the
	// filesystem rather than about this process's bookkeeping - answered by `probeUnixSocket` above.

	cmd := exec.Command("qemu-system-x86_64", args...)
	// QEMU RUNS IN THE WORK DIRECTORY, so a file a walk names needs no path at all.
	//
	// EVERYTHING A GUEST READS LIVES THERE: its disk, its media, its firmware, its console log. An
	// argument that said `{{work}}/ubuntu.iso` was naming the directory it was already in - and a
	// placeholder that only ever expands to one value is a way of saying nothing.
	//
	// IT IS ALSO WHAT MAKES A WALK RELOCATABLE. The same file runs in `/work`, in
	// `/__w/easyvm-linux/.../work` and in a person's temporary directory, because the only absolute
	// paths in the command line are the ones the DAEMON contributes: the qmp socket, the vnc display
	// and the serial log.
	cmd.Dir = d.work
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	d.logf("qemu " + strings.Join(args, " "))

	// THE PROCESS IS STARTED AND OWNED IN ONE STEP, WHICH IS THE WHOLE POINT OF `qemuProcess`.
	//
	// `startQemu` takes the `Wait`, so from here there is exactly ONE owner of this process and the
	// failure path below is a `Kill` on an object that cannot be lost. The shape it replaces
	// registered `d.proc` by hand after `cmd.Start()` and needed a `defer` to remember to kill it:
	//
	//	qemu 383 started, a failure then returned WITHOUT setting `d.proc` - so `shutdown`
	//	(`if proc != nil`) skipped the kill, `Stop` (`if !running`) reported success, and
	//	`easyvm destroy` printed "the machine was destroyed" over a live qemu that then owned the
	//	work directory for ever.
	// THE IDENTITY AND THE POINTER KIND ARE SET BEFORE THE PROCESS STARTS, because `dialQMP` reads
	// `bootArgs` to decide how to move the pointer - and it runs before the publish block below.
	// MEASURED: with the assignment after the handshake, `dialQMP` saw an empty `bootArgs`, decided
	// the guest had no tablet, and every `click` on a Windows Server Setup page did nothing while the
	// arrow keys worked.
	pointerAbsolute := machine.PointerIsAbsolute(boot.Args)
	d.mu.Lock()
	d.bootArgs = append([]string{spec.Machine}, spec.Args...)
	d.mu.Unlock()
	qemu, err := startQemu(cmd, qmpPath, pointerAbsolute, d.dialQMP)
	if err != nil {
		return fmt.Errorf("starting qemu: %w", err)
	}
	bootFailed := true
	defer func() {
		if bootFailed {
			qemu.Kill()
		}
	}()

	// THE HANDSHAKE IS THIS FUNCTION'S JOB, because a failure here is a boot failure and this is the
	// only place that can attach qemu's own output to the message.
	conn, err := d.dialQMP()
	if err != nil {
		// QEMU'S OWN OUTPUT IS THE ANSWER when it never came up: a missing -device, a bad -machine
		// option or an accelerator it cannot use all say so on stderr and then exit.
		if !qemu.Alive() {
			at, waitErr := qemu.Exit()
			return fmt.Errorf("qemu exited before its QMP socket appeared (%v, %v): %w. qemu's own "+
				"output is above - a missing -device, a bad -machine option or an accelerator it "+
				"cannot use all say so there and then exit", at.Format(time.TimeOnly), waitErr, err)
		}
		return err
	}

	// THE MACHINE IS PUBLISHED ONLY NOW, WITH EVERY PART OF IT IN PLACE: a started process, an open
	// QMP connection, and a disk. NOTHING ELSE IN THIS PROCESS CAN SEE A HALF-BUILT MACHINE, because
	// `qemu` and `booting` are set together under the lock and the process object answers `Alive()`
	// for everybody afterwards.
	qemu.SetConnection(conn)
	bootFailed = false
	d.mu.Lock()
	d.qemu = qemu
	d.booting = false
	d.mu.Unlock()
	ok = true

	// AND THE TAIL OF THE ARGUMENT LIST IS LOGGED, because the line above it is 300 words long.
	d.logf(fmt.Sprintf("  qemu is up (pid %d); one QMP client on %s", cmd.Process.Pid,
		filepath.Base(qmpPath)))
	return nil
}

// MachineBusy is what a second boot is refused with.
//
// IT IS A TYPE RATHER THAN A SENTENCE because the caller has to tell three cases apart that a string
// flattens into one: a machine is RUNNING (name its family), a machine is STILL STARTING (retry in a
// moment), and the slot is held by something that has not produced a machine yet (the boot failed
// part-way and the reservation is being released). The step that gets this prints `Error()`, and a
// person reading the log gets the sentence; a supervisor gets the fields.
type MachineBusy struct {
	// OS is the family of the machine already holding the slot, when it got as far as being known.
	OS string
	// Running says the machine reached qemu, so it is a real guest and not a failed boot.
	Running bool
}

func (e *MachineBusy) Error() string {
	if !e.Running {
		return "a machine is being started on this daemon already; its boot is still in progress " +
			"or its last attempt has not finished unwinding"
	}
	if e.OS == "" {
		return "a machine is already running on this daemon; `destroy` it first"
	}
	return fmt.Sprintf("a %s machine is already running on this daemon; `destroy` it first", e.OS)
}

// Stop shuts down by the method the file named, and there is no silent fallback between them.
// graceOrDefault is the step's `timeout:` when the file gave one, and the build's default otherwise.
func graceOrDefault(grace time.Duration) time.Duration {
	if grace > 0 {
		return grace
	}
	return shutdownTimeout
}

func (m machineControl) Stop(method string, grace time.Duration) error {
	d := m.d
	// THE LOCK IS TAKEN TO LOOK, NOT TO WORK.
	//
	// MEASURED ON UBUNTU-VM: the leg reported `stop the machine` and then sat in `follow it` for
	// thirty minutes with `status` still saying `running`. `Stop` held `d.mu` for its whole body, and
	// the first thing a graceful shutdown does is `d.Guest()` - which takes `d.mu` itself. Same
	// goroutine, same mutex, no way out: the stage had finished every step it exists to do and could
	// never report it.
	//
	// A LOCK AROUND A NETWORK ROUND TRIP WOULD BE WRONG EVEN IF IT DID NOT DEADLOCK: `poweroff` waits
	// on a guest, and every other caller of this driver - `status`, `Frame`, the console - would be
	// blocked behind it for up to five minutes.
	d.mu.Lock()
	qemu := d.qemu
	d.mu.Unlock()
	if !qemu.Alive() {
		return nil
	}
	killNow := false
	switch method {
	case "kill":
		// A KILL IS A KILL, AND IT MUST NOT WAIT FIRST. The placeholder here only logged, and the
		// `waitForExit` below then waited the full `shutdownTimeout` for a guest nobody had asked to
		// power off - five minutes per stage whose file says `shutdown: kill`, which is every
		// `windows-ssh` and every throwaway overlay here.
		d.logf("  killing the guest")
		killNow = true
	case "ssh":
		// KEPT FOR A FILE THAT WAS NOT PORTED. It is the old one-step form: send `poweroff` and wait.
		// New files write the two steps, so the command is visible in the file.
		if err := d.shutdownOverSSH(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%q is not a shutdown method; the ones this build has are `kill` and "+
			"`ssh`. To press the power button write `acpi: power`, to ask the guest agent write "+
			"`qga: shutdown`, and to stop it over ssh write the command as its own step: "+
			"`ssh: ... poweroff` then `wait_until: stopped`", method)
	}
	// AND THE PROCESS IS WAITED FOR, WHICHEVER PATH ASKED IT TO STOP. A guest that was asked politely
	// has to actually exit, or the disk is still open when the next step reads it.
	//
	// A KILL SKIPS THE WAIT: there is nothing to be patient with, the process is being taken down
	// because the file said so or because a graceful request expired.
	var err error
	if killNow {
		err = d.killAndReap()
	} else {
		// THE STEP'S OWN TIMEOUT, AND THE DEFAULT WHEN IT DID NOT GIVE ONE. A `shutdown:` step that
		// says `timeout: 600` means the guest gets ten minutes to power off, which is what a stage
		// that just wrote a desktop needs - see the note on the interface.
		err = d.waitForExit(graceOrDefault(grace))
	}
	if err != nil {
		return err
	}
	// `waitForExit` ALREADY REAPED IT - CALLING `Wait` AGAIN BLOCKS FOREVER.
	//
	// Measured on ubuntu-vm: the leg sat in `follow it` for thirty minutes with `status` still saying
	// `running`, on a machine that had been asked to power off. `waitForExit` calls `d.proc.Wait()`
	// in a goroutine and returns when it comes back; the SECOND `Wait()` below it blocks on an
	// `os.Process` that has already been reaped, and Go's `Wait` does not return an error for that - it
	// waits on a channel nothing will ever close.
	//
	// THE KILL IS ALSO `waitForExit`'S JOB NOW: it returns after reaping, or kills and returns.
	//
	// THE QMP CONNECTION GOES WITH THE PROCESS, AND LEAVING IT BEHIND IS A SELF-CONFLICT.
	//
	// `Stop` USED TO CLEAR `d.proc` AND NOTHING ELSE. That left the dead machine's `*qmp.Conn` in the
	// cache and its path in the registry, and the next `boot` - which is the ordinary shape of a
	// sandbox that runs one leg after another - then failed on its own leftovers:
	//
	//	boot: this qmp socket is already held by this process (by "easyvm"); pass that connection
	//	instead of opening a second - qemu allows one client per socket
	//
	// MEASURED on the pod, twice, on the step after a `stop kill`: `destroy` had been called in
	// between, and `destroy` reaches `shutdown`, so the leftovers were supposed to be gone - but the
	// `stop kill` STEP inside the previous walk had already cleared `proc` and left `qmp` behind, and
	// the boot path's own `Release` then removed the registry entry under a cache that still said a
	// connection existed. The next `qmpSocket` handed out that dead `*Conn`, and every command on it
	// reported `the qmp connection is closed` or, when it was re-`Acquire`d, `ErrBusy` from itself.
	//
	// THE MACHINE IS FORGOTTEN, AND ITS CONNECTION GOES WITH IT.
	//
	// This used to have to clear `d.proc`, `d.qmp` AND a registry entry, in that order, under the
	// right lock - because three objects described one machine. Now the process object owns the
	// connection and `watch` closes it when the process is reaped, so forgetting the process forgets
	// everything. A path is free exactly when its qemu is gone.
	d.mu.Lock()
	d.qemu = nil
	d.mu.Unlock()
	return nil
}

// shutdownTimeout is how long a guest is given to power off after being asked.
//
// THE PYTHON IMPLEMENTATION USED 60 AND RAISED IT TO 300, with a measurement behind it: macOS writes
// its logs, unmounts the share and closes APFS, and a 60-second wait expired on a disk that was doing
// exactly what it should. What happens on expiry is a kill, and the disk then needs a journal replay.
const shutdownTimeout = 300 * time.Second

// shutdownOverSSH asks the guest to power off through its own sshd.
func (d *Driver) shutdownOverSSH() error {
	client := d.Guest()
	if client == nil {
		return fmt.Errorf("`shutdown: ssh` needs a connection, and there is none. A `require_ssh:` " +
			"step must have succeeded first - or use `shutdown: kill` on a throwaway overlay")
	}
	d.logf("  asking the guest to shut down over ssh")
	// `poweroff` RATHER THAN `shutdown -h now`: both are the same request to systemd, and `poweroff`
	// is in /usr/bin on every distribution here while `shutdown` lives in /sbin in some of them.
	//
	// THE CONNECTION DIES AS THE GUEST GOES, SO AN ERROR HERE IS EXPECTED. What matters is that the
	// request was delivered, which is why the failure is logged rather than returned.
	if res, err := client.Run("poweroff", 30*time.Second); err != nil {
		d.logf(fmt.Sprintf("  the connection closed as the guest went down (%v)", err))
		_ = res
	}
	return nil
}

// shutdownOverAgent asks the guest's own qemu-guest-agent to power off.
func (d *Driver) shutdownOverAgent() error {
	sock := filepath.Join(d.work, "qga.sock")
	if _, err := os.Stat(sock); err != nil {
		return fmt.Errorf("`shutdown: guest_agent` needs the agent's socket at %s: %v. The guest "+
			"must have qemu-guest-agent installed AND running - `prove the guest agent is running` "+
			"is the step that checks it", sock, err)
	}
	d.logf("  asking the guest agent to shut down")
	if err := qgaShutdown(sock, "powerdown"); err != nil {
		return fmt.Errorf("guest agent shutdown: %w", err)
	}
	return nil
}

// shutdownOverACPI presses the power button through QMP.
func (d *Driver) shutdownOverACPI() error {
	d.logf("  pressing the power button")
	return d.qmpRetry(func(conn *qmp.Conn) error { return conn.SystemPowerdown() })
}

// killAndReap takes the machine down now and returns once it is gone.
//
// IT REAPS THE WAIT, so the next `stop` or `start` does not find a zombie whose exit status nobody
// collected - and so the disk is closed before the step that publishes it reads the file.
// killAndReap stops qemu in a way that does not throw its writes away.
//
// SIGTERM, THEN SIGKILL ONLY IF IT REFUSES - AND THE DIFFERENCE IS A DISK, MEASURED.
//
// EVERY `Exec format error` IN THIS REPOSITORY'S FREEBSD CHAIN CAME FROM THIS FUNCTION. It sent
// SIGKILL, which drops qemu's own write-back cache: the guest's `sync` puts the data in the VIRTUAL
// disk's cache, and the cache lives in the qemu PROCESS. Killing it is the same as unplugging the
// drive:
//
//	install leg   51 of 51 steps, success
//	disk          /dev/vtbd0p2  freebsd-ufs  - and EVERY BYTE OF IT ZERO
//	ESP           665600-byte loader.efi, intact, `4d 5a 90 00`   (written early, flushed by then)
//	boot          BdsDxe: failed to load ... /\efi\freebsd\loader.efi: Load Error
//
// SIGTERM is qemu's documented "exit now": it closes the block layer, which flushes every cached
// write to the qcow2 before the process goes away. The kill is kept as a FALLBACK for a qemu that
// ignores it, because a machine that will not stop is worse than a disk that needs a replay - but it
// is no longer the first thing that happens.
func (d *Driver) killAndReap() error {
	d.mu.Lock()
	qemu := d.qemu
	d.mu.Unlock()
	if !qemu.Alive() {
		return nil
	}

	// GRACEFUL FIRST. Ten seconds is generous: qemu's flush is bounded by the disk, and the whole
	// point is that this machine is not busy when it is asked to stop.
	//
	// THE `Wait` BELONGS TO `qemuProcess`. This function used to start its own `proc.Wait` in a
	// goroutine and then wait on that channel - which worked, and was the third copy of the same
	// idea in this file. `Terminate` is that idea in one place: SIGTERM, then the one `Wait` the
	// process has, with a deadline.
	if qemu.Terminate(10 * time.Second) {
		d.logf("  the guest stopped on SIGTERM, and qemu flushed the disk as it went")
		return nil
	}
	d.logf("  qemu ignored SIGTERM for 10s; killing it - the disk will need a journal replay")

	qemu.Kill()
	d.logf("  the guest was killed")
	return nil
}

// waitForExit waits for qemu to leave, and kills it if it does not.
func (d *Driver) waitForExit(limit time.Duration) error {
	d.mu.Lock()
	qemu := d.qemu
	d.mu.Unlock()
	if !qemu.Alive() {
		return nil
	}
	if qemu.Wait(limit) {
		d.logf("  the guest powered off")
		return nil
	}
	// THE EXPIRY IS REPORTED, NOT HIDDEN. A guest that did not power off in five minutes is a guest
	// whose disk will need a journal replay, and the next stage's "slow boot" is this line.
	d.logf(fmt.Sprintf("  the guest did not power off within %s; killing it - the disk will need "+
		"a journal replay on its next boot", limit))
	qemu.Kill()
	return nil
}

// Reboot resets the machine and waits for it to answer again.
//
// IT RESETS RATHER THAN STOPPING AND STARTING, and that is not an optimisation: a stop-and-start
// gives the guest a cold boot, which for an installer means it starts over. `system_reset` through
// QMP is what `REBOOT` means in every file that asks for one.
func (m machineControl) Reboot() error {
	d := m.d
	// QEMU'S OWN RESET, NOT A PROCESS RESTART. The command is the machine's, and the guest sees a
	// warm reboot - which is what a stage that says `reboot:` wants, because the alternative is a
	// cold boot that restarts an installer.
	if err := d.qmpRetry(func(conn *qmp.Conn) error { return conn.Reset() }); err != nil {
		return fmt.Errorf("resetting the machine: %w", err)
	}
	// AND THE CACHED SSH CONNECTION DIES WITH THE GUEST.
	//
	// `Guest()` KEEPS ONE CONNECTION for the whole run, which is right until the machine reboots:
	// after a reset the guest's sshd is gone with its TCP state, so the next `ssh:` step writes into
	// a socket that will never answer and reports
	//
	//	error: read what this boot did: ssh session: EOF
	//
	// Measured on run 43063744565's manjaro and opensuse legs, whose LAST step is an `ssh:` after a
	// `reboot:`. Closing it here means the next step dials fresh, which is the only correct answer:
	// a connection to a machine that restarted is not a connection.
	// NOBODY HAS TO REMEMBER THIS, WHICH IS THE IMPROVEMENT: `sshSession.Do` would repair the stale
	// connection on the next step anyway. Dropping it here means the next step does not spend a failed
	// round trip discovering it, and the log says why.
	d.dropGuest()
	d.logf("  the machine was reset; the ssh connection was closed with it")
	return nil
}

// OS is the family this driver was prepared for, so a `boot:` step can tell a no-op from a mistake.
//
// THE FILE'S OWN `os:` IS THE ANSWER, because the driver was built from that file. A second YAML
// naming a different guest against this machine is a question with no good answer - one daemon holds
// one machine - and saying so is better than letting a click land on the wrong screen.
func (m machineControl) OS() string {
	d := m.d
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.guestOS
}

// IsRunning reports whether the guest's qemu process is alive - NOT whether a `*exec.Cmd` is non-nil.
// ResetMachine is the QMP reset, which is what `acpi: reset` sends.
func (m machineControl) ResetMachine() error {
	m.d.logf("  resetting the machine")
	return m.d.qmpRetry(func(conn *qmp.Conn) error { return conn.Reset() })
}

// PressPowerButton is `acpi: power`: the ACPI power-button event, and nothing else.
//
// `shutdown: acpi` USED TO BE THIS PLUS A WAIT. The wait is `wait_until: stopped` now, so a file
// shows both halves - and a file that presses the button and does not wait is a file whose meaning
// is visible rather than one that stopped early by accident.
func (m machineControl) PressPowerButton() error {
	return m.d.shutdownOverACPI()
}

// GuestAgent runs one qemu-guest-agent command.
//
// THE NAMES ARE THE PROTOCOL'S OWN, so a reader of the walkthrough and a reader of the QGA schema see
// the same words. This switch is the whole list, so an unknown name is refused here rather than
// reaching the agent and coming back in its vocabulary.
func (m machineControl) GuestAgent(command string, args map[string]any) (string, error) {
	sock := filepath.Join(m.d.work, "qga.sock")
	if _, err := os.Stat(sock); err != nil {
		return "", fmt.Errorf("no guest agent socket at %s: %v. The guest must have "+
			"qemu-guest-agent installed AND running", sock, err)
	}
	switch command {
	case "ping":
		return "", qgaPing(sock)
	case "info":
		return qgaInfo(sock)
	case "shutdown":
		mode, _ := args["mode"].(string)
		return "", qgaShutdown(sock, mode)
	case "exec":
		code, out, err := qgaExec(sock, stringsOf(args["argv"]),
			time.Duration(floatOf(args["timeout"], 300)*float64(time.Second)))
		if err != nil {
			return out, err
		}
		if code != 0 {
			return out, fmt.Errorf("the command exited %d: %s", code, out)
		}
		return out, nil
	case "file-write":
		path, _ := args["path"].(string)
		body, _ := args["body"].(string)
		return "", qgaWriteFile(sock, path, []byte(body))
	case "file-read":
		path, _ := args["path"].(string)
		data, err := qgaReadFile(sock, path)
		return string(data), err
	case "set-time":
		return "", qgaSetTime(sock, time.Now())
	case "interfaces":
		out, err := qgaNetworkInterfaces(sock)
		return strings.Join(out, "\n"), err
	}
	return "", fmt.Errorf("`qga: %s` is not a command this build runs; the ones it runs are ping, "+
		"info, shutdown, exec, file-read, file-write, set-time and interfaces", command)
}

func stringsOf(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, item := range list {
		if str, ok := item.(string); ok {
			out = append(out, str)
		}
	}
	return out
}

func floatOf(v any, fallback float64) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	}
	return fallback
}

func (m machineControl) IsRunning() bool {
	d := m.d
	d.mu.Lock()
	qemu := d.qemu
	d.mu.Unlock()
	return qemu.Alive()
}

// qmpSocket opens the connection, once - WITHOUT HOLDING THE LOCK WHILE IT WAITS.
//
// THE LOCK IS `d.mu`, AND IT GUARDS EVERY FACT ABOUT THE MACHINE: whether it is running, what disk it
// has, where its console listens. `qmpSocket` used to take it and then call `Acquire`, which waits up
// to THIRTY SECONDS for a socket that a dead qemu is never going to create. For those thirty seconds
// EVERY OTHER CALLER BLOCKED:
//
//	goroutine 1  snapshot()    -> ConsolePort() -> d.mu.Lock()   [sync.Mutex.Lock]
//	goroutine ?  handle("status")                                 blocked behind it
//
// and the symptom was not "qemu is gone" but
//
//	FAILED: reading the reply to "status": read unix @->/work/easyvm.sock: i/o timeout
//
// - a daemon that looks hung, from a connection that is perfectly fine.
//
// SO THE I/O HAPPENS OUTSIDE: the fast path takes the lock, finds a connection and returns; a slow
// path that has to connect does so with the lock RELEASED, then re-checks in case two callers raced
// and keeps the one that got there first.
//
// THERE IS EXACTLY ONE FOR THE LIFE OF THE PROCESS, and that is the property that makes `Acquire`
// able to refuse a second client: `Release` drops a path and `CloseAll` drops everything, but the MAP
// is never replaced. See the note in `Boot` for the seven-minute hang that a second map produced.
//
// THE REGISTRY BELONGS TO THE DAEMON AND NOT TO THE DRIVER, because the guarantee it makes is about
// this process and qemu's sockets - not about one machine. A driver comes and goes; the registry
// outlives every boot, which is what lets `Release` mean "this socket has no client" instead of "a
// whole new map that happens to be empty".
// dialQMP opens one connection to this daemon's QMP socket, waiting for qemu to create it.
//
// IT IS THE ONLY PLACE A CONNECTION IS MADE. The connection belongs to the `qemuProcess` that called
// this - either through `startQemu`'s actor or through `SetConnection` at boot - so there is no
// registry to consult and no cache to keep in step with one.
func (d *Driver) dialQMP() (*qmp.Conn, error) {
	d.mu.Lock()
	args := append([]string(nil), d.bootArgs...)
	d.mu.Unlock()
	conn, err := qmp.Open(filepath.Join(d.work, "drive.sock.qmp"), 30*time.Second)
	if err != nil {
		return nil, err
	}
	conn.SetAbsolutePointer(machine.PointerIsAbsolute(args))
	return conn, nil
}

// qmpRun executes one QMP command on the running machine's owner goroutine.
//
// THE CALLER NEVER TOUCHES A CONNECTION. It says what it wants; the machine decides when, on which
// connection, and whether that connection needs replacing - which is what makes "two callers shared
// one socket" a scheduling question rather than a correctness one.
func (d *Driver) qmpRun(fn qmpCommand) error {
	d.mu.Lock()
	qemu := d.qemu
	d.mu.Unlock()
	if !qemu.Alive() {
		return errMachineGone
	}
	return qemu.Run(fn)
}

// qmpCommand is one command against the monitor.
//
// IT IS A FUNCTION RATHER THAN AN INTERFACE, because the callers are one-liners that close over what
// they want to send - `func(c *qmp.Conn) error { return c.Click(x, y) }` - and an interface would put
// a type in front of every one of them.
type qmpCommand func(*qmp.Conn) error

// qmpRetry runs one QMP command, repairing a dead connection once.
//
// IT IS NOW A NAME FOR `qmpRun`, kept because thirty call sites say it and the intent they express -
// "run this command, survive a closed connection" - has not changed. The repair moved from a shared
// cache-and-registry dance into the owner goroutine, where the connection is a field of the object
// doing the reading rather than something a third party could close underneath it.
func (d *Driver) qmpRetry(fn qmpCommand) error { return d.qmpRun(fn) }

// qmpSocket is the cached connection, for the callers that must look at it rather than command it.
//
// IT IS A NARROW SURFACE: the boot's own wait and `QMP()`. Everything else goes through `qmpRun`, so
// that no caller can hold a connection across another caller's turn.
func (d *Driver) qmpSocket() (*qmp.Conn, error) {
	d.mu.Lock()
	qemu := d.qemu
	d.mu.Unlock()
	if !qemu.Alive() {
		return nil, errMachineGone
	}
	if c := qemu.Connection(); c != nil {
		return c, nil
	}
	c, err := d.dialQMP()
	if err != nil {
		return nil, err
	}
	qemu.SetConnection(c)
	return c, nil
}

// shutdown releases everything this machine owns, AND IT IS IDEMPOTENT.
//
// THE ORDER IS THE DEPENDENCY ORDER, and getting it wrong is how a daemon crashes while shutting
// down: ssh talks to a guest that qemu must still be running; qmp talks to the monitor that lives in
// that qemu; the tap and the share are host devices that outlive a process; and the diff is a file
// that must be verified AFTER the guest has stopped writing to it.
//
// THE QMP CONNECTION IS CLOSED THROUGH THE REGISTRY'S `Release`, NOT THROUGH ITS OWN `Close`, so the
// registry's map stops claiming the path. That is what lets the NEXT machine acquire the same socket
// - the failure mode of "closed but still held" is `ErrBusy` naming an owner that no longer exists.

// probeUnixSocket reports whether anything is LISTENING on path.
//
// IT CONNECTS AND HANGS UP WITHOUT SPEAKING, which is enough and is deliberately not QMP. The
// question this answers is "is there a process behind this path", not "is that process healthy":
// a qemu mid-boot answers the connect and has not yet greeted a client, and that is exactly one of
// the cases where the socket must NOT be removed. Sending a QMP handshake would make this probe
// consume qemu's single client slot, which is the bug `qmp.Registry` exists to prevent.
//
// THE TIMEOUT IS SHORT AND BOUNDED. A unix socket's connect is a scheduler operation, not a network
// round trip, so 250ms is generous; the point of a bound here is that a wedged listener must not
// turn a boot into a hang.
func probeUnixSocket(path string, wait time.Duration) error {
	if path == "" {
		return fmt.Errorf("no path")
	}
	conn, err := net.DialTimeout("unix", path, wait)
	if err != nil {
		return err
	}
	return conn.Close()
}

func (d *Driver) shutdown() {
	d.mu.Lock()
	sess := d.ssh
	d.ssh = nil
	qemu := d.qemu
	d.qemu = nil
	d.booting = false
	d.mu.Unlock()

	// EACH OF THESE IS DONE WITHOUT THE LOCK, because each one can block: closing a session, waiting
	// for a process, stopping a server. A lock held across them blocks `status` for as long as the
	// slowest one, and that reads as a hung daemon.
	if sess != nil {
		sess.Drop()
	}
	// NOTHING TO RELEASE AND NOTHING TO CLOSE BY HAND: the connection belongs to the process object
	// and its `watch` closed it when qemu was reaped. There used to be a registry entry to clear and a
	// cache to empty here, in an order that mattered; both are gone with the map.
	//
	// THE PROCESS GOES WITH ITS OWN OWNER. `Kill` returns only after `watch` has reaped it, so the
	// disk's write lock is released before this function returns - which is what a caller that is
	// about to start another machine on the same work directory depends on.
	qemu.Kill()
	// THE TAP IS A HOST DEVICE, NOT A PROCESS'S. `Down` is a list of `iptables -D` and `ip link del`
	// calls that each tolerate "not there", so it is safe to run when nothing was ever brought up -
	// which is why this needs no `netUp` flag to decide.
	easyvmnet.Down()

	// THE SHARE NEEDS NO TEARDOWN. It used to be one smbd per run that had to be stopped before the
	// bridge went away; it is now a directory the daemon's own HTTP listener reads, so ending the
	// machine ends it.

	// AND THE DIFF IS RELEASED AND ITS BASE VERIFIED, EVEN WHEN THE WALK FAILED.
	//
	// `compress:` releases it on the success path. A walk that failed before that step never
	// re-checks the base - and a failed walk is exactly when "I did not write to it" is most
	// likely to be false. Close is idempotent, so this is safe after a successful flatten too.
	d.mu.Lock()
	err := d.releaseDiffLocked()
	d.mu.Unlock()
	if err != nil {
		d.logf("  " + err.Error())
	}
}

func copyFile(from, dest string) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer dst.Close()
	_, err = dst.ReadFrom(src)
	return err
}

// screenUnused keeps the screen import honest while the frame reader has no OCR: the desktop step
// calls screen.Load directly, and this reference documents that the package is reached from here.
var _ = screen.MinBrightnessDrop

// --- snapshots ---------------------------------------------------------------------
//
// SEE THE NOTE IN package qmp FOR WHY THIS IS A MIGRATION AND NOT `savevm`. The short version: the
// guests here are UEFI, their NVRAM is a pflash device, and `savevm` requires every writable block
// device to support internal snapshots. A migration stream asks nothing of them.
//
// MEASURED ON ALL THREE GUESTS: Linux 2.7 GiB, Windows 12 GiB (unsettled) / 3.7 GiB (settled),
// macOS 5.3 GiB, from an 8 GiB guest. The size is NOT a fixed multiple of RAM - it depends on how
// many pages are zero or compressible at the moment the snapshot is taken, which is why a caller
// must be told the file it got rather than a number it can predict.

// Snapshot writes the machine's whole state to path.
//
// THE MACHINE MUST BE RUNNING. A stopped qemu has no state to save, and a caller that asked is told
// so rather than given an empty file.
func (d *Driver) Snapshot(path string) error {
	d.mu.Lock()
	qemu := d.qemu
	d.mu.Unlock()
	if !qemu.Alive() {
		return fmt.Errorf("no machine is running; a `snapshot:` step needs a `boot:` before it")
	}
	conn, err := d.qmpSocket()
	if err != nil {
		return err
	}
	d.logf(fmt.Sprintf("  snapshot: writing the machine state to %s", path))

	// A LONG SNAPSHOT REPORTS PROGRESS. A 12 GiB state file is a minute of work, and a step that
	// printed nothing until it finished is indistinguishable from a hang.
	done := make(chan struct{})
	go func() {
		tick := time.NewTicker(15 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				if st, n, err := conn.MigrationStatus(); err == nil && st == "active" {
					d.logf(fmt.Sprintf("  snapshot: %s, %s written", st, humanBytes(n)))
				}
			}
		}
	}()
	err = conn.MigrateToFile(path, snapshotTimeout)
	close(done)
	if err != nil {
		return err
	}
	if fi, statErr := os.Stat(path); statErr == nil {
		d.logf(fmt.Sprintf("  snapshot: %s is %s", path, humanBytes(fi.Size())))
	}
	return nil
}

// snapshotTimeout is how long a migration may take.
//
// IT IS GENEROUS BECAUSE THE FILE IS THE GUEST'S MEMORY. 8 GiB of RAM took about 50 seconds on the
// node this was measured on, and a slower node with more memory would take minutes; a tight timeout
// would turn a slow node into a broken snapshot.
const snapshotTimeout = 20 * time.Minute

// humanBytes renders a size for a log line.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// QMP exposes the connection so the interactive surface can drive the machine.
//
// IT RETURNS THE DAEMON'S OWN CONNECTION RATHER THAN OPENING ONE, because qemu serves one client per
// socket: a second `qmp.Registry.Acquire` on the same path would block on the greeting and look like
// a hung guest. That is the same rule `Registry` exists to enforce, and it is why the click and key
// commands live in the daemon rather than in a process of their own.
func (d *Driver) QMP() (*qmp.Conn, error) { return d.qmpSocket() }

// QMPCommand runs one command with the same repair every walkthrough step gets.
//
// IT EXISTS BECAUSE THE INTERACTIVE SURFACE WAS THE ONE CALLER LEFT OUT. `/api/key`, `/api/click` and
// `/api/type` went through `Daemon.withQMP`, which asked `QMP()` for the connection and ran the
// command ONCE - so a connection that had gone away answered `the qmp connection is closed` for
// every request, for ever, while the recorder's sampler on the SAME driver recovered by itself
// through `qmpRetry`. MEASURED on the pod: `api/state` reported a frame captured 0.2 seconds ago
// (the sampler was fine) at the same moment `POST /api/command {"command":"key"}` answered
// `the qmp connection is closed` - two paths, one connection, only one of them repairing it.
//
// A dead connection is discovered by USING it, so the repair belongs on every path that uses one.
func (d *Driver) QMPCommand(fn func(*qmp.Conn) error) error { return d.qmpRetry(fn) }

// VNCPort is where the CURRENT machine's VNC listener is, or 0 when there is no machine.
//
// THE CONSOLE'S URL NO LONGER DEPENDS ON IT, and that is the improvement: there is one listener, on the
// daemon's own port, and it asks this question each time a browser connects. What this answers is
// therefore "is there a guest to look at", which is what a page needs to know before it tries.
func (d *Driver) VNCPort() int {
	d.mu.Lock()
	qemu := d.qemu
	d.mu.Unlock()
	if !qemu.Alive() {
		// NO MACHINE, NO PORT. The port itself is not a variable: see `vncDisplay`.
		return 0
	}
	return vncBase + d.vncNumber()
}

// vncNumber is the display this driver's qemu listens on, derived from its work directory.
//
// TWO DAEMONS MUST NOT ASK FOR THE SAME DISPLAY, and the work directory is what identifies a daemon:
// the deployed pod has one and gets 0, which is the port the console page expects; a second daemon
// anywhere else gets a number that its own path hashes to, so the two cannot collide.
//
// THE RANGE IS SMALL ON PURPOSE - 32 displays, 5900 to 5931 - because a display number is a port and
// a wide scatter would be indistinguishable from a real service. A collision inside the range is
// still possible and qemu says so plainly, which is better than the silent wrong-answer this
// replaced.
func (d *Driver) vncNumber() int {
	if d.work == "" || d.work == "/work" {
		return vncDisplay
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(d.work))
	return int(h.Sum32() % 32)
}

// ErrMachineGone is what a caller gets once the guest's qemu has exited.
//
// IT IS A SENTINEL RATHER THAN A STRING because the executor has to tell it apart from an ordinary
// capture failure. MEASURED ON macos-setup: qemu exited, and the walk's `wait for the recovery menu`
// loop ran to its `max: 180` ceiling - THIRTY MINUTES of
//
//	attempt 40 could not read the screen (screendump: png refused (write: broken pipe) ...); trying again
//
// against a machine that was never coming back. A loop whose condition can no longer be evaluated must
// fail, not retry: "the guest is gone" is the answer, and every further attempt is a minute of somebody's
// afternoon spent confirming it.
var ErrMachineGone = errors.New("the guest is no longer running")

// SSHTarget is where this machine's sshd listens, for a caller that has to ask whether it answers.
func (d *Driver) SSHTarget() guest.Target {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sshTarget
}

// auditCall writes one external call down, when a journal is attached.
//
// IT TAKES THE START TIME rather than timing the write: a keystroke that takes 40ms and a screendump
// that takes 900ms are the two most useful numbers in an audit of a slow stage, and neither is
// visible if the timer wraps the INSERT.
func (d *Driver) auditCall(kind journal.CallKind, detail string, started time.Time, ok *bool, err error, rel string) {
	if d.audit == nil {
		return
	}
	d.audit.call(kind, detail, started, ok, err, rel)
}

// mustKeyFile is guest.LoadKey with a log line instead of an error, for the daemon's own session.
func mustKeyFile(path string) []byte {
	if path == "" {
		return nil
	}
	key, err := guest.LoadKey(path)
	if err != nil {
		log.Printf("easyvm: %v", err)
		return nil
	}
	return key
}

// RunningTheSame reports whether the machine that is up was started from these arguments.
//
// A BOOT NO LONGER NAMES A GUEST, so "is it the same machine" is no longer a name comparison. What a
// second boot in one file means is either "this machine again" - a sandbox adding a toolchain - or a
// different machine, which a daemon cannot hold. The argument list is the identity.
func (m machineControl) RunningTheSame(args []string) bool {
	m.d.mu.Lock()
	have := m.d.bootArgs
	m.d.mu.Unlock()
	if len(have) != len(args) {
		return false
	}
	for i := range have {
		if have[i] != args[i] {
			return false
		}
	}
	return true
}

// ovmfVarsTemplate is the read-only variables store the image ships.
//
// THE ENVIRONMENT OVERRIDES IT because a container image carries one OVMF build and a run may point
// at another; the default is what the `Containerfile` installs.
func ovmfVarsTemplate(d *Driver) string {
	if p := os.Getenv("OVMF_VARS_TEMPLATE"); p != "" {
		return p
	}
	return "/usr/share/OVMF/OVMF_VARS_4M.fd"
}

// sshPassword is the value the ssh client and every `ssh:` step dial with.
//
// A CALLER'S PASSWORD WINS; THE FILE'S OWN `password:` IS THE FALLBACK. `password admin1` in a walk
// is a template variable, not a walk.File field, so a stage that passes no `--password` would
// otherwise dial with an empty one - and an empty password against a disk whose account is `admin1`
// is the failure this whole investigation chased: "unable to authenticate ... no supported methods
// remain" on a guest whose sshd was up, listening and reachable.
func sshPassword(fromCaller string, f *walk.File) string {
	if fromCaller != "" {
		return fromCaller
	}
	if f != nil {
		if v, ok := f.Vars["password"].(string); ok {
			return v
		}
	}
	return ""
}
