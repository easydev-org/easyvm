package daemon

import (
	"os"
	"strings"
	"testing"
)

// THE SHARE CONFIG'S THREE LOAD-BEARING LINES, EACH WITH A FAILURE BEHIND IT.
//
// `share_serve:` was `not implemented` in this build, so every `vm-configure` stage that wanted a
// share failed at that step - thirteen of them, and the ones that reached it reported
//
//	not implemented in this build: step "serve the host share" needs "share_serve"
//
// These are the lines a Samba that "started perfectly and bound nothing" is missing.
func TestTheShareConfigCarriesTheLinesThatMatter(t *testing.T) {
	conf := shareConfig("/work/shared")

	// IT BINDS THE BRIDGE ADDRESS AND NOTHING ELSE. The guest reaches 172.30.0.1 over the tap; an
	// smbd listening on every interface is one the guest cannot necessarily route to, and the Python
	// implementation lost a macOS ssh stage to an smbd that logged "waiting for connections" against
	// an address that did not exist yet.
	for _, want := range []string{"interfaces = 172.30.0.1/24", "bind interfaces only = yes"} {
		if !strings.Contains(conf, want) {
			t.Fatalf("the share config is missing %q:\n%s", want, conf)
		}
	}

	// THE GUEST ACCOUNT HAS NO PASSWORD, AND SAMBA REFUSES AN EMPTY ONE WITHOUT THIS. The failure it
	// prevents is `NT_STATUS_LOGON_FAILURE`, which reads as a wrong password.
	if !strings.Contains(conf, "null passwords = yes") {
		t.Fatalf("`null passwords = yes` is missing, so the guest's passwordless account is refused "+
			"with NT_STATUS_LOGON_FAILURE:\n%s", conf)
	}

	// AND THE GUEST NAME IS NOT A HOST ACCOUNT, so it must be mapped rather than rejected.
	if !strings.Contains(conf, "map to guest = bad user") {
		t.Fatalf("`map to guest = bad user` is missing, so Samba rejects `guest` before consulting "+
			"the share:\n%s", conf)
	}

	// THE SHARE ITSELF IS WRITABLE AND ANONYMOUS.
	for _, want := range []string{"path = /work/shared", "read only = no", "guest ok = yes"} {
		if !strings.Contains(conf, want) {
			t.Fatalf("the share stanza is missing %q:\n%s", want, conf)
		}
	}
}

// AND A SECOND `share_serve:` FOR A DIFFERENT DIRECTORY IS REFUSED BY NAME.
//
// ONE GUEST HAS ONE SHARE: a second directory would need a second server, a second port or a second
// name, and none of those is in the file. A silent second server is worse than a refusal, because
// `share_mount:` would mount whichever won the race.
func TestASecondShareDirectoryIsRefused(t *testing.T) {
	d := &Driver{work: t.TempDir(), pass: "admin"}
	// Pretend one is already being served, without starting smbd.
	d.share = &shareServer{dir: "/work/first"}
	err := d.ServeShare("/work/second")
	if err == nil {
		t.Fatal("a second share directory must be refused; one guest has one share")
	}
	if !strings.Contains(err.Error(), "/work/first") {
		t.Fatalf("the refusal must name the directory already being served; got %v", err)
	}
	// AND THE SAME DIRECTORY TWICE IS NOT AN ERROR, which is what a stage that restarts needs.
	if err := d.ServeShare("/work/first"); err != nil {
		t.Fatalf("serving the same directory twice must be a no-op: %v", err)
	}
}

// A GUEST THAT WRITES TO ~/shared WHEN THE MOUNT IS ABSENT WRITES INTO ITS OWN DIRECTORY.
//
// MEASURED ON MANJARO, AND IT IS THE REASON THIS PACKAGE HAS A `ShareDir` AT ALL:
//
//	--- what is mounted ---
//	--- the mount point ---
//	drwx------ 1 vmuser vmuser
//	--- the round trip ---
//	read back: written-by-the-guest
//
// THE MOUNT NEVER HAPPENED - `sudo: a password is required`, because an ssh exec has no terminal - and
// the guest's own round trip passed anyway. A file written to `~/shared` and read back from `~/shared`
// proves the guest's filesystem, not the share.
//
// THE PROOF HAS TO CROSS THE BOUNDARY: the host writes into the served directory and the guest reads
// it. That can only happen if the mount is in place.
func TestAShareIsProvenFromTheHostSide(t *testing.T) {
	body, err := os.ReadFile("../run/actions.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	start := strings.Index(src, "func (e *Executor) doShareMount(")
	if start < 0 {
		t.Fatal("no doShareMount")
	}
	rest := src[start:]
	if end := strings.Index(rest[1:], "\nfunc "); end > 0 {
		rest = rest[:end+1]
	}
	if !strings.Contains(rest, "ShareDir()") {
		t.Fatal("`share_mount:` does not look at the served directory on the host, so a guest whose " +
			"mount silently failed still passes its own round trip")
	}
	if !strings.Contains(rest, "os.WriteFile") {
		t.Fatal("nothing is written on the HOST side to prove the share; a guest-side round trip " +
			"cannot tell a mount from a local directory")
	}
	// AND THE FILE IS REMOVED, so a second run does not find yesterday's proof sitting in the share.
	if !strings.Contains(rest, "defer os.Remove") {
		t.Fatal("the proof file is left in the shared directory")
	}
}

// AND THE MOUNT THAT REPORTED SUCCESS WHILE FAILING IS FIXED AT ITS SOURCE.
//
// `sudo` in an ssh exec has no terminal, so it refuses with "a terminal is required to read the
// password" - and the shell's LAST command was `ls`, which succeeded, so the failure was invisible.
func TestTheGuestMountUsesSudoWithoutATerminal(t *testing.T) {
	body, err := os.ReadFile("share.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	if !strings.Contains(src, "sudo -S -p ''") {
		t.Fatal("the guest's mount does not use `sudo -S`, so sudo refuses with \"a terminal is " +
			"required to read the password\" and the mount never happens")
	}
	// AND THE MOUNT ITSELF IS CHECKED, not the last command in the chain.
	if !strings.Contains(src, "MOUNT_NOT_PRESENT") {
		t.Fatal("the mount is not verified after it is attempted; `ls` on an empty directory " +
			"succeeds and the function reports a mount that never happened")
	}
}

// THE THREE GRACEFUL SHUTDOWNS WERE ALL `not implemented`, AND ONE OF THEM COST A STAGE ITS LAST STEP.
//
// Measured on ubuntu's vm leg, which had done EVERYTHING it exists to do:
//
//	=== write the launcher
//	=== copy the launcher in
//	=== install the launcher
//	=== prove the launcher is installed
//	=== stop the machine
//	  error: stop the machine: shutdown method "ssh" is not implemented in this build; `kill` is
//
// `shutdown: ssh` IS WHAT EVERY LINUX AND BSD vm-configure FILE NAMES, so thirteen stages were one step
// from green and none of them could get there.
//
// AND `kill` IS NOT A SUBSTITUTE: the disk that stage publishes IS the artifact, and a killed guest
// leaves a filesystem that replays a journal on the next boot - which reads in the next stage's log as
// a slow boot rather than as a torn disk.
func TestTheThreeGracefulShutdownsAreImplemented(t *testing.T) {
	body, err := os.ReadFile("driver.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	start := strings.Index(src, "func (m machineControl) Stop(")
	if start < 0 {
		t.Fatal("no Stop")
	}
	rest := src[start:]
	if end := strings.Index(rest[1:], "\nfunc "); end > 0 {
		rest = rest[:end+1]
	}
	for _, method := range []string{"ssh", "guest_agent", "acpi"} {
		if !strings.Contains(rest, `case "`+method+`"`) {
			t.Errorf("`shutdown: %s` has no branch, so a stage naming it fails on its last step", method)
		}
	}
	// AND NONE OF THEM SILENTLY FALLS BACK TO A KILL. A shutdown that reported success after killing
	// is the failure the Python implementation removed a whole fallback chain to stop making.
	if !strings.Contains(rest, "shutdownOverSSH") || !strings.Contains(rest, "shutdownOverAgent") ||
		!strings.Contains(rest, "shutdownOverACPI") {
		t.Fatal("a shutdown method does not reach its own implementation")
	}
	// AND THE WAIT EXISTS, because a guest that was asked politely has to actually exit before the
	// next step reads its disk.
	if !strings.Contains(rest, "waitForExit") {
		t.Fatal("no wait after the request; the next step would read a disk qemu still holds")
	}
}

// AND THE AGENT'S SHUTDOWN READS ITS ANSWER, because "sent" and "accepted" are different.
func TestTheAgentShutdownReadsItsAnswer(t *testing.T) {
	body, err := os.ReadFile("qga.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	for _, want := range []string{"guest-shutdown", `json:"error"`, "isEOF"} {
		if !strings.Contains(src, want) {
			t.Fatalf("the agent shutdown is missing %q; a request that is written and not answered "+
				"reports a shutdown that never started", want)
		}
	}
}

// A SECOND `Wait()` ON AN ALREADY-REAPED PROCESS BLOCKS FOREVER, AND IT DID.
//
// MEASURED ON UBUNTU-VM: the leg sat in `follow it` for thirty minutes with `status` still saying
// `running`, on a machine that had been asked to power off. `waitForExit` calls `d.proc.Wait()` in a
// goroutine and returns when it comes back - and `Stop` then called `d.proc.Wait()` AGAIN, which
// blocks on an `os.Process` that has already been reaped. Go's `Wait` does not return an error for
// that; it waits on a channel nothing will ever close.
//
// THE STAGE HAD DONE ALL ITS WORK and could not report it, which reads from the outside exactly like a
// hung guest.
func TestStopWaitsExactlyOnce(t *testing.T) {
	body, err := os.ReadFile("driver.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	start := strings.Index(src, "func (m machineControl) Stop(")
	if start < 0 {
		t.Fatal("no Stop")
	}
	rest := src[start:]
	if end := strings.Index(rest[1:], "\nfunc "); end > 0 {
		rest = rest[:end+1]
	}
	// THE COMMENTS ARE STRIPPED FIRST, AND THAT IS NOT COSMETIC: the comment explaining this defect
	// QUOTES the offending call, so a check over the raw text fails on its own explanation. Measured -
	// this test reported the bug present after the bug was gone, because the note next to the fix
	// says `` `d.proc.Wait()` `` in prose.
	rest = stripGoComments(rest)
	// THE ONLY WAIT ALLOWED IS THE ONE INSIDE waitForExit, which is a different function.
	if strings.Contains(rest, "d.proc.Wait()") {
		t.Fatal("`Stop` calls `d.proc.Wait()` itself as well as through `waitForExit`, and the " +
			"second call blocks forever on a process that was already reaped")
	}
	if !strings.Contains(rest, "waitForExit") {
		t.Fatal("`Stop` does not wait for the process at all")
	}
	// AND THE TIMEOUT PATH MUST KILL AND REAP, or a guest that misses its deadline is left running.
	wfe := src[strings.Index(src, "func (d *Driver) waitForExit("):]
	if end := strings.Index(wfe[1:], "\nfunc "); end > 0 {
		wfe = wfe[:end+1]
	}
	wfe = stripGoComments(wfe)
	if !strings.Contains(wfe, "Process.Kill()") {
		t.Fatal("the timeout path logs that it is killing the guest and does not")
	}
	if !strings.Contains(wfe, "<-done") {
		t.Fatal("the timeout path kills without reaping, leaving a zombie that holds the disk open")
	}
}

// stripGoComments removes `//` comments and /* */ blocks so a check can read the CODE.
//
// A TEST THAT MATCHES RAW SOURCE CANNOT TELL A DEFECT FROM THE NOTE THAT DESCRIBES IT, and here it
// could not: the fix carries a paragraph explaining the double `Wait`, which quotes the call it
// removed. The test went red on its own documentation.
//
// This is not a Go parser and does not need to be - it removes comments only, and preserves string
// and rune literals, which is the one place a `//` is not a comment.
func stripGoComments(src string) string {
	var out []byte
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == '"' || c == '\'':
			// COPY THE LITERAL WHOLE, so a `//` inside a string is not mistaken for a comment.
			quote := c
			out = append(out, c)
			i++
			for i < len(src) {
				out = append(out, src[i])
				if src[i] == '\\' && i+1 < len(src) {
					out = append(out, src[i+1])
					i += 2
					continue
				}
				if src[i] == quote {
					i++
					break
				}
				i++
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			i += 2
		default:
			out = append(out, c)
			i++
		}
	}
	return string(out)
}

// `Stop` HELD THE DRIVER'S MUTEX AND THEN ASKED FOR IT AGAIN THROUGH `Guest()`.
//
// MEASURED ON UBUNTU-VM, AND IT IS THE THIRTY-MINUTE HANG:
//
//	=== stop the machine
//	(follow it ... thirty minutes, `status` still saying `running`)
//
// `Stop` locks `d.mu` for its whole body. The first thing `shutdown: ssh` does is `d.Guest()`, which
// locks `d.mu` itself. SAME GOROUTINE, SAME MUTEX, NO PATH OUT - and Go's mutex is not reentrant, so
// the stage that had finished every step it exists to do could never report it.
//
// A LOCK AROUND A NETWORK ROUND TRIP IS WRONG EVEN WITHOUT THE RECURSION: `poweroff` waits on a
// guest, and `status`, `Frame` and the console page would all be blocked behind it for minutes.
func TestStopDoesNotHoldTheLockWhileItWorks(t *testing.T) {
	body, err := os.ReadFile("driver.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	start := strings.Index(src, "func (m machineControl) Stop(")
	if start < 0 {
		t.Fatal("no Stop")
	}
	rest := src[start:]
	if end := strings.Index(rest[1:], "\nfunc "); end > 0 {
		rest = rest[:end+1]
	}
	code := stripGoComments(rest)
	if strings.Contains(code, "defer d.mu.Unlock()") {
		t.Fatal("`Stop` unlocks with `defer`, which means it holds the lock for its whole body - " +
			"including the `d.Guest()` call that takes the same lock")
	}
	if !strings.Contains(code, "d.mu.Lock()") {
		t.Fatal("`Stop` reads `d.proc` without the lock at all")
	}
}

// AND `Guest()` DOES NOT DIAL UNDER THE LOCK EITHER - the same rule, one function over.
//
// A dial to a guest that is not answering takes the full thirty seconds. Held across `d.mu`, that
// blocks `status`, `Frame` and the console page for thirty seconds per attempt.
func TestGuestDoesNotDialUnderTheLock(t *testing.T) {
	body, err := os.ReadFile("driver.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	start := strings.Index(src, "func (d *Driver) Guest(")
	if start < 0 {
		t.Fatal("no Guest")
	}
	rest := src[start:]
	if end := strings.Index(rest[1:], "\nfunc "); end > 0 {
		rest = rest[:end+1]
	}
	code := stripGoComments(rest)
	dial := strings.Index(code, "guest.Dial(")
	if dial < 0 {
		t.Fatal("`Guest` does not dial at all")
	}
	if unlock := strings.Index(code, "d.mu.Unlock()"); unlock < 0 || unlock > dial {
		t.Fatal("`Guest` dials while holding the driver's mutex, so every other caller waits out " +
			"the full dial timeout")
	}
}

// `shutdown: kill` DID NOT KILL ANYTHING - IT LOGGED AND THEN WAITED FIVE MINUTES.
//
// The branch was
//
//	case "kill":
//		d.logf("  killing the guest")
//
// and control then fell through to `waitForExit(shutdownTimeout)` - which waits for a guest that had
// never been asked to power off, so it waited the FULL three hundred seconds and only then killed it.
// Every stage whose file says `shutdown: kill` paid it: `windows-ssh`, and every throwaway overlay.
//
// A KILL HAS NOTHING TO BE PATIENT WITH. It is taken down now, and reaped, so the disk is closed before
// the step that publishes it reads the file.
func TestKillDoesNotWaitForAGuestNobodyAsked(t *testing.T) {
	body, err := os.ReadFile("driver.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	start := strings.Index(src, "func (m machineControl) Stop(")
	if start < 0 {
		t.Fatal("no Stop")
	}
	rest := src[start:]
	if end := strings.Index(rest[1:], "\nfunc "); end > 0 {
		rest = rest[:end+1]
	}
	code := stripGoComments(rest)

	// THE KILL BRANCH MUST SET A FLAG THAT SKIPS THE WAIT.
	if !strings.Contains(code, "killNow = true") {
		t.Fatal("the `kill` branch does not record that it is killing, so control falls through to " +
			"`waitForExit` and waits three hundred seconds for a guest that was never asked to stop")
	}
	// AND THE WAIT MUST BE BYPASSED FOR IT.
	if !strings.Contains(code, "killAndReap()") {
		t.Fatal("`shutdown: kill` does not reach a kill-and-reap path")
	}
	if !strings.Contains(code, "if killNow {") {
		t.Fatal("the wait is not conditional on the method, so a kill waits it out")
	}

	// AND `killAndReap` REAPS, because a kill without a wait leaves a zombie holding the disk open.
	k := src[strings.Index(src, "func (d *Driver) killAndReap("):]
	if end := strings.Index(k[1:], "\nfunc "); end > 0 {
		k = k[:end+1]
	}
	k = stripGoComments(k)
	if !strings.Contains(k, "Process.Kill()") || !strings.Contains(k, "proc.Wait()") {
		t.Fatal("`killAndReap` does not both kill and reap")
	}
}

// A WINDOWS GUEST HAS NO `cat`, `sudo` OR `mount -t cifs`, AND THE SHARE COMMAND USED ALL THREE.
//
// MEASURED ON windows-vm-configure STEP 13 OF 20, AND IT IS THE SENTINEL THAT FOUND IT:
//
//	mount the host share: the guest cannot see a file this host wrote into /github/home/shared:
//	the mount is not in place ... read "'cat' is not recognized as an internal or external command"
//
// `MountShare` WAS ONE POSIX SCRIPT. The guest-side round trip it replaced would have "passed" against
// a local directory in the guest - exactly as it did on manjaro - and the mount would have been missing
// with nothing saying so. The host-side sentinel cannot be fooled that way, and the failure it produced
// named the shell rather than the share.
func TestTheShareMountSpeaksTheGuestSHostShell(t *testing.T) {
	body, err := os.ReadFile("share.go")
	if err != nil {
		t.Fatal(err)
	}
	src := stripGoComments(string(body))

	// THE PLATFORM BRANCH IS IN `mountOnce`, BECAUSE THE MOUNT IS ONE COMMAND PER PLATFORM AND THE
	// DECISIONS ARE IN GO. It used to be a shell script per platform inside `MountShare`, and every
	// one of those was a small program whose control flow lived in a string.
	i := strings.Index(src, "func (d *Driver) mountOnce(")
	if i < 0 {
		t.Fatal("no mountOnce - the per-platform mount has no home")
	}
	once := src[i:]
	if end := strings.Index(once[1:], "\nfunc "); end > 0 {
		once = once[:end+1]
	}
	if !strings.Contains(once, `case "windows"`) {
		t.Fatal("`mountOnce` does not branch on the family, so a Windows guest is sent a POSIX command " +
			"and answers `'cat' is not recognized as an internal or external command`")
	}
	// `New-SmbMapping`, NOT `net use`, AND THE REFERENCE IMPLEMENTATION HAD ALREADY WRITTEN DOWN WHY:
	//
	//	net use \\172.30.0.1\shared "" /user:guest /persistent=no
	//	The option /PERSISTENT=NO is unknown.
	//
	// `net use` TAKES THE PASSWORD AS A POSITIONAL ARGUMENT. The account has no password, so the
	// argument is `""`; cmd drops it, every argument after it moves left, and the switch meant to be
	// `/persistent` is read as the password. MEASURED ON windows-vm-configure, 13 of 20, where cmd
	// answered with its own help text.
	if !strings.Contains(once, "New-SmbMapping") {
		t.Fatal("the Windows branch does not use `New-SmbMapping`. `net use` takes the password " +
			"positionally, so an empty one shifts the whole argument list and cmd answers with help " +
			"text instead of mounting")
	}
	if strings.Contains(once, `net use`) {
		t.Fatal("the Windows branch still uses `net use`, whose positional password cannot be empty")
	}
	// AND THE EMPTY PASSWORD IS AN EXPLICIT EMPTY STRING, which `New-SmbMapping` keeps as one.
	if !strings.Contains(once, `-Password ''`) {
		t.Fatal("the Windows mount does not pass `-Password ''`; the share account has no password " +
			"and `null passwords = yes` is what accepts it")
	}
	// AND A REFUSAL IS AN EXCEPTION, not a warning the exit status never sees.
	if !strings.Contains(once, "-ErrorAction Stop") {
		t.Fatal("the Windows mount does not use `-ErrorAction Stop`, so a refusal would be a warning " +
			"and the step would report success over an unmounted share")
	}
	// AND macOS HAS ITS OWN, because `mount -t cifs` DOES NOT EXIST THERE.
	if !strings.Contains(once, `case "macos"`) {
		t.Fatal("`mountOnce` has no macOS branch, so a Mac is sent `mount -t cifs` - a Linux utility " +
			"for a filesystem type macOS does not have")
	}
	if !strings.Contains(once, "mount_smbfs") {
		t.Fatal("the macOS branch does not use `mount_smbfs`")
	}
	if !strings.Contains(once, `//guest:@`) {
		t.Fatal("the macOS mount does not spell the empty password as `//guest:@`, which is the man " +
			"page's own form for \"this user, no password\"")
	}

	// AND THE DECISIONS ARE GO, NOT SHELL: a retry loop, a table check, and a listing, each a single
	// command whose RESULT Go inspects.
	ms := src[strings.Index(src, "func (d *Driver) MountShare("):]
	if end := strings.Index(ms[1:], "\nfunc "); end > 0 {
		ms = ms[:end+1]
	}
	if !strings.Contains(ms, "for i := 0; i < attempts; i++") {
		t.Fatal("`MountShare` does not retry in Go, so a transient failure is final")
	}
	if !strings.Contains(ms, "mountsShare(family, table.Combined())") {
		t.Fatal("`MountShare` does not check the mount table in Go, so a mount that reported success " +
			"and did not happen is believed")
	}
	// AND THE SHELL-SCRIPT IDIOMS ARE GONE. `sh -s <<'...'`, `while ... done`, `set -e` and `&&`
	// chains inside the mount command are the pattern this rewrite removed.
	for _, bad := range []string{"sh -s <<", "set -e", "while [ \"$i\""} {
		if strings.Contains(ms, bad) {
			t.Fatalf("`MountShare` still contains the shell idiom %q; the control flow belongs in Go, "+
				"where a mistake is a compile error rather than a string", bad)
		}
	}

	// AND THE SHARE IS STILL PROVEN IN EVERY BRANCH - the sentinel in `actions.go` reads it back, and
	// this function refuses a mount the table does not list.
	if !strings.Contains(src, "mountsShare") {
		t.Fatal("the mount is not verified at all")
	}
}

// THE HOST-SIDE SENTINEL READ ITS OWN FILE WITH `cat`, WHICH A WINDOWS GUEST DOES NOT HAVE.
//
// MEASURED ON windows-vm-configure STEP 13 OF 20, AFTER THE MOUNT ITSELF HAD BEEN FIXED:
//
//	the guest cannot see a file this host wrote into /github/home/shared: the mount is not in
//	place ... read "'cat' is not recognized as an internal or external command,"
//
// THE MESSAGE SAYS THE MOUNT FAILED AND THE FAILURE WAS THE PROOF'S OWN COMMAND. `MountShare`'s
// Windows branch uses `net use` and may well have succeeded; the check that proves it then asked a
// Windows guest to run `cat`.
//
// THIS IS THE THIRD TIME THE SAME MISTAKE APPEARED IN THIS MECHANISM - POSIX in the mount, POSIX in
// the proof - and each time the message named the SHARE rather than the shell. A check about a guest
// must speak the guest's language, and both halves of this one now branch on the family.
func TestTheShareProofReadsWithTheGuestsOwnCommand(t *testing.T) {
	body, err := os.ReadFile("../run/actions.go")
	if err != nil {
		t.Fatal(err)
	}
	src := stripGoComments(string(body))
	i := strings.Index(src, "func (e *Executor) doShareMount(")
	if i < 0 {
		t.Fatal("no doShareMount")
	}
	rest := src[i:]
	if end := strings.Index(rest[1:], "\nfunc "); end > 0 {
		rest = rest[:end+1]
	}
	// A BARE `cat` ON THE PROOF PATH IS THE DEFECT: it must be behind a family branch.
	if strings.Contains(rest, `client.Run("cat ~/shared/`) {
		t.Fatal("the share proof runs a bare `cat`, so a Windows guest answers \"'cat' is not " +
			"recognized as an internal or external command\" and the step blames the mount")
	}
	if !strings.Contains(rest, `family == "windows"`) {
		t.Fatal("the proof does not branch on the family, so one of the two guests gets the wrong " +
			"command")
	}
	// AND `type` IS THE WINDOWS READ.
	if !strings.Contains(rest, `type "%USERPROFILE%\shared\`) {
		t.Fatal("the Windows proof does not read with `type`")
	}
	// AND THE POSIX PATH IS STILL THERE, unchanged for the guests that have it.
	if !strings.Contains(rest, `"cat ~/shared/"`) {
		t.Fatal("the POSIX proof no longer reads with `cat`")
	}
}

// A NON-ZERO EXIT IS THE COMMAND SAYING IT FAILED, AND THE RETRY LOOP NEVER SAW IT.
//
// MEASURED ON windows-vm-configure, STEP 13 OF 20:
//
//	share: net use "%USERPROFILE%\shared" \\172.30.0.1\shared /user:guest "" /persistent:no -> 1
//	share: ... the windows guest does not list //172.30.0.1/shared in its mount table after a
//	       successful mount; the table is: There are no entries in the list.
//
// "A SUCCESSFUL MOUNT" OVER A TABLE WITH NO ENTRIES. `client.Run` RETURNS `err` ONLY FOR A TRANSPORT
// OR SESSION FAILURE - an ordinary non-zero exit is in `res.Status` and `err` is nil - and `mountOnce`
// returned `err` alone. So the thirty-attempt retry loop ran thirty failing `net use` calls and called
// each one a success.
//
// The mount table check is what surfaced it, which is the argument for keeping a check that can be
// fooled by nothing.
func TestTheMountRetriesOnANonZeroExit(t *testing.T) {
	body, err := os.ReadFile("share.go")
	if err != nil {
		t.Fatal(err)
	}
	src := stripGoComments(string(body))
	i := strings.Index(src, "func (d *Driver) mountOnce(")
	if i < 0 {
		t.Fatal("no mountOnce")
	}
	rest := src[i:]
	if end := strings.Index(rest[1:], "\nfunc "); end > 0 {
		rest = rest[:end+1]
	}
	if !strings.Contains(rest, "res.Status != 0") {
		t.Fatal("`mountOnce` does not check `res.Status`, so a command that exited non-zero is " +
			"reported as a successful mount - measured on Windows as thirty failed `net use` calls " +
			"described as one success")
	}
	if !strings.Contains(rest, "return fmt.Errorf") {
		t.Fatal("`mountOnce` never returns an error of its own, so nothing can retry")
	}
}

// THE macOS MOUNT TABLE SPELLS THE SHARE WITH A COLON AFTER THE USER, AND THE CHECK DID NOT.
//
// MEASURED ON macos-vm-configure, STEP 11 OF 25 - AND THE MOUNT HAD SUCCEEDED:
//
//	//guest:@172.30.0.1/shared on /Users/vmuser/shared (smbfs, noowners)
//	... the macos guest does not list //172.30.0.1/shared in its mount table after a successful mount
//
// THE TABLE IS PRINTED IN THE ERROR MESSAGE AND CONTRADICTS IT. `mount_smbfs` writes the user with a
// TRAILING COLON because the colon separates user from password in its own URL syntax - the same
// `user:` the command line uses to mean "this user, no password" - and the check asked for
// `//guest@...` without it.
//
// A GUARD WHOSE OWN OUTPUT REFUTES IT IS THE WORST KIND, because the message reads as a broken mount
// and the table above it says the mount is there.
func TestTheMacOSMountCheckMatchesTheRealSpelling(t *testing.T) {
	body, err := os.ReadFile("share.go")
	if err != nil {
		t.Fatal(err)
	}
	src := stripGoComments(string(body))
	i := strings.Index(src, "func mountsShare(")
	if i < 0 {
		t.Fatal("no mountsShare")
	}
	rest := src[i:]
	if end := strings.Index(rest[1:], "\nfunc "); end > 0 {
		rest = rest[:end+1]
	}
	// THE COLON IS PART OF THE SPELLING.
	if !strings.Contains(rest, "//guest:@172.30.0.1/shared") {
		t.Fatal("the macOS mount check does not look for `//guest:@172.30.0.1/shared`, which is what " +
			"`mount` prints - measured on macos-vm-configure as a working mount reported missing")
	}
	if strings.Contains(rest, `"//guest@172.30.0.1/shared"`) {
		t.Fatal("the check still looks for the form without the colon")
	}
}

// EVERY BRANCH MAKES ITS OWN MOUNT POINT.
//
// MEASURED ON linux-ubuntu-vm, ON THE FIRST RUN WHERE THE VM LEG ACTUALLY GOT THE SSH DISK:
//
//	the input disk is linux-ubuntu-ssh
//	status: failed step 15/23 "mount the host share"
//	error: mount the host share: ... exit 1: Couldn't chdir to /home/vmuser/shared:
//	       No such file or directory
//
// A MOUNT NEEDS A MOUNT POINT, and the macOS branch has had `mkdir -p ~/shared` since the rewrite
// while the Linux branch never did. The note beside the Linux command said the opposite of what the
// code did, which is how a one-line omission survived four runs.
func TestEveryMountBranchCreatesItsMountPoint(t *testing.T) {
	body, err := os.ReadFile("share.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	// THE COMMAND, NOT THE PROSE. Comments above these branches quote the commands they explain, so a
	// count over the whole file counts the explanations too. Each branch's command is a Go string
	// literal that ENDS in its mount call, and that is what is looked for.
	//
	// MEASURED ON linux-ubuntu-vm: `Couldn't chdir to /home/vmuser/shared: No such file or directory`
	// - the Linux branch mounted without making the directory, and the note beside it claimed it did.
	for _, branch := range []struct{ name, literal string }{
		{"macOS", "`mkdir -p ~/shared; ` +"},
		{"Linux", "`mkdir -p ~/shared; ` +"},
	} {
		if !strings.Contains(src, branch.literal) {
			t.Errorf("the %s branch has no `mkdir -p ~/shared` in its command, so a guest without that "+
				"directory answers `Couldn't chdir to ~/shared: No such file or directory`", branch.name)
		}
	}
	// AND THERE IS ONE PER POSIX BRANCH, counted on the command literal rather than on the prose.
	if n := strings.Count(src, "`mkdir -p ~/shared; ` +"); n < 2 {
		t.Errorf("only %d command(s) create the mount point; both the macOS and the Linux branch need "+
			"one", n)
	}
}

// THE WINDOWS SHARE MUST LAND WHERE THE PROOF READS IT.
//
// MEASURED ON windows-vm-configure, STEP 13 OF 20 - AND THE MOUNT ITSELF REPORTED SUCCESS:
//
//	share: SMB_MAPPED
//	share: Volume in drive C is Windows
//	share: Directory of C:\Users\vmuser
//	share: File Not Found
//	error: the guest cannot see a file this host wrote into /github/home/shared
//
// `New-SmbMapping` WITHOUT `-LocalPath` INVENTS A DRIVE LETTER. Nothing knows which one, so the
// verification's `dir` fell back to the working directory and the sentinel the host had just written
// came back "File Not Found" - a successful mount reported as a failed one.
//
// THE PROOF READS `%USERPROFILE%\shared\<sentinel>`, so that path has to resolve to the share. A
// junction does it, and a drive letter cannot.
func TestTheWindowsShareIsMountableAtThePathTheProofReads(t *testing.T) {
	body, err := os.ReadFile("share.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	i := strings.Index(src, `case "windows":`)
	if i < 0 {
		t.Fatal("the windows branch is gone")
	}
	end := strings.Index(src[i:], `case "macos":`)
	if end < 0 {
		t.Fatal("cannot find the end of the windows branch")
	}
	branch := src[i : i+end]

	if !strings.Contains(branch, "mklink") {
		t.Error("the windows branch does not create a junction, so the share has no fixed path. " +
			"`New-SmbMapping` without `-LocalPath` picks its own drive letter and the proof, which " +
			"reads the sentinel under the user profile, sees the working directory instead")
	}
	if !strings.Contains(branch, `-Password ''`) {
		t.Error("the windows branch no longer passes an empty password; the share account has no " +
			"password and `null passwords = yes` is what accepts it")
	}
	if !strings.Contains(branch, "USERPROFILE") {
		t.Error("the windows branch does not use the user profile, so the junction is not at the " +
			"path the proof reads")
	}
}
