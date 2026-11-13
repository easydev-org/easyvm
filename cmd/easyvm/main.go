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
	"github.com/easydev-org/easyvm/internal/oci"
	"github.com/easydev-org/easyvm/internal/protocol"
	"github.com/easydev-org/easyvm/internal/walk"
)

const usage = `easyvm - drive a walkthrough YAML file against a machine

The machine has ONE owner: a daemon. This command is a client and sends it requests.
` + "`check`, `list` and `versions` are questions about a file, not about a machine, and need no daemon." + `

TWO MODES, AND THEY DIFFER IN WHEN THE MACHINE STOPS:

  easyvm run     FILE [flags]                    A BAKE: run every step, then stop the machine.
  easyvm create  FILE [flags]                    A SANDBOX: boot and STAY UP until destroy.

  easyvm serve   --work DIR [--novnc-port N]     start the owner
  easyvm status  [--work DIR]                    where is it now, and is the guest up
  easyvm stop    [--work DIR]                    stop the run and the machine
  easyvm destroy [--work DIR]                    stop a sandbox's machine (its only ending)
  easyvm health  [--work DIR]                    exit 0 only if QMP and the guest both answer

  easyvm check   FILE                            validate without any machine at all
  easyvm list    FILE                            one line per step
  easyvm versions                                what this build expects of qemu
  easyvm pull    --tag TAG [--dest PATH]         fetch a published disk from the registry

run flags:
  --work DIR        the daemon's directory (its socket is here)
  --boot-disk PATH  the disk a boot: step starts from
  --out PATH        where a compress: step writes
  --password PW     the guest account's password
  --novnc-port N    where the browser page listens; 0 disables it
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
	case "pull":
		os.Exit(cmdPull(args))
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

// cmdPull fetches a published disk from the registry.
//
// IT IS NOT PART OF `run`, AND THAT IS DELIBERATE. A walkthrough file is a description of what to
// do to a machine; which disk it starts from is a fact about the JOB, and a file that carried a
// registry path would make every run of it depend on a package that may have moved. The workflow
// pulls, then hands the file a `--boot-disk`.
// cmdPull PULLS A PUBLISHED DISK BY ASKING `oras`, AND THIS PROGRAM DOES NOT DO IT ITSELF.
//
// THE TRANSFER IS NOT THIS PROGRAM'S JOB. It was 246 lines of registry protocol that duplicated what
// `oras` already is: token exchange, manifest reading, resumable blob fetch. `easyvm push` is already
// gone for that reason, and the pull side now matches - a qcow2 in a registry is an artifact, and
// `oras` is the tool that handles artifacts.
//
// WHAT STAYS IS `internal/oci`, AND THE DIFFERENCE IS THE CALLER. `fetch:` inside a walkthrough has to
// resolve an installer image or a macOS asset AT RUN TIME, in the daemon, on a machine that may have
// no `oras` - that is the asset layer using a library, not a user-facing command. THIS FUNCTION IS
// THE USER-FACING ONE, and it shells out.
//
// THE FILE ENDS UP AT THE PATH THE CALLER ASKED FOR, because a walkthrough names `installer_iso` and
// the workflow names `work/disk.qcow2`: oras writes the layer under its own title in the directory it
// is told, so the file is moved into place here.
func cmdPull(args []string) int {
	fs := flag.NewFlagSet("pull", flag.ContinueOnError)
	tag := fs.String("tag", "", "the artifact to pull, `repository:tag` or just `tag`")
	dest := fs.String("dest", "", "where to write the disk; the default is the tag's own name")
	registry := fs.String("registry", oci.DefaultRegistry, "the registry host")
	repository := fs.String("repository", "", "owner/repo, lowercased; default is $GITHUB_REPOSITORY")
	// THE TIMEOUT IS KEPT AS A FLAG SO AN EXISTING CALLER DOES NOT BREAK, but oras owns the transfer
	// and has no flag for it; the workflow's own step timeout is the real ceiling.
	_ = fs.Duration("timeout", 60*time.Minute, "accepted for compatibility; the step timeout governs")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *tag == "" && fs.NArg() > 0 {
		*tag = fs.Arg(0)
	}
	if *tag == "" {
		fmt.Fprintln(os.Stderr, "easyvm pull: --tag is required, for example easyvm/win11:windows11-ssh")
		return 2
	}
	var art oci.Artifact
	if strings.Contains(*tag, ":") || strings.Contains(*tag, "/") {
		parsed, err := oci.TagFrom(*tag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FAILED: %v\n", err)
			return 1
		}
		art = parsed
	} else {
		art = oci.Artifact{Tag: *tag}
	}
	if *registry != "" {
		art.Registry = *registry
	}
	if *repository != "" {
		art.Repository = *repository
	} else if art.Repository == "" {
		env := os.Getenv("GITHUB_REPOSITORY")
		if env == "" {
			fmt.Fprintln(os.Stderr, "easyvm pull: --repository is required when GITHUB_REPOSITORY "+
				"is not set")
			return 2
		}
		art.Repository = strings.ToLower(env)
	}
	if *dest == "" {
		*dest = filepath.Base(art.Tag) + ".qcow2"
	}

	// A DISK THAT IS ALREADY THERE IS NOT PULLED AGAIN, AND THAT IS WHAT MAKES A LOCAL LOOP POSSIBLE.
	// CI starts empty so the pull happens; a machine with disks already on disk skips it. NOTHING IN
	// THE FILE IS EDITED FOR THAT CASE, so the walk that runs locally is the walk CI runs.
	if fi, err := os.Stat(*dest); err == nil && fi.Size() > 512<<20 {
		fmt.Printf("[easyvm] %s is already here (%d MiB); not pulling %s\n",
			*dest, fi.Size()>>20, fmt.Sprintf("%s/%s:%s", art.Registry, art.Repository, art.Tag))
		return 0
	}

	tool, err := exec.LookPath("oras")
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAILED: oras is not installed. `easyvm pull` delegates the transfer "+
			"to it - this program drives a machine, not a registry\n")
		return 1
	}
	ref := fmt.Sprintf("%s/%s:%s", art.Registry, art.Repository, art.Tag)
	dir := filepath.Dir(*dest)
	if dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "FAILED: %v\n", err)
		return 1
	}

	fmt.Printf("[easyvm] oras pull %s -> %s\n", ref, *dest)
	pull := exec.Command(tool, "pull", ref, "-o", dir)
	pull.Stdout = os.Stdout
	pull.Stderr = os.Stderr
	if err := pull.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "FAILED: oras pull %s: %v\n", ref, err)
		return 1
	}

	// WHAT ORAS WROTE IS NAMED BY THE LAYER'S TITLE, so the caller's name is applied here. A single
	// file in the directory is the disk; anything else means the artifact shape changed.
	entries, err := os.ReadDir(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAILED: %v\n", err)
		return 1
	}
	got := ""
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if fi, err := os.Stat(p); err != nil || fi.Size() < 512<<20 {
			continue
		}
		if got != "" {
			fmt.Fprintf(os.Stderr, "FAILED: oras wrote more than one large file into %s; the "+
				"artifact should carry exactly one disk\n", dir)
			return 1
		}
		got = p
	}
	if got == "" {
		fmt.Fprintf(os.Stderr, "FAILED: oras reported success and there is no disk in %s\n", dir)
		return 1
	}
	if got != *dest {
		if err := os.Rename(got, *dest); err != nil {
			fmt.Fprintf(os.Stderr, "FAILED: %v\n", err)
			return 1
		}
	}
	fi, err := os.Stat(*dest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAILED: %s is not there after the pull: %v\n", *dest, err)
		return 1
	}
	fmt.Printf("[easyvm] %s: %d MiB\n", *dest, fi.Size()>>20)
	return 0
}

// --- the daemon -----------------------------------------------------------------------

func cmdServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	work := fs.String("work", "", "the directory the socket and the frames live in")
	novncPort := fs.Int("novnc-port", daemon.DefaultNovNCPort, "where the noVNC page listens; 0 disables it")
	httpAddr := fs.String("http", daemon.DefaultHTTPAddr, "the browser surface: a live screen, a "+
		"click map, keys and typing. Empty disables it")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *work == "" {
		fmt.Fprintln(os.Stderr, "easyvm serve: --work is required; the socket lives there")
		return 2
	}

	d := daemon.New()
	fmt.Printf("[easyvm] daemon for %s\n", *work)

	// THE BROWSER SURFACE IS A SECOND FRONT END ON THE SAME DAEMON, started beside the socket and
	// sharing its state. It is not a second service: every click and key it receives goes through the
	// same `handle` the CLI uses, and the QMP connection is the one this daemon already holds.
	if *httpAddr != "" {
		go func() {
			if err := d.ServeHTTP(daemon.HTTPServeOptions{
				Addr: *httpAddr,
				Ready: func(addr string) {
					fmt.Printf("[easyvm] browser surface on http://%s/\n", addr)
				},
			}); err != nil {
				fmt.Fprintf(os.Stderr, "[easyvm] http surface stopped: %v\n", err)
			}
		}()
	}

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
	vars := gatherVars(args)

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
	novnc := fs.Int("novnc", 0, "the browser page's port; 0 uses the daemon's")
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
		Command:   protocol.CommandCreate,
		File:      *file,
		BootDisk:  *disk,
		Password:  *password,
		NoVNCPort: *novnc,
		Vars:      set,
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
