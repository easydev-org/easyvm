package run

import (
	"fmt"
	"strings"

	"github.com/easydev-org/easyvm/internal/walk"
)

// THE MACHINE'S OWN STATE IS A SET OF BOOLEANS, AND A FILE READS THEM BY NAME.
//
// BEFORE THIS THERE WERE TWO KINDS OF TRUTH WITH TWO MECHANISMS. A `check:` bound a boolean into
// `e.facts` and an `if:`/`repeat until` read it; the machine's real state was asked of the driver by
// `wait_until:`, with its own polling loop and its own names. So a file could branch on "the greeter
// said password" but not on "the machine has stopped", and the two could not be combined.
//
// NOW THERE IS ONE NAME SPACE. A `machine_` NAME IS LIVE: it is asked of the driver at the moment it
// is read, because the answer changes without a step to change it. Everything else is what a
// `check:` bound. That makes these equivalent:
//
//	machine                    repeat
//	  stopped                    until
//	  timeout 600                  machine_stopped
//	                             max 300
//	                             pause 2
//
// AND `if machine_stopped` ASKS ONCE, which is the difference the two forms exist for: `if` decides,
// `repeat` waits.
//
// THE PREFIX IS RESERVED. A `check:` cannot bind a name beginning with `machine_`, so a live fact
// cannot be shadowed by a stale one - see `Validate`.
const machinePrefix = "machine_"

// errNoSuchFact says a name refers to nothing, which is a mistake in the file rather than an answer.
// It is a sentinel because "false" and "nobody bound this" are different failures and only one of
// them should stop a walk.
const errNoSuchFact = "no `check:` step has made it"

// machineFacts are the live booleans a file may read, and what each one asks.
//
// EACH IS A QUESTION ABOUT ONE FACT THE DRIVER ALREADY REPORTS, and there is no expression language
// on purpose: a file that could write `machine_running and not machine_ssh` could also write
// something whose failure does not say which half never happened.
var machineFacts = map[string]string{
	"machine_running":      "the qemu process is up",
	"machine_stopped":      "the qemu process has gone",
	"machine_ssh":          "sshd accepts a connection",
	"machine_ssh_gone":     "sshd has stopped answering",
	"machine_qga":          "the guest agent answers",
	"machine_booted":       "the guest finished starting",
	"machine_console_idle": "the serial console has stopped being written",
	"machine_disk_idle":    "the disk has stopped growing",
	"machine_recording":    "something is recording",
	"machine_share":        "the host share has a directory",
	"machine_frame_still":  "the frame has stopped changing",
}

// isMachineFact reports whether a name is a live machine fact rather than a bound boolean.
func isMachineFact(name string) bool {
	return strings.HasPrefix(name, machinePrefix)
}

// machineFact asks the driver one question, now.
//
// IT IS THE ONLY PLACE THAT READS THE DRIVER'S FACTS, so `wait_until`, `if` and `repeat until` all
// agree about what "stopped" means - they used to have separate implementations, and a condition
// added to one was invisible to the others.
func (e *Executor) machineFact(s walk.Step, name string) (bool, string, error) {
	driver, ok := e.Driver.(interface {
		Facts() (map[string]string, error)
	})
	if !ok {
		return false, "", fmt.Errorf("this driver cannot report the machine's state, so `%s` "+
			"cannot be asked", name)
	}
	f, err := driver.Facts()
	if err != nil {
		return false, "", err
	}
	// THE THRESHOLDS ARE THE STEP'S OWN, because "quiet" and "still" are not one number: a boot
	// prompt goes quiet for a second and an install pauses for a minute between phases.
	switch name {
	case "machine_running":
		return f["qemu_running"] == "true", "the machine is not running", nil
	case "machine_stopped":
		return f["qemu_running"] == "false", "the machine is still running", nil
	case "machine_ssh":
		// THE DAEMON PROBES ssh AND qga ON ITS OWN TICKER, INDEPENDENT OF ANY WALK - see
		// internal/daemon/probe.go. This reads that answer rather than dialling again: a second dial
		// would be a second truth about the same guest, and the daemon's is the one `/api/state`
		// reports too.
		//
		// THE ANSWER HAS AN AGE. `runProbes` ticks every ten seconds, so a `repeat` that waits on
		// this must poll at least that slowly - which is why the file says `pause 10` rather than
		// trusting a default.
		return f["ssh_ready"] == "true", "sshd does not accept a connection", nil
	case "machine_ssh_gone":
		// THE NEGATIVE FORM, and it has a caller: a `require_ssh:` after `systemctl reboot` used to
		// wait for the OLD daemon to stop answering first, so the success it reported was the new
		// one's. MEASURED: without this the wait passed on the process that was about to die.
		return f["ssh_ready"] != "true", "sshd is still answering", nil
	case "machine_qga":
		return f["qga_ready"] == "true", "the guest agent does not answer", nil
	case "machine_booted":
		return f["guest_phase"] == "up", "the guest has not finished starting", nil
	case "machine_console_idle":
		return f["console_idle"] != "" && atLeast(f["console_idle"], s),
			"the console is still being written", nil
	case "machine_disk_idle":
		return atLeast(f["disk_grow_rate"], s), "the disk is still growing", nil
	case "machine_recording":
		return f["recording"] == "true", "nothing is recording", nil
	case "machine_share":
		return f["share_ready"] == "true", "the share has no directory", nil
	case "machine_frame_still":
		return atLeast(f["frame_age"], s), "the frame is still being captured", nil
	}
	return false, "", fmt.Errorf("`%s` is not a machine fact this build knows. The ones it knows "+
		"are %s", name, machineFactNames())
}

// factValue is the ONE lookup every reader of a boolean goes through: `if`, `repeat until`, and
// `wait_until` under its own name.
//
// IT IS WHY A LIVE FACT CANNOT GO STALE. A cached boolean is a statement about a moment that has
// passed; the machine's state is a statement about now. The two are stored differently because they
// are different kinds of fact, and this function is where a caller does not have to know which it
// asked for.
func (e *Executor) factValue(s walk.Step, name string) (bool, string, error) {
	if isMachineFact(name) {
		return e.machineFact(s, name)
	}
	if e.facts == nil {
		return false, errNoSuchFact, nil
	}
	value, ok := e.facts[name]
	if !ok {
		return false, errNoSuchFact, nil
	}
	// A BOUND BOOLEAN THAT IS FALSE IS AN ANSWER, NOT AN ERROR. The caller branches on it.
	return value, "", nil
}

// machineFactNames lists the live facts for a failure message.
func machineFactNames() string {
	var names []string
	for n := range machineFacts {
		names = append(names, n)
	}
	sortStrings(names)
	return strings.Join(names, ", ")
}

// sortStrings is a stable order for a message; it exists so this file does not import sort for one
// call.
func sortStrings(in []string) {
	for i := 1; i < len(in); i++ {
		for j := i; j > 0 && in[j] < in[j-1]; j-- {
			in[j], in[j-1] = in[j-1], in[j]
		}
	}
}
