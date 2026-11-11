// Command easyvm drives a walkthrough YAML file against a machine.
//
// THERE IS ONE OWNER OF THE MACHINE, AND IT IS A DAEMON.
//
// This command is a CLIENT. It sends a request to a daemon over a unix socket and prints what came
// back. It does not start qemu, does not open a QMP socket, and cannot. That is a deliberate limit
// with a structural reason: the step executor is a state machine with a position, and a position
// held in a CLI's local variable dies with the process - taking the qemu it started with it. At
// step twelve of twenty-nine, nobody can look at the screen, no shell is attached, and the next
// attempt costs a fifteen-gigabyte pull.
//
// THE ALTERNATIVE - a CLI that embeds the engine and a daemon that hosts it - is two engines. They
// would drift, and the drift would show up as a run that behaves differently under CI than under a
// daemon, which is the worst possible pair to have to tell apart.
//
// SO THE COMMANDS SPLIT IN TWO GROUPS:
//
//	without a daemon   check, list, versions    - pure questions about a file or an image
//	with a daemon      serve, run, status, stop, health
//
// `check` and `list` are questions with no machine in them: a file's vocabulary and its resolved
// variables. Requiring a daemon for those would mean a validator that cannot run on a machine where
// the daemon is not up yet, which is exactly when a file needs validating.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/easydev-org/easyvm/internal/client"
	"github.com/easydev-org/easyvm/internal/daemon"
	"github.com/easydev-org/easyvm/internal/machine"
	"github.com/easydev-org/easyvm/internal/walk"
)

const usage = `easyvm - drive a walkthrough YAML file against a machine

The machine has ONE owner: a daemon. This command is a client and sends it requests.
` + "`check`, `list` and `versions` are questions about a file, not about a machine, and need no daemon." + `

  easyvm serve   --work DIR [--novnc-port N]     start the owner
  easyvm run     FILE [flags]                    ask it to run a walkthrough
  easyvm status  [--work DIR]                    where is it now, and is the guest up
  easyvm stop    [--work DIR]                    stop the run and the machine
  easyvm health  [--work DIR]                    exit 0 only if QMP and the guest both answer

  easyvm check   FILE                            validate without any machine at all
  easyvm list    FILE                            one line per step
  easyvm versions                                what this build expects of qemu

run flags:
  --work DIR        the daemon's directory (its socket is here)
  --boot-disk PATH  the disk a boot: step starts from
  --out PATH        where a compress: step writes
  --password PW     the guest account's password
  --novnc-port N    where the browser page listens; 0 disables it

every guest starts with -cpu ` + machine.CPUModel + `; -cpu host is refused.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	args := os.Args[2:]
	switch os.Args[1] {
	case "check", "list":
		os.Exit(cmdInspect(os.Args[1], args))
	case "versions":
		os.Exit(cmdVersions())
	case "serve":
		os.Exit(cmdServe(args))
	case "run":
		os.Exit(cmdRun(args))
	case "status":
		os.Exit(cmdStatus(args))
	case "stop":
		os.Exit(cmdStop(args))
	case "health":
		os.Exit(cmdHealth(args))
	case "-h", "--help", "help":
		fmt.Print(usage)
		os.Exit(0)
	}
	fmt.Fprintf(os.Stderr, "easyvm: unknown command %q\n\n%s", os.Args[1], usage)
	os.Exit(2)
}

// --- questions about a file, no machine involved ----------------------------------------

func cmdInspect(mode string, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "easyvm check: a walkthrough file is required")
		return 2
	}
	path := args[0]
	f, err := walk.Load(path, gatherOverrides(args[1:]))
	if err != nil {
		// THE ERROR IS THE OUTPUT. A validator that prints "invalid" and exits has made the
		// reader open the file to find out why; this one already knows.
		fmt.Fprintf(os.Stderr, "FAILED: %v\n", err)
		return 1
	}

	fmt.Printf("%s: %s, %d step(s)\n", path, f.Name, len(f.Steps))
	fmt.Printf("  guest: %dx%d, ssh %s@%d\n",
		f.Resolution[0], f.Resolution[1], f.SSHUser, f.SSHPort)
	fmt.Printf("  cpu:   %s (fixed; -cpu host is refused)\n", machine.CPUModel)
	if len(f.Vars) > 0 {
		names := make([]string, 0, len(f.Vars))
		for k := range f.Vars {
			names = append(names, k)
		}
		sort.Strings(names)
		var rendered []string
		for _, n := range names {
			rendered = append(rendered, fmt.Sprintf("%s=%v", n, f.Vars[n]))
		}
		fmt.Printf("  vars:  %s\n", strings.Join(rendered, " "))
	}

	for _, s := range f.Steps {
		keys := make([]string, 0, len(s.Spec))
		for k := range s.Spec {
			if k != "name" {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		if mode == "check" {
			fmt.Printf("  %2d %-44s %s\n", s.Index, s.Name, strings.Join(keys, ", "))
			continue
		}
		fmt.Printf("%d\t%s\t%s\n", s.Index, s.Name, strings.Join(keys, ","))
	}
	return 0
}

func cmdVersions() int {
	fmt.Printf("cpu model  %s\n", machine.CPUModel)
	// THE BINARIES ARE NAMED BUT NOT INVOKED, so this works on a machine with no qemu - which is
	// where a file needs validating.
	for _, tool := range []string{"qemu-system-x86_64", "qemu-img", "smbd", "websockify"} {
		if p, err := exec.LookPath(tool); err == nil {
			fmt.Printf("%-18s %s\n", tool, p)
		} else {
			fmt.Printf("%-18s (not on PATH)\n", tool)
		}
	}
	return 0
}

// --- the daemon -----------------------------------------------------------------------

func cmdServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	work := fs.String("work", "", "the directory the socket and the frames live in")
	novncPort := fs.Int("novnc-port", daemon.DefaultNovNCPort, "where the noVNC page listens; 0 disables it")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *work == "" {
		fmt.Fprintln(os.Stderr, "easyvm serve: --work is required; the socket lives there")
		return 2
	}

	d := daemon.New()
	fmt.Printf("[easyvm] daemon for %s\n", *work)
	err := d.Serve(daemon.ServeOptions{
		Work:      *work,
		NoVNCPort: *novncPort,
		Ready: func(sock string) {
			// THE READY LINE IS A CONTRACT. A supervisor - or a test - waits for it rather than
			// polling for the socket file, because a file that exists is not a daemon that
			// answers.
			fmt.Printf("[easyvm] listening on %s\n", sock)
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAILED: %v\n", err)
		return 1
	}
	fmt.Println("[easyvm] stopped")
	return 0
}

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	work := fs.String("work", "", "the daemon's directory")
	bootDisk := fs.String("boot-disk", "", "the disk a boot: step starts from")
	out := fs.String("out", "", "where a compress: step writes")
	password := fs.String("password", "admin", "the guest account's password")
	// FLAGS AFTER THE FILE ARE STILL FLAGS. Go's flag package stops at the first non-flag argument,
	// so `easyvm run f.yaml --work /tmp/x` would leave --work unread and the work directory at its
	// default - which is the invocation people actually type.
	rest := fs.Args()
	_ = rest
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest = fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "easyvm run: a walkthrough file is required")
		return 2
	}
	file := rest[0]
	if len(rest) > 1 {
		if err := fs.Parse(rest[1:]); err != nil {
			return 2
		}
	}

	// A FILE THAT CANNOT LOAD IS REFUSED HERE, not by the daemon. The daemon would refuse it too,
	// but the client has the file on disk and can say what is wrong without a round trip - and a
	// client that hands over a broken file has made the daemon's log the place to look for a
	// mistake that is not its own.
	if _, err := walk.Load(file, map[string]string{"args.out": *out}); err != nil {
		fmt.Fprintf(os.Stderr, "FAILED: %v\n", err)
		return 1
	}

	workDir := daemon.SocketDir(file, *work)
	cl, err := client.Dial(workDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n\n  A daemon owns the machine, so it must be started first:\n"+""+
			"    easyvm serve --work %s\n  then this command again.\n", err, workDir)
		return 3
	}
	defer cl.Close()

	resp, err := cl.Do(client.Request{
		Command:  "run",
		File:     absPath(file),
		Work:     workDir,
		BootDisk: *bootDisk,
		Out:      *out,
		Password: *password,
		// THE DAEMON'S OWN --novnc-port WINS, because the page's port is a fact about the
		// machine's host rather than about this file. A client that passed one here would be
		// overriding a decision it cannot see.
		NoVNCPort: 0,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAILED: %v\n", err)
		return 1
	}
	if !resp.OK {
		fmt.Fprintf(os.Stderr, "REFUSED: %s\n", resp.Error)
		return 1
	}
	printState(resp.State)
	fmt.Printf("\n[easyvm] the daemon is driving it; `easyvm status --work %s` to follow\n", workDir)
	return 0
}

func cmdStatus(args []string) int {
	work, code := workArg("status", args)
	if code != 0 {
		return code
	}
	cl, err := client.Dial(work)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 3
	}
	defer cl.Close()

	resp, err := cl.Do(client.Request{Command: "status"})
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAILED: %v\n", err)
		return 1
	}
	if !resp.OK {
		fmt.Fprintf(os.Stderr, "REFUSED: %s\n", resp.Error)
		return 1
	}
	printState(resp.State)
	switch resp.State.Status {
	case "failed":
		return 2
	case "finished":
		return 0
	}
	return 0
}

func cmdStop(args []string) int {
	work, code := workArg("stop", args)
	if code != 0 {
		return code
	}
	cl, err := client.Dial(work)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 3
	}
	defer cl.Close()

	resp, err := cl.Do(client.Request{Command: "stop"})
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAILED: %v\n", err)
		return 1
	}
	if !resp.OK {
		fmt.Fprintf(os.Stderr, "REFUSED: %s\n", resp.Error)
		return 1
	}
	fmt.Println("[easyvm] stopping")
	return 0
}

func cmdHealth(args []string) int {
	work, code := workArg("health", args)
	if code != 0 {
		return code
	}
	cl, err := client.Dial(work)
	if err != nil {
		// A HEALTH CHECK MUST BE HONEST ABOUT ITS OWN FAILURE. A container that reported healthy
		// because the probe could not reach the daemon is the failure mode `pgrep` has, one level
		// up.
		fmt.Fprintf(os.Stderr, "unhealthy: %v\n", err)
		return 1
	}
	defer cl.Close()

	resp, err := cl.Do(client.Request{Command: "status"})
	if err != nil || !resp.OK {
		fmt.Fprintf(os.Stderr, "unhealthy: %v %s\n", err, resp.Error)
		return 1
	}
	// HEALTH IS ASKED OF THE PRODUCT, NOT OF THE PROCESS. `pgrep qemu-system-x86_64` succeeds over a
	// guest that panicked, because the process is still there. What matters is whether the machine
	// answers - and the daemon is the only thing that can say.
	switch resp.State.Status {
	case "running", "waiting", "starting":
		fmt.Printf("healthy: %s at %q\n", resp.State.Status, resp.State.Step)
		return 0
	case "finished":
		fmt.Printf("healthy: finished (%s)\n", resp.State.Message)
		return 0
	}
	fmt.Fprintf(os.Stderr, "unhealthy: %s %s\n", resp.State.Status, resp.State.Error)
	return 1
}

func workArg(cmd string, args []string) (string, int) {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	work := fs.String("work", "", "the daemon's directory")
	if err := fs.Parse(args); err != nil {
		return "", 2
	}
	if *work == "" {
		fmt.Fprintf(os.Stderr, "easyvm %s: --work is required, because the socket lives in it\n", cmd)
		return "", 2
	}
	return *work, 0
}

func printState(st *daemon.State) {
	if st == nil {
		return
	}
	where := ""
	if st.StepCount > 0 {
		where = fmt.Sprintf(" step %d/%d %q", st.StepIndex, st.StepCount, st.Step)
	}
	fmt.Printf("status: %s%s\n", st.Status, where)
	if st.File != "" {
		fmt.Printf("  file:    %s\n", st.File)
	}
	if st.NoVNCPort > 0 {
		fmt.Printf("  noVNC:   http://127.0.0.1:%d/vnc.html\n", st.NoVNCPort)
	}
	if st.SSH != "" {
		fmt.Printf("  ssh:     %s\n", st.SSH)
	}
	if st.Message != "" {
		fmt.Printf("  message: %s\n", st.Message)
	}
	if st.Error != "" {
		fmt.Printf("  error:   %s\n", st.Error)
	}
	if len(st.Answered) > 0 {
		fmt.Printf("  answered %d step(s)\n", len(st.Answered))
	}
}

func absPath(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

// gatherOverrides reads `--name value` pairs from a check/list invocation, so those two commands
// resolve vars exactly as `run` would. A check that used different values than the run would
// validate a file nobody is going to execute.
func gatherOverrides(args []string) map[string]string {
	out := map[string]string{}
	for i := 0; i+1 < len(args); i++ {
		if !strings.HasPrefix(args[i], "--") {
			continue
		}
		name := strings.TrimPrefix(args[i], "--")
		switch name {
		case "user", "password", "out", "boot-timeout":
			out["args."+name] = args[i+1]
			i++
		}
	}
	return out
}

var _ = time.Now
