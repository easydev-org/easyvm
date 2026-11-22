# easyvm walkthrough — every primitive, in one table

A step is one of nine verbs and its fields. `internal/step/vocab.go` is the source; this document is
checked against it row by row.

## 1. The nine verbs

| key | what it does |
|---|---|
| `boot` | start the machine this file owns |
| `press` | send input to the guest - a click, a keystroke, or text |
| `run` | run one command in the guest, over the console, ssh or adb |
| `copy` | copy one file into the guest |
| `read` | capture one frame and bind it to a name. It measures nothing and decides nothing; the region and the question belong to the `check:` that follows |
| `check` | ask ONE question about a picture and bind the answer. `source:` names the frame a `read:` captured; `mode:` says how it is compared |
| `control` | a machine-level message rather than a guest action |
| `if` | run `then:` when a boolean is true and `else:` when it is false |
| `stop` | stop the machine, and publish the disk in the same step |
| `repeat` | run `do:` and then ask `until:`, up to `max:` times |

## 2. Shared — what every verb reads

| key | what it does |
|---|---|
| `name` | what the log calls this step; required |
| `timeout` | how long this step may take, in seconds |
| `optional` | a step that may fail without failing the stage - only for a check whose NEGATIVE result is information, never for a verdict |
| `settle` | seconds to wait where there is NO fact to wait on; the older `wait:` and `wait_after:` both become this |
| `when` | run this step only when the screen says these words (`{none: ...}` for the negative form) |
| `when_not` | run this step only when the screen does NOT say these words |
| `when_not_any` | run this step only when NONE of these word-sets is on screen |
| `settle_before` | seconds to wait before the step acts |
| `source` | WHERE the question is asked - the name of a frame a `read:` captured, or `console` for the serial line |
| `mode` | HOW the frame is compared: like, mean_between, sat_between, spread_between, text, contains, matches, equals |
| `words` | with `mode: text`: the word-sets, any one of which satisfies it |
| `none` | the negative form of `words:`: none of these word-sets may appear |
| `without` | words that must NOT be on screen |
| `rect` | the region to measure, as a FRACTION (`middle`, `bottom 4.5%`) or `[x0, y0, x1, y1]` pixels |
| `rect_at` | with a PIXEL `rect:`: [width, height] it was measured at, so it can be scaled |
| `scale` | enlargement for a cropped read; small text needs it |
| `range` | with a numeric mode: [low, high] |
| `pattern` | with `mode: matches`: the regular expression |
| `ratio` | with `mode: like`: [low, high] share of the picture that must match |
| `reference` | with `mode: like`: the image to compare against |

## 3. Each verb's fields


### `boot`

| key | what it does |
|---|---|
| `os` | the guest family - linux, windows, macos, bsd, android, microvm |
| `iso` | the installer medium to boot from |
| `answer_cd` | Windows Setup's answer file, on its own CD |
| `driver_cd` | the virtio driver CD Windows Setup reads |
| `recovery` | macOS's recovery image |
| `memory_mb` | the guest's memory, in MiB |
| `cpus` | how many virtual CPUs |
| `microvm` | the smallest machine that can boot a modern Linux kernel: no firmware, no emulated devices the guest does not need |
| `kernel` | with `microvm:`: the kernel the host hands qemu |
| `initrd` | with `microvm:`: the initramfs |
| `kernel_append` | with `microvm:`: the guest's own kernel command line |
| `microvm_display` | with `microvm:`: desktop, terminal or serial |
| `disk` | the disk to start from, or the disk to make: `{file, size, gpt, esp_mib, min_mb, wait}`. A bare file name is a disk that already exists |
| `disk_gb` | how big to make the disk, in GiB |
| `disk_gpt` | write a GPT partition table |
| `disk_esp_mib` | the size of the EFI system partition, in MiB |
| `disk_min_mb` | the smallest a finished disk may be, in MiB |
| `disk_settle` | wait this long for the disk file to stop growing |
| `click` | the point to click, as [x, y] or `{button: [x, y], fallback: [x, y]}` |
| `text` | the string to type |
| `key` | a qemu qcode: ret, esc, tab, meta_l+q |
| `colour` | click the largest blob of this [r, g, b] |
| `button` | click the control found by this label |
| `at` | with `text:`: the point to click before typing |
| `anchor` | measure the point from `bottom` instead of the top-left |
| `fallback` | the point to use when no control carries the label |
| `gap` | seconds between keystrokes while typing |
| `clear` | select-all and delete before typing |
| `modifier` | the modifier `clear:` uses; ctrl by default |
| `type_gap` | seconds between characters |
| `double` | double-click at this point |
| `at_color` | click the blob of this colour |
| `color_region` | where to look for that colour |
| `color_tolerance` | how far from the colour still counts |
| `at_button` | click the control at this point |
| `at_bottom` | click at this point, measured from the bottom edge |

### `run`

| key | what it does |
|---|---|
| `via` | where it runs: console (default), ssh, adb |
| `terminal_wait` | seconds to wait for the console to answer |
| `ssh_probe` | with `require_ssh:`: the command the wait runs |
| `ssh_after_reboot` | wait out the old daemon first, or the wait passes on the process that is about to die |
| `from` | the local file, relative to the walkthrough |
| `to` | the path in the guest |
| `template` | render one of the built-in templates instead of copying a file: launcher, macos_agent_plist, windows_task |
| `contains` | with `mode: contains`: the substring to find |
| `must` | a FALSE answer fails the step. Without it the check only binds a boolean |
| `as` | the name the boolean this check binds is stored under, for an `if:` to read |

### `control`

| key | what it does |
|---|---|
| `acpi` | one ACPI message: power, reset, sleep |
| `qga` | one guest-agent command: ping, info, shutdown, file-read, file-write |
| `wait_until` | wait for a condition about the machine: `stopped` |
| `stopped` | wait until the machine's process has gone |
| `path` | with `qga: file-read`/`file-write`: the file in the GUEST |
| `body` | with `qga: file-write`: the bytes to write |
| `minimum` | with `wait_until:`: the number of seconds that counts as arrived |
| `then` | the steps that run when the boolean is true |
| `else` | the steps that run when it is false |
| `method` | how to stop it: kill, ssh, acpi, guest_agent |
| `save` | publish the disk to this path after the machine is gone |
| `compress` | the 1.0 spelling of `save:` |
| `shutdown` | the 1.0 spelling of `method:` |
| `min_mb` | the smallest the published disk may be, in MiB - a check that the guest really wrote what it claimed |

### `repeat`

| key | what it does |
|---|---|
| `do` | the steps to run before each attempt |
| `until` | the condition that ends it, written the way a `check:` is |
| `max` | the attempt ceiling. Required - a repeat with no ceiling never ends |
| `pause` | seconds between attempts |
| `nudge` | actions that fire AT MOST ONCE, once the threshold has passed |
| `nudge_after` | with `nudge:`: the number of seconds before it fires |

## 4. Aliases — shapes the engine dispatches on

| key | what it does |
|---|---|
| `action` | a step whose action is a list of steps run in order under one name |
| `do` | the pane model's action list, run in order |
| `then` | a branch |
| `else` | a branch |
| `until` | the 1.0 single-shot wait on a console line |
| `until_any` | the 1.0 negative form of `until:` |
| `wait` | the 1.0 name for `settle:` |
| `needs` | the files a stage reads before it starts; a header declaration |
| `require_ssh` | wait for sshd before the steps that use it |
| `desktop` | wait until a desktop of this family is on screen |
| `poll` | seconds between looks while waiting for a desktop |
| `blind_after` | seconds of a blank screen before a wait starts looking |
| `fetch` | obtain a prepared asset by name |
| `release` | with `fetch:`: which release of it |
| `write` | write one of the built-in templates to disk |
| `esp_fallback` | edit a FAT filesystem inside a stopped disk |
| `screenshot` | save the screen to this path, for a post-mortem nobody had to ask for |
| `ssh` | run a command over ssh; the exit status is the verdict |
| `adb` | run a command over adb |
| `adb_connect` | connect adb before the commands that need it |
| `adb_privileged` | ask adbd for root before the commands that need it |

## 5. File header

| key | |
|---|---|
| `name` | what the log calls this walkthrough |
| `resolution` | [width, height] the coordinates were measured at |
| `recording_resolution` | [width, height] the VIDEO's size; required |
| `user` | the account the walk logs in as |
| `password` | the guest account's password |
| `var` | one variable |
| `needs` | files that must exist before the stage runs |
