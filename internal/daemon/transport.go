package daemon

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/easydev-org/easyvm/internal/run"
	"github.com/easydev-org/easyvm/internal/walk"
)

// listenUnix creates the socket with 0600, so only the owning user can drive the machine.
//
// THE PERMISSION IS THE SECURITY MODEL. There is no token and no password on this channel: the
// daemon controls a machine on this host, and "the user who can open this socket" is exactly the
// set of people allowed to. 0600 states that rather than relying on a umask.
func listenUnix(path string) (net.Listener, error) {
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// socketAnswers reports whether something is listening and answering on path.
func socketAnswers(path string) bool {
	conn, err := net.DialTimeout("unix", path, 500*time.Millisecond)
	if err != nil {
		return false
	}
	defer conn.Close()
	// A LIVE DAEMON ANSWERS A PING. A socket file left by a crashed process accepts the connect and
	// reads EOF; that distinction is what lets a restart clean up after a crash without ever
	// taking over from a running daemon.
	if err := json.NewEncoder(conn).Encode(Request{Command: "ping"}); err != nil {
		return false
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return false
	}
	return resp.OK
}

// acceptLoop serves requests one at a time, which the machine's single state requires.
func (d *Daemon) acceptLoop(ln net.Listener) error {
	defer ln.Close()
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-d.shutdown:
				return nil
			default:
				return err
			}
		}
		d.serveConn(conn)
		conn.Close()
	}
}

// serveConn answers requests on one connection until the client closes it.
//
// IT DID NOT LOOP AT FIRST, AND THAT WAS A REAL DEFECT. The client's obvious shape is one connection
// and several calls - `status` polled while a run proceeds - and a server that closed after the
// first reply made every later call decode from a closed socket. The symptom was a `status` that
// returned an EMPTY response with no error, which reads as a daemon that forgot its own state.
//
// A CONNECTION IS NOT AUTHORITY. Holding one lets a client keep ASKING, and each ask is answered on
// its own; nothing about the machine is granted by the socket staying open.
func (d *Daemon) serveConn(conn net.Conn) {
	dec := json.NewDecoder(bufio.NewReader(conn))
	for {
		// A CONNECTION THAT GOES QUIET IS DROPPED rather than held forever: a client that crashed
		// must not keep a descriptor for the life of the daemon.
		_ = conn.SetDeadline(time.Now().Add(10 * time.Minute))
		var req Request
		if err := dec.Decode(&req); err != nil {
			if !errors.Is(err, io.EOF) && !isClosed(err) {
				writeResponse(conn, Response{OK: false, Error: fmt.Sprintf("unreadable request: %v", err)})
			}
			return
		}
		writeResponse(conn, d.handle(req))
	}
}

// isClosed reports whether a read failed because the peer went away, which is the normal end of a
// connection and not worth a reply.
func isClosed(err error) bool {
	return errors.Is(err, net.ErrClosed) || strings.Contains(err.Error(), "use of closed network connection")
}

func writeResponse(conn net.Conn, resp Response) {
	_ = json.NewEncoder(conn).Encode(resp)
}

// runWalkthrough drives the file, and is the daemon's whole job.
//
// THE EXECUTOR'S DRIVER IS THE ONE THIS PACKAGE OWNS, so the machine and its position in the
// walkthrough live in the same process. That is the structural claim the package exists to make.
func (d *Daemon) runWalkthrough(req Request, work string, done chan struct{}) {
	defer close(done)

	fail := func(msg string, err error) {
		d.mu.Lock()
		d.st.Status = "failed"
		d.st.Message = msg
		if err != nil {
			d.st.Error = err.Error()
		}
		d.ex = nil
		d.mu.Unlock()
	}

	// `args.work` IS A RUN FACT LIKE `args.out`. A `fetch:` puts its file in the work directory and
	// the `scp:` that follows names it - so the file has to be able to say where that is, rather
	// than hard-coding a path the daemon chose.
	file, err := walk.Load(req.File, map[string]string{
		"args.out":  req.Out,
		"args.work": work,
	})
	if err != nil {
		fail("the walkthrough could not be loaded", err)
		return
	}

	driver, closeDriver, err := NewDriver(file, work, req.BootDisk, file.SSHPort,
		req.Password, req.NoVNCPort)
	if err != nil {
		fail("the machine could not be prepared", err)
		return
	}
	defer closeDriver()

	// THE HOOK IS HOW `stop` REACHES A RUN THAT IS MID-STEP. Without it, `stop` could only set a
	// flag the executor never reads, and a run blocked in a 900-second ssh wait would ignore it.
	ex := &run.Executor{
		Driver:   driver,
		File:     file,
		Work:     work,
		SSHPort:  file.SSHPort,
		Password: req.Password,
		OnStep: func(index int, total int, name string) {
			d.mu.Lock()
			d.st.Status = "running"
			d.st.Step = name
			d.st.StepIndex = index
			d.st.StepCount = total
			d.mu.Unlock()
		},
		ShouldStop: func() bool {
			d.mu.Lock()
			defer d.mu.Unlock()
			return d.stopping
		},
	}

	d.mu.Lock()
	d.ex = ex
	d.st.Status = "running"
	d.st.StepCount = len(file.Steps)
	d.mu.Unlock()

	res, runErr := ex.Run(file.Steps)

	d.mu.Lock()
	d.ex = nil
	d.st.Answered = res.Answered
	if runErr != nil {
		if d.stopping {
			d.st.Status = "finished"
			d.st.Message = "stopped as asked"
		} else {
			d.st.Status = "failed"
			d.st.Error = runErr.Error()
		}
	} else {
		d.st.Status = "finished"
		d.st.Message = fmt.Sprintf("%d step(s) ran", len(res.Answered))
	}
	d.mu.Unlock()
}

// SocketDir is where a daemon for a walkthrough file would put its socket, for a client that was
// given only the file.
func SocketDir(file, work string) string {
	if work != "" {
		return work
	}
	return filepath.Dir(file)
}
