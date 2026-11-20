package journal

import (
	"fmt"
	"testing"
	"time"
)

// THE THREE READ PATTERNS THE PAGE MAKES, measured at the size a real work directory reaches.
//
// A WORK DIRECTORY IS REUSED ACROSS RUNS. That is the fact the append-only file could not survive:
// its `since` read was O(n) in the whole file, so a pod that had run fifty times scanned fifty runs'
// worth of records every second. These are the numbers that decided the schema.
func TestFrontEndReadPatterns(t *testing.T) {
	dir := t.TempDir()
	j, err := Open(dir, "run-A")
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()

	// FORTY EARLIER RUNS of 40 records, so the table is the size of a working pod.
	for r := 0; r < 40; r++ {
		run := fmt.Sprintf("old-%d", r)
		for i := 0; i < 40; i++ {
			_, _ = j.Write(Record{RunID: run, Step: fmt.Sprintf("s%d", i), StepIndex: i,
				StepCount: 40, Kind: KindCheck, Status: StatusPassed})
		}
		// AND THE EVENTS BENEATH THEM: a run makes thousands of external calls, so this is where the
		// real row count lives.
		for i := 0; i < 300; i++ {
			_ = j.Event(Event{RunID: run, Step: fmt.Sprintf("s%d", i/10), StepIndex: i / 10,
				Kind: CallInput, Detail: "key a"})
		}
	}
	// AND THE RUN BEING WATCHED: 40 steps, 4000 calls - a forty-minute stage with a one-a-second
	// recorder plus typed text.
	for i := 0; i < 40; i++ {
		_, _ = j.Write(Record{RunID: "run-A", Step: fmt.Sprintf("step %d", i), StepIndex: i,
			StepCount: 40, Kind: KindCheck, Status: StatusPassed, Mode: "like", Want: "0.78..1.00",
			Images: []Image{{Rel: "journal/a.png", Label: "the frame", W: 1920, H: 1080}}})
		for k := 0; k < 100; k++ {
			_ = j.Event(Event{RunID: "run-A", Step: fmt.Sprintf("step %d", i), StepIndex: i,
				Kind: CallInput, Detail: "key x"})
		}
	}

	var recs, evts int
	row := j.sql.QueryRow(`SELECT COUNT(*) FROM record`)
	_ = row.Scan(&recs)
	row = j.sql.QueryRow(`SELECT COUNT(*) FROM event`)
	_ = row.Scan(&evts)
	t.Logf("tables: %d records, %d events", recs, evts)

	// 1. THE OVERVIEW: one run's steps, in order.
	start := time.Now()
	for i := 0; i < 20; i++ {
		if _, err := j.Steps("run-A", 0); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("OVERVIEW   every step of the live run      : %s", time.Since(start)/20)

	// 2. POLLING: what is new since the reader last looked.
	start = time.Now()
	for i := 0; i < 20; i++ {
		if _, err := j.Steps("run-A", 39); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("POLL       records after seq 39            : %s", time.Since(start)/20)

	// 2b. ONE STEP with everything - the detail view's record read.
	start = time.Now()
	for i := 0; i < 20; i++ {
		if _, err := j.Record("run-A", 18); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("ONE STEP   its record, with its pictures   : %s", time.Since(start)/20)

	// 3. THE DETAIL VIEW: one step's external calls.
	start = time.Now()
	for i := 0; i < 20; i++ {
		if _, err := j.Events("run-A", 17, ""); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("DETAIL     one step's calls                : %s", time.Since(start)/20)

	// 4. THE AUDIT: every click in the run, across all steps.
	start = time.Now()
	for i := 0; i < 20; i++ {
		if _, err := j.Events("run-A", -1, CallInput); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("AUDIT      every input of the whole run    : %s", time.Since(start)/20)

	// 5. THE SUMMARY: counts by kind, slowest, failures - all in SQL.
	start = time.Now()
	for i := 0; i < 20; i++ {
		if _, err := j.Summary("run-A"); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("SUMMARY    counts + failures + slowest     : %s", time.Since(start)/20)
}
