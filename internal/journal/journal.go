// Package journal is the RECORD of what a walkthrough did - one row per step, and one row per
// external call - with the pictures a judgement was made from.
//
// # WHY THIS EXISTS, AND WHY IT IS NOT THE LOG
//
// The log was the only record, and `Driver.Log` is `fmt.Println`: it goes to the container's stdout,
// it is unstructured, and a reader has to grep it. MEASURED on a failed fedora run: the useful
// evidence was in the recording's frames, and the frames were numbered files nobody could map back
// to a step.
//
// A step that DECIDES produced the most valuable evidence of all and kept none of it: `check: mode:
// like` cropped a reference, cropped the frame, compared them, got 0.7093, and printed
//
//	the create-account form is up: ready = false (0.7097 similar, wanted 0.7800..1.0000)
//
// - one number and a range. The TWO PICTURES THE NUMBER CAME FROM were local variables and were
// thrown away when the function returned, so "what did it actually see" had no answer.
//
// # WHY SQLITE AND NOT AN APPEND-ONLY FILE
//
// THE FIRST VERSION OF THIS PACKAGE WAS A JSONL FILE, AND IT WAS MEASURED RATHER THAN ARGUED ABOUT.
// Two facts settled it:
//
//  1. A RUN PRODUCES THOUSANDS OF EXTERNAL CALLS, NOT DOZENS. `type:` sends one `input-send-event`
//     per character, the recorder takes a `screendump` every second, and a forty-minute stage is
//     over two thousand captures on its own. An audit of "every click, every keystroke, every
//     capture" is ten thousand rows for ONE run - and /work is reused across runs in a pod, so the
//     file accumulates.
//
//  2. A `since` READ OF A JSONL FILE IS O(n). MEASURED: 0.75ms at 250 records, 44ms at 20,000,
//     225ms at 100,000. A page polling once a second would scan the whole history every second, and
//     the cost grows with every run that has ever happened in this work directory.
//
// An index makes all three read patterns cost what their RESULT costs instead of what the table
// costs. That is the whole reason for the dependency.
//
// # IT IS PURE GO
//
// `modernc.org/sqlite` is a translation of SQLite to Go, with no cgo. That matters here for a
// concrete reason: the image is built with `CGO_ENABLED=0`, and `mattn/go-sqlite3` would need a
// toolchain at build time and a libc at run time.
//
// # WHAT IS ON DISK
//
// `journal.db` holds the rows. The PICTURES stay files, because a database is a poor place for a
// megabyte of PNG and `/api/artifact` already serves files from the work directory. A row names its
// picture by a path RELATIVE to the work directory, and the picture is COPIED into `journal/` under
// a name that includes the step's ordinal - a frame in `frames/` is overwritten by the next capture
// a second later, so a record pointing there would show a different picture every time it was read.
package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Status is how a step ended.
type Status string

const (
	// StatusRunning is written when a step BEGINS, so a reader following a long step sees it.
	StatusRunning Status = "running"
	// StatusPassed is a step that returned no error.
	StatusPassed Status = "passed"
	// StatusFailed is a step that failed and stopped the walk.
	StatusFailed Status = "failed"
	// StatusOptional is a step that failed and the file said may be passed over. IT IS ITS OWN
	// STATUS rather than "passed" with a note: a stage that quietly stepped over a failure is
	// exactly the thing this journal is meant to make visible.
	StatusOptional Status = "optional"
	// StatusSkipped is a step whose `when:`/`unless:` did not hold, so it did not run.
	StatusSkipped Status = "skipped"
)

// Kind names what sort of evidence a Record carries, so a reader can render it without guessing
// from whichever fields happen to be set.
type Kind string

const (
	// KindStep is an ordinary step: an action, a wait.
	KindStep Kind = "step"
	// KindCheck is a `check:` - it decided a boolean, and holds the comparison it decided from.
	KindCheck Kind = "check"
	// KindSee is a `see:` capture.
	KindSee Kind = "see"
	// KindRun is a `run:` typed into a terminal.
	KindRun Kind = "run"
	// KindSSH is an `ssh:` command.
	KindSSH Kind = "ssh"
	// KindLoop is a `loop:`/`until:` wait: it holds every attempt it made.
	KindLoop Kind = "loop"
)

// CallKind is what sort of external call an Event recorded.
//
// THESE ARE THE AUDIT GRANULARITY, and they are deliberately finer than a step: a step is "type the
// password", and the events beneath it are the individual keystrokes, the frame captures and the
// comparisons. An audit that only kept steps could not answer "was anything typed while the wizard
// was closed", which is the question this repository has had to answer more than once.
type CallKind string

const (
	// CallInput is one `input-send-event`: a keystroke or a pointer motion/press.
	CallInput CallKind = "input"
	// CallClick is a pointer press at a pixel.
	CallClick CallKind = "click"
	// CallType is one character typed.
	CallType CallKind = "type"
	// CallKey is one key, with its modifiers.
	CallKey CallKind = "key"
	// CallScreendump is one screen capture.
	CallScreendump CallKind = "screendump"
	// CallSSH is one command run in the guest over ssh.
	CallSSH CallKind = "ssh"
	// CallSCP is one file copy.
	CallSCP CallKind = "scp"
	// CallQGA is one guest-agent command.
	CallQGA CallKind = "qga"
	// CallCompare is one image comparison.
	CallCompare CallKind = "compare"
	// CallOCR is one text read.
	CallOCR CallKind = "ocr"
	// CallQMP is any other QMP command (reset, powerdown, query).
	CallQMP CallKind = "qmp"
)

// Origin says WHO asked for a call: the walkthrough, a person through the HTTP API, or the daemon
// itself.
//
// THE DISTINCTION IS THE POINT OF THE WHOLE PAGE. A run of a `.walk` file and a person clicking the
// console produce the SAME events - a click at 1413,193 is a click at 1413,193 - and a record that
// could not tell them apart makes "why did the wizard advance" unanswerable. MEASURED: before this
// field existed, `/api/click` went straight to QMP and was not recorded AT ALL, so a person's own
// clicks were invisible in the record of their own run.
type Origin string

const (
	// OriginWalk is a call the walkthrough file asked for.
	OriginWalk Origin = "walk"
	// OriginAPI is a call a person made through the HTTP surface - the console page, `curl`, a
	// script. It is not part of the file, and a run reproduced from the file will not do it.
	OriginAPI Origin = "api"
	// OriginSystem is the daemon acting on its own: a boot, a shutdown, an error it handled.
	OriginSystem Origin = "system"
)

// Image is one picture a step captured or compared.
//
// THERE ARE TWO WAYS TO NAME A PICTURE AND THEY MEAN DIFFERENT THINGS:
//
//	Sha   a CONTENT ADDRESS in the artifact library - immutable, deduplicated, published with the run
//	Rel   a path in the work directory - a transient artifact of the moment, served but not kept
//
// A `see:`/`screenshot:` picture gets a Sha and a Name. A `check:`'s crops get a Rel, because they
// exist to explain one comparison and are not part of the library a person browses.
type Image struct {
	// Sha is the content address, when this picture is in the artifact library. It is what
	// `/api/artifact?h=` serves, and it never goes stale.
	Sha string `json:"sha,omitempty"`
	// Name is what the walkthrough called it: the `see:` name, or the `screenshot:` path's base.
	Name string `json:"name,omitempty"`
	// Rel is the path relative to the work directory, slash-separated, for `/api/artifact?p=`.
	Rel string `json:"rel,omitempty"`
	// Label is what the picture is - "the frame", "the reference", "the difference".
	Label string `json:"label"`
	// W and H are its pixel size, 0 when not measured.
	W int `json:"w,omitempty"`
	H int `json:"h,omitempty"`
	// Rect is the region of it that was measured, when only part was.
	Rect []int `json:"rect,omitempty"`
	// Note is one line about it - the reference's own path, or why it is here.
	Note string `json:"note,omitempty"`
}

// Attempt is one pass of a `loop:` or a retrying wait.
//
// THE ATTEMPTS ARE THE POINT OF A LOOP RECORD. A loop that ran eight times and never held looks, in
// the log, like eight identical lines; what a reader needs is WHICH ONE was closest and what it saw.
type Attempt struct {
	// N is 1-based.
	N int `json:"n"`
	// Value is the measured number, when the condition was a threshold.
	Value float64 `json:"value,omitempty"`
	// Read is what the condition read - the words, or the fact's value.
	Read string `json:"read,omitempty"`
	// Held is whether this attempt satisfied the condition.
	Held bool `json:"held"`
	// Image is the frame this attempt looked at.
	Image *Image `json:"image,omitempty"`
}

// Record is one step of the journal.
type Record struct {
	// Seq is 1-based and strictly increasing within a run, so a reader can ask for what it has not
	// seen.
	Seq int `json:"seq"`
	// RunID groups the records of one run. A work directory is reused, so "the steps of THIS run"
	// is a question a reader has to be able to ask.
	RunID string `json:"run_id"`
	// Time is when the record was written, RFC3339 with milliseconds.
	Time string `json:"time"`
	// Step is the step's name, `StepIndex`/`StepCount` place it.
	Step      string `json:"step"`
	StepIndex int    `json:"step_index"`
	StepCount int    `json:"step_count"`
	// Kind says what sort of evidence this record carries.
	Kind Kind `json:"kind"`
	// Status is how it ended. A record written at the START of a step says `running`.
	Status Status `json:"status"`
	// DurationMS is how long the step took, absent while running.
	DurationMS int64 `json:"duration_ms,omitempty"`
	// Message is the step's own sentence - the error when it failed.
	Message string `json:"message,omitempty"`

	// --- WHAT THE STEP MEASURED --------------------------------------------------------
	//
	// THESE ARE OMITTED WHEN NOT APPLICABLE rather than zeroed, so a reader can tell "the similarity
	// was 0.000" from "this step did not compare a picture".

	// Mode is the `check:` mode: like, text, mean_between, ...
	Mode string `json:"mode,omitempty"`
	// Want is the threshold or accepted range, as the file wrote it.
	Want string `json:"want,omitempty"`
	// Got is the actual value, as a number when it is one.
	Got *float64 `json:"got,omitempty"`
	// OK is the boolean a `check:` produced.
	OK *bool `json:"ok,omitempty"`
	// Words is what an OCR read produced, in the reading's own order.
	Words []string `json:"words,omitempty"`
	// Missing names the words a text condition required and did not find.
	Missing []string `json:"missing,omitempty"`
	// Command is a `run:`/`ssh:` body, one entry per line.
	Command []string `json:"command,omitempty"`
	// Images are the pictures this step captured or compared.
	Images []Image `json:"images,omitempty"`
	// Attempts are a loop's passes.
	Attempts []Attempt `json:"attempts,omitempty"`
	// Facts are the booleans this step read or bound, by name.
	Facts map[string]bool `json:"facts,omitempty"`
}

// Event is one external call: the audit trail beneath the steps.
type Event struct {
	// Seq is 1-based and strictly increasing within a run.
	Seq int `json:"seq"`
	// RunID groups a run's events.
	RunID string `json:"run_id"`
	// Time is when the call was made, RFC3339 with milliseconds.
	Time string `json:"time"`
	// Step and StepIndex place the call under the step that made it, so a step's detail view is one
	// indexed read.
	Step      string `json:"step"`
	StepIndex int    `json:"step_index"`
	// Kind is what sort of call it was.
	Kind CallKind `json:"kind"`
	// Origin is who asked for it: the walkthrough, a person, or the daemon.
	//
	// A COLUMN RATHER THAN A GUESS FROM `Step`: an HTTP click made while a walk is running has a
	// step, because the walk is where the machine is, and that is exactly the case where a reader
	// must be able to tell "the file did this" from "somebody did this to the file's machine".
	Origin Origin `json:"origin"`
	// Detail is the call's own text: the key name, the command, the pixel pair.
	Detail string `json:"detail,omitempty"`
	// OK is whether it worked. Nil means "not a call that can fail".
	OK *bool `json:"ok,omitempty"`
	// Error is why it failed, when it did.
	Error string `json:"error,omitempty"`
	// DurationMS is how long the call took, when measured.
	DurationMS int64 `json:"duration_ms,omitempty"`
	// Rel is a picture this call produced, when it produced one.
	Rel string `json:"rel,omitempty"`
}

// DB is the journal, backed by one SQLite file.
//
// IT IS SAFE FOR CONCURRENT USE. The step loop writes records, the daemon answers `/api/journal` from
// another goroutine and a `loop:`'s attempts are written while the loop is still going. `database/sql`
// pools, and the DSN sets a busy timeout so a reader never fails on a writer.
type DB struct {
	mu   sync.Mutex
	sql  *sql.DB
	path string
	// run is the run every record is stamped with.
	run string
	// recSeq and evtSeq are per-run counters, kept here rather than derived with MAX() so a write
	// costs one INSERT and not an INSERT after a scan.
	recSeq  int
	evtSeq  int
	imgSeqN int
}

// Open opens or creates the journal in dir and continues its sequence numbers.
//
// IT APPENDS RATHER THAN TRUNCATES. A daemon restarted mid-run must not erase the steps the run had
// already done - the same reasoning as an append-only file, one level up.
//
// `run` names the run these records belong to. A work directory is reused across runs, so it is the
// only thing that makes "this run's steps" a question the schema can answer.
func Open(dir, run string) (*DB, error) {
	path := filepath.Join(dir, "journal.db")
	// THE DSN IS WHERE THE DURABILITY LIVES, and every pragma here is load-bearing:
	//
	//	journal_mode(WAL)      a reader does not block the writer, which is what lets the page poll
	//	                       while a step is being recorded
	//	synchronous(FULL)      the reason this table exists is a run that DIED; a buffer that only
	//	                       reaches the disk on a clean exit loses exactly the evidence wanted
	//	busy_timeout(5000)     a concurrent writer waits rather than failing the step
	dsn := "file:" + path +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(FULL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening the journal: %w", err)
	}
	// ONE WRITER AT A TIME. SQLite serialises writes anyway; making the pool small keeps a burst of
	// events from queueing a dozen goroutines on the same file handle.
	db.SetMaxOpenConns(4)

	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	j := &DB{sql: db, path: path, run: run}
	if err := j.resumeSeq(); err != nil {
		db.Close()
		return nil, err
	}
	return j, nil
}

// migrate creates the tables and the indexes the read patterns need.
//
// EVERY INDEX HERE EXISTS FOR ONE READ THE FRONT END MAKES, and the measured cost of each is in the
// package comment: the overview, the poll, one step's detail, and the audit's count-by-kind. A
// schema without them answers the poll by scanning the history, which was the 225ms that decided
// against an append-only file.
func migrate(db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS record (
	run_id      TEXT    NOT NULL,
	seq         INTEGER NOT NULL,
	time        TEXT    NOT NULL,
	step        TEXT    NOT NULL,
	step_index  INTEGER NOT NULL,
	step_count  INTEGER NOT NULL,
	kind        TEXT    NOT NULL,
	status      TEXT    NOT NULL,
	duration_ms INTEGER NOT NULL DEFAULT 0,
	message     TEXT    NOT NULL DEFAULT '',
	mode        TEXT    NOT NULL DEFAULT '',
	want        TEXT    NOT NULL DEFAULT '',
	got         REAL,
	ok          INTEGER,
	words       TEXT,
	missing     TEXT,
	command     TEXT,
	images      TEXT,
	attempts    TEXT,
	facts       TEXT,
	PRIMARY KEY (run_id, seq)
);
CREATE INDEX IF NOT EXISTS record_by_run ON record (run_id, seq);

CREATE TABLE IF NOT EXISTS image (
	run_id     TEXT    NOT NULL,
	seq        INTEGER NOT NULL,
	time       TEXT    NOT NULL,
	name       TEXT    NOT NULL,
	sha        TEXT    NOT NULL,
	kind       TEXT    NOT NULL,
	step       TEXT    NOT NULL,
	step_index INTEGER NOT NULL,
	w          INTEGER NOT NULL DEFAULT 0,
	h          INTEGER NOT NULL DEFAULT 0,
	rect       TEXT,
	note       TEXT    NOT NULL DEFAULT '',
	PRIMARY KEY (run_id, seq)
);
CREATE INDEX IF NOT EXISTS image_by_step ON image (run_id, step_index, seq);
CREATE INDEX IF NOT EXISTS image_by_name ON image (run_id, name);

CREATE TABLE IF NOT EXISTS event (
	run_id      TEXT    NOT NULL,
	seq         INTEGER NOT NULL,
	time        TEXT    NOT NULL,
	step        TEXT    NOT NULL,
	step_index  INTEGER NOT NULL,
	kind        TEXT    NOT NULL,
	origin      TEXT    NOT NULL DEFAULT 'walk',
	detail      TEXT    NOT NULL DEFAULT '',
	ok          INTEGER,
	error       TEXT    NOT NULL DEFAULT '',
	duration_ms INTEGER NOT NULL DEFAULT 0,
	rel         TEXT    NOT NULL DEFAULT '',
	PRIMARY KEY (run_id, seq)
);
CREATE INDEX IF NOT EXISTS event_by_step ON event (run_id, step_index, seq);
CREATE INDEX IF NOT EXISTS event_by_kind ON event (run_id, kind, seq);
-- THE TIMELINE ASKS FOR EVERY CALL OF ONE RUN IN ORDER, and the since poll is the page's heartbeat;
-- the primary key already answers that. This one serves "only the person's own actions", which is how
-- a reader answers "did I break it or did the file".
CREATE INDEX IF NOT EXISTS event_by_origin ON event (run_id, origin, seq);
`
if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("creating the journal schema: %w", err)
	}
	return nil
}

// resumeSeq finds the highest sequence already stored for this run, so a restart continues rather
// than overwriting.
func (j *DB) resumeSeq() error {
	row := j.sql.QueryRow(`SELECT COALESCE(MAX(seq),0) FROM record WHERE run_id = ?`, j.run)
	if err := row.Scan(&j.recSeq); err != nil {
		return fmt.Errorf("reading the journal's last sequence: %w", err)
	}
	row = j.sql.QueryRow(`SELECT COALESCE(MAX(seq),0) FROM event WHERE run_id = ?`, j.run)
	if err := row.Scan(&j.evtSeq); err != nil {
		return fmt.Errorf("reading the journal's last event sequence: %w", err)
	}
	// A JOURNAL WRITTEN BEFORE THE LIBRARY EXISTED HAS NO image TABLE, and a reader of that file
	// should see its steps rather than an error about a table it never had.
	row = j.sql.QueryRow(`SELECT COALESCE(MAX(seq),0) FROM image WHERE run_id = ?`, j.run)
	if err := row.Scan(&j.imgSeqN); err != nil {
		j.imgSeqN = 0
	}
	return nil
}

// now is the timestamp format every row carries.
//
// MILLISECONDS, because a run makes several calls a millisecond apart and a reader ordering "what
// happened when" needs to tell them apart.
func now() string { return time.Now().Format("2006-01-02T15:04:05.000Z07:00") }

// Write appends one step record and returns it with its sequence number filled in.
func (j *DB) Write(r Record) (Record, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.recSeq++
	r.Seq = j.recSeq
	r.RunID = j.run
	if r.Time == "" {
		r.Time = now()
	}
	_, err := j.sql.Exec(`INSERT INTO record
		(run_id, seq, time, step, step_index, step_count, kind, status, duration_ms, message,
		 mode, want, got, ok, words, missing, command, images, attempts, facts)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.RunID, r.Seq, r.Time, r.Step, r.StepIndex, r.StepCount, string(r.Kind), string(r.Status),
		r.DurationMS, r.Message, r.Mode, r.Want, nullFloat(r.Got), nullBool(r.OK),
		jsonOrNil(r.Words), jsonOrNil(r.Missing), jsonOrNil(r.Command),
		jsonOrNil(r.Images), jsonOrNil(r.Attempts), jsonOrNil(r.Facts),
	)
	if err != nil {
		return r, fmt.Errorf("writing a journal record: %w", err)
	}
	return r, nil
}

// Image records one NAMED picture in the library, and returns the row it wrote.
//
// THIS IS THE `see:`/`screenshot:` PATH, not the recording's. A picture enters the library because a
// file or a person ASKED for it by name; the recorder's thousand frames a run are the recording and
// stay out of it.
func (j *DB) Image(img Image, step string, stepIndex int) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	var rect any
	if len(img.Rect) > 0 {
		rect = jsonOrNil(img.Rect)
	}
	_, err := j.sql.Exec(`INSERT INTO image
		(run_id, seq, time, name, sha, kind, step, step_index, w, h, rect, note)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		j.run, j.imageSeq(), now(), img.Name, img.Sha, img.Label, step, stepIndex,
		img.W, img.H, rect, img.Note,
	)
	if err != nil {
		return fmt.Errorf("recording an artifact: %w", err)
	}
	return nil
}

// imageSeq is the image table's own counter, kept on the DB for the reason the other two are: a
// write should cost one INSERT, not an INSERT after a scan.
func (j *DB) imageSeq() int {
	j.imgSeqN++
	return j.imgSeqN
}

// Library returns every named picture of a run, newest last.
func (j *DB) Library(run string) ([]Image, error) {
	rows, err := j.sql.Query(`SELECT name, sha, kind, w, h, rect, note FROM image
		WHERE run_id = ? ORDER BY seq`, run)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Image
	for rows.Next() {
		var im Image
		var rect []byte
		if err := rows.Scan(&im.Name, &im.Sha, &im.Label, &im.W, &im.H, &rect, &im.Note); err != nil {
			return nil, err
		}
		decodeJSON(rect, &im.Rect)
		out = append(out, im)
	}
	return out, rows.Err()
}

// ImageByName resolves a `see:` name to the picture it captured, for the read a check performs later.
//
// THE LATEST ONE WINS. A loop may `see:` the same name eight times; the picture a reader wants when
// they ask for "shot" is what it saw last.
func (j *DB) ImageByName(run, name string) (Image, bool, error) {
	var im Image
	var rect []byte
	err := j.sql.QueryRow(`SELECT name, sha, kind, w, h, rect, note FROM image
		WHERE run_id = ? AND name = ? ORDER BY seq DESC LIMIT 1`, run, name).
		Scan(&im.Name, &im.Sha, &im.Label, &im.W, &im.H, &rect, &im.Note)
	if err == sql.ErrNoRows {
		return im, false, nil
	}
	if err != nil {
		return im, false, err
	}
	decodeJSON(rect, &im.Rect)
	return im, true, nil
}

// Event appends one external call.
//
// AN EVENT IS NOT A RECORD AND IS NOT ALLOWED TO STOP THE WALK. `Write` returns its error because a
// step's own record is the spine of the report; an event is the audit trail beneath it, and losing
// one must not lose the step. The caller logs it and carries on.
func (j *DB) Event(e Event) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.evtSeq++
	e.Seq = j.evtSeq
	e.RunID = j.run
	if e.Time == "" {
		e.Time = now()
	}
	if e.Origin == "" {
		e.Origin = OriginWalk
	}
	_, err := j.sql.Exec(`INSERT INTO event
		(run_id, seq, time, step, step_index, kind, origin, detail, ok, error, duration_ms, rel)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.RunID, e.Seq, e.Time, e.Step, e.StepIndex, string(e.Kind), string(e.Origin), e.Detail,
		nullBool(e.OK), e.Error, e.DurationMS, e.Rel,
	)
	if err != nil {
		return fmt.Errorf("writing a journal event: %w", err)
	}
	return nil
}

// RunID is the run these records belong to.
func (j *DB) RunID() string { return j.run }

// Path is where the database lives.
func (j *DB) Path() string { return j.path }

// Close closes the database.
func (j *DB) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.sql == nil {
		return nil
	}
	// A CHECKPOINT BEFORE CLOSE, so a WAL that the reader might want is folded into the main file.
	// `PRAGMA wal_checkpoint(TRUNCATE)` is best-effort: a failure here does not mean the data is
	// lost, it means the WAL still has it.
	_, _ = j.sql.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	err := j.sql.Close()
	j.sql = nil
	return err
}

// Steps returns one run's step records WITHOUT the picture and attempt payloads, for the list a
// reader scans first.
//
// THE OVERVIEW IS A LIST OF NAMES AND TICKS. Decoding a step's images for a row that draws its title
// is work nobody asked for, and at forty steps it was measurable. This is the same indexed query
// with the heavy columns left on disk; `Record` (singular) fetches one step with everything.
func (j *DB) Steps(run string, since int) ([]Record, error) {
	rows, err := j.sql.Query(`SELECT run_id, seq, time, step, step_index, step_count, kind, status,
		duration_ms, message, mode, want, got, ok, words, missing
		FROM record WHERE run_id = ? AND seq > ? ORDER BY seq`, run, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Record
	for rows.Next() {
		var r Record
		var kind, status string
		var got sql.NullFloat64
		var okVal sql.NullBool
		var words, missing []byte
		if err := rows.Scan(&r.RunID, &r.Seq, &r.Time, &r.Step, &r.StepIndex, &r.StepCount,
			&kind, &status, &r.DurationMS, &r.Message, &r.Mode, &r.Want, &got, &okVal,
			&words, &missing); err != nil {
			return nil, err
		}
		r.Kind, r.Status = Kind(kind), Status(status)
		if got.Valid {
			v := got.Float64
			r.Got = &v
		}
		if okVal.Valid {
			v := okVal.Bool
			r.OK = &v
		}
		decodeJSON(words, &r.Words)
		decodeJSON(missing, &r.Missing)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Record returns ONE step with everything it holds - the pictures it compared, the attempts its loop
// made. This is the read a detail view performs, and it is keyed by the primary key, so its cost is
// the row's.
func (j *DB) Record(run string, seq int) (Record, error) {
	var r Record
	var kind, status string
	var got sql.NullFloat64
	var okVal sql.NullBool
	var words, missing, command, images, attempts, facts []byte
	err := j.sql.QueryRow(`SELECT run_id, seq, time, step, step_index, step_count, kind, status,
		duration_ms, message, mode, want, got, ok, words, missing, command, images, attempts, facts
		FROM record WHERE run_id = ? AND seq = ?`, run, seq).Scan(
		&r.RunID, &r.Seq, &r.Time, &r.Step, &r.StepIndex, &r.StepCount, &kind, &status,
		&r.DurationMS, &r.Message, &r.Mode, &r.Want, &got, &okVal,
		&words, &missing, &command, &images, &attempts, &facts)
	if err != nil {
		return r, err
	}
	r.Kind, r.Status = Kind(kind), Status(status)
	if got.Valid {
		v := got.Float64
		r.Got = &v
	}
	if okVal.Valid {
		v := okVal.Bool
		r.OK = &v
	}
	decodeJSON(words, &r.Words)
	decodeJSON(missing, &r.Missing)
	decodeJSON(command, &r.Command)
	decodeJSON(images, &r.Images)
	decodeJSON(attempts, &r.Attempts)
	if len(facts) > 0 {
		_ = json.Unmarshal(facts, &r.Facts)
	}
	return r, nil
}

// Records returns one run's step records, or only those after `since`.
//
// THE LIST ON THE FRONT PAGE DOES NOT NEED THE PICTURES, AND ASKING FOR THEM COST MORE THAN THE
// LOOKUP DID. MEASURED: the overview took 7.3ms for forty steps while the index alone answers in
// well under one - because every row's `images`, `attempts` and `facts` JSON was decoded for a list
// that draws a step's name and a tick. `Steps` is the same query without those columns, and this one
// is for a caller that opens a step.
func (j *DB) Records(run string, since int) ([]Record, error) {
	rows, err := j.sql.Query(`SELECT run_id, seq, time, step, step_index, step_count, kind, status,
		duration_ms, message, mode, want, got, ok, words, missing, command, images, attempts, facts
		FROM record WHERE run_id = ? AND seq > ? ORDER BY seq`, run, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Record
	for rows.Next() {
		var r Record
		var kind, status string
		var got sql.NullFloat64
		var okVal sql.NullBool
		var words, missing, command, images, attempts, facts []byte
		if err := rows.Scan(&r.RunID, &r.Seq, &r.Time, &r.Step, &r.StepIndex, &r.StepCount,
			&kind, &status, &r.DurationMS, &r.Message, &r.Mode, &r.Want, &got, &okVal,
			&words, &missing, &command, &images, &attempts, &facts); err != nil {
			return nil, err
		}
		r.Kind, r.Status = Kind(kind), Status(status)
		if got.Valid {
			v := got.Float64
			r.Got = &v
		}
		if okVal.Valid {
			v := okVal.Bool
			r.OK = &v
		}
		decodeJSON(words, &r.Words)
		decodeJSON(missing, &r.Missing)
		decodeJSON(command, &r.Command)
		decodeJSON(images, &r.Images)
		decodeJSON(attempts, &r.Attempts)
		if len(facts) > 0 {
			_ = json.Unmarshal(facts, &r.Facts)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Events returns one run's external calls, narrowed as the caller asks.
//
// `stepIndex` of -1 means every step; `kind` of "" means every kind. Both are indexed, which is the
// whole point of the two indexes in the schema: a detail view asks about ONE step and an audit asks
// about ONE kind, and neither should scan the run.
func (j *DB) Events(run string, stepIndex int, kind CallKind) ([]Event, error) {
	q := `SELECT run_id, seq, time, step, step_index, kind, origin, detail, ok, error, duration_ms, rel
		FROM event WHERE run_id = ?`
	args := []any{run}
	if stepIndex >= 0 {
		q += ` AND step_index = ?`
		args = append(args, stepIndex)
	}
	if kind != "" {
		q += ` AND kind = ?`
		args = append(args, string(kind))
	}
	q += ` ORDER BY seq`

	rows, err := j.sql.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var e Event
		var k, origin string
		var okVal sql.NullBool
		if err := rows.Scan(&e.RunID, &e.Seq, &e.Time, &e.Step, &e.StepIndex, &k, &origin, &e.Detail,
			&okVal, &e.Error, &e.DurationMS, &e.Rel); err != nil {
			return nil, err
		}
		e.Kind, e.Origin = CallKind(k), Origin(origin)
		if okVal.Valid {
			v := okVal.Bool
			e.OK = &v
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EventsSince returns a run's calls after `seq`, in order, for a stream that follows a run.
//
// IT IS `Events` WITH A CURSOR rather than a separate query, so the page's poll and its stream read
// the same rows the same way - two shapes for one table is how a live view comes to disagree with
// the history it is showing.
func (j *DB) EventsSince(run string, since int) ([]Event, error) {
	rows, err := j.sql.Query(`SELECT run_id, seq, time, step, step_index, kind, origin, detail, ok,
		error, duration_ms, rel
		FROM event WHERE run_id = ? AND seq > ? ORDER BY seq`, run, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var e Event
		var k, origin string
		var okVal sql.NullBool
		if err := rows.Scan(&e.RunID, &e.Seq, &e.Time, &e.Step, &e.StepIndex, &k, &origin, &e.Detail,
			&okVal, &e.Error, &e.DurationMS, &e.Rel); err != nil {
			return nil, err
		}
		e.Kind, e.Origin = CallKind(k), Origin(origin)
		if okVal.Valid {
			v := okVal.Bool
			e.OK = &v
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// StepsSince returns a run's step records after `seq`, for the same stream.
func (j *DB) StepsSince(run string, since int) ([]Record, error) { return j.Steps(run, since) }

// Runs lists every run this journal holds, newest first, with a count of its records.
//
// A detail view over ALL runs has no SQL projection here on purpose: the bytes of a step's images
// are not queried, so a summary table is enough.
func (j *DB) Runs() ([]Run, error) {
	rows, err := j.sql.Query(`SELECT run_id, COUNT(*), MIN(time), MAX(time)
		FROM record GROUP BY run_id ORDER BY MAX(time) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Run
	for rows.Next() {
		var r Run
		if err := rows.Scan(&r.ID, &r.Records, &r.Started, &r.Ended); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Run is one run's summary, for a list of runs.
type Run struct {
	ID      string `json:"id"`
	Records int    `json:"records"`
	Started string `json:"started"`
	Ended   string `json:"ended"`
}

// slowestThresholdMS is what counts as a slow call in the audit.
//
// IT IS A NUMBER RATHER THAN A PERCENTAGE because the question a person asks is "did anything take
// longer than a second", not "what is the 90th percentile". A click that took two seconds means the
// machine was not answering; a click that took 40ms means it was.
const slowestThresholdMS = 1000

// Summary is the audit's arithmetic for one run: counts by kind, the slowest calls, and every
// failure recorded.
//
// IT IS COMPUTED IN SQL, not by reading every event into Go, because the whole reason for the
// database is that "count every click in this run" must not cost a full scan.
func (j *DB) Summary(run string) (Summary, error) {
	var s Summary
	s.RunID = run

	rows, err := j.sql.Query(`SELECT kind, COUNT(*) FROM event WHERE run_id = ? GROUP BY kind`, run)
	if err != nil {
		return s, err
	}
	s.ByKind = map[string]int{}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			rows.Close()
			return s, err
		}
		s.ByKind[k] = n
		s.Total += n
	}
	rows.Close()

	rows, err = j.sql.Query(`SELECT COUNT(*) FROM event WHERE run_id = ? AND ok = 0`, run)
	if err != nil {
		return s, err
	}
	if rows.Next() {
		_ = rows.Scan(&s.Failures)
	}
	rows.Close()

	slow, err := j.sql.Query(`SELECT time, step, step_index, kind, detail, duration_ms
		FROM event WHERE run_id = ? AND duration_ms >= ? ORDER BY duration_ms DESC LIMIT 20`,
		run, slowestThresholdMS)
	if err != nil {
		return s, err
	}
	defer slow.Close()
	for slow.Next() {
		var c SlowCall
		if err := slow.Scan(&c.Time, &c.Step, &c.StepIndex, &c.Kind, &c.Detail, &c.DurationMS); err != nil {
			return s, err
		}
		s.Slowest = append(s.Slowest, c)
	}
	return s, slow.Err()
}

// Summary is one run's audit arithmetic.
type Summary struct {
	RunID    string         `json:"run_id"`
	Total    int            `json:"total"`
	ByKind   map[string]int `json:"by_kind"`
	Failures int            `json:"failures"`
	Slowest  []SlowCall     `json:"slowest,omitempty"`
}

// SlowCall is one external call that took longer than a second.
type SlowCall struct {
	Time       string `json:"time"`
	Step       string `json:"step"`
	StepIndex  int    `json:"step_index"`
	Kind       string `json:"kind"`
	Detail     string `json:"detail"`
	DurationMS int64  `json:"duration_ms"`
}

// --- helpers -------------------------------------------------------------------------

func nullFloat(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullBool(p *bool) any {
	if p == nil {
		return nil
	}
	if *p {
		return 1
	}
	return 0
}

// jsonOrNil stores a slice or map as JSON text, or NULL when it is empty.
//
// NULL RATHER THAN '[]' makes "this step compared no pictures" distinguishable from "it compared an
// empty list", which matters for a reader deciding whether to draw a gallery.
func jsonOrNil(v any) any {
	switch t := v.(type) {
	case []string:
		if len(t) == 0 {
			return nil
		}
	case []Image:
		if len(t) == 0 {
			return nil
		}
	case []Attempt:
		if len(t) == 0 {
			return nil
		}
	case map[string]bool:
		if len(t) == 0 {
			return nil
		}
	case nil:
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return string(b)
}

func decodeJSON(b []byte, dst any) {
	if len(b) == 0 {
		return
	}
	_ = json.Unmarshal(b, dst)
}

// Exporter writes one run as JSONL, for a CI artifact and for a person with `jq`.
//
// THE DATABASE IS FOR THE PAGE; THE EXPORT IS FOR EVERYTHING ELSE. A binary artifact in a GitHub
// Actions run cannot be read without downloading and opening it, and the evidence this journal holds
// is exactly what a person looks at when a run failed. `Records` and `Events` already do the
// reading, so the export is those two loops over an encoder.
func (j *DB) Export(dir, run string) (string, error) {
	recs, err := j.Records(run, 0)
	if err != nil {
		return "", err
	}
	evts, err := j.Events(run, -1, "")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "journal.jsonl")
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	for _, r := range recs {
		if err := enc.Encode(map[string]any{"type": "record", "record": r}); err != nil {
			return "", err
		}
	}
	for _, e := range evts {
		if err := enc.Encode(map[string]any{"type": "event", "event": e}); err != nil {
			return "", err
		}
	}
	return path, nil
}

// Heartbeat exists so a test can prove the connection is alive without touching a table.
func (j *DB) Heartbeat(ctx context.Context) error {
	return j.sql.PingContext(ctx)
}
// OpenExisting opens a journal that is already on disk, WITHOUT creating one.
//
// IT IS WHAT A REPORT USES, and the difference from `Open` matters: a report must not bring a journal
// into existence. `Open` creates the file and its schema, which for a mistyped `--work` would leave a
// plausible empty database where a person would then look for evidence that was never there.
func OpenExisting(dir string) (*DB, error) {
	path := filepath.Join(dir, "journal.db")
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("no journal at %s: %w", path, err)
	}
	db, err := sql.Open("sqlite", "file:"+path+
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)

	j := &DB{sql: db, path: path}
	// THE RUN ID IS DISCOVERED RATHER THAN ASSERTED: a reader of the file has no run to stamp rows
	// with, and `RunID` is used by the HTTP handlers to mean "the run to show".
	var newest string
	row := db.QueryRow(`SELECT run_id FROM record ORDER BY time DESC LIMIT 1`)
	if err := row.Scan(&newest); err == nil {
		j.run = newest
	}
	if err := j.resumeSeq(); err != nil {
		db.Close()
		return nil, err
	}
	return j, nil
}
