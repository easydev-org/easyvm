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
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/easydev-org/easyvm/internal/protocol"
)

// Request and Response are the PROTOCOL's types, not the daemon's. The daemon aliases the same
// ones; neither imports the other, which is what keeps a client from ever being able to reach the
// engine.
type Request = protocol.Request
type Response = protocol.Response
type State = protocol.State

// Client is one connection to a daemon, over a unix socket or over HTTP.
//
// TWO TRANSPORTS AND ONE PROTOCOL. The socket is what a supervisor uses: it is a filesystem
// permission, it cannot be reached from another container, and it is the shape "only this user may
// drive this VM" wants. HTTP is what a BROWSER uses, and what a person uses when the daemon is in
// another pod.
//
// THE REQUEST TYPE IS THE SAME ON BOTH, so a command cannot mean one thing to the CLI and another to
// the page - which is the whole reason to have two transports rather than two APIs.
type Client struct {
	conn net.Conn
	// base is the HTTP endpoint, when this client is talking over TCP. Empty means the socket.
	base string
	// http is used for the TCP transport.
	http *http.Client
}

// Dial connects to the daemon whose socket is in work.
//
// A MISSING SOCKET AND A DAEMON THAT IS NOT ANSWERING ARE DIFFERENT MESSAGES. The first is "start
// one", which is the common case and has a one-line fix; the second is "it is there and wedged",
// which is a different problem and must not be reported as the first.
// DialHTTP connects to a daemon's browser surface, for a caller in another pod or a person with a
// URL. It takes an address like `:8080`, `http://host:8080` or `host:8080`.
func DialHTTP(addr string) (*Client, error) {
	base := addr
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	base = strings.TrimRight(base, "/")
	if base == "" {
		return nil, fmt.Errorf("DialHTTP needs an address")
	}
	return &Client{base: base, http: &http.Client{Timeout: 30 * time.Second}}, nil
}

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
	// THE TCP TRANSPORT IS ONE POST PER COMMAND, and the path is the command's own name. The daemon
	// serves both shapes from the same `handle`, so this is a transport and not a translation.
	if c.base != "" {
		return c.doHTTP(req)
	}
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

// doHTTP sends one request to the browser surface.
//
// THE COMMAND SELECTS THE ROUTE, and the route is the command - `/api/click`, `/api/key`,
// `/api/type`, `/api/double`. Everything else, `run` and `create` included, goes to `/api/command`,
// which takes the whole Request. That keeps the fast paths readable for a browser and still gives the
// CLI one endpoint for all 11 commands.
func (c *Client) doHTTP(req Request) (Response, error) {
	route := "/api/command"
	switch req.Command {
	case protocol.CommandClick, protocol.CommandDouble, protocol.CommandKey, protocol.CommandType:
		route = "/api/" + req.Command
	case "status":
		// THE CHEAPEST POLL. A page asks for this every second, and `GET` lets a browser cache the
		// connection rather than renegotiating a POST per tick.
		resp, err := c.http.Get(c.base + "/api/state")
		if err != nil {
			return Response{}, fmt.Errorf("GET %s/api/state: %w", c.base, err)
		}
		defer resp.Body.Close()
		var out Response
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return Response{}, fmt.Errorf("reading /api/state: %w", err)
		}
		return out, nil
	}

	body, err := json.Marshal(req)
	if err != nil {
		return Response{}, err
	}
	resp, err := c.http.Post(c.base+route, "application/json", bytes.NewReader(body))
	if err != nil {
		return Response{}, fmt.Errorf("POST %s%s: %w", c.base, route, err)
	}
	defer resp.Body.Close()
	var out Response
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Response{}, fmt.Errorf("reading the reply to %q: %w", req.Command, err)
	}
	return out, nil
}

// Close closes the connection.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}
