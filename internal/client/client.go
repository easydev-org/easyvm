// Package client talks to a daemon over its unix socket.
//
// IT IS DELIBERATELY SMALL AND DELIBERATELY DUMB. A client sends one request and reads one reply;
// it holds no state, opens no QMP socket and cannot start a machine. The reason is in the daemon
// package's header: one owner, because a second owner is a second engine.
//
// THE TYPES ARE ALIASES of the daemon's, not copies. A copy would be a second definition of the
// wire format, and the two would disagree the first time a field was added - which is the same
// "one fact in two places" mistake as everything else this port is removing.
package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/easydev-org/easyvm/internal/protocol"
)

// Request and Response are the PROTOCOL's types, not the daemon's. The daemon aliases the same
// ones; neither imports the other, which is what keeps a client from ever being able to reach the
// engine.
type Request = protocol.Request
type Response = protocol.Response
type State = protocol.State

// Client is one connection to a daemon.
type Client struct {
	conn net.Conn
}

// Dial connects to the daemon whose socket is in work.
//
// A MISSING SOCKET AND A DAEMON THAT IS NOT ANSWERING ARE DIFFERENT MESSAGES. The first is "start
// one", which is the common case and has a one-line fix; the second is "it is there and wedged",
// which is a different problem and must not be reported as the first.
func Dial(work string) (*Client, error) {
	sock := protocol.SocketPath(work)
	conn, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || isNoSuchFile(err) {
			return nil, fmt.Errorf("%w: no socket at %s", protocol.ErrNoDaemon, sock)
		}
		return nil, fmt.Errorf("the daemon at %s did not answer: %w", sock, err)
	}
	return &Client{conn: conn}, nil
}

func isNoSuchFile(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return stringsContains(opErr.Error(), "no such file")
	}
	return stringsContains(err.Error(), "no such file")
}

func stringsContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// Do sends one request and reads the reply.
func (c *Client) Do(req Request) (Response, error) {
	// THE DEADLINE IS GENEROUS FOR STATUS AND SHORT FOR EVERYTHING ELSE. A `run` is answered as
	// soon as the daemon accepts it, so nothing here waits for a walkthrough; the only call that can
	// take real time is a `stop` that is waiting for a step boundary.
	_ = c.conn.SetDeadline(time.Now().Add(30 * time.Second))
	if err := json.NewEncoder(c.conn).Encode(req); err != nil {
		return Response{}, fmt.Errorf("sending %q: %w", req.Command, err)
	}
	var resp Response
	if err := json.NewDecoder(c.conn).Decode(&resp); err != nil {
		return Response{}, fmt.Errorf("reading the reply to %q: %w", req.Command, err)
	}
	return resp, nil
}

// Close closes the connection.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}
