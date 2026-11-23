# easyvm walkthrough — every primitive, in one table

A step is one of nine verbs, or one of the actions the engine dispatches on, plus its fields.
`internal/step/vocab.go` is the source and this document is checked against it.

## 1. The nine verbs

| key | what it does |
|---|---|
| `boot` | start the machine this file owns |
| `check` | ask ONE question about a picture and bind the answer. `source:` names the frame a `read:` captured; `mode:` says how it is compared |
| `control` | a machine-level message rather than a guest action |
| `copy` | copy one file into the guest |
| `if` | run `then:` when a boolean is true and `else:` when it is false |
| `input` | send input to the guest - a click, a keystroke, or text |
| `read` | capture one frame and bind it to a name. It measures nothing and decides nothing; the region and the question belong to the `check:` that follows |
| `repeat` | run `do:` and then ask `until:`, up to `max:` times |
| `run` | run one command in the guest, over the console, ssh or adb |
| `stop` | stop the machine, and publish the disk in the same step |

## 2. Shared — what every step may say

| key | what it does |
|---|---|
| `mode` | HOW the frame is compared: like, mean_between, sat_between, spread_between, text, contains, matches, equals |
| `name` | what the log calls this step; required |
| `none` | the negative form of `words:`: none of these word-sets may appear |
| `optional` | a step that may fail without failing the stage - only for a check whose NEGATIVE result is information, never for a verdict |
| `pattern` | with `mode: matches`: the regular expression |
| `range` | with a numeric mode: [low, high] |
| `ratio` | with `mode: like`: [low, high] share of the picture that must match |
| `rect` | the region to measure, as a FRACTION (`middle`, `bottom 4.5%`) or `[x0, y0, x1, y1]` pixels |
| `rect_at` | with a PIXEL `rect:`: [width, height] it was measured at, so it can be scaled |
| `reference` | with `mode: like`: the image to compare against |
| `scale` | enlargement for a cropped read; small text needs it |
| `settle` | seconds to wait where there is NO fact to wait on; the older `wait:` and `wait_after:` both become this |
| `settle_before` | seconds to wait before the step acts |
| `source` | WHERE the question is asked - the name of a frame a `read:` captured, or `console` for the serial line |
| `timeout` | how long this step may take, in seconds |
| `type_gap` | seconds between characters when text or a command is typed |
| `when` | run this step only when the screen says these words (`{none: ...}` for the negative form) |
| `when_not` | run this step only when the screen does NOT say these words |
| `when_not_any` | run this step only when NONE of these word-sets is on screen |
| `without` | words that must NOT be on screen |
| `words` | with `mode: text`: the word-sets, any one of which satisfies it |

## 3. Each verb's fields

### `boot`

| key | what it does |
|---|---|
| `answer_cd` | Windows Setup's answer file, on its own CD |
| `cpus` | how many virtual CPUs |
| `disk` | the disk to start from, or the disk to make: `{file, size, gpt, esp_mib, min_mb, wait}`. A bare file name is a disk that already exists |
| `disk_esp_mib` | the size of the EFI system partition, in MiB |
| `disk_gb` | how big to make the disk, in GiB |
| `disk_gpt` | write a GPT partition table |
| `disk_min_mb` | the smallest a finished disk may be, in MiB |
| `disk_settle` | wait this long for the disk file to stop growing |
| `driver_cd` | the virtio driver CD Windows Setup reads |
| `initrd` | with `microvm:`: the initramfs |
| `iso` | the installer medium to boot from |
| `kernel` | with `microvm:`: the kernel the host hands qemu |
| `kernel_append` | with `microvm:`: the guest's own kernel command line |
| `memory_mb` | the guest's memory, in MiB |
| `microvm` | the smallest machine that can boot a modern Linux kernel: no firmware, no emulated devices the guest does not need |
| `microvm_display` | with `microvm:`: desktop, terminal or serial |
| `os` | the guest family - linux, windows, macos, bsd, android, microvm |
| `recovery` | macOS's recovery image |

### `check`

| key | what it does |
|---|---|
| `as` | the name the boolean this check binds is stored under, for an `if:` to read |
| `contains` | with `mode: contains`: the substring to find |
| `must` | a FALSE answer fails the step. Without it the check only binds a boolean |

### `control`

| key | what it does |
|---|---|
| `acpi` | one ACPI message: power, reset, sleep |
| `body` | with `qga: file-write`: the bytes to write |
| `minimum` | with `wait_until:`: the number of seconds that counts as arrived |
| `path` | with `qga: file-read`/`file-write`: the file in the GUEST |
| `qga` | one guest-agent command: ping, info, shutdown, file-read, file-write |
| `stopped` | wait until the machine's process has gone |
| `wait_until` | wait for a condition about the machine: `stopped` |

### `copy`

| key | what it does |
|---|---|
| `from` | the local file, relative to the walkthrough |
| `template` | render one of the built-in templates instead of copying a file: launcher, macos_agent_plist, windows_task |
| `to` | the path in the guest |

### `if`

| key | what it does |
|---|---|
| `else` | the steps that run when it is false |
| `then` | the steps that run when the boolean is true |

### `input`

| key | what it does |
|---|---|
| `anchor` | measure the point from `bottom` instead of the top-left |
| `at` | with `text:`: the point to click before typing |
| `at_bottom` | click at this point, measured from the bottom edge |
| `at_button` | click the control at this point |
| `at_color` | click the blob of this colour |
| `button` | click the control found by this label |
| `clear` | select-all and delete before typing |
| `click` | the point to click, as [x, y] or `{button: [x, y], fallback: [x, y]}` |
| `color_region` | where to look for that colour |
| `color_tolerance` | how far from the colour still counts |
| `colour` | click the largest blob of this [r, g, b] |
| `double` | double-click at this point |
| `fallback` | the point to use when no control carries the label |
| `gap` | seconds between keystrokes while typing |
| `key` | a qemu qcode: ret, esc, tab, meta_l+q |
| `modifier` | the modifier `clear:` uses; ctrl by default |
| `text` | the string to type |

### `repeat`

| key | what it does |
|---|---|
| `do` | the steps to run before each attempt |
| `max` | the attempt ceiling. Required - a repeat with no ceiling never ends |
| `nudge` | actions that fire AT MOST ONCE, once the threshold has passed |
| `nudge_after` | with `nudge:`: the number of seconds before it fires |
| `pause` | seconds between attempts |
| `until` | the condition that ends it: a `check:`-shaped block, or the name of one of the machine's own facts such as `machine_stopped` |

### `run`

| key | what it does |
|---|---|
| `ssh_after_reboot` | wait out the old daemon first, or the wait passes on the process that is about to die |
| `ssh_probe` | with `run:`: the command the wait runs |
| `terminal_wait` | seconds to wait for the console to answer |
| `via` | where it runs: console (default), ssh, adb |

### `stop`

| key | what it does |
|---|---|
| `compress` | the 1.0 spelling of publishing the disk |
| `method` | how to stop it: kill, ssh, acpi, guest_agent |
| `min_mb` | the smallest the published disk may be, in MiB - a check that the guest really wrote what it claimed |
| `save` | publish the disk to this path after the machine is gone |
| `shutdown` | the 1.0 spelling of `method:` |

## 4. Actions the engine dispatches on

| key | what it does |
|---|---|
| `acpi` | send one ACPI message: power, reset, sleep |
| `adb` | run a command over adb |
| `adb_connect` | connect adb before the commands that need it |
| `adb_privileged` | ask adbd for root before the commands that need it |
| `disk` | make one disk, or name the one a machine starts from |
| `disk_settle` | wait until the disk file stops growing, and check it is big enough |
| `do` | run a list of steps in order under one name |
| `esp_fallback` | edit a FAT filesystem inside a stopped disk, at byte offsets |
| `fetch` | obtain a prepared asset by name |
| `needs` | the files this stage reads must be on disk before it starts |
| `qga` | one guest-agent command: ping, info, shutdown, file-read, file-write |
| `save` | publish the disk to this path: the second half of `stop:` |
| `screenshot` | save the screen to this path, for a post-mortem nobody had to ask for |
| `ssh` | run a command over ssh; the exit status is the verdict |
| `until` | wait for a console line, once |
| `until_any` | wait for any one of several console lines |
| `wait_until` | wait for a condition about the machine: `stopped` |
| `write` | write one of the built-in templates to disk |

### `disk`

| key | what it does |
|---|---|
| `disk_esp_mib` | the EFI system partition's size, in MiB |
| `disk_gb` | how big to make it, in GiB |
| `disk_gpt` | write a GPT partition table |

### `disk_settle`

| key | what it does |
|---|---|
| `disk_min_mb` | the smallest a finished disk may be, in MiB |
| `poll` | seconds between looks |

### `esp_fallback`

| key | what it does |
|---|---|
| `disk` | the disk file to edit |
| `from` | the directory inside it |

### `fetch`

| key | what it does |
|---|---|
| `os` | the guest family the asset is for |
| `recovery` | macOS's recovery image |
| `release` | which release of it |
| `template` | the built-in template to render |

### `qga`

| key | what it does |
|---|---|
| `path` | the file in the GUEST |

### `ssh`

| key | what it does |
|---|---|
| `terminal_wait` | seconds to wait for the console to answer |

### `until`

| key | what it does |
|---|---|
| `blind_after` | seconds of silence before the wait starts looking |
| `from` | the channel: `console` |
| `poll` | seconds between looks |

### `until_any`

| key | what it does |
|---|---|
| `from` | the channel: `console` |

### `write`

| key | what it does |
|---|---|
| `os` | the guest family the template is for |
| `release` | the release the template names |
| `template` | which template |

## 5. The machine's own facts

A `check:` binds a boolean. These are LIVE: asked of the daemon when read, and usable anywhere
a boolean is - `if`, `repeat until`, `wait_until`.

| name | |
|---|---|
| `machine_running` | |
| `machine_stopped` | |
| `machine_ssh` | |
| `machine_qga` | |

## 6. File header

| key | |
|---|---|
| `name` | what the log calls this walkthrough |
| `resolution` | [width, height] the coordinates were measured at |
| `recording_resolution` | [width, height] the VIDEO's size; required |
| `user` | the account the walk logs in as |
| `password` | the guest account's password |
| `var` | one variable |
| `needs` | files that must exist before the stage runs |
