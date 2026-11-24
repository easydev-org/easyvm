// Package daemon owns the machine, and it is the ONLY process that does.
//
// # WHY THIS IS A SERVER AND NOT AN EMBEDDED LIBRARY
//
// A step executor is a state machine with a position: which step is next, what the guest answered,
// whether the machine is up. A CLI that runs it holds that position in a local variable, so the
// position dies with the process - and with it the machine, because the qemu it started is its
// child.
//
// The consequence that matters is diagnostic: at step twelve of twenty-nine, a person who wants to
// look at the screen cannot. There is no page, no shell, no pause - only a log that ends, and the
// next attempt costs a fifteen-gigabyte pull.
//
// SO THERE IS ONE OWNER. The daemon holds qemu, the QMP socket, the ssh connection and the noVNC
// bridge, and every client is a REQUEST. That is not a preference about architecture; it is the
// only way the rule this repository already has - one QMP client per socket - can be a STRUCTURAL
// fact rather than a convention two processes might each believe they are keeping.
//
// THE COST, STATED PLAINLY: a CI job now starts a daemon and then asks it to run, instead of
// running one command. `easyvm run` refuses without a daemon rather than falling back to an
// embedded engine, because a fallback is how you get two engines that disagree.
package daemon

import (
	"errors"
	"fmt"
	"github.com/easydev-org/easyvm/internal/journal"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/easydev-org/easyvm/internal/keys"
	"github.com/easydev-org/easyvm/internal/protocol"
	"github.com/easydev-org/easyvm/internal/qmp"
	"github.com/easydev-org/easyvm/internal/run"
)

// The wire types live in internal/protocol so the client can import them without importing this
// package. These aliases keep the daemon's own code readable.
type State = protocol.State
type Request = protocol.Request
type Response = protocol.Response

// Screen is one captured frame, aliased for the same reason.
type Screen = protocol.Screen

// SocketPath is where a daemon listens, given a work directory.
//
// IT IS A UNIX SOCKET AND NOT A TCP PORT. The daemon controls a machine on this host and needs no
// network exposure; a socket is a filesystem permission, which is the right shape for "only this
// user may drive this VM". It also cannot be reached by accident from another container.
func SocketPath(work string) string {
	return filepath.Join(work, "easyvm.sock")
}

// Daemon is the owner.
type Daemon struct {
	// jdb is the run's journal: the structured record of every step and every external call, which
	// `/api/journal` reads. Nil until a run has opened one.
	jdb *journal.DB
	mu  sync.Mutex
	// st is the current state, guarded by mu.
	st State
	// ex is the executor of the run in progress, nil when idle.
	ex *run.Executor
	// done is closed when the current run finishes.
	done chan struct{}
	// stopping asks the run to stop at the next step boundary.
	stopping bool
	// shutdown asks Serve to return.
	shutdown chan struct{}
	// work is this daemon's directory: its socket, its frames and its disks.
	work string
	// lastProbe is the newest ssh and guest-agent answer. See probe.go: measuring them inside
	// `/api/state` made every poll slower than the poller's own timeout on a real run.
	lastProbe probeState

	// --- THE ONE MACHINE ---------------------------------------------------------------
	//
	// A DAEMON HOLDS AT MOST ONE MACHINE, and "at most one" has to be a fact about ONE variable, not
	// a rule about four. The version before this had `machine`, `machineClose`, `sandbox` and
	// `sandboxClose`, and the four could disagree:
	//
	//	`sandbox`      says a `create` owns the machine
	//	`machine`      says there is one
	//	`machineClose` is what stops it
	//	`sandboxClose` is the SAME closer under another name, set by the other entry point
	//
	// MEASURED: `startSandbox` checked `d.sandbox` and `d.ex` but NOT `d.machine`, so `create` on a
	// daemon already running a `run`'s machine built a second driver and overwrote the closer - and
	// the first machine's qemu was then unreachable, holding /dev/kvm until the container died.
	//
	// SO THEY ARE ONE STRUCT, and the invariants are the struct's own: a `slot` with a `driver` has a
	// `close`; `closing` is set only while `close` runs; and `owner` says who may end it.
	machine *machineSlot
	// qmpReg is THE PROCESS'S ONE QMP REGISTRY, and it lives here rather than on a Driver because
	// "this process may have one QMP client per socket" is a fact about the process.
	//
	// IT USED TO BE A DRIVER FIELD, AND `Boot` REPLACED IT ON EVERY BOOT. The recorder's sampler
	// holds the driver, so it kept the OLD map while the boot made a new one - two maps, two empty
	// `held` sets, two clients, and qemu greeting only the first. The sampler's client worked and
	// `Boot` waited seven minutes for a greeting that was never coming:
	//
	//	goroutine 21 [IO wait, 7 minutes]:  qmp.(*SocketMonitor).Connect()
	//	goroutine 27 [IO wait]:             the sampler's successful listen
	//
	// A single owner for the life of the process is the only shape in which `Acquire`'s refusal
	// means anything.
	qmpRegAll *qmp.Registry
}

// machineSlot is the one machine a daemon may hold: ITS OWNER, ITS PHASE, AND THE TWO THINGS THAT
// MATCH IT TO THE WORLD - the driver to act through and the function that stops it.
//
// # THE PHASE IS THE STATE MACHINE, AND IT IS THE ONLY ONE
//
//	absent ──reserve──► starting ──publish──► running ──quiesce──► stopping ──► absent
//	                        │                    │
//	                        └──fail──────────────┴──(close)──► absent
//
// EVERY DECISION ABOUT "MAY A BOOT START" IS A QUESTION ABOUT THIS FIELD. Before it, the answer was
// spread over `machine == nil`, `d.sandbox`, `d.ex != nil` and `proc != nil`, and each combination
// meant something slightly different to whoever wrote the check:
//
//	`startSandbox` read `d.sandbox` and `d.ex` and NEVER `d.machine`, so `create` on a daemon already
//	running a `run`'s machine built a second driver, overwrote the closer, and left the first qemu
//	unreachable holding /dev/kvm until the container died.
//
// THE LAST TRANSITION IS THE IMPORTANT ONE. `stopping` means the slot is STILL OCCUPIED while `destroy`
// tears the machine down, so `absent` never means "the previous one is half gone" - it means gone. A
// boot that arrives mid-teardown is told to retry, not handed a disk that is still being written.
type machineSlot struct {
	// phase is the state machine. Guarded by Daemon.mu.
	phase slotPhase
	// driver is what steps act through. Nil only while `starting` has not produced one yet.
	driver run.Driver
	// close stops everything the driver owns. IT IS SET BY `newMachineSlot` AND NEVER AFTERWARDS, so
	// a slot with a driver has a closer - which is how a machine became unreachable once.
	close func()
	// owner is who may end it: `run` for a walkthrough in progress, `create` for a sandbox.
	//
	// THE DISTINCTION DECIDES WHAT HAPPENS WHEN THE WALK ENDS. A `run` closes its machine when it
	// finishes cleanly, because a bake that outlived its file would hold /dev/kvm forever. A
	// `create` has no end except `destroy`, so its machine stays.
	owner machineOwner
	// started is when the slot was reserved, for the errors that name how long something has held it
	// and for the abandoned-run check below.
	started time.Time
}

// slotPhase is where a machine is in its life. THE ZERO VALUE IS `absent`, so a nil slot and an unused
// slot mean the same thing and there is no third state to forget.
type slotPhase int

const (
	// slotAbsent means there is no machine and the slot is free.
	slotAbsent slotPhase = iota
	// slotStarting means a boot or a prepare has been accepted and has not produced a machine yet.
	//
	// IT IS A REAL STATE, NOT A MOMENT: a `run` loads a YAML, creates a 128 GiB qcow2 and may fetch a
	// multi-gigabyte ISO before qemu exists. A second request that saw `absent` during that would build
	// a second machine over the first one's disk.
	slotStarting
	// slotRunning means there is a live machine.
	slotRunning
	// slotStopping means `destroy` is tearing it down. THE SLOT IS STILL OCCUPIED.
	slotStopping
)

func (p slotPhase) String() string {
	switch p {
	case slotStarting:
		return "starting"
	case slotRunning:
		return "running"
	case slotStopping:
		return "stopping"
	}
	return "absent"
}

// machineOwner names who holds the slot, so a refusal can say so.
type machineOwner int

const (
	// ownerRun is a walkthrough: its machine ends when the file does.
	ownerRun machineOwner = iota
	// ownerCreate is a sandbox: its machine ends only at `destroy`.
	ownerCreate
)

func (o machineOwner) String() string {
	if o == ownerCreate {
		return "a `create` sandbox"
	}
	return "a `run`"
}

// newMachineSlot pairs a driver with the only function that can stop it.
//
// THE PAIRING IS THE CONSTRUCTOR'S JOB, so there is no way to publish a driver whose closer is
// missing - which is how a machine became unreachable and held /dev/kvm until the container died.
func newMachineSlot(driver run.Driver, close func(), owner machineOwner) *machineSlot {
	return &machineSlot{
		phase:   slotRunning,
		driver:  driver,
		close:   close,
		owner:   owner,
		started: time.Now(),
	}
}

// --- the slot's transitions ------------------------------------------------------------
//
// THEY ALL TAKE `d.mu` THEMSELVES AND ARE THE ONLY WRITERS OF `d.machine`. Anything that reads the slot
// goes through `d.slot()` for the same reason: one accessor, one lock, no caller deciding which field
// answers "is there a machine".

// reserve claims the slot for a new machine, or says why it cannot.
//
// IT SETS `slotStarting`, SO THE CLAIM IS VISIBLE BEFORE ANY WORK BEGINS. The gap between "is there a
// machine" and "start one" is where two boots used to both see `nil`, which is the whole reason this
// method exists as one operation rather than as a check followed by a store.
// stoppingLimit is how long a teardown may hold the slot before it is released anyway.
//
// IT IS NOT A TIMEOUT ON THE TEARDOWN - the closer keeps running. It is the point after which the
// NEXT machine stops being refused on behalf of work that has already taken longer than any honest
// teardown does.
const stoppingLimit = 2 * time.Minute

func (d *Daemon) reserve(owner machineOwner) (*machineSlot, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.reserveLocked(owner); err != nil {
		return nil, err
	}
	return d.machine, nil
}

// reserveLocked is the body of `reserve` for a caller that already holds `d.mu`.
func (d *Daemon) reserveLocked(owner machineOwner) error {
	switch slot := d.machine; {
	case slot == nil || slot.phase == slotAbsent:
		d.machine = &machineSlot{phase: slotStarting, owner: owner, started: time.Now()}
		return nil
	case slot.phase == slotStopping:
		// THE PREVIOUS MACHINE IS STILL CLOSING ITS DISK. This is a retry, not a refusal: the caller
		// asked too early, and a second attempt a moment later will succeed.
		//
		// BUT A TEARDOWN THAT NEVER FINISHES MUST NOT HOLD THE SLOT FOR EVER. MEASURED on a pod:
		//
		//	FAILED: the previous machine is still closing; retry in a moment
		//
		// answered for minutes, across every later create, with no qemu left to wait for. The cause
		// was a closer that blocked on a child this process never reaped (see `reapOrphans`), and a
		// second cause is any closer that returns only on a signal that never comes.
		//
		// A CLOSER HAS NO TIMEOUT OF ITS OWN, so the slot needs one. Two minutes is longer than any
		// honest teardown - a kill plus a disk flush - and short enough that a wedged one is a
		// nuisance rather than a dead daemon. When it expires the slot is released with a loud line:
		// whatever was closing is now this process's leak, not the next machine's refusal.
		if time.Since(slot.started) > stoppingLimit {
			fmt.Printf("[easyvm]   a teardown has been running for %s; releasing the slot so the "+
				"next machine can start. Whatever it was closing is now leaked\n",
				time.Since(slot.started))
			d.machine = &machineSlot{phase: slotStarting, owner: owner, started: time.Now()}
			return nil
		}
		return ErrMachineClosing
	case slot.phase == slotStarting:
		return ErrMachineStarting
	default:
		return &ErrMachineRunning{Owner: slot.owner, OS: driverOS(slot.driver),
			For: time.Since(slot.started)}
	}
}

// publish fills a reserved slot with the machine that was built for it.
//
// IT REFUSES IF THE SLOT IS NO LONGER THE ONE THAT WAS RESERVED, which is what stops a run that was
// abandoned - or a `destroy` that arrived while the boot was working - from installing a live machine
// into a slot that no longer belongs to it.
func (d *Daemon) publish(slot *machineSlot, driver run.Driver, close func()) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.machine != slot || slot.phase != slotStarting {
		return ErrSlotLost
	}
	slot.driver = driver
	slot.close = close
	slot.phase = slotRunning
	return nil
}

// slot returns the current slot and its phase under the lock, or nil.
func (d *Daemon) slot() (*machineSlot, slotPhase) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.machine == nil {
		return nil, slotAbsent
	}
	return d.machine, d.machine.phase
}

// release publishes `absent`, and it is the LAST thing `destroy` does.
//
// RELEASING BEFORE THE TEARDOWN WOULD BE THE BUG THIS TYPE EXISTS TO PREVENT: a boot arriving in the
// window would start qemu against a disk the previous machine is still writing.
func (d *Daemon) release(slot *machineSlot) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.machine == slot {
		d.machine = nil
	}
}

// --- the errors a reserved slot answers with -------------------------------------------
//
// THEY ARE TYPES RATHER THAN SENTENCES because the caller has to make three different decisions from
// them: retry (`closing`, `starting`), give up (`running`), or start fresh (`absent`). A single string
// flattens that into one, and every caller then guesses.

// ErrMachineClosing says a machine IS there, and its teardown has not finished.
var ErrMachineClosing = errors.New("the previous machine is still closing; retry in a moment")

// ErrMachineStarting says a machine is being built right now and has not published itself yet.
var ErrMachineStarting = errors.New("a machine is being started on this daemon already; retry in a moment")

// ErrSlotLost says the reservation was taken away while the machine was being built.
var ErrSlotLost = errors.New("this machine's reservation was cancelled while it was starting - a " +
	"`stop` or a `destroy` arrived, so the machine that was just built is closed again")

// ErrMachineRunning is a refusal that names the machine already holding the slot.
type ErrMachineRunning struct {
	Owner machineOwner
	OS    string
	For   time.Duration
}

func (e *ErrMachineRunning) Error() string {
	held := e.Owner.String()
	if e.OS != "" {
		held += " (`" + e.OS + "`)"
	}
	if e.For > 0 {
		held += fmt.Sprintf(", up %s", e.For.Round(time.Second))
	}
	return "a machine is already running on this daemon: " + held +
		". One daemon holds one machine; `destroy` it first"
}

// driverOS is the family a driver reports, for a message. Empty when it has none yet.
func driverOS(drv run.Driver) string {
	if drv == nil {
		return ""
	}
	if m := drv.Machine(); m != nil {
		if named, ok := m.(interface{ OS() string }); ok {
			return named.OS()
		}
	}
	return ""
}

// New returns an idle daemon.
func New() *Daemon {
	return &Daemon{
		st:       State{Status: "idle"},
		shutdown: make(chan struct{}),
	}
}

// ServeOptions configures Serve.
type ServeOptions struct {
	// Work is where the frames and the guest's scratch files live.
	//
	// IT NO LONGER HOLDS A SOCKET, because there is no socket: HTTP is the only transport, for the
	// reason spelled out on Serve.
	Work string
	// HTTPAddr is where the browser surface and the whole API listen. Empty means DefaultHTTPAddr.
	HTTPAddr string
	// Ready is called once the listener is open, for a supervisor that waits for an address rather
	// than polling for a file - a file that exists is not a daemon that answers.
	Ready func(addr string)
	// ShapesDir REPLACES the machine shapes compiled into the binary.
	//
	// Empty means "use the embedded ones", which is what a container wants: the image carries the
	// binary, not a source tree. A walkthrough repository can point this at its own directory to add
	// a machine this build does not ship - see `internal/machine/shape.go` for why a shape is data.
	ShapesDir string
}

// Serve listens on ONE HTTP PORT until Shutdown.
//
// THE UNIX SOCKET IS GONE, AND IT WAS THE SOURCE OF A DEADLOCK THAT LOOKED LIKE A DEAD DAEMON.
//
// Two transports meant two code paths into the same state, and the socket's was SERIAL:
//
//	for {
//		conn, _ := ln.Accept()
//		d.serveConn(conn)     // reads until the CLIENT closes
//		conn.Close()
//	}
//
// A client that asked `status`, got its answer and kept the connection open - which is exactly what a
// poller does - held that loop for as long as it stayed connected. Nothing else was accepted, and the
// HTTP surface blocked behind the machine's lock:
//
//	goroutine 1   [IO wait]                     <- acceptLoop, in serveConn
//	goroutine 66  [sync.Mutex.Lock] 3 minutes   <- http / -> vncPort -> d.mu
//	goroutine 11  [sync.Mutex.Lock] 8 minutes   <- run -> qmpSocket -> d.mu
//
// and `curl http://127.0.0.1:8080/` accepted the connection and never got a byte back, which reads as
// a hung daemon rather than as contention on a second transport nobody needed.
//
// HTTP HAS NO SUCH SHAPE: `net/http` spawns a goroutine per connection itself, so one slow client
// cannot stop another, and `curl` is a diagnostic that a person can run.
func (d *Daemon) Serve(opts ServeOptions) error {
	if opts.Work == "" {
		return fmt.Errorf("a work directory is required; the frames live in it")
	}
	if err := os.MkdirAll(opts.Work, 0o755); err != nil {
		return err
	}
	d.work = opts.Work

	// THE DIRECTORY IS CLAIMED BEFORE ANYTHING WRITES TO IT, and the claim is the KERNEL's.
	//
	// Everything a machine owns lives in this one directory - the qmp socket, the tap, the overlay,
	// the frames, the disk - and each is a singleton that `internal/qmp.Registry` can only enforce
	// within one process. A second `easyvm` on the same `--work` therefore agrees with the first
	// about nothing, and the first symptom used to be a socket file deleted out from under a running
	// qemu. `LockWork` makes that state unreachable rather than diagnosable.
	//
	// IT IS RELEASED ON EVERY EXIT PATH, which `syscall.Flock` makes automatic: the kernel drops the
	// lock when the file descriptor closes, and it drops it when the process dies, including on
	// SIGKILL. The deferred Release is for tidiness; the guarantee does not depend on it.
	lock, err := LockWork(opts.Work)
	if err != nil {
		return err
	}
	defer lock.Release()

	// THE PROBES RUN BESIDE THE DAEMON, NOT INSIDE A REQUEST. See probe.go for the measurement: a
	// snapshot that asked them made every `/api/state` slower than the poller's own timeout.
	probesStop := make(chan struct{})
	defer close(probesStop)
	go d.runProbes(probesStop)

	// SIGTERM STOPS THE MACHINE AND THE DAEMON, in that order, so a container stop does not leave
	// qemu behind holding /dev/kvm.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		select {
		case <-sigs:
			d.requestShutdown()
		case <-d.shutdown:
		}
	}()

	return d.ServeHTTP(HTTPServeOptions{Addr: opts.HTTPAddr, Ready: opts.Ready})
}

// requestShutdown stops any run and asks Serve to return.
func (d *Daemon) requestShutdown() {
	d.mu.Lock()
	d.stopping = true
	ex := d.ex
	done := d.done
	// THE MACHINE IS STOPPED HERE TOO, AND THROUGH THE SLOT RATHER THAN A FLAG BESIDE IT.
	//
	// A sandbox's machine is not part of any run loop, so a container stop that only waited for `ex`
	// would leave qemu holding /dev/kvm with nothing able to reach it. There is one slot, and
	// whatever holds it is what gets closed.
	var closeFn func()
	if slot := d.machine; slot != nil && slot.phase != slotStopping && slot.phase != slotAbsent {
		slot.phase = slotStopping
		closeFn = slot.close
	}
	d.mu.Unlock()

	if closeFn != nil {
		closeFn()
	}

	if ex != nil {
		// THE MACHINE IS STOPPED HERE, NOT BY THE RUN LOOP, because the run loop may be blocked in
		// a step that waits for a guest which will never answer.
		ex.StopNow("kill")
		if done != nil {
			select {
			case <-done:
			case <-time.After(15 * time.Second):
			}
		}
	}
	d.mu.Lock()
	select {
	case <-d.shutdown:
	default:
		close(d.shutdown)
	}
	d.mu.Unlock()
}

// handle answers one request.
func (d *Daemon) handle(req Request) Response {
	switch req.Command {
	case "status":
		st := d.snapshot()
		return Response{OK: true, State: &st}

	case "run":
		return d.startRun(req)

	case protocol.CommandCreate:
		return d.startSandbox(req)

	case protocol.CommandDestroy:
		return d.destroySandbox()

	case "stop":
		d.mu.Lock()
		ex := d.ex
		d.stopping = true
		d.mu.Unlock()
		if ex == nil {
			return Response{OK: false, Error: "nothing is running"}
		}
		ex.StopNow("kill")
		st := d.snapshot()
		return Response{OK: true, State: &st}

	case "shutdown":
		go d.requestShutdown()
		return Response{OK: true}

	case "ping":
		return Response{OK: true}

	// THE INTERACTIVE SURFACE. Each one needs a RUNNING machine, and each answers "there is no
	// machine" rather than failing obscurely - a browser that clicks before a machine exists should
	// be told that, not shown a QMP connection error.
	case protocol.CommandScreenshot:
		return d.screenshot()
	// A CLICK WITH NO COORDINATES IS REFUSED BY NAME, NOT PERFORMED AT (0, 0).
	//
	// MEASURED: `POST /api/click {}` - or a body whose keys were misspelled - used to decode to the
	// zero value, click the top-left corner and answer `{"ok":true}`. A caller had no way to tell
	// that from a real click, and the walkthrough it was driving failed later on a widget that had
	// never been touched.
	case protocol.CommandClick:
		x, y, err := xyOf("click", req)
		if err != nil {
			return Response{OK: false, Error: err.Error()}
		}
		// THROUGH THE DRIVER'S OWN METHOD, NOT A QMP CALL, so the click is AUDITED. The driver is the
		// one place a pointer press is recorded; a `withQMP` call here would send the event and leave
		// no trace of it, which is how a person's own clicks came to be missing from the record of
		// their own run.
		return d.withDriver("click", func(drv *Driver) error { return drv.Click(x, y) })
	case protocol.CommandDouble:
		x, y, err := xyOf("double-click", req)
		if err != nil {
			return Response{OK: false, Error: err.Error()}
		}
		return d.withDriver("double-click", func(drv *Driver) error { return drv.DoubleClick(x, y) })
	case protocol.CommandKey:
		if req.Key == "" {
			return Response{OK: false, Error: "`key` needs a key, for example ret or ctrl+alt+t"}
		}
		strokes, err := keys.ParseCombo(req.Key)
		if err != nil {
			return Response{OK: false, Error: err.Error()}
		}
		// AND THE SAME RULE FOR A KEY: `drv.Stroke` is where a keystroke is written down.
		return d.withDriver("key", func(drv *Driver) error {
			for _, stroke := range strokes {
				if err := drv.Stroke(stroke.Modifiers, stroke.Key); err != nil {
					return err
				}
			}
			return nil
		})
	case protocol.CommandType:
		if req.Text == "" {
			return Response{OK: false, Error: "`type` needs `text`"}
		}
		// THE PACE IS THE FILE'S, NOT THIS CALLER'S. A typed line in this repository has needed 0.15s
		// per character since a `$` arrived as nothing on a Windows guest - see internal/keys. A
		// browser that typed faster would lose characters on exactly the guests that made that
		// number what it is, and the loss is invisible: the field simply ends up missing a character.
		// A CHARACTER THE GUEST CANNOT TYPE IS A REFUSAL, NOT A SUCCESSFUL TYPE.
		//
		// `Type` returns the runes it could not send, and this call discarded them with `_`. MEASURED
		// SHAPE FROM THE SAME FIELD in the walkthrough path: a `$` arrived as nothing on a Windows
		// guest and the step reported success, so the account had a password one character shorter
		// than the file said and the next stage could not log in - with nothing in the log about it.
		//
		// THE MESSAGE NAMES THE RUNES, because "typing failed" sends a reader to the keyboard layout
		// and "$" sends them to the character.
		return d.withDriver("type", func(drv *Driver) error {
			unsupported, err := drv.Type(req.Text, 150*time.Millisecond)
			if err != nil {
				return err
			}
			if len(unsupported) > 0 {
				return fmt.Errorf("this guest cannot type %q: it is not in the qcode table, so %d "+
					"of the %d character(s) never arrived", string(unsupported), len(unsupported),
					len([]rune(req.Text)))
			}
			return nil
		})
	}
	return Response{OK: false, Error: fmt.Sprintf("%q is not a command; use run, create, status, "+
		"stop, destroy, shutdown, ping, screenshot, click, double, key or type", req.Command)}
}

// startRun begins a walkthrough in the background and returns at once.
//
// IT DOES NOT WAIT. A `run` that blocked until the walkthrough finished would make the client a
// supervisor and the daemon unreachable for the whole run - which defeats the reason it is a
// daemon. The client that needs the verdict polls `status`.
func (d *Daemon) startRun(req Request) Response {
	if req.File == "" {
		return Response{OK: false, Error: "`run` needs a walkthrough file"}
	}

	d.mu.Lock()
	// A RUN IN PROGRESS IS A REFUSAL, AND IT IS THE RUN RATHER THAN THE MACHINE THAT SAYS SO.
	//
	// The two are different facts: a machine can be up with no run (a `create` sandbox), and a run can
	// be preparing a disk with no machine yet. This is the only place that asks about the RUN.
	if d.ex != nil {
		st := d.st
		slot := d.machine
		alive := true
		if slot == nil || slot.driver == nil {
			// A RUN THAT HAS MADE NO MACHINE YET IS EITHER STILL PREPARING OR ABANDONED, AND TIME TELLS
			// THEM APART. AN HOUR is longer than any preparation here - the longest is an overlay
			// create plus an ISO copy, minutes - and shorter than a real install.
			alive = time.Since(st.Started) < time.Hour
		} else if m, ok := slot.driver.Machine().(interface{ IsRunning() bool }); ok {
			alive = m.IsRunning()
		}
		d.mu.Unlock()
		if alive {
			return Response{OK: false,
				Error: fmt.Sprintf("a run is already in progress (step %q); stop it first", st.Step),
				State: &st}
		}
		// THE RUN IS DEAD: its machine exited and `stop` cannot end a goroutine blocked on a guest that
		// will never answer. Taking the slot back is what un-wedges the daemon, which otherwise
		// answers `a run is already in progress` to every later leg for the life of the process.
		//
		// The abandoned goroutine is left to fail on its own read; its own `d.ex = nil` is guarded by
		// the same lock and cannot resurrect it.
		d.mu.Lock()
		if d.ex != nil {
			d.ex = nil
			slot2 := d.machine
			d.machine = nil
			d.st.Status = "failed"
			d.st.Error = "the machine went away while a step was running; the run was abandoned"
			d.stopping = false
			d.mu.Unlock()
			if slot2 != nil && slot2.close != nil {
				// THE DEAD MACHINE IS STILL CLOSED, or its qemu's file descriptors and the tap outlive
				// it - the abandoned goroutine will not do it, because it is blocked on the read that
				// made the machine look dead in the first place.
				slot2.close()
			}
		} else {
			d.mu.Unlock()
		}
		d.mu.Lock()
	}

	// A NEW RUN CLEARS THE STOP FLAG, AND NOT CLEARING IT COST TWO LEGS.
	//
	// `stop` sets `d.stopping` so the executor returns at the next step boundary. NOTHING SET IT BACK,
	// so the flag stayed set for the LIFE OF THE DAEMON - and every later `run` was refused by its own
	// first step:
	//
	//	=== finished rc=0
	//	status: finished step 0/31 ""
	//	/dsk/os/macos/run-setup/macos15-setup.mp4   0 MiB
	//
	// A leg that "finished" without running a step, and a video of nothing.
	d.stopping = false
	// THE DAEMON'S OWN DIRECTORY WINS, ALWAYS. A client that named a different one would put the
	// frames in one place and the disk in another.
	work := d.work
	if work == "" {
		work = filepath.Dir(req.File)
	}
	// THE SLOT IS RESERVED HERE, BEFORE THE GOROUTINE STARTS, so a second `run` or `create` arriving a
	// microsecond later is refused by a claim that already exists rather than by a race it might win.
	if err := d.reserveLocked(ownerRun); err != nil {
		d.mu.Unlock()
		return Response{OK: false, Error: err.Error()}
	}
	d.st = State{
		Status:  "starting",
		File:    req.File,
		Started: time.Now(),
	}
	done := make(chan struct{})
	d.done = done
	d.mu.Unlock()

	go d.runWalkthrough(req, work, done)

	st := d.snapshot()
	return Response{OK: true, State: &st}
}

// snapshot copies the state under the lock.
func (d *Daemon) snapshot() State {
	d.mu.Lock()
	st := d.st
	d.mu.Unlock()

	// THE CONSOLE HAS NO PORT OF ITS OWN ANY MORE. The page, its assets and the WebSocket are served
	// by the daemon's listener on :8080, so a person opens the Service URL - there is no second port to
	// report, and no URL that can point at something that is not listening.
	st = d.withMeasuredFacts(st)
	return st
}

// withMeasuredFacts adds the things a step may WAIT ON, measured now.
//
// THEY ARE MEASURED RATHER THAN REMEMBERED because every one of them is a question about the world
// that changes without anybody telling this struct: a guest agent starts answering, a disk stops
// growing, a console goes quiet. A cached boolean would be right at the moment it was set and wrong
// afterwards, which is exactly the failure mode `share_mount:` had.
//
// EACH ONE IS CHEAP AND BOUNDED. Nothing here waits: `wait_until:` polls this snapshot, so a
// measurement that blocked would make the wait unable to see its own condition.
func (d *Daemon) withMeasuredFacts(st State) State {
	// THE PROBED FACTS ARE REPORTED WHATEVER THE MACHINE'S STATE IS. They are the last observation,
	// and `probeOnce` CLEARS them the moment there is no machine - so a stopped guest shows nil
	// rather than the previous one's success, which is what keeps `wait_until: ssh` honest.
	st.SSHReady = d.lastProbe.sshAnswer()
	st.QGAReady = d.lastProbe.qgaAnswer()

	slot := d.machine
	if slot == nil || slot.driver == nil {
		// NO MACHINE IS NOT "EVERYTHING IS FALSE". A nil pointer says "not applicable", which is
		// what lets `wait_until: ssh` refuse a file that never booted instead of waiting 30 minutes
		// for a port that could never open.
		return st
	}
	drv := slot.driver

	// --- qemu -----------------------------------------------------------------------------
	phase := "absent"
	running := false
	pid := 0
	if m := drv.Machine(); m != nil {
		running = m.IsRunning()
		if running {
			phase = "running"
		} else {
			phase = "stopping"
		}
	}
	st.QEMU = &protocol.QEMUState{Running: running, PID: pid, Phase: phase}

	// --- the console ----------------------------------------------------------------------
	path := run.ConsolePath(d.work)
	var size int64
	var idle float64
	if fi, err := os.Stat(path); err == nil {
		size = fi.Size()
		idle = time.Since(fi.ModTime()).Seconds()
	}
	st.Console = &protocol.ConsoleState{Path: path, Bytes: size, IdleSeconds: idle}

	// --- the disk -------------------------------------------------------------------------
	if m := drv.Machine(); m != nil {
		if dsk, ok := m.(interface{ Disk() string }); ok {
			if p := dsk.Disk(); p != "" {
				mb := int64(0)
				if fi, err := os.Stat(p); err == nil {
					mb = fi.Size() / (1 << 20)
				}
				st.Disk = &protocol.DiskState{Path: p, AllocatedMB: mb}
			}
		}
	}

	// --- ssh and qga, FROM A BACKGROUND PROBE AND NEVER FROM THIS CALL --------------------
	//
	// MEASURED: PROBING HERE BROKE `/api/state` ON A REAL RUN. `log-stream` polls it every ten
	// seconds, and a probe that waited - a TCP dial that could take two seconds, a guest-agent
	// handshake that could take sixty - made every request slower than the poller's own timeout:
	//
	//	FAILED: GET http://127.0.0.1:8080/api/state: context deadline exceeded
	//
	// repeated for the whole install, so the run could not be watched at all.
	//
	// A SNAPSHOT MUST BE INSTANT. What it reports is a FACT SOMEBODY ELSE LAST OBSERVED, with the age
	// of that observation beside it - `probeTicker` refreshes it, and a caller that needs the current
	// answer is a `wait_until:` step, which polls this same state.
	// --- recording -----------------------------------------------------------------------
	recording := false
	if rec, ok := drv.(interface{ Recording() bool }); ok {
		recording = rec.Recording()
	}
	st.Recording = &recording

	// --- the share -----------------------------------------------------------------------
	//
	// IT IS ALWAYS ON NOW, so this is a fact about the DIRECTORY rather than about a server: a share
	// whose directory does not exist answers 503, and a step that waits for it would wait forever.
	shareUp := false
	if sv, ok := drv.(interface{ ShareDir() string }); ok {
		shareUp = sv.ShareDir() != ""
	}
	st.ShareReady = &shareUp

	// --- the last frame, and the guest phase it implies ------------------------------------
	//
	// A GUEST IS `up` WHEN THE FRAMEBUFFER HAS SETTLED AT A DESKTOP SIZE. The console is not enough:
	// a guest with `-serial null` writes nothing and one that is installing writes constantly, so the
	// console alone cannot tell "still booting" from "working". The frame size is the boot's actual
	// milestone - the firmware's mode is small and the desktop's is not.
	booted := "booting"
	if f, err := drv.Frame(); err == nil {
		w, h := f.Size()
		age := 0.0
		if fi, err := os.Stat(f.Path()); err == nil {
			age = time.Since(fi.ModTime()).Seconds()
		}
		st.Frame = &protocol.FrameState{Width: w, Height: h, AgeSeconds: age}
		if w >= 800 && h >= 600 {
			booted = "up"
		}
	}
	st.Guest = &protocol.GuestState{Phase: booted, Since: 0}

	return st
}

// errors callers may compare against.
var (
	// ErrNoDaemon is what a client returns when nothing answers on the socket.
	ErrNoDaemon = errors.New("no daemon is listening")
	// ErrBusy is what a client returns when the daemon refused because it was working.
	ErrBusy = errors.New("the daemon is busy")
)

// withQMP runs one QMP operation against the machine this daemon holds.
//
// IT IS A FUNCTION RATHER THAN SIX COPIES OF THE SAME GUARD, and the guard is the point: every
// interactive command needs the same three answers - is a machine up, did the operation succeed, and
// what does the state look like now. A browser polling `status` while clicking must not be told a
// click worked when there was no machine to click.
func (d *Daemon) withQMP(what string, op func(*qmp.Conn) error) Response {
	// THE COMMAND GOES THROUGH THE DRIVER'S REPAIR, NOT THROUGH A CONNECTION TAKEN AND USED ONCE.
	//
	// MEASURED on the pod: `api/state` reported a frame captured 0.2 seconds earlier - the recorder's
	// sampler was working - while `POST /api/command {"command":"key"}` answered `the qmp connection
	// is closed`, every time. Both use the same `*Driver` and the same one connection; the sampler
	// recovers through `qmpRetry` and this path did not, because `interactiveConn` handed out the
	// cached connection and the command ran on it exactly once.
	err := d.withMachine(func(drv run.Driver) error {
		concrete, ok := drv.(*Driver)
		if !ok {
			return fmt.Errorf("this driver does not expose QMP")
		}
		if concrete.audit != nil {
			concrete.audit.setOrigin(journal.OriginAPI)
			defer concrete.audit.setOrigin(journal.OriginWalk)
		}
		return concrete.QMPCommand(op)
	})
	if err != nil {
		return Response{OK: false, Error: fmt.Sprintf("%s: %v", what, err)}
	}
	st := d.snapshot()
	return Response{OK: true, State: &st}
}

// withDriver is `withQMP` for the operations that need the DRIVER rather than the connection -
// `type` paces its keystrokes, and that loop lives on the driver.
//
// IT MARKS THE AUDIT AS `api` FOR THE DURATION, and that is what makes a person's own clicks
// distinguishable from the file's. The driver's methods are the ONE place a keystroke is recorded, so
// an HTTP click that went straight to QMP was not recorded at all - MEASURED: before this, a run's
// journal held the walkthrough's 7 clicks and none of the operator's.
func (d *Daemon) withDriver(what string, op func(*Driver) error) Response {
	runErr := d.withMachine(func(drv run.Driver) error {
		concrete, ok := drv.(*Driver)
		if !ok {
			return fmt.Errorf("this driver cannot type")
		}
		if concrete.audit != nil {
			concrete.audit.setOrigin(journal.OriginAPI)
			defer concrete.audit.setOrigin(journal.OriginWalk)
		}
		return op(concrete)
	})
	if runErr != nil {
		return Response{OK: false, Error: fmt.Sprintf("%s: %v", what, runErr)}
	}
	st := d.snapshot()
	return Response{OK: true, State: &st}
}

// withMachine runs fn against the machine this daemon holds, and it is the ONLY way a caller reaches
// one. The alternative - each caller taking `d.mu`, reading `d.machine`, and asserting it is non-nil -
// is how a nil machine got dereferenced in three places.
//
// A CALLER THAT ARRIVES WHILE THE MACHINE IS BEING TORN DOWN WAITS, rather than being told there is no
// machine while one is still writing its disk. `destroy` holds the slot until the teardown has
// finished, so "the slot is empty" always means "the previous machine is completely gone".
func (d *Daemon) withMachine(fn func(run.Driver) error) error {
	d.mu.Lock()
	slot := d.machine
	d.mu.Unlock()
	// A SLOT THAT IS STARTING HAS NO DRIVER YET AND A STOPPING ONE IS BEING TORN DOWN, so neither is
	// something a step may act through. Both answer the same way as "no machine", because the caller
	// asked to touch a guest and there is none to touch.
	if slot == nil || slot.phase != slotRunning {
		return ErrNoMachine
	}
	return fn(slot.driver)
}

// ErrNoMachine is what a caller gets when this daemon holds no guest.
var ErrNoMachine = errors.New("no machine is running; a `create` or a `run` with a `boot:` step " +
	"starts one")

func (d *Daemon) interactiveConn() (*qmp.Conn, error) {
	// THE DRIVER OWNS THE SINGLE QMP CONNECTION, and qemu serves one client per socket - so the
	// interactive surface goes through it rather than opening a second.
	var conn *qmp.Conn
	err := d.withMachine(func(drv run.Driver) error {
		concrete, ok := drv.(*Driver)
		if !ok {
			return fmt.Errorf("this driver does not expose QMP")
		}
		c, err := concrete.QMP()
		if err != nil {
			return err
		}
		conn = c
		return nil
	})
	return conn, err
}

// screenshot writes one frame and answers where it is.
//
// THE FILE IS ALREADY PNG, so the path is handed on and nothing is converted: qemu's `screendump`
// writes the same format a browser draws, and the daemon keeps the frame it captured for a
// post-mortem instead of a re-encoding of it.
func (d *Daemon) screenshot() Response {
	conn, err := d.interactiveConn()
	if err != nil {
		return Response{OK: false, Error: err.Error()}
	}
	dir := filepath.Join(d.work, "live")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Response{OK: false, Error: err.Error()}
	}
	f, err := conn.Screendump(dir)
	if err != nil {
		return Response{OK: false, Error: fmt.Sprintf("screenshot: %v", err)}
	}
	st := d.snapshot()
	return Response{OK: true, State: &st,
		Screen: &Screen{Path: f.Path, Width: f.Width, Height: f.Height}}
}

// Facts reports the measurements a `wait_until:` step polls, as strings.
//
// IT IS THE SAME MEASUREMENT `/api/state` SERVES, not a second implementation: the snapshot is built
// first and this reads it. Two sources for "is sshd up" is how a step comes to wait on one answer
// while the log shows another.
//
// A MISSING FACT IS ABSENT FROM THE MAP rather than empty, so a condition that needs it can say so
// instead of comparing "" to a number.
func (d *Daemon) Facts() (map[string]string, error) {
	st := d.snapshot()
	out := map[string]string{}
	if st.QEMU != nil {
		out["qemu_running"] = strconv.FormatBool(st.QEMU.Running)
		out["qemu_phase"] = st.QEMU.Phase
	}
	if st.SSHReady != nil {
		out["ssh_ready"] = strconv.FormatBool(*st.SSHReady)
	}
	if st.QGAReady != nil {
		out["qga_ready"] = strconv.FormatBool(*st.QGAReady)
	}
	if st.Console != nil {
		out["console_bytes"] = strconv.FormatInt(st.Console.Bytes, 10)
		out["console_idle"] = fmt.Sprintf("%.1f", st.Console.IdleSeconds)
	}
	if st.Disk != nil {
		out["disk_allocated_mb"] = strconv.FormatInt(st.Disk.AllocatedMB, 10)
		out["disk_grow_rate"] = fmt.Sprintf("%.2f", st.Disk.GrowRateMBps)
	}
	if st.Recording != nil {
		out["recording"] = strconv.FormatBool(*st.Recording)
	}
	if st.ShareReady != nil {
		out["share_ready"] = strconv.FormatBool(*st.ShareReady)
	}
	if st.Frame != nil {
		out["frame_age"] = fmt.Sprintf("%.1f", st.Frame.AgeSeconds)
		out["frame_size"] = fmt.Sprintf("%dx%d", st.Frame.Width, st.Frame.Height)
	}
	if st.Guest != nil {
		out["guest_phase"] = st.Guest.Phase
	}
	return out, nil
}

// fileExistsDaemon is one stat, named so the snapshot reads as a question.
func fileExistsDaemon(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// currentStep is the step a picture or a call belongs to, for the audit and the library.
func (d *Daemon) currentStep() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.st.Step
}

// currentStepIndex places that step.
func (d *Daemon) currentStepIndex() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.st.StepIndex
}
