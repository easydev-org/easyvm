package daemon

import (
	"sync"
	"time"

	"github.com/easydev-org/easyvm/internal/journal"
)

// audit is what one run's external calls are written down in: every click, every keystroke, every
// screen capture, every command sent to the guest.
//
// # WHY THE DRIVER OWNS IT
//
// THE DRIVER IS WHERE THE CALLS ACTUALLY HAPPEN. `Click`, `Stroke`, `Type`, `Frame` and the ssh path
// are the only places a keystroke or a picture leaves this process, so it is the only place where
// "every call" means every call. Instrumenting the step dispatcher instead would record what a FILE
// asked for; a loop's eight attempts and a re-typed password are calls, and they happen inside those
// methods.
//
// # WHY IT IS SEPARATE FROM THE JOURNAL WRITER
//
// It has to be safe to call from anywhere and it must NEVER fail the walk. A full disk that cannot
// record a click must not turn a working run into a failing one - so an audit write that fails sets
// a sticky error that the step boundary reports once, and everything else carries on.
type audit struct {
	mu    sync.Mutex
	db    *journal.DB
	err   error
	step  string
	index int
}

// newAudit builds the sink for one run.
func newAudit(db *journal.DB) *audit { return &audit{db: db} }

// step is called by the executor at each step boundary, so every call knows which step made it.
//
// IT IS SET RATHER THAN PASSED because the call sites are inside `Click`/`Type`/`Frame`, which take
// the arguments the walk wrote and nothing else. Threading a step name through every signature would
// be a change to the interface the walkthrough vocabulary is built on, for the sake of a reader.
func (a *audit) setStep(name string, index int) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.step, a.index = name, index
	a.mu.Unlock()
}

// call records one external call. `ok` of nil means "not a call that can fail".
//
// IT TAKES A START TIME SO THE CALL'S DURATION IS THE CALL'S, not the write's. A keystroke that takes
// 40ms and a screenshot that takes 900ms are the two most useful numbers in an audit of a slow stage,
// and neither is visible if the timer wraps the INSERT.
func (a *audit) call(kind journal.CallKind, detail string, started time.Time, ok *bool, err error, rel string) {
	if a == nil || a.db == nil {
		return
	}
	a.mu.Lock()
	step, index := a.step, a.index
	a.mu.Unlock()

	e := journal.Event{
		Step:       step,
		StepIndex:  index,
		Kind:       kind,
		Detail:     detail,
		OK:         ok,
		Rel:        rel,
		DurationMS: time.Since(started).Milliseconds(),
	}
	if err != nil {
		e.Error = err.Error()
	}
	if werr := a.db.Event(e); werr != nil {
		a.mu.Lock()
		if a.err == nil {
			a.err = werr
		}
		a.mu.Unlock()
	}
}

// takeError returns the first write failure, if any, and clears it.
//
// THE STEP BOUNDARY REPORTS IT ONCE rather than the log filling with one line per keystroke: a disk
// that filled up would otherwise print a complaint for every character typed after it.
func (a *audit) takeError() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	err := a.err
	a.err = nil
	return err
}

// okPtr is the pointer form `audit.call` wants for a call that succeeded or failed.
func okPtr(v bool) *bool { return &v }