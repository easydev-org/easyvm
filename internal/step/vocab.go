package step

import "strings"

// THE VOCABULARY, IN ONE PLACE.
//
// A STEP IS ONE VERB AND ITS FIELDS, and this file is the only place that says which fields a verb
// reads. There used to be four tables that had to agree with each other - `Subkeys` for what is
// written inside a value, `Consumes` for what an action reads beside itself, `nestedOwners` for keys
// inside another key's MAPPING, and `Roles` for which of the three a key is - and every one of them
// could silently disagree with the others. MEASURED, all three failure modes happened while this
// migration was in progress:
//
//	a field listed as consumed but not declared  -> the argument was never recognised
//	a subkey with no owner                       -> the file that wrote it was refused
//	a key in `Keys` and not in `Roles`           -> a test failed on a key nobody had decided about
//
// SO THERE IS ONE TABLE. `Verbs` says what each verb reads; `Shared` says what every verb reads for
// the same reason. A key that is in neither is refused by name, and there is no third place for it to
// hide.

// Verb is one of the nine things a step can do, and the fields it reads.
type Verb struct {
	// Name is the word a file writes.
	Name string
	// Summary is what it DOES, in the imperative.
	Summary string
	// Fields are the keys this verb reads beside itself. Each carries its own Kind, so the shape of
	// a coordinate and the shape of a word list are checked where they are declared.
	Fields map[string]Key
	// Required is the field the verb cannot work without, or "" when any of its fields will do. It
	// exists so the error a person reads can say which key is missing rather than "nothing happened".
	Required string
	// Kind says what the action's OWN value looks like: a mapping for `check:`, a string for
	// `desktop:`, a list for `needs:`. It is the shape of the key itself, not of its fields.
	Kind Kind
}

// Shared is what every verb reads, and means the same thing on all of them.
//
// IT IS SEPARATE BECAUSE THESE ARE NOT ARGUMENTS: `timeout:` on a click and on a boot are the same
// number of seconds, and writing it into nine verbs would be nine places to change one rule.
var Shared = map[string]Key{
	"name": {Name: "name", Kind: KindString,
		Summary: "what the log calls this step; required"},
	"timeout": {Name: "timeout", Kind: KindTemplated,
		Summary: "how long this step may take, in seconds"},
	"optional": {Name: "optional", Kind: KindBool,
		Summary: "a step that may fail without failing the stage - only for a check whose NEGATIVE " +
			"result is information, never for a verdict"},
	"type_gap": {Name: "type_gap", Kind: KindFloat,
		Summary: "seconds between characters when text or a command is typed"},
	"settle": {Name: "settle", Kind: KindFloat,
		Summary: "seconds to wait where there is NO fact to wait on; the older `wait:` and " +
			"`wait_after:` both become this"},
	"when": {Name: "when", Kind: KindWordsOrNone,
		Summary: "run this step only when the screen says these words (`{none: ...}` for the " +
			"negative form)"},
	"when_not": {Name: "when_not", Kind: KindWords,
		Summary: "run this step only when the screen does NOT say these words"},
	"when_not_any": {Name: "when_not_any", Kind: KindWordSets,
		Summary: "run this step only when NONE of these word-sets is on screen"},
	"settle_before": {Name: "settle_before", Kind: KindFloat,
		Summary: "seconds to wait before the step acts"},
	// ---- THE CONDITION FIELDS -------------------------------------------------------------
	//
	// A QUESTION IS A QUESTION wherever it is asked: `check:` asks once, a `repeat`'s `until:` asks
	// again, and an `if:` chooses a branch on the answer. All three read these, and they mean the
	// same thing on all three - which is the definition of a shared word.
	"source": {Name: "source", Kind: KindString,
		Summary: "WHERE the question is asked - the name of a frame a `read:` captured, or " +
			"`console` for the serial line"},
	"mode": {Name: "mode", Kind: KindString,
		Summary: "HOW the frame is compared: like, mean_between, sat_between, spread_between, text, " +
			"contains, matches, equals"},
	"words": {Name: "words", Kind: KindList,
		Summary: "with `mode: text`: the word-sets, any one of which satisfies it"},
	"none": {Name: "none", Kind: KindWordSets,
		Summary: "the negative form of `words:`: none of these word-sets may appear"},
	"without": {Name: "without", Kind: KindList,
		Summary: "words that must NOT be on screen"},
	"rect": {Name: "rect", Kind: KindRect,
		Summary: "the region to measure, as a FRACTION (`middle`, `bottom 4.5%`) or " +
			"`[x0, y0, x1, y1]` pixels"},
	"rect_at": {Name: "rect_at", Kind: KindPair,
		Summary: "with a PIXEL `rect:`: [width, height] it was measured at, so it can be scaled"},
	"scale": {Name: "scale", Kind: KindInt,
		Summary: "enlargement for a cropped read; small text needs it"},
	"range": {Name: "range", Kind: KindList,
		Summary: "with a numeric mode: [low, high]"},
	"pattern": {Name: "pattern", Kind: KindString,
		Summary: "with `mode: matches`: the regular expression"},
	"ratio": {Name: "ratio", Kind: KindList,
		Summary: "with `mode: like`: [low, high] share of the picture that must match"},
	"reference": {Name: "reference", Kind: KindString,
		Summary: "with `mode: like`: the image to compare against"},
}

// Verbs is the nine things a step can do.
var Verbs = map[string]Verb{
	// ---- 1. `boot` -------------------------------------------------------------------------
	"boot": {Name: "boot",
		Summary: "start the machine this file owns, from the arguments this file writes",
		Fields: map[string]Key{
			// THE ONLY KEYS A BOOT TAKES. Everything else about a machine - its firmware, its CPU
			// flags, its disk order, its adapter, its sound card - is a qemu argument, and the file
			// that knows what machine it wants writes them.
			"args": {Name: "args", Kind: KindList,
				Summary: "the qemu argument pairs, in order, written out in full - a path is the " +
					"path qemu will see, because qemu runs in the work directory"},
			"dir": {Name: "dir", Kind: KindString,
				Summary: "the working directory qemu runs in, when it is not the run's own"},
			"machine": {Name: "machine", Kind: KindString,
				Summary: "the `-machine` value"},
			"incoming": {Name: "incoming", Kind: KindString,
				Summary: "a `snapshot:` file to load instead of booting"},
			"overlay": {Name: "overlay", Kind: KindBool,
				Summary: "whether the guest's writes go to a throwaway diff (the default) or " +
					"STRAIGHT TO THE DISK. `overlay: false` is for a stage whose diff does not " +
					"fit beside the disk it starts from: `publish` reads the disk AND the diff " +
					"and writes a third file, so the peak is the sum of three"},
		}},

	// ---- 2. `input` ------------------------------------------------------------------------
	"input": {Name: "input",
		Summary: "send input to the guest - a click, a keystroke, or text",
		Fields: map[string]Key{
			"click": {Name: "click", Kind: KindPairOrMapping,
				Summary: "the point to click, as [x, y] or `{button: [x, y], fallback: [x, y]}`"},
			"click_at": {Name: "click_at", Kind: KindPair,
				Summary: "with a `click:`/`double:`/`at:` coordinate: [width, height] it was " +
					"measured at, so a guest that draws another size can still be clicked"},
			"text": {Name: "text", Kind: KindString,
				Summary: "the string to type"},
			"key": {Name: "key", Kind: KindString,
				Summary: "a qemu qcode: ret, esc, tab, meta_l+q"},
			"colour": {Name: "colour", Kind: KindList,
				Summary: "click the largest blob of this [r, g, b]"},
			"button": {Name: "button", Kind: KindRect,
				Summary: "click the control found by this label"},
			"at": {Name: "at", Kind: KindPair,
				Summary: "with `text:`: the point to click before typing"},
			"anchor": {Name: "anchor", Kind: KindString,
				Summary: "measure the point from `bottom` instead of the top-left"},
			"fallback": {Name: "fallback", Kind: KindPair,
				Summary: "the point to use when no control carries the label"},
			"gap": {Name: "gap", Kind: KindFloat,
				Summary: "seconds between keystrokes while typing"},
			"clear": {Name: "clear", Kind: KindBool,
				Summary: "select-all and delete before typing"},
			"modifier": {Name: "modifier", Kind: KindString,
				Summary: "the modifier `clear:` uses; ctrl by default"},
			"double": {Name: "double", Kind: KindPair,
				Summary: "double-click at this point"},
			"at_color": {Name: "at_color", Kind: KindColor,
				Summary: "click the blob of this colour"},
			"color_region": {Name: "color_region", Kind: KindRect,
				Summary: "where to look for that colour"},
			"color_tolerance": {Name: "color_tolerance", Kind: KindInt,
				Summary: "how far from the colour still counts"},
			"at_button": {Name: "at_button", Kind: KindRect,
				Summary: "click the control at this point"},
			"at_bottom": {Name: "at_bottom", Kind: KindPair,
				Summary: "click at this point, measured from the bottom edge"},
		},
		Required: "click"},

	// ---- 3. `run` --------------------------------------------------------------------------
	"run": {Name: "run",
		Summary: "run one command in the guest, over the console, ssh or adb",
		Fields: map[string]Key{
			"via": {Name: "via", Kind: KindString,
				Summary: "where it runs: console (default), ssh, adb"},
			"terminal_wait": {Name: "terminal_wait", Kind: KindFloat,
				Summary: "seconds to wait for the console to answer"},
			"ssh_probe": {Name: "ssh_probe", Kind: KindString,
				Summary: "with `run:`: the command the wait runs"},
			"ssh_after_reboot": {Name: "ssh_after_reboot", Kind: KindBool,
				Summary: "wait out the old daemon first, or the wait passes on the process that is " +
					"about to die"},
		}},

	// ---- 4. `copy` -------------------------------------------------------------------------
	"copy": {Name: "copy",
		Summary: "copy one file into the guest",
		Fields: map[string]Key{
			"from": {Name: "from", Kind: KindString,
				Summary: "the local file, relative to the walkthrough"},
			"to": {Name: "to", Kind: KindString,
				Summary: "the path in the guest"},
			"template": {Name: "template", Kind: KindString,
				Summary: "render one of the built-in templates instead of copying a file: launcher, " +
					"macos_agent_plist, windows_task"},
		}},

	// ---- 5. `read` -------------------------------------------------------------------------
	"read": {Name: "read",
		Summary: "capture one frame and bind it to a name. It measures nothing and decides nothing; " +
			"the region and the question belong to the `check:` that follows",
		Fields: map[string]Key{}},

	// ---- 6. `check` ------------------------------------------------------------------------
	"check": {Name: "check",
		Summary: "ask ONE question about a picture and bind the answer. `source:` names the frame a " +
			"`read:` captured; `mode:` says how it is compared",
		Fields: map[string]Key{
			"contains": {Name: "contains", Kind: KindString,
				Summary: "with `mode: contains`: the substring to find"},
			"must": {Name: "must", Kind: KindBool,
				Summary: "a FALSE answer fails the step. Without it the check only binds a boolean"},
			"as": {Name: "as", Kind: KindString,
				Summary: "the name the boolean this check binds is stored under, for an `if:` to read"},
		},
		Required: "source"},

	// ---- 7. `control` ----------------------------------------------------------------------
	"control": {Name: "control",
		Summary: "a machine-level message rather than a guest action",
		Fields: map[string]Key{
			"acpi": {Name: "acpi", Kind: KindString,
				Summary: "one ACPI message: power, reset, sleep"},
			"qga": {Name: "qga", Kind: KindString,
				Summary: "one guest-agent command: ping, info, shutdown, file-read, file-write"},
			"wait_until": {Name: "wait_until", Kind: KindString,
				Summary: "wait for a condition about the machine: `stopped`"},
			"stopped": {Name: "stopped", Kind: KindBool,
				Summary: "wait until the machine's process has gone"},
			"path": {Name: "path", Kind: KindString,
				Summary: "with `qga: file-read`/`file-write`: the file in the GUEST"},
			"body": {Name: "body", Kind: KindString,
				Summary: "with `qga: file-write`: the bytes to write"},
			"minimum": {Name: "minimum", Kind: KindFloat,
				Summary: "with `wait_until:`: the number of seconds that counts as arrived"},
		}},

	// ---- 8. `if` ---------------------------------------------------------------------------
	"if": {Name: "if",
		Summary: "run `then:` when a boolean is true and `else:` when it is false",
		Fields: map[string]Key{
			"then": {Name: "then", Kind: KindList,
				Summary: "the steps that run when the boolean is true"},
			"else": {Name: "else", Kind: KindList,
				Summary: "the steps that run when it is false"},
		}},

	// ---- 9. `stop` -------------------------------------------------------------------------
	"stop": {Name: "stop",
		Summary: "stop the machine, and publish the disk in the same step",
		Fields: map[string]Key{
			"method": {Name: "method", Kind: KindString,
				Summary: "how to stop it: kill, ssh, acpi, guest_agent"},
			"save": {Name: "save", Kind: KindString,
				Summary: "publish the disk to this path after the machine is gone"},
			"compress": {Name: "compress", Kind: KindString,
				Summary: "the 1.0 spelling of publishing the disk"},
			"shutdown": {Name: "shutdown", Kind: KindString,
				Summary: "the 1.0 spelling of `method:`"},
			"min_mb": {Name: "min_mb", Kind: KindInt,
				Summary: "the smallest the published disk may be, in MiB - a check that the guest " +
					"really wrote what it claimed"},
		},
		Required: "method"},

	// ---- the statement ---------------------------------------------------------------------
	"repeat": {Name: "repeat",
		Summary: "run `do:` and then ask `until:`, up to `max:` times",
		Fields: map[string]Key{
			"do": {Name: "do", Kind: KindList,
				Summary: "the steps to run before each attempt"},
			"until": {Name: "until", Kind: KindMapping,
				Summary: "the condition that ends it: a `check:`-shaped block, or the name of one " +
					"of the machine's own facts such as `machine_stopped`"},
			"max": {Name: "max", Kind: KindInt,
				Summary: "the attempt ceiling. Required - a repeat with no ceiling never ends"},
			"pause": {Name: "pause", Kind: KindFloat,
				Summary: "seconds between attempts"},
			"nudge": {Name: "nudge", Kind: KindList,
				Summary: "actions that fire AT MOST ONCE, once the threshold has passed"},
			"nudge_after": {Name: "nudge_after", Kind: KindFloat,
				Summary: "with `nudge:`: the number of seconds before it fires"},
		},
		Required: "max"},
}

var Actions = map[string]Verb{
	"needs": {Name: "needs", Kind: KindList,
		Summary: "the files this stage reads must be on disk before it starts",
		Fields:  map[string]Key{}},
	"until": {Name: "until", Kind: KindWords,
		Summary: "wait for a console line, once",
		Fields: map[string]Key{
			"from": {Name: "from", Kind: KindString,
				Summary: "the channel: `console`"},
			"blind_after": {Name: "blind_after", Kind: KindInt,
				Summary: "seconds of silence before the wait starts looking"},
			"poll": {Name: "poll", Kind: KindFloat,
				Summary: "seconds between looks"},
		}},
	"until_any": {Name: "until_any", Kind: KindWordSets,
		Summary: "wait for any one of several console lines",
		Fields: map[string]Key{
			"from": {Name: "from", Kind: KindString, Summary: "the channel: `console`"},
		}},
	"acpi": {Name: "acpi", Kind: KindString,
		Summary: "send one ACPI message: power, reset, sleep",
		Fields:  map[string]Key{}},
	"qga": {Name: "qga", Kind: KindString,
		Summary: "one guest-agent command: ping, info, shutdown, file-read, file-write",
		Fields: map[string]Key{
			"path": {Name: "path", Kind: KindString,
				Summary: "the file in the GUEST"},
		}},
	"wait_until": {Name: "wait_until", Kind: KindString,
		Summary: "wait for a condition about the machine: `stopped`",
		Fields:  map[string]Key{}},
	"disk": {Name: "disk", Kind: KindStringOrMapping,
		Summary: "make one disk, or name the one a machine starts from",
		// THE MAPPING FORM IS WHAT THE OLD VERBS BECAME. `disk_gb`, `disk_gpt`, `disk_esp_mib`,
		// `disk_min_mb` and `disk_settle` used to be keys of their own beside a `disk:` that named a
		// file - six keys for one file's size and layout. They are fields now, and the two flat keys
		// below stay because a boot's argument list still writes them.
		Fields: map[string]Key{
			"file": {Name: "file", Kind: KindString,
				Summary: "the disk file; required when `disk:` is a mapping"},
			"size": {Name: "size", Kind: KindTemplated,
				Summary: "how big to make it, in GiB"},
			"gpt": {Name: "gpt", Kind: KindBool,
				Summary: "write a GPT partition table"},
			"esp_mib": {Name: "esp_mib", Kind: KindTemplated,
				Summary: "the EFI system partition's size, in MiB"},
			"min_mb": {Name: "min_mb", Kind: KindTemplated,
				Summary: "the smallest a finished disk may be, in MiB"},
			"wait": {Name: "wait", Kind: KindTemplated,
				Summary: "wait this long for the disk file to stop growing"},
			"disk_gb": {Name: "disk_gb", Kind: KindTemplated,
				Summary: "how big to make it, in GiB"},
			"disk_gpt": {Name: "disk_gpt", Kind: KindBool,
				Summary: "write a GPT partition table"},
			"disk_esp_mib": {Name: "disk_esp_mib", Kind: KindTemplated,
				Summary: "the EFI system partition's size, in MiB"},
		}},
	"write": {Name: "write", Kind: KindString,
		Summary: "write one of the built-in templates to disk",
		Fields: map[string]Key{
			"template": {Name: "template", Kind: KindString,
				Summary: "which template"},
			"os": {Name: "os", Kind: KindString,
				Summary: "the guest family the template is for"},
			"release": {Name: "release", Kind: KindString,
				Summary: "the release the template names"},
		}},
	"screenshot": {Name: "screenshot", Kind: KindString,
		Summary: "save the screen to this path, for a post-mortem nobody had to ask for",
		Fields:  map[string]Key{}},
	"ssh": {Name: "ssh", Kind: KindScript,
		Summary: "run a command over ssh; the exit status is the verdict",
		Fields: map[string]Key{
			"terminal_wait": {Name: "terminal_wait", Kind: KindFloat,
				Summary: "seconds to wait for the console to answer"},
		}},
	"adb": {Name: "adb", Kind: KindScript,
		Summary: "run a command over adb",
		Fields:  map[string]Key{}},
	"adb_connect": {Name: "adb_connect", Kind: KindBool,
		Summary: "connect adb before the commands that need it",
		Fields:  map[string]Key{}},
	"adb_privileged": {Name: "adb_privileged", Kind: KindBool,
		Summary: "ask adbd for root before the commands that need it",
		Fields:  map[string]Key{}},
	"do": {Name: "do", Kind: KindList,
		Summary: "run a list of steps in order under one name",
		Fields:  map[string]Key{}},
	"save": {Name: "save", Kind: KindString,
		Summary: "publish the disk to this path: the second half of `stop:`",
		Fields:  map[string]Key{}},
}

// Roles classifies every key, and it is DERIVED rather than written: a key is an action if a verb
// declares it, an argument if a verb declares it as a field, and a modifier if it is shared.
//
// IT IS BUILT AT INIT RATHER THAN LISTED, because the list was a fourth place for the same fact and
// it drifted - `expect:` stayed in `Roles` after the vocabulary renamed it, and the only thing that
// noticed was a test.
var Roles = buildRoles()

func buildRoles() map[string]Role {
	m := make(map[string]Role, 128)
	for name, v := range Verbs {
		m[name] = RoleAction
		for f := range v.Fields {
			// A FIELD THAT IS ALSO A VERB IS AN ACTION. `click` is a verb's field AND a dispatch
			// branch of its own, and the action role is the one that decides whether two keys may
			// share a step.
			if _, isVerb := Verbs[f]; isVerb {
				continue
			}
			m[f] = RoleArgument
		}
	}
	// AN ACTION IS AN ACTION, AND ITS FIELDS ARE ARGUMENTS - the same rule as a verb's.
	for name, a := range Actions {
		m[name] = RoleAction
		for f := range a.Fields {
			if _, isVerb := Verbs[f]; isVerb {
				continue
			}
			if _, isAction := Actions[f]; isAction {
				continue
			}
			if _, taken := m[f]; !taken {
				m[f] = RoleArgument
			}
		}
	}
	for k := range Shared {
		if _, taken := m[k]; !taken {
			m[k] = RoleModifier
		}
	}
	// A NAME THAT IS BOTH IS DECIDED BY THE ACTION, which is the stricter of the two: `click` is a
	// field of `input` and a dispatch branch, and a step that writes it has an action.
	for name := range Verbs {
		m[name] = RoleAction
	}
	return m
}

// DispatchOrder is the order `runOne` tests the keys, and it matters because the FIRST matching
// branch wins: a step that carries two of them runs the earlier one and drops the later.
//
// THE GATE KEYS COME FIRST, AND THAT IS THE POINT OF THE ORDER.
//
// `runOne` used to reach `input` before `when`, so a step written as an action with a gate ran the
// action and NEVER READ THE GATE. MEASURED on fedora-workstation install, run 45397314773: the step
// that closes GNOME Settings pressed `alt+f4` while the frame in front was the INSTALLER - the screen
// the gate existed to protect - and closed anaconda. The gate is now tested before any action branch,
// so the order starts with it.
var dispatchOrder = []string{
	"when", "when_not", "when_not_any",
	"boot",
	"input",
	"run", "ssh", "adb", "adb_connect", "adb_privileged",
	"copy",
	"read", "screenshot",
	"check",
	"control", "acpi", "qga", "wait_until",
	"if",
	"stop",
	"repeat",
	"do", "until", "until_any",
	"needs", "require",
	"disk",
	"write",
	"at_color", "at_bottom", "at_button",
	"wait", "settle",
}

// dispatchRank is the position of a key in that order, built once.
var dispatchRank = func() map[string]int {
	m := make(map[string]int, len(dispatchOrder))
	for i, k := range dispatchOrder {
		m[k] = i
	}
	return m
}()

// DispatchOrder returns a key's position in the executor's switch, or a large number for a key that
// is not a dispatch case (an argument, a sub-key).
func DispatchOrder(key string) int {
	if r, ok := dispatchRank[key]; ok {
		return r
	}
	return 1 << 20
}

// MachineFacts are the live booleans the machine reports, and a file reads them by name.
//
// THEY ARE DECLARED HERE because `Validate` has to know the prefix is reserved, and a second copy in
// `internal/run` would be the drift this vocabulary keeps removing.
var MachineFacts = []string{
	"machine_running", "machine_stopped", "machine_ssh", "machine_qga",
}

// MachineFactNames lists them for a failure message.
func MachineFactNames() string { return strings.Join(MachineFacts, ", ") }
