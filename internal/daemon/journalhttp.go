package daemon

import (
	"fmt"
	"html"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/easydev-org/easyvm/internal/journal"
	"github.com/easydev-org/easyvm/internal/report"
)

// THE JOURNAL'S READ SURFACE. Four endpoints, and what each is FOR decides its shape:
//
//	GET /api/journal                    the steps of one run   -> the overview list
//	GET /api/journal/step?seq=N         one step, with pictures -> the detail pane
//	GET /api/journal/events?step=N&kind= one step's calls, or every call of a kind -> the audit
//	GET /api/journal/library            the named pictures of a run -> the asset browser
//
// NONE OF THEM DRIVES ANYTHING. There is no `check` endpoint and no "run this step" endpoint, and
// that is deliberate rather than unfinished: a `check:` reads a frame a `see:` CAPTURED AND NAMED,
// and that binding lives in the running walkthrough's executor. An HTTP check could only capture a
// fresh frame and compare that - a different question - or accept a picture and become a general
// image-difference API with nothing to do with the walk. Worse, a check's verdict is CONTROL FLOW
// (`loop: until: {fact: ...}`), so letting a request decide it would let a caller forge the condition
// a stage is waiting on.

// handleJournal returns one run's steps.
//
// `since` makes the poll cheap: the page asks for what it has not seen. `run` defaults to the run the
// daemon is driving now, which is the one a page opened during a run wants.
func (d *Daemon) handleJournal(w http.ResponseWriter, r *http.Request) {
	db := d.journalDB()
	if db == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok":    false,
			"error": "this daemon has no journal; it was started without one or it could not be opened",
		})
		return
	}
	run := r.URL.Query().Get("run")
	if run == "" {
		run = db.RunID()
	}
	since, _ := strconv.Atoi(r.URL.Query().Get("since"))

	steps, err := db.Steps(run, since)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"run":   run,
		"steps": steps,
	})
}

// handleJournalStep returns ONE step with everything it holds: the pictures it compared, the attempts
// its loop made, the words it read.
func (d *Daemon) handleJournalStep(w http.ResponseWriter, r *http.Request) {
	db := d.journalDB()
	if db == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok": false, "error": "this daemon has no journal",
		})
		return
	}
	seq, err := strconv.Atoi(r.URL.Query().Get("seq"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok": false, "error": "`seq` must be the number `/api/journal` gave the step",
		})
		return
	}
	run := r.URL.Query().Get("run")
	if run == "" {
		run = db.RunID()
	}
	rec, err := db.Record(run, seq)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"ok": false, "error": fmt.Sprintf("no step %d in run %q", seq, run),
		})
		return
	}
	// THE STEP'S CALLS COME WITH IT, because the detail pane shows them and two round trips for one
	// click is a page that flickers.
	events, err := db.Events(run, rec.StepIndex, "")
	if err != nil {
		events = nil
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"step":   rec,
		"events": events,
	})
}

// handleJournalEvents is the audit: one step's calls, or every call of one kind across the run.
//
//	?step=3        the calls under step 3
//	?kind=click    every click in the run
//	(nothing)      every call in the run
func (d *Daemon) handleJournalEvents(w http.ResponseWriter, r *http.Request) {
	db := d.journalDB()
	if db == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok": false, "error": "this daemon has no journal",
		})
		return
	}
	run := r.URL.Query().Get("run")
	if run == "" {
		run = db.RunID()
	}
	stepIndex := -1
	if v := r.URL.Query().Get("step"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"ok": false, "error": "`step` must be a number",
			})
			return
		}
		stepIndex = n
	}
	kind := journal.CallKind(r.URL.Query().Get("kind"))

	events, err := db.Events(run, stepIndex, kind)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"run":    run,
		"count":  len(events),
		"events": events,
	})
}

// handleJournalSummary is the audit's arithmetic: how many calls of each kind, how many failed, and
// the slowest ones.
func (d *Daemon) handleJournalSummary(w http.ResponseWriter, r *http.Request) {
	db := d.journalDB()
	if db == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok": false, "error": "this daemon has no journal",
		})
		return
	}
	run := r.URL.Query().Get("run")
	if run == "" {
		run = db.RunID()
	}
	s, err := db.Summary(run)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "summary": s})
}

// handleJournalLibrary lists the NAMED pictures of a run - the ones a `see:` or a `screenshot:`
// produced.
//
// IT IS THE LIBRARY, NOT THE RECORDING. Two thousand unnamed frames a run are the recording and are
// not here; what is here is what a file or a person asked for by name.
func (d *Daemon) handleJournalLibrary(w http.ResponseWriter, r *http.Request) {
	db := d.journalDB()
	if db == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok": false, "error": "this daemon has no journal",
		})
		return
	}
	run := r.URL.Query().Get("run")
	if run == "" {
		run = db.RunID()
	}
	lib, err := db.Library(run)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"run":     run,
		"count":   len(lib),
		"library": lib,
	})
}

// handleJournalRuns lists every run the journal holds, newest first.
func (d *Daemon) handleJournalRuns(w http.ResponseWriter, r *http.Request) {
	db := d.journalDB()
	if db == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok": false, "error": "this daemon has no journal",
		})
		return
	}
	runs, err := db.Runs()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "runs": runs})
}

// handleArtifact serves a picture from the run's library, by its content address.
//
//	/api/artifact?h=<sha>   a named picture, from the library
//	/api/artifact?p=<rel>   a file in the work directory - a check's crops
//
// THE TWO ADDRESSES ARE NOT THE SAME THING and the difference is worth keeping: a library picture is
// immutable and deduplicated, and a work-directory file is whatever that path holds right now. A
// report uses the first, a live view of a failed step may use the second.
//
// THE PATH IS CONFINED. `p` is resolved against the work directory and refused if it escapes; `h` can
// only be sixteen hex characters. Without both checks this endpoint would serve `/etc/passwd`.
func (d *Daemon) handleArtifact(w http.ResponseWriter, r *http.Request) {
	work := d.workDir()
	if work == "" {
		http.Error(w, "this daemon has no work directory", http.StatusServiceUnavailable)
		return
	}

	if key := r.URL.Query().Get("h"); key != "" {
		path := filepath.Join(work, "artifact", key[:min(len(key), 2)], key+".png")
		if !confined(work, path) {
			http.Error(w, "not a library address", http.StatusBadRequest)
			return
		}
		// IMMUTABLE, AND THAT IS THE POINT OF A CONTENT ADDRESS: a browser that has this picture can
		// keep it for ever, and a report reloaded a week later shows the same bytes.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		serveFileOr404(w, r, path)
		return
	}

	rel := r.URL.Query().Get("p")
	if rel == "" {
		http.Error(w, "say `h=<content address>` or `p=<path in the work directory>`",
			http.StatusBadRequest)
		return
	}
	path := filepath.Join(work, filepath.FromSlash(rel))
	if !confined(work, path) {
		http.Error(w, "that path is outside the work directory", http.StatusForbidden)
		return
	}
	// A WORK-DIRECTORY FILE IS NOT IMMUTABLE - a frame is overwritten a second later - so it is
	// served without a long cache, which is the honest thing to say about it.
	w.Header().Set("Cache-Control", "no-store")
	serveFileOr404(w, r, path)
}

// journalDB is the run's journal, or nil.
func (d *Daemon) journalDB() *journal.DB {
	d.mu.Lock()
	db, work := d.jdb, d.work
	d.mu.Unlock()
	if db != nil {
		return db
	}
	// A DAEMON THAT IS NOT RUNNING A WALK STILL HAS A JOURNAL, and this is a correction rather than a
	// convenience.
	//
	// MEASURED: `/report` answered 200 with the whole page while `/api/journal` answered
	// 503 "this daemon has no journal" - on the same process, about the same run. The cause was that
	// `d.jdb` is only set when `startRun` opens a journal, so a daemon that had finished its run, or
	// was restarted, served the report from disk but could not answer the list it was built from.
	//
	// The journal on disk IS the daemon's answer; `OpenExisting` refuses to create one, so a work
	// directory with no run still says "no journal" instead of inventing an empty database.
	if work == "" {
		return nil
	}
	opened, err := journal.OpenExisting(work)
	if err != nil {
		return nil
	}
	d.mu.Lock()
	if d.jdb == nil {
		d.jdb = opened
		opened = nil
	}
	kept := d.jdb
	d.mu.Unlock()
	if opened != nil {
		_ = opened.Close()
	}
	return kept
}

// workDir is the daemon's work directory.
func (d *Daemon) workDir() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.work
}

// serveFileOr404 serves a file, refusing a missing one with the sentence a person needs.
func serveFileOr404(w http.ResponseWriter, r *http.Request, path string) {
	if _, err := os.Stat(path); err != nil {
		http.Error(w, "no such picture: "+filepath.Base(path), http.StatusNotFound)
		return
	}
	http.ServeFile(w, r, path)
}

// confined reports whether path is inside root.
//
// IT IS THE WHOLE PATH-TRAVERSAL DEFENCE FOR `?p=`. `filepath.Join` cleans `..` but does not refuse
// it, so a `p=../../etc/passwd` resolves outside the work directory and must be caught here.
func confined(root, path string) bool {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	return absPath == absRoot || strings.HasPrefix(absPath, absRoot+string(filepath.Separator))
}
// handleReport renders the run's journal as the HTML page, served rather than written.
//
// THE SAME PAGE `easyvm report` PRODUCES, with its pictures fetched from `/api/artifact` instead of
// sitting beside it. That is the only difference, and it is what makes "open the daemon's URL and
// read the report" work without a build step or a published directory.
//
//	/report            the newest run
//	/report?run=<id>   one run
func (d *Daemon) handleReport(w http.ResponseWriter, r *http.Request) {
	work := d.workDir()
	if work == "" {
		http.Error(w, "this daemon has no work directory", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// NO CACHE: a run in progress changes under the reader, and a cached page would show a stale
	// step list while the machine moved on.
	w.Header().Set("Cache-Control", "no-store")
	if _, err := report.Render(w, work, r.URL.Query().Get("run")); err != nil {
		// THE HEADERS ARE ALREADY SENT, so the error has to be visible in the page rather than as a
		// status code. A reader who opened this URL wants to know why it is empty.
		fmt.Fprintf(w, "<h1>no report</h1><p>%s</p>", html.EscapeString(err.Error()))
	}
}
