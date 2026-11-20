# `check:` — one capture, many questions

This is a design note, not a description of the code. It answers the question
"can the whole screenshot-and-inspect flow be ONE primitive", and the answer is
yes — with one condition that the current design gets wrong.

## Why one step, and not `see:` + `want:`

**BECAUSE EVERY `Frame()` IS A REAL SCREENSHOT.** Measured in
`internal/daemon/driver.go`: `Frame()` takes `frameMu`, calls `Screendump`, and
returns a NEW file each time. Nothing caches it.

So this pair, as I proposed it yesterday:

```yaml
- see:  {name: bottom, rect: bottom 4.5%, of: [spread, flat_rows]}
- see:  {name: body,   rect: middle,       of: [mean]}
- want: {name: up, all: [{bottom.spread: {less_than: 42}}, ...]}
```

is **three separate screenshots of three different moments**, and it compares
numbers from moments that need not agree. On a repainting pane that is not a
performance problem, it is a WRONG-ANSWER problem: `body` can be measured while
the wizard is fading and `bottom` after it settles, and the two numbers then
describe no frame that ever existed.

The single capture is therefore not an optimisation. It is the only way the
comparisons are about the same picture.

## The shape

```yaml
- check:
    name: page                 # the boolean this step binds
    region: middle             # OPTIONAL: the default rect for criteria that name none
    text:                      # any number of OCR criteria, all on the SAME frame
      - contains: [erase, disk]
        fuzzy: 0.85            # optional, per character ratio
      - not_contains: [installing]
    colour:                    # any number of pixel criteria, all on the SAME frame
      - rect: bottom 4.5%
        mean: {between: [20, 60]}
        spread: {less_than: 42}
        flat_rows: {at_least: 3}
      - rect: middle
        mean: {between: [30, 50]}
    any_of:                    # a group that passes if ONE inside it passes
      - colour: [{rect: top 4.5%, mean: {greater_than: 100}}]
      - colour: [{rect: bottom 4.5%, flat_rows: {at_least: 3}}]
    unless: [welcome, setup]   # every word here must be ABSENT
```

`name:` is required, because the result is a fact a `loop:`/`until:`/`if:`
tests. Everything else is optional and additive: **one capture, every criterion
evaluated against it, one boolean out.**

## The three criterion kinds, and what each is for

### 1. `text:` — OCR, and THREE kinds of comparison because they answer three questions

The trap here is that one criterion cannot do all three jobs, and picking the wrong
unit makes the criterion either useless on short words or dangerous on long ones.
Measured, on the strings this repository's panes actually produce:

```
A. DOES THE REGION CONTAIN THESE WORDS?          exact words, case/punctuation folded
   region 'Erase disk and install Ubuntu'  want [erase, disk]        -> yes

B. IS THE REGION THIS SENTENCE?                  ratio over the whole region
   region 'insall now'                     want 'install now'        0.909
   region 'root partiton will be ext4'      want 'root partition...'  0.963
   region 'Erase disk and install Ubuntu'   want 'erase disk'         0.345  -> NO

C. DOES THE REGION CONTAIN THESE WORDS, GIVEN OCR NOISE?   EDIT DISTANCE per word
   'dsk'   for 'disk'      distance 1     ratio 0.750  <- a ratio REFUSES this
   'ext'   for 'ext4'      distance 1     ratio 0.750  <- a ratio REFUSES this
   'instal' for 'install'  distance 1     ratio 0.857
```

**A RATIO IS THE WRONG UNIT FOR A SHORT WORD.** `disk`→`dsk` is one dropped glyph and
scores 0.750, so `fuzzy: 0.85` refuses it; `yes`→`ys` scores 0.667. A ratio divides by
length, which punishes exactly the words a pane uses for buttons and fields. **Edit
distance does not**: one glyph is one edit whether the word is three characters or
twelve, and `disk`→`uninstall` is seven edits, so the separation survives.

So `fuzzy:` takes an **edit distance**, and it is per word inside `contains:`:

```yaml
text:
  - contains: [erase, disk]                    # A: exact words, any order
  - contains: [ext4], fuzzy: 1                 # C: each word within ONE edit
  - like: "install now"                        # B: the whole region ~ this sentence
    ratio: 0.85                                #    (the only place a ratio is right)
  - contains_any: [[manual], [custom]]
  - not_contains: [installing, please wait]
  - matches: 'copying \d+ of \d+ files'
```

**`fuzzy:` IS AN EDIT DISTANCE, DEFAULT 0, AND THE VALIDATOR CAPS IT.** A distance of 1
accepts `dsk`, `ext`, `instal`, `partiton`, `Wpgrade` and `insall` — every single-glyph
misread in this repository's logs. It refuses `uninstall` for `install` (distance 2) and
`disk` for `uninstall` (distance 7). The cap is **2**, because at 3 `install` and
`uninstall` start to be one edit apart from other real words.

`like:`/`ratio:` IS THE ONLY RATIO, and it is right only for a whole region that is one
sentence. It exists because some panes say a phrase, not a set of words:
`root partition will be ext4`. A ratio over THAT is reliable, because the region is long
and the alternatives are not near it in edit space. **It is not a page test** — a
full-frame ratio cannot tell `install` from `uninstall` (0.778) or `welcome` from OCR's
`Wesn Home` (0.556), which is why the key is a CROP criterion in a vocabulary where
`ocr:` reads one region.

**AND THE CASE YOU ASKED ABOUT IS `contains:`, NOT `like:`.** `Erase disk and install
Ubuntu` contains both words, so `contains: [erase, disk]` passes and `like: "erase disk"`
fails at 0.345. That is correct: the region is *not* the sentence "erase disk", it
*contains* those words. Two criteria, two questions, and the file picks which it is
asking.

### 2. `colour:` — measurements, each over its own rect, all on one frame

```yaml
colour:
  - rect: bottom 4.5%
    mean:     {between: [20, 60]}     # brightness 0..255
    spread:   {less_than: 42}         # mean absolute deviation - "is it flat"
    sat:      {greater_than: 0.05}    # HSV saturation 0..1
    flat_rows:{at_least: 3}           # rows that are one colour across the width
    contrast: {greater_than: 40}      # max - min inside the rect
    min:      {at_least: 5}
    max:      {at_most: 250}
    vs: {rect: middle, mean: {greater_than: 40}}   # a DIFFERENCE from another rect
```

Seven numbers per rect: `mean`, `spread`, `sat`, `flat_rows`, `contrast`, `min`,
`max`. Every one is already computed by `internal/screen.Measure` — this is the
measurement half of `IsLinuxDesktop`, `IsCachyOSDesktop` and `IsCinnamonDesktop`,
which is 270 lines of Go that become a rule a file can write.

`vs:` is the only composition: it compares this rect against another, which is
the panel rule's `bottom.mean - body.mean > 40` and the reason one rect alone
cannot answer "is it a panel".

### 3. `any_of:` / `unless:` — the two combinators that earn their place

```yaml
any_of:                       # one of these groups passes
  - colour: [{rect: top 4.5%,    mean: {greater_than: 100}}]
  - colour: [{rect: bottom 4.5%, flat_rows: {at_least: 3}}]
unless: [welcome, setup]      # these words must be absent (a text criterion)
```

`any_of:` exists because the measured panel rule is genuinely a disjunction of
four shapes (top edge bright, bottom edge bright, top edge flat, bottom edge
flat). `all:` is the default for every list, so `any_of:` is the only other form
needed. Three levels of nesting is enough for every rule in this repository, and
the validator refuses a fourth.

## What this replaces

| today | steps | after | steps |
|---|---|---|---|
| `ocr:` a region, `check:` it | 2 | `check: {text: ...}` | 1 |
| `ocr:` then 3 `check:` questions of one reading | 4 | `check: {text: [3 criteria]}` | 1 |
| `desktop: linux/cinnamon` + 80 lines Go | 1 + rebuild | `check: {colour: [...]}` | 1, no rebuild |
| `expect_any:` beside a click | 2 | `check:` then `until:` | 2 (unchanged) |

## What is deliberately NOT in it

**No `click:`.** A step that captures, decides AND clicks cannot say which of
the three failed, and the repository has already paid for that lesson:
`until:` inside a click step reported "did not advance" when the click had
missed. So `check:` reads and decides; a click is the `do:` beside it, gated by
`when:`.

**No `settle:`/`within:`.** Timing belongs to the step, not the criterion.
A `check:` inside a `loop:` is retried by the loop's own `max`/`sleep`.

**No arithmetic beyond `vs:`.** Two rects, one subtraction. `install`/`uninstall`
shows why a general expression language is the wrong direction: it would let a
file express a comparison nobody can check by reading it.

## The written-out example, which is the whole point

The rule that needed 80 lines of Go and an image build:

```yaml
- check:
    name: desktop_up
    colour:
      - rect: middle
        mean: {between: [10, 250]}
    any_of:
      - colour: [{rect: top 4.5%,    spread: {less_than: 42}, vs: {rect: middle, mean: {greater_than: 40}}, sat: {greater_than: 0.05}}]
      - colour: [{rect: bottom 4.5%, spread: {less_than: 42}, vs: {rect: middle, mean: {greater_than: 40}}, sat: {greater_than: 0.05}}]
      - colour: [{rect: top 4.5%,    flat_rows: {at_least: 3}}]
      - colour: [{rect: bottom 4.5%, flat_rows: {at_least: 3}}]
```

and the numbers it decides on, measured from the frames already in
`internal/screen/testdata`:

```
mint   bottom spread=8.5  flat_rows=11/49  -> true    (its panel IS its wallpaper)
zorin  top    spread=25.7 mean=202.7 vs body 140.9 -> true
mx     bottom spread=40.7 flat_rows=0/49   -> false   (a BIOS menu)
```

## How this stays honest

1. **One frame per step** is asserted by a test: a `check:` that calls
   `Driver.Frame()` more than once fails it. That is the property the whole
   design rests on.
2. **`fuzzy:`'s default is 0.85 for `like:`, and a test pins the measured
   pairs above**: `insall now`→`install now` passes at 0.90, `Erase disk and
   install Ubuntu`→`erase disk` fails at 0.345, and — the one that matters —
   `uninstall`→`install` at 0.778 fails, so a region that says the opposite
   thing cannot pass as the thing. A change that makes that last pair pass
   fails this test.
3. **The three golden frames are the arithmetic's fixtures**, so a threshold can
   move without a machine: `measure_test.go` already evaluates the panel rule
   against mint, zorin and a BIOS menu and reaches the verdicts the Go reaches.
4. **No criterion may name a pixel coordinate.** Only fractions. A rect that is
   a fraction cannot be measured at the wrong size, which is the failure the
   files record as `a coordinate measured at 1920x1080 but the framebuffer is
   640x480`.
