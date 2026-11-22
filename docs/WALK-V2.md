# easyvm 2.0 — the `.walk` format, redesigned

This is a design, not a migration note. Everything below is what a walkthrough should look like
once the five problems in the current format are gone, and every claim about the current format is
measured against the 62 files in this tree (1577 steps).

The five problems, with the measurement that shows them:

1. **`==` carries five different syntaxes.** `settle == 2`, `click == [715, 267]`,
   `check == {from: details, ...}`, `see == name: recovery` (a bare YAML mapping), and
   `loop == {check: {...}, do: [{...}], max: 40}` (a whole document). Looking at one line does not
   tell you whether the right side is a value or a document.
2. **Braces.** 384 of them: 209 in step bodies, 175 in `var`/`header` lines, plus 55
   `do == [{...}, {...}]` list-of-mapping lines.
3. **`name` means three different things.** The step's name (`step "x"`), a label that is then
   overridden by `, named "..."` (143 lines, of which **142 repeat the same string twice**), and a
   bound boolean inside `check:` (`check: {name: has_install}`).
4. **Six ways to wait**: `settle` 551, `loop` 153, `terminal_wait` 59, `wait_until` 32, `poll` 14,
   `until` 5.
5. **Dead names.** 19 of the 83 step keys are produced by no file at all; `ocr`, `see` and
   `screenshot` are three names for one operation, and `screenshot` is used zero times.

---

# 1. The one rule

```
one line = one verb, then its required argument
indentation = what belongs to what
```

**There is exactly one structural marker, and it is indentation.** No braces, no `==`, no commas, no
brackets around a mapping. A line at one depth is a sibling of the line above it; a line indented
under it is its child. That is the whole grammar of structure.

What may stay on the verb's own line is only what the verb cannot do without:

```
press 960 628            two numbers: the click has no meaning without them
press Return             the key
run uname -a             the command
copy a.sh /tmp/a.sh      the two paths
read recovery            the name to bind
stop kill                the method
```

Everything else - each optional field, each list, each nested structure - goes on its own line,
indented under its parent. So a click's `after` is a child, an `expect`'s `from` and `words` are
children, and a `repeat`'s `do` and `until` are children:

```
press 960 628
  after 2

expect
  from screen
  words [password]
```

`from` and `words` are **siblings**, both children of `expect`. Neither is written on the `expect`
line, because neither is required for the verb to mean anything - and a rule that says "children are
indented" admits no exceptions, or it is not a rule.

A value is a **scalar** or a **flat list of scalars**. Those are the only two shapes that fit on one
line, and they are why `words [a, b]` and `click == [640, 690]` do not need a block: a list of
values has no keys, so there is nothing to indent.

This is the whole syntax. Anything a file needs that is not covered by it is a bug in the design,
not a reason to add a second syntax.

---

# 2. The file header

```
walkthrough "linux-fedora-workstation - sshd"
resolution 1920x1080
recording_resolution 1920x1080
user vmuser
password admin

var out
  default out/distro.qcow2
  from args.out

needs disk.qcow2
```

- `var` is a mapping, so it is a block. No braces, no `default: x` on the same line.
- `needs` is a flat list of names, so it is one line.
- `resolution` and `recording_resolution` are two numbers, so they are one line.
- **`recording_resolution` is required** and is not derived from `resolution`: one is where a
  coordinate was measured, the other is how big the video is. A default would silently letterbox a
  720x400 guest into 1920x1080.

---

# 3. A step

```
step "the greeter is drawn"
  optional
  repeat
    until
      from screen
      words [[welcome, to, fedora], [start, setup]]
      within 0 150 1920 1080
      at 1920 1080
    max 90
```

- **`step "name"` is the only place a step is named.** There is no `, named "..."` clause, no
  label that gets overridden, and no second `name` inside a nested mapping.
- The body is one or more **verbs**, each with its own children.
- Modifiers (`optional`, `timeout`, `after`, `when`) are children of the step at the same depth as
  the verb, because they qualify the whole step.

---

# 4. Nine verbs

Every step does exactly one of these. A step may also carry modifiers.

| verb | value | what it does |
|---|---|---|
| `boot` | block: `os`, `memory`, `disk`, `iso`, ... | start the machine |
| `press` | coordinate, key, or text | click, key, or type into the guest |
| `run` | command, plus `via` | one command, over the console, ssh or adb |
| `copy` | local and remote path | one file into the guest |
| `read` | a name | capture one frame and bind it to that name |
| `expect` | block: `from`, `words`, `within`, ... | assert something, once |
| `control` | `acpi`, `qga`, or `stopped` | a machine-level message |
| `if` | block: condition, `then`, `else` | branch on a fact |
| `stop` | method, plus optional `save` | stop the machine, optionally publishing the disk |

**`wait` is not a verb.** It is `repeat ... until`. **`save` is not a verb.** It is
`stop: {save: ...}`.

---

# 5. The verbs in detail

## 5.1 `boot`

```
step "boot the installer"
  boot
    os linux
    memory 4096
    disk
      file disk.qcow2
      size 128
      gpt
      esp_mib 512
      min_mb 4000
      wait 300
    iso kubuntu.iso
```

A disk that already exists is one line:

```
  boot
    os linux
    disk out/distro.qcow2
```

`boot` with a sub-block of `disk` is a disk to create; `boot` with `disk` as a path is a disk that
is there. The engine already distinguishes the two by the presence of `size`.

## 5.2 `press`

```
step "click the password field"
  press 960 628

step "press Return"
  press Return

step "open a terminal"
  press ctrl+alt+t

step "type the password"
  press text "{{ password }}"
    clear
    select ctrl

step "double-click the disk icon"
  press 292 927
    double

step "click measured from the bottom edge"
  press 10 1
    anchor bottom

step "click the largest red blob"
  press colour 255 0 0

step "click the button, or the fallback if there is no window"
  press 703 336
    fallback 10 10

step "type slowly into a field it clicks first"
  press text terminal
    at 960 59
    gap 0.15
```

Six old keys (`click`, `double`, `at_button`, `at_color`, `at_bottom`, `at_fallback`) are one verb
with six argument forms. The discriminator is the first word after the verb, which is the same rule
`expect` uses.

## 5.3 `run`

```
step "keep the screen on"
  run xset s off; xset -dpms; xset s noblank

step "prove the kernel"
  run uname -a
    via ssh

step "ask adb"
  run id
    via adb
```

`via` is the transport: `console` (default), `ssh`, `adb`. Three old keys (`run`, `ssh`, `adb`) are
one verb. A `run` over ssh has its exit status as the verdict; a `run` over the console does not,
and that is why `terminal_wait` disappears: the console cannot tell you it failed.

## 5.4 `copy`

```
step "put the installer script in"
  copy install-buildtools.ps1 C:/Users/vmuser/install-buildtools.ps1

step "put a rendered launcher in"
  copy bootstrap.sh /tmp/bootstrap.sh
    template launcher
```

## 5.5 `read`

```
step "look at the recovery pane"
  read recovery
```

**`read` does exactly one thing: capture the whole screen and bind it to a name.** It takes no
region, no scale, and makes no judgement. Its job is to make a frame that later `expect` steps can
ask any number of questions about, from the same pixels.

`ocr`, `see` and `screenshot` are all this one operation under three names, and two of them are
dead: `ocr` is never produced, `screenshot` is used zero times and binds no name a `expect` can
read. They are deleted, not aliased.

**There is no `read ... to path.png`.** `read` already puts every frame in the run's library, which
is what a report reads from. A second way to save the same picture is a second name for one thing.

## 5.6 `expect`

```
step "the greeter's password box is up"
  expect
    from screen
    words [[password], [vmuser]]
```

Eight forms, one verb. The form is the name of the child that carries the condition:

```
expect                    the screen says these words
  words [password]

expect                    the screen says none of these
  none [continue, back]

expect                    the region's brightness is in this range
  mean 0 110

expect                    the region matches this image
  reference reference/desktop.png
  ratio 0.9 1.0

expect                    a desktop of this family is up
  desktop linux/gnome

expect                    sshd answers
  port 2222
  shell
  after_reboot

expect                    a file exists in the guest
  file /etc/ssh/sshd_config

expect                    the machine is gone
  stopped
```

- **`from <name>` asks about a frame a `read` bound.** Without `from`, `expect` reads the live
  screen once. That is the only place a live capture is implicit.
- **A condition that names a picture MUST say `from`.** This is a rule, not a default: `from` and
  `words` are siblings, and a file that reads a picture without naming it is a file whose meaning
  depends on a name happening to match. MEASURED: in every one of the 154 existing loops,
  `loop.see.name` and `loop.check.from` are the SAME string - the pairing worked only because the
  two names happened to agree, and nothing would have caught it if they had not.
- **`expect` runs ONCE.** It asserts and fails the step; it does not retry. A file that needs to
  wait says `repeat ... until` (§7), because a retry is a loop and a loop is a statement. MEASURED:
  all 37 top-level `check` mappings in this tree are one-shot, and every one of the 154 retries is a
  `loop`. Two ways to say "try again" would be one too many.
- **`within` is the only region.** `read` has none; `expect` owns it, because the region is part of
  the question and not part of the picture. This is already true in the engine:
  `refuseLegacySee` rejects a `see:` with a `rect:`.

`check`, `when`, `when_not`, `when_not_any`, `loop`, `until`, `poll`, `wait_until` and
`terminal_wait` are all `expect` or `repeat`. Six ways to wait become one.

## 5.7 `control`

```
step "reset the guest"
  control acpi reset

step "read the hostname over the agent"
  control qga file-read
    path /etc/hostname

step "wait for the process to go"
  control stopped
```

`control stopped` is the old `wait_until: stopped`. It is a question about the machine, and this is
the verb for questions about the machine.

## 5.8 `if`

```
step "answer the sheet when it is there"
  if
    words [are, you, sure]
    then
      item "click Continue"
        press 1018 520
    else
      item "press Escape"
        press Escape
```

A branch is a list of steps, so each item is a `step`-shaped block without repeating `step`. The
condition is the same vocabulary as `expect`, so there is one way to ask a question.

## 5.9 `stop`

```
step "stop the machine"
  stop kill

step "stop, then publish the disk"
  stop kill
    save "{{ out }}"
```

`shutdown` and `compress` were always required in that order, so they are one step.

---

# 6. Modifiers

Six, and each works on every verb with the same meaning.

| modifier | value | meaning |
|---|---|---|
| `optional` | none | a failure here does not fail the step |
| `timeout` | seconds | how long the whole verb may take |
| `after` | seconds | a pause AFTER the verb, when there is no fact to wait on |
| `when` | condition block | run only if the condition holds |

`after` is the successor to `settle`, and it is deliberately a modifier rather than a verb: a pause
with no fact to wait on is a property of the action that needs it. `settle 8s` as its own step is
gone, because 28 of the 818 timed pauses were immediately followed by a real measurement of the same
screen, and two of those failed on CI where the guess was right on the measuring machine.

**`optional` is forbidden on a step that acts.** A file that says `optional` on a click is a file
saying "click, and it is fine if this fails" - but the click still happens, into whatever is on
screen. The engine already refuses this (`CheckOptionalDoesNotAct`); the format makes it harder to
write by putting `optional` beside `expect`, where it means something.

---

# 7. `repeat ... until`

The engine's `loop:` has `do`, `check`, `see`, `until`, `max`, `every`, `clock`. It is replaced by
**one statement**, `repeat`, whose body is a list of steps and whose stop test is a condition:

```
step "answer the wizard until the desktop appears"
  read wizard
  repeat
    do
      press 1228 826
      after 4
      expect
        from pane
        words [continue]
    until
      from wizard
      words [finder]
      none [continue, back, later]
    max 40
```

- **`repeat` is a statement, not a verb with flags.** Its `do` is a real list of steps, so it may
  contain `if`, another `repeat`, `read` - anything a step body may contain. MEASURED: 3 of the 154
  loops in this tree already nest an `if` inside their body, so a flat form could not express them.
- **`until` is a condition**, written with the same children `expect` uses. There is one way to ask
  a question, whether the answer ends a loop or fails a step.
- **`until` MUST say `from`** when it reads a picture, for the reason §5.6 gives: 154 of 154 loops
  pair a `see` with a `check` of the same name today, and nothing enforced it.
- **`do` / `until` / `nudge` are children of `repeat`**, and that is the complete set. There is no
  retry interval: the pause between attempts is a `settle` in the `do`, which is where a person
  already writes it. MEASURED: every one of the 154 loops has its per-attempt pause as a `settle` in
  its `do`.

### Pure polling

119 of the 154 loops have a `do` that is nothing but a pause. That pause is the loop's own wait, so
the statement is just the test:

```
step "wait for the greeter"
  repeat
    until
      from screen
      words [password]
    max 40
```

### `nudge`: the once-only clock, and why `every` is gone

Eight loops - all in `macos/26/steps/setup.walk` - also need to take an action **once**, after the
loop has been running for a while, because macOS's installer goes idle and the screen never changes
on its own.

The old form had two keys for this, `every:` and `clock:`, and **`every` had no meaning of its own**:
MEASURED, it appeared 8 times in this tree and all 8 were beside a `clock`, and no loop has one
without the other. A key that only ever appears with another key is that key's argument, so it is
written inside it:

```
step "answer the installer until it finishes"
  read screen
  repeat
    do
      settle 20
      read cancelshot
      expect
        bind cancel_sheet
        from cancelshot
        words [continue, installing]
    until
      from screen
      words [welcome]
    max 270
    nudge 5400
      press 731 547
      after 15
      control acpi reset
      timeout 300
      after 60
```

**`nudge 5400` reads as "once 5400 seconds have passed", and it fires AT MOST ONCE per loop.** That is
what the engine does today (`fired = true` after it runs), and a design that said otherwise would
change behaviour on the eight longest runs in this tree.

**It is one pattern in eight places, not eight needs.** All eight are the same three actions. If it
is ever needed outside macOS, the honest thing is a named `control` action - `control nudge` -
rather than a more general `nudge` whose only user would be the same eight loops.

### `every` is deleted

There is no `every` in v2, and not as a rename: there is no way to say "pause N seconds between
attempts" other than a `settle` in the `do`, because that is what every file already does and what
the executor already runs. A second way to name the pause is the defect this design exists to remove.

---

# 8. Coverage: what replaces what

| v1 key | v2 | measured uses |
|---|---|---|
| `click`, `double`, `at_button`, `at_color`, `at_bottom`, `at_fallback` | `press` | 269 / 3 / 0 / 2 / 0 / 0 |
| `run`, `ssh`, `adb` | `run` + `via` | 97 / 346 / 16 |
| `disk`, `disk_gb`, `disk_gpt`, `disk_esp_mib`, `disk_min_mb`, `disk_settle`, `disk_copy`, `disk_from`, `disk_overlay` | `boot` + `disk` block | 33/19/1/1/7/7/0/0/0 |
| `needs` | header `needs` | 29 |
| `settle`, `loop`, `poll` | `after`, `repeat ... until` | 551 / 153 / 14 |
| `settle` with no fact | `after` on the action | — |
| `wait_until`, `terminal_wait` | `expect stopped`, `read` + `expect` | 32 / 59 |
| `shutdown` + `compress` | `stop` + `save` | 29 / 53 |
| `when`, `when_not`, `when_not_any`, `if` | `if` + `when` modifier | 19 / 6 / 1 / 3 |
| `require_ssh`, `ssh_probe`, `ssh_after_reboot` | `expect port 2222` | 39 / 4 / 3 |
| `see`, `ocr`, `screenshot` | `read` | 51 / 0 / 0 |
| `check` | `expect` | 36 |
| `desktop` | `expect desktop` | 22 |
| `fetch` | `fetch` (kept: it is not a screen or a machine action) | 4 |

The dead 19 step keys and 26 subkeys are deleted outright:

```
adb_serial argv at_bottom at_button at_fallback body boot_from cpu cpus
disk_copy disk_from disk_overlay esp_from line_gap microvm_display minimum
qga screenshot write
```

(`qga` and `microvm_display` keep their behaviour under `control qga ...` and `boot`; the *names* go.)

---

# 9. The count

| | v1 | v2 |
|---|---|---|
| step-level words | 83 | **9 verbs + 6 modifiers** |
| ways to write a value | 5 (`==` scalar, `==` list, `==` braced map, bare YAML map, whole document) | **2** (one line, or a block) |
| braces | 384 | **0** |
| ways to name a step | 3 | **1** |
| ways to wait | 6 | **1** |
| ways to click a point | 6 | **1** |
| ways to run a command | 3 | **1** |
| ways to assert | 4 | **1** |
| ways to capture a frame | 3 | **1** |

---

# 10. What this does not change

**`internal/run` is not rewritten.** v2 is a front end that compiles to the same internal step shape
the executor already understands, because every action in it was measured against real guests and
the bugs were in the source language, not the engine.

```
  .walk v1 ──┐
              ├─→ walk.Step ──→ internal/run (unchanged)
  .walk v2 ──┘
```

The acceptance test is not an opinion: **every one of the 62 files converts mechanically, and the
converted file produces the same 1577 steps as the original.** The baseline is captured at
`/tmp/docs_before.json`; a single different byte is a design bug.

---

# 11. The open question

`also` (§7) is the only new concept the design needs that the old vocabulary had no name for. It
exists for one measured case: macOS's installer sleeps, and eight loops already work around it. The
alternative is to keep `loop` as a tenth verb with `clock`, which is a second way to write a wait -
the thing this design exists to remove.

The design above chooses `nudge`. If it turns out to be wrong, the honest fix is a named
`control` action, not a return to `loop` and not a second retry interval.
