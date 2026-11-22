# easyvm walkthrough — every primitive, in one table

THE VOCABULARY IS 2.0'S. A step is one of nine verbs with its argument, plus modifiers.
The 1.0 names are refused by name; see the last section.

Checked by `internal/step/audit_test.go`.

## 1. The nine verbs

| key | what it does |
|---|---|
| `boot` | start the machine this file owns. Its arguments are SIBLING keys - os, iso, kernel, initrd, memory_mb, cpus, cpu, recovery, answer_cd, driver_cd - and the disk is the one an earlier `disk:` step named |
| `copy` | copy one file into the guest: the local path, and the remote path as a child |
| `stop` | stop the machine: kill, ssh, acpi or guest_agent |
| `press` | click at a point. `[x, y]` is a coordinate; `{button: [x, y]}` is a point inside a window; `{button: [x, y], fallback: [x, y]}` adds a second place to try; `{colour: [r, g, b]}` is the largest blob of one colour. Fixed positions, never OCR - a button's position is a measured fact about a screen, and a search for it is a guess that moves |
| `run` | a shell line typed into the guest's own terminal, one line at a time |
| `read` | capture one frame and bind it under `name:`. Every `expect:` that follows asks its own question about THAT frame, with its own `rect:` |
| `expect` | compare a named reading and bind a BOOLEAN: {name, from, mode, ...}. `mode:` says HOW to compare. `must: true` makes a FALSE answer fail the step, which is what a file means when it says the screen IS the reference |
| `if` | run `then:` when this named boolean is true and `else:` when it is false. The boolean comes from a `expect:` step; a branch is a plain step list |
| `repeat` | the parts written down: `max:`, `do:` (the action), `ocr:` (the reading -> text), `expect:` (the comparison -> boolean), `until: {fact: ...}` (tests the boolean). Optionally `every: <seconds>` with `clock: [...]` - an action that runs ONCE when that long has passed, whatever the screen says |

## 2. Modifiers

| key | what it does |
|---|---|
| `name` | what the log calls this step; required |
| `optional` | a step that may fail without failing the stage - only for a check whose NEGATIVE result is information, never for a verdict |
| `when_not` | the negative gate: run this step only if the screen does NOT show these words |
| `timeout` | seconds THIS step may wait. THE DEFAULT DEPENDS ON THE VERB, because the waits are not one thing: ssh and require_ssh 900, until from: console 180, disk_settle 3600, adb_connect 300, adb 60, a desktop 900, a shutdown grace 0. AND ON A `boot:` IT IS THE BUDGET THE FOLLOWING WAIT INHERITS, because a boot returns before the guest is up - previously the number was written 53 times and read by nothing |
| `when` | run this step only if the screen shows these words. `when: {none: [[a], [b, c]]}` is the NEGATIVE form: run only if NONE of those word-sets is present (it replaces `when_not_any:`) |

## 3. Arguments — the shapes a verb takes

| key | what it does |
|---|---|
| `needs` | files that must be on disk before this stage runs; the first step of every stage names its medium here so a missing input fails in the first second rather than forty minutes in |
| `when_not_any` | the alternatives form of `when_not:`: the step runs only if the screen shows NONE of these word-sets |
| `os` | which family the machine is: windows, macos, linux, android |
| `iso` | an installer ISO. Windows and Linux boot it directly; macOS has none and is refused by name if given one |
| `recovery` | macOS's recovery image, attached as removable media. It is the install medium AND the source of the APFS driver OpenCore needs, so it is attached on every macOS boot |
| `answer_cd` | a second CD-ROM carrying Windows' answer file. Setup finds it nowhere on the install ISO |
| `driver_cd` | a third CD-ROM carrying the virtio drivers, for the installed system to pick up network and ballooning |
| `memory_mb` | the guest's RAM in MiB; 4096 when not said. A guest that installs from a memdisk needs more than the default |
| `cpus` | how many vCPUs the guest gets; 4 when not said |
| `kernel` | with `microvm:`: the kernel qemu hands the guest, because a microvm has no firmware to read a boot sector |
| `initrd` | with `microvm:`: the initramfs beside the kernel |
| `kernel_append` | with `microvm:`: the guest's OWN kernel command line. Required for any microvm whose root is a real filesystem - the default is Alpine's diskless `modloop=/dev/vda`, which mounts OVER the root disk and drops the guest to an emergency shell |
| `microvm` | with `boot:`: the smallest machine that can boot a modern Linux kernel - no firmware, no devices the guest does not need, a direct kernel hand-off. For a walkthrough whose job is to prove the vocabulary drives a guest, not to install a distribution through its GUI |
| `shutdown` | a NAMED method: guest_agent, ssh, acpi, or kill. EVERY ONE OF THEM STOPS THE MACHINE AND WAITS FOR IT TO GO - a method that only waited was here for one commit and did not belong: `shutdown:` names how to stop it, so a verb that stopped nothing was a word that lied. Waiting for qemu to release the disk is a precondition of `compress:` and lives there |
| `acpi` | send one ACPI message: `power` (the power button) or `reset`. It SENDS ONLY - whether the guest goes is `wait_until: stopped`, a step of its own |
| `qga` | run one qemu-guest-agent command: ping, info, shutdown, exec, file-read, file-write, set-time, interfaces. The only channel into a guest with no sshd and no ACPI handler |
| `compress` | flatten the overlay into this artifact; the path is relative to the repository root, not to the work directory |
| `disk` | the disk file, or a mapping that creates it with its own size and layout. Alone it names the disk `boot:` starts; as a mapping it creates that disk, and the fields are listed in Subkeys |
| `disk_gb` | size of a disk this step creates |
| `disk_gpt` | partition the created disk: a 260 MiB ESP typed EF00 at LBA 2048, Linux on the rest |
| `disk_esp_mib` | size of that ESP; 260 is what the installer draws as 0.25GB |
| `disk_settle` | wait until the target qcow2 stops growing for this many quiet seconds |
| `disk_min_mb` | with disk_settle: only start waiting once it holds this many allocated MiB |
| `click` | with `press:`: where the pointer goes, as [x, y] |
| `text` | with `press:`: the string to type |
| `colour` | with `press:`: click the largest blob of this [r, g, b] |
| `button` | with `press:`: click the control found by this label |
| `at` | with `press text:`: the point to click before typing |
| `anchor` | with `press:`: measure the point from `bottom` instead of the top-left |
| `fallback` | with `press button:`: the point to use when no control carries that label |
| `gap` | with `press text:`: seconds between keystrokes |
| `method` | with `stop:`: how to stop the machine - kill, ssh, acpi or guest_agent |
| `save` | with `stop:`: publish the disk to this path after the machine is gone |
| `via` | with `run:`: where it runs - console, ssh or adb |
| `double` | double-click [x, y]; separate from `press` because the gap between the two clicks is what makes it a double-click |
| `at_button` | [x0, y0, x1, y1] - click the centre of the coloured fill inside this region. The ONE lookup this repository allows, for a control whose position moves per pane and whose fill is the only one of its kind in that strip |
| `at_color` | [r, g, b] - click the centre of the largest blob of this colour. For a control the window draws in a fixed colour, which is how a macOS window button is found without knowing where the window is |
| `color_tolerance` | with `at_color`: how far a pixel may be from the target colour and still count, for antialiasing. 45 matches macOS's window buttons |
| `color_region` | with `at_color`: [x0, y0, x1, y1] to search inside, when the colour could appear somewhere else on the screen too |
| `at_bottom` | [x, dy] - click x at dy pixels above the window's bottom edge |
| `key` | a qemu qcode, or a + combo such as ctrl+alt+t |
| `type` | text to type; combined with `at` it clicks the field first and clears it |
| `clear` | clear the field before typing (select-all, then backspace) |
| `modifier` | the select-all modifier for this family: ctrl on Linux, meta_l on macOS |
| `type_gap` | seconds between keystrokes; 0.02 suits a POSIX shell, 0.15 a Windows one |
| `terminal_wait` | seconds to wait for the terminal after `run` opens it |
| `blind_after` | with until_console: go on after this many seconds whether or not the text appeared, for media that boot with no prompt at all |
| `until` | words that end a wait loop |
| `until_any` | the alternatives form of `until:`: ONE of these word-sets must be on screen. `doUntil` reads it beside `until:` and joins the two into one list of alternatives |
| `poll` | seconds between looks in an until: loop |
| `scale` | enlargement for a cropped read; small text needs it |
| `screenshot` | save the screen to this path, for a post-mortem nobody had to ask for |
| `desktop` | wait until a desktop is on screen; options include family and timeout |
| `ssh` | a command run over ssh IN the guest; the exit status is the verdict |
| `require_ssh` | wait for sshd before the steps that use it |
| `ssh_probe` | the command that wait runs; `true` is not a Windows command |
| `ssh_after_reboot` | on a require_ssh: wait out the old daemon first, or the wait passes on the process that is about to die |
| `scp` | copy a file in. [local-file, remote-path], or {template: KIND, to: REMOTE} to render a bootstrap file (launcher, macos_agent_plist, windows_task) and copy it |
| `adb` | a command run over adb IN the guest; Android's remote access is adbd and there is no sshd until a stage installs one |
| `adb_privileged` | prove the adb shell can change system state - a write that reads back. NOT `adb root`: this guest is a `user` build with ro.debuggable=0 and no `su`, and the shell it already has owns appops, settings, cmd and setprop |
| `adb_connect` | attach to the guest's adbd on its forwarded port and prove it answers |
| `do` | a pane's actions, in order, run in one pass |
| `release` | which OS release is being driven; the Apple Account pane differs by release |
| `write` | write one of the login/bootstrap files on the HOST, for the scp: after it. Kinds: launcher, macos_agent_plist, macos_mount_script, macos_mount_plist, windows_task |
| `esp_fallback` | copy a disk's EFI loader tree to \\EFI\\BOOT and repair its config search. The value is the disk (`disk` for the one this stage built) or {disk, from}, where `from` is the loader directory inside the ESP |
| `settle` | seconds to wait where there is NO fact to wait on; the older `wait:` and `wait_after:` both become this |
| `wait_until` | wait for a CONDITION about the machine: `stopped` - its process has gone. A file that sent `acpi: power` or `ssh: poweroff` says this next, and a file that did not gets a clear message instead of a disk qemu still holds open |
| `fetch` | produce one host-side input: an installer image, a guest agent, a firmware, an answer CD. The value names WHAT, not how it is obtained |
| `template` | with `generate: answer_iso`: the answer file to substitute into. It is a path in the repository because the placeholders are the file's vocabulary, not a download |

## 4. Subkeys — written INSIDE another key

| key | what it does |
|---|---|
| `file` | with `disk:`: the disk file the step names or creates |
| `size` | with `disk:`: the size in GiB to create it at (was `disk_gb:`) |
| `gpt` | with `disk:`: write a GPT partition table (was `disk_gpt:`) |
| `esp_mib` | with `disk:`: the size of the EFI system partition (was `disk_esp_mib:`) |
| `min_mb` | with `disk:`: the smallest a finished disk may be (was `disk_min_mb:`) |
| `wait` | with `disk:`: wait this long for the file to stop growing (was `disk_settle:`) |
| `rect` | the region to measure: as a FRACTION of the frame (`middle`, `bottom 4.5%`), or `[x0, y0, x1, y1]` pixels. With `check:`, the region asked about; with `desktop: {reference:}`, the part of the picture compared - which is how a wallpaper is compared without the installer window that comes to sit over it |
| `rect_at` | with a PIXEL `rect:`: [width, height] the rectangle was measured at, so it can be scaled for a guest drawing at another size |
| `of` | with the retired `see:`: which numbers to bind - mean, spread, sat, flat_rows, contrast, min, max, or text. A question is a `check:` now |
| `reference` | the image to compare against, relative to the walkthrough: with a `check:` of `mode: like`, the region; with `desktop:`, the whole frame - which replaces six brightness thresholds with one measurement |
| `mode` | with `check:`: HOW the frame is questioned - like, mean_between, sat_between, spread_between, text |
| `words` | with `check: mode: text`: the word-sets, any one of which satisfies it |
| `without` | with `check: mode: text`: words that must NOT be on screen |
| `range` | with a `check:` mode that compares a number: [low, high] |
| `pattern` | with `check: mode: matches`: the regular expression |
| `as` | the name the boolean a gate or wait produces is bound under |
| `from` | with `expect:`: the frame a `read:` captured, by its name; with `until:`/`repeat:`: the source (`console`) |
| `must` | with `check:`: a FALSE answer fails the step. Without it a check only binds the boolean, for an `if:` or a `loop: until:` to consume |
| `contains` | a substring the reading must hold |
| `contains_any` | any one of these word-sets must appear |
| `not_contains` | a substring the reading must not hold |
| `equals` | the reading equals this, normalised |
| `matches` | the reading matches this expression |
| `like` | the reading is similar to this string |
| `fuzzy` | how similar, for a fuzzy comparison |
| `ratio` | the share of words that must match |
| `mean` | mean brightness, 0..255 |
| `spread` | mean absolute deviation of brightness |
| `sat` | mean HSV saturation, 0..1 |
| `flat_rows` | rows one colour across the full width |
| `contrast` | how much the region varies |
| `vs` | the reading a comparison is made against |
| `min` | the darkest pixel in the region |
| `any_of` | any one of these values |
| `less_than` | a comparison: below this |
| `greater_than` | a comparison: above this |
| `at_least` | a comparison: this or more |
| `at_most` | a comparison: this or less |
| `between` | a comparison: within this range |
| `max` | with `loop:`: the attempt ceiling. Required - a loop with no ceiling presses a key until the stage times out |
| `every` | with `loop:`: seconds after which the `clock:` actions run once, independent of the reading |
| `clock` | with `loop: every:`: the actions to run once when the clock runs out. It is named `clock` because a bare YAML key spelled o-n parses as the boolean true |
| `fact` | with `until:`: the boolean a loop ends on |
| `then` | with `if:`: the steps that run when it is true |
| `else` | with `if:`: the steps that run when it is false |
| `path` | with `qga: file-read`/`file-write`: the file in the GUEST |
| `to` | with `scp: {template: KIND}`: the remote path it is copied to |

## 5. File header — not step keys

| key | what it does |
|---|---|
| `name` | what the log calls this walkthrough |
| `resolution` | [width, height] the coordinates were measured at |
| `recording_resolution` | [width, height] the VIDEO's size; required, not derived |
| `user` | the account the walk logs in as |
| `password` | the guest account's password |
| `var` | one variable, as a block of `default`/`from` |
| `needs` | files that must exist before the stage runs |

## 6. Retired — names a 1.0 file wrote

| key | becomes |
|---|---|
| `see` | `read` |
| `ocr` | `read` |
| `screenshot` | `read` |
| `check` | `expect` |
| `loop` | `repeat` |
| `click` | `press` |
| `type` | `press text` |
| `key` | `press` |
| `shutdown` | `stop` |
| `compress` | `stop with `save`` |
| `scp` | `copy` |
| `at_color` | `press colour` |
| `at_button` | `press button` |
| `at_bottom` | `press anchor bottom` |
