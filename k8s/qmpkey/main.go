// Command qmpkey drives a RUNNING guest directly, without going through `easyvm run`.
//
// THE LAB NEEDS THIS AND THE WALKTHROUGH CANNOT PROVIDE IT. `easyvm run` refuses to start a second
// walk while one is in progress - measured:
//
//	REFUSED: a run is already in progress (step "wait for the launcher's cards"); stop it first
//
// and `easyvm stop` KILLS THE GUEST, which throws away the live session that is the whole reason for
// probing. So a probe has to be a separate, smaller tool: open the same QMP socket, send one event,
// take one frame.
//
// IT IS NOT PART OF `easyvm` ON PURPOSE. `easyvm` is the daemon and the walkthrough engine; this is
// an instrument for looking at a machine that is already up, used by `k8s/watch.sh` and by hand.
// Putting it in the main binary would mean shipping a debugging tool to every user.
//
//	go build -o /tmp/qmpkey ./k8s/qmpkey
//	/tmp/qmpkey -qmp /work/w/drive.sock.qmp -key meta_l -shot /work/shots/super.png
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/easydev-org/easyvm/internal/keys"
	"github.com/easydev-org/easyvm/internal/qmp"
)

func main() {
	qmpPath := flag.String("qmp", "", "the QMP socket of a RUNNING guest")
	key := flag.String("key", "", "one qcombo to send, e.g. `meta_l` or `ctrl+alt+t`")
	text := flag.String("type", "", "text to type")
	click := flag.String("click", "", "an x,y pixel pair to click")
	shot := flag.String("shot", "", "where to write a PNG-ready PPM frame")
	wait := flag.Duration("wait", 30*time.Second, "how long to wait for the socket")
	flag.Parse()

	if *qmpPath == "" {
		fmt.Fprintln(os.Stderr, "qmpkey: -qmp is required")
		os.Exit(2)
	}
	c, err := qmp.Open(*qmpPath, *wait)
	if err != nil {
		fmt.Fprintf(os.Stderr, "qmpkey: %v\n", err)
		os.Exit(1)
	}
	defer c.Close()

	if *key != "" {
		strokes, err := keys.ParseCombo(*key)
		if err != nil {
			fmt.Fprintf(os.Stderr, "qmpkey: %v\n", err)
			os.Exit(2)
		}
		for _, s := range strokes {
			if err := c.Stroke(s.Modifiers, s.Key); err != nil {
				fmt.Fprintf(os.Stderr, "qmpkey: key %q: %v\n", *key, err)
				os.Exit(1)
			}
		}
		fmt.Printf("  key %s\n", *key)
	}

	if *text != "" {
		// `keys.ForText` IS THE SAME CALL `type:` USES, so what this tool sends is byte for byte what
		// a walkthrough step sends - a second typing implementation would be a second thing to be
		// wrong about, which is the whole class of bug this session keeps finding.
		strokes, err := keys.ForText(*text)
		if err != nil {
			fmt.Fprintf(os.Stderr, "qmpkey: %v\n", err)
			os.Exit(1)
		}
		for _, s := range strokes {
			if err := c.Stroke(s.Modifiers, s.Key); err != nil {
				fmt.Fprintf(os.Stderr, "qmpkey: typing: %v\n", err)
				os.Exit(1)
			}
		}
		fmt.Printf("  typed %d character(s)\n", len(*text))
	}

	if *click != "" {
		var x, y int
		if _, err := fmt.Sscanf(*click, "%d,%d", &x, &y); err != nil {
			fmt.Fprintf(os.Stderr, "qmpkey: -click wants x,y; got %q\n", *click)
			os.Exit(2)
		}
		if err := c.Click(x, y); err != nil {
			fmt.Fprintf(os.Stderr, "qmpkey: click: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("  clicked (%d, %d)\n", x, y)
	}

	if *shot != "" {
		// `Screendump` writes into a directory and returns the frame it wrote, so the file is named
		// by the same code the recording uses rather than by this tool.
		dir := *shot
		if fi, err := os.Stat(dir); err == nil && !fi.IsDir() {
			fmt.Fprintf(os.Stderr, "qmpkey: -shot wants a directory; %q is a file\n", dir)
			os.Exit(2)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "qmpkey: %v\n", err)
			os.Exit(1)
		}
		f, err := c.Screendump(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "qmpkey: screendump: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("  frame %s\n", f.Path)
	}
}
