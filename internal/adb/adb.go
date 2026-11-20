// Package adb speaks enough of Android Debug Bridge to run commands in a guest.
//
// # WHY THIS EXISTS RATHER THAN A `platform-tools` BINARY
//
// The wire protocol is a handful of messages and one socket: CNXN to say hello, OPEN/WRTE/OKAY/CLSE
// to run a command and read what it says. `platform-tools` is a 10 MiB download, an RSA key the
// device must be told to trust, and a daemon (`adb start-server`) that binds 5037 and outlives the
// step that started it - for a protocol this small, all three are more moving parts than the thing
// they move.
//
// MEASURED ON A BLISSOS GUEST THIS REPOSITORY INSTALLED, and it is why the package exists at all:
//
//	sent     CNXN host::
//	device   CNXN version=0x1000001 maxdata=4096
//	         banner=ro.product.name=Standard PC (Q35 + ICH9, 2009);ro.product.model=Generic Android-x86_64
//	         features=shell_v2,cmd,stat_v2,ls_v2,apex,abb,remount_shell,sendrecv_v2,...
//
// CNXN AND NOT AUTH. That guest runs `ro.adb.secure=0`, so adbd trusts a host it has never seen -
// no RSA key, no "Allow USB debugging?" dialog, no `adb_keys` file. A guest with secure adb would
// answer AUTH TOKEN, and this package reports that as the sentence it is rather than hanging.
//
// AND THE HEADER IS SIX WORDS, NOT FIVE. An adb message is
//
//	command, arg0, arg1, data_length, data_crc32, magic       with magic = command ^ 0xffffffff
//
// The first version of this sent five and then the payload, so adbd read the payload's first word as
// `magic`, disagreed, and closed the socket - which is indistinguishable from a device that refuses
// unauthenticated hosts, and cost an hour of looking at the wrong problem.
package adb

import (
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// The messages, named as system/core/adb/protocol.h names them.
const (
	cmdCNXN = 0x4E584E43
	cmdAUTH = 0x48545541
	cmdOPEN = 0x4E45504F
	cmdOKAY = 0x59414B4F
	cmdWRTE = 0x45545257
	cmdCLSE = 0x45534C43
)

// AVersion is the protocol version every adb since 2013 speaks.
const AVersion = 0x01000001

// maxPayload is what this side is willing to receive. adbd sends its own in its CNXN and both
// sides use the smaller.
const maxPayload = 1 << 20

// Client is a connection to one device's adbd.
type Client struct {
	conn net.Conn
	rw   io.ReadWriter

	localID  uint32
	remoteID uint32
	// DeviceMaxData is what the device said it can receive in one message.
	DeviceMaxData uint32
	// Banner is the `ro.product.*` block adbd sends in its CNXN, useful in a log line.
	Banner string
}

// Dial connects to an adbd and completes the CNXN handshake.
func Dial(host string, port int, timeout time.Duration) (*Client, error) {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, fmt.Errorf("no adbd at %s: %v. An Android guest runs adbd on 5555, and a "+
			"`boot:` with a tap network is what makes it reachable", addr, err)
	}
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		conn.Close()
		return nil, err
	}
	c := &Client{conn: conn, rw: conn}
	if err := c.handshake(); err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}

// handshake sends CNXN and reads until the device either agrees or asks for a key.
func (c *Client) handshake() error {
	// THE HOST DECLARES ITSELF FIRST. adbd never speaks unprompted - a bare read on a healthy
	// device's socket times out - so this is not optional politeness.
	if err := c.send(cmdCNXN, AVersion, maxPayload, []byte("host::\x00")); err != nil {
		return err
	}
	for {
		cmd, arg0, arg1, payload, err := c.recv()
		if err != nil {
			return err
		}
		switch cmd {
		case cmdCNXN:
			c.DeviceMaxData = arg1
			c.Banner = banner(payload)
			return nil
		case cmdAUTH:
			// A SECURE GUEST ASKS FOR A SIGNATURE, AND THIS PROGRAM CANNOT GIVE ONE.
			//
			// The honest answer is the sentence, not a retry: `ro.adb.secure=1` means the device
			// requires a key in /data/misc/adb/adb_keys, and seeding one is a decision about the
			// disk rather than something a step can improvise.
			kind := "a signature"
			if arg0 == 3 {
				kind = "a public key"
			}
			return fmt.Errorf("adbd at %s wants %s: this guest runs secure adb "+
				"(ro.adb.secure=1), so a host key has to be seeded in /data/misc/adb/adb_keys, or "+
				"the guest has to be built with secure adb off. Every Android-x86 image measured "+
				"here has it off", c.conn.RemoteAddr(), kind)
		default:
			return fmt.Errorf("adbd answered %#x where CNXN was expected", cmd)
		}
	}
}

// banner pulls the device's own description out of a CNXN payload.
func banner(payload []byte) string {
	text := strings.TrimRight(string(payload), "\x00")
	parts := strings.Split(text, ";")
	if len(parts) > 1 {
		return parts[1]
	}
	return text
}

// Run executes `command` in the guest and returns its output and exit code.
//
// THE EXIT CODE COMES FROM `shell_v2`'s OWN FOOTER rather than from an `echo $?` sentinel. A
// sentinel collides with a command whose output ends the same way, and it cannot distinguish "the
// shell exited 3" from "the command printed a line that looks like a marker".
func (c *Client) Run(command string, timeout time.Duration) (string, int, error) {
	if err := c.conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return "", -1, err
	}
	c.localID++
	if c.localID == 0 {
		c.localID = 1
	}
	local := c.localID

	// `shell,v2:<cmd>` SELECTS THE BINARY SHELL PROTOCOL, WHICH IS WHAT CARRIES AN EXIT CODE.
	//
	// NOT `shell,v2,raw:` - measured, and the symptom was not an error. `raw` asks adbd to exec the
	// command WITHOUT a shell, so `pm list packages | grep taskbar` arrives as one argument and the
	// pipe is a literal character in a package name. AND NOT A TRAILING NUL: the payload's length is
	// in the header, so the extra byte becomes part of the command - `id -un\x00` is a command whose
	// name is four characters and a NUL, which the shell answers with nothing and exit 127.
	spec := fmt.Sprintf("shell,v2:%s", command)
	if err := c.send(cmdOPEN, local, 0, []byte(spec)); err != nil {
		return "", -1, err
	}

	var out strings.Builder
	exit := 0
	gotExit := false
	for {
		cmd, arg0, _, payload, err := c.recv()
		if err != nil {
			return out.String(), -1, err
		}
		switch cmd {
		case cmdOKAY:
			// The device accepted the stream; this side has nothing left to send.
			c.remoteID = arg0
		case cmdWRTE:
			c.send(cmdOKAY, local, arg0, nil)
			// ONE WRTE CAN CARRY SEVERAL SHELL_V2 PACKETS, AND THAT IS WHY THIS IS A LOOP.
			//
			// A packet is `1 byte id, 4 bytes little-endian length, then the data`. Measured on
			// this guest, one command's whole answer arrived as a SINGLE WRTE holding both packets:
			//
			//	\x01\x03\x00\x00\x00 13\n              stdout, 3 bytes
			//	\x03\x01\x00\x00\x00 00                 exit,   1 byte
			//
			// so reading one packet per message left the exit status inside the stdout string - the
			// output was "13\n\x03\x01\x00\x00\x00\x00" and the exit was -1.
			for rest := payload; len(rest) >= 5; {
				kind := rest[0]
				size := int(uint32(rest[1]) | uint32(rest[2])<<8 |
					uint32(rest[3])<<16 | uint32(rest[4])<<24)
				if size > len(rest)-5 {
					// A partial packet: the rest is on the next WRTE, and appending what is here
					// would put half a line into the output.
					break
				}
				data := rest[5 : 5+size]
				rest = rest[5+size:]
				switch kind {
				case 1, 2: // stdout, stderr - one buffer, because a caller reads one stream
					out.Write(data)
				case 3: // exit status, ONE byte
					if len(data) >= 1 {
						exit = int(data[0])
						gotExit = true
					}
				}
			}
		case cmdCLSE:
			c.send(cmdCLSE, local, arg0, nil)
			if !gotExit {
				// A CLSE with no exit packet means the stream died. -1 says "unknown", which is
				// different from 0 and must not be reported as success.
				return out.String(), -1, nil
			}
			return out.String(), exit, nil
		}
	}
}

// Root asks adbd to restart itself as uid 0, and reports what it said.
//
// THERE IS NO STEP FOR THIS TODAY, AND THAT IS A DECISION RATHER THAN AN OVERSIGHT.
// `android/blissos16/steps/vm-configure.yaml` measured that the uid-2000 shell owns every operation
// that stage needs - appops, settings, cmd, setprop - so a step that took root would add a
// privilege and a dependency for nothing. This method stays because the protocol fact is worth
// having written down and measured: adbd's `root:` service is how a later stage (one that installs a
// Magisk module, say) would get uid 0, and a second implementation of that handshake would be a
// second set of mistakes.
//
// IT IS A SERVICE, NOT A SHELL COMMAND. `root:` is a stream name; asking for it through `adb shell`
// asks the shell to restart the daemon underneath itself, which cannot work. The answer is one of
//
//	"adbd is already running as root"                  nothing to do
//	"restarting adbd as root"                          it is doing it now
//	"adbd cannot run as root in production builds"     ro.debuggable=0
//
// MEASURED ON A BLISSOS GUEST THIS REPOSITORY INSTALLED, and it is why the third answer is not the
// end of the story: that guest reports `ro.debuggable=0` and STILL answers "restarting adbd as
// root", and `id` afterwards is `uid=0(root) ... context=u:r:su:s0`. The image's adbd is built to
// hand out root whatever the property says, which is what `me.weishu.kernelsu` being installed
// would predict.
//
// THE CONNECTION IS DEAD AFTER THIS WORKS: adbd exits and a new one starts, so every later command
// needs a Reconnect. That is the caller's business because the caller knows when to wait.
func (c *Client) Root(timeout time.Duration) (string, error) {
	if err := c.conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return "", err
	}
	c.localID++
	local := c.localID
	if err := c.send(cmdOPEN, local, 0, []byte("root:\x00")); err != nil {
		return "", err
	}
	var said strings.Builder
	for {
		cmd, arg0, _, payload, err := c.recv()
		if err != nil {
			// THE SOCKET DYING IS THE SUCCESS CASE: adbd exits to restart itself, so the read
			// returns EOF right after "restarting adbd as root" has been written.
			if said.Len() > 0 {
				return strings.TrimSpace(said.String()), nil
			}
			return "", err
		}
		switch cmd {
		case cmdOKAY:
			c.remoteID = arg0
		case cmdWRTE:
			c.send(cmdOKAY, local, arg0, nil)
			said.Write(payload)
		case cmdCLSE:
			c.send(cmdCLSE, local, arg0, nil)
			return strings.TrimSpace(said.String()), nil
		}
	}
}

// Close releases the socket.
func (c *Client) Close() error { return c.conn.Close() }

// Reconnect closes this socket and says hello again.
//
// WHY ONE CONNECTION IS NOT ENOUGH FOR SEVERAL COMMANDS. Measured on this guest, running four
// commands down one socket:
//
//	1  id -un                 exit 0, out "shell\n"        correct
//	2  getprop ...            exit -1, out ""              the stream never opened
//	3  pm list packages ...   exit 0, out "13\n"           THE PREVIOUS COMMAND'S OUTPUT
//	4  exit 3                 exit -1, out ""
//
// adb's streams are opened, used and closed, and a closed stream's late packets are still in flight.
// The next OPEN is matched against them by id, and a packet whose id was already retired is dropped
// or - worse - attributed to the new stream. Reconnecting makes each command start from a socket
// with nothing queued on it, which is a few milliseconds against a wrong answer.
func (c *Client) Reconnect(host string, port int, timeout time.Duration) error {
	c.conn.Close()
	fresh, err := Dial(host, port, timeout)
	if err != nil {
		return err
	}
	c.conn, c.rw = fresh.conn, fresh.rw
	c.DeviceMaxData, c.Banner = fresh.DeviceMaxData, fresh.Banner
	c.localID, c.remoteID = 0, 0
	return nil
}

// send writes one message: six 32-bit words and the payload.
func (c *Client) send(command, arg0, arg1 uint32, payload []byte) error {
	header := make([]byte, 24)
	put := func(at int, v uint32) {
		header[at], header[at+1], header[at+2], header[at+3] =
			byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
	}
	put(0, command)
	put(4, arg0)
	put(8, arg1)
	put(12, uint32(len(payload)))
	put(16, crc32.ChecksumIEEE(payload))
	// THE MAGIC IS THE COMMAND WITH EVERY BIT FLIPPED, and it is the sixth word. Sending five was
	// the bug this package's header records.
	put(20, command^0xFFFFFFFF)
	if _, err := c.rw.Write(header); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := c.rw.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

// recv reads one message.
func (c *Client) recv() (command, arg0, arg1 uint32, payload []byte, err error) {
	header := make([]byte, 24)
	if _, err = io.ReadFull(c.rw, header); err != nil {
		return 0, 0, 0, nil, err
	}
	get := func(at int) uint32 {
		return uint32(header[at]) | uint32(header[at+1])<<8 |
			uint32(header[at+2])<<16 | uint32(header[at+3])<<24
	}
	command, arg0, arg1 = get(0), get(4), get(8)
	length, magic := get(12), get(20)
	if magic != command^0xFFFFFFFF {
		return 0, 0, 0, nil, fmt.Errorf("adbd sent %#x with the wrong magic %#x: this socket is "+
			"not speaking adb", command, magic)
	}
	if length > 0 {
		payload = make([]byte, length)
		if _, err = io.ReadFull(c.rw, payload); err != nil {
			return 0, 0, 0, nil, err
		}
	}
	return command, arg0, arg1, payload, nil
}
