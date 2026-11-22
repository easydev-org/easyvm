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
//	without a daemon   pull                     - fetch a published disk from the registry
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
	"github.com/easydev-org/easyvm/internal/protocol"
	"github.com/easydev-org/easyvm/internal/walk"
)

const usage = `easyvm - drive a walkthrough YAML file against a machine

The machine has ONE owner: a daemon. This command is a client and sends it requests.
` + "`check`, `list` and `versions` are questions about a file, not about a machine, and need no daemon." + `

TWO MODES, AND THEY DIFFER IN WHEN THE MACHINE STOPS:

  easyvm run     FILE [flags]                    A BAKE: run every step, then stop the machine.
  easyvm create  FILE [flags]                    A SANDBOX: boot and STAY UP until destroy.

  easyvm serve   --work DIR                       start the owner
  easyvm status  [--work DIR]                    where is it now, and is the guest up
  easyvm stop    [--work DIR]                    stop the run and the machine
  easyvm destroy [--work DIR]                    stop a sandbox's machine (its only ending)
  easyvm health  [--work DIR]                    exit 0 only if QMP and the guest both answer
  easyvm report  [--work DIR] [--out DIR]        write the run's journal as a self-contained page

  easyvm check   FILE                            validate without any machine at all
  easyvm list    FILE                            one line per step
  easyvm versions                                what this build expects of qemu

run flags:
  --work DIR        the daemon's directory (its socket is here)
  --boot-disk PATH  the disk a boot: step starts from
  --out PATH        where a compress: step writes
  --password PW     the guest account's password
  --name=value       set one of the file's own variables (e.g. --shutdown=kill)

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
	case "create":
		os.Exit(cmdCreate(args))
	case "destroy":
		os.Exit(cmdDestroy(args))
	case "status":
		os.Exit(cmdStatus(args))
	case "stop":
		os.Exit(cmdStop(args))
	case "health":
		os.Exit(cmdHealth(args))
	case "report":
		os.Exit(cmdReport(args))
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
	overrides := gatherOverrides(args[1:])
	// `work` IS A RUN FACT THE FILE MAY NAME, so `check` resolves it the same way `run` does. A
	// validator that could not resolve `{{ work }}` would refuse a file the daemon accepts, which
	// makes it useless for the thing it exists for.
	if _, ok := overrides["args.work"]; !ok {
		overrides["args.work"] = filepath.Dir(absPath(path))
	}
	f, err := walk.Load(path, overrides)
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
	//
	// websockify IS NOT ON THIS LIST BECAUSE IT IS NO LONGER USED: the console is served by
	// internal/daemon, which bridges the WebSocket itself. Listing it would be a build that claims
	// a dependency it does not have, and the next reader would go looking for the package.
	//
	// THE LIST IS THE HOST-SIDE COMMANDS THE PROGRAM ACTUALLY RUNS: the machine, the disks, the
	// share's server, and the network's four.
	for _, tool := range []string{
		"qemu-system-x86_64", "qemu-img",
		"smbd", "dnsmasq", "ip", "iptables", "sysctl",
		"ffmpeg", "tesseract",
	} {
		if p, err := exec.LookPath(tool); err == nil {
			fmt.Printf("%-18s %s\n", tool, p)
		} else {
			fmt.Printf("%-18s (not on PATH)\n", tool)
		}
	}
	return 0
}

func cmdServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	work := fs.String("work", "", "the directory the frames and the guest scratch live in")
	httpAddr := fs.String("http", daemon.DefaultHTTPAddr, "the browser surface: a live screen, a "+
		"click map, keys and typing. Empty disables it")
	shapesDir := fs.String("shapes", "", "a directory of shape files to use INSTEAD of the ones "+
		"compiled into this binary. A shape says what machine an `os:` means - firmware, CPU flags, "+
		"devices - so a repository can add an operating system this build does not ship")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *work == "" {
		fmt.Fprintln(os.Stderr, "easyvm serve: --work is required; the frames and the guest scratch live there")
		return 2
	}

	d := daemon.New()
	fmt.Printf("[easyvm] daemon for %s\n", *work)

	// ONE LISTENER, STARTED BY `Serve`. It used to be started HERE as a goroutine and again inside
	// `Serve`, which bound :8080 twice and produced
	//
	//	FAILED: http listen :8080: listen tcp :8080: bind: address already in use
	//
	// on a daemon that had done nothing wrong. The address is `Serve`'s business now, and this is the
	// only place it is decided.
	err := d.Serve(daemon.ServeOptions{
		Work:      *work,
		HTTPAddr:  *httpAddr,
		ShapesDir: *shapesDir,
		Ready: func(addr string) {
			// THE READY LINE IS A CONTRACT. A supervisor - or a test - waits for it rather than
			// polling for a socket file, because a file that exists is not a daemon that answers.
			fmt.Printf("[easyvm] listening on http://%s/\n", addr)
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
	//
	// AND A `--name=value` THAT IS NOT A FLAG IS THE FILE'S OWN VARIABLE, so it has to be taken out
	// before `fs.Parse` sees it. THE TWO ARE THE SAME SHAPE ON THE COMMAND LINE and the flag package
	// cannot tell them apart: `--shutdown=kill` is not declared here because it is a variable of the
	// walkthrough, and `fs.Parse` answered
	//
	//	flag provided but not defined: -shutdown
	//
	// which is a usage error for something the help text advertises. Splitting them first is what
	// makes `--shutdown=kill` and `--mirror=<url>` work at all - measured on this build, where
	// passing either returned exit 2 before running a single step.
	flags, named := splitVars(args)
	if err := fs.Parse(flags); err != nil {
		return 2
	}
	rest := fs.Args()
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
	if _, err := walk.Load(file, map[string]string{
		"args.out":  *out,
		"args.work": workDirFor(file, *work),
	}); err != nil {
		fmt.Fprintf(os.Stderr, "FAILED: %v\n", err)
		return 1
	}

	workDir := daemon.SocketDir(file, *work)
	cl, err := connect(workDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n\n  A daemon owns the machine, so it must be started first:\n"+""+
			"    easyvm serve --work %s\n  then this command again.\n", err, workDir)
		return 3
	}
	defer cl.Close()

	// THE FILE'S OWN VARIABLES COME FROM THE COMMAND LINE, because a run fact is not a property of
	// the walkthrough: the same file runs against a throwaway overlay (`shutdown: kill`) and against
	// a disk it owns (`shutdown: ssh`).
	vars := named

	resp, err := cl.Do(client.Request{
		Command:  "run",
		File:     absPath(file),
		Work:     workDir,
		BootDisk: *bootDisk,
		Out:      *out,
		Password: *password,
		Vars:     vars,
		// THE DAEMON'S OWN --novnc-port WINS, because the page's port is a fact about the
		// machine's host rather than about this file. A client that passed one here would be
		// overriding a decision it cannot see.
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
	cl, err := connect(work)
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
	cl, err := connect(work)
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
	cl, err := connect(work)
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

// connect opens a client through whichever transport is configured.
//
// EASYVM_HTTP IS THE SWITCH, AND IT IS AN ENVIRONMENT VARIABLE RATHER THAN A FLAG ON EVERY COMMAND.
// The transport is a property of where the daemon is, not of what you are asking it - a person with a
// URL exports it once and every `easyvm status`, `stop` and `create` follows. A flag would have to be
// repeated on six subcommands and would be forgotten on the seventh.
//
//	unset                  the unix socket in the work directory, which is what CI uses
//	EASYVM_HTTP=:8080      the daemon's browser surface
//
// THE COMMANDS DO NOT CHANGE AT ALL. Both transports carry the same Request, so `easyvm stop --work
// x` and `EASYVM_HTTP=host:8080 easyvm stop` run the same code after this function returns.
func connect(work string) (*client.Client, error) {
	if addr := os.Getenv("EASYVM_HTTP"); addr != "" {
		return client.DialHTTP(addr)
	}
	return client.Dial(work)
}

func workArg(cmd string, args []string) (string, int) {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	work := fs.String("work", "", "the daemon's directory")
	if err := fs.Parse(args); err != nil {
		return "", 2
	}
	if *work == "" {
		fmt.Fprintf(os.Stderr, "easyvm %s: --work is required, because the frames live in it\n", cmd)
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

// workDirFor is the work directory a client would tell the daemon about: the same rule the daemon
// uses, so a check resolves the same paths a run would.
func workDirFor(file, work string) string {
	if work != "" {
		return work
	}
	return filepath.Dir(absPath(file))
}

// gatherVars reads `--name=value` pairs, which is how a run sets a variable the file declared.
//
// IT IS THE `key=value` FORM RATHER THAN A PAIR OF ARGUMENTS, and that is deliberate: a bare
// `--shutdown kill` would be indistinguishable from a flag taking a value, and Go's flag package
// would claim it. `--shutdown=kill` says what it is.
func gatherVars(args []string) map[string]string {
	out := map[string]string{}
	for _, a := range args {
		if !strings.HasPrefix(a, "--") || !strings.Contains(a, "=") {
			continue
		}
		kv := strings.SplitN(strings.TrimPrefix(a, "--"), "=", 2)
		if kv[0] == "" {
			continue
		}
		out[kv[0]] = kv[1]
	}
	return out
}

// splitVars separates the command's own flags from the walkthrough's variables.
//
// THE PROBLEM IT SOLVES IS THAT `--out=x` AND `--shutdown=x` ARE THE SAME SHAPE. One is a flag this
// command declares; the other is a variable the FILE declares, and the flag package refuses it with
// `flag provided but not defined` before `gatherVars` ever runs. So the declared flag names are
// enumerated here, everything else of the `--name=value` form is lifted out, and both halves are
// handed on: the flags to `fs.Parse`, the variables to the request.
//
// A FLAG THAT TAKES A VALUE IS RECOGNISED WITH ITS VALUE, whether it is written `--work /tmp` or
// `--work=/tmp`, because the two halves must not be separated or `fs.Parse` would read the path as
// the file to run.
func splitVars(args []string) (flags []string, vars map[string]string) {
	// THE DECLARED FLAGS OF `run`, WHICH IS THE ONLY COMMAND THAT TAKES VARIABLES.
	valued := map[string]bool{"work": true, "boot-disk": true, "out": true, "password": true}
	vars = map[string]string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			flags = append(flags, a)
			continue
		}
		name := strings.TrimPrefix(a, "--")
		if eq := strings.Index(name, "="); eq >= 0 {
			base := name[:eq]
			if valued[base] {
				flags = append(flags, a)
				continue
			}
			if base != "" {
				vars[base] = name[eq+1:]
				continue
			}
			flags = append(flags, a)
			continue
		}
		if valued[name] {
			// A VALUE-TAKING FLAG TAKES THE NEXT ARGUMENT TOO.
			flags = append(flags, a)
			if i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		// A BARE `--name` WITH NO `=` IS NOT A VARIABLE (there is no value), so it is left for
		// `fs.Parse` to refuse with its own message rather than silently ignored.
		flags = append(flags, a)
	}
	return flags, vars
}

// cmdCreate starts a machine and leaves it running, for a sandbox pod.
//
// IT IS NOT `run` WITH A LONGER TIMEOUT. A bake ends by running out of steps; a sandbox has no steps
// to run out of, and `destroy` is the only ending it has. The file still describes the machine - its
// `boot:` steps are the shape - but no other step runs.
func cmdCreate(args []string) int {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	file := fs.String("file", "", "the walkthrough whose `boot:` describes the machine")
	work := fs.String("work", "", "the daemon's directory")
	password := fs.String("password", "admin", "the guest account's password")
	disk := fs.String("boot-disk", "", "the disk to start, when the file does not name one")
	timeout := fs.Duration("wait", 10*time.Minute, "how long to wait for the machine to be up")
	set := map[string]string{}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *file == "" && fs.NArg() > 0 {
		*file = fs.Arg(0)
	}
	if *file == "" {
		fmt.Fprintln(os.Stderr, "easyvm create: a walkthrough file is required")
		return 2
	}
	if *work == "" {
		*work = filepath.Dir(*file)
	}
	cl, err := connect(*work)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 3
	}
	defer cl.Close()

	resp, err := cl.Do(client.Request{
		Command:  protocol.CommandCreate,
		File:     *file,
		BootDisk: *disk,
		Password: *password,
		Vars:     set,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAILED: %v\n", err)
		return 1
	}
	if !resp.OK {
		fmt.Fprintf(os.Stderr, "REFUSED: %s\n", resp.Error)
		return 1
	}

	// THE MACHINE IS UP WHEN `status` SAYS running, NOT WHEN THE REQUEST RETURNED. `create` returns at
	// once and boots in the background, exactly as `run` does, because a request that blocked for a
	// two-minute boot would make the client a supervisor.
	deadline := time.Now().Add(*timeout)
	for time.Now().Before(deadline) {
		st, err := cl.Do(client.Request{Command: "status"})
		if err != nil {
			fmt.Fprintf(os.Stderr, "FAILED: %v\n", err)
			return 1
		}
		if st.State != nil {
			switch st.State.Status {
			case "running":
				fmt.Printf("[easyvm] the machine is up: %s\n", st.State.File)
				if st.State.SSH != "" {
					fmt.Printf("[easyvm] ssh: %s\n", st.State.SSH)
				}
				return 0
			case "failed":
				fmt.Fprintf(os.Stderr, "FAILED: %s: %s\n", st.State.Message, st.State.Error)
				return 1
			}
		}
		time.Sleep(2 * time.Second)
	}
	fmt.Fprintf(os.Stderr, "FAILED: the machine was not up within %s\n", *timeout)
	return 1
}

// cmdDestroy stops a sandbox's machine and clears it, which is the only ending a sandbox has.
func cmdDestroy(args []string) int {
	work, code := workArg("destroy", args)
	if code != 0 {
		return code
	}
	cl, err := connect(work)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 3
	}
	defer cl.Close()

	resp, err := cl.Do(client.Request{Command: protocol.CommandDestroy})
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAILED: %v\n", err)
		return 1
	}
	if !resp.OK {
		fmt.Fprintf(os.Stderr, "REFUSED: %s\n", resp.Error)
		return 1
	}
	fmt.Println("[easyvm] the machine was destroyed")
	return 0
}
