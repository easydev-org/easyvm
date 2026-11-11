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
	// NoVNCPort is where a browser can look, 0 when there is no page.
	NoVNCPort int `json:"novnc_port,omitempty"`
	// SSH is host:port a client can reach the guest on, for a human.
	SSH string `json:"ssh,omitempty"`
	// Started is when this run began.
	Started time.Time `json:"started"`
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
	// NoVNCPort is where the page should listen; 0 uses the default.
	NoVNCPort int `json:"novnc_port,omitempty"`
}

type Response struct {
	// OK is false when the request was refused, and Error says why.
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	State *State `json:"state,omitempty"`
}
