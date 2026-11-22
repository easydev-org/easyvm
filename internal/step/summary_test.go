package step

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// A SUMMARY THAT NAMES A KEY IS A CLAIM THAT KEY IS AN ARGUMENT, AND THIS FILE IS WHAT MAKES IT TRUE.
//
// The vocabulary drifted from its own descriptions in ways nothing could see:
//
//	`boot` said "its options include os, cpu, mem_mb, cpus, share, iso, disk, tpm"
//	                        ^^^^^ and ^^^^ and ^^^ were read by NO executor
//	`disk` said "prepare the file boot: starts from a source: {from, copy, overlay}"
//	                        ^^^^^^^^^^^^^^^^^^^^^^ a form the executor refuses BY NAME
//
// Both read as documentation and both were wrong, and the cost is a reader writing a key that
// `Validate` accepts and the executor never looks at. So a Summary that names a key has to be
// checked against what the implementation reads.
//
// THE RULE: every backticked `name:` in a Summary must be either
//
//	1. read by the executor that runs this key, or
//	2. a key that exists in the vocabulary and is referenced as ANOTHER step's action in prose
//	   ("the disk is the one an earlier `disk:` step named") - which this test allows because
//	   `TestEveryKeyItNamesExists` checks it is a real key, and being an action elsewhere is not a
//	   claim about THIS key's arguments.
//
// A THIRD CASE IS PROSE ABOUT THE PAST, and it is the majority of these: `settle:`'s Summary says it
// absorbed the older `wait:` and `wait_after:`, and `check:`'s says it replaced `ocr:`. Those names
// must NOT be in the vocabulary - that is the point of the sentence - so they are exempt, and the
// exemption is a list rather than a pattern so adding one is a decision.
//
// WHAT IS NOT EXEMPT is a PLAUSIBLE CURRENT KEY that the executor does not read. That is `boot:
// share` exactly: a real word, in a sentence about arguments, that no code looks at.

// retiredNames are keys whose Summary refers to them in the past tense: "was", "became", "replaces".
// A name here is exempt from "must be a key" because the sentence's whole subject is that it is not
// one any more.
var retiredNames = map[string]bool{
	"wait": true, "wait_after": true, "ocr": true, "unless": true, "snapshot": true,
	"panes": true, "asset": true, "build": true, "generate": true,
	"desktop_family": true, "keep_alive": true, "change_settle": true, "change_timeout": true,
}

// summaryNames returns the `name:` references in every Summary, by key.
func summaryNames() map[string][]string {
	out := map[string][]string{}
	for name, key := range Keys {
		out[name] = backtickedNames(key.Summary)
	}
	return out
}

var backtickName = regexp.MustCompile("`([a-z][a-z0-9_]*):`")

func backtickedNames(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range backtickName.FindAllStringSubmatch(s, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// TestEveryNamedArgumentExists is the cheap half: a Summary cannot name a key the vocabulary does
// not have.
//
// A FIELD OF ANY VERB IS A REAL NAME. `check:`'s Summary explains `rect:`, and `rect:` is a field of
// `check:` rather than a verb, so `declared` looks at every table - the point is that the Summary is
// telling the truth about a key a reader will type.
func TestEveryNamedArgumentExists(t *testing.T) {
	for key, names := range summaryNames() {
		for _, n := range names {
			if declared(n) {
				continue
			}
			if retiredNames[n] {
				continue
			}
			t.Errorf("`%s:`'s Summary names `%s:`, which is in neither table. A reader would write "+
				"it and `Validate` would refuse the file", key, n)
		}
	}
}

// TestEveryNamedArgumentIsReadOrIsAnAction is the half that would have caught `boot: share`.
//
// IT NEEDS THE EXECUTOR SOURCE, so it skips rather than fails when run from somewhere the file is
// not reachable - a test that fails for the wrong reason teaches people to ignore it.
func TestEveryNamedArgumentIsReadOrIsAnAction(t *testing.T) {
	src, err := os.ReadFile("../run/executor.go")
	if err != nil {
		t.Skipf("executor source not readable: %v", err)
	}
	executor := string(src)
	// Every key any `do*` body asks for, collected from the whole package rather than one file.
	read := map[string]bool{}
	for _, f := range []string{"../run/executor.go", "../run/actions.go", "../run/ocr.go",
		"../run/text.go", "../run/loop.go", "../run/adb.go", "../run/see.go", "../run/gpu.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, m := range regexp.MustCompile(`s\.(?:Get|Has|Float|Int|Bool|String)\("([a-z_0-9]+)"\)`).
			FindAllStringSubmatch(string(b), -1) {
			read[m[1]] = true
		}
	}
	if len(read) < 40 {
		t.Fatalf("read only %d keys from the executor; the parser broke and this test would pass "+
			"about nothing", len(read))
	}

	var bad []string
	for key, names := range summaryNames() {
		for _, n := range names {
			if n == key || read[n] || retiredNames[n] {
				continue
			}
			// IT MAY STILL BE A DIFFERENT STEP'S ACTION, named in prose. `boot` reading "the disk is
			// the one an earlier `disk:` step named" is a true statement about `disk:`, and `disk`
			// is an action with its own branch.
			if Roles[n] == RoleAction && strings.Contains(executor, `"`+n+`"`) {
				continue
			}
			// A SUB-KEY IS READ FROM A MAPPING, NOT FROM THE STEP: `check:` reads `mode` out of its
			// own value with `raw["mode"]`, so `s.Get("mode")` never appears and never should.
			if Roles[n] == RoleSubkey || Roles[n] == RoleArgument || Roles[n] == RoleModifier {
				continue
			}
			bad = append(bad, key+" -> "+n)
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("these Summaries name a key that no executor reads and that is not an action "+
			"elsewhere: %s. Either the claim is wrong or the executor forgot to read it - and "+
			"`Validate` accepts the key either way, so the file's author cannot tell", strings.Join(bad, ", "))
	}
}
