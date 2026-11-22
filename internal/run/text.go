package run

import (
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/easydev-org/easyvm/internal/screen"
	"github.com/easydev-org/easyvm/internal/walk"
)

// READING THE SCREEN IS THE ONE THING THAT NEEDS OCR, and it is why tesseract is in the image.
//
// THE CROP MATTERS MORE THAN THE RECOGNITION. macOS's menu bar is twelve-pixel text; read whole, a
// 1920x1080 desktop gives tesseract a page it decides has no text at all and answers
// "Empty page!!" - which is run 42383829203's frame, a picture OF a desktop that a whole-frame read
// said nothing about. `region:` and `scale:` in a step are what make the difference: a 700x34 strip
// enlarged three times is what tesseract needs.
//
// WORDS ARE COMPARED WITH PUNCTUATION STRIPPED. A matcher for `[remote, login, on]` would otherwise
// fail on "Remote Login: On", and the Python implementation's own note records that variant costing
// a stage.

// tesseract reads text from an image file.
//
// IT IS A TOOL, NOT A LIBRARY, and that is deliberate: tesseract's language data is ~15 MB of trained
// models that a Go binding would have to ship or shell out to anyway. The image's own package is
// what `--list-langs` reports, so a missing language pack is visible in the build.
func tesseract(path, lang string) ([]string, error) {
	bin, err := exec.LookPath("tesseract")
	if err != nil {
		return nil, fmt.Errorf("tesseract is not installed: `expect:` and `until:` read text off "+
			"the screen, and there is no other way to do it. Install tesseract-ocr and %s", lang)
	}
	if lang == "" {
		lang = "eng"
	}
	cmd := exec.Command(bin, path, "stdout", "-l", lang, "--psm", "6")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("tesseract: %v (%s)", err, firstLineOf(string(out)))
	}
	var words []string
	for _, w := range strings.Fields(string(out)) {
		words = append(words, w)
	}
	return words, nil
}

// normalise strips everything that is not a letter or a digit, and lowercases.
//
// A MATCHER MUST NOT DEPEND ON PUNCTUATION. "Remote Login: On" and "remote login on" are the same
// claim, and the Python implementation lost a stage to the difference - with the extra trap that a
// phrase containing the word `on` was written unquoted in YAML, so YAML made it a boolean and the
// crash happened only when the switch was ON.
func normalise(w string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(w) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Says reports whether every one of `want` appears in `words`.
func says(words []string, want []string) bool {
	have := make(map[string]bool, len(words))
	for _, w := range words {
		have[normalise(w)] = true
	}
	for _, w := range want {
		if !have[normalise(w)] {
			return false
		}
	}
	return true
}

// readWords reads a screen.Rect with OCR, which is what lets a `see:` step produce WORDS as well as
// numbers. The rect is a FRACTION, so it is converted to pixels against the frame that arrived - the
// same protection `region:` gets from `region_at:`, without the key.
// readWords OCRs a rect at a chosen ENLARGEMENT.
//
// `scale:` IS NOT TUNING AND IT IS NOT OPTIONAL FOR SMALL TEXT. MEASURED on macOS's 640x480 recovery
// frame, whose `Uninitialized` row is drawn at about 12 px: at scale 6 tesseract read four rows, at
// scale 2 it read two. A reading that finds nothing is indistinguishable from a pane that is not
// there, so the enlargement belongs to the reading rather than to a default.
func (e *Executor) readWords(img image.Image, r screen.Rect, scale int) ([]string, error) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	// A PIXEL RECT IS SCALED AGAINST THE SCREEN IT WAS WRITTEN FOR - `rect_at:`, carried in `Rect` as
	// `AtW`/`AtH` - and NOT against the frame that happened to arrive.
	//
	// THE DIFFERENCE IS THE WHOLE BUG: a macos recovery frame is 640x480 and its rects were measured at
	// 1920x1080. Dividing by the frame reads `1400` as a coordinate in a 640-wide image and asks for a
	// crop outside it:
	//
	//	crop [760, 200, 1400, 560] is not inside a 640x480 frame
	//
	// The frame is the fallback ONLY when the file named no size, which is the one case where it is
	// the best answer available.
	from, to, xf, xt := r.From, r.To, r.XFrom, r.XTo
	if r.Pixels {
		aw, ah := float64(r.AtW), float64(r.AtH)
		if aw <= 0 || ah <= 0 {
			aw, ah = float64(w), float64(h)
		}
		from, to = r.From/ah, r.To/ah
		xf, xt = r.XFrom/aw, r.XTo/aw
	}
	region := [4]int{
		int(float64(w) * xf), int(float64(h) * from),
		int(float64(w) * xt), int(float64(h) * to),
	}
	if scale <= 0 {
		scale = 3
	}
	cropped, err := screen.Crop(img, region, scale)
	if err != nil {
		return nil, err
	}
	return e.readCrop(cropped, "see")
}

// readScreen captures a frame and reads it, cropping when the step says so.
//
// THE CROP IS DONE IN GO RATHER THAN WITH AN EXTERNAL TOOL. `lib/assistant.py` used Pillow for this,
// which is a Python dependency in an image that no longer has Python - and the operation is a
// rectangle of pixels copied into a smaller image.
func (e *Executor) readScreen(s walk.Step, label string) ([]string, error) {
	f, err := e.Driver.Frame()
	if err != nil {
		return nil, err
	}
	img, err := screen.Load(f.Path())
	if err != nil {
		return nil, err
	}

	crop := img
	if region, ok := rectOf(s, "rect"); ok {
		// `region_at:` IS REQUIRED WITH `region:`, AND ITS ABSENCE IS AN ERROR RATHER THAN A DEFAULT.
		//
		// A RECTANGLE IS PIXELS ON ONE PARTICULAR SCREEN. Without the size it was measured at there
		// is no way to know whether the crop lands on the pane or on the wallpaper when the guest
		// draws at another size - and a guest DOES: the UEFI shell draws at 1280x800 and the desktop
		// at 1920x1080, macOS recovery stays at 640x480, mx's live session picks 1024x768.
		//
		// MEASURED, TWICE, AND BOTH FAILURES LOOKED LIKE SOMETHING ELSE:
		//
		//	nixos:   `region_at` is [1920, 1080] but this frame is 1280x800
		//	mx:      the framebuffer is 1024x768 but this region was measured at 1920x1080
		//
		// The first version FAILED on a mismatch, which turned "the pane is not up yet" into "the
		// file is broken"; the second scaled the rectangle silently. Neither is possible to reason
		// about when the number that says what the measurement was is optional.
		if _, ok := pairAt(s); !ok {
			return nil, fail(s.Name, "`region:` must say `region_at: [width, height]` - the size "+
				"those pixels were measured at. A rectangle without it cannot be scaled for a guest "+
				"that draws at another size, and this guest does: the UEFI shell is 1280x800, macOS "+
				"recovery is 640x480, and mx's live session picks 1024x768")
		}
		// `region_at` EXISTS BECAUSE A CROP MEASURED AT ONE RESOLUTION IS WRONG AT ANOTHER. A step
		// that says [880, 960, 1045, 1000] was measured on a 1920x1080 guest; a guest booted at
		// another size would have the crop on the wrong part of the screen.
		//
		// BUT A MISMATCH IS NOT ALWAYS A MISTAKE IN THE FILE, AND FAILING ON IT WAS WRONG.
		// THE GUEST CHANGES SIZE DURING A BOOT - the UEFI shell draws at 1280x800, the desktop at
		// 1920x1080 - so a poll can land before the mode changes. The Python implementation reports
		// it and reads NOTHING, which for an `until` means "try again next poll", and the caller's
		// timeout decides whether the pane ever arrives at the size the file expects.
		//
		// Measured on run 43063744565's nixos leg, which failed with
		//
		//     wait for the Console result: `region_at` is [1920, 1080] but this frame is 1280x800
		//
		// on a file whose region is right and whose guest had not finished switching modes. A
		// hard failure turned "the pane is not up yet" into "the file is broken".
		// AND A SIZE MISMATCH SCALES THE RECTANGLE RATHER THAN ABANDONING THE READ.
		//
		// MEASURED ON mx-install, WHICH GOT FURTHER THAN ANY install LEG BEFORE IT - the desktop
		// gate passed in five seconds once the Desktop ISO replaced the Server one - and then failed
		// thirty times in a row with
		//
		//	wait for the terms page: the framebuffer is 1024x768 but this region was measured at
		//	1920x1080; not reading it
		//
		// THE GUEST NEVER SWITCHED MODES: `-device VGA,xres=1920,yres=1080` tells the ADAPTER what
		// it can do, and the live session's X server chose 1024x768 because nothing told it to
		// prefer the larger mode. `ubuntu-vm` proves the parameter itself is fine - a disk with an
		// installed system reads at 1920x1080 with the same qemu command - so this is a property of
		// a LIVE session, not of this build's machine.
		//
		// THE REGION IS A PLACE ON THE PAGE, NOT A PLACE ON A SCREEN: [690, 225, 1560, 265] on
		// 1920x1080 is the right-hand content pane, and the same pane at 1024x768 is the same
		// FRACTION of the frame. Scaling preserves what the measurement was about and makes it work
		// at whatever size the guest picked.
		//
		// AND THE LOG SAYS SO, because a silently scaled rectangle is a reading taken from somewhere
		// other than where the file said, and that has to be visible when a step fails.
		if at, ok := pairAt(s); ok {
			w, h := f.Size()
			if at[0] != w || at[1] != h {
				scaled := [4]int{
					region[0] * w / at[0], region[1] * h / at[1],
					region[2] * w / at[0], region[3] * h / at[1],
				}
				e.Driver.Log(fmt.Sprintf("  %s: the framebuffer is %dx%d and this region was "+
					"measured at %dx%d; reading the same place scaled to [%d, %d, %d, %d]",
					s.Name, w, h, at[0], at[1], scaled[0], scaled[1], scaled[2], scaled[3]))
				region = scaled
			}
		}
		scale := 3
		if v, err := s.Float("scale", 3); err == nil && v > 0 {
			scale = int(v)
		}
		cropped, err := screen.Crop(img, region, scale)
		if err != nil {
			return nil, err
		}
		crop = cropped
	}

	return e.readCrop(crop, label)
}

// readCrop saves one crop and reads it, which is the ONLY place tesseract is invoked.
//
// IT IS SEPARATE FROM `readScreen` SO THAT "WHICH RECTANGLE" AND "READ IT" ARE TWO DECISIONS. The
// conditions (`until:`, `until_any:` and the rest) reach this through `readScreen`, which takes its
// rectangle from the step's own `region:`; an `ocr:` step reaches it with a rectangle it validated
// as required. One tesseract call, one enlargement, one saved ppm - so a gate and a loop cannot
// disagree about what the screen said.
func (e *Executor) readCrop(crop image.Image, label string) ([]string, error) {
	path := filepath.Join(e.Work, "read-"+sanitise(label)+".ppm")
	if err := os.MkdirAll(e.Work, 0o755); err != nil {
		return nil, err
	}
	if err := screen.SavePPM(crop, path); err != nil {
		return nil, err
	}
	words, err := tesseract(path, "eng")
	if err != nil {
		return nil, err
	}
	e.Driver.Log(fmt.Sprintf("  %s: %d word(s): %s", label, len(words),
		truncateWords(words, 12)))
	return words, nil
}

// doUntil is ONE measurement of a wait, not a loop.
//
// IT USED TO BE A LOOP AND THAT WAS THE HIDDEN RETRY THIS VOCABULARY IS BEING CLEANED OF. `timeout:`
// and `poll:` were a `for` loop in Go, so 109 steps in this repository each carried an invisible
// retry: a reader could see `until_any:` and `timeout: 1800` and had no way to tell that the executor
// would read the screen up to 180 times, or that a failed read was swallowed, or that `keep_alive:`
// clicked the guest between attempts.
//
// THE LOOP IS `loop:` NOW, AND IT SAYS SO IN THE FILE:
//
//   - name: the recovery menu is up
//     loop:
//     max: 180
//     do: [{wait: 10}]
//     ocr: {name: recovery, region: [...], region_at: [...], wait: 0}
//     check: {name: up, from: recovery, says_any: [[restore, time, machine], [safari]]}
//     until: {fact: up}
//
// A BARE `until:` REMAINS FOR THE CASE THAT NEEDS NO RETRY - a screen that must ALREADY show the
// words, checked once. `timeout:` and `poll:` are refused with it, because accepting them would
// restore the loop this replaced.
func (e *Executor) doUntil(s walk.Step) error {
	// `from: console` IS THE SERIAL LOG, AND IT IS A PARAMETER RATHER THAN A SECOND KEY.
	//
	// `until_console:` WAS ITS OWN STEP with its own do* function, and the two differed in ONE thing:
	// where the text was read. The console wait polls a file for `timeout:` seconds and matches a
	// substring; the screen wait reads once and matches words. That made `until:` a dead key (0 uses)
	// beside a live twin (4), and two functions to keep in step.
	//
	// THE CONSOLE PATH KEEPS ITS OWN POLLING because a serial log is a FILE that grows: there is no
	// "read it once" that means anything, so `timeout:`, `poll:` and `blind_after:` are legal here
	// and refused for the screen.
	if s.Get("from") == "console" {
		return e.untilConsole(s)
	}
	if s.Has("timeout") || s.Has("poll") {
		return fail(s.Name, "`until:` is ONE measurement and takes no `timeout:` or `poll:`. A "+
			"retry is a `loop:` - with `do:`, `ocr:`, `check:` and `until: {fact: ...}` - so the "+
			"file says how many times it will look and what it waits between. For a serial log, "+
			"which grows, say `from: console`")
	}
	if s.Has("keep_alive") {
		return fail(s.Name, "`keep_alive:` belonged to the retrying `until:`. A loop that has to "+
			"wake a blanked screen says so in its `do:` list, where the click is visible")
	}

	want := wordsOf(s, "until")
	var alternatives [][]string
	if len(want) > 0 {
		alternatives = append(alternatives, want)
	}
	for _, set := range wordSetsOf(s, "until_any") {
		alternatives = append(alternatives, set)
	}
	if len(alternatives) == 0 {
		return fail(s.Name, "`until:` or `until_any:` must name the words to check for")
	}

	words, err := e.readScreen(s, s.Name)
	if err != nil {
		return fail(s.Name, "%v", err)
	}
	okNow := false
	var hit string
	for _, set := range alternatives {
		if says(words, set) {
			okNow, hit = true, strings.Join(set, " ")
			break
		}
	}
	// EVERY CONDITION CAN BIND ITS RESULT, SO EVERY CONDITION PRODUCES A BOOLEAN.
	//
	// `as: <name>` makes the answer a value a later `if:` can ask about, which is what turns a gate
	// from "this step stops the walk" into "this step answers a question". Without it the only way
	// to act on a negative result was an error, and an error ends the walk.
	if name := s.Get("as"); name != "" {
		if e.facts == nil {
			e.facts = make(map[string]bool)
		}
		e.facts[name] = okNow
	}
	if okNow {
		e.Driver.Log(fmt.Sprintf("  saw %q", hit))
		return nil
	}
	return failWithScreen(s.Name, e.Driver, "the screen does not show %s; read: %s",
		describeSets(alternatives), truncateWords(words, 16))
}

// doUnless runs a step only when the screen does NOT already show the words.
//
// IT IS HOW A RETRY IS WRITTEN WITHOUT A CONDITIONAL INSIDE A CLICK. opensuse's installer retry must
// not press the desktop icon while YaST2 is already open, because a second double-click lands on the
// wizard - and a step that cannot say "unless this is already up" has to be a Python function.
// doActionList runs a step's `do:` list in order, under the step's own name.
//
// IT IS THE PANE MODEL'S ACTION LIST AND IT IS ORDINARY: `loop:`'s `do:` and `if:`'s `then:` are the
// same shape, and both already run through this dispatcher. Only the TOP-LEVEL `do:` had no branch,
// so `macos/15/steps/sequoia.yaml`'s eight multi-click panes clicked nothing.
//
// A FAILURE NAMES THE OUTER STEP AND THE INNER ONE, because "the analytics pane failed" is not enough
// when it is the second of two clicks - the reader needs to know which.
func (e *Executor) doActionList(s walk.Step) error {
	raw, ok := s.Spec["do"].([]any)
	if !ok || len(raw) == 0 {
		return fail(s.Name, "`do:` must be a list of steps")
	}
	for i, entry := range raw {
		m, ok := entry.(map[string]any)
		if !ok {
			return fail(s.Name, "`do:` entry %d is not a step", i+1)
		}
		inner := walk.Step{Index: s.Index, Name: s.Name, Spec: m}
		if _, hasName := m["name"]; !hasName {
			inner.Name = fmt.Sprintf("%s [%d]", s.Name, i+1)
		}
		if err := e.runOne(inner); err != nil {
			return fmt.Errorf("%s, step %d of %d: %w", s.Name, i+1, len(raw), err)
		}
	}
	return nil
}

// doWhen runs a step only if the screen shows the words it names.
//
// `when:` IS `unless:`'S MIRROR, AND IT WAS DECLARED WITHOUT AN IMPLEMENTATION - which made it the
// worst kind of key. MEASURED: 16 uses in `macos/15/steps/sequoia.yaml`, the Setup Assistant walk,
// and `runOne` has no branch for it, so every one of those steps reached the switch's last arm and
// returned `no implementation for this step's keys`. The macOS setup walk could not run.
//
// IT IS THE SECOND STEP MODEL ("panes") AND THAT IS WHY IT IS BEING FOLDED IN RATHER THAN REVIVED ON
// ITS OWN. A pane file said `when:` (the words that mean this pane is up) and `do:` (the actions),
// which is the same three parts `loop:` and `check:` already spell out. `when:` keeps its meaning and
// gains an implementation here; `do:` is read by the dispatcher as the step's own action; and the
// separate `once:`/`end:`/`stuck_after:` state machine is deleted rather than rebuilt, because a
// pane that is answered twice is a `loop:` whose fact goes false.
//
// THE READING IS ONE CAPTURE, like `unless:`, so the gate and the action that follows it cannot see
// two different screens.
func (e *Executor) doWhen(s walk.Step) error {
	// `when: {none: [[a], [b, c]]}` IS THE NEGATIVE FORM, folded in from `when_not_any:`.
	//
	// One key carries both polarities of one question, so a reader does not have to know that a
	// negative gate has its own name - and the alternatives form (a pane can announce itself two
	// ways) lives in the same place as the positive one.
	if m, ok := s.Spec["when"].(map[string]any); ok {
		if _, has := m["none"]; has {
			return e.doUnless(s)
		}
	}
	want := wordsOf(s, "when")
	if len(want) == 0 {
		return fail(s.Name, "`when:` must name the words that mean this step applies")
	}
	words, err := e.readScreen(s, s.Name+" - when")
	if err != nil {
		return err
	}
	if !says(words, want) {
		e.Driver.Log(fmt.Sprintf("  skipped: the screen does not say %q", strings.Join(want, " ")))
		e.skipped = true
		return nil
	}
	e.Driver.Log(fmt.Sprintf("  applies: the screen says %q", strings.Join(want, " ")))
	// THE ACTION IS THE REST OF THE STEP. `when:` is a GATE, not an action, so the dispatcher is
	// re-entered with the gate removed - the same shape `unless:` uses, and for the same reason: a
	// step says one thing and a gate is not that thing.
	return e.dispatchRest(s)
}

func (e *Executor) doUnless(s walk.Step) error {
	// `unless_any:` IS THE ALTERNATIVES FORM, and it exists for the same reason `expect_any:` does: a
	// pane can announce itself in more than one way. Measured on opensuse-tumbleweed's installer, whose
	// welcome screen reads `installation` and `upgrade` correctly while `welcome` comes back from
	// tesseract as `Wesn Home` - so the gate has to accept whichever of the three it can see.
	//
	// `unless:` REMAINS ONE WORD-SET WITH AND MEANING, because a gate that required ANY of two words
	// and a gate that required BOTH were spelled the same way and that is a trap. Two keys, two
	// meanings, no guessing.
	// `when: {none: ...}` IS THE FOLDED NAME AND `when_not_any:` / `when_not:` ARE WHAT FILES WRITE
	// TODAY. All three arrive here, so the negative question has one implementation.
	var alternatives [][]string
	if m, ok := s.Spec["when"].(map[string]any); ok {
		if none, has := m["none"]; has {
			alternatives = setsOfAny(none)
		}
	}
	if len(alternatives) == 0 {
		alternatives = wordSetsOf(s, "when_not_any")
	}
	if len(alternatives) == 0 {
		if want := wordsOf(s, "when_not"); len(want) > 0 {
			alternatives = [][]string{want}
		}
	}
	if len(alternatives) == 0 {
		return fail(s.Name, "`when_not:` or `when_not_any:` must name words")
	}
	words, err := e.readScreen(s, s.Name+" - unless")
	if err != nil {
		// A SCREEN THAT CANNOT BE READ IS NOT "THE WORDS ARE ABSENT". Guessing would run the step on
		// a screen nobody looked at, which is the failure the whole check exists to prevent.
		return fail(s.Name, "could not read the screen to decide `unless`: %v", err)
	}
	already, hit := false, ""
	for _, want := range alternatives {
		if says(words, want) {
			already, hit = true, strings.Join(want, " ")
			break
		}
	}
	if name := s.Get("as"); name != "" {
		if e.facts == nil {
			e.facts = make(map[string]bool)
		}
		// THE NAME IS THE QUESTION, NOT THE ANSWER: `unless: [done] as: delivered` means
		// `delivered` is true when the words were NOT there. A boolean that changes meaning with the
		// key that made it is a trap, so it is documented here and in the vocabulary.
		e.facts[name] = !already
	}
	if already {
		e.Driver.Log(fmt.Sprintf("  skipped: the screen already shows %q", hit))
		// THE CALLER MUST BE ABLE TO TELL "SKIPPED" FROM "GO AHEAD", AND IT COULD NOT.
		//
		// MEASURED ON THE macOS 26 ssh STAGE, and it cost that stage its whole run. This function
		// reported success for both outcomes, and the dispatcher ran the step's action on either - so
		//
		//	- name: open the remote login pane from the keyboard
		//	  unless: [allow, full, disk, access]
		//	  do: [Cmd+F, Return]
		//
		// logged `skipped: the screen already shows "allow full disk access"` and THEN pressed Cmd+F
		// and Return, which closed the panel it was supposed to leave alone. The next step's reading
		// was 0 words - a blank screen - and the stage failed there.
		//
		// A FLAG DOES NOT STOP THE ACTION. This branch of the merge had one - `e.skipped = true`
		// followed by `return nil` - and the dispatcher runs `dispatchRest` whenever this function
		// returns nil, so the flag changed what the JOURNAL called the step and nothing about what
		// the step DID. That is the defect above, dressed as a fix.
		//
		// THE SENTINEL IS THE ONLY MECHANISM: `dispatchRest` is what runs the rest of the step, so
		// the "do not" answer has to be a value the dispatcher recognises. It is also what the
		// dispatcher uses to mark the step `skipped` in the journal, so the report and the action
		// cannot disagree - see the `when_not` arm.
		return errUnlessAlready
	}
	// THE STEP CONTINUES. `unless` is a gate on the rest of the same step, and the dispatcher ran
	// this first.
	return nil
}

// errUnlessAlready is `unless` saying "the words were there, do not run the action".
//
// IT IS A SENTINEL RATHER THAN A BOOLEAN RETURN because `doUnless` already has an error return and a
// second one would have to be checked at every call site. `errors.Is` is how the dispatcher reads it.
var errUnlessAlready = errors.New("unless: the screen already shows the words")

// --- small helpers ---------------------------------------------------------------------

func wordsOf(s walk.Step, key string) []string {
	v, ok := s.Spec[key]
	if !ok {
		return nil
	}
	switch t := v.(type) {
	case string:
		return strings.Fields(t)
	case []any:
		var out []string
		for _, item := range t {
			out = append(out, fmt.Sprint(item))
		}
		return out
	}
	return nil
}

func wordSetsOf(s walk.Step, key string) [][]string {
	v, ok := s.Spec[key]
	if !ok {
		return nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	var out [][]string
	for _, item := range list {
		switch t := item.(type) {
		case []any:
			var set []string
			for _, w := range t {
				set = append(set, fmt.Sprint(w))
			}
			out = append(out, set)
		case string:
			out = append(out, strings.Fields(t))
		}
	}
	return out
}

func describeSets(sets [][]string) string {
	var parts []string
	for _, set := range sets {
		parts = append(parts, fmt.Sprintf("%q", strings.Join(set, " ")))
	}
	return strings.Join(parts, " or ")
}

func truncateWords(words []string, n int) string {
	if len(words) <= n {
		return strings.Join(words, " ")
	}
	return strings.Join(words[:n], " ") + " ..."
}

func sanitise(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 40 {
		return out[:40]
	}
	return out
}

func firstLineOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 160 {
		s = s[:160] + "..."
	}
	return s
}

// pairAt reads the size a pixel rect was measured at, under `rect_at:` or its old name
// `region_at:`. One region, one meaning: a rectangle is pixels on ONE screen.
func pairAt(s walk.Step) ([2]int, bool) {
	if v, ok := pairOf(s, "rect_at"); ok {
		return v, true
	}
	return pairOf(s, "region_at")
}

func rectOf(s walk.Step, key string) ([4]int, bool) {
	v, ok := s.Spec[key]
	if !ok && key == "rect" {
		v, ok = s.Spec["region"]
	}
	if !ok {
		return [4]int{}, false
	}
	list, ok := v.([]any)
	if !ok || len(list) != 4 {
		return [4]int{}, false
	}
	var out [4]int
	for i := 0; i < 4; i++ {
		n, ok := asInt(list[i])
		if !ok {
			return [4]int{}, false
		}
		out[i] = n
	}
	return out, true
}

func pairOf(s walk.Step, key string) ([2]int, bool) {
	v, ok := s.Spec[key]
	if !ok {
		return [2]int{}, false
	}
	list, ok := v.([]any)
	if !ok || len(list) != 2 {
		return [2]int{}, false
	}
	a, oka := asInt(list[0])
	b, okb := asInt(list[1])
	if !oka || !okb {
		return [2]int{}, false
	}
	return [2]int{a, b}, true
}

// doExpectAny checks that one of several word-sets appeared after the action.
//
// IT IS THE VERDICT HALF OF A STEP, and a step that does not advance is NOT COUNTED AS ANSWERED: a
// run that says "13 of 14 answered" must not include the one that clicked and then failed its own
// check, or the transcript disagrees with the verdict.
func (e *Executor) doExpectAny(s walk.Step) error {
	sets := wordSetsOf(s, "expect_any")
	if len(sets) == 0 {
		if single := wordsOf(s, "expect"); len(single) > 0 {
			sets = [][]string{single}
		}
	}
	if len(sets) == 0 {
		return fail(s.Name, "`expect_any:` must name the word-sets that would pass")
	}
	words, err := e.readScreen(s, s.Name+" - after")
	if err != nil {
		return fail(s.Name, "%v", err)
	}
	for _, set := range sets {
		if says(words, set) {
			e.Driver.Log(fmt.Sprintf("  advanced: saw %q", strings.Join(set, " ")))
			return nil
		}
	}
	if s.Has("optional") {
		// AN OPTIONAL CHECK WHOSE NEGATIVE RESULT IS INFORMATION. It says so and continues.
		e.Driver.Log(fmt.Sprintf("  expected %s, did not see it, and the step is optional",
			describeSets(sets)))
		return nil
	}
	return failWithScreen(s.Name, e.Driver, "did not advance: wanted %s, read %q",
		describeSets(sets), truncateWords(words, 16))
}

// doAtColor clicks the centre of the largest blob of one colour.
//
// IT REPLACES `doAtWindow`, WHOSE PREMISE WAS THAT A CONTROL'S OFFSET FROM ITS WINDOW IS CONSTANT - and
// that is true and useless, because finding the window's edge was the unreliable part. Four attempts at
// Disk Utility's Erase button were four attempts at the scan:
//
//	592,495   the reference implementation's gbi numbers, unrelated to this pane
//	810,376   a bounding box that included the word `vtbd0` to the right of the box
//	807,367   a threshold that found only the box's top edge
//	1289,15   the reference implementation's own macOS numbers, which assume ITS window position
//
// A COLOUR IS A FACT THE COMPOSITOR DRAWS. macOS paints the green zoom control rgb(40,200,64) on every
// run, at every window height, in every Space, and the same is true of the traffic lights and of any
// control an application draws in a fixed colour.
func (e *Executor) doAtColor(s walk.Step) error {
	r, g, b, err := colorTriple(s, "at_color")
	if err != nil {
		return err
	}
	tolerance, _ := s.Float("color_tolerance", 45)
	region := [4]int{0, 0, 0, 0}
	if raw, ok := s.Spec["color_region"].([]any); ok && len(raw) == 4 {
		for i := range region {
			if v, ok := toFloat(raw[i]); ok {
				region[i] = int(v)
			}
		}
	}

	f, ferr := e.Driver.Frame()
	if ferr != nil {
		return fail(s.Name, "%v", ferr)
	}
	img, ierr := screen.Load(f.Path())
	if ierr != nil {
		return fail(s.Name, "%v", ierr)
	}
	x, y, ok := screen.FindColor(img, [3]uint8{uint8(r), uint8(g), uint8(b)}, int(tolerance), region)
	if !ok {
		return failWithScreen(s.Name, e.Driver,
			"no pixel within %.0f of rgb(%d,%d,%d) on this screen, so the control this step names "+
				"is not drawn - which means the pane it belongs to is not the one on screen",
			tolerance, r, g, b)
	}
	if err := e.Driver.Click(x, y); err != nil {
		return fail(s.Name, "click (%d, %d): %v", x, y, err)
	}
	e.Driver.Log(fmt.Sprintf("  clicked (%d, %d), the centre of the rgb(%d,%d,%d) blob", x, y, r, g, b))
	return e.settle(s)
}

// --- a control found by its fill -------------------------------------------------------

// doAtButton clicks the centre of the dominant coloured fill inside a region.
//
// THIS IS THE ONE LOOKUP THIS REPOSITORY ALLOWS, AND IT IS THE ONE ITS OWN RULE NAMES AS THE
// EXCEPTION. Pop!_OS' installer footer button moves with every pane - measured at y=790, 810, 798,
// 810 for four consecutive panes - because the card SHRINKS AND GROWS as the pane changes. A single
// constant is wrong on most of them, and the failure is worse than silent: a press ten pixels below
// the button lands on wallpaper, nothing advances, and the NEXT step's clicks were aimed at a pane
// that never appeared. Four runs were lost that way (42525650794, 42526614298, 42529279468,
// 42531166731), each ending with the disk at 0 MiB.
//
// IT IS SAFE HERE FOR THE REASON THE CAPACITY CHECK IN setup.yaml IS SAFE: the instruction is not
// "go to this position", it is "press the button in this pane's footer" - and the footer button is
// the only rounded fill of its kind in that box. Prose cannot be talked into being a button; a
// coloured fill in a measured strip can.
//
// THE LABEL IS NOT READ. tesseract reads these 14-px labels unreliably - `Select` came back as
// nothing at all on two panes - while the fill is 84x34 px of one colour.
// doAtBottom clicks at an x, a fixed number of pixels above the FRAME's bottom edge.
//
// IT WAS DECLARED AND NEVER DISPATCHED, which made it the worst kind of key: eight nixos steps write
// `at_bottom: [1327, 15]` and the executor answered `no implementation for this step's keys`. A file
// that loads, validates and then fails at run time on a key the vocabulary advertises.
//
// WHY A KEY AT ALL RATHER THAN A PLAIN COORDINATE: a wizard's `Next` sits a fixed distance above the
// BOTTOM OF THE WINDOW, and the window's height is what changes between pages - Calamares' footer
// moves from 751 to 872 depending on how tall the content above it is. A plain y would have to be
// re-measured per page; this does not. MEASURED on nixos: every one of the eight is `[1327, 15]`,
// against a footer at y=1065 in a 1080 frame - so 15 from the bottom is 1065.
//
// THE FRAME, NOT A SCAN FOR THE WINDOW. `at_window` tried to find the window's edge and its own
// comment records four failed attempts at the SCAN rather than at the button. This is the case where
// there is nothing to scan: the wizard's footer is at the screen's bottom.
func (e *Executor) doAtBottom(s walk.Step) error {
	pair, ok := s.Spec["at_bottom"].([]any)
	if !ok || len(pair) != 2 {
		return fail(s.Name, "`at_bottom:` must be [x, dy] - an x, and how many pixels above the "+
			"bottom edge to click")
	}
	x, okX := toFloat(pair[0])
	dy, okY := toFloat(pair[1])
	if !okX || !okY {
		return fail(s.Name, "`at_bottom:` must be two numbers")
	}
	f, err := e.Driver.Frame()
	if err != nil {
		return fail(s.Name, "reading the frame to find its bottom edge: %v", err)
	}
	_, h := f.Size()
	y := h - int(dy)
	if y < 0 {
		y = 0
	}
	e.Driver.Log(fmt.Sprintf("  clicking (%d, %d): %g above the bottom of a %d-pixel frame",
		int(x), y, dy, h))
	if err := e.Driver.Click(int(x), y); err != nil {
		return fail(s.Name, "%v", err)
	}
	return e.settle(s)
}

func (e *Executor) doAtButton(s walk.Step) error {
	r, ok := rectOf(s, "at_button")
	if !ok {
		return fail(s.Name, "`at_button:` must be a region [x0, y0, x1, y1] to search for the "+
			"control's fill")
	}
	f, err := e.Driver.Frame()
	if err != nil {
		return fail(s.Name, "%v", err)
	}
	img, err := screen.Load(f.Path())
	if err != nil {
		return fail(s.Name, "%v", err)
	}
	cx, cy, left, top, right, bottom, ok := screen.FindFill(img,
		screen.Region{X0: r[0], Y0: r[1], X1: r[2], Y1: r[3]})
	if !ok {
		// `at_fallback:` IS A MEASURED COORDINATE FOR WHEN THE COLOUR IS NOT THERE.
		//
		// GhostBSD's installer icon is found by its teal disc, and that driver falls back to the
		// measured row when the colour does not match - so it behaves exactly as the fixed constant
		// did rather than pressing nothing at all. A control that CAN be missing is different from
		// a footer that cannot: a disabled footer means a step above failed, while an icon can be
		// hidden by a wallpaper change and still be in the same place.
		if pair, ok := s.Spec["at_fallback"].([]any); ok && len(pair) == 2 {
			fx, okX := toFloat(pair[0])
			fy, okY := toFloat(pair[1])
			if okX && okY {
				e.Driver.Log(fmt.Sprintf("  no fill in [%d,%d %d,%d]; using the measured "+
					"fallback (%d,%d)", r[0], r[1], r[2], r[3], int(fx), int(fy)))
				if v, isDouble := s.Spec["double"].(bool); isDouble && v {
					if err := e.Driver.DoubleClick(int(fx), int(fy)); err != nil {
						return fail(s.Name, "%v", err)
					}
				} else if err := e.Driver.Click(int(fx), int(fy)); err != nil {
					return fail(s.Name, "%v", err)
				}
				return e.settle(s)
			}
		}
		// NO CONTROL AND NO FALLBACK IS A STOP, NOT A GUESS. A disabled footer cannot be pressed,
		// and the step before this one must select whatever enables it - so returning success here
		// would hide a pane that never became pressable.
		return failWithScreen(s.Name, e.Driver,
			"no control fill in [%d,%d %d,%d], and no `at_fallback:` to use instead: the "+
				"dominant colour there is too small to be a control, or there is none",
			r[0], r[1], r[2], r[3])
	}
	e.Driver.Log(fmt.Sprintf("  the control spans x=%d..%d y=%d..%d; pressing (%d,%d)",
		left, right, top, bottom, cx, cy))
	// `double: true` MAKES IT A DOUBLE-CLICK, because some controls only open on one - a desktop icon
	// in particular, and the gap between the two clicks is what makes it a double-click rather than
	// two clicks.
	if v, ok := s.Spec["double"]; ok {
		if dbl, isBool := v.(bool); isBool && dbl {
			if err := e.Driver.DoubleClick(cx, cy); err != nil {
				return fail(s.Name, "double-click (%d, %d): %v", cx, cy, err)
			}
			return e.settle(s)
		}
	}
	if err := e.Driver.Click(cx, cy); err != nil {
		return fail(s.Name, "click (%d, %d): %v", cx, cy, err)
	}
	return e.settle(s)
}

// --- the console, which is not the screen ----------------------------------------------

// ConsolePath is where qemu writes the guest's serial output.
//
// IT IS A SEPARATE CHANNEL FROM THE SCREEN, and one step needs it: a Windows install ISO prints
// "Press any key to boot from CD or DVD..." in its boot sector and waits about five seconds. That
// text is on the SERIAL CONSOLE - the firmware's own output - not on the framebuffer a screenshot
// reads, and pressing the key at the wrong moment means the firmware falls through to PXE and the
// run ends with no Windows at all.
//
// MUST MATCH THE `-serial file:` THE DRIVER PASSES. A path in two places is a path that will
// disagree, so it is named once here.
func ConsolePath(work string) string { return filepath.Join(work, "console.log") }

// doUntilConsole waits for text in the guest's serial output, then reports it.
//
// `until_console:` IS `until:` FOR A CHANNEL THAT IS NOT THE SCREEN, and one specific problem needs
// it. A Windows install ISO prints, in its BOOT SECTOR,
//
//	Press any key to boot from CD or DVD...
//
// and waits about five seconds for a key; none arrives, so the firmware falls through to PXE and the
// run ends with four thousand bytes of firmware output and no Windows.
//
// WHY THIS IS NOT AN OCR STEP, HAVING CONSIDERED IT. The prompt IS drawn on the framebuffer, so a
// screenshot could in principle read it - but that is the wrong marker and the wrong timescale:
//
//   - THE MARKER THAT WORKS IS THE FIRMWARE'S OWN, and it is not on the screen at all. It is OVMF's
//     line `UEFI QEMU ... DVD-ROM`, which appears SECONDS BEFORE the prompt and in every UEFI boot.
//     dockur/windows triggers on exactly it, and the Python implementation's first attempt - waiting
//     for the visible prompt instead - fell through to PXE: "The prompt is on screen for about five
//     seconds; by 60 the guest had fallen through to PXE and the run was over."
//   - FIVE SECONDS IS FEWER OCR ROUNDS THAN KEYS. A screendump plus a tesseract pass is most of a
//     second, so a five-second window gives four or five attempts at a string that OCR can also
//     misread ("DUD" for "DVD"). Text matching on a file has no such failure.
//
// AND THE KEYPRESS ITSELF IS TIMING-SENSITIVE IN A WAY THAT BOUNDS THE WHOLE APPROACH: the key is
// ESC rather than Return because a Return left in the queue when Windows reaches its desktop opens
// the Start menu, and a run that did that "looks like a bake that failed at the end: a desktop, then
// a screen full of menu". The earlier the press, the safer it is, so the EARLIER marker wins.
//
// OCR STILL HAS ITS PLACE IN THIS STAGE, one step later: `until:` on the installer's first pane is
// how the run confirms it actually reached Setup. That is a question about a screen, which is what
// OCR is for; this step is a question about a five-second firmware window, which is not.
func (e *Executor) untilConsole(s walk.Step) error {
	want := s.Get("until")
	if want == "" {
		return fail(s.Name, "`until: {from: console}` must name the text to wait for")
	}
	// THE 180 IS NOT A CONSTANT ANY MORE WHEN THE FILE'S OWN `boot:` SAID OTHERWISE.
	timeout := e.budget(s, "timeout", 180)
	poll := seconds(s, "poll", 1)
	// THE BLIND FALLBACK, AND IT IS FROM MEASUREMENT RATHER THAN CAUTION. Some media boot without
	// any prompt, and some firmwares never emit the marker - so a step that waited for text alone
	// would wait out its whole budget on a guest that was already at the pane it was waiting for.
	// dockur keeps the same fallback for the same reason.
	blindAfter := seconds(s, "blind_after", 0)

	path := ConsolePath(e.Work)
	e.Driver.Log(fmt.Sprintf("  waiting up to %s for %q in %s", timeout,
		shorten(want, 60), filepath.Base(path)))

	start := time.Now()
	var last string
	for time.Since(start) < timeout {
		text, err := readTail(path, 256<<10)
		if err != nil {
			// A CONSOLE THAT DOES NOT EXIST YET IS NOT A FAILURE: qemu creates the file when it
			// starts, and a boot step may return before that write lands.
			if !os.IsNotExist(err) {
				return fail(s.Name, "%v", err)
			}
		} else {
			last = text
			if strings.Contains(text, want) {
				e.Driver.Log(fmt.Sprintf("  saw %q (%s)", shorten(want, 60),
					time.Since(start).Round(time.Second)))
				return nil
			}
		}
		if blindAfter > 0 && time.Since(start) >= blindAfter {
			e.Driver.Log(fmt.Sprintf("  no %q after %s; going on anyway (blind_after)",
				shorten(want, 60), blindAfter))
			return nil
		}
		time.Sleep(poll)
	}
	// THE TAIL IS THE DIAGNOSIS. "never appeared" for a console marker means either the guest never
	// got that far or the file is not being written, and the last lines distinguish them.
	return fail(s.Name, "never saw %q in %s within %s; the console ends with:\n%s",
		shorten(want, 60), path, timeout, lastLines(last, 20))
}

// readTail reads the end of a file, for output that grows without bound.
func readTail(path string, limit int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	start := int64(0)
	if fi.Size() > limit {
		start = fi.Size() - limit
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return "", err
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func lastLines(text string, n int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	if len(lines) == 1 && lines[0] == "" {
		return "  (the file is empty - qemu has written nothing yet)"
	}
	for i, l := range lines {
		lines[i] = "  " + shorten(strings.TrimRight(l, "\r"), 160)
	}
	return strings.Join(lines, "\n")
}

func shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// --- a host-side log, for a step that witnesses rather than judges ---------------------
func (e *Executor) doRun(s walk.Step) error {
	// `run:` TAKES A COMMAND OR A MAPPING WITH `cmd:` AND `via:`. The mapping form is how 2.0 says
	// WHERE the command runs, so the body is read from whichever shape the step holds.
	body := s.Get("run")
	if m, ok := s.Spec["run"].(map[string]any); ok {
		if c, ok := m["cmd"].(string); ok {
			body = c
		}
	}
	if strings.TrimSpace(body) == "" {
		return fail(s.Name, "`run:` must contain a command")
	}
	if wait, _ := s.Float("terminal_wait", 0); wait > 0 {
		if err := e.Driver.Stroke([]string{"ctrl", "alt"}, "t"); err != nil {
			return fail(s.Name, "opening the terminal: %v", err)
		}
		time.Sleep(time.Duration(wait * float64(time.Second)))
	}
	perChar, _ := s.Float("type_gap", 0.02)
	lineGap, _ := s.Float("line_gap", 1.0)
	gap := time.Duration(perChar * float64(time.Second))
	between := time.Duration(lineGap * float64(time.Second))

	// MULTI-LINE, AND THAT IS NOT COSMETIC. `keys.for_text` refuses a newline, and a version that
	// typed the body as one string silently dropped everything after it - measured, in the Python
	// implementation's own note. Each line is typed, Return, then the next: exactly what a person
	// does at a terminal.
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	// THE COMMAND IS LOGGED, TRUNCATED, BECAUSE A `run:` THAT TYPED NOTHING AND ONE THAT TYPED THE
	// WRONG THING LOOK IDENTICAL WITHOUT IT.
	//
	// Measured on manjaro's ssh leg: six `run:` steps each completed in under a tenth of a second and
	// printed nothing at all, so there was no way to tell whether the terminal had received the
	// command, received an empty line, or never received focus. The Python implementation logged
	// `typed N character(s)` for `type:` and nothing for `run:`, and that gap cost this run its
	// diagnosis.
	e.Driver.Log(fmt.Sprintf("  running %d line(s), %d character(s): %s", len(lines),
		len([]rune(body)), shorten(strings.ReplaceAll(body, "\n", " ; "), 140)))
	// THE COMMAND ITSELF IS EVIDENCE. A `run:` step's whole effect on the guest is the text it typed,
	// and until this the journal could say "step 12 ran" without being able to say what it ran.
	if e.evidence == nil {
		e.evidence = &evidence{}
	}
	e.evidence.Command = append([]string(nil), lines...)
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		unsupported, err := e.Driver.Type(line, gap)
		if err != nil {
			return fail(s.Name, "typing line %d: %v", i+1, err)
		}
		if len(unsupported) > 0 {
			// LOUD, because a command that lost a character is a command that will fail in the
			// guest with a message about ITS syntax rather than about this step.
			e.Driver.Log(fmt.Sprintf("  COULD NOT TYPE %q on line %d", string(unsupported), i+1))
		}
		if err := e.Driver.Key("ret"); err != nil {
			return fail(s.Name, "pressing Return after line %d: %v", i+1, err)
		}
		if i < len(lines)-1 {
			time.Sleep(between)
		}
	}
	if err := e.settle(s); err != nil {
		return err
	}

	// A `run:` MAY CARRY ITS OWN `expect:`/`expect_any:`, AND THE DISPATCHER MUST NOT SWALLOW THE TYPING.
	//
	// MEASURED ON manjaro-ssh, step 9 of 23:
	//
	//	=== install openssh
	//	status: failed step 9/23 "install openssh"
	//	error:   install openssh: did not advance: wanted "pacman" or "sudo", read "_ io"
	//
	// `install openssh` HAD NO `running N line(s)` LINE AT ALL - the trace this project added precisely
	// so a silent `run:` could be told from an empty one. THE CAUSE IS THE DISPATCH ORDER: `expect_any`
	// is checked BEFORE `run` in the executor's switch, so a step with both went to `doExpectAny` and
	// THE TYPEWRITER NEVER RAN. It read the screen, found nothing, and reported a screen that had not
	// advanced - which is exactly what a step that did nothing would report.
	//
	// The check is the SAME ONE `doExpectAny` makes, run after the typing it is about.
	if s.Has("expect") || s.Has("expect_any") {
		return e.doExpectAny(s)
	}
	return nil
}

// describeLastError renders the last failed read for a log line, or nothing when the last poll read.
func describeLastError(err error) string {
	if err == nil {
		return ""
	}
	return ": " + firstLineOf(err.Error())
}

// setsOfAny reads the value of `when: {none: ...}` into word-sets.
//
// A flat list is ONE set (all of its words), and a list of lists is one set per inner list - the
// same reading `wordSetsOf` gives `when_not_any:`, so the two spellings cannot disagree.
func setsOfAny(v any) [][]string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([][]string, 0, len(list))
	for _, item := range list {
		switch t := item.(type) {
		case string:
			out = append(out, []string{t})
		case []any:
			out = append(out, stringsToSlice(t))
		}
	}
	return out
}
