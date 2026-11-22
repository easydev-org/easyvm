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
	// KindPairOrMapping is [x, y] OR a mapping that says how the point is named.
	//
	// IT EXISTS FOR `press:` AND FOR NOTHING ELSE, and it is the fold that removes five step keys:
	// `{button: [x, y]}`, `{button: [x, y], fallback: [x, y]}` and `{colour: [r, g, b]}` used to be
	// `at_button:`, `at_fallback:` and `at_color:`. The value form is what makes ONE verb cover them.
	KindPairOrMapping
	// KindStringOrMapping is a name, OR a mapping that adds facts to it.
	//
	// IT EXISTS FOR `disk:`: a line either names a file or creates one, and the five keys that used
	// to say "and 128 GiB, GPT, at least 4000 MiB, settled after 300 s" are fields of that mapping.
	KindStringOrMapping
	// KindWordsOrNone is a word list, or `{none: <word-sets>}` for the negative form.
	//
	// IT EXISTS SO ONE KEY CARRIES BOTH POLARITIES OF THE SAME QUESTION, and so `when_not_any:` (0
	// uses in the tree, and the alternatives form of a negative gate) has a home that does not need
	// its own name.
	KindWordsOrNone
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
	// may write either. What it REFUSES is anything else - a `read:` carrying parameters is the old
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
	"name": {Name: "name", Kind: KindString,
		Summary: "what the log calls this step; required"},

	"optional": {Name: "optional", Kind: KindBool,
		Summary: "a step that may fail without failing the stage - only for a check whose " +
			"NEGATIVE result is information, never for a verdict"},

	"needs": {Name: "needs", Kind: KindList,
		Summary: "files that must be on disk before this stage runs; the first step of every stage " +
			"names its medium here so a missing input fails in the first second rather than forty " +
			"minutes in"},

	"when_not": {Name: "when_not", Kind: KindWords,
		Summary: "the negative gate: run this step only if the screen does NOT show these words"},

	"when_not_any": {Name: "when_not_any", Kind: KindWordSets,
		Summary: "the alternatives form of `when_not:`: the step runs only if the screen shows " +
			"NONE of these word-sets"},

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

	"memory_mb": {Name: "memory_mb", Kind: KindInt,
		Summary: "the guest's RAM in MiB; 4096 when not said. A guest that installs from a " +
			"memdisk needs more than the default"},

	"cpus": {Name: "cpus", Kind: KindInt,
		Summary: "how many vCPUs the guest gets; 4 when not said"},

	"kernel": {Name: "kernel", Kind: KindString,
		Summary: "with `microvm:`: the kernel qemu hands the guest, because a microvm has no " +
			"firmware to read a boot sector"},

	"initrd": {Name: "initrd", Kind: KindString,
		Summary: "with `microvm:`: the initramfs beside the kernel"},

	"kernel_append": {Name: "kernel_append", Kind: KindString,
		Summary: "with `microvm:`: the guest's OWN kernel command line. Required for any microvm " +
			"whose root is a real filesystem - the default is Alpine's diskless `modloop=/dev/vda`, " +
			"which mounts OVER the root disk and drops the guest to an emergency shell"},

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

	"acpi": {Name: "acpi", Kind: KindString,
		Summary: "send one ACPI message: `power` (the power button) or `reset`. It SENDS ONLY - " +
			"whether the guest goes is `wait_until: stopped`, a step of its own"},

	"qga": {Name: "qga", Kind: KindString,
		Summary: "run one qemu-guest-agent command: ping, info, shutdown, exec, file-read, " +
			"file-write, set-time, interfaces. The only channel into a guest with no sshd and no " +
			"ACPI handler"},

	"compress": {Name: "compress", Kind: KindString,
		Summary: "flatten the overlay into this artifact; the path is relative to the repository " +
			"root, not to the work directory"},

	"disk": {Name: "disk", Kind: KindStringOrMapping,
		Summary: "the disk file, or a mapping that creates it with its own size and layout. " +
			"Alone it names the disk `boot:` starts; as a mapping it creates that disk, and the " +
			"fields are listed in Subkeys"},

	"disk_gb": {Name: "disk_gb", Kind: KindTemplated,
		Summary: "size of a disk this step creates"},

	"disk_gpt": {Name: "disk_gpt", Kind: KindBool,
		Summary: "partition the created disk: a 260 MiB ESP typed EF00 at LBA 2048, Linux on the rest"},

	"disk_esp_mib": {Name: "disk_esp_mib", Kind: KindTemplated,
		Summary: "size of that ESP; 260 is what the installer draws as 0.25GB"},

	"disk_settle": {Name: "disk_settle", Kind: KindFloat,
		Summary: "wait until the target qcow2 stops growing for this many quiet seconds"},

	"disk_min_mb": {Name: "disk_min_mb", Kind: KindInt,
		Summary: "with disk_settle: only start waiting once it holds this many allocated MiB"},

	// `press` IS THE SHAPE OF A CLICK, WHICH IS THE POSITION PART OF `press`. It is a step-level key
	// because the executor reads it there: the coordinate pair is the argument, and `press` says the
	// step is an input action at all.
	"click": {Name: "click", Kind: KindPairOrMapping,
		Summary: "with `press:`: where the pointer goes, as [x, y]"},
	// `press` ARGUMENTS THAT SIT AT STEP LEVEL, because that is where the executor reads them.
	"text": {Name: "text", Kind: KindString,
		Summary: "with `press:`: the string to type"},
	"colour": {Name: "colour", Kind: KindList,
		Summary: "with `press:`: click the largest blob of this [r, g, b]"},
	"button": {Name: "button", Kind: KindRect,
		Summary: "with `press:`: click the control found by this label"},
	"at": {Name: "at", Kind: KindPair,
		Summary: "with `press text:`: the point to click before typing"},
	"anchor": {Name: "anchor", Kind: KindString,
		Summary: "with `press:`: measure the point from `bottom` instead of the top-left"},
	"fallback": {Name: "fallback", Kind: KindPair,
		Summary: "with `press button:`: the point to use when no control carries that label"},
	"gap": {Name: "gap", Kind: KindFloat,
		Summary: "with `press text:`: seconds between keystrokes"},
	"copy": {Name: "copy", Kind: KindAny,
		Summary: "copy one file into the guest: the local path, and the remote path as a child"},
	"stop": {Name: "stop", Kind: KindAny,
		Summary: "stop the machine: kill, ssh, acpi or guest_agent"},
	"method": {Name: "method", Kind: KindString,
		Summary: "with `stop:`: how to stop the machine - kill, ssh, acpi or guest_agent"},
	"save": {Name: "save", Kind: KindString,
		Summary: "with `stop:`: publish the disk to this path after the machine is gone"},
	"via": {Name: "via", Kind: KindString,
		Summary: "with `run:`: where it runs - console, ssh or adb"},
	"press": {Name: "press", Kind: KindAny,
		Summary: "click at a point. `[x, y]` is a coordinate; `{button: [x, y]}` is a point " +
			"inside a window; `{button: [x, y], fallback: [x, y]}` adds a second place to try; " +
			"`{colour: [r, g, b]}` is the largest blob of one colour. Fixed positions, never OCR " +
			"- a button's position is a measured fact about a screen, and a search for it is a " +
			"guess that moves"},

	"double": {Name: "double", Kind: KindPair,
		Summary: "double-click [x, y]; separate from `press` because the gap between the two " +
			"clicks is what makes it a double-click"},

	"at_button": {Name: "at_button", Kind: KindRect,
		Summary: "[x0, y0, x1, y1] - click the centre of the coloured fill inside this region. " +
			"The ONE lookup this repository allows, for a control whose position moves per pane " +
			"and whose fill is the only one of its kind in that strip"},

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

	"blind_after": {Name: "blind_after", Kind: KindInt,
		Summary: "with until_console: go on after this many seconds whether or not the text " +
			"appeared, for media that boot with no prompt at all"},

	"until": {Name: "until", Kind: KindWords,
		Summary: "words that end a wait loop"},

	"until_any": {Name: "until_any", Kind: KindWordSets,
		Summary: "the alternatives form of `until:`: ONE of these word-sets must be on screen. " +
			"`doUntil` reads it beside `until:` and joins the two into one list of alternatives"},

	"poll": {Name: "poll", Kind: KindFloat,
		Summary: "seconds between looks in an until: loop"},

	"timeout": {Name: "timeout", Kind: KindTemplated,
		Summary: "seconds THIS step may wait. THE DEFAULT DEPENDS ON THE VERB, because the waits " +
			"are not one thing: ssh and require_ssh 900, until from: console 180, disk_settle 3600, " +
			"adb_connect 300, adb 60, a desktop 900, a shutdown grace 0. AND ON A `boot:` IT IS THE " +
			"BUDGET THE FOLLOWING WAIT INHERITS, because a boot returns before the guest is up - " +
			"previously the number was written 53 times and read by nothing"},

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

	"scp": {Name: "scp", Kind: KindAny,
		Summary: "copy a file in. [local-file, remote-path], or {template: KIND, to: REMOTE} to " +
			"render a bootstrap file (launcher, macos_agent_plist, windows_task) and copy it"},

	"adb": {Name: "adb", Kind: KindScript,
		Summary: "a command run over adb IN the guest; Android's remote access is adbd and " +
			"there is no sshd until a stage installs one"},

	"adb_privileged": {Name: "adb_privileged", Kind: KindBool,
		Summary: "prove the adb shell can change system state - a write that reads back. NOT " +
			"`adb root`: this guest is a `user` build with ro.debuggable=0 and no `su`, and the " +
			"shell it already has owns appops, settings, cmd and setprop"},

	"adb_connect": {Name: "adb_connect", Kind: KindBool,
		Summary: "attach to the guest's adbd on its forwarded port and prove it answers"},

	"when": {Name: "when", Kind: KindWordsOrNone,
		Summary: "run this step only if the screen shows these words. `when: {none: [[a], [b, c]]}` " +
			"is the NEGATIVE form: run only if NONE of those word-sets is present (it replaces " +
			"`when_not_any:`)"},

	"do": {Name: "do", Kind: KindList,
		Summary: "a pane's actions, in order, run in one pass"},

	"release": {Name: "release", Kind: KindString,
		Summary: "which OS release is being driven; the Apple Account pane differs by release"},

	"write": {Name: "write", Kind: KindString,
		Summary: "write one of the login/bootstrap files on the HOST, for the scp: after it. " +
			"Kinds: launcher, macos_agent_plist, macos_mount_script, macos_mount_plist, " +
			"windows_task"},

	"esp_fallback": {Name: "esp_fallback", Kind: KindAny,
		Summary: "copy a disk's EFI loader tree to \\EFI\\BOOT and repair its config search. " +
			"The value is the disk (`disk` for the one this stage built) or {disk, from}, where " +
			"`from` is the loader directory inside the ESP"},

	"read": {Name: "read", Kind: KindShot,
		Summary: "capture one frame and bind it under `name:`. Every `expect:` that follows asks its " +
			"own question about THAT frame, with its own `rect:`"},

	"settle": {Name: "settle", Kind: KindFloat,
		Summary: "seconds to wait where there is NO fact to wait on; the older `wait:` and " +
			"`wait_after:` both become this"},

	"wait_until": {Name: "wait_until", Kind: KindString,
		Summary: "wait for a CONDITION about the machine: `stopped` - its process has gone. " +
			"A file that sent `acpi: power` or `ssh: poweroff` says this next, and a file that " +
			"did not gets a clear message instead of a disk qemu still holds open"},

	"expect": {Name: "expect", Kind: KindMapping,
		Summary: "compare a named reading and bind a BOOLEAN: {name, from, mode, ...}. `mode:` says " +
			"HOW to compare. `must: true` makes a FALSE answer fail the step, which is what a file " +
			"means when it says the screen IS the reference"},

	"if": {Name: "if", Kind: KindString,
		Summary: "run `then:` when this named boolean is true and `else:` when it is false. The " +
			"boolean comes from a `expect:` step; a branch is a plain step list"},

	"repeat": {Name: "repeat", Kind: KindMapping,
		Summary: "the parts written down: `max:`, `do:` (the action), `ocr:` (the reading -> text), " +
			"`expect:` (the comparison -> boolean), `until: {fact: ...}` (tests the boolean). " +
			"Optionally `every: <seconds>` with `clock: [...]` - an action that runs ONCE when that " +
			"long has passed, whatever the screen says"},

	"fetch": {Name: "fetch", Kind: KindString,
		Summary: "produce one host-side input: an installer image, a guest agent, a firmware, an " +
			"answer CD. The value names WHAT, not how it is obtained"},

	"template": {Name: "template", Kind: KindString,
		Summary: "with `generate: answer_iso`: the answer file to substitute into. It is a path in " +
			"the repository because the placeholders are the file's vocabulary, not a download"},
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
	// THE FAILURE THIS PREVENTS IS SILENT: a step that writes `double:` beside `expect:` loads today,
	// and the dispatcher takes the `expect` branch and returns, so the double-click never happens and
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
		// `rect:` beside `press:` is real: the CLICK does not take a rectangle, the `when_not:` GATE
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
// rather than beside it: `repeat:` owns `max`/`every`/`clock`/`fact`, `if:` owns `then`/`else`, and
// `expect:` owns `must`. Those are still sub-keys - they are written inside a mapping in every file -
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
