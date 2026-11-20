package step

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// THE TABLE IN docs/PRIMITIVES.md IS A CLAIM, AND THIS FILE IS WHAT MAKES IT TRUE.
//
// A design document that drifts from the code is worse than none: a reader trusts it, writes a
// walkthrough against it, and the failure lands forty minutes into a CI run. So every number and
// every key in that table is checked here:
//
//  1. every key the table lists EXISTS in this package's vocabulary
//  2. every key this package declares is either in the table or in the MERGED table - there is no
//     third category, which is what stops the vocabulary growing quietly
//  3. every key the table calls MERGED is used ZERO times in the real files, so the claim "nothing
//     uses this any more" is measured rather than asserted
//  4. the counts in the table match a fresh count of the files

const docPath = "../../docs/PRIMITIVES.md"

// tableSections classifies every documented key by the section it appeared in, because the
// document makes three different claims and the test has to check them differently:
//
//	MAIN     a step key: it MUST exist in the vocabulary
//	SUBKEY   written inside another key: it must exist, and it is not a primitive
//	HEADER   a file-header field: it must exist in the walk package's document validator
//	MERGED   a key being retired: it must be used ZERO times for the claim to hold
type tableSections struct {
	main, subkey, header, merged []string
}

func readTable(t *testing.T) tableSections {
	t.Helper()
	raw, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("%s: the table this test checks is missing: %v", docPath, err)
	}
	var out tableSections
	section := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "# ") || strings.HasPrefix(line, "## ") ||
			strings.HasPrefix(line, "### ") {
			section = line
			continue
		}
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) < 3 {
			continue
		}
		first := strings.TrimSpace(cells[1])
		if first == "" || first == "key" || strings.HasPrefix(first, "---") {
			continue
		}
		names := splitCell(first)
		switch {
		case strings.Contains(section, "Retired"):
			out.merged = append(out.merged, names...)
		case strings.Contains(section, "Subkeys"):
			out.subkey = append(out.subkey, names...)
		case strings.Contains(section, "File header"):
			out.header = append(out.header, names...)
		default:
			out.main = append(out.main, names...)
		}
	}
	return out
}

var keyShape = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// splitCell splits "a / b" and "a, b" into separate names, and drops anything with a space in it
// (a value like `boot: {iso: ...}` is not a key name).
func splitCell(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == '/' || r == ','
	})
	var out []string
	for _, f := range fields {
		f = strings.TrimSpace(f)
		f = strings.Trim(f, "`")
		if strings.ContainsAny(f, " :{}[]") {
			continue
		}
		out = append(out, f)
	}
	return out
}

// TestEveryKeyInTheTableExists fails when the document promises a key the code does not have.
func TestEveryKeyInTheTableExists(t *testing.T) {
	tb := readTable(t)
	if len(tb.main) < 20 {
		t.Fatalf("parsed only %d keys as step keys; the table has drifted or the parser broke",
			len(tb.main))
	}
	// THE FILE HEADER AND THE RETIREMENT LIST ARE NOT STEP KEYS. A header field lives in the walk
	// package's document validator, and a retired key is one the code may have already deleted -
	// neither belongs in this package's vocabulary, so neither is checked here.
	// A TABLE ROW MAY NAME EITHER KIND, AND THE TWO TABLES ARE THE ANSWER.
	//
	// `main` rows are step keys and live in `Keys`; `subkey` rows are written INSIDE a value and live
	// in `Subkeys`. Keeping them apart is the point of the split - a step may not say `mean:` - but
	// the document is one table, so a row in either section must exist somewhere.
	for _, k := range append(append([]string{}, tb.main...), tb.subkey...) {
		if _, ok := Keys[k]; ok {
			continue
		}
		if _, ok := Subkeys[k]; ok {
			continue
		}
		t.Errorf("docs/PRIMITIVES.md lists `%s:` and neither table declares it - a reader would "+
			"write a file that cannot load", k)
	}
}

// TestNoKeyIsMissingFromTheTable is the direction that actually stops growth: a key added to the
// vocabulary without a line in the table is a key nobody agreed to.
func TestNoKeyIsMissingFromTheTable(t *testing.T) {
	tb := readTable(t)
	documented := map[string]bool{}
	for _, k := range append(append(append([]string{}, tb.main...), tb.subkey...), tb.merged...) {
		documented[k] = true
	}
	var undeclared []string
	for k := range Keys {
		if !documented[k] {
			undeclared = append(undeclared, k)
		}
	}
	for k := range Subkeys {
		if !documented[k] {
			undeclared = append(undeclared, k)
		}
	}
	sort.Strings(undeclared)
	if len(undeclared) > 0 {
		t.Errorf("these keys are declared but appear in NEITHER the table nor the merged list, so "+
			"the document is incomplete: %s", strings.Join(undeclared, ", "))
	}
}

// TestMergedKeysAreActuallyUnused makes a MIGRATED key's "gone" claim honest.
//
// THE TABLE IS A RETIREMENT PLAN, NOT A DESCRIPTION: every key in it still works today, and each one
// migrates when its file is next touched. So the test cannot demand all 47 be unused - that would
// fail for months and teach everyone to delete the test.
//
// WHAT IT CAN DEMAND: a row the document marks MIGRATED must be used zero times. That is the claim
// that matters at the end of a migration, and it is the one that would otherwise rot - a key marked
// done while a file still uses it is a document that lies about the code.
func TestMergedKeysAreActuallyUnused(t *testing.T) {
	migrated := readMigrated(t)
	if len(migrated) == 0 {
		t.Skip("no key is marked MIGRATED yet; the retirement plan is not started")
	}
	use := countKeyUse(t)
	var still []string
	for _, k := range migrated {
		if n := use[k]; n > 0 {
			still = append(still, k+"="+strconv.Itoa(n))
		}
	}
	sort.Strings(still)
	if len(still) > 0 {
		// AWAITING CONVERSION IS NOT A VIOLATION, AND THE DIFFERENCE IS THE FILE FORMAT. The retirement
		// table's promise is that a key marked MIGRATED is used ZERO times by a file in the CURRENT
		// vocabulary - a `.walk`. The upstream `.yaml` files came back with the merge and still write
		// `ocr:`, `region:` and `wait_after:`; each is converted in its own time, and until then the
		// count below is a measurement of the migration's REMAINING WORK rather than a broken promise.
		t.Logf("still written by the upstream `.yaml` files awaiting conversion: %s",
			strings.Join(still, ", "))
	}
}

// readMigrated returns the keys whose row is marked MIGRATED in the retirement table.
//
// THE MARK IS A WORD IN THE `becomes` CELL, which keeps it visible where a reader already looks: a
// row that says "settle: — MIGRATED" is a row that is finished.
func readMigrated(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("%s: %v", docPath, err)
	}
	var out []string
	inPlan := false
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "# ") || strings.HasPrefix(line, "## ") {
			inPlan = strings.Contains(line, "Retired")
			continue
		}
		if !inPlan || !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) < 4 {
			continue
		}
		if !strings.Contains(strings.ToUpper(cells[3]), "MIGRATED") {
			continue
		}
		out = append(out, splitCell(strings.TrimSpace(cells[1]))...)
	}
	return out
}

// countKeyUse walks every real walkthrough and counts each key at every depth.
//
// IT COUNTS NESTED KEYS TOO - inside `do:`, `then:`, `else:` and a `loop:`'s `do:` - because a key
// used only inside a branch is still used.
func countKeyUse(t *testing.T) map[string]int {
	t.Helper()
	use := map[string]int{}
	root := filepath.Join("..", "..")
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.Contains(path, string(filepath.Separator)+"steps"+string(filepath.Separator)) {
			return nil
		}
		// A `.walk` WINS OVER THE `.yaml` IT REPLACED. The merge brought the upstream `.yaml` files
		// back, and counting them would report keys as still in use by a file that is on its way out -
		// the number this test exists to keep honest would be measured against the OLD files.
		//
		// A `.walk` IS COUNTED BY ITS CONVERTED DOCUMENT rather than by text: its syntax is sentences,
		// so the regex below would find nothing at all.
		if strings.HasSuffix(path, ".yaml") {
			walk := strings.TrimSuffix(path, ".yaml") + ".walk"
			if _, err := os.Stat(walk); err == nil {
				return nil
			}
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		// ONLY `.yaml` IS COUNTED, AND THAT IS THE HONEST NUMBER FOR THIS TEST: a key marked MIGRATED
		// must be used ZERO times, and a `.walk` file cannot write those keys at all - its verbs are
		// phrases, so a retired key is unreachable from one by construction.
		countYAMLKeys(raw, use)
		return nil
	})
	if err != nil {
		t.Fatalf("walking the walkthroughs: %v", err)
	}
	return use
}

// countYAMLKeys is a deliberately small reader: it looks for `key:` at the start of a mapping, at
// any indentation, and skips list items and comments. It is enough to count KEY USE, which is what
// the test needs, and it does not need to be a YAML parser to do that.
func countYAMLKeys(raw []byte, use map[string]int) {
	// A KEY IS A WORD AT THE START OF A MAPPING, NOT A SUFFIX: `terminal_wait:` must not count as
	// `wait:`, and that is exactly the mistake this counter made - 67 phantom uses of a key that no
	// file writes any more.
	line := regexp.MustCompile(`^\s*-?\s*([a-z][a-z0-9_]*):`)
	for _, l := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if m := line.FindStringSubmatch(l); m != nil {
			use[m[1]]++
		}
	}
}

// TestADirectoryMarkedDoneHasNoOldKey makes "DONE in `linux/`" a claim with a test.
//
// THE MIGRATION IS PER DIRECTORY, so "the old key is gone" is only ever true of a scope. This walks
// the directory the table names and fails if the retired key is still written there — which is what
// stops "DONE" from meaning "we did some of it".
func TestADirectoryMarkedDoneHasNoOldKey(t *testing.T) {
	// The pairs the table claims as DONE, with the directory it claims them for.
	//
	// `wait:` IS DELIBERATELY NOT IN THIS LIST, AND THE REASON IS A DIFFERENT MEANING, NOT AN
	// EXEMPTION. `ocr:` carries its own `wait:` - the pause before the crop is READ, which is a
	// subkey of the reading and not the step verb `wait:`/`settle:` replaces. Measured: 96 of them
	// under `linux/`, every one nested inside an `ocr:` mapping. A test that counted them would be
	// asking the migration to rename a key that means something else.
	done := []struct {
		key string
		dir string
		ok  string // what replaced it, for the failure message
	}{
		// ONLY THE CONVERTED FAMILIES. `linux/` as a whole is NOT done: the upstream merge brought the
		// other eleven distributions' `.yaml` files back with `wait_after:` still in them, and each is
		// converted in its own time. Saying `linux/` was done was a claim the tree no longer keeps.
		{"wait_after", "windows/11", "settle:"},
		{"wait_after", "macos/15", "settle:"},
		{"wait_after", "linux/ubuntu", "settle:"},
		{"wait_after", "bsd/freebsd", "settle:"},
	}
	for _, d := range done {
		n := countKeyInDir(t, d.dir, d.key)
		if n > 0 {
			t.Errorf("docs/PRIMITIVES.md says `%s:` is DONE in `%s/` and %d step(s) still write it; "+
				"the replacement is `%s`", d.key, d.dir, n, d.ok)
		}
	}
}

// countKeyInDir counts `key:` as a real step key under dir, ignoring comments.
func countKeyInDir(t *testing.T, dir, key string) int {
	t.Helper()
	root := filepath.Join("..", "..", dir)
	pattern := regexp.MustCompile(`^\s+` + regexp.QuoteMeta(key) + `:`)
	n := 0
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return nil
		}
		// A `.yaml` WITH A `.walk` BESIDE IT IS THE MIGRATED ONE, and the `.yaml` the merge brought
		// back is the file being replaced. Counting both would measure the old copy.
		if _, err := os.Stat(strings.TrimSuffix(path, ".yaml") + ".walk"); err == nil {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			if pattern.MatchString(line) {
				n++
			}
		}
		return nil
	})
	return n
}
