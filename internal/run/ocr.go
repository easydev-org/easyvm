package run

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/easydev-org/easyvm/internal/screen"
	"github.com/easydev-org/easyvm/internal/walk"
)

// THE THREE-STEP MODEL, AND WHY IT REPLACED SIX VERBS THAT EACH DID TWO THINGS.
//
// THE OLD MODEL: `until`, `until_any`, `expect`, `expect_any`, `unless` and `unless_any` all meant
// "run OCR and then decide", and they were the same keys that could sit inside a click step:
//
//	- name: open the installer
//	  at: [960, 743]          <- an ACTION
//	  repeat: [2, 0.8]
//	  settle: 35
//	  expect_any:             <- AND an OCR read, in the same step
//	  - [welcome]
//
// TWO FAULTS FOLLOWED FROM THAT, BOTH MEASURED:
//
//  1. THE REGION WAS OPTIONAL, AND FORTY-SEVEN OF SEVENTY STEPS LEFT IT OUT, so they read the whole
//     screen. A whole-frame read on a GUI full of installer text matches words that belong to
//     another pane - and it is slow, which is what made a 640x480 gate expire before its first
//     usable reading.
//
//  2. A FAILURE COULD NOT SAY WHICH HALF WAS WRONG. `did not advance` is the same message whether
//     the click missed or the crop was pointed at the wrong strip.
//
// SO READING IS A STEP. It requires a `region:`, it names what it read, and it decides NOTHING:
//
//	- name: read the recovery menu
//	  ocr:
//	    name: menu
//	    region: [760, 200, 1400, 560]
//	    region_at: [1920, 1080]
//	    scale: 6
//
// THE READING IS A FILE AND A VARIABLE. `work/ocr/menu.txt` holds the words a person can open after
// a failure, and `when: menu` answers from the same list without touching the screen again. That
// separation is the point: a condition can be re-asked at no cost, and a post-mortem has the exact
// text the decision was made from.
//
// CONDITIONS ARE A STEP TOO, so "is it up" and "press the button" are not one entry:
//
//	- name: the menu is up
//	  when: menu
//	  has_any:
//	  - [restore, time, machine]
//	  - [safari]

// ocrSpec is one `ocr:` mapping, already validated.
type ocrSpec struct {
	// Name is what the reading is called. `check:` and `loop.until` reach it by this name.
	Name string
	// Region is REQUIRED. A read with no rectangle is a read of the whole screen.
	Region [4]int
	// RegionAt is the size the rectangle was measured at, so it can be scaled for a guest that
	// draws at another size.
	RegionAt [2]int
	// Scale is the enlargement before tesseract.
	Scale int
	// Wait is REQUIRED and is the pause BEFORE the capture.
	//
	// THE READING OFTEN FOLLOWS AN ACTION, AND THE PANE REPAINTS FOR SECONDS AFTER IT. `settle:` is
	// the pause after an action; `wait:` is the pause before a reading, and making it explicit means
	// the file says how long it waited and for what. MEASURED ON the macOS recovery menu, where a
	// poll landing mid-paint reads half a window and the gate then matches a row that is not there.
	Wait float64
}

// doOCR reads one region, saves it, and binds it to a name. It decides nothing.
func (e *Executor) doOCR(s walk.Step) error {
	spec, err := parseOCRStep(s)
	if err != nil {
		return err
	}

	// THE WAIT IS EXPLICIT IN THE FILE AND HONOURED HERE.
	if spec.Wait > 0 {
		time.Sleep(time.Duration(spec.Wait * float64(time.Second)))
	}

	f, err := e.Driver.Frame()
	if err != nil {
		return fail(s.Name, "%v", err)
	}
	img, err := screen.Load(f.Path())
	if err != nil {
		return fail(s.Name, "%v", err)
	}

	region := spec.Region
	if spec.RegionAt != [2]int{} {
		w, h := f.Size()
		if at := spec.RegionAt; at[0] != w || at[1] != h {
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

	cropped, err := screen.Crop(img, region, spec.Scale)
	if err != nil {
		return fail(s.Name, "%v", err)
	}
	words, err := e.readCrop(cropped, spec.Name)
	if err != nil {
		return fail(s.Name, "%v", err)
	}

	// THE READING IS SAVED AS TEXT, because the condition may be re-asked and a post-mortem needs
	// the words the decision was actually made from - not a re-read of a screen that has moved on.
	dir := filepath.Join(e.Work, "ocr")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fail(s.Name, "%v", err)
	}
	path := filepath.Join(dir, spec.Name+".txt")
	if err := os.WriteFile(path, []byte(strings.Join(words, " ")+"\n"), 0o644); err != nil {
		return fail(s.Name, "%v", err)
	}
	e.Driver.Log(fmt.Sprintf("  %s: %d word(s) -> %s", s.Name, len(words),
		filepath.Join("ocr", spec.Name+".txt")))

	if e.seen == nil {
		e.seen = make(map[string][]string)
	}
	e.seen[spec.Name] = words
	return nil
}

// parseOCRStep validates an `ocr:` mapping. THE REGION IS REQUIRED AND ITS ABSENCE IS AN ERROR.
func parseOCRStep(s walk.Step) (ocrSpec, error) {
	raw, ok := s.Spec["ocr"].(map[string]any)
	if !ok {
		return ocrSpec{}, fail(s.Name, "`ocr:` must be a mapping: name, region, region_at, scale")
	}
	var spec ocrSpec
	if name, ok := raw["name"].(string); ok {
		spec.Name = strings.TrimSpace(name)
	}
	if spec.Name == "" {
		return ocrSpec{}, fail(s.Name, "`ocr:` must name what it read: `ocr: {name: menu, ...}`. "+
			"A reading with no name cannot be asked about by `check:`")
	}
	region, ok := rectOfMapping(raw, "region")
	if !ok {
		return ocrSpec{}, fail(s.Name, "`ocr:` must name its `region: [x0, y0, x1, y1]`. A read "+
			"without one reads the whole screen, which is how a gate matched the title of the "+
			"window it was waiting to open, and what made 47 of 70 steps read a full 1920x1080 frame")
	}
	spec.Region = region
	if at, ok := pairOfMapping(raw, "region_at"); ok {
		spec.RegionAt = at
	}
	spec.Scale = 3
	if v, ok := toFloat(raw["scale"]); ok && v > 0 {
		spec.Scale = int(v)
	}
	wait, ok := toFloat(raw["wait"])
	if !ok || wait < 0 {
		return ocrSpec{}, fail(s.Name, "`ocr:` must say `wait:` - the seconds to wait BEFORE the "+
			"capture. A reading taken the instant after a click sees the previous pane, and a "+
			"number written down is the only way to know what a run actually waited for")
	}
	spec.Wait = wait
	// THE MAPPING MAY NOT CARRY ANYTHING ELSE, so a typo is refused rather than ignored - the same
	// rule the step vocabulary applies.
	for k := range raw {
		switch k {
		case "name", "region", "region_at", "scale", "wait":
		default:
			return ocrSpec{}, fail(s.Name, "`ocr:` has unknown key %q; it takes name, region, "+
				"region_at, scale and wait", k)
		}
	}
	return spec, nil
}

// doWhen answers a condition from a reading, WITHOUT reading the screen again.
//
// IT IS THE THIRD STEP OF THE THREE, and the reason it takes a NAME rather than a region: the screen
// may have moved on, and a condition has to answer about the frame the action produced.
func (e *Executor) doWhen(s walk.Step) error {
	name := s.Get("check")
	if name == "" {
		return fail(s.Name, "`check:` must name a reading an `ocr:` step produced")
	}
	words, ok := e.seen[name]
	if !ok {
		return fail(s.Name, "`when: %s` names a reading that no `ocr:` step has made. The steps "+
			"run in file order, and an `ocr:` step has to come first with that `name:`", name)
	}

	if set := wordsOf(s, "says"); len(set) > 0 {
		if !says(words, set) {
			return failWithWords(s.Name, words, "the reading %q is not what was expected: wanted %s",
				name, strings.Join(set, " "))
		}
		e.Driver.Log(fmt.Sprintf("  %s: read %s", s.Name, strings.Join(set, " ")))
		return nil
	}
	sets := wordSetsOf(s, "says_any")
	if len(sets) == 0 {
		return fail(s.Name, "`when: %s` needs `says:` or `says_any:` - a condition with no test", name)
	}
	for _, set := range sets {
		if says(words, set) {
			e.Driver.Log(fmt.Sprintf("  %s: read %q", s.Name, strings.Join(set, " ")))
			return nil
		}
	}
	return failWithWords(s.Name, words, "the reading %q matched none of %s", name,
		describeSets(sets))
}

// failWithWords is `fail` with the reading attached, because the reading is what the decision used.
func failWithWords(step string, words []string, format string, args ...any) error {
	e := &StepError{Step: step, What: fmt.Sprintf(format, args...)}
	if len(words) > 0 {
		e.Screen = "read: " + truncateWords(words, 24)
	}
	return e
}

// rectOfMapping and pairOfMapping read a rectangle and a pair out of a nested mapping.
func rectOfMapping(m map[string]any, key string) ([4]int, bool) {
	list, ok := m[key].([]any)
	if !ok || len(list) != 4 {
		return [4]int{}, false
	}
	var out [4]int
	for i, v := range list {
		f, ok := toFloat(v)
		if !ok {
			return [4]int{}, false
		}
		out[i] = int(f)
	}
	if out[2] <= out[0] || out[3] <= out[1] {
		return [4]int{}, false
	}
	return out, true
}

func pairOfMapping(m map[string]any, key string) ([2]int, bool) {
	list, ok := m[key].([]any)
	if !ok || len(list) != 2 {
		return [2]int{}, false
	}
	var out [2]int
	for i, v := range list {
		f, ok := toFloat(v)
		if !ok {
			return [2]int{}, false
		}
		out[i] = int(f)
	}
	return out, true
}
