package step

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
		Summary: "start the machine this file owns",
		Fields: map[string]Key{
			"os": {Name: "os", Kind: KindString,
				Summary: "the guest family - linux, windows, macos, bsd, android, microvm"},
			"iso": {Name: "iso", Kind: KindString,
				Summary: "the installer medium to boot from"},
			"answer_cd": {Name: "answer_cd", Kind: KindString,
				Summary: "Windows Setup's answer file, on its own CD"},
			"driver_cd": {Name: "driver_cd", Kind: KindString,
				Summary: "the virtio driver CD Windows Setup reads"},
			"recovery": {Name: "recovery", Kind: KindString,
				Summary: "macOS's recovery image"},
			"memory_mb": {Name: "memory_mb", Kind: KindInt,
				Summary: "the guest's memory, in MiB"},
			"cpus": {Name: "cpus", Kind: KindInt,
				Summary: "how many virtual CPUs"},
			"microvm": {Name: "microvm", Kind: KindBool,
				Summary: "the smallest machine that can boot a modern Linux kernel: no firmware, " +
					"no emulated devices the guest does not need"},
			"kernel": {Name: "kernel", Kind: KindString,
				Summary: "with `microvm:`: the kernel the host hands qemu"},
			"initrd": {Name: "initrd", Kind: KindString,
				Summary: "with `microvm:`: the initramfs"},
			"kernel_append": {Name: "kernel_append", Kind: KindString,
				Summary: "with `microvm:`: the guest's own kernel command line"},
			"microvm_display": {Name: "microvm_display", Kind: KindString,
				Summary: "with `microvm:`: desktop, terminal or serial"},
			"disk": {Name: "disk", Kind: KindStringOrMapping,
				Summary: "the disk to start from, or the disk to make: `{file, size, gpt, esp_mib, " +
					"min_mb, wait}`. A bare file name is a disk that already exists"},
			"disk_gb": {Name: "disk_gb", Kind: KindTemplated,
				Summary: "how big to make the disk, in GiB"},
			"disk_gpt": {Name: "disk_gpt", Kind: KindBool,
				Summary: "write a GPT partition table"},
			"disk_esp_mib": {Name: "disk_esp_mib", Kind: KindTemplated,
				Summary: "the size of the EFI system partition, in MiB"},
			"disk_min_mb": {Name: "disk_min_mb", Kind: KindInt,
				Summary: "the smallest a finished disk may be, in MiB"},
			"disk_settle": {Name: "disk_settle", Kind: KindFloat,
				Summary: "wait this long for the disk file to stop growing"},
		}},

	// ---- 2. `press` ------------------------------------------------------------------------
	"press": {Name: "press",
		Summary: "send input to the guest - a click, a keystroke, or text",
		Fields: map[string]Key{
			"click": {Name: "click", Kind: KindPairOrMapping,
				Summary: "the point to click, as [x, y] or `{button: [x, y], fallback: [x, y]}`"},
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
			"type_gap": {Name: "type_gap", Kind: KindFloat,
				Summary: "seconds between characters"},
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
				Summary: "with `require_ssh:`: the command the wait runs"},
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
				Summary: "the 1.0 spelling of `save:`"},
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
				Summary: "the condition that ends it, written the way a `check:` is"},
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

// Aliases are the step-level keys the engine still dispatches on because a verb compiles to them.
//
// THEY ARE NOT PART OF THE VOCABULARY A PERSON WRITES. `press text "x"` compiles to `{press, text}`,
// and `run` over ssh compiles to `{ssh: cmd}` - the executor branches on those shapes because they
// are what it knows how to run. A file may write them and they are checked, but the design does not
// ask a person to.
var Aliases = map[string]Key{
	"action": {Name: "action", Kind: KindMapping,
		Summary: "a step whose action is a list of steps run in order under one name"},
	"do": {Name: "do", Kind: KindList,
		Summary: "the pane model's action list, run in order"},
	"then": {Name: "then", Kind: KindList,
		Summary: "a branch"}, // also an `if` field; kept here for the flat form
	"else": {Name: "else", Kind: KindList,
		Summary: "a branch"},
	"until": {Name: "until", Kind: KindWords,
		Summary: "a single-shot wait on a console line"},
	"from": {Name: "from", Kind: KindString,
		Summary: "with `copy:`: the local file; with `until:`: the channel (`console`)"},
	"until_any": {Name: "until_any", Kind: KindWordSets,
		Summary: "the 1.0 negative form of `until:`"},
	"wait": {Name: "wait", Kind: KindFloat,
		Summary: "the 1.0 name for `settle:`"},
	"needs": {Name: "needs", Kind: KindList,
		Summary: "the files a stage reads before it starts; a header declaration"},
	"require_ssh": {Name: "require_ssh", Kind: KindBool,
		Summary: "wait for sshd before the steps that use it"},
	"desktop": {Name: "desktop", Kind: KindString,
		Summary: "wait until a desktop of this family is on screen"},
	"poll": {Name: "poll", Kind: KindFloat,
		Summary: "seconds between looks while waiting for a desktop"},
	"blind_after": {Name: "blind_after", Kind: KindInt,
		Summary: "seconds of a blank screen before a wait starts looking"},
	"fetch": {Name: "fetch", Kind: KindString,
		Summary: "obtain a prepared asset by name"},
	"release": {Name: "release", Kind: KindString,
		Summary: "with `fetch:`: which release of it"},
	"recovery": {Name: "recovery", Kind: KindString,
		Summary: "with `fetch:`/`boot:`: macOS's recovery image"},
	"os": {Name: "os", Kind: KindString,
		Summary: "with `fetch:`: the guest family the asset is for"},
	"write": {Name: "write", Kind: KindString,
		Summary: "write one of the built-in templates to disk"},
	"disk": {Name: "disk", Kind: KindStringOrMapping,
		Summary: "the disk a machine is made on, or the file it starts from"},
	"disk_gb": {Name: "disk_gb", Kind: KindTemplated,
		Summary: "how big to make it, in GiB"},
	"disk_gpt": {Name: "disk_gpt", Kind: KindBool,
		Summary: "write a GPT partition table"},
	"disk_esp_mib": {Name: "disk_esp_mib", Kind: KindTemplated,
		Summary: "the EFI system partition's size"},
	"disk_min_mb": {Name: "disk_min_mb", Kind: KindInt,
		Summary: "the smallest a finished disk may be"},
	"disk_settle": {Name: "disk_settle", Kind: KindFloat,
		Summary: "wait for the disk file to stop growing"},
	"esp_fallback": {Name: "esp_fallback", Kind: KindAny,
		Summary: "edit a FAT filesystem inside a stopped disk"},
	"screenshot": {Name: "screenshot", Kind: KindString,
		Summary: "save the screen to this path, for a post-mortem nobody had to ask for"},
	"ssh_after_reboot": {Name: "ssh_after_reboot", Kind: KindBool,
		Summary: "with `require_ssh:`: wait out the old daemon before the wait"},
	"ssh_probe": {Name: "ssh_probe", Kind: KindString,
		Summary: "with `require_ssh:`: the command the wait runs"},
	"terminal_wait": {Name: "terminal_wait", Kind: KindFloat,
		Summary: "seconds to wait for the console to answer a command"},
	"type_gap": {Name: "type_gap", Kind: KindFloat,
		Summary: "seconds between characters"},
	"ssh": {Name: "ssh", Kind: KindScript,
		Summary: "run a command over ssh; the exit status is the verdict"},
	"adb": {Name: "adb", Kind: KindScript,
		Summary: "run a command over adb"},
	"adb_connect": {Name: "adb_connect", Kind: KindBool,
		Summary: "connect adb before the commands that need it"},
	"adb_privileged": {Name: "adb_privileged", Kind: KindBool,
		Summary: "ask adbd for root before the commands that need it"},
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
	for k := range Shared {
		m[k] = RoleModifier
	}
	// AN ALIAS IS AN ACTION ONLY WHEN NO VERB ALREADY CLAIMS IT AS A FIELD. `then`/`else` are `if`'s
	// fields, and calling them actions would say a step could be "a then" - which is not a thing.
	for k := range Aliases {
		if _, isField := m[k]; !isField {
			m[k] = RoleAction
		}
	}
	// A NAME THAT IS BOTH IS DECIDED BY THE ACTION, which is the stricter of the two: `click` is a
	// field of `press` and a dispatch branch, and a step that writes it has an action.
	for name := range Verbs {
		m[name] = RoleAction
	}
	return m
}

// DispatchOrder is the order `runOne` tests the keys, and it matters because the FIRST matching
// branch wins: a step that carries two of them runs the earlier one and drops the later.
var dispatchOrder = []string{
	"boot",
	"press",
	"run", "ssh", "adb", "adb_connect", "adb_privileged",
	"copy",
	"read", "screenshot",
	"check",
	"control", "acpi", "qga", "wait_until",
	"if",
	"stop",
	"repeat",
	"when", "when_not", "when_not_any", "do", "until", "until_any",
	"needs", "require", "require_ssh",
	"disk", "disk_settle",
	"fetch", "write", "esp_fallback",
	"desktop", "at_color", "at_bottom", "at_button",
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
