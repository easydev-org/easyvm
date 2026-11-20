// Package step defines the vocabulary a walkthrough YAML file may use.
//
// THIS FILE IS THE CONTRACT. The Python implementation this replaces accepted 85 distinct step
// keys, and every one of them was added because a specific CI run needed it. A key that is
// accepted here but does nothing is worse than a key that is rejected: the file loads, the step
// reports success, and the run proves nothing. So every key below is listed once, with the reason
// it exists, and the validator refuses anything not on the list.
package step

import (
	"fmt"
	"sort"
	"strings"
)

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
	// KindColor is [r, g, b] - a colour, not a rectangle. `at_color: [40, 200, 64]` was declared
	// `KindRect` and nothing read the declaration, so the two disagreed since the key was written.
	KindColor
	// KindShot is a frame bound under a name: `see: {name: shot}` or `see: shot`.
	//
	// IT ACCEPTS BOTH SPELLINGS because the `.walk` converter writes the short one and a YAML file
	// may write either. What it REFUSES is anything else - a `see:` carrying parameters is the old
	// model, and `refuseLegacySee` names each one and where it belongs.
	KindShot
	// `KindRecord` WAS HERE, FOR `record:` - a file name to start a recording or `false` to stop it.
	// It went with the key: the recording is a property of the run now, so no key's value means
	// "start" or "stop" and no kind is needed to describe one.
	//
	// THE ENUM IS NOT NUMBERED, so removing a member renumbers the rest and nothing depends on the
	// numbers. A `Kind` is only ever compared to another `Kind`, never to an integer or stored.
	// KindTemplated is a number OR a `{{ name }}` placeholder, and the placeholder wins because the
	// loader substitutes variables after this map is read. `timeout: '{{ boot_timeout }}'` is what
	// ubuntu and windows ssh legs write.
	KindTemplated
	// KindAny is a key with more than one accepted shape. IT IS NOW EMPTY, AND THAT IS THE FINDING.
	//
	// IT HELD TEN KEYS AND EVERY ONE WAS A DECLARATION THAT HAD DRIFTED, NOT A KEY THAT NEEDED IT.
	// Measured on the loaded tree - the shape `Validate` actually sees, after variables are
	// substituted and after the older names are read under their current ones:
	//
	//	boot      bool:55                       one shape (its `os:`/`iso:` are SIBLING keys)
	//	disk      string:31                     one shape
	//	script    map{body,name,shell}:16       one shape
	//	rect      []any[4]:2                    one shape
	//	desktop   string:21                     one shape
	//	timeout   int/float64/string:482/6/26   one shape once the {{ }} template is resolved
	//	record    bool:49 string:51             TWO, and both are statements - see KindRecord
	//
	// SO THE TWO KEYS THAT REALLY DO CARRY TWO FORMS HAVE THEIR OWN KINDS AND ARE NOT HERE:
	// `KindRecord` names its pair (a file name or false) and `KindTemplated` names the template. This
	// kind stays in the list because a future key may need it, but a key landing here is a DECISION
	// and `TestTheStructuralKeysAreTheOnesWeThinkTheyAre` fails until somebody records it.
	KindAny
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
	case KindColor:
		return "[r, g, b]"
	case KindTemplated:
		return "a number or a {{ template }}"
	case KindAny:
		return "any"
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
	// `needs:` IS THE NAME AND `require:` WAS THE TRAP. `require:` named the FILES a stage needs on
	// disk; `requires:` names the CAPABILITIES the machine must answer. One letter apart, two
	// completely different assertions, and 43 uses between them - a reader had to count the `s`.
	// MEASURED: `require:` 22, `requires:` 21.
	"needs": {Name: "needs", Kind: KindList,
		Summary: "files that must be on disk before this stage runs; the first step of every stage " +
			"names its medium here so a missing input fails in the first second rather than forty " +
			"minutes in"},
	// `requires:` WAS HERE AND WAS NEVER READ.
	//
	// It was documented as "capabilities that must answer before the step runs (ssh, qga, share,
	// network, overlay)" and the dispatcher's own comment promised it was the other half of a pair -
	// "the rename is about one letter... `requires:` names the CAPABILITIES". THE CODE ONLY EVER
	// READ `needs:` AND `require:`, so all 21 uses were silently ignored, including 11 that asked for
	// the share. `notImplementedCount` listed it as a PASSIVE key, which is what kept it looking
	// harmless.
	//
	// IT WAS NOT REPLACABLE WITH A CHECK, EITHER: nothing in this tree implements a capability probe,
	// so a real `requires: qga` would have had no answer to compare against. The one thing it was
	// used for - "the share must be up" - is now a fact rather than a gate, because the share is
	// always on. A key that asserts something no code can test is worse than no key.
	// `when_not:` IS THE NEGATIVE GATE AND IT REPLACES `unless:`.
	//
	// `when:` AND `unless:` WERE ONE GATE WITH A NEGATION between them - "run if the screen says X"
	// and "run if it does NOT say X". Two names for one idea, 16 uses and 10, and a reader had to
	// remember which was which. `when:` / `when_not:` says it in the name.
	"when_not": {Name: "when_not", Kind: KindWords,
		Summary: "the negative gate: run this step only if the screen does NOT show these words"},
	"when_not_any": {Name: "when_not_any", Kind: KindWordSets,
		Summary: "the alternatives form of `when_not:`: the step runs only if the screen shows " +
			"NONE of these word-sets"},
	"skip_when_unfinished": {Name: "skip_when_unfinished", Kind: KindBool,
		Summary: "in a panes epilogue: run only if the walk finished"},

	// --- the machine's own lifecycle ----------------------------------------------------
	// `boot: true` IS A FLAG AND `boot: {os: linux}` IS THE SAME KEY WITH OPTIONS - both are in the
	// tree, so the Kind here is `any` and `doBoot` is what reads the two shapes.
	// THE VALUE IS `true`, AND IT IS THE ONE KEY WHOSE VALUE CARRIES NOTHING.
	//
	// IT USED TO SAY "its options include os, cpu, mem_mb, cpus, share, iso, disk, tpm" - and `doBoot`
	// reads `os`, `cpu`, `mem_mb`/`memory_mb`, `cpus`, `iso`, `kernel`, `initrd`, `recovery`,
	// `answer_cd`, `driver_cd` and `boot_from`. IT DOES NOT READ `share` (the share is always on), and
	// it has no `disk:` or `tpm:` option at all: the disk is chosen by an earlier `disk:` STEP that
	// sets the machine's file. A reader who trusted the old sentence would have written two keys that
	// were accepted by `Validate` and ignored by the executor.
	//
	// `boot: false` IS NOT MEANINGFUL and no file writes it - measured 57 `true` and 0 `false`. The
	// bool exists because the dispatcher asks `s.Has("boot")` and a step needs a value; `boot: true`
	// is what says "this step is the boot".
	"boot": {Name: "boot", Kind: KindBool,
		Summary: "start the machine this file owns. Its arguments are SIBLING keys - os, iso, " +
			"kernel, initrd, memory_mb, cpus, cpu, recovery, answer_cd, driver_cd - and the disk is " +
			"the one an earlier `disk:` step named"},
	"os": {Name: "os", Kind: KindString,
		Summary: "which family the machine is: windows, macos, linux, android"},
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
	// --- how big the guest is ------------------------------------------------------------
	//
	// THESE ARE ON THE BOOT STEP RATHER THAN ONLY IN A `machine:` BLOCK, because the boot step is
	// what reaches qemu. A `machine:` block's own `memory_mb:` is copied onto the boot step when the
	// machine is turned back into steps, so both spellings end up here.
	//
	// THE SIZE WAS READ AND DROPPED FOR ONE RELEASE: `machineFromBlock` stored it on the spec,
	// nothing put it on a step, `doBoot` had nothing to pass, and every guest ran with the 4096 MiB
	// default whatever the file said - which is why ghostbsd's installer ran out of memdisk.
	"memory_mb": {Name: "memory_mb", Kind: KindInt,
		Summary: "the guest's RAM in MiB; 4096 when not said. A guest that installs from a " +
			"memdisk needs more than the default"},
	"cpus": {Name: "cpus", Kind: KindInt,
		Summary: "how many vCPUs the guest gets; 4 when not said"},
	// `mem_mb:` WAS THE OLDER SPELLING OF `memory_mb:` AND IS GONE.
	//
	// It was kept "so a file that has not been renamed runs", and no file ever used it: measured
	// zero uses across the 57 walkthroughs. A second spelling that nothing spells is not a
	// compatibility shim, it is a key a reader has to guess between - and `internal/step/audit_test`
	// now reports any key this package declares that no file uses.
	"kernel": {Name: "kernel", Kind: KindString,
		Summary: "with `microvm:`: the kernel qemu hands the guest, because a microvm has no " +
			"firmware to read a boot sector"},
	"initrd": {Name: "initrd", Kind: KindString,
		Summary: "with `microvm:`: the initramfs beside the kernel"},
	"kernel_append": {Name: "kernel_append", Kind: KindString,
		Summary: "with `microvm:`: the guest's OWN kernel command line. Required for any microvm " +
			"whose root is a real filesystem - the default is Alpine's diskless `modloop=/dev/vda`, " +
			"which mounts OVER the root disk and drops the guest to an emergency shell"},
	"microvm_display": {Name: "microvm_display", Kind: KindString,
		Summary: "with `microvm:`: `serial` (no VGA, so `see:` reads nothing), `terminal` (VGA, " +
			"and the guest draws its TTY there), or `desktop` (VGA with a window manager). The " +
			"default is `terminal`, because a black video cannot be debugged"},
	"microvm": {Name: "microvm", Kind: KindBool,
		Summary: "with `boot:`: the smallest machine that can boot a modern Linux kernel - no " +
			"firmware, no devices the guest does not need, a direct kernel hand-off. For a " +
			"walkthrough whose job is to prove the vocabulary drives a guest, not to install a " +
			"distribution through its GUI"},
	"shutdown": {Name: "shutdown", Kind: KindString,
		Summary: "a NAMED method: guest_agent, ssh, acpi, or kill. EVERY ONE OF THEM STOPS THE " +
			"MACHINE AND WAITS FOR IT TO GO - a method that only waited was here for one commit and " +
			"did not belong: `shutdown:` names how to stop it, so a verb that stopped nothing was a " +
			"word that lied. Waiting for qemu to release the disk is a precondition of `compress:` " +
			"and lives there"},
	// `acpi:` AND `qga:` ARE CHANNELS WITH OPERATIONS, AND THEY REPLACED TWO `shutdown:` METHODS.
	//
	// `shutdown: acpi` and `shutdown: guest_agent` asked "how do I stop this machine" when the file
	// meant "send this message". That made the channel unavailable for anything else - a RESET is an
	// ACPI message and a `guest-exec` is an agent command, and neither is a shutdown.
	"acpi": {Name: "acpi", Kind: KindString,
		Summary: "send one ACPI message: `power` (the power button) or `reset`. It SENDS ONLY - " +
			"whether the guest goes is `wait_until: stopped`, a step of its own"},
	"qga": {Name: "qga", Kind: KindString,
		Summary: "run one qemu-guest-agent command: ping, info, shutdown, exec, file-read, " +
			"file-write, set-time, interfaces. The only channel into a guest with no sshd and no " +
			"ACPI handler"},
	"argv": {Name: "argv", Kind: KindList,
		Summary: "with `qga: exec`: the program and its arguments, as a list"},
	"body": {Name: "body", Kind: KindString,
		Summary: "with `qga: file-write`: the bytes to put in the guest's file"},
	"graceful": {Name: "graceful", Kind: KindBool,
		Summary: "ask the guest to shut down rather than killing it"},
	// `record:` WAS HERE. The recording is now a property of the RUN: it starts when the walk does
	// and `finishRecording` encodes it whichever way the walk ended. MEASURED: 74 uses, of which 36
	// were the `record: false` half - a step whose only job was to stop a recorder that the run's own
	// defer already stops, and which was unreachable when a step before it failed.
	"compress": {Name: "compress", Kind: KindString,
		Summary: "flatten the overlay into this artifact; the path is relative to the repository " +
			"root, not to the work directory"},
	"boot_from": {Name: "boot_from", Kind: KindString,
		Summary: "start the machine from a `snapshot:` file instead of running a boot loader. The " +
			"cpu, machine type and device set must match the ones the snapshot was taken with"},

	// --- the disk ----------------------------------------------------------------------
	// `disk: disk.qcow2` IS THE FORM EVERY FILE USES; the {from, copy} mapping is the older one and is
	// refused by name when `doDisk` sees it, which is a better error than a Kind mismatch here.
	//
	// THE SAME GOES FOR SIX MORE, AND EACH IS A KEY THAT GENUINELY CARRIES MORE THAN ONE SHAPE:
	//
	//	desktop       `linux/kde` is a name, `{os: linux, desktop: gnome}` is the same wait with options
	//	record        `install.mp4` starts one, `false` stops it - a bool is a STATEMENT here
	//	timeout       a number of seconds, or `'{{ boot_timeout }}'` resolved before this is reached
	//	script        a body string, or `{body, shell, name}` - both spellings are in the tree
	//	rect          a named rect (`middle`), a pixel list, or a list of fractions
	//	disk_esp_mib  a number, or a `{{ }}` template
	//
	// THEY WERE ALL DECLARED WITH ONE SHAPE, which is what happens when the Kind is documentation
	// rather than enforcement: nothing read it, so it drifted from what the executors accept. NOW IT IS
	// READ, so each was reconciled against the files rather than the other way round.
	// IT NAMES A FILE. THERE IS NO `{from, copy, overlay}` FORM, and the executor refuses one by name:
	//
	//	`disk:` names a FILE, not a mapping: write `disk: disk.qcow2`.
	//	The {from, copy} form was never used by any file in this repository
	//
	// The old Summary described that form for as long as the code rejected it. What actually happens:
	// `disk:` alone names the file `boot:` will start; `disk:` beside `disk_gb:` CREATES the file -
	// two actions behind one key, decided by whether `disk_gb:` is present.
	"disk": {Name: "disk", Kind: KindString,
		Summary: "the disk file: alone, it names the one `boot:` starts; beside `disk_gb:` it " +
			"creates it at that size"},
	"disk_from": {Name: "disk_from", Kind: KindString,
		Summary: "the source image a disk: step takes"},
	"disk_copy": {Name: "disk_copy", Kind: KindString,
		Summary: "move or copy the source: move, copy, reflink"},
	"disk_overlay": {Name: "disk_overlay", Kind: KindBool,
		Summary: "run on a throwaway diff over the read-only source"},
	"disk_gb": {Name: "disk_gb", Kind: KindTemplated,
		Summary: "size of a disk this step creates"},
	// THE PARTITION TABLE, FOR AN INSTALLER THAT EXPECTS ONE TO EXIST.
	//
	// Android-x86's first pane lists the partitions it FINDS; given a blank disk it hands the run to
	// cgdisk, whose seventeen line prompts take `260M` and `EF00` as TYPED TEXT and where one
	// misaligned key silently retypes the ESP as "Linux filesystem".
	"disk_gpt": {Name: "disk_gpt", Kind: KindBool,
		Summary: "partition the created disk: a 260 MiB ESP typed EF00 at LBA 2048, Linux on the rest"},
	"disk_esp_mib": {Name: "disk_esp_mib", Kind: KindTemplated,
		Summary: "size of that ESP; 260 is what the installer draws as 0.25GB"},
	"disk_settle": {Name: "disk_settle", Kind: KindFloat,
		Summary: "wait until the target qcow2 stops growing for this many quiet seconds"},
	"disk_min_mb": {Name: "disk_min_mb", Kind: KindInt,
		Summary: "with disk_settle: only start waiting once it holds this many allocated MiB"},

	// --- the screen: act ---------------------------------------------------------------
	// `click:` IS WHAT `at:` ALWAYS MEANT, AND THE OLD NAME WAS A PREPOSITION.
	//
	// A step that says `at: [1331, 785]` reads as "at this coordinate" and leaves the verb to be
	// inferred from the key next to it. It is a CLICK - one press at one pixel - and beside `double:`
	// the pair now says what they do:
	//
	//	- name: welcome
	//	  click: [1331, 785]      one press
	//	- name: open the pane
	//	  double: [960, 540]      two presses, with the gap that makes them a double-click
	//
	// NO COMPATIBILITY ENTRY FOR `at:`. The rule in this repository is that a name is renamed in the
	// same commit as its files, and a table of old spellings is a second vocabulary that drifts from
	// the first - the same argument that deleted the `python:` entries.
	"click": {Name: "click", Kind: KindPair,
		Summary: "click this [x, y]. Fixed coordinates, never OCR - a button's position is a " +
			"measured fact about a screen, and a search for it is a guess that moves"},
	"double": {Name: "double", Kind: KindPair,
		Summary: "double-click [x, y]; separate from `click` because the gap between the two " +
			"clicks is what makes it a double-click"},
	"at_button": {Name: "at_button", Kind: KindRect,
		Summary: "[x0, y0, x1, y1] - click the centre of the coloured fill inside this region. " +
			"The ONE lookup this repository allows, for a control whose position moves per pane " +
			"and whose fill is the only one of its kind in that strip"},
	"at_fallback": {Name: "at_fallback", Kind: KindPair,
		Summary: "[x, y] - with `at_button`: the measured position to click when the fill is not " +
			"found. For a control that CAN be missing (an icon behind a wallpaper change) rather " +
			"than one that cannot (a footer button whose absence means a step above failed)"},
	// `at_color:` FINDS A CONTROL BY ITS COLOUR AND CLICKS ITS CENTRE, WHICH NEEDS NO GEOMETRY.
	//
	// IT REPLACED `at_window`, whose whole premise was that a control's offset from its window is
	// constant - true, and useless, because finding the window's edge was itself an unreliable
	// measurement. Four attempts at Disk Utility's Erase button (592,495 / 810,376 / 807,367 / 1289,15)
	// were four attempts at the SCAN, not at the button.
	//
	// A COLOUR IS A FACT THE COMPOSITOR DRAWS: macOS paints the green zoom control rgb(40,200,64) on
	// every run, at every window height, in every Space. `at_color: [40, 200, 64]` is the whole
	// instruction, and `color_tolerance:` says how far a pixel may sit from it for antialiasing.
	// `at_color: [40, 200, 64]` IS AN RGB TRIPLE, NOT A RECTANGLE - the Kind said `KindRect` and nothing
	// read it, so the documentation and the executor had disagreed since the key was written.
	"at_color": {Name: "at_color", Kind: KindColor,
		Summary: "[r, g, b] - click the centre of the largest blob of this colour. For a control " +
			"the window draws in a fixed colour, which is how a macOS window button is found without " +
			"knowing where the window is"},
	"color_tolerance": {Name: "color_tolerance", Kind: KindInt,
		Summary: "with `at_color`: how far a pixel may be from the target colour and still count, " +
			"for antialiasing. 45 matches macOS's window buttons"},
	"color_region": {Name: "color_region", Kind: KindRect,
		Summary: "with `at_color`: [x0, y0, x1, y1] to search inside, when the colour could appear " +
			"somewhere else on the screen too"},
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
	// THERE IS NO `repeat:`. IT WAS A DEAD KEY: fourteen of them across eight files, a vocabulary
	// entry describing "(count, settle) for a footer button pressed the same way N times", and NO
	// DISPATCHER BRANCH ANYWHERE THAT READ IT - so `repeat: [10, 4]` pressed nothing. The vocabulary
	// test that requires every key to have a branch did not catch it because `repeat` was never
	// claimed as executable, only declared.
	//
	// A LOOP IS `loop:`, and a fixed number of presses is a `do:` list. Both are visible in the file.
	"sweep": {Name: "sweep", Kind: KindMapping,
		Summary: "a band of clicks at one x, stopping when the sheet opens"},
	"press": {Name: "press", Kind: KindString,
		Summary: "press a named control rather than coordinates"},
	"change_settle": {Name: "change_settle", Kind: KindFloat,
		Summary: "with wait_for_change: how long the screen must stay unchanged"},
	"change_timeout": {Name: "change_timeout", Kind: KindFloat,
		Summary: "with wait_for_change: how long to wait for a change at all"},

	// --- the screen: read --------------------------------------------------------------
	// `until_console:` WAS HERE AND IS `until: {from: console}` NOW.
	//
	// The two steps differed in ONE thing - where the text came from - and that made a dead key
	// (`until:` 0 uses) beside a live one (`until_console:` 4). The source is a parameter:
	//
	//	until: {from: console, words: [...], timeout: 180, blind_after: 60}
	//
	// The serial log is the firmware's own words, which a screenshot cannot see: a Windows install
	// ISO prints its boot prompt there and waits five seconds for a key.
	"blind_after": {Name: "blind_after", Kind: KindInt,
		Summary: "with until_console: go on after this many seconds whether or not the text " +
			"appeared, for media that boot with no prompt at all"},
	"until": {Name: "until", Kind: KindWords,
		Summary: "words that end a wait loop"},
	"until_any": {Name: "until_any", Kind: KindWordSets,
		Summary: "the alternatives form of `until:`: ONE of these word-sets must be on screen. " +
			"`doUntil` reads it beside `until:` and joins the two into one list of alternatives"},
	"until_dark": {Name: "until_dark", Kind: KindBool,
		Summary: "wait until the screen goes black - an installer's own start signal"},
	"wait_for_change": {Name: "wait_for_change", Kind: KindBool,
		Summary: "wait until the screen changes rather than for particular words"},
	"poll": {Name: "poll", Kind: KindFloat,
		Summary: "seconds between looks in an until: loop"},
	"timeout": {Name: "timeout", Kind: KindTemplated,
		Summary: "seconds THIS step may wait. THE DEFAULT DEPENDS ON THE VERB, because the waits " +
			"are not one thing: ssh and require_ssh 900, until from: console 180, disk_settle 3600, " +
			"adb_connect 300, adb 60, a desktop 900, a shutdown grace 0. AND ON A `boot:` IT IS THE " +
			"BUDGET THE FOLLOWING WAIT INHERITS, because a boot returns before the guest is up - " +
			"previously the number was written 53 times and read by nothing"},
	// `keep_alive:` IS GONE. It was written `true` in all 25 places it appeared and never once
	// `false`, so it was not a switch - it was the behaviour, spelled as an option. A screen that
	// blanks during a boot is normal, and both waits that need to survive it now wake it
	// unconditionally: see `wakeTheScreen` in internal/run.
	"scale": {Name: "scale", Kind: KindInt,
		Summary: "enlargement for a cropped read; small text needs it"},
	"screenshot": {Name: "screenshot", Kind: KindString,
		Summary: "save the screen to this path, for a post-mortem nobody had to ask for"},
	"desktop": {Name: "desktop", Kind: KindString,
		Summary: "wait until a desktop is on screen; options include family and timeout"},
	"ssh": {Name: "ssh", Kind: KindScript,
		Summary: "a command run over ssh IN the guest; the exit status is the verdict"},
	"require_ssh": {Name: "require_ssh", Kind: KindBool,
		Summary: "wait for sshd before the steps that use it"},
	"ssh_probe": {Name: "ssh_probe", Kind: KindString,
		Summary: "the command that wait runs; `true` is not a Windows command"},
	"ssh_after_reboot": {Name: "ssh_after_reboot", Kind: KindBool,
		Summary: "on a require_ssh: wait out the old daemon first, or the wait passes on the " +
			"process that is about to die"},
	// TWO FORMS, AND THE SECOND REPLACED A STEP.
	//
	//	scp: [local-file, remote-path]                    copy a file an earlier step made
	//	scp: {template: launcher, to: /tmp/x}             RENDER one of this build's bootstrap
	//	                                                  files and copy it, in one step
	//
	// The second exists because all 16 `write:` steps in the tree were followed immediately by the
	// `scp:` of what they had just written: "make this" and "copy the thing you made" were two
	// names for one operation.
	"scp": {Name: "scp", Kind: KindAny,
		Summary: "copy a file in. [local-file, remote-path], or {template: KIND, to: REMOTE} to " +
			"render a bootstrap file (launcher, macos_agent_plist, windows_task) and copy it"},
	// `script:` WAS HERE, AND IT IS THE FILE BESIDE THE WALKTHROUGH NOW.
	//
	// It was `scp` + `ssh` in one key: write the body on the host, copy it in, run it by path. I
	// argued to keep it as an "escape valve" because inlining a 6600-character PowerShell body into
	// `ssh: "..."` needs dozens of escaped quotes - and that argument was answering the wrong
	// question. The body did not have to be IN the file. Once `scp:` could find a file BESIDE the
	// walkthrough, the two steps it always was, written plainly:
	//
	//	scp: [install-powershell-7.ps1, C:/Users/{{ user }}/install-powershell-7.ps1]
	//	ssh: powershell -NoProfile -ExecutionPolicy Bypass -File C:/Users/{{ user }}/install-powershell-7.ps1
	//
	// MEASURED on the sixteen bodies it held: 42,000 characters of escaped script across two files,
	// and extracting them took windows/11/steps/dev.walk from 46,315 bytes to 6,932. The scripts are
	// now real files a shell can lint, with syntax highlighting and no escaping at all.
	//
	"adb": {Name: "adb", Kind: KindScript,
		Summary: "a command run over adb IN the guest; Android's remote access is adbd and " +
			"there is no sshd until a stage installs one"},
	"adb_privileged": {Name: "adb_privileged", Kind: KindBool,
		Summary: "prove the adb shell can change system state - a write that reads back. NOT " +
			"`adb root`: this guest is a `user` build with ro.debuggable=0 and no `su`, and the " +
			"shell it already has owns appops, settings, cmd and setprop"},
	"adb_connect": {Name: "adb_connect", Kind: KindBool,
		Summary: "attach to the guest's adbd on its forwarded port and prove it answers"},
	"adb_serial": {Name: "adb_serial", Kind: KindString,
		Summary: "the serial to use"},

	// --- the host's share ---------------------------------------------------------------
	// THE FOUR `share_*` KEYS ARE GONE, AND THIS IS WHERE THEY WERE.
	//
	// `share_mount:` had not mounted anything since the transport became HTTP - the implementation's
	// own comment said "IT USED TO MOUNT AN SMB SHARE". It ran `curl` in the guest over the ssh
	// session the file already had, which is a command a file can write itself; windows/vm-configure
	// wrote it by hand in the very next step. `share_serve:` set one variable on a listener that was
	// already running, `share_family:` chose between `curl` and `Invoke-WebRequest` (with `macos` and
	// `linux` taking the SAME branch), and `share_dir:` was used zero times in the whole tree.
	//
	// The share is now always on at `~/shared`; see internal/daemon/share.go. A file that wants to
	// read from it writes the URL.

	// --- panes: the macOS shape ---------------------------------------------------------
	"when": {Name: "when", Kind: KindWords,
		Summary: "a pane: the words on screen that mean this step applies"},
	"do": {Name: "do", Kind: KindList,
		Summary: "a pane's actions, in order, run in one pass"},
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
	// THE UEFI FALLBACK PATH, PUT ON A DISK THAT IS NOT RUNNING.
	//
	// AN INSTALLER THAT REGISTERS ITS LOADER WITH `efibootmgr` PUTS THE ENTRY IN THE NVRAM, NOT ON
	// THE DISK, and this repository makes a FRESH variable store for every boot - deliberately, so a
	// guest cannot inherit an entry naming a disk somebody else wrote. Firmware with an empty NVRAM
	// looks in exactly one place:
	//
	//	\EFI\BOOT\BOOTX64.EFI
	//
	// which is why a disk whose installer wrote /EFI/BLISSOS still boots to
	//
	//	BdsDxe: No bootable option or device was found.
	//
	// This step copies the loader tree to that path and rewrites the config's `search.fs_uuid`, a
	// UUID recorded BEFORE the installer formatted the partition - so it names a filesystem that no
	// longer exists.
	// `esp_fallback` IS ONE ACTION WITH TWO ARGUMENTS, AND THEY WERE TWO KEYS AT THE SAME LEVEL.
	//
	//	esp_fallback: disk             which disk to repair
	//	esp_from: /EFI/BLISSOS         where the loader is inside its ESP
	//
	// SIBLING KEYS LOOK LIKE TWO ACTIONS. `esp_from:` read as something a step could do on its own,
	// and `boot`'s `os:`/`iso:` have the same shape for the same reason - the vocabulary has no
	// nesting, so an argument is a key beside its action. That is what `Consumes` in roles.go exists
	// to declare, and what makes `esp_from:` an ARGUMENT rather than a second action.
	//
	// THE MAPPING FORM IS BETTER AND IS NOW ACCEPTED:
	//
	//	esp_fallback: {disk: disk, from: /EFI/BLISSOS}
	//
	// A reader sees one thing with two settings rather than two adjacent words whose relationship is
	// only in the prose. The flat form still works, because files use it.
	"esp_from": {Name: "esp_from", Kind: KindString,
		Summary: "with the FLAT form of `esp_fallback:`: the loader directory inside the ESP, as " +
			"in `esp_from: /EFI/BLISSOS`. Required - there is no default, because a distribution's " +
			"own name in a primitive is wrong for the next guest. Prefer " +
			"`esp_fallback: {disk, from}`"},
	"esp_fallback": {Name: "esp_fallback", Kind: KindAny,
		Summary: "copy a disk's EFI loader tree to \\EFI\\BOOT and repair its config search. " +
			"The value is the disk (`disk` for the one this stage built) or {disk, from}, where " +
			"`from` is the loader directory inside the ESP"},
	// --- the three-step model ---------------------------------------------------------------
	//
	// ONE OCR STEP, ONE CONDITION STEP, AND A LOOP THAT NAMES ALL THREE. The six keys they replace
	// (`until`, `until_any`, `expect`, `expect_any`, `unless`, `unless_any`) each meant "read the
	// screen and decide", and they could sit inside a click step - so a failure said `did not
	// advance` whether the click missed or the crop was wrong, and 47 of 70 readings covered the
	// whole frame because `region:` was optional.
	// `ocr:` IS GONE: `see: {name: X, of: [text]}` IS the same operation.
	//
	// MEASURED: `ocr:` 118 uses against `see:`'s handful, and the two produced IDENTICAL readings -
	// `seeText` and `doOCR` both crop a rect and run tesseract on it. Keeping both meant a reader had
	// to know that a word list came from one key and a number from another, when the only difference
	// is what the reading is USED for.
	//
	// IT IS ANSWERED BY THE ONE QUESTION THAT MATTERS: "is this a measurement?" A word list is a
	// measurement of what the screen says. So it is `see:`, and `of:` names which parts.

	// --- `see:` and `want:`: a picture is a VALUE --------------------------------------------
	//
	// THE PROBLEM THESE TWO SOLVE IS COUNTED, not a matter of taste. `internal/screen` is 1219 lines,
	// because every question about a picture is a Go function reached through a dispatch table -
	// `desktop: linux/cinnamon` and eight siblings. So a new screen shape is new Go, and new Go is an
	// image build. The reason it has to be Go at all is that the COMPARISON against the thresholds
	// lived beside the MEASUREMENT, so moving one threshold was a rebuild.
	//
	// MEASURED, from the 57 files and 1429 steps: 38 ocr steps of which 34 crop the whole frame and
	// 38 restate `region_at`, which the header already says; 61 steps exist only to be half of one
	// thought; 469 steps carry `wait_after`, and 28 of those are a timed guess immediately followed by
	// an actual measurement of the same screen - which is the bug class that failed cachyos and
	// endeavouros on CI.
	//
	// SO: A MEASUREMENT IS CODE, A VERDICT IS DATA.
	//
	//	- see:
	//	    name: bottom
	//	    rect: bottom 4.5%
	//	    of: [mean, spread, sat, flat_rows]
	//	- want:
	//	    name: desktop_up
	//	    any:
	//	      - bottom.spread: {less_than: 42}
	//	      - bottom.flat_rows: {at_least: 3}
	//
	// `see:` binds NUMBERS with names; `want:` binds a BOOLEAN from them. Together they are `ocr:` +
	// `check:` generalised from words-only to any measurement, and they make a desktop test a
	// sentence in the file rather than a function in the binary.
	//
	// THE VERDICT IS DATA AND IS PROVEN FOR THE THREE MEDIA ALREADY IN testdata: `measure_test.go`
	// evaluates this exact rule against mint, zorin and a BIOS menu, and reproduces the verdicts the
	// Go reaches - which is what makes `IsCinnamonDesktop` deletable rather than merely redundant.
	// `see:` CAPTURES ONE FRAME AND NAMES IT. IT MEASURES NOTHING.
	//
	//	see:   {name: shot}
	//	check: {name: dark, from: shot, mode: mean_between, rect: bottom 4.5%, range: [0, 60]}
	//
	// THE REGION BELONGS TO THE QUESTION, NOT THE CAPTURE. A `see:` used to carry one `rect:` and one
	// `of:`, so three questions about one screen cost three captures - three frames at three moments,
	// two of which could disagree about what the screen said.
	"see": {Name: "see", Kind: KindShot,
		Summary: "capture one frame and bind it under `name:`. Every `check:` that follows asks its " +
			"own question about THAT frame, with its own `rect:`"},

	// --- `check:`'s criteria, and the relations inside them -------------------------------
	//
	// THESE ARE SUBKEYS: they are written inside `check:` and never on a step. They are declared
	// here for the same reason every other key is - the validator refuses an unknown key, and a
	// subkey that is not declared would make a legitimate file fail to load. `audit_test.go` reports
	// any declared key that the table omits, which is what keeps this list and the document in step.
	//
	// THE THREE TEXT COMPARISONS ANSWER THREE QUESTIONS, and the split is measured rather than
	// tidy: `contains:` asks whether the region CONTAINS words, `like:`/`ratio:` asks whether it IS
	// a sentence, and `fuzzy:` is the tolerance `contains:` needs when a glyph is misread.

	// `settle:` IS `wait:` AND `wait_after:` WITH ONE NAME.
	//
	// MEASURED: `wait_after` appeared 627 times and `wait` 118 - the same key under two names, and
	// the pair was 36 to 1 because the 627 was a timed GUESS after an action. One in three steps
	// carried a guess, and 28 of those guesses were immediately followed by a real measurement of
	// the same screen: that is the bug class that failed cachyos and endeavouros on CI, where the
	// guess was right on the measuring machine and wrong on the runner.
	//
	// `settle:` is the honest name for what both did: "wait this long because there is no fact to
	// wait on". A step that CAN wait on a fact must - the validator reports a `settle:` followed by
	// a `check:` of the same region, because the fact should have been used.
	"settle": {Name: "settle", Kind: KindFloat,
		Summary: "seconds to wait where there is NO fact to wait on; the older `wait:` and " +
			"`wait_after:` both become this"},
	// `wait_until:` IS THE OTHER HALF OF EVERY SHUTDOWN, AS A STEP.
	//
	// `shutdown: ssh` used to send the request and wait for qemu in one key, and its request was
	// refused (no `sudo`) - so the wait spent its whole grace on a machine nobody had asked to stop.
	// Sending is now `ssh: ... poweroff` and this is the waiting, visible in the file as its own step.
	"minimum": {Name: "minimum", Kind: KindFloat,
		Summary: "with `wait_until: console_idle|disk_idle|frame_still`: the number of seconds (or " +
			"MiB/s of growth) that counts as arrived. A wait on a THRESHOLD has no default because " +
			"'quiet' is not one number: a boot prompt is quiet for a second and an install pauses " +
			"for a minute between phases"},
	"wait_until": {Name: "wait_until", Kind: KindString,
		Summary: "wait for a CONDITION about the machine: `stopped` - its process has gone. " +
			"A file that sent `acpi: power` or `ssh: poweroff` says this next, and a file that " +
			"did not gets a clear message instead of a disk qemu still holds open"},

	"check": {Name: "check", Kind: KindMapping,
		Summary: "compare a named reading and bind a BOOLEAN: {name, from, mode, ...}. `mode:` says " +
			"HOW to compare. `must: true` makes a FALSE answer fail the step, which is what a file " +
			"means when it says the screen IS the reference"},
	"if": {Name: "if", Kind: KindString,
		Summary: "run `then:` when this named boolean is true and `else:` when it is false. The " +
			"boolean comes from a `check:` step; a branch is a plain step list"},
	"loop": {Name: "loop", Kind: KindMapping,
		Summary: "the parts written down: `max:`, `do:` (the action), `ocr:` (the reading -> text), " +
			"`check:` (the comparison -> boolean), `until: {fact: ...}` (tests the boolean). " +
			"Optionally `every: <seconds>` with `clock: [...]` - an action that runs ONCE when that " +
			"long has passed, whatever the screen says"},
	// THREE KINDS OF HOST-SIDE PREPARATION, AND `fetch:` WAS ALL THREE OF THEM.
	//
	// MEASURED FROM THE 23 USES IN THIS REPOSITORY, its eight values did three different jobs:
	//
	//	download a published file   linux_iso, windows_iso, virtio_win, recovery_macos, xcode
	//	build an artifact           opencore - reads apfs.efi out of the recovery image and
	//	                            assembles boot.img
	//	generate a file             answer_iso - renders a template and packs it into an ISO
	//
	// A NAME THAT SAYS "fetch" FOR ALL THREE HIDES THE BUILD AND THE GENERATE BEHIND A WORD THAT
	// PROMISES A DOWNLOAD, and a reader who saw `fetch: opencore` had no way to know it was reading
	// a 888 MiB recovery image.
	// `asset:`, `build:` AND `generate:` WERE ONE OPERATION UNDER THREE NAMES.
	//
	// All three dispatched to `doFetch` and differed only in which WORD the file chose: take a
	// published file, assemble one, render one. MEASURED: `asset:` 1 use, `build:` 2, `generate:` 1
	// - the distinction was never load-bearing, because the KIND is the value.
	"fetch": {Name: "fetch", Kind: KindString,
		Summary: "produce one host-side input: an installer image, a guest agent, a firmware, an " +
			"answer CD. The value names WHAT, not how it is obtained"},
	// `reference:` MAKES THE MEASUREMENT A COMPARISON, AND THE RESULT IS A NUMBER.
	//
	//	see: {name: screen, rect: [0,0,1920,1080], reference: reference/desktop.png}
	//	check: {name: same, from: screen, mode: number_between, range: [0.97, 1.0]}
	//
	// `see:` binds `screen.similarity` - the share of pixels that matched, 0..1 - exactly as it binds
	// `mean` and `spread`. It stays a MEASUREMENT, so `check:` still never reads the screen.
	//
	// MEASURED over four real CI recordings (ubuntu, windows, macos, freebsd): the same image scores
	// 1.0000, one second later 0.9910-0.9947, and two different screens 0.0002-0.0166.
	"template": {Name: "template", Kind: KindString,
		Summary: "with `generate: answer_iso`: the answer file to substitute into. It is a path in " +
			"the repository because the placeholders are the file's vocabulary, not a download"},

	// THERE IS NO `python:` AND NO `python_screen:`. The Go implementation has no interpreter, so
	// a file that names one is refused as an unknown key - which is the same answer a typo gets, and
	// the right one: a key that cannot be executed must not be in the vocabulary at all. Keeping it
	// here so it could be refused BY NAME was the `Deprecated` table, and that is deleted.
}

// Validate checks a step mapping against the vocabulary.
//
// A KEY THAT IS UNKNOWN FAILS THE FILE, and that is the whole point of this list: the Python
// implementation kept 85 keys alive by accepting whatever the executor happened to read, so a
// misspelled key was a step that did nothing and reported success. Two such key names are in this
// project's history already (`family:` then `os:`, `eject` spelled two ways).
// THERE IS NO DEPRECATED LIST. A NAME IS EITHER IN THE VOCABULARY OR IT IS REFUSED.
//
// THERE WAS ONE, and it existed so a renamed key could be refused with a sentence rather than a parse
// error. IT WAS REMOVED BECAUSE THE FILES ARE THE ONLY CALLER. Every walkthrough in this repository
// is renamed in the same commit as the vocabulary, so a stale name is not a user to be guided - it is
// a file that was not updated, and "unknown key" is the correct answer for it. A compatibility table
// with no external consumer is a second place the vocabulary is written down, and the two drift.
// THE BLIND-SLEEP RULE WAS RIGHT FOR ONE COMMIT AND IS NOW WRONG, AND SAYING SO IS THE POINT.
//
// `settle:` used to be `time.Sleep` and did not poll `ShouldStop`, so a long pause beside an action
// was a wall: `easyvm stop` sets a flag and nothing reads it until the pause ends. This validator
// therefore refused anything over ten seconds, and it did its job by failing all 24 linux files at
// once - because the migration in the previous commit had converted every `wait_after:` to `settle:`,
// including the twenty-second ones.
//
// THE FIX WAS IN `settle` RATHER THAN IN THE FILES: it polls in five-second steps now, exactly as
// `doWait` does, so ANY length is interruptible and there is nothing left to refuse. A validator that
// kept the rule would be forcing files to split a pause in two to satisfy a limit whose reason no
// longer exists - which is how a validator becomes a tax.
//
// `actionKeys` USED TO LIVE HERE AND WAS READ BY NOTHING - a second, stale list of "the keys that do
// something" beside the dispatcher's own switch order. It is `Roles` in roles.go now, which `Validate`
// actually enforces and which a test keeps honest against `runOne`.

// Validate refuses a key the vocabulary does not have, and a blind pause long enough that stopping
// the machine would not work.
func Validate(stepName string, mapping map[string]any) error {
	// ONE ACTION PER STEP, CHECKED BEFORE THE KEYS THEMSELVES.
	//
	// THE FAILURE THIS PREVENTS IS SILENT: a step that writes `double:` beside `check:` loads today,
	// and the dispatcher takes the `check` branch and returns, so the double-click never happens and
	// the step reports a verdict it was supposed to reach by clicking. Measured 63 such steps in the
	// tree this was written against. See roles.go.
	if err := CheckOneAction(stepName, mapping); err != nil {
		return err
	}
	// AND A STEP THAT ACTS MAY NOT BE `optional`. See the measurement in CheckOptionalDoesNotAct:
	// four optional greeter steps pressed Escape, clicked, typed and returned into a wizard.
	if err := CheckOptionalDoesNotAct(stepName, mapping); err != nil {
		return err
	}
	for name, value := range mapping {
		if key, ok := Keys[name]; ok {
			// THE NAME IS WHAT THE LOG SAYS, SO IT IS A STRING AND NOTHING ELSE.
			if name == "name" {
				continue
			}
			if err := checkKind(key, value); err != nil {
				return fmt.Errorf("step %q: %w", stepName, err)
			}
			continue
		}
		key, ok := Subkeys[name]
		if !ok {
			return fmt.Errorf("step %q has unknown key %q; an unknown key used to mean a step that did nothing",
				stepName, name)
		}
		// A SUB-KEY IS ALLOWED HERE ONLY WHEN THE STEP ALSO NAMES WHAT READS IT.
		//
		// `rect:` beside `click:` is real: the CLICK does not take a rectangle, the `when_not:` GATE
		// that qualified it does, and the dispatcher strips the region before the click runs. What is
		// not real is `{mean: 40}` - a step whose only key is an argument to a question nobody asked.
		//
		// BEFORE THE SPLIT BOTH LOADED, and the second one reached the dispatcher, which answered
		// "no implementation for this step's keys" - a true sentence about a mistake made earlier.
		owners := OwnersOf(name)
		if len(owners) == 0 {
			return fmt.Errorf("step %q writes `%s:`, which is only meaningful INSIDE another key's "+
				"value (%s). A step cannot say it at its own level", stepName, name, Subkeys[name].Summary)
		}
		consumed := false
		for _, owner := range owners {
			if _, present := mapping[owner]; present {
				consumed = true
			}
		}
		if !consumed {
			return fmt.Errorf("step %q writes `%s:`, which belongs to %s - and this step does not "+
				"name any of them. A sub-key without its owner reads as a step with no action",
				stepName, name, quoted(owners))
		}
		if err := checkKind(key, value); err != nil {
			return fmt.Errorf("step %q: %w", stepName, err)
		}
	}
	return nil
}

// OwnersOf names the keys whose value may legitimately carry this sub-key at step level.
//
// IT IS THE INVERSE OF `Consumes`, plus the five keys whose arguments live in the key's own mapping
// rather than beside it: `loop:` owns `max`/`every`/`clock`/`fact`, `if:` owns `then`/`else`, and
// `check:` owns `must`. Those are still sub-keys - they are written inside a mapping in every file -
// but the action that owns them is the one in the step, so a step that names it may carry them.
func OwnersOf(subkey string) []string {
	var out []string
	for action, args := range Consumes {
		for _, a := range args {
			if a == subkey {
				out = append(out, action)
				break
			}
		}
	}
	if owners, ok := nestedOwners[subkey]; ok {
		out = append(out, owners...)
	}
	sort.Strings(out)
	return out
}

// nestedOwners is the part `Consumes` cannot express: keys written inside another key's MAPPING.
var nestedOwners = map[string][]string{
	"max":   {"loop"},
	"every": {"loop"},
	"clock": {"loop"},
	"fact":  {"loop"},
	"then":  {"if"},
	"else":  {"if"},
	"must":  {"check"},
}

// quoted renders owners as `a:`, `b:` and `c:` for a failure message.
func quoted(owners []string) string {
	parts := make([]string, len(owners))
	for i, o := range owners {
		parts[i] = "`" + o + ":`"
	}
	switch len(parts) {
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " or " + parts[1]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " or " + parts[len(parts)-1]
}
