package run

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/easydev-org/easyvm/internal/journal"
	"github.com/easydev-org/easyvm/internal/walk"
)

// THE JOURNAL IS A SINK, NOT A DEPENDENCY.
//
// `Executor` must be usable from a test that has no work directory and no daemon - 340 tests build
// one by value - so the interface here is ONE method and the zero value is a working no-op. The
// daemon provides a real one; everything else ignores it.
type Journal interface {
	Write(journal.Record) (journal.Record, error)
}

// journ writes one record when a journal is configured, and does nothing when it is not.
//
// IT NEVER FAILS THE WALK. The journal is evidence ABOUT a run; a full disk that cannot record a step
// must not turn a working walkthrough into a failing one. The failure is reported to the log, where a
// reader will see it beside everything else, and the run continues - which is the opposite of the
// choice `run:` makes, and deliberately so: a command that did not run is a fact about the guest, a
// record that could not be written is a fact about the host.
func (e *Executor) journ(r journal.Record) {
	if e.Journal == nil {
		return
	}
	if _, err := e.Journal.Write(r); err != nil {
		e.Driver.Log(fmt.Sprintf("  journal: could not record this step: %v", err))
	}
}

// journStep writes the record for one step, filling in the placing facts every record carries.
func (e *Executor) journStep(s walk.Step, index, total int, kind journal.Kind, status journal.Status, started time.Time) journal.Record {
	r := journal.Record{
		Step:      s.Name,
		StepIndex: index,
		StepCount: total,
		Kind:      kind,
		Status:    status,
	}
	if !started.IsZero() {
		r.DurationMS = time.Since(started).Milliseconds()
	}
	return r
}

// kindOf names what sort of evidence a step is going to produce, BEFORE it runs.
//
// IT IS DERIVED FROM THE KEYS rather than declared in the file: a walkthrough already says what a
// step is by the key it writes, and a second place to say it would be a second place to disagree.
func kindOf(s walk.Step) journal.Kind {
	switch {
	case s.Has("check"):
		return journal.KindCheck
	case s.Has("read"):
		return journal.KindSee
	case s.Has("run"):
		return journal.KindRun
	case s.Has("ssh"):
		return journal.KindSSH
	case s.Has("repeat"), s.Has("until"), s.Has("until_any"):
		return journal.KindLoop
	}
	return journal.KindStep
}

// relArtifact turns an absolute path inside the work directory into the relative, slash-separated
// form a record carries.
//
// A PATH OUTSIDE THE WORK DIRECTORY IS RETURNED EMPTY, and the record then has an image with no
// address rather than an address the daemon would refuse. That is deliberate: `/api/artifact` only
// serves the work directory, so writing `/etc/passwd` into a journal would produce a link that 403s,
// which reads as "the daemon is broken" rather than "this file is not an artifact of the run".
func relArtifact(work, path string) string {
	if path == "" || work == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	root, err := filepath.Abs(work)
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.ToSlash(rel)
}

// imageOf describes one artifact for a record.
func (e *Executor) imageOf(path, label, note string, w, h int, rect []int) *journal.Image {
	rel := relArtifact(e.Work, path)
	if rel == "" {
		return nil
	}
	return &journal.Image{Rel: rel, Label: label, W: w, H: h, Rect: rect, Note: note}
}
