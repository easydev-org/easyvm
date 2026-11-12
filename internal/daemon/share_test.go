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
	i := strings.Index(src, "func (d *Driver) MountShare(")
	if i < 0 {
		t.Fatal("no MountShare")
	}
	rest := src[i:]
	if end := strings.Index(rest[1:], "\nfunc "); end > 0 {
		rest = rest[:end+1]
	}

	// A BRANCH ON THE FAMILY, because the shell is part of the platform.
	if !strings.Contains(rest, `family == "windows"`) {
		t.Fatal("`MountShare` does not branch on the family, so a Windows guest is sent a POSIX script " +
			"and answers like a Windows guest: `\"'cat' is not recognized as an internal or external " +
			"command\"")
	}
	// AND `net use` IS THE WINDOWS MOUNT.
	if !strings.Contains(rest, "net use") {
		t.Fatal("the Windows branch does not use `net use`, which is how a Windows guest mounts a share")
	}
	// AND THE EMPTY PASSWORD IS SPELLED, or cmd's splitting drops it and Samba answers
	// NT_STATUS_LOGON_FAILURE - which reads as a wrong password rather than a missing argument.
	if !strings.Contains(rest, `/user:guest ""`) {
		t.Fatal("the Windows mount does not pass an explicit empty password (`\"\"`); cmd would drop " +
			"the argument and Samba would answer NT_STATUS_LOGON_FAILURE")
	}
	// AND `&` RATHER THAN `&&` BETWEEN STATEMENTS: cmd's `&&` and `||` have equal precedence, so a
	// chain mixing them groups differently from what it looks like.
	if !strings.Contains(rest, `}, " & ")`) {
		t.Fatal("the Windows statements are joined with `&&`, whose precedence relative to `||` in " +
			"cmd makes `a && b || c && d` mean `((a && b) || c) && d`")
	}
	// AND THE POSIX BRANCH KEEPS ITS OWN PROOF, unchanged.
	if !strings.Contains(rest, "mount | grep -q") {
		t.Fatal("the POSIX branch no longer verifies that the mount is present")
	}
}
