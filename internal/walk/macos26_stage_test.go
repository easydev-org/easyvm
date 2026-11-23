package walk

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// THE STAGE MUST FAIL WHEN THE SETUP ASSISTANT IS NOT ON SCREEN, AND THIS IS THE TEST FOR THAT.
//
// MEASURED ON RUN 44248083728, which published the `macos26` tag and was reported green: its own
// daemon.log ends with
//
//	=== the Setup Assistant is up
//	the Setup Assistant is up: assistant = false (mode says_any: none of "select your country
//	or region" ... in "e")
//
// A `expect:` only BINDS A BOOLEAN - it does not stop a walk - so a script could report success with a
// half-installed disk behind the tag. `until:` reads the screen once and fails the walk when the
// words are absent, which is the assertion this stage needs.
func TestMacos26FailsWhenTheSetupAssistantIsAbsent(t *testing.T) {
	raw, err := os.ReadFile(macosStage(t, "26", "setup"))
	if err != nil {
		t.Fatalf("%v", err)
	}
	src := string(raw)

	// the final gate is the step whose name says so
	re := regexp.MustCompile(`(?s)step "the Setup Assistant is up, OR THIS STAGE FAILS"\n(.*?)\nstep "`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		t.Fatal("the final gate step is gone; a stage that cannot fail on a missing Setup " +
			"Assistant is a stage that publishes whatever it has")
	}
	step := m[1]
	// BOTH SPELLINGS: `until_any ==` is the 1.0 form and `until_any` alone is the 2.0 one.
	if !strings.Contains(step, "until_any ==") && !strings.Contains(step, "until_any ") {
		t.Error("the final gate is not `until_any:`; a `expect:` only binds a boolean and lets the " +
			"walk succeed on a black frame (measured: run 44248083728, assistant = false, published)")
	}
	// a REAL `expect:` line, not the word inside the comment that explains why it was removed
	for _, line := range strings.Split(step, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "check ==") || trimmed == "expect" {
			t.Errorf("the final gate still carries a `expect:` line (%q); it must be an assertion",
				trimmed)
		}
	}
	for _, want := range []string{"select", "country", "region"} {
		if !strings.Contains(step, want) {
			t.Errorf("the final gate no longer looks for %q, so it would fail on a correct screen",
				want)
		}
	}
}

// AND A BLACK FRAME MUST NOT END A LOOK. That regression published a half-installed disk: the
// installer blanks its own screen at "About 21 minutes remaining" and keeps writing for another
// twenty minutes, so a pattern that accepts a black frame ends the look mid-install.
func TestMacos26LooksDoNotEndOnABlackFrame(t *testing.T) {
	raw, err := os.ReadFile(macosStage(t, "26", "setup"))
	if err != nil {
		t.Fatalf("%v", err)
	}
	src := string(raw)

	looks := 0
	for _, line := range strings.Split(src, "\n") {
		if strings.Contains(line, "the copy runs, look") {
			looks++
		}
	}
	if looks == 0 {
		t.Fatal("no looks found; this test is looking at the wrong file")
	}
	// a pattern that can match an empty or near-empty reading
	for _, bad := range []string{"'^$|", "'^.{0,3}$|", "'^.{0,2}$|"} {
		if strings.Contains(src, bad) {
			t.Errorf("a look pattern still accepts a black frame (%s). MEASURED on run 44248083728: "+
				"the install blanks its screen with 21 minutes still to run, so accepting that "+
				"publishes a half-installed disk", bad)
		}
	}
	// every look must still be able to recognise the panes it is waiting for
	if !strings.Contains(src, "select your country or region") {
		t.Error("the look patterns no longer name the Setup Assistant's first pane")
	}
}
