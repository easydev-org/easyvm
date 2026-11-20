package daemon

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"
)

// --- the qemu guest agent ---------------------------------------------------------------
//
// THE AGENT IS A JSON STREAM OVER A CHARDEC: one object per line out, one back, no framing beyond the
// newline. It is worth having because it is the only channel into a guest that needs neither sshd nor
// an ACPI handler - macOS answers the power button with a dialog and, before Remote Login is turned
// on, has nothing else to ask.
//
// EACH COMMAND IS A SEPARATE CONNECTION, DELIBERATELY. The protocol is synchronous and the agent
// multiplexes nothing, so a caller that held one connection across commands would have to track which
// reply belonged to which request. A step is one command, so a connection per step costs nothing and
// removes the whole class of "the reply I read was for the last question".
//
// THE COMMANDS THE PROTOCOL DEFINES, and which of them this build acts on:
//
//	guest-ping                      is the agent alive? (acts on it)
//	guest-info                      version and supported commands (acts on it)
//	guest-shutdown                  power off / reboot / halt (acts on it, and it is the one that
//	                                mattered: a guest with no sshd)
//	guest-exec                      run a program in the guest (acts on it)
//	guest-exec-status               what became of one (acts on it)
//	guest-file-open/read/write/close  move bytes in and out (acts on all four)
//	guest-set-time                  set the guest's clock (acts on it)
//	guest-network-get-interfaces    what the guest thinks its addresses are (acts on it)
//
// THE REST OF THE SCHEMA IS NOT HERE ON PURPOSE. `guest-fstrim`, `guest-suspend-*`, `guest-set-vcpus`
// and the filesystem-freeze family are real commands, and a vocabulary that claimed to run them
// without a file that does would be the same defect as `requires:` - a key that reads as a capability
// nobody ever exercised. A command lands here when a walkthrough needs it.

// qgaConn is one request/reply exchange with the agent's socket.
type qgaConn struct {
	conn net.Conn
	dec  *json.Decoder
}

// qgaReply is the agent's answer. `Return` is left raw because every command has its own shape.
type qgaReply struct {
	Return json.RawMessage `json:"return"`
	Error  *struct {
		Class string `json:"class"`
		Desc  string `json:"desc"`
	} `json:"error"`
}

// qgaDial connects and checks the agent is the thing on the other end.
//
// `guest-ping` FIRST, BECAUSE A LIVE UNIX SOCKET IS NOT A LIVE AGENT. qemu creates the chardev's path
// whether or not a guest-side agent ever opened its end, so a dial that succeeds proves the HOST side
// is there and nothing about the guest.
func qgaDial(sock string) (*qgaConn, error) {
	conn, err := net.DialTimeout("unix", sock, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connecting to the agent at %s: %w. The port exists whether or not "+
			"an agent is listening on it, so this says the GUEST has none running", sock, err)
	}
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
	c := &qgaConn{conn: conn, dec: json.NewDecoder(conn)}
	if _, err := c.call("guest-ping", nil); err != nil {
		conn.Close()
		return nil, fmt.Errorf("the agent at %s does not answer `guest-ping`: %w. The socket is "+
			"open on the host side, so this is the GUEST's agent: it is not installed, or not "+
			"running, or it is busy", sock, err)
	}
	return c, nil
}

func (c *qgaConn) Close() error { return c.conn.Close() }

// call sends one command and reads its one reply.
func (c *qgaConn) call(command string, args any) (json.RawMessage, error) {
	request := map[string]any{"execute": command}
	if args != nil {
		request["arguments"] = args
	}
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	if _, err := c.conn.Write(append(body, '\n')); err != nil {
		return nil, fmt.Errorf("sending %s: %w", command, err)
	}

	var reply qgaReply
	if err := c.dec.Decode(&reply); err != nil {
		// THE CONNECTION MAY CLOSE AS THE GUEST GOES DOWN, and the agent does not always answer a
		// shutdown before it executes it. That is a success that looks like a failure.
		if command == "guest-shutdown" && isEOF(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading the agent's answer to %s: %w", command, err)
	}
	if reply.Error != nil {
		return nil, fmt.Errorf("the agent refused %s: %s: %s", command, reply.Error.Class, reply.Error.Desc)
	}
	return reply.Return, nil
}

// --- the commands a step can name ---------------------------------------------------------

// qgaPing proves the agent answers.
func qgaPing(sock string) error {
	c, err := qgaDial(sock)
	if err != nil {
		return err
	}
	defer c.Close()
	return nil // qgaDial already pinged; reaching here IS the answer
}

// qgaInfo returns the agent's version and the commands it says it supports.
func qgaInfo(sock string) (string, error) {
	c, err := qgaDial(sock)
	if err != nil {
		return "", err
	}
	defer c.Close()
	raw, err := c.call("guest-info", nil)
	if err != nil {
		return "", err
	}
	var info struct {
		Version   string `json:"version"`
		Supported []struct {
			Name string `json:"name"`
		} `json:"supported_commands"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		return "", fmt.Errorf("the agent's `guest-info` is not the shape the protocol describes: %w", err)
	}
	return fmt.Sprintf("version %s, %d supported command(s)", info.Version, len(info.Supported)), nil
}

// qgaShutdown asks the agent to power the guest off.
//
// THE MODE IS A PARAMETER AND THE DEFAULT IS `powerdown`, which is what a stage about to publish the
// disk wants: a reboot leaves a running machine and a halt leaves one whose filesystems were never
// unmounted. It is a parameter because the protocol has it and a future file may need `reboot`.
func qgaShutdown(sock, mode string) error {
	if mode == "" {
		mode = "powerdown"
	}
	switch mode {
	case "powerdown", "reboot", "halt":
	default:
		return fmt.Errorf("`qga: shutdown` takes powerdown, reboot or halt, not %q", mode)
	}
	c, err := qgaDial(sock)
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.call("guest-shutdown", map[string]any{"mode": mode})
	return err
}

// qgaExec runs a program in the guest and waits for it, returning its exit status and output.
//
// IT IS `guest-exec` PLUS `guest-exec-status`, because the protocol's exec is asynchronous: the first
// call returns a pid and the second polls until `exited` is true. A caller that read only the pid
// would report success for a command that had not run yet.
func qgaExec(sock string, argv []string, timeout time.Duration) (int, string, error) {
	if len(argv) == 0 {
		return 0, "", fmt.Errorf("`qga: exec` needs a command line")
	}
	c, err := qgaDial(sock)
	if err != nil {
		return 0, "", err
	}
	defer c.Close()

	raw, err := c.call("guest-exec", map[string]any{
		"path":           argv[0],
		"arg":            argv[1:],
		"capture-output": true,
	})
	if err != nil {
		return 0, "", err
	}
	var started struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(raw, &started); err != nil {
		return 0, "", fmt.Errorf("`guest-exec` did not return a pid: %w", err)
	}

	deadline := time.Now().Add(timeout)
	for {
		raw, err := c.call("guest-exec-status", map[string]any{"pid": started.PID})
		if err != nil {
			return 0, "", err
		}
		var status struct {
			Exited   bool   `json:"exited"`
			ExitCode int    `json:"exitcode"`
			OutData  string `json:"out-data"`
			ErrData  string `json:"err-data"`
		}
		if err := json.Unmarshal(raw, &status); err != nil {
			return 0, "", fmt.Errorf("`guest-exec-status` is not the shape the protocol describes: %w", err)
		}
		if status.Exited {
			out := decodeQGAOutput(status.OutData)
			if e := decodeQGAOutput(status.ErrData); e != "" {
				out += e
			}
			return status.ExitCode, out, nil
		}
		if time.Now().After(deadline) {
			return 0, "", fmt.Errorf("`guest-exec` pid %d did not finish within %s", started.PID, timeout)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// decodeQGAOutput un-base64s a chunk the agent captured, keeping asymmetry out of the caller.
func decodeQGAOutput(encoded string) string {
	if encoded == "" {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return encoded
	}
	return string(raw)
}

// qgaWriteFile puts bytes into the guest and closes the handle.
//
// THE PROTOCOL IS A HANDLE, THREE CALLS: `guest-file-open` gives an fd, `guest-file-write` takes
// base64, `guest-file-close` releases it. A caller that skipped the close leaks an fd in the GUEST,
// which is a guest that runs out of them after enough steps.
func qgaWriteFile(sock, path string, data []byte) error {
	c, err := qgaDial(sock)
	if err != nil {
		return err
	}
	defer c.Close()

	raw, err := c.call("guest-file-open", map[string]any{"path": path, "mode": "wb"})
	if err != nil {
		return err
	}
	var handle int
	if err := json.Unmarshal(raw, &handle); err != nil {
		return fmt.Errorf("`guest-file-open` did not return a handle: %w", err)
	}
	// THE CLOSE RUNS EVEN WHEN THE WRITE FAILS, or the guest keeps the descriptor for the rest of
	// its life.
	defer c.call("guest-file-close", map[string]any{"handle": handle})

	if _, err := c.call("guest-file-write", map[string]any{
		"handle":  handle,
		"buf-b64": base64.StdEncoding.EncodeToString(data),
	}); err != nil {
		return err
	}
	return nil
}

// qgaReadFile reads a path out of the guest.
func qgaReadFile(sock, path string) ([]byte, error) {
	c, err := qgaDial(sock)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	raw, err := c.call("guest-file-open", map[string]any{"path": path, "mode": "rb"})
	if err != nil {
		return nil, err
	}
	var handle int
	if err := json.Unmarshal(raw, &handle); err != nil {
		return nil, fmt.Errorf("`guest-file-open` did not return a handle: %w", err)
	}
	defer c.call("guest-file-close", map[string]any{"handle": handle})

	var out []byte
	for {
		raw, err := c.call("guest-file-read", map[string]any{"handle": handle, "count": 1 << 20})
		if err != nil {
			return nil, err
		}
		var chunk struct {
			BufB64 string `json:"buf-b64"`
			EOF    bool   `json:"eof"`
		}
		if err := json.Unmarshal(raw, &chunk); err != nil {
			return nil, fmt.Errorf("`guest-file-read` is not the shape the protocol describes: %w", err)
		}
		decoded, derr := base64.StdEncoding.DecodeString(chunk.BufB64)
		if derr != nil {
			return nil, fmt.Errorf("`guest-file-read` returned data that is not base64: %w", derr)
		}
		out = append(out, decoded...)
		if chunk.EOF {
			return out, nil
		}
		if len(decoded) == 0 {
			return out, nil
		}
	}
}

// qgaSetTime sets the guest's clock to the host's, in seconds and microseconds since the epoch.
func qgaSetTime(sock string, at time.Time) error {
	c, err := qgaDial(sock)
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.call("guest-set-time", map[string]any{
		"time": at.Unix()*1e9 + int64(at.Nanosecond()),
	})
	return err
}

// qgaNetworkInterfaces reports what the guest thinks its addresses are.
func qgaNetworkInterfaces(sock string) ([]string, error) {
	c, err := qgaDial(sock)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	raw, err := c.call("guest-network-get-interfaces", nil)
	if err != nil {
		return nil, err
	}
	var ifaces []struct {
		Name      string `json:"name"`
		Addresses []struct {
			Type    string `json:"ip-address-type"`
			Address string `json:"ip-address"`
			Prefix  int    `json:"prefix"`
		} `json:"ip-addresses"`
	}
	if err := json.Unmarshal(raw, &ifaces); err != nil {
		return nil, fmt.Errorf("`guest-network-get-interfaces` is not the shape the protocol "+
			"describes: %w", err)
	}
	var out []string
	for _, i := range ifaces {
		for _, a := range i.Addresses {
			out = append(out, fmt.Sprintf("%s %s %s/%d", i.Name, a.Type, a.Address, a.Prefix))
		}
	}
	return out, nil
}

func isEOF(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "EOF") || strings.Contains(msg, "closed network connection") ||
		strings.Contains(msg, "connection reset")
}
