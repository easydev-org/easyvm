// Package report renders one run's journal as a SELF-CONTAINED HTML page, with its pictures and its
// video beside it.
//
// # WHY A STATIC PAGE AND NOT ONLY THE LIVE VIEW
//
// The live view is for watching. A report is for the other two things a person does with a run that
// failed: SENDING it to somebody, and READING it a week later when the container, the pod and the
// work directory are all gone. So the output is a directory that opens from a file:// URL with no
// server, no JavaScript framework and no network:
//
//	report/
//	  index.html              every step, newest state, and the evidence under each
//	  assets/<sha>.png        the library's pictures, addressed by content
//	  evidence/<step>-*.png   the crops a check compared (they are not in the library - they explain
//	                          one comparison and belong to that step, not to the run's assets)
//	  video.mp4               the recording, when there is one
//	  journal.jsonl           the whole journal, for `jq`
//
// # WHAT MAKES IT USEFUL RATHER THAN PRETTY
//
// EVERY FAILED CHECK SHOWS THE THREE PICTURES IT DECIDED FROM: the frame it saw, the reference it
// wanted, and a diff with the disagreement in red. That is the thing the log could never give - it
// printed `.7097 similar, wanted 0.7800..1.0000` about a page that looked correct, and the crops
// were local variables that were gone a second later.
//
// AND EVERY STEP CARRIES ITS OWN CALLS: the clicks, the keystrokes, the captures and the commands,
// with what each cost. A stage that "ran eight commands" and typed them into a search box looks
// exactly like one that ran them in a terminal until the calls are listed.
package report

import (
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/easydev-org/easyvm/internal/journal"
)

// Options is what a caller says about the run it wants reported.
type Options struct {
	// Work is the run's work directory: where `journal.db`, `artifact/` and the recording live.
	Work string
	// Run is the run id. Empty means the newest run in the journal.
	Run string
	// Out is the directory to write into. It is created if missing, and its `assets/` and `evidence/`
	// are filled from the work directory.
	Out string
	// Title is the page's heading. Empty means the run id.
	Title string
}

// Written is what a report produced, for a caller that wants to print it or upload it.
type Written struct {
	Index    string
	Out      string
	Steps    int
	Pictures int
	Events   int
	Videos   []string
}

// Build writes the report and returns where it is.
func Build(opts Options) (Written, error) {
	var out Written
	if opts.Work == "" || opts.Out == "" {
		return out, fmt.Errorf("a report needs `Work` and `Out`")
	}
	db, err := journal.OpenExisting(opts.Work)
	if err != nil {
		return out, err
	}
	defer db.Close()

	run := opts.Run
	if run == "" {
		runs, err := db.Runs()
		if err != nil {
			return out, err
		}
		if len(runs) == 0 {
			return out, fmt.Errorf("the journal in %s holds no runs", opts.Work)
		}
		run = runs[0].ID
	}

	steps, err := db.Steps(run, 0)
	if err != nil {
		return out, err
	}
	library, err := db.Library(run)
	if err != nil {
		return out, err
	}
	summary, err := db.Summary(run)
	if err != nil {
		return out, err
	}

	if err := os.MkdirAll(opts.Out, 0o755); err != nil {
		return out, err
	}

	// THE PICTURES ARE COPIED, not linked: a report is meant to survive the work directory it came
	// from, and a link would be a page whose pictures vanish with the container.
	copied, err := copyAssets(opts.Work, opts.Out, library)
	if err != nil {
		return out, err
	}
	out.Pictures = copied

	videos, err := copyVideos(opts.Work, opts.Out)
	if err != nil {
		return out, err
	}
	out.Videos = videos

	// THE TEXT EXPORT IS WRITTEN TOO, because a person with `jq` should not have to open a database
	// to read what a run did, and a CI artifact of a `.jsonl` is greppable where a `.db` is not.
	if p, err := db.Export(filepath.Join(opts.Out), run); err == nil {
		_ = p
	}

	title := opts.Title
	if title == "" {
		title = run
	}

	// EVERY STEP'S FULL RECORD AND ITS CALLS ARE COLLECTED BEFORE THE HTML IS WRITTEN, because the
	// page is generated in one pass over a slice rather than querying while it renders.
	details := make([]detail, 0, len(steps))
	for _, s := range steps {
		rec, err := db.Record(run, s.Seq)
		if err != nil {
			rec = s
		}
		evs, err := db.Events(run, rec.StepIndex, "")
		if err != nil {
			evs = nil
		}
		details = append(details, detail{rec: rec, events: evs})
		out.Events += len(evs)
	}
	out.Steps = len(details)

	index := filepath.Join(opts.Out, "index.html")
	f, err := os.Create(index)
	if err != nil {
		return out, err
	}
	defer f.Close()

	if err := render(f, run, title, details, summary, library, videos); err != nil {
		return out, err
	}
	out.Index = index
	out.Out = opts.Out
	return out, nil
}

// copyAssets copies the library's pictures into the report.
//
// A PICTURE THAT CANNOT BE FOUND IS SKIPPED rather than fatal: a journal can outlive a deleted
// artifact directory, and a report with one missing picture is worth more than no report.
func copyAssets(work, out string, library []journal.Image) (int, error) {
	dir := filepath.Join(out, "assets")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	n := 0
	for _, im := range library {
		src := filepath.Join(work, "artifact", safeKey(im.Sha), im.Sha+".png")
		dst := filepath.Join(dir, im.Sha+".png")
		if _, err := os.Stat(dst); err == nil {
			continue
		}
		if err := copyFile(src, dst); err != nil {
			continue
		}
		n++
	}
	return n, nil
}

// copyVideos copies the recordings beside the page.
//
// THE VIDEO IS NOT ALWAYS ONE FILE: a run that restarted a stage has several, and they are copied in
// name order so the page can list them oldest first.
func copyVideos(work, out string) ([]string, error) {
	entries, err := os.ReadDir(work)
	if err != nil {
		return nil, nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".mp4") {
			continue
		}
		if err := copyFile(filepath.Join(work, e.Name()), filepath.Join(out, e.Name())); err != nil {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	// THE EVIDENCE CROPS ARE COPIED TOO. They live under `journal/` in the work directory and are
	// what a failed check's three pictures come from.
	if err := copyTree(filepath.Join(work, "journal"), filepath.Join(out, "evidence")); err != nil {
		return names, nil
	}
	return names, nil
}

// safeKey is the first two characters of a content address, which is the directory it lives in.
func safeKey(sha string) string {
	if len(sha) < 2 {
		return "00"
	}
	return sha[:2]
}

// render writes the page.
// detail is one step with the calls it made, collected before the page is written.
type detail struct {
	rec    journal.Record
	events []journal.Event
}

func render(w *os.File, run, title string, details []detail, summary journal.Summary, library []journal.Image, videos []string) error {
	var b strings.Builder

	b.WriteString(`<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8">
<title>`)
	b.WriteString(html.EscapeString(title))
	b.WriteString(`</title>
<style>
:root{--fg:#1a1a1a;--dim:#666;--bg:#fff;--line:#e2e2e2;--ok:#0a7a32;--bad:#c02020;--warn:#a06000;--soft:#f6f7f8}
@media(prefers-color-scheme:dark){:root{--fg:#e8e8e8;--dim:#9a9a9a;--bg:#16181a;--line:#2c3033;--ok:#4ade80;--bad:#f87171;--warn:#fbbf24;--soft:#1e2124}}
*{box-sizing:border-box}
body{margin:0;padding:24px 20px 60px;background:var(--bg);color:var(--fg);
 font:14px/1.55 ui-sans-serif,system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}
h1{font-size:20px;margin:0 0 4px}
.sub{color:var(--dim);font-size:13px;margin-bottom:20px}
.summary{display:flex;flex-wrap:wrap;gap:8px;margin:0 0 24px}
.chip{background:var(--soft);border:1px solid var(--line);border-radius:999px;padding:3px 11px;font-size:12px}
.chip b{font-variant-numeric:tabular-nums}
ol.steps{list-style:none;margin:0;padding:0}
ol.steps>li{border:1px solid var(--line);border-radius:8px;margin:0 0 8px;overflow:hidden;background:var(--soft)}
summary.row{display:flex;align-items:center;gap:10px;padding:10px 14px;cursor:pointer;list-style:none}
summary.row::-webkit-details-marker{display:none}
.n{color:var(--dim);font-variant-numeric:tabular-nums;min-width:3.2em}
.name{font-weight:600;flex:1;overflow-wrap:anywhere}
.dur{color:var(--dim);font-variant-numeric:tabular-nums;font-size:12px}
.tag{font-size:11px;font-weight:700;letter-spacing:.04em;text-transform:uppercase;border-radius:4px;padding:2px 7px}
.t-passed{color:var(--ok);background:color-mix(in srgb,var(--ok) 12%,transparent)}
.t-failed{color:var(--bad);background:color-mix(in srgb,var(--bad) 12%,transparent)}
.t-optional{color:var(--warn);background:color-mix(in srgb,var(--warn) 14%,transparent)}
.t-skipped{color:var(--dim);background:color-mix(in srgb,var(--dim) 12%,transparent)}
.t-running{color:var(--dim);background:color-mix(in srgb,var(--dim) 12%,transparent)}
.body{padding:4px 14px 16px;border-top:1px solid var(--line)}
.msg{margin:12px 0;padding:10px 12px;border-left:3px solid var(--bad);background:color-mix(in srgb,var(--bad) 7%,transparent);
 border-radius:0 6px 6px 0;white-space:pre-wrap;overflow-wrap:anywhere;font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:12.5px}
.verdict{margin:12px 0;font-size:13px}
.verdict code{background:var(--soft);border:1px solid var(--line);border-radius:4px;padding:1px 5px}
.figs{display:flex;flex-wrap:wrap;gap:12px;margin:14px 0}
figure{margin:0;max-width:100%}
figure img{display:block;max-width:100%;height:auto;border:1px solid var(--line);border-radius:6px;background:#000}
figcaption{color:var(--dim);font-size:12px;margin-top:5px}
table{border-collapse:collapse;width:100%;margin:12px 0;font-size:12.5px}
th,td{text-align:left;padding:5px 9px;border-bottom:1px solid var(--line);vertical-align:top}
th{color:var(--dim);font-weight:600;font-size:11px;text-transform:uppercase;letter-spacing:.04em}
td.mono{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;overflow-wrap:anywhere}
td.ok{color:var(--ok)}td.bad{color:var(--bad)}
.words{display:flex;flex-wrap:wrap;gap:5px;margin:10px 0}
.w{font-family:ui-monospace,Menlo,monospace;font-size:12px;background:var(--soft);border:1px solid var(--line);
 border-radius:4px;padding:1px 6px}
.w.miss{border-color:var(--bad);color:var(--bad);text-decoration:line-through}
h2{font-size:15px;margin:28px 0 10px;padding-bottom:6px;border-bottom:1px solid var(--line)}
.library{display:flex;flex-wrap:wrap;gap:14px}
.library figure img{max-width:320px}
video{max-width:100%;border:1px solid var(--line);border-radius:6px;margin:10px 0}
details>summary{outline:none}
</style></head><body>
`)
	fmt.Fprintf(&b, "<h1>%s</h1>\n", html.EscapeString(title))
	fmt.Fprintf(&b, `<div class="sub">run <code>%s</code> &middot; %d steps &middot; %d external calls &middot; %d pictures</div>`+"\n",
		html.EscapeString(run), len(details), summary.Total, len(library))

	// THE SUMMARY STRIP: the audit's arithmetic, which is the first thing worth knowing.
	b.WriteString(`<div class="summary">`)
	fmt.Fprintf(&b, `<span class="chip">calls <b>%d</b></span>`, summary.Total)
	if summary.Failures > 0 {
		fmt.Fprintf(&b, `<span class="chip" style="color:var(--bad)">failed calls <b>%d</b></span>`, summary.Failures)
	}
	kinds := make([]string, 0, len(summary.ByKind))
	for k := range summary.ByKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		fmt.Fprintf(&b, `<span class="chip">%s <b>%d</b></span>`, html.EscapeString(k), summary.ByKind[k])
	}
	b.WriteString("</div>\n")

	for _, v := range videos {
		fmt.Fprintf(&b, `<video controls preload="metadata" src="%s"></video>`+"\n", html.EscapeString(v))
	}

	// THE STEPS.
	b.WriteString(`<h2>Steps</h2>` + "\n" + `<ol class="steps">` + "\n")
	for i, d := range details {
		renderStep(&b, i+1, d.rec, d.events)
	}
	b.WriteString("</ol>\n")

	// THE LIBRARY: the named pictures, in the order they were captured.
	if len(library) > 0 {
		b.WriteString(`<h2>Picture library</h2>` + "\n" + `<div class="library">` + "\n")
		for _, im := range library {
			b.WriteString("<figure>")
			fmt.Fprintf(&b, `<img loading="lazy" src="assets/%s.png" alt="%s">`,
				html.EscapeString(im.Sha), html.EscapeString(im.Name))
			fmt.Fprintf(&b, "<figcaption>%s<br><span style=\"font-size:11px\">%s</span></figcaption></figure>",
				html.EscapeString(im.Name), html.EscapeString(im.Sha))
		}
		b.WriteString("</div>\n")
	}

	if len(summary.Slowest) > 0 {
		b.WriteString(`<h2>Slowest calls</h2>` + "\n" + "<table><tr><th>step</th><th>kind</th><th>detail</th><th>ms</th></tr>\n")
		for _, c := range summary.Slowest {
			fmt.Fprintf(&b, `<tr><td>%d %s</td><td>%s</td><td class="mono">%s</td><td>%d</td></tr>`+"\n",
				c.StepIndex, html.EscapeString(c.Step), html.EscapeString(c.Kind),
				html.EscapeString(truncate(c.Detail, 90)), c.DurationMS)
		}
		b.WriteString("</table>\n")
	}

	b.WriteString("</body></html>\n")
	_, err := w.WriteString(b.String())
	return err
}

// renderStep writes one step, collapsed to its name and verdict.
//
// THE DETAIL IS IN A `<details>` ELEMENT rather than an always-open block, because a report of forty
// steps where every one shows its calls is a page nobody can scan. What is always visible is what a
// reader scans for: the number, the name, the verdict and how long it took.
func renderStep(b *strings.Builder, n int, r journal.Record, events []journal.Event) {
	status := string(r.Status)
	if status == "" {
		status = "running"
	}
	b.WriteString("<li><details><summary class=\"row\">")
	fmt.Fprintf(b, `<span class="n">%d</span>`, n)
	fmt.Fprintf(b, `<span class="name">%s</span>`, html.EscapeString(r.Step))
	if r.DurationMS > 0 {
		fmt.Fprintf(b, `<span class="dur">%s</span>`, shortDuration(r.DurationMS))
	}
	fmt.Fprintf(b, `<span class="tag t-%s">%s</span>`, status, status)
	b.WriteString("</summary><div class=\"body\">\n")

	if r.Message != "" {
		fmt.Fprintf(b, `<div class="msg">%s</div>`+"\n", html.EscapeString(r.Message))
	}

	// THE VERDICT OF A CHECK, with its threshold and its actual value side by side.
	if r.Mode != "" || r.Got != nil || r.OK != nil {
		b.WriteString(`<div class="verdict">`)
		if r.Mode != "" {
			fmt.Fprintf(b, `mode <code>%s</code> `, html.EscapeString(r.Mode))
		}
		if r.Got != nil {
			fmt.Fprintf(b, `read <code>%.4f</code> `, *r.Got)
		}
		if r.Want != "" {
			fmt.Fprintf(b, `wanted <code>%s</code> `, html.EscapeString(r.Want))
		}
		if r.OK != nil {
			if *r.OK {
				b.WriteString(`<b style="color:var(--ok)">matched</b>`)
			} else {
				b.WriteString(`<b style="color:var(--bad)">did not match</b>`)
			}
		}
		b.WriteString(`</div>` + "\n")
	}

	// THE PICTURES: the frame, the reference and the diff.
	if len(r.Images) > 0 {
		b.WriteString(`<div class="figs">`)
		for _, im := range r.Images {
			src := pictureSrc(im)
			if src == "" {
				continue
			}
			fmt.Fprintf(b, `<figure><img loading="lazy" src="%s" alt="%s">`, html.EscapeString(src),
				html.EscapeString(im.Label))
			cap := im.Label
			if len(im.Rect) == 4 {
				cap += fmt.Sprintf(" &middot; rect %d,%d–%d,%d", im.Rect[0], im.Rect[1], im.Rect[2], im.Rect[3])
			}
			if im.Note != "" {
				cap += " &middot; " + html.EscapeString(im.Note)
			}
			fmt.Fprintf(b, `<figcaption>%s</figcaption></figure>`, cap)
		}
		b.WriteString(`</div>` + "\n")
	}

	// WHAT THE OCR READ, with the wanted words struck through when they were absent.
	if len(r.Words) > 0 {
		fmt.Fprintf(b, `<div class="sub" style="margin:12px 0 6px">read %d word(s)</div>`, len(r.Words))
		b.WriteString(`<div class="words">`)
		missing := map[string]bool{}
		for _, m := range r.Missing {
			missing[strings.ToLower(m)] = true
		}
		for _, word := range r.Words {
			cls := "w"
			if missing[strings.ToLower(word)] {
				cls += " miss"
			}
			fmt.Fprintf(b, `<span class="%s">%s</span>`, cls, html.EscapeString(word))
		}
		b.WriteString(`</div>` + "\n")
	}

	// THE COMMANDS this step sent.
	for _, line := range r.Command {
		fmt.Fprintf(b, `<div class="mono" style="font-family:ui-monospace,Menlo,monospace;font-size:12.5px;background:var(--soft);border:1px solid var(--line);border-radius:6px;padding:8px 10px;margin:8px 0;white-space:pre-wrap;overflow-wrap:anywhere">%s</div>`+"\n",
			html.EscapeString(line))
	}

	// A LOOP'S ATTEMPTS: which one came closest, and what each saw.
	if len(r.Attempts) > 0 {
		b.WriteString("<table><tr><th>#</th><th>value</th><th>read</th><th>held</th></tr>\n")
		for _, a := range r.Attempts {
			held := `<span style="color:var(--dim)">no</span>`
			if a.Held {
				held = `<span style="color:var(--ok)">yes</span>`
			}
			val := ""
			if a.Value != 0 {
				val = fmt.Sprintf("%.4f", a.Value)
			}
			fmt.Fprintf(b, `<tr><td>%d</td><td>%s</td><td class="mono">%s</td><td>%s</td></tr>`+"\n",
				a.N, val, html.EscapeString(truncate(a.Read, 70)), held)
		}
		b.WriteString("</table>\n")
	}

	// EVERY EXTERNAL CALL this step made.
	if len(events) > 0 {
		fmt.Fprintf(b, `<div class="sub" style="margin:14px 0 6px">%d external call(s)</div>`, len(events))
		b.WriteString("<table><tr><th>time</th><th>kind</th><th>detail</th><th>ms</th></tr>\n")
		for _, e := range events {
			cls := ""
			if e.OK != nil {
				if *e.OK {
					cls = "ok"
				} else {
					cls = "bad"
				}
			}
			detail := e.Detail
			if e.Error != "" {
				detail += " — " + e.Error
			}
			fmt.Fprintf(b, `<tr><td class="mono">%s</td><td>%s</td><td class="mono %s">%s</td><td>%d</td></tr>`+"\n",
				html.EscapeString(shortTime(e.Time)), html.EscapeString(string(e.Kind)),
				cls, html.EscapeString(truncate(detail, 100)), e.DurationMS)
		}
		b.WriteString("</table>\n")
	}

	b.WriteString("</div></details></li>\n")
}

// pictureSrc is where a record's picture is in the report.
//
// A LIBRARY PICTURE IS `assets/`, a check's crop is `evidence/`. The distinction is the one
// `journal.Image` makes: a content address is part of the run's library, a path in the work directory
// is evidence for one comparison.
func pictureSrc(im journal.Image) string {
	if im.Sha != "" {
		return "assets/" + im.Sha + ".png"
	}
	if im.Rel != "" {
		// The work-directory crops are copied under `evidence/`, keeping only their file name: the
		// step name is already in it, and a nested path from another machine is not portable.
		return "evidence/" + filepath.Base(im.Rel)
	}
	return ""
}

// shortDuration renders a millisecond count the way a person reads it.
func shortDuration(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", ms)
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
}

// shortTime keeps only the time of day, which is what a reader compares against the video.
func shortTime(t string) string {
	if len(t) >= 19 {
		return t[11:23]
	}
	return t
}

// truncate shortens a string for a table cell.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// copyFile copies one file.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := out.ReadFrom(in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}

// copyTree copies every regular file under src into dst, flattened.
func copyTree(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if filepath.Ext(e.Name()) != ".png" {
			continue
		}
		_ = copyFile(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name()))
	}
	return nil
}

// MarshalJSONLenient is here so a caller can dump a record without a cycle through the package.
func MarshalJSONLenient(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}