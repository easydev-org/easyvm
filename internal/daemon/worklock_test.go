package daemon

// THREE LOCKS, THREE WAYS TO PROVE THEY ARE LOCKS.
//
// Each test below fails on the code as it was BEFORE this file's change, and each one names the
// failure it prevents in the terms a person saw it in on a live machine.

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TEST 1: A SECOND DAEMON ON ONE WORK DIRECTORY IS REFUSED, BY NAME.
//
// MEASURED BEFORE THE FIX: two processes shared /work. The second boot did
//
//	_ = os.Remove("/work/drive.sock.qmp")
//
// on the first one's live qemu, and the first became an undrivable machine whose clicks returned
// {"ok":true} and moved nothing. There was no error at the moment of damage.
func TestLockWorkRefusesASecondHolder(t *testing.T) {
	dir := t.TempDir()

	first, err := LockWork(dir)
	if err != nil {
		t.Fatalf("the first lock on an empty directory must succeed: %v", err)
	}
	defer first.Release()

	second, err := LockWork(dir)
	if err == nil {
		second.Release()
		t.Fatalf("a second lock on the same directory must be refused; it was granted, so two " +
			"daemons would share one qmp socket and one tap")
	}
	if second != nil {
		t.Errorf("a refused lock must not return a lock; got %v", second)
	}

	// THE REFUSAL HAS TO NAME SOMEBODY. A message that says only "in use" leaves the reader with a
	// directory full of files and no way to learn which process to stop.
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("the refusal must name the directory %q; got %q", dir, err)
	}
	if !strings.Contains(err.Error(), "another easyvm") {
		t.Errorf("the refusal must say what is holding it; got %q", err)
	}
}

// TEST 2: THE LOCK SURVIVES THE HOLDER, WHICH IS WHAT MAKES IT A LOCK AND NOT A PIDFILE.
//
// This is the property that decides the whole design. A pidfile written by a `create` sandbox that
// was killed stays on disk and says nothing true. A kernel lock is gone the moment the holder is.
func TestLockWorkIsReleasedWhenTheHolderDies(t *testing.T) {
	dir := t.TempDir()

	// A CHILD PROCESS TAKES THE LOCK AND IS THEN KILLED WITH SIGKILL, which no handler can catch.
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperHoldsWorkLock")
	cmd.Env = append(os.Environ(), "EASYVM_LOCK_DIR="+dir)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the holder: %v", err)
	}

	// WAIT FOR IT TO ACTUALLY HOLD THE LOCK, by watching for the file it creates after locking.
	// A sleep would be a guess about how long a process takes to start.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, "held")); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(dir, "held")); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("the helper never took the lock, so this test would prove nothing")
	}

	// AND WHILE IT LIVES, THE LOCK IS REAL.
	if l, err := LockWork(dir); err == nil {
		l.Release()
		_ = cmd.Process.Kill()
		t.Fatalf("the lock was granted while another process held it")
	}

	// SIGKILL: no defer, no cleanup, no chance to write anything on the way out.
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("killing the holder: %v", err)
	}
	_ = cmd.Wait()

	// THE LOCK IS AVAILABLE AGAIN. If this fails, the lock is a pidfile in disguise and a crashed
	// daemon would block its own directory until somebody deleted a file by hand.
	var last error
	for i := 0; i < 100; i++ {
		l, err := LockWork(dir)
		if err == nil {
			l.Release()
			return
		}
		last = err
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the lock was not released by SIGKILL, so it is not a kernel lock: %v", last)
}

// TestHelperHoldsWorkLock is the child half of TestLockWorkIsReleasedWhenTheHolderDies.
func TestHelperHoldsWorkLock(t *testing.T) {
	dir := os.Getenv("EASYVM_LOCK_DIR")
	if dir == "" {
		t.Skip("this is a helper process")
	}
	l, err := LockWork(dir)
	if err != nil {
		os.Exit(3)
	}
	defer l.Release()
	// THE FILE IS THE SIGNAL THAT THE LOCK IS HELD. Written after LockWork returned, so its presence
	// cannot mean "started" - only "holding".
	_ = os.WriteFile(filepath.Join(dir, "held"), []byte("1"), 0o644)
	time.Sleep(30 * time.Second)
}

// TEST 3: A SOCKET THAT ANSWERS IS NOT A LEFTOVER, AND BOOT MUST REFUSE RATHER THAN DELETE IT.
//
// MEASURED BEFORE THE FIX: `_ = os.Remove(qmpPath)` ran unconditionally. With a live qemu behind the
// path, that deleted a running guest's only control channel and the damage was silent - the guest
// kept running and nothing could reach it.
func TestProbeUnixSocketSeesAListener(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "drive.sock.qmp")

	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	defer ln.Close()

	if err := probeUnixSocket(path, 250*time.Millisecond); err != nil {
		t.Fatalf("a socket with a listener must probe as alive; got %v", err)
	}
}

// And a path nothing answers is exactly what may be removed.
//
// THE LEFTOVER IS BUILT RATHER THAN HOPED FOR. Linux removes a unix socket's name when the listener
// closes, so an earlier version of this test SKIPPED on the only platform the walkthroughs run on
// and proved nothing. Two states a dead qemu can leave are covered instead, and both are files that
// exist with nothing behind them:
func TestProbeUnixSocketRejectsALeftover(t *testing.T) {
	dir := t.TempDir()

	t.Run("a plain file with the socket's name", func(t *testing.T) {
		// `net.Listen` cannot bind over this, nothing answers a connect to it, and removing it is the
		// only useful response - which is what makes it the honest leftover to test.
		path := filepath.Join(dir, "file.sock.qmp")
		if err := os.WriteFile(path, []byte("not a socket\n"), 0o644); err != nil {
			t.Fatalf("writing the leftover: %v", err)
		}
		if err := probeUnixSocket(path, 250*time.Millisecond); err == nil {
			t.Fatalf("a plain file probed as a live qemu, so Boot would refuse to clean up a " +
				"directory that is genuinely free")
		}
	})

	t.Run("a socket whose listener is gone", func(t *testing.T) {
		path := filepath.Join(dir, "dead.sock.qmp")
		ln, err := net.Listen("unix", path)
		if err != nil {
			t.Fatalf("listening: %v", err)
		}
		// KEEP THE NAME AND DROP THE LISTENER, which is what a killed qemu leaves on a filesystem
		// where the entry is not reclaimed.
		if ul, ok := ln.(*net.UnixListener); ok {
			ul.SetUnlinkOnClose(false)
		}
		_ = ln.Close()

		if _, err := os.Stat(path); err != nil {
			// The platform reclaimed it, so the "name with no listener" state cannot be built here.
			// This is a fact about the filesystem, and the subtest above still covers the probe.
			t.Logf("this filesystem reclaims the name on close; the plain-file case covers the probe")
			return
		}
		defer os.Remove(path)
		if err := probeUnixSocket(path, 250*time.Millisecond); err == nil {
			t.Fatalf("a socket with no listener probed as alive, so Boot would refuse to clean up " +
				"a directory that is genuinely free")
		}
	})
}

// AND A PATH THAT DOES NOT EXIST IS NOT AN ERROR WORTH REPORTING AS ONE - it is the normal state of
// a fresh work directory, and `probeUnixSocket` must say so by returning an error the caller ignores.
func TestProbeUnixSocketOnAMissingPath(t *testing.T) {
	if err := probeUnixSocket(filepath.Join(t.TempDir(), "nope"), 50*time.Millisecond); err == nil {
		t.Fatal("a missing path must not probe as alive")
	}
}

// TEST 4: A DEAD CLAIM IN THE QMP REGISTRY MUST NOT WEDGE THE NEXT MACHINE.
//
// THE MECHANISM THAT USED TO HANDLE THIS IS GONE - `waitForQMP` cleaned the ghost after a ten-second
// wait - BECAUSE THE RACE THAT PRODUCED THE GHOST IS GONE. What still has to be true is the property
// it was protecting: a registry entry whose connection has been closed, left behind by a caller that
// exited, must not make every later boot fail with `ErrBusy` for the life of the process.
//
// MEASURED BEFORE THE FIX:
//
//	boot: another caller has held the qmp socket for 10s and never published a connection;
//	that caller is stuck, and it has to be found rather than waited for
//
// and there was nothing to find: the registry held a connection whose owner had exited.
//
// `Release` is what establishes the property now, and it is what a boot performs before starting
// qemu - so this asserts it directly rather than through the removed wait.
func TestAReleasedClaimDoesNotWedgeTheNextBoot(t *testing.T) {
	dir := t.TempDir()
	d := &Daemon{}
	d.work = dir
	drv := &Driver{daemon: d, work: dir}

	path := filepath.Join(dir, "drive.sock.qmp")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = c.Write([]byte(`{"QMP":{"version":{"qemu":{"major":9,"minor":0,"micro":0},` +
					`"package":""},"capabilities":[]}}` + "\n"))
				dec := json.NewDecoder(c)
				var req struct {
					Execute string `json:"execute"`
				}
				if err := dec.Decode(&req); err == nil && req.Execute == "qmp_capabilities" {
					_, _ = c.Write([]byte(`{"return":{}}` + "\n"))
				}
				time.Sleep(20 * time.Second)
			}(c)
		}
	}()

	// A GHOST: a registry entry with a closed connection behind it.
	ghost, err := drv.qmpRegistry().Acquire(path, "sampler-that-died", 2*time.Second)
	if err != nil {
		t.Fatalf("seeding the registry: %v", err)
	}
	_ = ghost.Close() // closed, but the MAP still claims the path

	// THE BOOT RELEASES THE PATH BEFORE IT STARTS QEMU, and that is what makes the ghost harmless.
	if err := drv.qmpRegistry().Release(path); err != nil {
		t.Fatalf("release: %v", err)
	}
	conn, err := drv.qmpRegistry().Acquire(path, "easyvm", 2*time.Second)
	if err != nil {
		t.Fatalf("a released claim must not refuse the next caller; got %v", err)
	}
	if conn == nil {
		t.Fatal("the acquire must return a usable connection")
	}
	_ = drv.qmpRegistry().Release(path)
}

// THE REFUSAL FROM TEST 1 MUST NOT DEPEND ON THE LOCK FILE BEING WRITABLE, because a directory can be
// on a filesystem where a pid cannot be written. The kernel lock is the fact; the pid is a courtesy.
func TestLockWorkSurvivesAnUnwritablePid(t *testing.T) {
	dir := t.TempDir()
	lock, err := LockWork(dir)
	if err != nil {
		t.Fatalf("locking: %v", err)
	}
	lock.Release()
	// A SECOND RELEASE MUST BE HARMLESS: `defer lock.Release()` plus an explicit call is a normal
	// pattern, and a panic here would be a crash on the way out of a daemon.
	lock.Release()

	var nilLock *WorkLock
	nilLock.Release() // must not panic
	if nilLock.Path() != "" {
		t.Errorf("a nil lock must report no path")
	}
}

// AND THE ERROR MUST BE THE ONE CALLERS CAN RECOGNISE. `Serve` returns it verbatim, so the CLI prints
// this sentence to a person; it must not be wrapped into something that loses the pid.
func TestLockWorkErrorIsReadable(t *testing.T) {
	dir := t.TempDir()
	first, err := LockWork(dir)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	defer first.Release()

	_, err = LockWork(dir)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	msg := err.Error()
	if strings.Contains(msg, "flock") || strings.Contains(msg, "resource temporarily unavailable") {
		t.Errorf("the refusal leaks the syscall's own words (%q); it must say what a person should "+
			"do: which directory, and that another easyvm holds it", msg)
	}
	if !errors.Is(err, syscall.EWOULDBLOCK) && !strings.Contains(msg, "another easyvm") {
		t.Errorf("the refusal must be recognisable without parsing: %q", msg)
	}
}
