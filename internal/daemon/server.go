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
	"os"
	"os/signal"
	"path/filepath"
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
	mu sync.Mutex
	// st is the current state, guarded by mu.
	st State
	// ex is the executor of the run in progress, nil when idle.
	ex *run.Executor
	// done is closed when the current run finishes.
	done chan struct{}
	// stopping asks the run to stop at the next step boundary.
	stopping bool
	// listener is the unix socket.
	listener interface{ Close() error }
	// shutdown asks Serve to return.
	shutdown chan struct{}
	// novncPort is the page's port for runs this daemon starts.
	// work is this daemon's directory: its socket, its frames and its disks.
	work string
	// sandbox says a `create` is holding a machine that must outlive its steps, and sandboxClose is
	// what `destroy` calls. IT IS SEPARATE FROM ex BECAUSE THEY MEAN DIFFERENT THINGS: ex is a
	// walkthrough in progress, which ends by itself; a sandbox has no end except `destroy`.
	sandbox      bool
	sandboxClose func()
	// machine is the driver this daemon HOLDS, and it outlives every walkthrough.
	//
	// THE DAEMON WAS ONE FILE PER MACHINE: `runWalkthrough` called `NewDriver` and deferred its
	// closer, so each YAML got a fresh driver and therefore a fresh qemu. That is a bake.
	//
	// A SANDBOX IS THE OTHER WAY ROUND - THE MACHINE IS THE LONG-LIVED THING AND A YAML IS A SET OF
	// ACTIONS ON IT. `init.yaml` installs, `dev.yaml` adds a toolchain, `test.yaml` runs the tests, and
	// all three act on ONE machine that was booted once. So the driver is created on the first file
	// that needs one and kept until `destroy`.
	machine      run.Driver
	machineClose func()
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
	// A SANDBOX IS STOPPED HERE TOO. Its machine is not part of any run loop, so a container stop
	// that only waited for `ex` would leave qemu holding /dev/kvm with nothing able to reach it.
	sandboxClose := d.sandboxClose
	d.sandboxClose = nil
	d.sandbox = false
	d.mu.Unlock()

	if sandboxClose != nil {
		sandboxClose()
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
	case protocol.CommandClick:
		return d.withQMP("click", func(c *qmp.Conn) error { return c.Click(req.X, req.Y) })
	case protocol.CommandDouble:
		return d.withQMP("double-click", func(c *qmp.Conn) error { return c.DoubleClick(req.X, req.Y) })
	case protocol.CommandKey:
		if req.Key == "" {
			return Response{OK: false, Error: "`key` needs a key, for example ret or ctrl+alt+t"}
		}
		strokes, err := keys.ParseCombo(req.Key)
		if err != nil {
			return Response{OK: false, Error: err.Error()}
		}
		return d.withQMP("key", func(c *qmp.Conn) error {
			for _, stroke := range strokes {
				if err := c.Stroke(stroke.Modifiers, stroke.Key); err != nil {
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
		return d.withDriver("type", func(drv *Driver) error {
			_, err := drv.Type(req.Text, 150*time.Millisecond)
			return err
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
	d.mu.Lock()
	if d.ex != nil {
		st := d.st
		machine := d.machine
		d.mu.Unlock()

		// A RUN WHOSE MACHINE IS GONE IS NOT A RUN, AND `stop` CANNOT END IT.
		//
		// MEASURED: `easyvm stop` returned, `d.stopping` was set, qemu was killed by hand - and the leg
		// went on reporting
		//
		//	status: running step 22/25 "installation runs"
		//	REFUSED: a run is already in progress (step "installation runs"); stop it first
		//
		// for twenty more minutes. `ShouldStop` is only read BETWEEN steps, so a step blocked inside a
		// guest round trip never sees it, and the daemon has no way to end a run whose machine no longer
		// exists - so every later leg is refused and the pod is wedged.
		//
		// THE FIX IS TO NOTICE THE MACHINE IS GONE and take the slot back. The abandoned goroutine is
		// left to fail on its own read; its `d.ex = nil` assignment is guarded by the same lock and
		// cannot resurrect it.
		// A RUN THAT HAS NOT MADE A MACHINE YET IS STILL A RUN. `machine == nil` means the executor is
		// loading the file or preparing a disk - ordinary work - so the slot stays taken. Only a machine
		// that EXISTED and is now gone is evidence the run is dead.
		alive := true
		if machine == nil {
			alive = true
		} else if m, ok := machine.(interface{ IsRunning() bool }); ok {
			alive = m.IsRunning()
		}
		if !alive {
			d.mu.Lock()
			if d.ex != nil {
				d.ex = nil
				d.machine = nil
				d.st.Status = "failed"
				d.st.Error = "the machine went away while a step was running; the run was abandoned"
				d.stopping = false
			}
			d.mu.Unlock()
		} else {
			return Response{OK: false,
				Error: fmt.Sprintf("a run is already in progress (step %q); stop it first",
					st.Step),
				State: &st}
		}
		d.mu.Lock()
	}
	if req.File == "" {
		d.mu.Unlock()
		return Response{OK: false, Error: "`run` needs a walkthrough file"}
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
	// A leg that "finished" without running a step, and a video of nothing. The daemon is one process
	// for many legs, and a stop belongs to the run it stopped.
	d.stopping = false
	// THE DAEMON'S OWN DIRECTORY WINS, ALWAYS. A client that named a different one would put the
	// socket in one place and the frames in another, and the frames are what a person looks at when
	// a step fails. `req.Work` is accepted for a caller that started the daemon elsewhere and knows
	// it; when it is empty the daemon's own is used.
	work := d.work
	if work == "" {
		work = filepath.Dir(req.File)
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
	machine := d.machine
	d.mu.Unlock()

	// THE CONSOLE HAS NO PORT OF ITS OWN ANY MORE. The page, its assets and the WebSocket are served
	// by the daemon's listener on :8080, so a person opens the Service URL - there is no second port to
	// report, and no URL that can point at something that is not listening.
	_ = machine
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
	conn, err := d.interactiveConn()
	if err != nil {
		return Response{OK: false, Error: err.Error()}
	}
	if err := op(conn); err != nil {
		return Response{OK: false, Error: fmt.Sprintf("%s: %v", what, err)}
	}
	st := d.snapshot()
	return Response{OK: true, State: &st}
}

// withDriver is `withQMP` for the operations that need the DRIVER rather than the connection -
// `type` paces its keystrokes, and that loop lives on the driver.
func (d *Daemon) withDriver(what string, op func(*Driver) error) Response {
	d.mu.Lock()
	drv := d.machine
	d.mu.Unlock()
	if drv == nil {
		return Response{OK: false, Error: "no machine is running; a `create` or a `run` with a " +
			"`boot:` step starts one"}
	}
	concrete, ok := drv.(*Driver)
	if !ok {
		return Response{OK: false, Error: fmt.Sprintf("%s: this driver cannot type", what)}
	}
	if err := op(concrete); err != nil {
		return Response{OK: false, Error: fmt.Sprintf("%s: %v", what, err)}
	}
	st := d.snapshot()
	return Response{OK: true, State: &st}
}

// interactiveConn is the QMP connection to the machine this daemon holds.
func (d *Daemon) interactiveConn() (*qmp.Conn, error) {
	d.mu.Lock()
	drv := d.machine
	d.mu.Unlock()
	if drv == nil {
		return nil, fmt.Errorf("no machine is running; a `create` or a `run` with a `boot:` step " +
			"starts one")
	}
	// THE DRIVER OWNS THE SINGLE QMP CONNECTION, and qemu serves one client per socket - so the
	// interactive surface goes through it rather than opening a second.
	if concrete, ok := drv.(*Driver); ok {
		return concrete.QMP()
	}
	return nil, fmt.Errorf("this driver does not expose QMP")
}

// screenshot writes one frame and answers where it is.
//
// THE FILE IS A PPM AND THE HTTP LAYER CONVERTS IT. qemu's `screendump` writes P6 and that is the
// only format it produces; a browser needs PNG, and the conversion belongs where the bytes are
// served rather than in the daemon that also has to keep the raw frame for a post-mortem.
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
