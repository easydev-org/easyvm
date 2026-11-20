package main

import (
	"fmt"
	"os"

	"github.com/easydev-org/easyvm/internal/report"
)

// cmdReport renders one run's journal as a self-contained HTML page with its pictures and video.
//
//	easyvm report --work /work --out report/
//	easyvm report --work /work --out report/ --run 2026-11-20T10-30-21
//
// IT READS THE JOURNAL AND WRITES FILES; it does not need a daemon and it does not need the machine.
// That is the point: the two things a person does with a failed run are SEND it and READ it later,
// and by then the container that produced it is gone.
func cmdReport(args []string) int {
	opts := report.Options{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--work":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "report: --work needs a directory")
				return 2
			}
			opts.Work = args[i]
		case "--out":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "report: --out needs a directory")
				return 2
			}
			opts.Out = args[i]
		case "--run":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "report: --run needs a run id")
				return 2
			}
			opts.Run = args[i]
		case "--title":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "report: --title needs a string")
				return 2
			}
			opts.Title = args[i]
		case "-h", "--help":
			fmt.Print(reportUsage)
			return 0
		default:
			fmt.Fprintf(os.Stderr, "report: unknown argument %q\n\n%s", args[i], reportUsage)
			return 2
		}
	}
	if opts.Work == "" {
		opts.Work = "work"
	}
	if opts.Out == "" {
		opts.Out = "report"
	}

	w, err := report.Build(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "report: %v\n", err)
		return 1
	}
	fmt.Printf("%s\n", w.Index)
	fmt.Printf("  %d step(s), %d external call(s), %d picture(s)\n", w.Steps, w.Events, w.Pictures)
	for _, v := range w.Videos {
		fmt.Printf("  video: %s\n", v)
	}
	return 0
}

const reportUsage = `easyvm report - write a run's journal as a self-contained HTML page

  easyvm report [--work DIR] [--out DIR] [--run ID] [--title TEXT]

  --work   the run's work directory, holding journal.db, artifact/ and the recording (default: work)
  --out    where to write the page and its pictures (default: report)
  --run    the run to report; default is the newest in the journal
  --title  the page's heading; default is the run id

The output is a directory that opens from a file:// URL with no server: index.html, assets/ for the
named pictures, evidence/ for the crops a check compared, and any recording beside it.
`
