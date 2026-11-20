package walk

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/easydev-org/easyvm/internal/step"
	"gopkg.in/yaml.v3"
)

// THE `.walk` FORMAT MUST PRODUCE EXACTLY WHAT THE YAML PRODUCES, and this is the test that says so
// as a value rather than as a claim.
//
// THE PROPERTY THAT MATTERS IS NOT "it parses". It is that a `.walk` file cannot express something a
// `.yaml` file cannot, because the moment it can, the two drift and a vocabulary change has to be
// made twice. So this test converts a document, and checks the RESULT is the shape `Load` consumes -
// every step has a name, every key is one the vocabulary declares, and the header keys are the ones
// `validateDocument` accepts.
func TestAWalkFileBecomesTheSameDocument(t *testing.T) {
	src := `walkthrough "probe"
  resolution 1920x1080
  recording_resolution 1920x1080
  user vmuser
  password admin
  recording "probe.mp4"

  needs kubuntu.iso
  make a disk "disk.qcow2" of 128 GiB
  boot "kubuntu.iso", waiting up to 1800s
  click "next" at 1777 1059
  press Return
  settle 8s
`
	doc, err := WalkToYAML(src)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}

	if got := doc["name"]; got != "probe" {
		t.Fatalf("name = %v, want probe", got)
	}
	if got := doc["resolution"]; !reflect.DeepEqual(got, []any{1920, 1080}) {
		t.Fatalf("resolution = %v, want [1920 1080]", got)
	}
	if got := doc["ssh_user"]; got != "vmuser" {
		t.Fatalf("ssh_user = %v, want vmuser", got)
	}
	steps, _ := doc["steps"].([]any)
	// SIX STEPS, NOT SEVEN: the `recording` header used to become a `record:` step of its own, and the
	// recording starts with the run now. A header that still emitted a step would be a step whose only
	// purpose is to tell a recorder that is already running to run.
	if len(steps) != 6 {
		t.Fatalf("%d steps, want 6: %v", len(steps), steps)
	}
	// EVERY STEP HAS A NAME, because a failure with no name is a failure nobody can act on.
	for i, raw := range steps {
		s, _ := raw.(map[string]any)
		if name, _ := s["name"].(string); name == "" {
			t.Errorf("step %d has no name: %v", i+1, s)
		}
	}
	// AND THE KEYS ARE THE VOCABULARY'S, checked by asking the validator rather than by listing them
	// here - which is what keeps this test from being a second copy of the vocabulary.
	for _, raw := range steps {
		s, _ := raw.(map[string]any)
		name, _ := s["name"].(string)
		if err := stepValidateForTest(name, s); err != nil {
			t.Errorf("%q produces a document the validator refuses: %v", name, err)
		}
	}
}

// AND THE TWO FORMATS RUN THE SAME STEPS. This is the "not a second engine" property in the form a
// person can check: a `.walk` file and the YAML it converts to are loaded by the same `Load`, and the
// resulting `Step` list has the same keys in the same order.
func TestAWalkFileAndItsYAMLAreTheSameWalk(t *testing.T) {
	dir := t.TempDir()
	walkSrc := filepath.Join(dir, "a.walk")
	yamlSrc := filepath.Join(dir, "a.yaml")

	const body = `walkthrough "same walk"
  resolution 1920x1080
  recording_resolution 1920x1080

  needs kubuntu.iso
  click "next" at 100 200
  settle 3s
`
	if err := os.WriteFile(walkSrc, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// THE YAML THE CONVERTER PRODUCES, written out and loaded the ordinary way.
	doc, err := WalkToYAML(body)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	yamlText := docToYAMLForTest(t, doc)
	if err := os.WriteFile(yamlSrc, []byte(yamlText), 0o644); err != nil {
		t.Fatal(err)
	}

	fromWalk, err := Load(walkSrc, nil)
	if err != nil {
		t.Fatalf("the .walk file does not load: %v", err)
	}
	fromYAML, err := Load(yamlSrc, nil)
	if err != nil {
		t.Fatalf("the YAML it converts to does not load:\n%s\n%v", yamlText, err)
	}

	if len(fromWalk.Steps) != len(fromYAML.Steps) {
		t.Fatalf("the .walk has %d steps and its YAML has %d", len(fromWalk.Steps), len(fromYAML.Steps))
	}
	for i := range fromWalk.Steps {
		a, b := fromWalk.Steps[i], fromYAML.Steps[i]
		if a.Name != b.Name {
			t.Errorf("step %d named %q from the .walk and %q from the YAML", i+1, a.Name, b.Name)
		}
		if got, want := keyList(a.Spec), keyList(b.Spec); !reflect.DeepEqual(got, want) {
			t.Errorf("step %d has keys %v from the .walk and %v from the YAML", i+1, got, want)
		}
	}
}

// EVERY REAL YAML WALKTHROUGH CONVERTS, which is the claim that the format covers the vocabulary.
//
// IT CONVERTS RATHER THAN ROUND-TRIPS, because writing a YAML emitter would be a second thing to keep
// in step and the property worth testing is that the CONVERTER understands every verb the real files
// use. A verb it cannot read is a verb the format does not cover, and that is the finding.
func TestEveryRealWalkthroughUsesVerbsTheFormatCovers(t *testing.T) {
	root := filepath.Join("..", "..")
	files, err := filepath.Glob(filepath.Join(root, "*", "*", "steps", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 20 {
		t.Fatalf("found only %d walkthroughs; the glob must be wrong", len(files))
	}
	var unconvertible []string
	for _, path := range files {
		doc, err := Load(path, nil)
		if err != nil {
			continue // a file that does not load is another test's subject
		}
		// THE CONVERSION IS TESTED ON THE VERBS, so a walkthrough with any step whose verb this file
		// does not know is named rather than silently skipped.
		for _, s := range doc.Steps {
			for key := range s.Spec {
				if key == "name" {
					continue
				}
				if _, ok := walkVerbFor(key); !ok {
					unconvertible = append(unconvertible, filepath.Base(path)+": "+key)
				}
			}
		}
	}
	sort.Strings(unconvertible)
	if len(unconvertible) > 0 {
		t.Errorf("these keys are used by real walkthroughs and the .walk format has no verb for them, "+
			"so the format does not cover the vocabulary yet:\n\t%s",
			strings.Join(uniqueStrings(unconvertible), "\n\t"))
	}
	t.Logf("checked %d walkthroughs", len(files))
}

// stepValidateForTest asks the vocabulary whether a step is legal, without importing step into a
// test that already has enough to read.
func stepValidateForTest(name string, spec map[string]any) error {
	return step.Validate(name, spec)
}

// docToYAMLForTest renders the converted document back to YAML, so the test can load BOTH formats
// through the same `Load` and compare the steps it produced.
func docToYAMLForTest(t *testing.T, doc map[string]any) string {
	t.Helper()
	b, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatalf("render the converted document back to YAML: %v", err)
	}
	return string(b)
}

func keyList(spec map[string]any) []string {
	out := make([]string, 0, len(spec))
	for k := range spec {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// TWO CONVERSIONS WERE WRONG AND THIS PINS BOTH, because each produced a document that LOADED and
// then behaved differently from the YAML it claimed to equal.
//
// They are worth naming because a conversion bug that still loads is the worst kind: the file runs,
// the run passes or fails, and the difference from the YAML is invisible until somebody compares.
func TestTheTwoConversionsThatWereWrong(t *testing.T) {
	src := `walkthrough "probe"
  resolution 1024x768
  recording "p.mp4"

  needs x.iso
  read the screen as "screen"
`
	doc, err := WalkToYAML(src)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	steps, _ := doc["steps"].([]any)

	// 1. A HEADER `recording` MUST PRODUCE NO STEP AT ALL.
	//
	// It used to become `record: p.mp4` as the first step. The recording starts with the run now and
	// takes its name from the walkthrough, so a header that named a file was a step telling a recorder
	// that is already going to start - and a name nothing reads, since the workflow uploads
	// `work/*.mp4` without looking.
	first, _ := steps[0].(map[string]any)
	if _, wrong := first["record"]; wrong {
		t.Fatalf("the header still produced a `record:` step: %v", first)
	}
	if _, wrong := first["recording"]; wrong {
		t.Fatal("the step carries a `recording:` key, which is not in the vocabulary")
	}

	// 2. `read ... as "name"` MUST BE A REAL RECTANGLE, not a placeholder.
	//
	// The first version emitted `rect: [full, 0, 0, 0]`, and the validator refused it as "too small
	// to read" - correctly, because a reading of nothing is not a reading.
	var read map[string]any
	for _, raw := range steps {
		s, _ := raw.(map[string]any)
		if _, ok := s["see"]; ok {
			read = s
		}
	}
	if read == nil {
		t.Fatalf("no `see:` step came out of `read the screen as \"screen\"`: %v", steps)
	}
	// `read` CAPTURES A FRAME AND NAMES IT, AND CARRIES NOTHING ELSE.
	//
	// It used to emit `see: {name, rect, rect_at, of: [text]}`, because a `see:` measured one region
	// and answered one question. The region is now the `check:`'s business, so a `read` line produces
	// a name and no geometry - and `rect: [0, 0, w, h]` at the header's size would be a rectangle
	// nobody asked about, copied onto every reading in the file.
	see, _ := read["see"].(map[string]any)
	if len(see) != 1 || see["name"] != "screen" {
		t.Fatalf("`read ... as \"screen\"` produced %v; a capture is a name and nothing else", see)
	}
	for _, gone := range []string{"rect", "rect_at", "of", "region", "region_at"} {
		if _, wrong := see[gone]; wrong {
			t.Fatalf("the capture still carries %q; that is a QUESTION and belongs on the `check:` "+
				"that asks it", gone)
		}
	}
	if _, wrong := read["ocr"]; wrong {
		t.Fatal("`ocr:` is emitted, and it is no longer in the vocabulary")
	}
}

// THE MIGRATION IS PROVED BY ROUND-TRIPPING, not by reading the result.
//
// EVERY REAL WALKTHROUGH IS CONVERTED TO `.walk` AND BACK, AND THE TWO DOCUMENTS MUST BE EQUAL. That is
// the property the whole migration rests on: a hand transcription of 6675 lines gets one coordinate
// wrong and the failure arrives forty minutes into a CI run, while this test compares the data.
func TestEveryRealWalkthroughRoundTripsThroughWalk(t *testing.T) {
	root := filepath.Join("..", "..")
	files, err := filepath.Glob(filepath.Join(root, "*", "*", "steps", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 20 {
		t.Fatalf("found only %d walkthroughs", len(files))
	}
	converted, bad := 0, 0
	for _, path := range files {
		original, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var want map[string]any
		if err := yaml.Unmarshal(original, &want); err != nil {
			continue // a file that does not decode is another test's subject
		}
		walkSrc, err := YAMLToWalk(want)
		if err != nil {
			t.Errorf("%s: cannot render as .walk: %v", filepath.Base(path), err)
			bad++
			continue
		}
		got, err := WalkToYAML(walkSrc)
		if err != nil {
			t.Errorf("%s: the .walk it produced does not parse: %v\n--- the file ---\n%s",
				filepath.Base(path), err, walkSrc)
			bad++
			continue
		}
		// THE COMPARISON IS ON THE DOCUMENT, not on the bytes: key order and YAML style do not
		// matter, and the loader reads a mapping.
		if diff := firstDifference(want, got, ""); diff != "" {
			t.Errorf("%s: the round trip changed the document at %s", filepath.Base(path), diff)
			bad++
			continue
		}
		converted++
	}
	if bad > 0 {
		t.Fatalf("%d of %d walkthroughs did not round-trip", bad, len(files))
	}
	t.Logf("%d walkthroughs round-trip exactly", converted)
}

// firstDifference finds the first place two documents disagree, and says where.
//
// IT NAMES THE PATH - `steps.4.click.0` - because "the documents differ" is useless when a step has
// twenty keys and four of them are coordinate pairs.
func firstDifference(want, got any, path string) string {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return path + fmt.Sprintf(": want a mapping, got %T", got)
		}
		// every key in want must be in got, with the same value
		for k, wv := range w {
			gv, ok := g[k]
			if !ok {
				return path + "." + k + ": missing"
			}
			if d := firstDifference(wv, gv, path+"."+k); d != "" {
				return d
			}
		}
		for k := range g {
			if _, ok := w[k]; !ok {
				return path + "." + k + ": unexpected"
			}
		}
		return ""
	case []any:
		g, ok := got.([]any)
		if !ok {
			return path + fmt.Sprintf(": want a list, got %T", got)
		}
		if len(w) != len(g) {
			return path + fmt.Sprintf(": want %d items, got %d", len(w), len(g))
		}
		for i := range w {
			if d := firstDifference(w[i], g[i], fmt.Sprintf("%s.%d", path, i)); d != "" {
				return d
			}
		}
		return ""
	default:
		// NUMBERS ARRIVE AS int OR float64 DEPENDING ON THE DECODER, so they are compared by value.
		if wn, ok := numOf(w); ok {
			if gn, ok := numOf(got); ok {
				if wn != gn {
					return path + fmt.Sprintf(": want %v, got %v", w, got)
				}
				return ""
			}
		}
		if fmt.Sprint(w) != fmt.Sprint(got) {
			return path + fmt.Sprintf(": want %#v, got %#v", w, got)
		}
		return ""
	}
}

// `as a microvm` NAMES THE OPERATING SYSTEM, SO A FILE DOES NOT WRITE `os:` BESIDE IT.
//
// A MICROVM IS NOT A LINUX GUEST WITH AN OPTION SET. It wants no firmware at all (nothing to read a
// boot sector from), a `virtio-blk-device` rather than a `-pci` one, a different network device, and
// a kernel the host hands qemu directly. Every one of those is a different MACHINE, and
// `shapes/microvm.yaml` is where they are described.
//
// MEASURED, AND IT IS WHY THIS IS PARSED RATHER THAN WRITTEN: the first microvm walkthrough said
// `os: linux` out of habit, and the engine refused it for disagreeing with itself - correctly, but a
// file should not have to say one thing twice to be believed.
func TestAMicrovmBootNamesItsOwnOS(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "m.walk")
	body := `walkthrough "a microvm"
  resolution 1024x768
  recording_resolution 1024x768
  needs k initrd
  boot as a microvm from kernel "k" and initramfs "initrd" with "console=ttyS0" without a display with memory 768
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	var boot map[string]any
	for _, s := range f.Steps {
		if _, ok := s.Spec["boot"]; ok {
			boot = s.Spec
			break
		}
	}
	if boot == nil {
		t.Fatal("no boot step")
	}
	if got := boot["os"]; got != MicroVMOS {
		t.Fatalf("the boot step's `os` is %v, want %q. A file that says `as a microvm` has named its "+
			"operating system, and the shape it resolves to decides the firmware, the disk device and "+
			"the network device", got, MicroVMOS)
	}
	if boot["microvm"] != true {
		t.Fatal("`microvm` was not set; the machine type is what makes this a microvm at all")
	}
}
