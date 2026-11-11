// Package step defines the vocabulary a walkthrough YAML file may use.
//
// THIS FILE IS THE CONTRACT. The Python implementation this replaces accepted 85 distinct step
// keys, and every one of them was added because a specific CI run needed it. A key that is
// accepted here but does nothing is worse than a key that is rejected: the file loads, the step
// reports success, and the run proves nothing. So every key below is listed once, with the reason
// it exists, and the validator refuses anything not on the list.
package step

import "fmt"

// Key is one accepted field name in a step mapping.
type Key struct {
	Name string
	// Summary is what the key DOES, in the imperative, because a reader looking at a log needs to
	// know what a step claimed to do.
	Summary string
	// Kind says how the value is read and checked.
	Kind Kind
	// Since names the CI run or the measurement that put this key here, when there is one. A key
	// nobody can explain the origin of is a key nobody dares remove.
	Since string
}

// Kind is how a key's value is interpreted. It drives validation, not execution: the executor
// reads the same mapping, but a typo like `until: "Continue"` meaning a phrase versus
// `until: 30` meaning seconds has to be caught before a guest is started.
type Kind int

const (
	// KindString is free text.
	KindString Kind = iota
	// KindBool is true/false; YAML 1.1 quoting quirks make `on` a bool, which has bitten this
	// project before, so booleans are accepted only as real YAML booleans.
	KindBool
	// KindInt is a whole number of seconds, MiB, or a count.
	KindInt
	// KindFloat is seconds with a fractional part.
	KindFloat
	// KindPair is [x, y].
	KindPair
	// KindRect is [x0, y0, x1, y1].
	KindRect
	// KindWords is a phrase, or a list of words that must all be present.
	KindWords
	// KindWordSets is a list of phrases, any one of which satisfies the key.
	KindWordSets
	// KindMapping is a nested mapping (the `do:` list, `boot:` options).
	KindMapping
	// KindList is a sequence of mappings.
	KindList
	// KindScript is a multi-line body handed to a shell ON THE GUEST. Not our code: the disk
	// under test is what runs it, which is why it stays text.
	KindScript
)

func (k Kind) String() string {
	switch k {
	case KindString:
		return "string"
	case KindBool:
		return "bool"
	case KindInt:
		return "int"
	case KindFloat:
		return "float"
	case KindPair:
		return "[x, y]"
	case KindRect:
		return "[x0, y0, x1, y1]"
	case KindWords:
		return "words"
	case KindWordSets:
		return "[words, ...]"
	case KindMapping:
		return "mapping"
	case KindList:
		return "[mapping, ...]"
	case KindScript:
		return "script"
	}
	return "?"
}

// Keys is every accepted step key. Grouped by what the step does to the machine, because that is
// the order a reader needs them in: a step opens a screen, acts on it, waits, and then says
// whether it worked. The groups are documentation; the map below is enforcement.
var Keys = map[string]Key{
	// --- identity and gating -----------------------------------------------------------
	"name": {Name: "name", Kind: KindString,
		Summary: "what the log calls this step; required"},
	"optional": {Name: "optional", Kind: KindBool,
		Summary: "a step that may fail without failing the stage - only for a check whose " +
			"NEGATIVE result is information, never for a verdict"},
	"require": {Name: "require", Kind: KindBool,
		Summary: "the whole walkthrough fails if this step did not run"},
	"requires": {Name: "requires", Kind: KindList,
		Summary: "capabilities that must answer before the step runs (ssh, qga, share, " +
			"network, overlay); missing is a StepError, never a quiet fallback"},
	"unless": {Name: "unless", Kind: KindWords,
		Summary: "do this only if the screen does NOT already show these words"},
	"skip_when_unfinished": {Name: "skip_when_unfinished", Kind: KindBool,
		Summary: "in a panes epilogue: run only if the walk finished"},

	// --- the machine's own lifecycle ----------------------------------------------------
	"boot": {Name: "boot", Kind: KindMapping,
		Summary: "start a machine this file owns; its options include os, cpu, mem_mb, cpus, " +
			"share, iso, disk, tpm"},
	"os": {Name: "os", Kind: KindString,
		Summary: "which family the machine is: windows, macos, linux, android"},
	"family": {Name: "family", Kind: KindString,
		Summary: "the older spelling of os, kept for files not yet renamed"},
	// --- how a boot gets its first instruction -------------------------------------------
	//
	// A GUEST HAS TWO INDEPENDENT FACTS: which OS it is, and whether an installer is attached.
	// `os:` says the first. These say the second, and the presence of one IS the statement - the
	// same rule `disk:` follows, so a file cannot say two contradictory things about what it
	// starts.
	"iso": {Name: "iso", Kind: KindString,
		Summary: "an installer ISO. Windows and Linux boot it directly; macOS has none and is " +
			"refused by name if given one"},
	"recovery": {Name: "recovery", Kind: KindString,
		Summary: "macOS's recovery image, attached as removable media. It is the install medium " +
			"AND the source of the APFS driver OpenCore needs, so it is attached on every macOS " +
			"boot"},
	"answer_cd": {Name: "answer_cd", Kind: KindString,
		Summary: "a second CD-ROM carrying Windows' answer file. Setup finds it nowhere on the " +
			"install ISO"},
	"driver_cd": {Name: "driver_cd", Kind: KindString,
		Summary: "a third CD-ROM carrying the virtio drivers, for the installed system to pick up " +
			"network and ballooning"},
	"cpu": {Name: "cpu", Kind: KindString,
		Summary: "the guest's -cpu model when the disk's packages need a fixed one; empty is " +
			"-cpu host"},
	"reboot": {Name: "reboot", Kind: KindMapping,
		Summary: "reset the guest and wait for it to come back"},
	"shutdown": {Name: "shutdown", Kind: KindString,
		Summary: "a NAMED method: guest_agent, ssh, acpi, or kill; there is no silent fallback " +
			"between them"},
	"graceful": {Name: "graceful", Kind: KindBool,
		Summary: "ask the guest to shut down rather than killing it"},
	"record": {Name: "record", Kind: KindString,
		Summary: "start recording to this path (a string) or stop with `false`; the step starts " +
			"its own sampler, because an encoder with no frames writes nothing"},
	"compress": {Name: "compress", Kind: KindString,
		Summary: "flatten the overlay into this artifact; the path is relative to the repository " +
			"root, not to the work directory"},

	// --- the disk ----------------------------------------------------------------------
	"disk": {Name: "disk", Kind: KindMapping,
		Summary: "prepare the file `boot:` starts from a source: {from, copy, overlay}"},
	"disk_from": {Name: "disk_from", Kind: KindString,
		Summary: "the source image a disk: step takes"},
	"disk_copy": {Name: "disk_copy", Kind: KindString,
		Summary: "move or copy the source: move, copy, reflink"},
	"disk_overlay": {Name: "disk_overlay", Kind: KindBool,
		Summary: "run on a throwaway diff over the read-only source"},
	"disk_gb": {Name: "disk_gb", Kind: KindInt,
		Summary: "size of a disk this step creates"},
	"disk_gpt": {Name: "disk_gpt", Kind: KindBool,
		Summary: "partition the created disk with a GPT"},
	"disk_esp_mib": {Name: "disk_esp_mib", Kind: KindInt,
		Summary: "size of the EFI system partition on a created disk"},
	"disk_settle": {Name: "disk_settle", Kind: KindFloat,
		Summary: "wait until the target qcow2 stops growing for this many quiet seconds"},
	"disk_min_mb": {Name: "disk_min_mb", Kind: KindInt,
		Summary: "with disk_settle: only start waiting once it holds this many allocated MiB"},

	// --- the screen: act ---------------------------------------------------------------
	"at": {Name: "at", Kind: KindPair,
		Summary: "click this [x, y]. Fixed coordinates, never OCR - a button's position is a " +
			"measured fact about a screen, and a search for it is a guess that moves"},
	"double": {Name: "double", Kind: KindPair,
		Summary: "double-click [x, y]; separate from `at` because the gap between the two " +
			"clicks is what makes it a double-click"},
	"at_window": {Name: "at_window", Kind: KindPair,
		Summary: "[x, dy] - click x at dy pixels below the window's top edge, for a dialog whose " +
			"vertical position depends on what is above it"},
	"at_bottom": {Name: "at_bottom", Kind: KindPair,
		Summary: "[x, dy] - click x at dy pixels above the window's bottom edge"},
	"key": {Name: "key", Kind: KindString,
		Summary: "a qemu qcode, or a + combo such as ctrl+alt+t"},
	"type": {Name: "type", Kind: KindString,
		Summary: "text to type; combined with `at` it clicks the field first and clears it"},
	"clear": {Name: "clear", Kind: KindBool,
		Summary: "clear the field before typing (select-all, then backspace)"},
	"modifier": {Name: "modifier", Kind: KindString,
		Summary: "the select-all modifier for this family: ctrl on Linux, meta_l on macOS"},
	"type_gap": {Name: "type_gap", Kind: KindFloat,
		Summary: "seconds between keystrokes; 0.02 suits a POSIX shell, 0.15 a Windows one"},
	"run": {Name: "run", Kind: KindScript,
		Summary: "a shell line typed into the guest's own terminal, one line at a time"},
	"terminal_wait": {Name: "terminal_wait", Kind: KindFloat,
		Summary: "seconds to wait for the terminal after `run` opens it"},
	"line_gap": {Name: "line_gap", Kind: KindFloat,
		Summary: "seconds between the lines of a multi-line `run`"},
	"repeat": {Name: "repeat", Kind: KindList,
		Summary: "(count, settle) for a footer button pressed the same way N times"},
	"sweep": {Name: "sweep", Kind: KindMapping,
		Summary: "a band of clicks at one x, stopping when the sheet opens"},
	"press": {Name: "press", Kind: KindString,
		Summary: "press a named control rather than coordinates"},
	"change_settle": {Name: "change_settle", Kind: KindFloat,
		Summary: "with wait_for_change: how long the screen must stay unchanged"},
	"change_timeout": {Name: "change_timeout", Kind: KindFloat,
		Summary: "with wait_for_change: how long to wait for a change at all"},

	// --- the screen: read --------------------------------------------------------------
	"expect": {Name: "expect", Kind: KindWords,
		Summary: "words that must appear after the action; a mismatch fails the step and " +
			"withholds it from the answered count"},
	"expect_any": {Name: "expect_any", Kind: KindWordSets,
		Summary: "a list of word-sets, any one of which passes"},
	"until_console": {Name: "until_console", Kind: KindString,
		Summary: "wait for text in the guest's SERIAL output - the firmware's own words, which " +
			"a screenshot cannot see. A Windows install ISO prints its boot prompt there and " +
			"waits five seconds for a key"},
	"blind_after": {Name: "blind_after", Kind: KindInt,
		Summary: "with until_console: go on after this many seconds whether or not the text " +
			"appeared, for media that boot with no prompt at all"},
	"until": {Name: "until", Kind: KindWords,
		Summary: "words that end a wait loop"},
	"until_any": {Name: "until_any", Kind: KindWordSets,
		Summary: "a list of word-sets, any one of which ends the wait"},
	"until_dark": {Name: "until_dark", Kind: KindBool,
		Summary: "wait until the screen goes black - an installer's own start signal"},
	"wait_for_change": {Name: "wait_for_change", Kind: KindBool,
		Summary: "wait until the screen changes rather than for particular words"},
	"wait": {Name: "wait", Kind: KindFloat,
		Summary: "wait this many seconds with nothing clicked"},
	"settle": {Name: "settle", Kind: KindFloat,
		Summary: "wait this long after the action, for the screen to catch up"},
	"poll": {Name: "poll", Kind: KindFloat,
		Summary: "seconds between looks in an until: loop"},
	"timeout": {Name: "timeout", Kind: KindInt,
		Summary: "seconds the step may run before it is a failure"},
	"keep_alive": {Name: "keep_alive", Kind: KindBool,
		Summary: "click harmlessly between polls, for a guest that blanks its own screen"},
	"region": {Name: "region", Kind: KindRect,
		Summary: "crop [x0, y0, x1, y1] before reading, for until/expect"},
	"region_at": {Name: "region_at", Kind: KindPair,
		Summary: "[width, height] the crop was measured at; a mismatch is an error"},
	"scale": {Name: "scale", Kind: KindInt,
		Summary: "enlargement for a cropped read; small text needs it"},
	"window_brightness": {Name: "window_brightness", Kind: KindFloat,
		Summary: "mean grey that identifies the window this step measures"},
	"window_from": {Name: "window_from", Kind: KindInt,
		Summary: "first scanline of the window search"},
	"window_to": {Name: "window_to", Kind: KindInt,
		Summary: "last scanline of the window search"},
	"window_x": {Name: "window_x", Kind: KindInt,
		Summary: "the column the window search samples"},
	"screenshot": {Name: "screenshot", Kind: KindString,
		Summary: "save the screen to this path, for a post-mortem nobody had to ask for"},
	"desktop": {Name: "desktop", Kind: KindMapping,
		Summary: "wait until a desktop is on screen; options include family and timeout"},
	"desktop_family": {Name: "desktop_family", Kind: KindString,
		Summary: "which desktop test to use: macos, windows, linux"},

	// --- the guest over a channel -------------------------------------------------------
	"ssh": {Name: "ssh", Kind: KindScript,
		Summary: "a command run over ssh IN the guest; the exit status is the verdict"},
	"require_ssh": {Name: "require_ssh", Kind: KindBool,
		Summary: "wait for sshd before the steps that use it"},
	"ssh_probe": {Name: "ssh_probe", Kind: KindString,
		Summary: "the command that wait runs; `true` is not a Windows command"},
	"ssh_after_reboot": {Name: "ssh_after_reboot", Kind: KindBool,
		Summary: "on a require_ssh: wait out the old daemon first, or the wait passes on the " +
			"process that is about to die"},
	"scp": {Name: "scp", Kind: KindList,
		Summary: "[local-file, remote-path] - copy a script in before running it"},
	"script": {Name: "script", Kind: KindScript,
		Summary: "a body written on the host, copied in, and run by path. The guest's " +
			"interpreter runs it, not ours"},
	"script_shell": {Name: "script_shell", Kind: KindString,
		Summary: "the interpreter that runs `script`: cmd, powershell, or sh"},
	"script_name": {Name: "script_name", Kind: KindString,
		Summary: "the file name `script` is written as"},
	"adb": {Name: "adb", Kind: KindScript,
		Summary: "a command run over adb IN the guest; Android's remote access is adbd and " +
			"there is no sshd until a stage installs one"},
	"adb_root": {Name: "adb_root", Kind: KindBool,
		Summary: "adb root, then wait for adbd to come back - uid 0 with no `su`"},
	"adb_connect": {Name: "adb_connect", Kind: KindBool,
		Summary: "attach to the guest's adbd on its forwarded port and prove it answers"},
	"adb_serial": {Name: "adb_serial", Kind: KindString,
		Summary: "the serial to use"},

	// --- the host's share ---------------------------------------------------------------
	"share_serve": {Name: "share_serve", Kind: KindBool,
		Summary: "start the host's smbd for a directory - one act, not two"},
	"share_mount": {Name: "share_mount", Kind: KindBool,
		Summary: "the guest mounts what share_serve: is serving"},
	"share_dir": {Name: "share_dir", Kind: KindString,
		Summary: "the host directory to share; default ~/shared"},
	"share_family": {Name: "share_family", Kind: KindString,
		Summary: "which client the guest uses: macos, windows, linux"},

	// --- panes: the macOS shape ---------------------------------------------------------
	"when": {Name: "when", Kind: KindWords,
		Summary: "a pane: the words on screen that mean this step applies"},
	"do": {Name: "do", Kind: KindList,
		Summary: "a pane's actions, in order, run in one pass"},
	"once": {Name: "once", Kind: KindBool,
		Summary: "a pane answered at most ONCE, for a pane whose answer starts a slow process"},
	"end": {Name: "end", Kind: KindWords,
		Summary: "a pane file's end condition: the words that mean there is nothing left"},
	"stuck_after": {Name: "stuck_after", Kind: KindFloat,
		Summary: "seconds of nothing matching before the walk reports STUCK"},
	"release": {Name: "release", Kind: KindString,
		Summary: "which OS release is being driven; the Apple Account pane differs by release"},

	// --- host-side files, written or fetched before a copy -------------------------------
	//
	// THESE REPLACE `python:` STEPS, and the split is the same one the Python implementation had:
	// the file is WRITTEN or FETCHED here, the `scp:` after it moves it in, and the `ssh:` after
	// that runs it. A single step that wrote, copied and ran reported "the launcher install
	// exited 1" and named neither the file nor the action.
	"write": {Name: "write", Kind: KindString,
		Summary: "write one of the login/bootstrap files on the HOST, for the scp: after it. " +
			"Kinds: launcher, macos_agent_plist, macos_mount_script, macos_mount_plist, " +
			"windows_task"},
	"repeat_until": {Name: "repeat_until", Kind: KindMapping,
		Summary: "run `actions:` and then read the screen, up to `attempts:` times, stopping " +
			"when `until_any:` matches. This is the loop the vocabulary was missing: the two " +
			"primitives it replaces (choose a disk by capacity, leave the Apple Account pane) " +
			"were loops and decisions hidden in Go, where a reader of the file cannot see them"},
	"wait_for": {Name: "wait_for", Kind: KindString,
		Summary: "wait for a control with no readable title; login_window is the password field, " +
			"twelve pixels tall, read from a crop at 6x"},
	"demo_script": {Name: "demo_script", Kind: KindString,
		Summary: "write a throwaway script, for the primitives walkthrough that exists to exercise " +
			"the vocabulary itself"},
	"fetch": {Name: "fetch", Kind: KindString,
		Summary: "download or extract a host-side asset: guest_agent_windows, guest_agent_macos, " +
			"xcode, packages, windows_iso, virtio_win, answer_iso"},
	"template": {Name: "template", Kind: KindString,
		Summary: "with `fetch: answer_iso`: the answer file to substitute into. It is a path in " +
			"the repository because the placeholders are the file's vocabulary, not a download"},

	// --- extensions ---------------------------------------------------------------------
	"python": {Name: "python", Kind: KindString,
		Summary: "DEPRECATED AND NOT SUPPORTED BY THIS IMPLEMENTATION. The Python stage " +
			"extensions are being replaced by Go step types; a file that names one here is " +
			"refused by name rather than silently ignored"},
	"python_screen": {Name: "python_screen", Kind: KindBool,
		Summary: "DEPRECATED with `python:`"},
}

// Deprecated lists keys that are accepted by the parser only so a file can be REFUSED with a
// sentence instead of a parse error. They are not implemented.
var Deprecated = map[string]string{
	"python":        "Python stage extensions are not available in the Go implementation; the step it names must become a Go step type",
	"python_screen": "this only qualified a `python:` step, which is not available",
}

// Validate checks a step mapping against the vocabulary.
//
// A KEY THAT IS UNKNOWN FAILS THE FILE, and that is the whole point of this list: the Python
// implementation kept 85 keys alive by accepting whatever the executor happened to read, so a
// misspelled key was a step that did nothing and reported success. Two such key names are in this
// project's history already (`family:` then `os:`, `eject` spelled two ways).
func Validate(stepName string, mapping map[string]any) error {
	for key := range mapping {
		if _, ok := Keys[key]; ok {
			if why, gone := Deprecated[key]; gone {
				return fmt.Errorf("step %q uses %q: %s", stepName, key, why)
			}
			continue
		}
		return fmt.Errorf("step %q has unknown key %q; an unknown key used to mean a step that did nothing",
			stepName, key)
	}
	return nil
}
