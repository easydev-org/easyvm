package daemon

import (
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// --- the qemu guest agent's shutdown ----------------------------------------------------
//
// THE AGENT IS A JSON STREAM OVER A CHARDEC, and the one command this build needs is `guest-shutdown`.
// It is worth having because it is the only shutdown that works on a guest with no sshd and no ACPI
// handler: macOS answers the power button with a dialog and, before Remote Login is turned on, has
// nothing else to ask.
//
// THE PROTOCOL IS CONNECTION-ORIENTED AND SYNCHRONOUS: one JSON object per line out, one back. There
// is no framing beyond the newline, and qemu reads the object before it answers.

// qgaShutdown asks the agent at sock to power the guest off.
//
// A FAILURE HERE IS USUALLY THE GUEST, NOT THE PROTOCOL: an agent that is installed and stopped
// refuses the connection, and an agent that answers but cannot shut the machine down returns an error
// object with its own words.
func qgaShutdown(sock string) error {
	conn, err := net.DialTimeout("unix", sock, 10*time.Second)
	if err != nil {
		return fmt.Errorf("connecting to the agent at %s: %w. The port exists whether or not an "+
			"agent is listening on it, so this says the GUEST has none running", sock, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	// `guest-shutdown` RATHER THAN `guest-shutdown` WITH A MODE. The agent's default is powerdown,
	// which is what a stage that is about to publish the disk wants: a reboot or a halt would leave
	// either a running machine or one whose filesystems were never unmounted.
	request := map[string]any{"execute": "guest-shutdown"}
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if _, err := conn.Write(append(body, '\n')); err != nil {
		return fmt.Errorf("sending guest-shutdown: %w", err)
	}

	// THE ANSWER IS READ, BECAUSE "SENT" AND "ACCEPTED" ARE DIFFERENT. An agent that refuses says so
	// in an `error` object, and a caller that only wrote the request would report a shutdown that
	// never started.
	var reply struct {
		Return any `json:"return"`
		Error  *struct {
			Class string `json:"class"`
			Desc  string `json:"desc"`
		} `json:"error"`
	}
	dec := json.NewDecoder(conn)
	if err := dec.Decode(&reply); err != nil {
		// THE CONNECTION MAY CLOSE AS THE GUEST GOES DOWN, and qemu's agent does not always answer a
		// shutdown before it executes it. That is a success that looks like a failure.
		if isEOF(err) {
			return nil
		}
		return fmt.Errorf("reading the agent's answer: %w", err)
	}
	if reply.Error != nil {
		return fmt.Errorf("the agent refused guest-shutdown: %s: %s", reply.Error.Class, reply.Error.Desc)
	}
	return nil
}

// isEOF reports whether a read failed because the peer closed, which for a shutdown is success.
func isEOF(err error) bool {
	if err == nil {
		return false
	}
	if err.Error() == "EOF" {
		return true
	}
	// A unix socket closed under a decoder surfaces as one of these strings rather than a sentinel.
	for _, s := range []string{"EOF", "connection reset by peer", "use of closed network connection"} {
		if contains(err.Error(), s) {
			return true
		}
	}
	return false
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
