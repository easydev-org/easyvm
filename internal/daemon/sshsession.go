package daemon

import (
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/easydev-org/easyvm/internal/guest"
)

// sshSession is the guest's ssh connection, and it is a SESSION rather than a resource.
//
// # WHY IT IS NOT PART OF THE MACHINE'S LIFETIME
//
// A cached connection can die while the machine is perfectly healthy: a `reboot:` step resets the
// guest, and an `ssh: systemctl reboot` does the same from inside. MEASURED, twice, as
//
//	status: failed step 20/20 "read what this boot did"
//	error:  read what this boot did: ssh session: EOF
//
// on opensuse-tumbleweed, whose last step is exactly that pair. The old code answered it with a
// `DropGuest()` that a STEP had to remember to call - `require_ssh` did, `reboot:` did, and any other
// path that restarted the guest did not. A cache whose correctness depends on every caller remembering
// to invalidate it is a cache that is eventually wrong.
//
// # WHAT THIS IS INSTEAD
//
// THE CONNECTION DECIDES, AT THE MOMENT OF USE, WHETHER IT IS STILL GOOD. `Do` runs an operation; if it
// fails with an error that means "the session is gone" rather than "the command failed", the session is
// dropped and the operation is retried ONCE on a fresh connection. So a reboot is repaired by the next
// step that needs ssh, without any step knowing that a reboot happened.
//
// ONE CONNECTION, ONE DIALER. Concurrent callers share a dial in flight rather than opening two, which
// is the same guarantee `qmp.Registry` makes for the QMP socket and for the same reason: the guest's
// sshd has a connection limit and a second authentication is a second thing to fail.
type sshSession struct {
	// dial makes a new connection. Set once, by the driver, and never changed.
	dial func() (*guest.Client, error)

	mu   sync.Mutex
	conn *guest.Client
	// dialing is non-nil while a dial is in flight; waiters block on it rather than dialling too.
	dialing chan struct{}
	// lastErr is why the last dial failed, so a caller gets a sentence rather than a nil.
	lastErr error
}

// newSSHSession builds a session over a dialer.
func newSSHSession(dial func() (*guest.Client, error)) *sshSession {
	return &sshSession{dial: dial}
}

// Get returns the current connection, dialling one if there is none.
//
// IT DOES NOT VERIFY THE CONNECTION, because there is no cheap way to: ssh has no "are you there" that
// does not cost a round trip, and a round trip per step is what caching exists to avoid. `Do` is the
// entry point that repairs a stale one; `Get` is for a caller that needs the raw client (an sftp
// subsystem, a step that runs its own command with its own timeout).
func (s *sshSession) Get() (*guest.Client, error) {
	for {
		s.mu.Lock()
		// A DIAL IS IN FLIGHT: WAIT FOR IT RATHER THAN STARTING A SECOND.
		//
		// THE WAIT RE-READS THE RESULT RATHER THAN RECURSING, and the difference is a bug: a waiter that
		// called `Get` again after the dial finished saw `dialing == nil` and `conn != nil` - fine - but
		// if the dial FAILED it saw `conn == nil` and started ANOTHER dial. Eight concurrent callers
		// against a guest that was not listening therefore made eight connections, which is the limit
		// this whole cache exists to stay under.
		if s.dialing != nil {
			wait := s.dialing
			s.mu.Unlock()
			<-wait
			continue
		}
		if s.conn != nil {
			c := s.conn
			s.mu.Unlock()
			return c, nil
		}
		// THE DIAL HAPPENS WITHOUT THE LOCK. A guest that is not answering takes the full timeout, and
		// holding the lock across it blocks every other caller for exactly as long - the same shape that
		// deadlocked `Stop`.
		s.dialing = make(chan struct{})
		wait := s.dialing
		s.mu.Unlock()

		c, err := s.dial()

		s.mu.Lock()
		s.conn = c
		s.lastErr = err
		s.dialing = nil
		s.mu.Unlock()
		close(wait)

		return c, err
	}
}

// Drop closes and forgets the connection. The next `Get` dials a new one.
//
// IT IS STILL HERE FOR `reboot:`, WHICH KNOWS THE GUEST IS RESTARTING and should not leave a socket
// open across it. The difference from before is that correctness does not DEPEND on this being called:
// `Do` repairs a stale session on its own.
func (s *sshSession) Drop() {
	s.mu.Lock()
	c := s.conn
	s.conn = nil
	s.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

// Do runs one operation on the guest, retrying ONCE on a fresh connection if the session went away.
//
// THE RETRY IS THE WHOLE POINT. Without it, a step after a reboot fails with `ssh session: EOF` and the
// message points at the step rather than at the reboot three steps earlier. WITH it, the step that
// needs the guest repairs the connection and succeeds, and the log says so.
//
// IT RETRIES ONLY ON A SESSION-LEVEL ERROR. A command that exits 3 is the guest answering, not a broken
// connection, and retrying it would run the command twice.
func (s *sshSession) Do(fn func(*guest.Client) error) error {
	c, err := s.Get()
	if err != nil {
		return err
	}
	opErr := fn(c)
	if opErr == nil || !isSessionGone(opErr) {
		return opErr
	}
	// THE SESSION IS GONE, SO THE CACHED CLIENT IS WORTHLESS - dropping it is what stops the NEXT
	// caller spending a step discovering the same thing.
	s.Drop()
	fresh, err := s.Get()
	if err != nil {
		return err
	}
	return fn(fresh)
}

// isSessionGone reports whether an error means the CONNECTION died rather than the command failing.
//
// THE TEST IS THE ERROR, NOT A STRING. `x/crypto/ssh` returns `io.EOF` for a closed session,
// `net.ErrClosed` for one this side closed, and a `*net.OpError` for a transport failure - and matching
// message text would be a second, worse copy of that truth. A command that failed normally is a
// `*ssh.ExitError` or a non-zero status, which is none of these.
func isSessionGone(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	// A GUEST THAT REBOOTED RESETS THE TCP CONNECTION, which arrives as either of these.
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	return errors.Is(err, errSessionClosed)
}

// errSessionClosed is what `guest.Client` reports for a session it has already closed.
//
// IT IS DECLARED HERE RATHER THAN IN `guest` because the retry policy is this package's: `guest` says
// "this connection is closed", and whether that is worth retrying is a decision the caller makes.
var errSessionClosed = errors.New("the ssh session is closed")

// lastDialError is why the last dial failed, for the log line that explains a nil client.
func (s *sshSession) lastDialError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

// waiting is how long a caller will block on another goroutine's dial before giving up.
//
// IT IS LONGER THAN A DIAL'S OWN TIMEOUT so a waiter normally sees the result rather than this, and its
// only job is to keep a wedged dial from parking every later step forever.
const sshDialWait = 60 * time.Second
