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

	"github.com/easydev-org/easyvm/internal/protocol"
	"github.com/easydev-org/easyvm/internal/run"
)

// The wire types live in internal/protocol so the client can import them without importing this
// package. These aliases keep the daemon's own code readable.
type State = protocol.State
type Request = protocol.Request
type Response = protocol.Response

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
	novncPort int
	// work is this daemon's directory: its socket, its frames and its disks.
	work string
	// sandbox says a `create` is holding a machine that must outlive its steps, and sandboxClose is
	// what `destroy` calls. IT IS SEPARATE FROM ex BECAUSE THEY MEAN DIFFERENT THINGS: ex is a
	// walkthrough in progress, which ends by itself; a sandbox has no end except `destroy`.
	sandbox      bool
	sandboxClose func()
}

// New returns an idle daemon.
func New() *Daemon {
	return &Daemon{
		st:       State{Status: "idle"},
		shutdown: make(chan struct{}),
	}
}

// Addr is the socket path this daemon serves on its work directory.
type ServeOptions struct {
	// Work is where the socket and the frames live.
	Work string
	// NoVNCPort is where the browser page listens; 0 disables it. It is the DAEMON'S flag rather
	// than a per-run one because the port belongs to the host the daemon runs on, not to the file.
	// The zero value means DefaultNovNCPort, so a caller that does not care gets the convention.
	NoVNCPort int
	// Ready is called once the socket is listening, for a caller that must wait. A supervisor
	// watches for this line instead of polling for the socket file, because a file that exists is
	// not a daemon that answers.
	Ready func(socketPath string)
}

// Serve listens until Shutdown, handling one request at a time.
//
// ONE AT A TIME IS THE POINT. The machine has one state, so two concurrent requests that both
// changed it would need locking that the step executor does not have - and a `run` while a `run` is
// in progress is a question with no good answer. A second request while busy is refused with the
// state, which tells the caller what is actually happening.
func (d *Daemon) Serve(opts ServeOptions) error {
	if opts.Work == "" {
		return fmt.Errorf("a work directory is required; the socket lives in it")
	}
	if err := os.MkdirAll(opts.Work, 0o755); err != nil {
		return err
	}
	sock := SocketPath(opts.Work)

	// A STALE SOCKET IS THE NORMAL CASE AFTER A CRASH, and refusing to start because of one is a
	// daemon that cannot be restarted. The file is removed only when nothing answers on it, so a
	// live daemon is still protected.
	if _, err := os.Stat(sock); err == nil {
		if !socketAnswers(sock) {
			_ = os.Remove(sock)
		} else {
			return fmt.Errorf("a daemon is already listening on %s; stop it rather than "+
				"starting a second, because two owners of one machine is the thing this "+
				"package exists to prevent", sock)
		}
	}

	ln, err := listenUnix(sock)
	if err != nil {
		return err
	}
	d.listener = ln
	d.work = opts.Work
	d.novncPort = opts.NoVNCPort
	if opts.NoVNCPort < 0 {
		d.novncPort = 0
	}
	if opts.Ready != nil {
		opts.Ready(sock)
	}

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

	return d.acceptLoop(ln)
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
	}
	return Response{OK: false, Error: fmt.Sprintf("%q is not a command; use run, create, status, "+
		"stop, destroy, shutdown or ping", req.Command)}
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
		d.mu.Unlock()
		return Response{OK: false,
			Error: fmt.Sprintf("a run is already in progress (step %q); stop it first",
				st.Step),
			State: &st}
	}
	if req.File == "" {
		d.mu.Unlock()
		return Response{OK: false, Error: "`run` needs a walkthrough file"}
	}
	// THE DAEMON'S OWN DIRECTORY WINS, ALWAYS. A client that named a different one would put the
	// socket in one place and the frames in another, and the frames are what a person looks at when
	// a step fails. `req.Work` is accepted for a caller that started the daemon elsewhere and knows
	// it; when it is empty the daemon's own is used.
	work := d.work
	if work == "" {
		work = filepath.Dir(req.File)
	}
	if req.NoVNCPort == 0 {
		req.NoVNCPort = d.novncPort
	}
	d.st = State{
		Status:    "starting",
		File:      req.File,
		Started:   time.Now(),
		NoVNCPort: req.NoVNCPort,
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
	defer d.mu.Unlock()
	return d.st
}

// errors callers may compare against.
var (
	// ErrNoDaemon is what a client returns when nothing answers on the socket.
	ErrNoDaemon = errors.New("no daemon is listening")
	// ErrBusy is what a client returns when the daemon refused because it was working.
	ErrBusy = errors.New("the daemon is busy")
)
