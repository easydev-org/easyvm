package run

import (
	"fmt"
	"image"
	"os"
	"path/filepath"

	"github.com/easydev-org/easyvm/internal/journal"
	"github.com/easydev-org/easyvm/internal/screen"
	"github.com/easydev-org/easyvm/internal/walk"
)

// decorate fills in the evidence a step produced.
//
// THE EVIDENCE IS COLLECTED AS THE STEP RUNS rather than re-derived here, because only the step
// itself knows WHICH region it cropped, WHICH reference it loaded and WHICH frame it looked at -
// that knowledge is in local variables that are gone by the time this is called.
//
// A LOOP IS THE EXCEPTION, and it is deliberate: a loop's attempts are its most useful evidence and
// they are collected by the loop into `e.attempts`, which this copies wholesale.
func (e *Executor) decorate(r journal.Record, s walk.Step) journal.Record {
	// FACTS: the booleans named by `check:` with `as:` and the `when:`/`unless:` answers.
	if len(e.facts) > 0 {
		r.Facts = make(map[string]bool, len(e.facts))
		for k, v := range e.facts {
			r.Facts[k] = v
		}
	}
	if e.evidence != nil {
		ev := e.evidence
		if ev.Mode != "" {
			r.Mode = ev.Mode
		}
		if ev.Want != "" {
			r.Want = ev.Want
		}
		if ev.Got != nil {
			v := *ev.Got
			r.Got = &v
		}
		if ev.OK != nil {
			v := *ev.OK
			r.OK = &v
		}
		if len(ev.Words) > 0 {
			r.Words = append([]string(nil), ev.Words...)
		}
		if len(ev.Missing) > 0 {
			r.Missing = append([]string(nil), ev.Missing...)
		}
		if len(ev.Command) > 0 {
			r.Command = append([]string(nil), ev.Command...)
		}
		if len(ev.Images) > 0 {
			r.Images = append([]journal.Image(nil), ev.Images...)
		}
		e.evidence = nil
	}
	if len(e.attempts) > 0 {
		r.Attempts = append([]journal.Attempt(nil), e.attempts...)
		e.attempts = nil
	}
	_ = s
	return r
}

// evidence is what one step measured, accumulated while it runs.
type evidence struct {
	Mode    string
	Want    string
	Got     *float64
	OK      *bool
	Words   []string
	Missing []string
	Command []string
	Images  []journal.Image
}

// note records one image on the evidence being collected for this step.
func (e *Executor) note(img *journal.Image) {
	if img == nil {
		return
	}
	if e.evidence == nil {
		e.evidence = &evidence{}
	}
	e.evidence.Images = append(e.evidence.Images, *img)
}

// setEvidence merges a finished evidence value into the step's.
func (e *Executor) setEvidence(ev *evidence) {
	if ev == nil {
		return
	}
	if e.evidence == nil {
		e.evidence = ev
		return
	}
	cur := e.evidence
	if ev.Mode != "" {
		cur.Mode = ev.Mode
	}
	if ev.Want != "" {
		cur.Want = ev.Want
	}
	if ev.Got != nil {
		cur.Got = ev.Got
	}
	if ev.OK != nil {
		cur.OK = ev.OK
	}
	if len(ev.Words) > 0 {
		cur.Words = ev.Words
	}
	if len(ev.Missing) > 0 {
		cur.Missing = ev.Missing
	}
	if len(ev.Command) > 0 {
		cur.Command = ev.Command
	}
	cur.Images = append(cur.Images, ev.Images...)
}

// saveCrop writes a cropped image into the journal's artifact directory and describes it.
//
// THIS IS THE FILE THAT MAKES "WHAT DID IT SEE" ANSWERABLE. A `check: mode: like` compares two
// pictures and keeps NEITHER; the number it prints is a summary of evidence that is then discarded.
// Writing the crops costs two small PNGs per comparison and turns a report from a sentence into
// something a person can look at.
//
// A FAILURE TO WRITE IS NOT A FAILURE OF THE STEP. The picture is a convenience; the comparison has
// already happened and its verdict stands.
func (e *Executor) saveCrop(img image.Image, name, label, note string, rect []int) *journal.Image {
	if img == nil || e.Work == "" {
		return nil
	}
	dir := filepath.Join(e.Work, "journal")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil
	}
	path := filepath.Join(dir, name+".png")
	if err := screen.SavePNG(img, path); err != nil {
		e.Driver.Log(fmt.Sprintf("  journal: could not save %s: %v", name, err))
		return nil
	}
	b := img.Bounds()
	return e.imageOf(path, label, note, b.Dx(), b.Dy(), rect)
}

// saveDiff writes a picture of WHERE two images differ, for a comparison that did not match.
//
// A SIMILARITY NUMBER SAYS HOW MUCH, NOT WHERE, and ".71 similar" on a page that looks right to a
// person is exactly the case where the answer is a handful of pixels in one corner. The diff is grey
// where the two agree and red where they do not, so a reader can see the disagreement rather than
// infer it.
//
// IT IS WRITTEN ONLY FOR A FAILED COMPARISON. A passing one has nothing to explain, and the file
// would be one more thing on disk.
func (e *Executor) saveDiff(frame, reference image.Image, name string, rect []int) *journal.Image {
	if frame == nil || reference == nil || e.Work == "" {
		return nil
	}
	dir := filepath.Join(e.Work, "journal")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil
	}
	diff, err := screen.MarkDifference(frame, reference, 8)
	if err != nil {
		return nil
	}
	path := filepath.Join(dir, name+".png")
	if err := screen.SavePNG(diff, path); err != nil {
		return nil
	}
	b := diff.Bounds()
	return e.imageOf(path, "the difference", "red where the two disagree; grey where they agree",
		b.Dx(), b.Dy(), rect)
}