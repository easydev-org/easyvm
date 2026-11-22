# The vocabulary, redesigned

This is a proposal, not a description of the code. It was written after reading
all 121 keys and counting how the 57 real walkthroughs use them, because the
question is "why do we keep changing Go and shipping an image", and that
question has a countable answer.

## What the numbers say

```
57 walkthroughs, 1429 steps
121 keys declared

ocr steps                              38
of those, region [0,0,1920,1080]       34   (89%)
of those, region_at [1920,1080]        38   (100%)

steps that are ONLY an ocr step        38
steps that are ONLY a check step       23
steps that are ONLY a click step      167

wait_after present                    469   (1 in 3 steps)
  of those, immediately followed
  by an ocr step                       28   (a wait then a separate read)
```

Four facts fall out of that:

1. **`region_at` is written 38 times and is wrong zero times.** The file already
   says `resolution: [1920, 1080]` at the top. A number that is always the same
   number the file already stated is not information.
2. **89% of OCR crops are the whole frame.** `region:` is optional in spirit but
   mandatory in syntax, so almost every file writes the full frame to satisfy the
   parser. The one case the key exists for - a crop - is 11% of uses.
3. **A read and its decision are two steps, always.** 61 steps exist only to be
   half of one thought. You cannot read `ocr` + `check` and see what was asked.
4. **`wait_after` is a guess in 1 of every 3 steps, and 28 times it is a guess
   followed by an actual measurement of the same screen.** Those 28 are the bug
   class that failed cachyos and endeavouros on CI: the guess was right on the
   workstation and wrong on the runner.

## Why that produces "change Go, rebuild the image"

Because the vocabulary has only ONE way to ask a question about pixels: a named
Go function (`desktop: linux/cinnamon`), reached through a dispatch table. Every
new screen shape is new Go, and new Go is an image build.

Measured: `internal/screen/screen.go` is 1219 lines, 742 of them not comments,
and the named predicates are:

```
IsWindowsDesktop   140 lines
IsMacOSDesktop     499 lines     <- the one that will never move
IsLinuxDesktop     121 lines
IsKubuntuLauncher  115 lines
IsCachyOSDesktop    69 lines
IsKDEDesktop        21 lines
IsGNOMEDesktop      37 lines
IsCinnamonDesktop   80 lines     <- what I added for mint, last week
IsXFCEDesktop        4 lines
```

That is the real cost. `IsCinnamonDesktop` exists because mint's panel is the
same colour as its wallpaper, and the only way to say "_flat row in the bottom
quarter with content under it_" was to write Go. It should have been a sentence.

## The design: three verbs, and a picture is a value

### 1. A step does one of three things

Today 121 keys are flat, and the dispatcher is a 40-branch `switch` on
`Has(key)`. The reader has no way to know that `click`, `at_color` and
`at_button` are the same verb, or that `says` and `says_any` differ only in a
quantifier.

Three verbs, and everything else is a modifier on the step:

```yaml
- do:    { ... }     # change the machine: click, type, key, run, ssh
- see:   { ... }     # measure the screen: produces NAMED VALUES
- want:  { ... }     # decide from those values: produces a BOOLEAN
```

`see:` is the missing primitive. It is `ocr:` generalised from "produce words"
to "produce numbers and booleans", and it is what makes a picture judgeable
without Go.

### 2. `see:` measures, and every crop is a rectangle with a name

```yaml
- see:
    name: panel
    rect: bottom 4%          # a named rect, or [x, y, w, h]
    of: {
      mean: brightness,      # 0..255
      spread: brightness,    # mean absolute deviation - "is it flat"
      sat: saturation,
      flat_rows: 3,          # how many rows are one colour across the width
    }
```

and then the decision is arithmetic the reader can check:

```yaml
- want:
    name: desktop_up
    is:
      - panel.spread < 3
      - panel.mean - body.mean > 40
      - body.sat > 0.05
```

**`region_at` disappears**, because the frame size is `resolution:` from the
header and a `rect` is expressed in FRACTIONS or named bands (`bottom 4%`,
`middle`, `top 40px`). A crop that is a fraction cannot be measured at the wrong
size. That single change deletes 38 lines of duplicated boilerplate and one
whole class of error ("coordinate measured at 1920x1080 but the framebuffer is
640x480").

**`ocr:` becomes a measurement like any other**, not a special step:

```yaml
- see:
    name: page
    text: true               # OCR this rect
```

and its result is usable by the same `want:` as numbers are.

### 3. `want:` is one comparison language, and it takes as many facts as it needs

Today `check:` takes ONE `mode:` and ONE `from:`. A panel test needs three
numbers from two rects; today that is impossible without Go.

```yaml
- want:
    name: up
    all:
      - text: {from: page, says_any: [[welcome], [install]]}
      - panel.spread: {less_than: 3}
      - panel.mean: {minus: body.mean, greater_than: 40}
```

`any:`/`all:`/`not:` are the only combining forms, and they are three words, not
a grammar. A comparison is always `<fact> <relation> <literal-or-fact>`, which is
what a person says out loud.

### 4. Waiting stops being a duration and becomes a condition

`wait_after: 8` is the single largest source of CI-only failures in this repo:
it is a guess that was true on the measuring machine. It becomes:

```yaml
- do: { click: next }
  until: up          # the fact from the `want:` above
  within: 90s        # a CEILING, not a duration
```

`wait_after:` stays in the language but loses its job: it is for a settle with
no measurable end (a repaint with no text change), and any use of it that is
followed by a `see:` is reported by the validator as a step that should have
waited on the fact instead. That is how "change Go, rebuild" stops growing: the
file cannot express the bug.

`loop:` collapses to the same thing, because a loop is a `do:` plus an `until:`
plus `max:`:

```yaml
- do:   [{click: [500, 764]}, {wait: 3}]
  see:  {name: page, text: true}
  want: {name: gone, not: {from: page, says: [installing]}}
  until: gone
  max: 20
```

which is what `loop:` already means - written as three sibling keys rather than
three nested mappings.

## What this fixes, keyed to real failures

| failure | today | after |
|---|---|---|
| mint's panel is its wallpaper's colour | 80 lines of Go + image rebuild | 3 lines of `want:` |
| cachyos/endeavouros page drift on CI | add `wait_after`, hope; then add arrival loops | `until:` a fact |
| `region_at` duplicated 38x | mandatory, always equal to `resolution:` | gone |
| a crop measured at the wrong size | error message at click time | cannot be expressed |
| "did it read the right page?" | `ocr` step, then `check` step | one `see:`/`want:` pair, printed together |

## The migration, and why it is not a rewrite

The three verbs are a REORGANISATION of what exists, not new behaviour:

```
click, double, at_color, at_button, at_bottom, key, type, run, ssh, scp, script   -> do:
ocr, screenshot, desktop, wait_for, at_console                                   -> see:
check (8 modes), if/then/else, until, expect                                     -> want:
boot, disk, record, compress, shutdown, reboot, snapshot                          -> do: (machine verbs)
```

`loop:` / `repeat_until:` / `unless:` / `expect_any:` / `until_any:` are the
same three-verb shape written twice, and collapse into `do:`+`until:`.

The 121 keys do not need deleting in one commit. The three verbs can be accepted
FIRST, translated onto today's keys by the loader, and every existing file keeps
running. Then each file moves when it is next touched, and the flat keys retire
one at a time. That is how this avoids becoming a second `panes:` - a parallel
model that lives beside the old one and rots.

## What `see:` needs in Go, and how big that is

Only the MEASUREMENTS, never a verdict:

```
brightness(rect) -> mean, spread, min, max
saturation(rect) -> mean
flat_rows(rect, tolerance) -> count
colour_present(rect, rgb, tolerance) -> fraction
text(rect) -> []word
```

Five numbers and a word list. `IsLinuxDesktop`, `IsCachyOSDesktop` and
`IsCinnamonDesktop` are all expressible over those five; `IsKubuntuLauncher` is
`saturation(rect)`; `IsMacOSDesktop` is not, and should stay in Go as a named
`see:` measurement (`dock: true`) rather than be forced into arithmetic.

That is the honest boundary: **a measurement is code, a verdict is data.** The
boundary is also where the current code already is - `screen.go` computes
`BrightnessDrop`, `BottomVar`, `BottomSat`; the mistake was that the COMPARISON
against thresholds lived next to it, so changing a threshold was a rebuild.

## The design was CHECKED against real frames before it was written down

The claim "a verdict is data" is worth nothing unless the data reproduces the
verdicts the Go reaches today. So the rule was evaluated over the frames already
in `internal/screen/testdata/`, with no Go involved:

```
                              body        edge        edge      flat
mint desktop 1920x1080       mean 39.9   bottom 4.5%  diff 7.1  rows 11
  today: IsLinuxDesktop=false  IsCinnamonDesktop=true (80 lines of Go)
zorin desktop 1280x720       mean 140.9  top 4.5%    diff 61.8  rows  2
  today: IsLinuxDesktop=true
mx grub 1920x1080            mean   n/a   top 4.5%    diff 28.8  rows  0
  today: IsLinuxDesktop=false  (a BIOS menu is not a desktop)
```

ONE rule answers all three:

```yaml
- want:
    name: desktop_up
    any:
      - all:                    # zorin: a panel that differs from the body
          - top.spread: {less_than: 42}
          - top.mean: {minus: body.mean, greater_than: 40}
          - top.sat: {greater_than: 0.05}
      - all:                    # ... the same along the bottom edge
          - bottom.spread: {less_than: 42}
          - bottom.mean: {minus: body.mean, greater_than: 40}
          - bottom.sat: {greater_than: 0.05}
      - top.flat_rows: {at_least: 3}       # mint: the panel's own border
      - bottom.flat_rows: {at_least: 3}
```

`mint` passes on `flat_rows=11` because its panel IS its wallpaper's colour and
no brightness difference exists to find. `zorin` passes on the brightness
difference it always did. `mx`'s GRUB menu passes neither and is correctly
refused. **That is `IsLinuxDesktop` and `IsCinnamonDesktop` replaced by fourteen
lines of YAML, and `IsCinnamonDesktop` - the Go I added last week - deleted.**

The two `all:` blocks are the same three comparisons repeated for the two edges.
That repetition is worth removing with a `for each edge in [top, bottom]` form,
and it is the one place this design still reads like a program; it is written out
twice here so the reader can see both before choosing a form for it.

## The 121 keys, and where they actually come from

Counted by what the key's PRIMARY effect is, then what only modifies it:

```
 20  do: the machine (lifecycle)     boot, disk, record, compress, snapshot, shutdown, reboot
 13  do: the guest (input)           click, double, at_color, at_button, key, type, press, write
 14  do: the guest (channel)         run, ssh, scp, script, adb, share_serve, share_mount
  4  see: measure the screen         ocr, desktop, wait_for, screenshot
 11  want: decide                    check, if, until, until_any, expect, expect_any, unless, loop
 13  MODIFIER: timing                wait, wait_after, poll, timeout, keep_alive, type_gap, line_gap,
                                     terminal_wait, change_settle, change_timeout, blind_after, once, max
  3  MODIFIER: region                region, region_at, scale
 11  MODIFIER: comparison            mode, words, without, range, text, pattern, as, fact, do, then, else
 27  MODIFIER: step                  name, optional, require, requires, require_ssh, ssh_probe, os,
                                     family, cpu, memory_mb, mem_mb, cpus, iso, recovery, asset, build,
                                     generate, template, host_log, release, stuck_after, end, ...
```

**FIFTY-FOUR OF THE 121 ARE MODIFIERS**, and that is the actual problem - not the
verbs. A vocabulary whose modifiers outnumber its verbs has modifiers that no
longer mean one thing. Measured, in the real files:

```
memory_mb  7      mem_mb       0     <- one of them is dead
os        56      family      15     <- two names, one meaning
until      0      until_any    0     <- DEAD. both.
loop      78      repeat_until 0     <- `repeat_until` is dead, `loop` is the only loop
do        48      then         1     <- `then` is the `if:` half of `do`
wait      13      wait_after 469     <- 36 to 1, and they are not the same thing
require   22      requires    21     <- the same word twice
script    16      script_shell 16    <- always together: 16/16
at_color   1      at_button    9
desktop   22      wait_for      0     <- `wait_for` is dead; its one caller is a hardcoded name
change_settle 0   change_timeout 0   <- BOTH DEAD
```

**FOURTEEN KEYS ARE DEAD OR NEAR-DEAD.** They are not dead because nobody needed
them: they are dead because a better key arrived and the old one was left in
place, which is how a vocabulary reaches 121. This design's first job is not to
add `see:` - it is to make the vocabulary smaller, and `see:` is how the dead
ones become unnecessary rather than merely unused.

## What the 54 modifiers should be

Nine, and each one names a QUESTION rather than an incidental:

```yaml
# 1. WHEN does this step apply?          -> when:      (a fact, from a want:)
# 2. WHICH machine does it act on?        -> on:        (a named machine, for split stages)
# 3. HOW LONG may it take?                -> within:    (a ceiling; failure names the step)
# 4. HOW LONG to settle first?            -> settle:    (seconds, when there is no fact to wait on)
# 5. WHAT MAY IT BE WRONG ABOUT?          -> optional:, retry:, accept:   (three, and they differ)
# 6. WHAT IS IT MEASURED IN?              -> rect:, text:, of:            (see:)
# 7. WHAT DOES SUCCESS MEAN?              -> wants:                         (want:)
```

`os:`/`family:` collapse to one. `require:`/`requires:` collapse to one.
`script:`+`script_shell:` collapse to a `shell:` value on `script:`.
`wait:`/`wait_after:` become `settle:` (13 uses) and `within:` (469 uses are
mostly guesses that a `want:` should own).

## The dead keys, as evidence for the shape

`until:` and `until_any:` are declared, documented, and used ZERO times, because
`loop:` replaced them and nothing removed them. The same is true of
`repeat_until:`. **A vocabulary that cannot retire a key grows forever**, and the
retirement mechanism has to be a test: a key used zero times in 57 real files,
with a successor that is used, is reported by the validator as dead and removed
from `allowedActionKeys` - which is the list `TestNoStepKeyIsAGoal` already reads.
That test exists; it just does not check for DEAD keys, only for GOAL ones.

## The Go-side tests that keep this from rotting

The sentence "a verdict is data" has to be enforced or it decays:

1. **No predicate may be a threshold.** A test walks `internal/screen` and fails
   on a new `Is<Something>Desktop` that is not reachable as a `see:` measurement.
   Today there are eight; the test fails at nine unless the ninth is arithmetic.
2. **Every golden frame is a fixture for the ARITHMETIC, not for a function.**
   `testdata/mint_desktop.png` is already there. The test reads the frame, runs
   the five measurements, and evaluates the `want:` from the walkthrough against
   the numbers - so a threshold change is caught without a machine.
3. **A `wait_after:` followed by a `see:` of the same rect is an error.** That is
   the CI-drift bug, and it is detectable statically.

## What I would do first, in order

1. `see:` with the five measurements and `text:`, plus `want:` with `all/any/not`
   and the `rect` forms. Accept them beside the old keys; the loader translates.
2. Rewrite `linux/mint/steps/install.walk`'s desktop wait in the new shape and
   DELETE `IsCinnamonDesktop`. If the numbers do not reproduce the verdict, the
   design is wrong and it stops there - one file, one night, no image.
3. Delete `region_at` once every `ocr:` uses the header's `resolution:`.
4. Express `until: <fact>` on a `do:` step and remove `wait_after` from the files
   where a `see:` follows it.
5. Only then touch `desktop:` as a key: it becomes a `see:`+`want:` PAIR stored
   in the file, and the dispatch table disappears.

## The one thing I would not change

`IsMacOSDesktop` is 499 lines and its thresholds are a photograph of one
operating system's dock. It stays Go, reached as a named measurement. Forcing it
into arithmetic would be the same mistake in the other direction.
