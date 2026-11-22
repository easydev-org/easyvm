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
	"syscall"
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
// SO THIS TYPE NOW HOLDS ONLY WHAT A MACHINE OWNS, and `qmpRegistry` moved to the Daemon because
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
	port    int
	pass    string

	// mu guards every field below it. THE LOCK IS NEVER HELD ACROSS I/O - a dial, a migration, a
	// shutdown request are all network round trips, and holding it across one blocks `status`, the
	// console page and every step for as long as the round trip takes. Three separate deadlocks in
	// this file were that mistake.
	mu sync.Mutex
	// booting is set from the moment a boot is accepted until the machine is published or the boot
	// has failed. IT IS WHAT MAKES "ONE MACHINE" TRUE, because it closes the gap between "is there a
	// machine" and "start one" - a gap in which two boots could both see nil and both start qemu.
	booting bool
	proc    *exec.Cmd
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
	// qmpMu SERIALISES "take the connection, use it, and repair it if it is dead" AS ONE UNIT.
	//
	// THE TRANSPORT ALREADY SERIALISES ONE COMMAND - `line.Run` holds its own mutex - AND THAT IS NOT
	// ENOUGH, BECAUSE THE REPAIR IS NOT A COMMAND. MEASURED ON THE POD, on opensuse's console login:
	//
	//	login [2.3]: typing: input-send-event []+7: the qmp connection is closed
	//
	// The first character of the password landed and the second did not, on a guest whose qemu was
	// running and whose socket was listening. The recorder's sampler captures a frame every second and
	// a `type:` sends one `input-send-event` per character; when a capture failed, the sampler took
	// the repair path - drop the shared connection, open another - and the typing caller's next
	// character went into the socket that had just been closed underneath it.
	//
	// SO THE UNIT OF EXCLUSION IS THE WHOLE TRANSACTION, NOT THE COMMAND. Two callers can no longer
	// both decide "this connection is dead" and both act on it; the second waits and finds a working
	// connection that the first already repaired. The cost is nothing that was not already paid:
	// `line.Run` serialises every command anyway, so this lock does not reduce throughput, it only
	// stops the DECISIONS from interleaving.
	qmpMu sync.Mutex
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
	d.sshTarget = guest.Target{
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
// EACH WRITE IS A 6.2 MB PPM, and they all landed in the same directory on the same qmp connection.
// A frame that was still being written by one caller was opened by another, which is exactly the
// `unexpected EOF` that made macos-setup's recovery gate read nothing for forty-five attempts:
//
//	screendump: frame-000294.ppm was not readable within 30s: unexpected EOF
//
// - on a file of 6,220,817 bytes with a correct P6 header. The file was complete; another screendump
// was writing into the same directory at the time.
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
			d.qmpMu.Lock()
			defer d.qmpMu.Unlock()
			conn, err := d.qmpSocket()
			if err != nil {
				return cachedFrame{}, err
			}
			f, err := conn.Screendump(filepath.Join(d.work, "frames"))
			if err != nil {
				return cachedFrame{}, err
			}
			return cachedFrame{path: f.Path, w: f.Width, h: f.Height, at: time.Now()}, nil
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
		if perChar > 0 {
			time.Sleep(perChar)
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

// machineType is the qemu machine this boot asks for.
//
// `q35` IS THE FULL MACHINE and the default: PCIe, an IDE controller for the CD-ROM, a USB
// controller for the tablet, a VGA adapter. Every distribution installer here needs all of it.
//
// `microvm` IS THE SMALL ONE: virtio-mmio devices and a serial line, no PCIe, no IDE, no USB, no
// VGA. A `microvm:` boot says so on its step, because a file may install a distribution and then
// verify the result in the same run, and only the verification wants the small machine.
func machineType(boot machine.Boot) string {
	if boot.MicroVM {
		// `graphics=on` IS WHAT MAKES `see:` POSSIBLE ON A MICROVM, and without it the machine is
		// serial-only. A walkthrough that PROVES THE VOCABULARY has to read the screen, so a microvm
		// with no display could only test the console half - and the measurement primitives are the
		// half this branch exists to make testable.
		//
		// IT IS STILL THE SMALL MACHINE: no PCIe, no IDE, no USB. The graphics option adds the VGA
		// adapter back on the ISA bus, which is one device and about 16 MiB.
		return "microvm,accel=kvm,pcie=off,graphics=on"
	}
	return "q35,accel=kvm,vmport=off"
}

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
	if d.booting || d.proc != nil {
		running := d.proc != nil
		d.mu.Unlock()
		return &MachineBusy{OS: d.guestOS, Running: running}
	}
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

	guestOS, err := machine.ParseOS(spec.OS)
	if err != nil {
		return err
	}
	// THE NAME BECOMES A MACHINE HERE, ONCE. Every qemu argument this boot produces reads a field of
	// this shape - firmware, CPU flags, adapter, sound card, media rules - so a walkthrough's `os:`
	// is the whole of what decides how its guest is started.
	shape, err := machine.Resolve(spec.OS)
	if err != nil {
		return err
	}
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
	opts, err := machine.CommonOptions{CPUs: spec.CPUs, MemMB: spec.MemMB, SSHPort: d.port}.
		Normalise()
	if err != nil {
		return err
	}

	// THE BOOT DESCRIPTION GOES THROUGH Validate BEFORE ANY PATH IS USED. A stage that never ran
	// its `fetch:` gets a sentence naming what is missing, not qemu's message about a drive.
	boot := machine.Boot{
		OS:             guestOS,
		Shape:          shape,
		Disk:           spec.Disk,
		ISO:            spec.ISO,
		AnswerCD:       spec.AnswerCD,
		DriverCD:       spec.DriverCD,
		MicroVM:        spec.MicroVM,
		MicroVMDisplay: spec.MicroVMDisplay,
		KernelAppend:   spec.KernelAppend,
		Kernel:         spec.Kernel,
		Initrd:         spec.Initrd,
		Recovery:       spec.Recovery,
		FreshDisk:      spec.FreshDisk,
		OpenCoreROM:    spec.OpenCoreROM,
		OpenCoreVars:   spec.OpenCoreVars,
		BootImage:      spec.BootImage,
		Incoming:       spec.Incoming,
	}
	if err := boot.Validate(); err != nil {
		return err
	}

	cpu, err := boot.CPU("")
	if err != nil {
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
	boot.Disk = running

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

	args := []string{
		// THE ACCELERATOR IS NAMED, AND ITS ABSENCE WAS THE REASON EVERY LEG RAN ON TCG.
		//
		// `-machine q35` WITH NO `-accel` LETS QEMU CHOOSE, and qemu's default is `tcg` - pure
		// software emulation. It does not fail, it does not warn in a way that reads as a problem,
		// and the guest boots: slowly. Measured on run 43063744565's macos-setup leg, whose log is
		// full of
		//
		//	qemu-system-x86_64: warning: TCG doesn't support requested feature:
		//	  CPUID[80000007h].EDX.invtsc [bit 8]
		//
		// - `+invtsc` is one of the twenty CPU flags macOS needs, and TCG cannot provide it. That is
		// the deadlock documented in internal/machine: every vCPU at 100%, block I/O frozen, the
		// serial output ending at `[EB|LOG:HANDOFF TO XNU]` with no panic.
		//
		// `-accel kvm` MAKES THE FAILURE LOUD: a container without a usable /dev/kvm now says so at
		// startup instead of running at a fiftieth of the speed and timing out forty minutes later.
		// `vmport=off` IS WHAT MAKES THE ABSOLUTE POINTER WORK ON A q35 GUEST.
		//
		// q35 GIVES THE GUEST THE VMWARE BACKDOOR PORT BY DEFAULT, and Linux's `psmouse` driver binds
		// `vmmouse` through it and CLAIMS THE ABSOLUTE-POINTER ROLE. QMP's `input-send-event` with an
		// `abs` axis then goes to a device that cannot read it, and qemu refuses the event outright:
		//
		//	click (965, 981): pointer to (965, 981): Input handler not found for event type abs
		//
		// MEASURED ON A DISK PULLED FROM ghcr: `macos-setup`'s ssh leg reached step 7 of 33 and died
		// there, on the first click of a walk whose earlier stages ran on the SAME qemu arguments -
		// because the guests that were driven before this one did not have `vmmouse` bound, and the
		// macOS disk boots into a state where it is:
		//
		//	QEMU PS/2 Mouse      current=False  abs=False
		//	QEMU HID Tablet      current=False  abs=True
		//	vmmouse              current=True   abs=True     <- every absolute event lands here
		//
		// THE TABLET IS STILL ATTACHED (`usb-tablet,bus=xhci.0`): this disables the COMPETING handler,
		// so the events reach the device the walkthrough was measured against.
		// A MICROVM IS `microvm` RATHER THAN `q35`, AND THAT IS THE WHOLE DIFFERENCE: no PCIe bus, no
		// IDE controller, no USB controller, no VGA - only virtio-mmio devices and a serial line.
		//
		// IT IS FOR WALKTHROUGHS THAT PROVE THE VOCABULARY RATHER THAN INSTALL A DISTRIBUTION. A
		// distribution installer needs UEFI, a virtio-blk disk, a VGA adapter and a USB tablet; a
		// walk whose job is "drive a guest and read its screen" pays for all four and needs none.
		// MEASURED: the kubuntu install is 4 GiB of RAM, 128 GiB of disk and forty minutes for
		// thirty-eight steps; the alpine microvm below is 512 MiB, 1 GiB and under two minutes.
		//
		// `pcie=off` IS EXPLICIT because qemu's default depends on the machine type, and a machine
		// that quietly grew a PCIe bus would be the thing this branch exists to avoid.
		"-machine", machineType(boot),
		"-cpu", cpu,
		"-smp", boot.SMP(opts.CPUs),
		"-m", fmt.Sprint(opts.MemMB),
		"-display", "none",
		// THE CONSOLE GOES TO A FILE, BECAUSE ONE STEP HAS TO READ IT.
		//
		// A Windows install ISO does not boot by itself. The El Torito boot image prints
		//
		//	Press any key to boot from CD or DVD...
		//
		// and waits about five seconds; nothing pressing it means the firmware falls through to
		// PXE and the run ends with four thousand bytes of firmware output and no Windows. That
		// prompt is in the boot sector, before setup.exe runs, so no answer-file setting can
		// suppress it - the only way past it is a keypress, and the only way to know when is the
		// console.
		//
		// OVMF's own marker comes first and is what the Python implementation actually keys on:
		// `UEFI QEMU ... DVD-ROM` appears seconds before the prompt and in every UEFI boot.
		//
		// `-serial file:` rather than a chardev socket: the reader is a step that polls a file,
		// and a socket would need its own owner.
		"-serial", "file:" + filepath.Join(d.work, "console.log"),
	}
	// THE GUEST'S OWN DEVICES, IN THE ORDER THE GUEST READS THEM. This is where the three-way split
	// between a Windows disk boot, a Windows ISO boot and a macOS boot lives - all of it in
	// internal/machine, none of it here.
	// THE QEMU GUEST AGENT'S PORT, WHICH WAS MISSING ENTIRELY.
	//
	// `vm-configure` INSTALLS `qemu-guest-agent` AND THEN PROVES IT IS RUNNING, and the proof failed:
	//
	//	prove the guest agent is running: the command exited 3
	//
	// THREE IS `systemctl is-active`'s "inactive". The agent was installed and could not start, because
	// the device it talks on does not exist: `/dev/virtio-ports/org.qemu.guest_agent.0` is created by a
	// `virtserialport` on a `virtio-serial` bus, and neither was in this command line. The Python
	// implementation has both, behind `shape.qga_socket`.
	//
	// `server=on,wait=off` SO QEMU STARTS WITHOUT A CLIENT: the host connects when it wants to ask
	// something, and a guest with no host attached is normal.
	//
	// macOS GETS `isa-serial` RATHER THAN THE VIRTIO BUS, and that is not a preference: Apple's own
	// agent never loads under QEMU, and the third-party one that does probes `/dev/cu.serial1` first -
	// which is the FIRST isa-serial device qemu adds, so this must be the only one and it is.
	qgaSocket := filepath.Join(d.work, "qga.sock")
	_ = os.Remove(qgaSocket)
	//
	// A MICROVM GETS `isa-serial` FOR THE SAME KIND OF REASON: `virtio-serial` is a PCI device and a
	// microvm has no PCI bus. MEASURED, live:
	//
	//	qemu-system-x86_64: -device virtio-serial: No 'PCI' bus found for device 'virtio-serial-pci'
	//
	// and because qemu exits at startup the step reports `qmp ... connection refused`, which reads
	// as a socket problem rather than an argument one.
	if guestOS.IsMacOS() || boot.MicroVM {
		args = append(args,
			"-chardev", "socket,id=qga0,path="+qgaSocket+",server=on,wait=off",
			"-device", "isa-serial,chardev=qga0",
		)
	} else {
		args = append(args,
			"-chardev", "socket,id=qga0,path="+qgaSocket+",server=on,wait=off",
			"-device", "virtio-serial",
			"-device", "virtserialport,chardev=qga0,name=org.qemu.guest_agent.0",
		)
	}

	// THE VIDEO ADAPTER, AND `-vga std` CANNOT CARRY A RESOLUTION.
	//
	// EVERY WALKTHROUGH IN THIS REPOSITORY WAS MEASURED AT 1920x1080, and a guest given `-vga std`
	// comes up at qemu's own default - 1280x800 - so every coordinate is read on a different screen.
	// That is not hypothetical: `linux-nixos-ssh` failed with
	//
	//	wait for the Console result: `region_at` is [1920, 1080] but this frame is 1280x800
	//
	// Only the `-device VGA` form takes `xres`/`yres`. `vgamem_mb` IS NOT OPTIONAL AT THIS SIZE: the
	// default is 8 MiB and 1920x1080 at 32 bits is 7.91 MiB before double buffering, so the adapter
	// comes up unable to hold a frame - and the symptom is a black screen from the first capture,
	// with no error.
	//
	// MACOS IS UNCHANGED: it passes `vga: none` because OpenCore's guest has `vmware-svga` as a PCIe
	// -device from `machine.ExtraArgs`, and a second adapter would give the guest two.
	// THE ADAPTER THE FAMILY NEEDS, AND FOR ANDROID THAT IS NOT THE DEFAULT.
	//
	// `Boot.VGA` answers `none` for macOS (OpenCore supplies `vmware-svga`), `virtio` for Android
	// (its HWC opens /dev/dri/card0 and a std VGA has no DRM node), and the default otherwise. The
	// resolution rides on the `-device` form because `-vga` cannot carry one.
	//
	// AN ANDROID GUEST CANNOT BE TALKED OUT OF `virtio`, AND THE GUARD IS IN `VGA` RATHER THAN HERE.
	//
	// `CommonOptions.Normalise` fills an unset `VGA` with `std`, so by the time a caller reaches this
	// switch every request LOOKS explicit - a check here refused every android boot, including the
	// ones that asked for nothing, which is how this was found: `boot: os: android needs --vga
	// virtio, not "std"` on a walkthrough that had never mentioned VGA at all.
	//
	// So `Boot.VGA` ignores its argument for this family and answers `virtio` regardless;
	// `TestAndroidRefusesAnyOtherAdapter` holds that for std, cirrus, qxl and vmware.
	switch boot.VGA(opts.VGA) {
	case "none":
		args = append(args, "-vga", "none")
	case machine.AndroidVGA:
		args = append(args, "-device", "virtio-vga,xres=1920,yres=1080")
	case "std":
		// A MICROVM HAS NO PCI BUS, so `-device VGA` cannot attach - measured:
		//
		//	qemu-system-x86_64: -device VGA,xres=1920,yres=1080,vgamem_mb=32:
		//	No 'PCI' bus found for device 'VGA'
		//
		// `-vga std` IS THE ISA FORM of the same adapter. It cannot carry a resolution, which is why
		// every other guest here uses the `-device` form - but a microvm's screen is a text console
		// whose resolution is qemu's own default, and the walkthrough that reads it says what it
		// expects rather than needing 1920x1080.
		if boot.MicroVM {
			// `desktop` ASKS FOR A GPU, NOT A TEXT CONSOLE.
			//
			// `isa-vga` IS A TEXT-MODE ADAPTER: it gives the guest 80x25 with no DRM node and no
			// `/dev/fb0`, so an X server on it cannot find a screen. MEASURED, twice:
			//
			//	openbox on isa-vga   (EE) no screens found - X exits before it draws
			//	xfce on Xvfb         the desktop is in the guest's MEMORY, and a qemu screendump
			//	                     reads the ADAPTER, so the recording shows the console instead
			//
			// `virtio-gpu-device` IS ON THE virtio BUS, NOT PCI, which is why a machine with no PCIe
			// can still attach it: it is the same transport `virtio-blk-device` and
			// `virtio-net-device` already use here. It provides DRM/KMS, so the guest gets
			// `/dev/dri/card0`, X draws, and - the point of this whole exercise - the picture is in
			// an adapter qemu owns, so `screendump` records it.
			if boot.MicroVMDisplay == "desktop" {
				args = append(args, "-device", "virtio-gpu-device")
				break
			}
			// `isa-vga` IS THE ADAPTER A `microvm` CAN ACTUALLY CREATE, and `-vga std` is not:
			//
			//	qemu-system-x86_64: warning: A -vga option was passed but this machine type does
			//	not use that option; No VGA device has been created
			//
			// MEASURED, after switching to `-device isa-vga`: a screendump of the running microvm
			// returns a 720x400 frame that reads
			//
			//	SeaBIOS (version 1.17.0-debian-1.17.0-1)
			//	Booting from ROM...
			//	Welcome to Alpine Linux 3.21
			//	Kernel 6.12.1-3-virt on an x86_64 (/dev/tty1)
			//	localhost login:
			//
			// - which is what `see:` measures. `vgamem_mb=16` is enough for a text console and a
			// quarter of what a full machine's adapter takes.
			args = append(args, "-device", "isa-vga,vgamem_mb=16")
			break
		}
		args = append(args, "-device", DefaultVideoDevice)
	}
	args = append(args, boot.StorageArgs()...)
	// THE SOUND CARD, BEFORE THE POINTER AND THE NETWORK, because `-audiodev` must precede the device
	// that names it and macOS's `usb-audio` rides the xHCI controller `StorageArgs` has just created.
	// See `machine.Boot.AudioArgs` for the measurement and for why macOS gets a different device.
	args = append(args, boot.AudioArgs()...)
	args = append(args, boot.SMCArgs("")...)
	args = append(args, boot.ExtraArgs()...)
	args = append(args,
		"-qmp", "unix:"+qmpPath+",server=on,wait=off",
		"-vnc", fmt.Sprintf("127.0.0.1:%d", d.vncNumber()),
		"-netdev", fmt.Sprintf("tap,id=net0,ifname=%s,script=no,downscript=no",
			easyvmnet.TapName),
		// `romfile=` AND `host_mtu=` ARE NOT OPTIONS EVERY VIRTIO NET DEVICE HAS. `virtio-net-device`
		// - the mmio form a microvm needs - has neither:
		//
		//	-device virtio-net-device,netdev=net0,...,romfile=,host_mtu=1450:
		//	Property 'virtio-net-device.romfile' not found
		"-device", netDevice(boot, easyvmnet.TapMAC, easyvmnet.UplinkMTU()),
	)
	// THE POINTER IS ADDED BY `StorageArgs`, NOT HERE, AND THAT IS THE FIX FOR WINDOWS.
	//
	//	qemu-system-x86_64: -device qemu-xhci,id=xhci: Duplicate ID 'xhci' for device
	//
	// The daemon used to append the controller and the tablet at the end of its own argument list. A
	// Windows boot already has an ANSWER CD and a DRIVER CD, so `StorageArgs` had created the xHCI
	// controller for those - and the daemon, unable to see that, asked for a second one with the same
	// id. qemu refuses rather than merging, so the machine never started and the failure arrived as
	//
	//	boot: qmp /work/drive.sock.qmp: connect: no such file or directory (waited 30s)
	//
	// which points at the socket rather than at the argument that killed qemu. See `Boot.StorageArgs`.

	// FIRMWARE. OVMF for Windows and Linux, OpenCore for macOS - and the variables file is COPIED
	// rather than referenced, because qemu writes to it.
	ovmfCode := os.Getenv("OVMF_CODE")
	if ovmfCode == "" {
		ovmfCode = "/usr/share/OVMF/OVMF_CODE_4M.fd"
	}
	ovmfVars := filepath.Join(d.work, "OVMF_VARS.fd")
	tmpl := os.Getenv("OVMF_VARS_TEMPLATE")
	if tmpl == "" {
		tmpl = "/usr/share/OVMF/OVMF_VARS_4M.fd"
	}
	if !guestOS.IsMacOS() {
		if err := machine.TouchOvmfVars(tmpl, ovmfVars); err != nil {
			return err
		}
	}
	args = append(args, boot.FirmwareArgs(ovmfCode, ovmfVars)...)
	// A MICROVM GETS ITS KERNEL FROM THE HOST, because there is no firmware to read a boot sector and
	// no display to fall back to. The two are mutually exclusive by construction: `FirmwareArgs`
	// returns nothing for a microvm.
	args = append(args, boot.KernelArgs()...)

	// A RESUME LOADS INSTEAD OF BOOTING, and `-incoming` is what says so.
	//
	// IT GOES LAST BECAUSE IT IS NOT A DEVICE - qemu reads the whole machine first and then loads the
	// state into it, so the device set must already be described. That is also why the arguments
	// before this line must match the ones the snapshot was taken with: a different cpu, a different
	// machine type or a different device set is refused with
	//
	//	load of migration failed: Unknown section or instance 'apic' 2
	//
	// which names a section rather than the argument that differed.
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
	// SO IT HAPPENS HERE, while `d.qmp` is nil and the previous machine's client is the only thing the
	// registry can be holding.
	//
	// `qmpMu` IS TAKEN EVEN THOUGH `Release` DOES ITS OWN LOCKING, and that is about the REGISTRY
	// rather than the map's own mutex. The recorder's sampler captures a frame every second and takes
	// `qmpMu` to do it; a `Release` outside that lock can land between the sampler's look at the
	// cache and its `Acquire`, and the two then disagree about who owns the socket. MEASURED on the
	// pod, at `boot the disk`:
	//
	//	boot: this qmp socket is already held by this process (by "easyvm"); pass that connection
	//	instead of opening a second - qemu allows one client per socket
	//
	// - the same self-conflict the earlier fixes were about, arriving through the one path that had
	// not been brought under the lock.
	d.qmpMu.Lock()
	_ = d.qmpRegistry().Release(qmpPath)
	d.qmpMu.Unlock()

	cmd := exec.Command("qemu-system-x86_64", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	d.logf("qemu " + strings.Join(args, " "))
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting qemu: %w", err)
	}

	// THE REGISTRY IS NOT REPLACED, AND THAT IS THE WHOLE FIX FOR A SEVEN-MINUTE HANG.
	//
	// A REGISTRY IS WHAT MAKES "QEMU TAKES ONE CLIENT" TRUE: `Acquire` holds its map across the
	// handshake, so a second caller gets `ErrBusy` naming the first owner. That guarantee is only as
	// wide as the map - and this function used to throw the map away:
	//
	//	d.qmpReg = qmp.NewRegistry()
	//
	// TWO MAPS MEAN TWO CLIENTS. The recorder's sampler starts at `record:` and ticks across every
	// `boot:`, so it takes the registry pointer that existed BEFORE the boot; the boot then made a
	// fresh one, and each asked qemu for the socket with its own empty `held`. qemu accepts the first
	// and queues the second in its listen backlog WITHOUT EVER GREETING IT:
	//
	//	goroutine 21 [IO wait, 7 minutes]:
	//	qmp.(*SocketMonitor).Connect()  socket.go:128   <- dec.Decode(&ban)
	//	machineControl.Boot()           driver.go:680
	//	doBoot -> runOne -> Run
	//
	// The guest had already drawn its recovery menu; the walk believed it was still booting, for seven
	// minutes, past its own `timeout:`.
	//
	// THE REGISTRY NOW LIVES ON THE DAEMON, because "this process is one QMP client" is a fact about
	// the process and not about a machine. What this function does is RELEASE the previous machine's
	// path from it: `Release` closes the connection and drops the path, which is exactly "this socket
	// has no client now" - the state a new qemu needs - while the map itself survives.

	// NO CONSOLE IS STARTED HERE ANY MORE, AND THAT IS THE POINT.
	//
	// The page, its assets and the WebSocket to the guest are served by the DAEMON'S OWN LISTENER on
	// :8080 - one port, bound once, alive as long as the daemon. It used to be a second HTTP server on
	// :6080 that had to be started per boot, which produced
	//
	//	boot: port 6080 is already in use, so the noVNC page cannot be served
	//
	// on the SECOND leg of a daemon that had been running the first one. See `Daemon.handleBridge`.

	// WAIT FOR THE SOCKET BEFORE RETURNING, so the next step can act on the machine rather than
	// racing its creation.
	//
	// AND WHEN IT DOES NOT APPEAR, THE QUESTION IS WHY QEMU IS NOT THERE. A QMP socket error is a
	// SOCKET error - `connect: connection refused`, or a thirty-second wait that says nothing - and the
	// reason qemu never created the socket is in QEMU'S OWN OUTPUT, which the caller cannot see:
	//
	//	-device usb-storage,bus=xhci.0,drive=rec: Bus 'xhci.0' not found
	//
	// Measured on the first macos-setup run that got past OpenCore. qemu printed that and exited, and
	// the step reported `qmp .../drive.sock.qmp: connect: connection refused (waited 30s; is another
	// process already holding this socket?)` - a hypothesis about the socket, pointing a reader at
	// qmp.Registry, while the machine had never started.
	//
	// SO THE PROCESS IS CHECKED FIRST. If it is gone, that is the answer and it is reported as such,
	// with qemu's exit status attached.
	// THE CONNECTION IS TAKEN UNDER THE SAME LOCK EVERY OTHER CALLER USES, because this function
	// `Acquire`s the registry entry the socket belongs to - and `Acquire` is what refuses a second
	// client. Outside `qmpMu` it races the sampler for that entry.
	d.qmpMu.Lock()
	conn, err := d.qmpSocket()
	d.qmpMu.Unlock()
	if err != nil {
		if waitErr := cmd.Process.Signal(syscall.Signal(0)); waitErr != nil {
			exit := "unknown"
			if cmd.ProcessState != nil {
				exit = cmd.ProcessState.String()
			}
			return fmt.Errorf("qemu exited before its QMP socket appeared (%s): %w. "+
				"qemu's own output is above - a missing -device, a bad -machine option or an "+
				"accelerator it cannot use all say so there and then exit", exit, err)
		}
		return err
	}

	// THE MACHINE IS PUBLISHED ONLY NOW, WITH EVERY PART OF IT IN PLACE: a started process, an open
	// QMP connection, and a disk. NOTHING ELSE IN THIS PROCESS CAN SEE A HALF-BUILT MACHINE, because
	// `proc` and `qmp` are set together under the lock and `booting` is what every other accessor
	// checked while this ran.
	d.mu.Lock()
	d.proc = cmd
	d.qmp = conn
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
	running := d.proc != nil
	d.mu.Unlock()
	if !running {
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
	// THE KILL IS ALSO `waitForExit`'S JOB NOW: it returns after reaping, or kills and returns. What
	// is left here is closing the page and clearing the field.
	d.mu.Lock()
	d.proc = nil
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
	proc := d.proc
	d.mu.Unlock()
	if proc == nil {
		return nil
	}

	// GRACEFUL FIRST. Ten seconds is generous: qemu's flush is bounded by the disk, and the whole
	// point is that this machine is not busy when it is asked to stop.
	if err := proc.Process.Signal(syscall.SIGTERM); err == nil {
		done := make(chan struct{})
		go func() {
			_ = proc.Wait()
			close(done)
		}()
		select {
		case <-done:
			d.logf("  the guest stopped on SIGTERM, and qemu flushed the disk as it went")
			return nil
		case <-time.After(10 * time.Second):
			d.logf("  qemu ignored SIGTERM for 10s; killing it - the disk will need a journal replay")
		}
	}

	if err := proc.Process.Kill(); err != nil {
		return fmt.Errorf("kill: %w", err)
	}
	_ = proc.Wait()
	d.logf("  the guest was killed")
	return nil
}

// waitForExit waits for qemu to leave, and kills it if it does not.
func (d *Driver) waitForExit(limit time.Duration) error {
	// THE PROCESS IS TAKEN ONCE, UNDER THE LOCK, AND THE WAIT RUNS ON THAT VALUE.
	//
	// `Stop` clears `d.proc` when it is done, and `status` reads it - so a wait that dereferenced the
	// field from a goroutine would race with both. One local pointer means this function owns what it
	// is waiting for, whatever else happens to the field.
	d.mu.Lock()
	proc := d.proc
	d.mu.Unlock()
	if proc == nil {
		return nil
	}
	done := make(chan struct{})
	go func() {
		_ = proc.Wait()
		close(done)
	}()
	select {
	case <-done:
		d.logf("  the guest powered off")
		return nil
	case <-time.After(limit):
		// THE EXPIRY IS REPORTED, NOT HIDDEN. A guest that did not power off in five minutes is a
		// guest whose disk will need a journal replay, and the next stage's "slow boot" is this line.
		d.logf(fmt.Sprintf("  the guest did not power off within %s; killing it - the disk will need "+
			"a journal replay on its next boot", limit))
		// AND IT IS KILLED AND REAPED HERE, because the goroutine above is still blocked on `Wait`
		// and this function is the only place that knows the guest missed its deadline. A kill with
		// no reap is a zombie holding the disk open.
		_ = proc.Process.Kill()
		<-done
		return nil
	}
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
//
// THE DIFFERENCE COST THIRTY MINUTES, TWICE. `d.proc != nil` stays true after qemu has exited, so every
// caller that asks "is there a machine" got YES for a machine that was gone:
//
//	status            `running step 7/31`      <- nothing was running
//	screen.png        409 from a dead socket
//	screendump        `write: broken pipe`     <- retried to the loop's ceiling
//
// and the walk's `wait for the recovery menu` spent its full `max: 180` on captures that could not
// succeed. THE STATE WAS WRONG; the retry loops were merely the symptom.
//
// `Signal(0)` ASKS THE KERNEL WHETHER THE PROCESS EXISTS, which is the fact. A process that has exited
// and been reaped gives `os.ErrProcessDone`; one that exited and has not been reaped is a zombie, which
// `Signal` also refuses - and in this container that is the normal state, because PID 1 is the daemon
// and it reaps only when it gets round to it.
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
	proc := d.proc
	d.mu.Unlock()
	if proc == nil || proc.Process == nil {
		return false
	}
	if err := proc.Process.Signal(syscall.Signal(0)); err != nil {
		return false
	}
	// A ZOMBIE IS NOT RUNNING. `/proc/<pid>/stat` reports `Z` for one, and on Linux that file is the
	// only place the difference between "exited" and "exited and waiting to be reaped" is visible.
	if state, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", proc.Process.Pid)); err == nil {
		// the state is the first field after the parenthesised command name
		if i := strings.LastIndexByte(string(state), ')'); i >= 0 && i+2 < len(state) {
			if state[i+2] == 'Z' {
				return false
			}
		}
	}
	return true
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
// qmpRegistryLocked returns the daemon's ONE registry, creating it on first use.
//
// THERE IS EXACTLY ONE FOR THE LIFE OF THE PROCESS, and that is the property that makes `Acquire`
// able to refuse a second client: `Release` drops a path and `CloseAll` drops everything, but the MAP
// is never replaced. See the note in `Boot` for the seven-minute hang that a second map produced.
//
// THE REGISTRY BELONGS TO THE DAEMON AND NOT TO THE DRIVER, because the guarantee it makes is about
// this process and qemu's sockets - not about one machine. A driver comes and goes; the registry
// outlives every boot, which is what lets `Release` mean "this socket has no client" instead of "a
// whole new map that happens to be empty".
func (d *Driver) qmpRegistry() *qmp.Registry {
	d.daemon.mu.Lock()
	defer d.daemon.mu.Unlock()
	if d.daemon.qmpRegAll == nil {
		d.daemon.qmpRegAll = qmp.NewRegistry()
	}
	return d.daemon.qmpRegAll
}

func (d *Driver) qmpSocket() (*qmp.Conn, error) {
	// THE CACHED CONNECTION IS HANDED OUT AS IT IS, AND THE REPAIR HAPPENS ON USE.
	//
	// THIS FUNCTION IS ONLY EVER CALLED WITH `qmpMu` HELD (from `qmpRetry` and from the capture
	// path), so "one connection" is not something it has to enforce - there is exactly one caller at
	// a time, and the connection it returns cannot be closed by anybody else while it is being used.
	//
	// THAT IS WHY THERE IS NO PROBE HERE ANY MORE. A probe that failed used to CLOSE the shared
	// connection, and the caller that had just been handed it sent its next command into a closed
	// socket. MEASURED, through the daemon's own HTTP endpoint:
	//
	//	POST /api/command {"command":"key","key":"shift"}   20/20 ok   one command per request
	//	POST /api/command {"command":"type","text":"a"}      0/15 ok   stroke + release-modifiers
	//	    "type: release modifiers: the qmp connection is closed"
	//
	// - the FIRST command of a request never failed and the SECOND repeatedly did, which is the
	// shape of a close performed by a third party in between. A dead connection is discovered by
	// USING it, in `qmpRetry`, where the failure is real.
	d.mu.Lock()
	if d.qmp != nil {
		conn := d.qmp
		d.mu.Unlock()
		return conn, nil
	}
	d.mu.Unlock()

	path := filepath.Join(d.work, "drive.sock.qmp")
	// THE HANDSHAKE IS OUTSIDE `d.mu` AND INSIDE `qmpMu`: it can wait for a socket qemu has not
	// created yet, and nothing else may touch the connection while that wait happens.
	conn, err := d.qmpRegistry().Acquire(path, "easyvm", 30*time.Second)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.qmp = conn
	d.mu.Unlock()
	return conn, nil
}

// qmpRetry runs one QMP command, and reconnects ONCE if the connection reported itself closed.
//
// THIS IS WHERE A DEAD CONNECTION IS NOW DISCOVERED - not by a probe before use, but by the command
// that needed it. `ErrClosed` is the transport saying the socket is gone, and it is trustworthy in a
// way a `query-status` failure was not: it comes from the command the caller actually wanted.
//
// ONLY `ErrClosed` IS RETRIED. A QMP error reply ("the device does not exist") is the machine's
// answer and is returned as-is; retrying it would send the same keystroke twice.
type qmpCommand func(*qmp.Conn) error

func (d *Driver) qmpRetry(fn qmpCommand) error {
	// ONE TRANSACTION AT A TIME: take the connection, use it, repair it, retry - and let nobody else
	// in until that is finished. See the note on `qmpMu` for the measured failure this prevents.
	d.qmpMu.Lock()
	defer d.qmpMu.Unlock()

	conn, err := d.qmpSocket()
	if err != nil {
		return err
	}
	err = fn(conn)
	if !errors.Is(err, qmp.ErrClosed) {
		return err
	}

	// THE FAILED CONNECTION IS DROPPED AND THE PATH RELEASED IN THE SAME CRITICAL SECTION, so no
	// caller can observe "cache empty, registry held" - and since this whole function is now
	// exclusive, nobody can be using the connection this is about to close.
	d.mu.Lock()
	if d.qmp == conn {
		d.qmp = nil
		_ = d.qmpRegistry().Release(filepath.Join(d.work, "drive.sock.qmp"))
	}
	other := d.qmp
	d.mu.Unlock()

	if other != nil {
		// somebody published a replacement while this command was in flight (the boot path does this)
		return fn(other)
	}

	fresh, err := d.qmpSocket()
	if err != nil {
		return err
	}
	if fresh == conn {
		return fmt.Errorf("the qmp connection reported itself closed and could not be replaced: %w",
			qmp.ErrClosed)
	}
	return fn(fresh)
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
	conn := d.qmp
	d.qmp = nil
	proc := d.proc
	d.proc = nil
	d.booting = false
	d.mu.Unlock()

	// EACH OF THESE IS DONE WITHOUT THE LOCK, because each one can block: closing a session, waiting
	// for a process, stopping a server. A lock held across them blocks `status` for as long as the
	// slowest one, and that reads as a hung daemon.
	if sess != nil {
		sess.Drop()
	}
	if conn != nil {
		_ = d.qmpRegistry().Release(filepath.Join(d.work, "drive.sock.qmp"))
	}
	if proc != nil {
		_ = proc.Process.Kill()
		_ = proc.Wait()
	}
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
	running := d.proc != nil
	d.mu.Unlock()
	if !running {
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
	running := d.proc != nil
	d.mu.Unlock()
	if !running {
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

// netDevice builds the network device this machine can attach.
//
// THE PCI FORM TAKES `romfile=` AND `host_mtu=`, and both are load-bearing there: the empty `romfile`
// keeps qemu from looking for a PXE ROM that is not installed in this image, and `host_mtu` matches
// the tap. The mmio form a microvm needs has NEITHER property, so the two cannot share a format
// string - MEASURED, live:
//
//	-device virtio-net-device,netdev=net0,...,romfile=,host_mtu=1450:
//	Property 'virtio-net-device.romfile' not found
func netDevice(boot machine.Boot, mac string, mtu int) string {
	if boot.MicroVM {
		return fmt.Sprintf("virtio-net-device,netdev=net0,mac=%s", mac)
	}
	return fmt.Sprintf("virtio-net-pci,netdev=net0,mac=%s,romfile=,host_mtu=%d", mac, mtu)
}

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
