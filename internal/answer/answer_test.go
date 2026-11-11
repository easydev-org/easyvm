package answer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// THE TEMPLATE IN THE REPOSITORY USES EXACTLY THE PLACEHOLDERS THIS PACKAGE KNOWS.
//
// THIS IS THE TEST FOR THE FAILURE THAT COSTS NINE MINUTES OF FRAMES OF A PAGE NOBODY CLICKS.
// __WINDOWS_IMAGE_INDEX__ was missing from the Python implementation's substitution list while the
// template used it, so Setup received a literal string where it expected the edition index, could
// not resolve it, and abandoned the unattended flow at "Enter a product key" - 131 identical frames
// (run measured, see the note in substitute).
//
// A template that invents a placeholder and a submission that omits one are the same bug from two
// sides, so both directions are checked here against the real file.
func TestTheRealTemplateAndTheVocabularyAgree(t *testing.T) {
	const template = "../../windows/11/answer/Autounattend.xml"
	body, err := os.ReadFile(template)
	if err != nil {
		t.Skipf("no template here (%v)", err)
	}
	text := string(body)

	// EVERY PLACEHOLDER IN THE FILE IS ONE THIS PACKAGE KNOWS.
	seen := map[string]bool{}
	for place := range knownPlaceholders {
		if strings.Contains(text, place) {
			seen[place] = true
		}
	}
	if len(seen) == 0 {
		t.Fatal("the template uses none of the placeholders this package knows; the substitution " +
			"would be a no-op and Setup would get whatever the template literally says")
	}
	for place := range seen {
		if !knownPlaceholders[place] {
			t.Fatalf("%s is used by the template and is not in the vocabulary", place)
		}
	}

	// AND NOTHING ELSE SHAPED LIKE ONE IS LEFT, which is the check that would have caught the
	// original bug: a placeholder in the file that this package does not know about is a value
	// Setup receives as literal text.
	for i := 0; i < len(text); i++ {
		if !strings.HasPrefix(text[i:], "__") {
			continue
		}
		rest := text[i+2:]
		end := strings.Index(rest, "__")
		if end <= 0 {
			continue
		}
		candidate := "__" + rest[:end] + "__"
		// Only a name made of capitals and underscores is a placeholder; XML has no other use for
		// a double underscore pair, so this catches every real one.
		name := rest[:end]
		if strings.Trim(name, "ABCDEFGHIJKLMNOPQRSTUVWXYZ_") != "" {
			continue
		}
		if !knownPlaceholders[candidate] {
			t.Fatalf("the template contains %q, which is not in this build's vocabulary (%s). "+
				"Setup would receive it as literal text and fall back to asking a human",
				candidate, strings.Join(Placeholders(), ", "))
		}
	}

	// AND SUBSTITUTING ALL OF THEM LEAVES NONE BEHIND.
	values := map[string]string{}
	for place := range knownPlaceholders {
		values[place] = "x"
	}
	out, err := substitute(text, values)
	if err != nil {
		t.Fatalf("the real template cannot be fully substituted: %v", err)
	}
	if strings.Contains(out, "__") {
		t.Fatalf("a placeholder survived substitution")
	}
}

// A PLACEHOLDER LEFT BEHIND IS REFUSED, NAMING IT, BEFORE ANY ISO IS BUILT.
func TestALeftoverPlaceholderIsRefusedByName(t *testing.T) {
	_, err := substitute("<x>__ADMIN_USER__ and __WINDOWS_IMAGE_INDEX__</x>",
		map[string]string{"__ADMIN_USER__": "vmuser"})
	if err == nil {
		t.Fatal("a template with an unfilled placeholder must be refused")
	}
	if !strings.Contains(err.Error(), "__WINDOWS_IMAGE_INDEX__") {
		t.Fatalf("the refusal must name the placeholder that was left; got %v", err)
	}
	if strings.Contains(err.Error(), "__ADMIN_USER__") {
		t.Fatalf("the refusal must not name a placeholder that WAS filled; got %v", err)
	}
}

// AND A VALUE FOR A NAME THIS BUILD DOES NOT KNOW IS REFUSED TOO, because that is a caller and a
// template disagreeing about the vocabulary - and silently ignoring the extra key hides it.
func TestAnUnknownValueNameIsRefused(t *testing.T) {
	_, err := substitute("nothing here", map[string]string{"__PRODUCT_KEY__": "abc"})
	if err == nil {
		t.Fatal("an unknown placeholder name must be refused")
	}
	if !strings.Contains(err.Error(), "__PRODUCT_KEY__") {
		t.Fatalf("the refusal must name the key; got %v", err)
	}
}

// THE XORRISO ARGV HAS NO `-rock`, AND THAT IS MEASURED.
//
// The Python implementation's first version passed `-rock on`, which xorriso does not have - Rock
// Ridge is produced by default, and asking for it is an error rather than a no-op. It died with
// exit 5 ninety seconds into a bake, after a 7.7 GiB download. The argv is pure so this costs
// nothing to check.
func TestTheXorrisoArgumentList(t *testing.T) {
	args := argv("/out/answer.iso", "/stage", "ANSWER")
	joined := strings.Join(args, " ")

	if strings.Contains(joined, "-rock") {
		t.Fatal("the argv asks for -rock, which xorriso does not have; Rock Ridge is on by " +
			"default and the option is an error")
	}
	for _, want := range []string{"-outdev", "-volid", "-joliet", "-map", "-commit"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("the argv is missing %q:\n%s", want, joined)
		}
	}
	// THE MAP DESTINATION IS THE IMAGE ROOT, which is where Setup looks for Autounattend.xml.
	if !strings.Contains(joined, "-map /stage /") {
		t.Fatalf("the staging directory is not mapped to the image root, so Setup would not find "+
			"Autounattend.xml:\n%s", joined)
	}
	// AND THE ISO IS NAMED BY -outdev FIRST, because a later -outdev is what gets written.
	if args[0] != "-outdev" || args[1] != "/out/answer.iso" {
		t.Fatalf("the output device is not first: %v", args[:2])
	}
}

// THE DRIVER LIST DOES NOT INCLUDE vioserial, AND THAT IS A MEASURED EXCLUSION.
//
// Its INF is Class=System, so a System-class INF on this CD is not ignored by windowsPE - Setup
// stages it into the offline image and the installed system loads it on the boot after the file
// copy. Run 42839116205: "Installing 54%", TianoCore, "The computer restarted unexpectedly".
func TestTheDriverListExcludesTheAgentDriver(t *testing.T) {
	for _, d := range winPEDrivers {
		if d == "vioserial" {
			t.Fatal("vioserial is on the answer CD, so Setup stages a System-class driver into " +
				"the image it is installing; the installed system then fails on the boot after " +
				"the file copy (run 42839116205)")
		}
	}
	// AND viostor IS THERE, because without it windowsPE cannot see the disk at all.
	var hasViostor bool
	for _, d := range winPEDrivers {
		if d == "viostor" {
			hasViostor = true
		}
	}
	if !hasViostor {
		t.Fatal("viostor is not on the answer CD; windowsPE then lists no disks and Setup stops " +
			"at a dialog nobody clicks")
	}
}

// A MISSING TEMPLATE IS NAMED BEFORE XORRISO IS INVOKED.
func TestAMissingTemplateIsRefusedEarly(t *testing.T) {
	err := Build(Spec{
		Template: filepath.Join(t.TempDir(), "absent.xml"),
		Out:      filepath.Join(t.TempDir(), "answer.iso"),
	}, func(string) {})
	if err == nil {
		t.Fatal("a missing template must be refused")
	}
	if !strings.Contains(err.Error(), "answer") {
		t.Fatalf("the refusal should say what was missing; got %v", err)
	}
}
