package report

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/easydev-org/easyvm/internal/artifact"
	"github.com/easydev-org/easyvm/internal/journal"
)

// THE WHOLE CHAIN, FROM A RUN'S ROWS TO A PAGE THAT OPENS WITHOUT A SERVER.
//
// THIS IS THE TEST THE FEATURE EXISTS FOR. Everything before it is plumbing: a journal writes rows, a
// library stores pictures, and this is the only place where the two become something a person can
// read. A report that renders an empty page would pass every test that only checked the store.
func TestBuildWritesAPageWithItsEvidence(t *testing.T) {
	work := t.TempDir()

	// A PICTURE IN THE LIBRARY, put there the way a `see:` puts one there.
	src := filepath.Join(work, "frame.png")
	if err := os.WriteFile(src, tinyPNG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	lib, err := artifact.Open(filepath.Join(work, "artifact"))
	if err != nil {
		t.Fatal(err)
	}
	sha, err := lib.Put(src)
	if err != nil {
		t.Fatal(err)
	}

	db, err := journal.Open(work, "2026-11-20T10-30-21")
	if err != nil {
		t.Fatal(err)
	}

	// THE STEPS: one that passed, one check that FAILED with the numbers and pictures a real one
	// carries, and one optional failure - the three states a reader has to tell apart.
	f := 0.7093
	yes := true
	no := false
	if _, err := db.Write(journal.Record{
		Step: "boot the disk", StepIndex: 1, StepCount: 3, Kind: journal.KindStep,
		Status: journal.StatusPassed, DurationMS: 12400,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Write(journal.Record{
		Step: "the account form is up", StepIndex: 2, StepCount: 3, Kind: journal.KindCheck,
		Status: journal.StatusFailed, DurationMS: 47000,
		Message: "the account form is up: ready must be true and is false: 0.7093 similar, wanted 0.7800..1.0000",
		Mode:    "like", Want: "0.7800..1.0000", Got: &f, OK: &no,
		Images: []journal.Image{
			{Sha: sha, Name: "account_form", Label: "see"},
			{Rel: "journal/the-account-form-1-frame.png", Label: "the frame",
				Rect: []int{700, 300, 1100, 420}},
			{Rel: "journal/the-account-form-1-diff.png", Label: "the difference"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Write(journal.Record{
		Step: "open a terminal", StepIndex: 3, StepCount: 3, Kind: journal.KindRun,
		Status: journal.StatusOptional, DurationMS: 300,
		Message: "click (960, 540): the qmp connection is closed",
		Command: []string{"sudo dnf install -y openssh-server"},
	}); err != nil {
		t.Fatal(err)
	}
	_ = yes

	// THE CALLS BENEATH THE FAILED STEP, including one that failed.
	if err := db.Event(journal.Event{
		Step: "the account form is up", StepIndex: 2, Kind: journal.CallCompare,
		Detail: "like 0.7093", OK: &no, DurationMS: 12,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Event(journal.Event{
		Step: "the account form is up", StepIndex: 2, Kind: journal.CallClick,
		Detail: "1413,193", OK: &no, Error: "the qmp connection is closed", DurationMS: 10001,
	}); err != nil {
		t.Fatal(err)
	}
	// AND THE LIBRARY ROW, so the page has a picture to list by name.
	if err := db.Image(journal.Image{Name: "account_form", Sha: sha, Label: "see",
		W: 1, H: 1}, "the account form is up", 2); err != nil {
		t.Fatal(err)
	}
	db.Close()

	// A CROP, for the evidence directory - the file `check:` wrote beside the frame.
	if err := os.MkdirAll(filepath.Join(work, "journal"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"the-account-form-1-frame.png", "the-account-form-1-diff.png"} {
		if err := os.WriteFile(filepath.Join(work, "journal", n), tinyPNG(t), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	out := filepath.Join(work, "report")
	w, err := Build(Options{Work: work, Out: out})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if w.Steps != 3 {
		t.Errorf("reported %d steps, want 3", w.Steps)
	}
	if w.Events != 2 {
		t.Errorf("reported %d events, want 2", w.Events)
	}
	if w.Pictures != 1 {
		t.Errorf("copied %d pictures, want 1", w.Pictures)
	}

	page, err := os.ReadFile(w.Index)
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)

	// WHAT THE PAGE MUST SAY: every step, its verdict, the check's numbers, and the three pictures.
	for _, want := range []string{
		"boot the disk",
		"the account form is up",
		"open a terminal",
		"0.7093",                         // the measured value
		"0.7800..1.0000",                 // the threshold
		"did not match",                  // the verdict
		"assets/" + sha + ".png",         // the library picture
		"evidence/the-account-form-1-frame.png", // the crop
		"evidence/the-account-form-1-diff.png",  // the diff
		"t-optional",                     // the optional failure is its own state
		"the qmp connection is closed",   // the call's error
		"sudo dnf install -y openssh-server", // the command the step ran
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the page does not mention %q", want)
		}
	}

	// AND THE FILES IT POINTS AT MUST EXIST, or the page is a set of broken links.
	for _, rel := range []string{"assets/" + sha + ".png",
		"evidence/the-account-form-1-frame.png", "evidence/the-account-form-1-diff.png"} {
		if _, err := os.Stat(filepath.Join(out, rel)); err != nil {
			t.Errorf("the page points at %s, which was not written: %v", rel, err)
		}
	}
}

// tinyPNG is a 1x1 PNG, so a test can write a real picture without a fixture file.
func tinyPNG(t *testing.T) []byte {
	t.Helper()
	const b64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	data, err := decodeBase64(b64)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// decodeBase64 is `encoding/base64` behind a name, so the fixture reads as a picture.
func decodeBase64(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}
