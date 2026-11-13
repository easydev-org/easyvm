package daemon

import (
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/easydev-org/easyvm/internal/guest"
)

// A SESSION THAT DIED WITH A REBOOT IS REPAIRED BY THE NEXT USE, WITHOUT ANY STEP KNOWING.
//
// THIS IS THE INVARIANT THAT REPLACED `DropGuest()`. The measured failure was
//
//	reboot: -> ssh:          "ssh session: EOF"
//
// on a guest that restarted itself, and the previous fix was a call in `require_ssh` - which every
// OTHER path that restarted the guest did not make. A cache whose correctness depends on callers
// remembering to invalidate it is a cache that is eventually wrong.
func TestADeadSessionIsRepairedOnceOnUse(t *testing.T) {
	var dials int32
	var mu sync.Mutex
	drops := 0

	s := newSSHSession(func() (*guest.Client, error) {
		atomic.AddInt32(&dials, 1)
		return nil, nil // the fake "connection": only the identity matters here
	})
	// THE FIRST CONNECTION IS DEAD; `Do` must notice and dial a second time.
	var calls int32
	err := s.Do(func(*guest.Client) error {
		if atomic.AddInt32(&calls, 1) == 1 {
			return io.EOF // exactly the error a rebooted guest produces
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Do returned %v, and it should have retried once and succeeded", err)
	}
	if got := atomic.LoadInt32(&dials); got != 2 {
		t.Fatalf("the session dialled %d time(s), wanted 2: the dead one and the replacement", got)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("the operation ran %d time(s), wanted 2", got)
	}
	mu.Lock()
	_ = drops
	mu.Unlock()
}

// A COMMAND THAT FAILED IS NOT RETRIED, because running it twice is worse than reporting it once.
//
// THE DISTINCTION IS THE WHOLE RISK IN THIS DESIGN. `io.EOF` means the socket is gone and the operation
// cannot have completed a second time; a non-zero exit means the guest RAN the command and answered,
// so retrying it would install a package twice or reboot an already-running machine.
func TestAFailedCommandIsNotRetried(t *testing.T) {
	var dials int32
	s := newSSHSession(func() (*guest.Client, error) {
		atomic.AddInt32(&dials, 1)
		return nil, nil
	})
	boom := errors.New("the command exited 3")
	var calls int32
	err := s.Do(func(*guest.Client) error {
		atomic.AddInt32(&calls, 1)
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Do returned %v, wanted the command's own error", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("the command ran %d time(s), wanted 1: a non-zero exit means the guest answered, "+
			"and retrying it runs the command twice", got)
	}
	if got := atomic.LoadInt32(&dials); got != 1 {
		t.Fatalf("the session dialled %d time(s), wanted 1", got)
	}
}

// CONCURRENT CALLERS SHARE ONE DIAL, so a busy moment does not open several connections.
//
// THE GUEST'S SSHD HAS A CONNECTION LIMIT and each dial is a separate authentication. The previous
// implementation had the same guarantee by hand - look, dial outside the lock, look again - and this
// keeps it in one place now that the cache has moved.
func TestConcurrentCallersShareOneDial(t *testing.T) {
	var dials int32
	release := make(chan struct{})
	s := newSSHSession(func() (*guest.Client, error) {
		atomic.AddInt32(&dials, 1)
		<-release // hold the dial open so every other caller arrives while it is in flight
		// A NON-NIL CONNECTION, so the waiters find something to reuse. A fake that returned nil
		// would make every waiter start its own dial, and the test would be measuring the fake.
		return &guest.Client{}, nil
	})

	const n = 8
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Get(); err != nil {
				t.Error(err)
			}
		}()
	}
	// LET THEM ALL REACH THE WAIT BEFORE THE DIAL FINISHES.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&dials); got != 1 {
		t.Fatalf("%d dial(s) for %d concurrent callers; they must share one, or the guest sees %d "+
			"authentications", got, n, got)
	}
}

// A DIAL THAT FAILS IS REPORTED, NOT CACHED AS SUCCESS.
//
// `Get` USED TO STORE WHATEVER `Dial` RETURNED, including nil, so a guest that was not yet listening
// left a session that answered `nil, nil` forever - and every step then dereferenced a nil client while
// the daemon reported that it had a connection.
func TestAFailedDialIsReportedAndRetried(t *testing.T) {
	var dials int32
	refused := errors.New("connection refused")
	s := newSSHSession(func() (*guest.Client, error) {
		if atomic.AddInt32(&dials, 1) == 1 {
			return nil, refused
		}
		return nil, nil
	})

	if _, err := s.Get(); !errors.Is(err, refused) {
		t.Fatalf("the first Get returned %v, wanted the dial's error", err)
	}
	if err := s.lastDialError(); !errors.Is(err, refused) {
		t.Fatalf("the session forgot why the dial failed: %v", err)
	}
	// AND THE NEXT GET TRIES AGAIN, rather than answering from a cached failure.
	if _, err := s.Get(); err != nil {
		t.Fatalf("the second Get returned %v, wanted success: a failed dial is not a cached result", err)
	}
	if got := atomic.LoadInt32(&dials); got != 2 {
		t.Fatalf("%d dial(s), wanted 2", got)
	}
}
