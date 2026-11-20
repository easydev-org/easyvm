# easyvm walkthrough — the operations, one by one

`docs/PRIMITIVES.md` is the table. **This is the manual**: every operation, what it
actually does to the machine, what it needs, what it binds, and what goes wrong.

Everything here is taken from the executor, not from recollection. Each entry names
the function that runs it, so a claim can be checked:
`grep -n 'func (e \*Executor) doClick' internal/run/`.

---

## How a step is read

A step is a mapping with a `name:` and then **one operation key**. The dispatcher is a
`switch` over which key is present, in a fixed order (`internal/run/executor.go`,
`runOne`). A step with two operation keys runs the first the switch matches and
**silently ignores the rest** — that is why `ocr:` + `check:` together is a load-time
refusal rather than a coin toss.

```yaml
- name: what the log will say          # REQUIRED, or the failure has no name
  click: [936, 628]                    # the operation
  settle: 3                            # a modifier, not an operation
```

Modifiers (`settle`, `timeout`, `optional`, `when`, `keep_alive`, …) are listed at the
end. They are **not** operations: a step that carries only modifiers does nothing.

---

# A. The machine

## `boot:` — start the machine this file owns

`doBoot`, `internal/run/actions.go`

```yaml
- name: boot the installer
  boot: true
  os: linux                 # which family: linux, windows, macos, freebsd, android
  iso: kubuntu.iso          # the medium to start from; absent means boot the disk
  disk: disk.qcow2          # the target this file created
  cpu: 4                    # vCPUs
  memory_mb: 4096           # RAM
  timeout: 1800             # how long the firmware may take to hand over
```

**What it actually does**: starts one qemu under the daemon's single ownership, with
`-cpu Skylake-Client-v4` (fixed — `-cpu host` is refused), UEFI firmware, a fresh
NVRAM, a virtio disk and a `usb-tablet` pointer. The daemon waits for QMP and for the
guest to answer before the next step runs.

**Its one rule**: `os:` is required. Without it the step cannot know whether to attach
an answer CD, an OpenCore ROM, or nothing.

**What goes wrong**: a boot that never hands over is a `timeout`, and the log says how
long it waited. `qmp … connect: connection refused` means qemu exited at startup —
read the daemon log, because that is where qemu's own complaint is.

## `reboot:` / `shutdown:` — stop it, by a NAMED method

`doReboot`, `doShutdown`

```yaml
- name: stop the machine
  shutdown: kill            # ssh | kill | acpi | guest_agent
  timeout: 120
```

**Why the method is a value and not a default**: measured, a live installer **does not
answer the power button** — a CI leg sat in "stop the machine" for forty minutes before
falling back to a kill. So a file says which one it means:

| method | what it does | when it is right |
|---|---|---|
| `ssh` | asks the guest's own init | an installed system, so the disk is clean |
| `kill` | destroys qemu | an installer, or anything that ignores ACPI |
| `acpi` | the power button | a system with ACPI and no ssh |
| `guest_agent` | qga `system_powerdown` | a Windows guest with the agent installed |

## `disk:` — create the target

`doDisk`

```yaml
- name: create the empty disk
  disk: disk.qcow2
  disk_gb: 128
  disk_gpt: true            # default; `false` writes an MBR
  disk_esp_mib: 300
```

Writes the partition table **before** the installer ever sees the disk, so the
installer is offered a device rather than a blank file. `disk_settle` + `disk_min_mb`
are the companion operation below.

## `disk_settle:` — wait for a disk to stop growing

`doDiskSettle`

```yaml
- name: the install writes the disk
  disk_settle: 300          # seconds of no growth that count as "done"
  disk_min_mb: 4000         # and it must reach this first, or it never started
  timeout: 3600
  poll: 20
```

**This is the operation that catches a fake success.** A walk whose clicks never
reached the installer finishes all its steps and produces a 200 KiB qcow2 header. The
floor is what turns that into a failure.

## `record:` / `compress:` — the video and the artifact

```yaml
- name: start recording
  record: kubuntu-install.mp4
- name: stop recording
  record: false             # `false` IS the stop: an absence and a stop are different

- name: publish the disk
  compress: out/kubuntu.qcow2
```

`record: false` is handled by a separate branch (`hasKey`, not `Has`) because a step
whose only key is a boolean false looked like "no operation" and failed with *no
implementation for this step's keys* — after the whole install had run.

## `esp_fallback:` — repair a loader that registered only in NVRAM

```yaml
- name: make the disk bootable on its own
  esp_fallback: disk        # or a path; `disk` means the one this stage built
  esp_from: /EFI/BLISSOS
```

**Why this exists**: an installer that registers its loader with `efibootmgr` puts the
entry in the **NVRAM, not the disk** — and every boot here makes a fresh NVRAM. So a
disk that booted in its own run boots to *"No bootable option or device was found"* in
the next. This copies the loader tree to `\EFI\BOOT\BOOTX64.EFI` and rewrites its
`search.fs_uuid`, which the installer recorded **before** formatting.

---

# B. The pointer and the keyboard

## `click:` / `double:` — one click

`doClick`, `doDoubleClick`, `internal/qmp/qmp.go`

```yaml
- name: next from welcome
  click: [1777, 1059]       # x, y in the file's own `resolution:`
```

**The single most important detail in this repository**: `Click` **moves, then
presses**. An earlier version sent position and button in one event and the guest
applied the press at the *previous* position — measured on ghostbsd, where
`/api/click` did not move the pointer at all while `/api/double` did.

`moveTo` also establishes the position **twice** (a corner, then the target), because
the first absolute report after device enumeration can be ignored.

**`double:` moves once and presses twice**, deliberately: moving between the presses
would be two clicks in two places.

## `at_button:` — click by position inside a window

```yaml
- name: erase
  at_button: [810, 376]
  within: [592, 240, 1289, 500]   # optional: the window's box, so the click is scaled
  fallback: [810, 376]
```

Replaces `at_window:`, whose premise was that a control's offset from its window is
constant — true, and useless, because **finding the window's edge was the unreliable
part**. Four attempts at one Disk Utility button were four attempts at the *scan*.

## `at_color:` — click the largest blob of one colour

`doAtColor`

```yaml
- name: the green zoom button
  at_color: [40, 200, 64]
  color_tolerance: 45       # how far a pixel may sit from it, for antialiasing
  color_region: [1200, 0, 1400, 60]   # search here only
```

**A colour is a fact the compositor draws**, not a guess: macOS paints that button
rgb(40,200,64) on every run, at every window height, in every Space. This is the only
click that needs no coordinate.

## `key:` — one keystroke or chord

`doKey`

```yaml
- name: submit
  key: ret
- name: open the terminal
  key: ctrl+alt+t
```

qemu qcodes, and `+` joins a chord. `ret` is Return, `esc` Escape, `space` Space.

## `type:` — type at the focus

`doType`

```yaml
- name: the password
  click: [1050, 563]        # optional: click the field first
  clear: true               # optional: select-all and clear before typing
  modifier: ctrl            # which is `ctrl` on Linux, `meta_l` on macOS
  type: '{{ password }}'
  type_gap: 0.02            # seconds between keys; 0.15 for a Windows shell
```

**Three behaviours from one key**, which is the one place this vocabulary is
implicitly polymorphic: with `click:` it clicks then types; with `clear:` it clears
first; alone it types where the focus already is.

## `write:` — one file on the HOST

```yaml
- name: write the launcher
  write: launcher           # launcher | macos_agent_plist | macos_mount_script |
                            # macos_mount_plist | windows_task
```

Writes beside the walkthrough, for the `scp:` after it. **A file is written and copied
in separate steps** because the Python predecessor's single step reported *"the
launcher install exited 1"* and named neither the file nor the action.

---

# C. Channels into the guest

## `run:` — type into the guest's own terminal

`doRun`, `internal/run/text.go`

```yaml
- name: install openssh-server
  run: |
    export DEBIAN_FRONTEND=noninteractive
    apt-get update && apt-get install -y openssh-server
  terminal_wait: 8          # open a terminal first and wait this long
  type_gap: 0.02
  line_gap: 1.0             # between lines
```

**Multi-line is not cosmetic**: each line is typed, then Return, then the next —
because `keys.for_text` refuses a newline, and a version that typed the body as one
string silently dropped everything after it.

**`terminal_wait` is what opens the terminal** (`ctrl+alt+t`). Without it the text goes
to whatever already has focus — measured: 74 `run:` steps in this repository do not
open a terminal, and four of those are Windows PowerShell steps where that is right.

## `ssh:` — one command, one exit status

`doSSH`

```yaml
- name: prove the kernel over ssh
  ssh: 'uname -s && echo PASSWORD_OK'
  timeout: 120
```

The verdict **is** the exit status. No output parsing, no OCR. This is the channel the
`vm-configure` stages use, and it is why they are cheap.

## `scp:` — one copy, checked on both sides

```yaml
- name: copy the launcher in
  scp: [/host/path/launcher.sh, /home/vmuser/launcher.sh]
```

The local size and the remote size are compared. `copy_in:` was a copy **plus** a size
check hiding in Go; the check is now every copy's property, so no file has to ask.

## `script:` — write on the host, copy in, run by path

```yaml
- name: install the launcher
  script: |                 # the body
    #!/bin/sh
    install -m 0755 launcher.sh /usr/local/bin/launcher
  script_shell: sh          # powershell when absent
  script_name: launcher.sh
```

Two steps' worth of work in one, and the reason it is safe: the body is **written to a
file** and run **by path**, so a shell quoting mistake is visible in the file rather
than eaten by the typewriter.

## `require_ssh:` — wait for sshd

```yaml
- name: the machine answers ssh
  require_ssh: true
  ssh_probe: 'echo up'      # the command that answers; `true` is not a Windows command
  ssh_after_reboot: true    # wait out the OLD daemon first
  timeout: 600
```

`ssh_after_reboot` exists because after a reboot the old daemon answers for a few
seconds — the wait passes on a process that is about to die.

## `share_serve:` / `share_mount:` — the host's share

```yaml
- name: serve the host share
  share_serve: true
  share_dir: /absolute/path  # what to export
- name: the guest mounts it
  share_mount: true
  share_family: linux        # linux | windows | macos | bsd
```

Two steps, not one, because **which side failed matters**: a server that would not start
and a mount that would not take are different repairs.

## `adb:` — the Android bridge

```yaml
- name: install the apk
  adb: 'install /data/local/tmp/app.apk'
  adb_privileged: true
  adb_connect: '127.0.0.1:5555'
```

---

# D. Measuring the screen — `see:`

These bind **values**. None of them decides anything. A `check:`/`want:`/`when:`
reads what they bound.

## `see:` — measure a rect, bind NUMBERS

`doSee`, `internal/screen/measure.go`

```yaml
- see:
    name: bottom
    rect: bottom 4.5%
    of: [mean, spread, sat, flat_rows]
```

Binds `bottom.mean`, `bottom.spread`, `bottom.sat`, `bottom.flat_rows`, `bottom.rows`,
`bottom.min`, `bottom.max` — **all of them, always**, so a comparison never depends on
whether `of:` happened to list the one it names. `of:` says what the *reader* cares
about; it is not a whitelist.

| measurement | what it is | measured example |
|---|---|---|
| `mean` | mean brightness 0–255 | mint body 39.9 |
| `spread` | mean absolute deviation — "is it flat" | panel border 0.2, wallpaper 8.4 |
| `sat` | mean HSV saturation 0–1 | kubuntu launcher 0.97, Calamares page 0.05 |
| `flat_rows` | rows that are one colour across the full width | mint panel 11 of 49 |
| `contrast` | max − min brightness in the rect | |
| `min` / `max` | darkest / brightest pixel | |

**`rect:` IS A FRACTION, NEVER A PAIR OF PIXELS**: `middle`, `top 4.5%`,
`bottom 40px`, or `[x0, y0, x1, y1]` as fractions. A fraction cannot be measured at
the wrong size — which is the failure the files record verbatim, *"a coordinate
measured at 1920x1080 but the framebuffer is 640x480"*. This is why `region_at:` is
gone: 42 uses, every one of them the header's own `resolution:`.

## `ocr:` — read a rect as words

`doOCR`

```yaml
- name: the partitions page
  ocr:
    name: parts
    rect: middle
    scale: 2
```

Binds `parts` → the word list, and writes `work/ocr/parts.txt` so a decision can be
audited from the words it was made on.

## `screenshot:` — keep a frame

```yaml
- name: photograph the screen
  screenshot: screen.png
```

Binds nothing. For a post-mortem nobody had to ask for.

## `desktop:` — wait for a desktop

`doDesktop`

```yaml
- name: wait for the live desktop
  desktop: linux            # linux | kde | gnome | cinnamon | xfce | windows | macos | android
  timeout: 5400
  poll: 5
  keep_alive: true
```

Each family is a measurement plus a verdict in Go. **This is the key the redesign is
about**: `docs/PRIMITIVES.md` shows the panel rule as four `check: colour:` criteria
that reproduce the Go's verdicts on mint, zorin and mx's BIOS menu — so a new screen
shape stops being an image build.

---

# E. Deciding — `want:`

These bind **a boolean**. None of them touches the screen.

## `check:` — one capture, many criteria, one boolean

`checkFrame`, `internal/run/checkframe.go`

```yaml
- check:
    name: page
    region: middle            # the default rect for text criteria with none
    text:
      - contains: [erase, disk]
      - not_contains: [installing]
    colour:
      - rect: bottom 4.5%
        spread: {less_than: 42}
        flat_rows: {at_least: 3}
    unless: [welcome, setup]
    any_of:
      - colour: [{rect: top 4.5%, flat_rows: {at_least: 3}}]
```

**ONE CAPTURE IS A CORRECTNESS REQUIREMENT, NOT A SPEED-UP.** Every `Driver.Frame()`
is a real screendump — `internal/daemon/driver.go` locks, calls `Screendump`, returns a
new file, nothing cached. So two separate `see:` steps read **two different moments**,
and on a repainting pane the numbers then describe no frame that ever existed.

### The three text comparisons answer three questions

```
contains: [erase, disk]    on 'Erase disk and install Ubuntu'    yes    it CONTAINS them
like: "erase disk"         on 'Erase disk and install Ubuntu'    0.345  it is NOT that
like: "install now"        on 'insall now'                       0.909  one glyph lost
contains: [ext4], fuzzy: 1 on 'ext'                              yes    distance 1
```

`fuzzy:` is an **edit distance per word**, default 0, capped at 2 — because a ratio
divides by length and punishes short words: `disk`→`dsk` is one glyph and scores 0.750,
so any ratio threshold refuses a pane's own button label. `install`→`uninstall` is
distance 2, which is why 2 is the cap.

`like:`/`ratio:` is the only **ratio**, and only for a whole region that is one
sentence (`root partition will be ext4`). It is not a page test: a full-frame ratio
cannot tell `install` from `uninstall` (0.778) or `welcome` from OCR's `Wesn Home`
(0.556).

### The pixel criteria

| key | compares |
|---|---|
| `mean` `spread` `sat` `flat_rows` `contrast` `min` `max` | that number, with `less_than` `greater_than` `at_least` `at_most` `between` `equals` |
| `vs:` | this rect's number against **another rect's** — `{rect: middle, mean: {greater_than: 40}}` |

`vs:` is the only composition, and it is why one rect cannot answer "is there a panel":
the panel is a **difference** from the body.

## `want:` — decide from `see:` numbers

`doWant`, `internal/run/see.go`

```yaml
- want:
    name: is_panel
    any:
      - bottom.spread: {less_than: 42}
      - all:
          - bottom.mean: {minus: body.mean, greater_than: 40}
          - bottom.sat: {greater_than: 0.05}
```

`all:` / `any:` / `not:`, nestable three deep. `{minus: <reading>, <relation>: <n>}`
subtracts another reading first — the only arithmetic, written as a word.

## `if:` — branch on a boolean

```yaml
- if: updater_shown
  then:
    - name: dismiss it
      click: [1464, 194]
  else:
    - name: nothing to dismiss
      wait: 1
```

## `loop:` — repeat until a fact is true

`doLoop`, `internal/run/loop.go`

```yaml
- name: wait for the installer window
  loop:
    max: 120                 # REQUIRED: a loop with no ceiling never ends
    do:
      - click: [420, 300]
      - settle: 5
    ocr: {name: screen, rect: full}
    check: {name: done, from: screen, mode: says_any, words: [[installation, method]]}
    until: done
```

Four parts and all four are required: the action, the reading, the comparison, and the
fact that ends it. **`max` is attempts, bounded by the step's own `timeout:`** — the
first version used a flat ceiling of 200 attempts and failed a real Ubuntu install,
because 200 × 10 s is thirty-three minutes and an install is longer.

---

# F. Modifiers

| key | applies to | what it does |
|---|---|---|
| `name` | every step | what the log says. Required. |
| `settle` | any action | wait this long after it. **Absorbs `wait:` and `wait_after:`** |
| `timeout` | any step | the ceiling; failure names the step |
| `poll` | waits | how often to look |
| `when` | any step | run only if this fact is true |
| `optional` | any step | may fail without failing the walk |
| `requires` | any step | capabilities that must answer: `ssh`, `qga`, `share`, `network`, `overlay` |
| `keep_alive` | waits | keep the screen awake while waiting |
| `type_gap` | `type`/`run` | seconds between keys |
| `terminal_wait` | `run` | open a terminal first, and wait this long |
| `clear` | `type` | select-all and clear before typing |
| `modifier` | `type` | which select-all chord: `ctrl` or `meta_l` |

---

# Where the redundancy still is, measured

The redesign's target state is in `docs/PRIMITIVES.md`. These numbers are what is
**still true of the code today**, and they are why the retirement is a plan rather
than a claim:

```
455 steps carry an action (click, key, type, run, double, at_button)
451 of them carry `wait_after`            <- 99%
```

**Almost every action pays a timed pause nobody measures.** That is the single
biggest duplication in the vocabulary, and `settle:` is what absorbs it: the executor
now reads `settle:` and falls back to `wait_after:`, so a file can migrate one step at
a time and no walkthrough stops loading.

The pause values themselves are the evidence for why a fact is better:

```
2s ×70   3s ×61   12s ×43   10s ×38   8s ×35   5s ×30   20s ×28   4s ×27
```

Eight different values, all guesses. The 28 steps at 20 s are the ones where the guess
was immediately followed by an actual measurement of the same screen — the bug class
that failed cachyos and endeavouros on CI, where the guess was right on the measuring
machine and wrong on the runner.
