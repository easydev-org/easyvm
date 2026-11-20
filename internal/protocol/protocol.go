// Package protocol is the wire format between the daemon and its clients.
//
// IT IS ITS OWN PACKAGE BECAUSE OF AN IMPORT CYCLE, NOT BECAUSE IT IS CLEANER: the client imports
// the daemon for these types, and the daemon's own tests import the client to talk to a real
// socket. Keeping the types here is what lets both directions exist without one of them being a
// copy - and a copy of the wire format is the kind of thing that disagrees the first time a field
// is added.
package protocol

import (
	"errors"
	"path/filepath"
	"time"
)

// SocketPath is where a daemon listens, given a work directory.
//
// IT IS A UNIX SOCKET AND NOT A TCP PORT. The daemon controls a machine on this host and needs no
// network exposure; a socket is a filesystem permission, which is the right shape for "only this
// user may drive this VM". It also cannot be reached by accident from another container.
func SocketPath(work string) string {
	return filepath.Join(work, "easyvm.sock")
}

var (
	// ErrNoDaemon is what a client returns when nothing answers on the socket.
	ErrNoDaemon = errors.New("no daemon is listening")
	// ErrBusy is what a client returns when the daemon refused because it was working.
	ErrBusy = errors.New("the daemon is busy")
)

type State struct {
	// Status is one of starting, running, waiting, stopping, finished, failed.
	Status string `json:"status"`
	// File is the walkthrough being driven.
	File string `json:"file,omitempty"`
	// Step is the name of the current or last step.
	Step string `json:"step,omitempty"`
	// StepIndex and StepCount place it, for "12 of 29".
	StepIndex int `json:"step_index,omitempty"`
	StepCount int `json:"step_count,omitempty"`
	// Answered are the steps that passed, in order.
	Answered []string `json:"answered,omitempty"`
	// Message is the last line worth reading, for a human.
	Message string `json:"message,omitempty"`
	// Error is why it failed, when Status is failed.
	Error string `json:"error,omitempty"`
	// SSH is host:port a client can reach the guest on, for a human.
	SSH string `json:"ssh,omitempty"`
	// Started is when this run began.
	Started time.Time `json:"started"`

	// --- THE FACTS A STEP MAY WAIT ON ------------------------------------------------
	//
	// EVERY ONE OF THESE IS ALSO A `wait_until:` CONDITION, and that is why they are in the state
	// rather than computed where they are used: a file that waits needs the same answer a person
	// reading `/api/state` does, and two implementations of "is sshd up" is how they come to disagree.
	//
	// A NIL POINTER MEANS "NOT ASKED YET OR NOT APPLICABLE" - there is no machine, so there is no
	// qemu to be running. The alternative, a zero value, cannot tell "stopped" from "never started".
	QEMU *QEMUState `json:"qemu,omitempty"`
	// SSH is whether sshd accepted a connection, not whether the port is forwarded.
	SSHReady *bool `json:"ssh_ready,omitempty"`
	// QGA is whether the guest agent answered `guest-ping`.
	QGAReady *bool `json:"qga_ready,omitempty"`
	// Console is the serial log's own state.
	Console *ConsoleState `json:"console,omitempty"`
	// Disk is the disk this machine runs on, when there is one.
	Disk *DiskState `json:"disk,omitempty"`
	// Recording is whether a sampler is writing frames.
	Recording *bool `json:"recording,omitempty"`
	// Share is whether the HTTP share answers.
	ShareReady *bool `json:"share_ready,omitempty"`
	// Frame is the last screendump this daemon knows about.
	Frame *FrameState `json:"frame,omitempty"`

	// Guest is the highest-order thing a walkthrough can wait for: did the guest finish starting?
	// It is derived, not measured - `booting` until the framebuffer changes size, then `up`.
	Guest *GuestState `json:"guest,omitempty"`
}

// QEMUState is the machine process.
type QEMUState struct {
	Running bool `json:"running"`
	// PID is the process's own pid, or 0 when nothing runs.
	PID int `json:"pid,omitempty"`
	// Phase is the daemon's slot phase: absent, starting, running, stopping.
	Phase string `json:"phase"`
}

// ConsoleState is the guest's serial log.
type ConsoleState struct {
	// Path is where the log is, on the host.
	Path string `json:"path,omitempty"`
	// Bytes is how big it is now, which is what "is it still being written" needs.
	Bytes int64 `json:"bytes"`
	// IdleSeconds is how long since it last grew. A console that has stopped is the only evidence
	// a boot with no prompt offers.
	IdleSeconds float64 `json:"idle_seconds"`
}

// DiskState is the file the machine writes to.
type DiskState struct {
	Path string `json:"path,omitempty"`
	// AllocatedMB is what the qcow2 has actually written, not its virtual size. An install grows
	// this; a disk that has stopped growing is an install that has stopped.
	AllocatedMB int64 `json:"allocated_mb"`
	// GrowRateMBps is how fast it grew over the last sample, 0 when it is quiet.
	GrowRateMBps float64 `json:"grow_rate_mbps"`
}

// FrameState is the last screendump.
type FrameState struct {
	Width  int `json:"width"`
	Height int `json:"height"`
	// AgeSeconds is how long since the frame was captured.
	AgeSeconds float64 `json:"age_seconds"`
	// Changed is whether the pixels differ from the previous frame - the cheapest "is anything
	// happening" there is, and the one a blanked screen and a frozen guest disagree about.
	Changed bool `json:"changed"`
}

// GuestState is the boot, as a name rather than a measurement.
type GuestState struct {
	// Phase is booting, or up.
	Phase string `json:"phase"`
	// Since is how long it has been in that phase.
	Since float64 `json:"since_seconds"`
}

type Request struct {
	// Command is run, status, stop, or shutdown.
	Command string `json:"command"`
	// File is the walkthrough, for `run`.
	File string `json:"file,omitempty"`
	// Work is the scratch directory, for `run`.
	Work string `json:"work,omitempty"`
	// BootDisk, Out and Port are the run facts a file cannot hold.
	BootDisk string `json:"boot_disk,omitempty"`
	Out      string `json:"out,omitempty"`
	Port     int    `json:"port,omitempty"`
	// Password is the guest account's password.
	Password string `json:"password,omitempty"`
	// Vars are the file's own variables, overridden from the command line.
	//
	// IT IS A MAP RATHER THAN MORE FIELDS, AND THAT IS THE POINT. Each stage names run facts of its
	// own - `shutdown` for a stage that must not ask macOS to power down, `disk_name` and
	// `expected_gb` for the erase step, `boot_timeout` for a loaded runner - and a struct that grew
	// a field per stage would be a second place the vocabulary lives. The file declares them; this
	// supplies values.
	Vars map[string]string `json:"vars,omitempty"`

	// --- the interactive surface -------------------------------------------------------------
	//
	// A MACHINE THAT IS RUNNING CAN BE LOOKED AT AND TOUCHED, and these fields carry that. They are
	// on the same request as `run` because it is the same daemon and the same QMP connection: the
	// alternative is a second service that would have to reach the first one's socket anyway.
	//
	//	click    X, Y                       press and release at a point
	//	double   X, Y                       one press-pair, which is what a double-click is
	//	key      Key                        one qcode, e.g. `ret`, `esc`, `ctrl+alt+t`
	//	type     Text                       a string, typed at the driver's pace
	//	screenshot                          write a frame and answer with its path and size
	//	frames                              the recording's frame files, newest last
	// X AND Y ARE DECLARED SEPARATELY BECAUSE THEY CARRY DIFFERENT TAGS. `X, Y int` with one tag
	// gives field Y the tag `x` as well - `go vet` refuses it, and it shipped because `go build`
	// does not run vet: the image's `binary` job failed on
	//
	//	internal/protocol/protocol.go:92:5: struct field Y repeats json tag "x"
	// POINTERS, SO "NO COORDINATE" IS A DIFFERENT FACT FROM "COORDINATE ZERO".
	//
	// `(0, 0)` IS THE TOP-LEFT PIXEL AND A LEGAL TARGET. As plain `int` it was indistinguishable from
	// a request that sent nothing, so
	//
	//	POST /api/click  {"x": 100, "y": 200}     both fields set
	//	POST /api/click  {}                       ALSO 0, 0 - and clicked the corner
	//
	// and a caller that misspelled the keys got a click at the top-left with `{"ok":true}`. THE
	// SECRETARY OF THE BUG IS THAT IT SUCCEEDED: there was no reading anywhere that said the two
	// numbers the request was supposed to carry had never arrived.
	//
	// WITH `*int` the daemon can refuse `nil` BY NAME, and `0` still means the corner.
	X    *int   `json:"x,omitempty"`
	Y    *int   `json:"y,omitempty"`
	Key  string `json:"key,omitempty"`
	Text string `json:"text,omitempty"`
}

type Response struct {
	// OK is false when the request was refused, and Error says why.
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	State *State `json:"state,omitempty"`

	// Screen is the artifact a `screenshot` produced: where it was written, and how big it is.
	//
	// THE PATH IS SERVED SEPARATELY RATHER THAN INLINED, because a 1920x1080 PPM is 6 MB and a JSON
	// document carrying base64 would be 8 MB per poll. The HTTP layer reads the file and answers with
	// the bytes; this only says which file.
	Screen *Screen `json:"screen,omitempty"`
}

// Screen is one captured frame.
type Screen struct {
	Path   string `json:"path"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// The two MODES a daemon can be asked for, and they are different products rather than different
// flags.
//
//	`run`    A BAKE. The walkthrough runs to its last step and the machine goes away. Every CI leg in
//	         this repository does this, and that is why `defer closeDriver()` was right there.
//
//	`create` A SANDBOX. The machine starts and STAYS STARTED. There is no final step to run out: a
//	         pod is a guest you can ssh into, screenshot and type at, for as long as the caller wants
//	         it, and `destroy` is what ends it.
//
// THE DIFFERENCE IS NOT A TIMEOUT. A bake with a very long timeout still ends by running out of
// steps; a sandbox has no steps to run out of. Making them one mode with a flag would mean the
// closing behaviour depended on a duration, which is the kind of rule that is wrong on the day a
// slow install takes longer than the number.
//
// WHAT A SANDBOX STILL USES FROM THE FILE is its `boot:` - the os, the disk, the share, the video
// adapter - because that is the machine's shape and the file is where shape lives. What it does NOT
// use is the steps: a sandbox that ran a walkthrough would be a bake with extra words.
const (
	// CommandClick, CommandDouble, CommandKey, CommandType and CommandScreenshot are the interactive
	// surface: what a person at the console does, reachable over the socket so a browser can do it.
	CommandClick      = "click"
	CommandDouble     = "double"
	CommandKey        = "key"
	CommandType       = "type"
	CommandScreenshot = "screenshot"

	// CommandCreate starts a machine and returns; it does not run any steps.
	CommandCreate = "create"
	// CommandDestroy stops the machine and its daemon, and is the only ending a sandbox has.
	CommandDestroy = "destroy"
)
