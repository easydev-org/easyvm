# The `.walk` format

A walkthrough written as prose-shaped lines instead of YAML, with a **one-to-one**
correspondence to the YAML that already runs. It is not a new engine: `easyvm` reads
a `.walk`, turns it into exactly the document `Load` would have parsed, and runs it.

The reason it exists: a YAML walkthrough is 344 lines of which the reader must know
which keys nest under which, and a fence of `-` and `:` hides the one thing that
matters — the order of actions. A `.walk` file is the same instructions written the
way a person would say them.

---

## The shape

```
walkthrough "kubuntu install"
  resolution 1920x1080

  needs kubuntu.iso
  make a disk "disk.qcow2" of 128 GiB
  boot the installer from "kubuntu.iso" (linux), waiting up to 1800s

  when the screen says "welcome" "partitions" "next":
    read the screen as "parts"
    click "next" at 1777 1059
    wait 8s
```

Three indentation levels mean three things, and nothing else changes meaning by
indenting:

| indent | what it is |
|---|---|
| 0 | the walkthrough's header |
| 2 | a step |
| 4+ | part of the step above it |

A line that ends in `:` opens a block. A line that does not is a complete instruction.

---

## Header

```
walkthrough "mint - the ubiquity walk"
  resolution 1920x1080
  user vmuser
  password admin
  recording "mint-install.mp4"
```

Equivalent YAML:

```yaml
name: mint - the ubiquity walk
resolution: [1920, 1080]
vars: {user: {default: vmuser}, password: {default: admin}}
steps:
  - name: start recording
    record: mint-install.mp4
```

`user`, `password` and `recording` are shorthands for the three header facts every
file has. `recording` is also emitted as the first step, because that is what it is.

---

## Steps: one line each

The verb is the first word, and it is the same verb as the YAML key.

```
  click "next" at 1777 1059          |  - name: click next
                                     |    click: [1777, 1059]
  type the password "admin"          |  - name: type the password
                                     |    type: '{{ password }}'
  press Return                       |  - name: press Return
                                     |    key: ret
  wait 8s                            |  - name: wait 8 seconds
                                     |    settle: 8
```

**A step's name is the sentence itself** unless the file says otherwise, which is why
there is no `name` on most lines. When the log needs a different name:

```
  click "next" at 1777 1059, named "next from welcome"
```

---

## Blocks

Four words open a block, and each maps to a key that is a mapping or a list.

### `when` / `unless` — a gate

```
  when the screen says "welcome" "partitions":
    click "next" at 1777 1059
```

```yaml
- name: when the screen says "welcome" "partitions"
  when: [welcome, partitions]
  click: [1777, 1059]
```

`unless` is `when_not:` — one gate with a negation, as the vocabulary says.

### `read` — a measurement

A `read` with no block is a TEXT reading; a `read` with a `measuring` block asks for NUMBERS.

```
  read the screen as "parts"                     |  - name: read the screen as "parts"
                                                 |    see: {name: parts, of: [text], rect: [0,0,1920,1080]}
  read the bottom 4.5% of the screen as "panel"  |  - name: read the bottom 4.5% ...
    measuring mean spread sat flat_rows          |    see: {name: panel, rect: "bottom 4.5%", of: [mean, spread, sat, flat_rows]}
```

### `decide` — a boolean

```
  decide "desktop up" is any of:
    panel.flat_rows is at least 3
    panel.spread is less than 42
```

```yaml
- name: decide desktop up
  want:
    name: desktop up
    any:
      - panel.flat_rows: {at_least: 3}
      - panel.spread: {less_than: 42}
```

### `repeat` — a loop

```
  repeat up to 30 times:
    click "next" at 420 300
    wait 10s
    read the screen as "page"
    decide "arrived" is:
      page contains "installation"
    until "arrived"
```

```yaml
- name: repeat up to 30 times
  loop:
    max: 30
    do: [{click: [420, 300]}, {settle: 10}]
    ocr: {name: page, ...}
    check: {name: arrived, from: page, mode: says, words: [installation]}
    until: {fact: arrived}
```

---

## The rules that make it exact

1. **Every line becomes exactly one key.** There is no line that becomes two, and no
   key that has no line. `easyvm walk --to-yaml` prints the YAML a `.walk` produces,
   and `easyvm walk --diff` proves it is byte-identical to a hand-written file.
2. **A sentence is a name.** The step name is the line itself, so a log line and a
   source line are the same words. `named "..."` overrides it.
3. **Quoted strings are values; bare words are enum values.** `click "next"` names a
   button for the log; `(linux)` selects an OS; `1777 1059` are numbers.
4. **Units are required where the YAML has them implicit.** `wait 8s`, not `wait 8`,
   because the YAML's `settle:` is seconds and a reader should not have to know that.
5. **No line is required to be indented by exactly two.** Any consistent indentation
   works, because the mapping is by nesting depth, not by column.

---

## Why this is not a second engine

The conversion is a single pass over lines into the same `map[string]any` the YAML
decoder produces. Everything after that — validation, `see:` measurement, the
dispatcher, the recordings — is untouched, so:

* a `.walk` and its `.yaml` behave identically, and a test asserts it by converting
  every real YAML file to `.walk` and back;
* a key added to the vocabulary works in both formats with no change to the
  converter, because the converter copies the verb through;
* a `.walk` file cannot express something the YAML cannot, which is the property that
  stops the two drifting.
