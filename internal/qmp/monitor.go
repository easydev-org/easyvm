package qmp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	qemump "github.com/digitalocean/go-qemu/qmp"
)

// line is the transport, and it is ~120 lines instead of the library's SocketMonitor ON PURPOSE.
//
// # WHY NOT `qemump.SocketMonitor`
//
// Its shape is a background listener goroutine reading a socket into an UNBUFFERED channel, and a
// `Run` that sends a command and then waits on that channel:
//
//	func (mon *SocketMonitor) listen(r io.Reader, ...) {
//		for scanner.Scan() { stream <- streamResponse{buf} }   // hands replies to whoever is waiting
//		defer close(stream)
//	}
//	func (mon *SocketMonitor) RunWithFile(cmd []byte, ...) {
//		mon.mu.Lock(); defer mon.mu.Unlock()
//		mon.c.Write(cmd)
//		res := <-mon.stream                                   // and waits, holding mon.mu
//	}
//	func (mon *SocketMonitor) Disconnect() error {
//		err := mon.c.Close()
//		if mon.stream != nil { for range mon.stream {} }      // and waits for the listener
//	}
//
// THREE SEPARATE FAILURES COME OUT OF THAT SHAPE, and this repository hit two of them:
//
//  1. A CLOSE WHILE A COMMAND IS IN FLIGHT CANNOT BE BOUNDED. `Disconnect` waits for the listener to
//     close its channel, and the listener can be blocked handing a reply to a `Run` that is blocked
//     waiting for it - so the close waits on a cycle the library does not break. MEASURED as a
//     `destroy` that never returned, with eight goroutines parked in `sync.Mutex.Lock`.
//
//  2. A COMMAND HAS NO DEADLINE. `Run` waits on an unbuffered channel with no timeout, so a qemu that
//     never answers parks the caller forever. MEASURED as `boot the recovery image` hanging for SEVEN
//     MINUTES - past the step's own `timeout:` - on a connection qemu was never going to greet.
//
//  3. `mon.mu` IS HELD ACROSS THE WAIT, so the next command queues behind a command that may never
//     return. The lock is inside the library, so no caller can bound it.
//
// # WHAT THIS IS INSTEAD
//
// ONE COMMAND AT A TIME, WITH A DEADLINE, ON A SOCKET WHOSE CLOSE IS UNCONDITIONAL:
//
//	Run    takes the lock, writes the command, reads ONE line, releases - with a read deadline
//	Close  sets `closed` and closes the file descriptor. No goroutine, nothing to drain.
//
// There is no background reader, so there is no third party to wait for and no channel to unblock. A
// read that is in progress when `Close` lands returns immediately because the descriptor is gone, and
// that is the whole of the cancellation story.
//
// IT STILL IMPLEMENTS `qemump.Monitor`, so `raw.Monitor` - 14,935 generated lines of union types and
// the 200-name key table - is unchanged. Only the transport is ours.
type line struct {
	nc net.Conn

	// mu serialises the CONVERSATION: one command written, one reply read. It is never held across
	// anything but the socket itself.
	mu sync.Mutex
	// closed is ATOMIC RATHER THAN GUARDED BY `mu`, because `Close` must not wait for `mu`: a command
	// blocked in a read holds it, and waiting there is the hang this type exists to remove.
	closed int32
	// once makes the close idempotent without a lock around it.
	once sync.Once
	err  error
	// version and caps are what the greeting said, kept because `raw` asks for the capabilities.
	version Version
	caps    []string
}

// Version is the emulator's self-description, from the greeting.
type Version struct {
	Major, Minor, Micro int
	Package             string
}

// commandTimeout bounds ONE command.
//
// A SCREENDDUMP ON A BUSY GUEST IS SECONDS, and a `migrate` is minutes - but `migrate` only STARTS a
// migration, and the waiting is `query-migrate` polling by the caller. So the longest legitimate single
// command here is a screendump, and thirty seconds is the same number `Screendump` already waits for
// its file. Anything slower is a machine that has stopped answering, and saying so is better than
// parking the caller and every command behind it.
const commandTimeout = 30 * time.Second

// dialLine connects and completes the QMP greeting and capability handshake.
//
// THE HANDSHAKE HAS A DEADLINE, and that deadline is the difference between a seven-minute hang and an
// error. qemu accepts exactly one QMP client; a second is queued in its listen backlog and NEVER
// GREETED, so a read with no deadline waits forever. With one, it fails in ten seconds and the caller
// can say "another client holds this socket", which is the actual problem.
func dialLine(path string, connectWait, handshake time.Duration) (*line, error) {
	nc, err := net.DialTimeout("unix", path, connectWait)
	if err != nil {
		return nil, err
	}
	l := &line{nc: nc}
	if err := l.handshake(handshake); err != nil {
		_ = nc.Close()
		return nil, err
	}
	return l, nil
}

func (l *line) handshake(limit time.Duration) error {
	_ = l.nc.SetDeadline(time.Now().Add(limit))
	dec := json.NewDecoder(l.nc)

	// THE GREETING IS THE FIRST LINE AND ARRIVES UNPROMPTED. If it does not, this socket is either not
	// qemu's or held by another client.
	var greeting struct {
		QMP *struct {
			Version struct {
				QEMU struct {
					Major   int    `json:"major"`
					Minor   int    `json:"minor"`
					Micro   int    `json:"micro"`
					Package string `json:"package"`
				} `json:"qemu"`
			} `json:"version"`
			Capabilities []string `json:"capabilities"`
		} `json:"QMP"`
	}
	if err := dec.Decode(&greeting); err != nil {
		return fmt.Errorf("reading the qmp greeting: %w", err)
	}
	if greeting.QMP == nil {
		return fmt.Errorf("the first line on this socket is not a qmp greeting")
	}
	q := greeting.QMP.Version.QEMU
	l.version = Version{Major: q.Major, Minor: q.Minor, Micro: q.Micro, Package: q.Package}
	l.caps = greeting.QMP.Capabilities

	// AND THE CAPABILITIES HAVE TO BE NEGOTIATED before any other command is allowed.
	if err := json.NewEncoder(l.nc).Encode(map[string]any{"execute": "qmp_capabilities"}); err != nil {
		return fmt.Errorf("asking for qmp capabilities: %w", err)
	}
	var reply struct {
		Return json.RawMessage `json:"return"`
		Error  *qmpError       `json:"error"`
	}
	if err := dec.Decode(&reply); err != nil {
		return fmt.Errorf("reading the capability reply: %w", err)
	}
	if reply.Error != nil {
		return reply.Error
	}
	// THE DEADLINE IS LIFTED AFTER THE HANDSHAKE. Each command sets its own, because a connection that
	// inherited a deadline from the greeting would fail later commands at an arbitrary moment.
	_ = l.nc.SetDeadline(time.Time{})
	return nil
}

// qmpError is qemu's own error object, unwrapped into Go's.
type qmpError struct {
	Class string `json:"class"`
	Desc  string `json:"desc"`
}

func (e *qmpError) Error() string {
	if e.Desc != "" {
		return e.Desc
	}
	return "qmp error class " + e.Class
}

// --- qemump.Monitor --------------------------------------------------------------------

// Connect is a no-op: `dialLine` has already completed the handshake.
//
// THE INTERFACE REQUIRES IT, and `raw.Monitor` never calls it - it exists so this type can be handed
// to `raw.NewMonitor`.
func (l *line) Connect() error { return nil }

// Disconnect closes the socket. It does NOT wait for anything, and that is the point: there is no
// listener goroutine whose channel has to drain.
func (l *line) Disconnect() error { return l.Close() }

// Run sends one command and reads one reply.
//
// ONE AT A TIME, AND EVERY COMMAND HAS A DEADLINE. The lock is held for the socket write and the socket
// read and nothing else, so a command that is slow delays exactly one command rather than all of them.
func (l *line) Run(command []byte) ([]byte, error) {
	if atomic.LoadInt32(&l.closed) != 0 {
		return nil, ErrClosed
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	// RE-CHECKED UNDER THE LOCK: the load above lets a closed connection fail without queueing behind a
	// command that is about to be cancelled anyway.
	if atomic.LoadInt32(&l.closed) != 0 {
		return nil, ErrClosed
	}
	// THE DEADLINE COVERS BOTH DIRECTIONS AND IS CLEARED AFTERWARDS, so a stale deadline cannot make a
	// later command fail at an unrelated moment.
	_ = l.nc.SetDeadline(time.Now().Add(commandTimeout))
	defer func() { _ = l.nc.SetDeadline(time.Time{}) }()

	if _, err := l.nc.Write(command); err != nil {
		// A WRITE ON A SOCKET THAT `Close` HAS TAKEN is `ErrClosed`, not a transport curiosity: the
		// caller's question is "is there a machine", and `use of closed network connection` answers it
		// only if you know what closed it.
		if atomic.LoadInt32(&l.closed) != 0 {
			return nil, ErrClosed
		}
		return nil, err
	}

	// ONE LINE IS ONE MESSAGE. QMP is newline-delimited JSON, so a decoder per call is exact - and it
	// tolerates a reply that arrives in two TCP segments, which a raw `Read` would not.
	dec := json.NewDecoder(l.nc)
	var reply struct {
		Return json.RawMessage `json:"return"`
		Error  *qmpError       `json:"error"`
		// EVENTS ARRIVE UNSOLICITED and are not replies; skip them rather than decoding them as one.
		Event string `json:"event"`
	}
	for {
		if err := dec.Decode(&reply); err != nil {
			// A CLOSED SOCKET IS `ErrClosed`, NOT A TRANSPORT CURIOSITY - a caller distinguishes "the
			// machine went away" from "this command was wrong" by the error it gets.
			//
			// THE CHECK IS THE FLAG, NOT THE ERROR TEXT. `Close` makes a parked read fail with `EOF`,
			// `use of closed network connection` or `i/o timeout` depending on how far it had got, and
			// matching those strings would be a second, worse copy of the truth.
			if atomic.LoadInt32(&l.closed) != 0 {
				return nil, ErrClosed
			}
			if errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("%w (qmp closed the socket: %v)", ErrClosed, err)
			}
			return nil, err
		}
		if reply.Event != "" {
			// NOBODY IN THIS REPOSITORY SUBSCRIBES TO EVENTS, so one is skipped. Resetting the fields is
			// what makes the next iteration a real reply rather than a stale one.
			reply.Return, reply.Error, reply.Event = nil, nil, ""
			continue
		}
		break
	}
	if reply.Error != nil {
		return nil, reply.Error
	}
	return reply.Return, nil
}

// Events is here only to satisfy `qemump.Monitor`.
//
// IT REFUSES RATHER THAN PRETENDING. Nothing in this repository subscribes - a walkthrough is a
// sequence of commands and readings, not a reaction to a shutdown notification - so a caller that asks
// gets a sentence naming that, rather than an empty channel it would wait on forever.
func (l *line) Events(context.Context) (<-chan qemump.Event, error) {
	return nil, errors.New("qmp events are not implemented in this transport; nothing in easyvm " +
		"subscribes to them - add a decoder here if that changes")
}

// Close closes the socket, once, from any goroutine - INCLUDING while a command is blocked in a read.
//
// IT DOES NOT TAKE `mu`, AND THAT IS THE WHOLE FIX. `Run` holds `mu` for the length of a command, so a
// `Close` that wanted the same mutex would wait for a reply that a dead or unresponsive qemu is never
// going to send. MEASURED: eight commands against a silent peer, and a close that waited forever - the
// same shape as the `destroy` that never returned.
//
// THE FILE DESCRIPTOR IS THE CANCELLATION. A `Read` parked on it returns `EOF`/`ErrClosed` the moment
// it goes away, without any goroutine having to be woken, and `sync.Once` makes a second close free.
func (l *line) Close() error {
	l.once.Do(func() {
		atomic.StoreInt32(&l.closed, 1)
		l.err = l.nc.Close()
	})
	return l.err
}

// capabilities is what the greeting advertised, for a caller that wants to know whether `oob` is
// supported before using an out-of-band send.
func (l *line) capabilities() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.caps...)
}

// Version is the emulator version this connection reported, for a log line.
func (l *line) Version() Version {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.version
}
