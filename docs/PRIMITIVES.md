# easyvm walkthrough — every primitive, in one table

The complete vocabulary. Every key is listed exactly once, in the section that says
what KIND of thing it is, with the number of times the 57 real walkthroughs use it.

**The counts and the completeness are enforced, not asserted**:
`internal/step/audit_test.go` fails when the table names a key the code lacks, when
the code declares a key the table omits, and when a key marked MIGRATED is still
used by a file.

```
147 keys declared in code
 81 are STEP keys        — 45 operations, the rest modifiers and gates
 66 are SUBKEYS          — written inside another key, never on a step
 42 rows of RETIRED keys — replaced, migrated, or dead
```

## The model: three verbs, and a picture is a value

```yaml
- do:    { click: [936, 628] }          # change the machine
- see:   { name: bottom, rect: bottom 4.5%, of: [spread, flat_rows] }
                                          # MEASURE -> binds NAMED NUMBERS
- want:  { name: up, any: [...] }        # DECIDE  -> binds a BOOLEAN
```

**A measurement is code; a verdict is data.** `see:` is the only place pixels are
touched; `want:` never reads the screen. That is what makes a new screen shape one
line in a file instead of a Go function and an image rebuild — measured as the
reason `internal/screen` grew to 1219 lines with nine `Is…Desktop` predicates.

---

# 1. `do:` — change the machine (24 keys)

## 1.1 The machine itself

| key | value | what it does | uses |
|---|---|---|---|
| `boot` | mapping | start the machine this file owns | 56 |
| `shutdown` | `kill` | kill the machine; every other way to stop a guest is a step of its own | 16 |
| `acpi` | `power` `reset` | send one ACPI message - it does not wait | 1 |
| `qga` | `ping` `info` `shutdown` `exec` `file-read` `file-write` `set-time` `interfaces` | one guest-agent command | 0 |
| `wait_until` | `stopped` | wait for the machine's process to go | 0 |
| `disk` | mapping | the file `boot:` starts from | 29 |
| `compress` | path | flatten the overlay into an artifact | 49 |
| `esp_fallback` | disk / `{disk, from}` | copy a disk's EFI loader to `\EFI\BOOT` and repair its search | 1 |

## 1.2 Input — the pointer and the keyboard

| key | value | what it does | uses |
|---|---|---|---|
| `click` | `[x, y]` | one click | 299 |
| `double` | `[x, y]` | one double-click, moving once | 6 |
| `at_button` | `[x, y]` | click inside a window, by position not title | 9 |
| `at_color` | `[r, g, b]` | click the largest blob of one colour | 1 |
| `key` | qcode or `ctrl+alt+t` | one keystroke or chord | 170 |
| `type` | text | type at the focus; with `at:` clicks the field first | 124 |
| `write` | kind | write one host-side file for a later `scp:` | 17 |
| `fetch` | kind | produce one host-side input (was `asset:`/`build:`/`generate:`) | 4 |
| `needs` | list | files that must be on disk before this stage runs | 22 |

## 1.3 Channels into the guest

| key | value | what it does | uses |
|---|---|---|---|
| `run` | shell text | type a command into the guest's own terminal | 78 |
| `ssh` | shell text | one command over ssh; exit status is the verdict | 266 |
| `scp` | `[local, remote]` | one copy, size-checked on both sides | 25 |
| `adb` | shell text | one command over the Android Debug Bridge | 16 |
| `require_ssh` | bool | wait for sshd before the steps that use it | 40 |

# 2. `see:` — measure the screen (4 keys)

| key | value | what it binds | uses |
|---|---|---|---|
| `see` | `{name, rect, of}` — `of: [text]` gives a word list, which is what `ocr:` was | `name.mean` `.spread` `.sat` `.flat_rows` `.contrast` `.min` `.max`, or `name.words` | **new** |
| `when_not` | words | run only if the screen does NOT say these | 10 |
| `when_not_any` | word-sets | the alternatives form of `when_not:` | 0 |
| `desktop` | `linux` `kde` `gnome` `cinnamon` `xfce` `windows` `macos` `android` | waits until that desktop's measurement passes | 22 |
| `screenshot` | path | saves a frame for a post-mortem; binds nothing | 3 |

`rect:` IS A FRACTION OF THE FRAME, NEVER A PIXEL PAIR — `middle`, `top 4.5%`,
`bottom 40px`, or `[x0, y0, x1, y1]` as fractions. A fraction cannot be measured at
the wrong size, which is the failure the files record verbatim: *"a coordinate
measured at 1920x1080 but the framebuffer is 640x480"*.

That is why `region_at:` is gone: it was written 42 times and was always the
header's own `resolution:`.

# 3. `want:` — decide (6 keys)

| key | value | what it does | uses |
|---|---|---|---|
| `check` | `{name, text?, colour?, any_of?, region?, must?}` | ONE capture, every criterion against it, one boolean | 128 |
| `when` | words | run this step only if the screen says these words | 16 |
| `do` | list | run these steps in order under one name | 8 |
| `if` | fact | branches: `{then: [...], else: [...]}` | 19 |
| `loop` | `{max, do, until}` | repeat `do:` until the fact is true, up to `max` | 79 |

## `check:` is the merged primitive

One capture, any number of criteria, one boolean out:

```yaml
- check:
    name: page
    region: middle
    text:
      - contains: [erase, disk]        # exact words, case/punctuation folded
      - contains: [ext4], fuzzy: 1     # within ONE edit per word
      - like: "install now"            # whole region ~ this sentence
        ratio: 0.85
      - not_contains: [installing]
    colour:
      - rect: bottom 4.5%
        mean:      {between: [20, 60]}
        spread:    {less_than: 42}
        flat_rows: {at_least: 3}
        vs: {rect: middle, mean: {greater_than: 40}}
    any_of:
      - colour: [{rect: top 4.5%, flat_rows: {at_least: 3}}]
    unless: [welcome, setup]
```

**WHY ONE STEP AND NOT `see:`+`want:` FOR THIS**: every `Driver.Frame()` is a real
screendump, so two steps read two different moments — and on a repainting pane the
numbers then describe no frame that ever existed. The single capture is a
correctness requirement, not an optimisation.

**THREE TEXT COMPARISONS, MEASURED, BECAUSE THEY ANSWER THREE QUESTIONS**:

```
contains: [erase, disk]    on 'Erase disk and install Ubuntu'    yes    (it CONTAINS them)
like: "erase disk"         on 'Erase disk and install Ubuntu'    0.345  no  (it is not that)
like: "install now"        on 'insall now'                       0.909  yes (one glyph)
contains: [ext4], fuzzy: 1 on 'ext'                              yes    (distance 1)
```

`fuzzy:` is an **edit distance per word**, default 0, capped at 2. A ratio is the
wrong unit for a short word: `disk`→`dsk` is one glyph and scores 0.750, so any ratio
threshold refuses a pane's own button label. `like:` is the only ratio, and it is
right only for a whole region that is one sentence.

# 4. Modifiers — each names a question (11 keys)

| key | value | the question | uses |
|---|---|---|---|
| `name` | text | what does the log call this? | 1526 |
| `when` | fact / words | does this step apply at all? | 16 |
| `optional` | bool | may it fail without failing the walk? | 30 |
| `timeout` | seconds | how long may the whole step take? | 533 |
| `settle` | seconds | how long to wait with **no fact** to wait on | 818 |
| `poll` | seconds | how often does a wait look? **only when not the default** | 1 |
| `type_gap` | seconds | how fast to type? | 16 |
| `terminal_wait` | seconds | open a terminal first, then wait this long | 68 |
| `clear` | bool | select-all and clear the field before typing | 17 |
| `modifier` | `ctrl` / `meta_l` | the select-all modifier for this family | 4 |

`settle:` absorbs `wait:` and `wait_after:` — the same key under two names, 818 uses
between them. `wait_after` was a timed GUESS in one step of three, and 28 of those
guesses were immediately followed by a real measurement of the same screen. That is
the bug class that failed cachyos and endeavouros on CI, where the guess was right on
the measuring machine and wrong on the runner.

# 5. Subkeys — written INSIDE another key (11 groups)

A SUBCKEY IS NOT A STEP KEY. `mode:` is written inside `check:`, and `rect:` inside `see:`; a step that
writes one at its own level is a step whose only key is an argument to a question nobody asked. Two
tables enforce that split - `Keys` for what a step may say, `Subkeys` for what belongs inside a value -
and `Validate` refuses `{mean: 40}` while accepting `{click: ..., when_not: ..., rect: ...}` (the gate
reads the region, the click does not).

| key | parent | value | uses |
|---|---|---|---|
| `rect`, `rect_at`, `scale` | `check`, `see`, `when`, `when_not`, `until` | the region to read, the size a pixel rect was measured at, the enlargement | 291 |
| `from` | `check`, `until` | the frame a `see:` captured, or the source (`console`) | 46 |
| `mode` | `check` | how the frame is questioned: `like`, `mean_between`, `sat_between`, `spread_between`, `text` | 128 |
| `words`, `without`, `range` | `check` | the words that satisfy it, the words that must not be there, the numeric bounds | 128 |
| `reference` | `check` | the image a `mode: like` compares the region against | 0 |
| `must` | `check` | a FALSE answer fails the step; without it a check only binds the boolean for an `if:` or a `loop:` | 0 |
| `as`, `pattern`, `text` | `check` | the name a gate binds, a regular expression, the text a comparison is about | 46 |
| `max`, `every`, `clock`, `fact` | `loop` | the ceiling, the seconds the clock runs, the actions it fires, the boolean it stops on | 79 |
| `then`, `else` | `if` | the branches | 21 |
| `of` | — | **DEAD**: the `see:` that measured numbers is gone; a question is a `check:` | 0 |
| `to`, `path`, `body`, `argv` | `scp`, `qga` | the remote path, the guest file, the bytes, the command line | 0 |
| `file`, `size`, `gpt`, `esp_mib`, `min_mb`, `wait` | `disk` | the folded form of `disk_gb:`, `disk_gpt:`, `disk_esp_mib:`, `disk_min_mb:` and `disk_settle:` — `disk: {file: disk.qcow2, size: 128}` | new |

## Step-level arguments that are NOT subkeys

These are written BESIDE the action that reads them (`boot: true` with `os:` on the next line), so
they live in `Keys` and `Validate` accepts them at step level. The list is here because a reader
looking for `os:` expects to find it in a table, and the difference from the section above is where
the file puts the key rather than what it means.

| key | parent | value | uses |
|---|---|---|---|
| `os` | `boot`, `write`, `fetch`, `desktop` | `linux` `windows` `macos` `freebsd` `android` | 56 |
| `iso`, `recovery`, `answer_cd`, `driver_cd` | `boot` | the media the machine starts from | 58 |
| `cpu`, `memory_mb`, `microvm`, `microvm_display` | `boot` | how much machine to build | 7 |
| `kernel`, `initrd`, `kernel_append` | `boot` | the kernel a microvm is handed, because there is no boot sector | new |
| `disk_gb`, `disk_gpt`, `disk_esp_mib`, `disk_min_mb`, `disk_settle` | `disk`, `compress` | the target's size, layout and the wait for it to stop growing | 30 |
| `esp_from` | `esp_fallback` | the loader directory inside the ESP | 1 |
| `template` | `scp`, `fetch` | what to render | 9 |
| `ssh_probe`, `ssh_after_reboot` | `require_ssh` | how to probe, and whether to wait out the old daemon | 11 |
| `adb_connect`, `adb_privileged` | `adb` | connection and privilege | 2 |
| `minimum` | `wait_until` | the threshold a `console_idle`/`disk_idle`/`frame_still` wait needs | 0 |
| `blind_after` | `until` | with `from: console`: go on after this long with no text | 2 |
| `colour`, `any_of`, `contains`, `equals`, `matches`, `like`, `fuzzy`, `ratio`, `not_contains`, `contains_any` | `check` | the text and pixel comparisons | 119 |
| `mean`, `spread`, `sat`, `flat_rows`, `contrast`, `min`, `max`, `vs`, `at_least`, `at_most`, `between`, `greater_than`, `less_than` | `check` | the measurements and the bounds they are held to | 291 |


<!-- A HANDFUL OF ARGUMENTS LOOK LIKE SUBCKEYS AND ARE NOT: `os`, `iso`, `disk_gb`, `template` and
their neighbours are written BESIDE the action that reads them, so they are step keys and live in
`Keys`. The difference is where the file puts them, and a reader can see it: `boot: {os: ...}` has the
argument inside the mapping, `boot: true` with `os: linux` beside it has it at step level. Both forms
exist, which is why `os` is an argument in `Consumes` rather than a subkey. -->


# 6. File header — not step keys (5 rows, 7 names)

| key | value | what it says |
|---|---|---|
| `name` | text | what this walkthrough is |
| `resolution` | `[w, h]` | the size every fraction and coordinate was measured at |
| `vars` | mapping | `{from: args.x, default: y}` |
| `ssh_user`, `ssh_port` | text / int | the account later stages log in as |
| `ends`, `waits_for`, `ends_at` | list / mapping | the old `panes:` model, read only by unported files |

`ends:`, `waits_for:` and `ends_at:` are the old `panes:` model. No current file uses
them; they are read only by files that have not been ported.

# 7. Retired — 34 rows

Three words, and they are not interchangeable:

* **MIGRATED** — the successor exists and **no file anywhere** uses this key any more. A key
  marked MIGRATED that a file still uses fails `audit_test.go`, so this claim cannot rot.
* **DONE in `<dir>/`** — the migration is finished for those files and not the others. It is a
  fact with a scope, verified by counting the keys in that directory.
* **DEAD** — nothing uses it anywhere and there is no successor; a key nobody needed.

THE SCOPE MATTERS BECAUSE THE MIGRATION IS PER DIRECTORY, AND IT IS NOT FINISHED EVERYWHERE. The four
families that were converted - `windows/11`, `macos/15`, `linux/ubuntu`, `bsd/freebsd` - say
`settle:`. THE OTHER DISTRIBUTIONS STILL SAY `wait_after:`, because their `.yaml` files came back with
the upstream merge and have not been converted yet: cachyos, endeavour, fedora-workstation, kubuntu,
manjaro, mint, mx, nixos, opensuse-leap-16.1, popos and zorin. Each is converted in its own time.

| key | uses | becomes |
|---|---|---|
| `mem_mb` | 0 | `memory_mb` — **MIGRATED** |
| `wait_after` | 0 | `settle:` — **MIGRATED**, 891 uses across 33 files |
| `wait` (the step verb) | 0 | `settle:` — **MIGRATED**. 118 `wait:` remain INSIDE `ocr:`, where it is the reading's own pause |
| `region`, `region_at` | 0 | `rect:`, `rect_at:` — **MIGRATED** |
| `ocr` | 0 | `see: {of: [text]}` — **MIGRATED**, 118 uses; the two produced identical readings |

| `scale` | 42 | `check: {scale: N}` |
| `require`, `requires` | 0 | `needs:` — **DELETED from the vocabulary**: `requires:` was never read by any code, so all 21 uses were silently ignored |
| `expect`, `expect_any` | 0 | `check: {must: true, text: …}` — **MIGRATED** |
| `until`, `until_any` | 79 | `loop: {until:}` |
| `family` | 0 | `os:` — **MIGRATED** |
| `asset`, `build`, `generate` | 0 | `fetch:` — **MIGRATED** |
| `script`, `script_shell`, `script_name` | 0 | `scp:` a file beside the walkthrough + `ssh:` — **DELETED**: the 16 bodies became real files, and windows/11/steps/dev.walk went from 46,315 to 6,932 bytes |
| `at_bottom` | 8 | `at_button:` |
| `skip_when_unfinished` | 5 | `if: finished` |
| `release` | 4 | `os: macos-15` |
| `color_tolerance`, `color_region` | 3 | `at_color: {tolerance:, within:}` |
| `at_fallback` | 1 | `at_button: {fallback:}` |
| `once` | 1 | `loop: {max: 1}` |
| `else` as a sibling | 2 | `if: {else: [...]}` |
| `unless`, `unless_any` | 0 | `when_not:`, `when_not_any:` — **MIGRATED** |
| `repeat_until` | 0 | `loop:` — **DELETED**: zero uses and no branch remains |
| `once`, `end`, `stuck_after`, `until_any` | 0 | the pane state machine — **DELETED** |
| `wait_for` | 0 | `check: {colour: …}` — **DELETED**: zero uses and no branch remains |
| `wait_for_change` | 0 | `check:` + `loop:` — **DEAD** |
| `until_dark` | 0 | `check: {colour: {mean: {less_than: N}}}` — **DEAD** |
| `change_settle`, `change_timeout` | 0 | `settle:`, `timeout:` — **DEAD** |
| `desktop_family` | 0 | `desktop:` — **DEAD** |
| `boot_from`, `disk_from`, `disk_copy`, `disk_overlay` | 0 | the mapping forms of `boot:`/`disk:` — **DEAD** |
| `snapshot` | 0 | — **DELETED**: zero uses and its branch called `notImplemented` |
| `reboot` | 0 | — **DELETED**: zero uses; a `boot:` of a running machine is a no-op already |
| `graceful` | 0 | `shutdown: {method:}` — **DEAD** |
| `press`, `sweep` | 0 | `at_button:`, `loop: {do: [{click: …}]}` — **DEAD** |
| `demo_script` | 0 | `write:` — **DELETED**: zero uses and it only ever answered `notImplemented` |
| `host_log`, `line_gap`, `adb_serial`, `share_dir`, `end`, `stuck_after`, `as`, `cpus` | 0 | — **DEAD** |
| `want`, `all`, `any`, `not`, `minus` | 0 | `check:` - **DELETED**: 265 lines of recursive group evaluation and zero walkthroughs ever wrote one, while `check:` was used 29 times |
| `mean`, `spread`, `sat`, `less_than`, `between` (as `check:` criteria) | 0 | **DELETED** with the retired `check: text`/`check: colour` syntax: those criteria were only ever read by `want:`, which nothing wrote |
| `record` | 0 | automatic - **DELETED**: the recording starts with the run and `finishRecording` encodes it however the walk ended |
| `until_console` | 0 | `until: {from: console}` - **DELETED**: it was `until:` with a different source, which is a parameter |
| `host_log` (step) | 0 | — **DELETED**: zero uses and no branch remains |
| `share_serve`, `share_mount`, `share_family` | 0 | the share is always on - **DELETED**: `share_mount:` had not mounted since the transport became HTTP, `share_serve:` set one variable, and `share_family:`'s `macos` and `linux` took the same branch |
| `keep_alive` | 0 | the wake is unconditional — **MIGRATED** |
| `desktop_family` | 0 | `desktop:` — **DELETED** |

## Why 29 keys are retired

They are not retired because nobody needed them. **Twenty of them are DEAD because a
better key arrived and the old one was never removed**, which is how a vocabulary
reaches 131 keys: `until:` was declared, documented, and used ZERO times while `loop:`
did the work 79 times. A vocabulary that cannot retire a key grows forever, so
`audit_test.go` reports a dead key as a failure rather than a curiosity.

## The rule that keeps it from growing back

**A key is a verb or a question, never a goal.** `dismiss:`, `copy_in:`,
`select_disk:` and `apple_account:` were removed for this reason already — each hid a
loop, a click sequence or a tolerance inside Go, so the file said one thing and the
code did three.

---

# The arithmetic this vocabulary makes possible

These numbers were measured from the frames already in `internal/screen/testdata`,
with no Go involved, and they are what `measure_test.go` asserts:

```
                       body            edge            edge diff   flat rows   verdict
mint   1920x1080   mean 39.9   bottom 4.5%  spread 8.5      7.1     11/49       desktop
zorin  1280x720    mean 140.9  top 4.5%     spread 25.7    61.8      2/32       desktop
mx grub 1920x1080  mean 56.4   bottom 4.5%  spread 40.7     4.6      0/49       NOT a desktop
```

One rule covers all three — so `IsLinuxDesktop` and `IsCinnamonDesktop` (the 80 lines
of Go added for mint, whose panel is the same colour as its wallpaper) both become:

```yaml
- check:
    name: desktop_up
    any_of:
      - colour: [{rect: top 4.5%,    spread: {less_than: 42}, vs: {rect: middle, mean: {greater_than: 40}}, sat: {greater_than: 0.05}}]
      - colour: [{rect: bottom 4.5%, spread: {less_than: 42}, vs: {rect: middle, mean: {greater_than: 40}}, sat: {greater_than: 0.05}}]
      - colour: [{rect: top 4.5%,    flat_rows: {at_least: 3}}]
      - colour: [{rect: bottom 4.5%, flat_rows: {at_least: 3}}]
```

`IsMacOSDesktop` is 499 lines and stays Go, reached as a named measurement: forcing a
photograph of one operating system's dock into arithmetic would be the same mistake
inverted.
