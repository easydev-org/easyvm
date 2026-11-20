package run

import (
	"testing"

	"github.com/easydev-org/easyvm/internal/walk"
)

// `every:` IS A CLOCK, AND THESE TESTS ARE THE REASON IT EXISTS.
//
// Measured across easydev-org/easyvm runs 44150638949, 44190274785 and 44227587443: a macOS 26
// install writes its disk and then draws a frame that means nothing to a walkthrough - black on the
// CI runner (`0 word(s)`), OpenCore's boot picker on another host (OCR noise, `@® cos inner en
// poset nvFan ...`). Every screen-based stop test I wrote for it was a guess:
//
//	says_any on the Setup Assistant panes   -> 0 word(s) matches none of them
//	matches: '^$'                            -> catches 0 words, misses the OCR noise
//	matches: '^.{0,3}$'                      -> catches both, and is still a guess about a frame
//	                                            nobody has measured on the host that runs it
//
// A loop that can also end on the clock does not need to recognise that frame at all.

// A CLOCK WITH NOTHING TO DO IS REFUSED, because it would spin without changing anything.
func TestEveryWithoutOnIsRefused(t *testing.T) {
	s := stepWith(t, map[string]any{
		"loop": map[string]any{
			"max":   3,
			"do":    []any{map[string]any{"wait": 1}},
			"every": 60,
			"see":   map[string]any{"name": "s"},
			// `mean_between` RATHER THAN `text`: this test is about the CLOCK, and an OCR read of a
			// 1920x1080 frame costs seconds per attempt for a word that is never there.
			"check": map[string]any{"name": "d", "from": "s", "mode": "mean_between",
				"rect": []any{0, 0, 1920, 1080}, "rect_at": []any{1920, 1080}, "range": []any{999, 1000}},
			"until": map[string]any{"fact": "d"},
		},
	})
	ex := &Executor{Work: t.TempDir()}
	err := ex.doLoop(s)
	if err == nil {
		t.Fatal("`every:` with no `on:` must be refused; a clock with nothing to do is a loop " +
			"that never ends")
	}
}

// AND AN ACTION WITH NO CLOCK IS REFUSED THE OTHER WAY, so a typo cannot silently drop the clock.
func TestOnWithoutEveryIsRefused(t *testing.T) {
	s := stepWith(t, map[string]any{
		"loop": map[string]any{
			"max":   3,
			"do":    []any{map[string]any{"wait": 1}},
			"clock": []any{map[string]any{"wait": 1}},
			"see":   map[string]any{"name": "s"},
			// `mean_between` RATHER THAN `text`: this test is about the CLOCK, and an OCR read of a
			// 1920x1080 frame costs seconds per attempt for a word that is never there.
			"check": map[string]any{"name": "d", "from": "s", "mode": "mean_between",
				"rect": []any{0, 0, 1920, 1080}, "rect_at": []any{1920, 1080}, "range": []any{999, 1000}},
			"until": map[string]any{"fact": "d"},
		},
	})
	ex := &Executor{Work: t.TempDir()}
	if err := ex.doLoop(s); err == nil {
		t.Fatal("`on:` without `every:` must be refused: it says what to do but not when")
	}
}

// A CLOCK THAT HAS NOT RUN OUT DOES NOTHING, so an ordinary loop is unaffected by carrying one.
func TestAClockThatHasNotRunOutDoesNothing(t *testing.T) {
	s := clockStep(3600) // an hour: this test finishes in milliseconds
	drv := &fakeDriver{}
	ex := &Executor{Driver: drv, Work: t.TempDir(), File: &walk.File{SSHUser: "vmuser"}}
	ex.seen = map[string][]string{}
	// the reading never satisfies the condition, so the loop runs to its ceiling -
	// what matters is that the `on:` action did NOT run.
	_ = ex.doLoop(s)
	if n := countOn(drv.clicks); n != 0 {
		t.Fatalf("the `on:` action ran %d time(s) before its clock ran out", n)
	}
}

// AND WHEN THE CLOCK HAS RUN OUT IT RUNS ONCE, not once per attempt.
func TestTheClockRunsItsActionOnce(t *testing.T) {
	s := clockStep(0.0001) // effectively already expired
	drv := &fakeDriver{}
	ex := &Executor{Driver: drv, Work: t.TempDir(), File: &walk.File{SSHUser: "vmuser"}}
	ex.seen = map[string][]string{}
	_ = ex.doLoop(s)
	if n := countOn(drv.clicks); n != 1 {
		t.Fatalf("the `on:` action ran %d time(s); it must run ONCE, or a loop that resets "+
			"the guest would reset it on every attempt", n)
	}
}

// clockStep builds a loop whose `on:` action is a `wait:` the fake driver can count.
func clockStep(every float64) walk.Step {
	return walk.Step{Index: 1, Name: "probe", Spec: map[string]any{
		"loop": map[string]any{
			"max":   2,
			"every": every,
			"clock": []any{map[string]any{"click": []any{731, 547}}},
			"do":    []any{map[string]any{"click": []any{1, 2}}},
			"see":   map[string]any{"name": "s"},
			// a condition that is never true, so the loop runs to its ceiling
			// `mean_between` RATHER THAN `text`: this test is about the CLOCK, and an OCR read of a
			// 1920x1080 frame costs seconds per attempt for a word that is never there.
			"check": map[string]any{"name": "d", "from": "s", "mode": "mean_between",
				"rect": []any{0, 0, 1920, 1080}, "rect_at": []any{1920, 1080}, "range": []any{999, 1000}},
			"until": map[string]any{"fact": "d"},
		},
	}}
}

// countOn counts the clicks only the `on:` action makes, so the loop's own `do:` clicks do not
// hide a clock that fired too often.
func countOn(clicks [][2]int) int {
	n := 0
	for _, c := range clicks {
		if c == [2]int{731, 547} {
			n++
		}
	}
	return n
}
