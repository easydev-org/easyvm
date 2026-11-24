package daemon

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/easydev-org/easyvm/internal/qmp"
)

// qemuProcess is ONE qemu, from `Start` to reaped, and it owns every fact about that machine.
//
// # THE THREE TABLES THIS REPLACES
//
// Before this type the daemon kept three parallel answers to "is there a machine", and six defects
// were one pair of them disagreeing:
//
//	qemuProcess  谁拥有这个进程      the driver's `d.proc *exec.Cmd`
//	Registry     谁拥有这个 socket    a daemon-wide `map[string]*Conn`
//	qmpMu        谁拥有这次事务       a mutex around "release, then acquire"
//
// They were not a division of labour: they were three copies of one fact, maintained by discipline.
// A `Release` had to be remembered in seven places, the registry outlived every machine, and the
// mutex protected a boundary that a new call site could forget to respect. The measured symptoms
// were always the same shape - a daemon refusing its own next boot, or a destroy that reported
// success over a live process.
//
// # THE MODEL NOW
//
//	THE PROCESS OWNS THE SOCKET AND THE CONNECTION. They are born with it and die with it.
//
// So `Alive()` is the single answer to all three questions:
//
//	is the process running?   is the socket taken?   is the connection usable?
//
// and "the next boot was refused by its own daemon" cannot be expressed, because the path's owner
// IS the process and a dead process owns nothing.
//
// # WHY AN ACTOR AND NOT A MUTEX
//
// The QMP transport takes one command at a time - qemu has one client per socket - and two kinds of
// caller share it: the recorder's sampler, once a second, and whatever a step is doing. A mutex is
// correct and unfair, and the failure it produced was invisible:
//
//	the sampler took the lock, found a dead connection, and CLOSED the shared one while a `type:`
//	was halfway through a password: "input-send-event []+7: the qmp connection is closed"
//
// A channel makes the same exclusion FIFO and observable, and - the part that matters - it lets the
// owner close the queue on exit, so a late caller gets `ErrMachineGone` instead of blocking on a
// mutex nobody will release.
type qemuProcess struct {
	cmd     *exec.Cmd
	sock    string
	pointer bool // whether the guest has a tablet; set before the connection is used

	// reqs carries every QMP command. `serve` is the only reader.
	reqs chan qmpRequest

	// done is closed by `watch` after `Wait` reaps the process. IT IS THE PUBLICATION: a reader that
	// has `<-done` in a select has a happened-before edge on the reap.
	done chan struct{}

	mu       sync.Mutex
	conn     *qmp.Conn
	exitedAt time.Time
	waitErr  error
}

// qmpRequest is one command, run by the owner goroutine.
type qmpRequest struct {
	fn   func(*qmp.Conn) error
	done chan error
}

// errMachineGone is what a caller gets when the machine it asked is no longer there.
//
// IT REPLACES A MISLEADING ERROR. The old code answered `this qmp socket is already held by this
// process (by "easyvm")` for a machine that had exited, which reads as a conflict between two
// clients and sent every reader to the wrong place.
var errMachineGone = errors.New("the machine is no longer running")

// startQemu takes ownership of cmd, starts it, and serves its QMP connection until it exits.
//
// IT REGISTERS BEFORE ANYTHING CAN FAIL. The process exists the moment `Start` returns, so a caller
// that can still return an error afterwards must not be able to lose it - which is exactly the
// orphan `Boot` used to produce.
func startQemu(cmd *exec.Cmd, sock string, pointerIsAbsolute bool, connect func() (*qmp.Conn, error)) (*qemuProcess, error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &qemuProcess{
		cmd:     cmd,
		sock:    sock,
		pointer: pointerIsAbsolute,
		reqs:    make(chan qmpRequest),
		done:    make(chan struct{}),
	}
	go p.watch()
	go p.serve(connect)
	return p, nil
}

// watch is the ONLY `Wait` this process will ever get, and it owns the connection's lifetime.
//
// THE CONNECTION IS CLOSED HERE, NOT BY A CALLER. That is the whole reason the registry could be
// deleted: there is no longer a map that has to be told the socket is free, because the thing that
// holds the connection is the thing that reaps the process.
func (p *qemuProcess) watch() {
	err := p.cmd.Wait()
	p.mu.Lock()
	p.exitedAt = time.Now()
	p.waitErr = err
	conn := p.conn
	p.conn = nil
	p.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	close(p.done)
}

// Alive reports whether qemu is still running.
//
// IT IS A CHANNEL TEST AND NOT A FIELD, so there is no window in which a stale pointer says "running"
// about a process that has been reaped.
func (p *qemuProcess) Alive() bool {
	if p == nil {
		return false
	}
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// Done is the channel to select on, for a caller that wants to wait for the exit.
func (p *qemuProcess) Done() <-chan struct{} {
	if p == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return p.done
}

// Pid is the process id, for log lines. Zero once the process is gone.
func (p *qemuProcess) Pid() int {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

// Socket is the QMP path this machine owns, for `probeUnixSocket` and for messages.
func (p *qemuProcess) Socket() string {
	if p == nil {
		return ""
	}
	return p.sock
}

// Run executes one QMP command on the owning goroutine.
//
// A DEAD CONNECTION IS REPAIRED HERE AND NOWHERE ELSE, which is the second half of the simplification:
// the old `qmpRetry` had to reason about a cache and a registry that another caller could change
// underneath it. Here the connection is a field of the object whose goroutine is reading the request,
// so "closed" is a fact this function can act on without asking anyone.
func (p *qemuProcess) Run(fn func(*qmp.Conn) error) error {
	if p == nil {
		return errMachineGone
	}
	reply := make(chan error, 1)
	select {
	case p.reqs <- qmpRequest{fn: fn, done: reply}:
	case <-p.done:
		return errMachineGone
	}
	select {
	case err := <-reply:
		return err
	case <-p.done:
		// The machine exited while the command was in flight. The command's own error is more
		// specific when it has one, but a caller must never block here.
		select {
		case err := <-reply:
			return err
		default:
			return errMachineGone
		}
	}
}

// serve is the owner goroutine's loop: one command at a time, FIFO, until the process exits.
func (p *qemuProcess) serve(connect func() (*qmp.Conn, error)) {
	for {
		select {
		case req := <-p.reqs:
			req.done <- p.runOne(req.fn, connect)
		case <-p.done:
			// DRAIN WHATEVER ARRIVED BEFORE THE EXIT, so a queued command is answered rather than
			// abandoned. A caller waiting on `reply` would otherwise block for ever.
			for {
				select {
				case req := <-p.reqs:
					req.done <- errMachineGone
				default:
					return
				}
			}
		}
	}
}

// runOne takes the connection, commands it, and repairs it once if the transport says it is gone.
func (p *qemuProcess) runOne(fn func(*qmp.Conn) error, connect func() (*qmp.Conn, error)) error {
	conn, fresh, err := p.connection(connect)
	if err != nil {
		return err
	}
	_ = fresh
	err = fn(conn)
	if !errors.Is(err, qmp.ErrClosed) {
		return err
	}
	// ONE REPAIR ATTEMPT. `ErrClosed` is the transport saying the socket is gone - trustworthy in a
	// way a failed `query-status` was not, because it comes from the command the caller wanted.
	p.drop()
	conn2, _, err2 := p.connection(connect)
	if err2 != nil {
		return fmt.Errorf("%w (and it could not be reopened: %v)", qmp.ErrClosed, err2)
	}
	return fn(conn2)
}

// connection returns the cached connection, opening one the first time.
func (p *qemuProcess) connection(connect func() (*qmp.Conn, error)) (*qmp.Conn, bool, error) {
	p.mu.Lock()
	if p.conn != nil {
		c := p.conn
		p.mu.Unlock()
		return c, false, nil
	}
	p.mu.Unlock()

	// THE HANDSHAKE IS OUTSIDE THE LOCK: it can wait for a socket qemu has not created yet, and
	// holding a mutex across that wait is how `status` used to hang for thirty seconds.
	c, err := connect()
	if err != nil {
		return nil, false, err
	}
	c.SetAbsolutePointer(p.pointer)
	p.mu.Lock()
	// SOMEBODY ELSE MAY HAVE WON THE RACE - the boot path publishes its own connection. Keep the
	// first one and close the loser rather than leaking it.
	if p.conn != nil {
		have := p.conn
		p.mu.Unlock()
		_ = c.Close()
		return have, false, nil
	}
	p.conn = c
	p.mu.Unlock()
	return c, true, nil
}

// drop forgets the cached connection, so the next command opens a fresh one.
//
// IT DOES NOT NEED A REGISTRY ENTRY TO CLEAR. The old code had to `Release` a path from a map that
// outlived the machine; here the connection is this object's field and the next `connection` call
// simply dials again.
func (p *qemuProcess) drop() {
	p.mu.Lock()
	c := p.conn
	p.conn = nil
	p.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

// SetConnection publishes a connection the boot path already made.
//
// THE BOOT OWNS THE HANDSHAKE, because it is the thing that knows the machine came up: it waits for
// the socket, and a failure there is a boot failure with qemu's own output attached. This hands that
// connection to the owner goroutine rather than opening a second one - which is what the registry
// used to enforce, at the cost of a global map.
func (p *qemuProcess) SetConnection(c *qmp.Conn) {
	p.mu.Lock()
	old := p.conn
	p.conn = c
	p.mu.Unlock()
	if old != nil && old != c {
		_ = old.Close()
	}
}

// Connection is the cached connection, for a caller that must not go through the queue (the boot's
// own wait, and `QMP()` for a test).
func (p *qemuProcess) Connection() *qmp.Conn {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.conn
}

// Wait waits up to `limit` for qemu to exit of its own accord.
//
// IT NEVER CALLS `Wait` ITSELF - `watch` owns that - so "the second Wait blocks for ever" cannot
// recur. It does NOT kill on expiry: killing is the caller's decision.
func (p *qemuProcess) Wait(limit time.Duration) bool {
	if p == nil {
		return true
	}
	if limit <= 0 {
		<-p.done
		return true
	}
	t := time.NewTimer(limit)
	defer t.Stop()
	select {
	case <-p.done:
		return true
	case <-t.C:
		return false
	}
}

// Kill sends SIGKILL and returns only once the process has been reaped.
//
// "ONLY ONCE REAPED" IS THE CONTRACT. A caller that killed a machine and is about to start another on
// the same disk needs the file lock released first, and the lock is released when the process is
// gone - not when the signal was delivered.
func (p *qemuProcess) Kill() bool {
	if p == nil || !p.Alive() {
		return false
	}
	_ = p.cmd.Process.Kill()
	<-p.done
	return true
}

// Terminate sends SIGTERM and waits up to `limit` for the guest to exit on its own.
//
// IT IS THE GRACEFUL HALF of a stop: qemu flushes the disk when it receives this, and a machine that
// answers it needs no journal replay on its next boot.
func (p *qemuProcess) Terminate(limit time.Duration) bool {
	if p == nil || !p.Alive() {
		return true
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	return p.Wait(limit)
}

// Exit describes how the process ended, for a message about a machine that never came up.
func (p *qemuProcess) Exit() (time.Time, error) {
	if p == nil {
		return time.Time{}, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exitedAt, p.waitErr
}