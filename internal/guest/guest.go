// Package guest talks to a running machine over ssh.
//
// IT IS A CLIENT, NOT A WRAPPER AROUND ONE. The Python implementation shelled out to `sshpass ssh`,
// which meant: a dependency on a binary that is not part of the image, an exit status mangled by a
// local shell, and a password in a process argument list visible in /proc. `golang.org/x/crypto/ssh`
// removes all three, and it is what makes an exit code trustworthy - several defects in this
// repository were "the exit status of the thing I ran, seen through two shells".
package guest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Target is where and how to log in.
type Target struct {
	// Host is the forwarded address, normally the bridge (172.30.0.1).
	Host string
	// Port is the forwarded port, normally 2222.
	Port int
	// User and Password are the account the disk was built with.
	User     string
	Password string
}

// Addr is host:port.
func (t Target) Addr() string { return net.JoinHostPort(t.Host, fmt.Sprint(t.Port)) }

// Result is one command's outcome.
type Result struct {
	// Status is the command's exit status in the guest. 0 is success.
	Status int
	// Stdout and Stderr are kept apart, because several proofs read one and ignore the other.
	Stdout string
	Stderr string
}

// Combined is both streams, as a log wants them.
func (r Result) Combined() string {
	switch {
	case r.Stdout == "":
		return r.Stderr
	case r.Stderr == "":
		return r.Stdout
	}
	return r.Stdout + r.Stderr
}

// Client is one authenticated connection.
type Client struct {
	target Target
	conn   *ssh.Client
}

// Dial connects. The timeout covers the TCP connect, the handshake and the auth.
//
// IT IS NOT RETRIED HERE. Waiting for sshd to come up is a step in a walkthrough (`require_ssh:`)
// with its own timeout and its own log line, and a Dial that silently retried would make the two
// indistinguishable - a stage that waited 900 seconds inside a connect would report a broken disk
// when it had merely been early.
func Dial(t Target, timeout time.Duration) (*Client, error) {
	if t.User == "" {
		t.User = "vmuser"
	}
	config := &ssh.ClientConfig{
		User: t.User,
		Auth: []ssh.AuthMethod{
			ssh.Password(t.Password),
			// A guest whose sshd has keyboard-interactive enabled answers that instead, and the
			// password is the same answer. Both are offered rather than guessing which one a
			// given image's sshd_config asks for.
			ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range answers {
					answers[i] = t.Password
				}
				return answers, nil
			}),
		},
		// THE HOST KEY IS NOT A FACT ABOUT THIS DISK. Every guest is fresh, on a forwarded port
		// that differs per run, so a known-hosts file would either grow without bound or refuse
		// the connection after a rebuild. There is no security claim being made here that a host
		// key would support: the network is a private bridge inside one CI container.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         timeout,
	}
	conn, err := ssh.Dial("tcp", t.Addr(), config)
	if err != nil {
		return nil, err
	}
	return &Client{target: t, conn: conn}, nil
}

// Raw exposes the ssh connection, for a caller that needs a subsystem on it - SFTP, for copying a
// script in. It is deliberately the only hole in this package's abstraction: a second dial would be
// a second authentication and a second thing to fail.
func (c *Client) Raw() *ssh.Client {
	if c == nil {
		return nil
	}
	return c.conn
}

// Close closes the connection.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// Run executes a command and returns its exit status.
//
// THE STATUS IS THE VERDICT, and it is extracted from ssh.ExitError rather than from a local
// shell's view of it. A command that could not be STARTED is a different thing from one that ran
// and failed, and the two are reported differently: 127 for "no such command" is sshd's own
// answer, and err is non-nil only when the channel itself broke.
func (c *Client) Run(command string, timeout time.Duration) (Result, error) {
	session, err := c.conn.NewSession()
	if err != nil {
		return Result{}, fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	type done struct {
		err error
	}
	finished := make(chan done, 1)
	go func() {
		finished <- done{session.Run(command)}
	}()

	select {
	case <-time.After(timeout):
		// THE CHANNEL IS CLOSED RATHER THAN LEFT RUNNING. A command still writing into a buffer
		// nobody reads is a goroutine that outlives the step, and a step that timed out should
		// leave nothing behind.
		_ = session.Close()
		return Result{Stdout: stdout.String(), Stderr: stderr.String()},
			fmt.Errorf("the command did not finish in %s (it was: %s)", timeout, oneLine(command))
	case d := <-finished:
		res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
		if d.err == nil {
			return res, nil
		}
		var exitErr *ssh.ExitError
		if errors.As(d.err, &exitErr) {
			res.Status = exitErr.ExitStatus()
			return res, nil
		}
		// io.EOF here means the guest dropped the connection mid-command, which is not the
		// command failing - it is the machine going away, and a caller that treated it as a
		// status would retry forever.
		if errors.Is(d.err, io.EOF) {
			return res, fmt.Errorf("the guest closed the connection while running %q; the machine "+
				"may have rebooted", oneLine(command))
		}
		return res, fmt.Errorf("ssh: %w", d.err)
	}
}

// Output runs a command and returns its combined output, for a caller that only wants to see it.
func (c *Client) Output(command string, timeout time.Duration) (Result, error) {
	res, err := c.Run(command, timeout)
	if res.Status != 0 {
		// COMMANDS OFTEN WRITE THE REASON TO STDERR AND EXIT NON-ZERO, and the caller of this
		// helper is a proof that wants to show it.
		res.Stdout = res.Combined()
	}
	return res, err
}

// Probe answers "does a shell answer here" with a short timeout.
//
// IT RETRIES A TIMEOUT AND NOTHING ELSE, which is a measured distinction: the Windows guest's first
// `echo ok` on a freshly started sshd took longer than five seconds once, and a capability check
// that refused because a healthy guest was slow is worse than no check - it looks like a diagnosis.
// A refusal or a network error is not worth a second five seconds, and retrying it would turn a
// real problem into a slow one.
func Probe(t Target, attempts int, perAttempt time.Duration) error {
	if attempts <= 0 {
		attempts = 3
	}
	if perAttempt <= 0 {
		perAttempt = 5 * time.Second
	}
	var lastErr error
	for i := 1; i <= attempts; i++ {
		client, err := Dial(t, perAttempt*time.Duration(i))
		if err == nil {
			res, runErr := client.Run("echo ok", perAttempt*time.Duration(i))
			client.Close()
			if runErr == nil && res.Status == 0 {
				return nil
			}
			if runErr != nil {
				lastErr = runErr
				// A broken channel is not a slow guest.
				break
			}
			lastErr = fmt.Errorf("echo ok answered %d", res.Status)
			break
		}
		lastErr = err
		if !isTimeout(err) {
			break
		}
	}
	return lastErr
}

// isTimeout reports whether a dial failed by not finishing in time, as opposed to being refused or
// unreachable.
func isTimeout(err error) bool {
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return true
	}
	// ssh.Dial wraps the net error, and its message carries the timeout on the failure that
	// matters: a handshake that stalls after the TCP connect succeeded.
	return strings.Contains(err.Error(), "i/o timeout") ||
		strings.Contains(err.Error(), "timed out")
}

// WaitForPort waits until something accepts TCP on the forwarded port.
//
// THIS IS NOT THE SAME QUESTION AS "IS SSHD UP". A listening socket means the port forward works
// and a daemon is bound; whether it will authenticate is what Dial answers. Keeping them apart is
// what lets a log say "no route to host" (no IP), "connection refused" (IP, no listener) and
// "connection timed out" (IP, packets unanswered) - three different faults that read identically
// if collapsed into one.
func WaitForPort(t Target, timeout, poll time.Duration, onWait func(elapsed time.Duration, last error)) error {
	if poll <= 0 {
		poll = 2 * time.Second
	}
	start := time.Now()
	deadline := start.Add(timeout)
	var last error
	for {
		conn, err := net.DialTimeout("tcp", t.Addr(), 3*time.Second)
		if err == nil {
			conn.Close()
			return nil
		}
		last = err
		if time.Now().After(deadline) {
			return fmt.Errorf("nothing accepted ssh on %s within %s: %w", t.Addr(), timeout, last)
		}
		if onWait != nil {
			onWait(time.Since(start), last)
		}
		time.Sleep(poll)
	}
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 120 {
		return s[:120] + "..."
	}
	return s
}
