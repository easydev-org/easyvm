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
func TestEveryShutdownMethodStopsTheMachineAndWaits(t *testing.T) {
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

	// EVERY METHOD `Stop` ACCEPTS MUST ACTUALLY STOP SOMETHING. There were three graceful ones
	// (`ssh`, `acpi`, `guest_agent`) and they are gone from here on purpose: a shutdown is now `kill`
	// (take the process down) or `ssh` (the old one-step form), and the graceful requests are their
	// own steps - `acpi: power`, `qga: shutdown`, `ssh: ... poweroff`.
	for _, method := range []string{"kill", "ssh"} {
		if !strings.Contains(rest, `case "`+method+`"`) {
			t.Errorf("`shutdown: %s` has no branch, so a stage naming it fails on its last step", method)
		}
	}
	// AND THE GRACEFUL PATHS DID NOT DISAPPEAR, THEY MOVED. `acpi: power` and `qga: shutdown` reach
	// the same implementations, so a request that used to be a `shutdown:` method still exists.
	for _, moved := range []string{"shutdownOverSSH", "shutdownOverAgent", "shutdownOverACPI"} {
		if !strings.Contains(src, moved) {
			t.Errorf("%s is gone, so the request it sends cannot be made from any key", moved)
		}
	}
	// AND THE WAIT EXISTS, because a guest that was asked politely has to actually exit before the
	// next step reads its disk.
	if !strings.Contains(rest, "waitForExit") {
		t.Fatal("no wait after the request; the next step would read a disk qemu still holds")
	}
	// AND IT IS NOT A WAIT-ONLY METHOD. `shutdown: wait` existed for one commit and was a verb that
	// did not happen: `shutdown:` names how to STOP the machine, so a case that sent nothing was a
	// word that lied. The waiting is `wait_until: stopped`, a step of its own.
	if strings.Contains(rest, `case "wait"`) {
		t.Error("`shutdown: wait` is back. It sends nothing, so it is not a way to stop a machine - " +
			"a file that needs to wait writes `wait_until: stopped`")
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
	// THE DIAL MOVED INTO `sshSession`, AND THE PROPERTY MOVED WITH IT: a dial to a guest that is not
	// answering takes the full timeout, and a mutex held across it stops `status`, the console page and
	// every step for exactly as long.
	//
	// THE TEST FOLLOWS THE DIAL. It used to read `Guest()` for `guest.Dial(` and now reads the session,
	// because a test that looks for code which has MOVED passes by finding nothing - which is worse
	// than failing.
	sess, err := os.ReadFile("sshsession.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(sess)
	start := strings.Index(src, "func (s *sshSession) Get() (*guest.Client, error) {")
	if start < 0 {
		t.Fatal("no `sshSession.Get`; the dialing has moved again")
	}
	rest := src[start:]
	if end := strings.Index(rest[1:], "\nfunc "); end > 0 {
		rest = rest[:end+1]
	}
	code := stripGoComments(rest)

	dial := strings.Index(code, "s.dial()")
	if dial < 0 {
		t.Fatal("`sshSession.Get` never dials, so a session can never be established")
	}
	unlock := strings.LastIndex(code[:dial], "s.mu.Unlock()")
	if unlock < 0 {
		t.Fatal("`sshSession.Get` reaches the dial without releasing its mutex, so every concurrent " +
			"caller waits out the full dial timeout")
	}
	// AND IT RE-ACQUIRES AFTERWARDS, or the result is stored without synchronisation.
	if !strings.Contains(code[dial:], "s.mu.Lock()") {
		t.Fatal("`sshSession.Get` dials without taking the lock back, so the connection is stored " +
			"alongside whatever another goroutine is writing")
	}
	// AND THE DRIVER'S `Guest` MUST NOT DIAL AT ALL: it asks the session.
	body, err := os.ReadFile("driver.go")
	if err != nil {
		t.Fatal(err)
	}
	dsrc := stripGoComments(string(body))
	i := strings.Index(dsrc, "func (d *Driver) Guest() *guest.Client {")
	if i < 0 {
		t.Fatal("no `Driver.Guest`")
	}
	drest := dsrc[i:]
	if end := strings.Index(drest[1:], "\nfunc "); end > 0 {
		drest = drest[:end+1]
	}
	if strings.Contains(drest, "guest.Dial(") {
		t.Fatal("`Driver.Guest` dials directly again, so a dead session is never noticed and a " +
			"reboot leaves the next step writing into a closed socket")
	}
	if !strings.Contains(drest, "sess.Get()") {
		t.Fatal("`Driver.Guest` does not ask the session for its connection")
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

// THE SHARE IS HTTP AND IT IS READ-ONLY.
//
// IT WAS SMB, AND SMB COST EVERY GUEST A DIFFERENT CLIENT. The measurement that ended it: FreeBSD's
// mount_smbfs dials NetBIOS 139 because netsmb speaks SMB1, and smbd serves SMB3 on 445 - so the
// kernel got a TCP RST and the library reported it as `syserr = Connection refused`, which reads
// like a network fault and took nine wrong hypotheses to see through. Working around it needed a
// ports package, a base filesystem, a root-only mount option and a mount table that names the bind.
//
// HTTP needs none of that: every guest already has a client, and the daemon already has a listener.
func TestTheShareIsServedOverHTTPAndRefusesWrites(t *testing.T) {
	body, err := os.ReadFile("sharehttp.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)

	for _, want := range []struct{ text, why string }{
		{"SharePath = \"/shared/\"", "the prefix needs its trailing slash or /sharedfoo routes here"},
		{"http.FileServer", "a hand-written handler gets ranges, If-Modified-Since and content types wrong"},
		{"http.StripPrefix", "without it the file server looks for a directory named `shared`"},
		{"http.MethodGet", "only GET is a share"},
		{"http.StatusMethodNotAllowed", "a refused write must say so by name, not 404"},
		{"escapes the shared directory", "a caller building a path by hand is not protected by " +
			"http.FileServer's URL check"},
	} {
		if !strings.Contains(src, want.text) {
			t.Fatalf("the share handler lost %q: %s", want.text, want.why)
		}
	}

	// AND IT MUST NOT STILL BE TALKING TO smbd: that transport is gone, and a leftover reference in
	// CODE means the handler is proxying to a server nothing starts. Comments may name it, because
	// the history is the reason the file looks the way it does.
	code := stripGoComments(src)
	for _, gone := range []string{"smbd", "mount_smbfs", "New-SmbMapping", "ServeShare("} {
		if strings.Contains(code, gone) {
			t.Fatalf("sharehttp.go still CALLS %q, which is the transport this replaced", gone)
		}
	}
}

// THE LAUNCHER FETCHES INSTEAD OF WAITING FOR A MOUNT.
//
// THIS IS THE ONE CONSUMER OF THE SHARE THAT MATTERED: a login hook that runs bootstrap.sh from it.
// Under SMB it waited for the mount; under HTTP it fetches, and the two failures it must keep apart
// are "the server is not ready yet" and "the file is not there".
func TestTheLauncherFetchesOverHTTP(t *testing.T) {
	body, err := os.ReadFile("../bootstrap/bootstrap.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)

	if !strings.Contains(src, "curl -fsS") {
		t.Fatal("the POSIX launcher does not fetch with curl; waiting for a mount is the mechanism " +
			"that could not work on FreeBSD")
	}
	if !strings.Contains(src, "Invoke-WebRequest") {
		t.Fatal("the Windows launcher does not fetch with Invoke-WebRequest; Windows ships it and " +
			"needs no credential, unlike the SMB mapping it replaced")
	}
	if !strings.Contains(src, "after $i attempt(s)") {
		t.Fatal("the launcher does not report how many attempts it made, so \"the server was not " +
			"ready\" and \"the file is not there\" produce the same sentence")
	}
	// `fetch` is in the FreeBSD base system and curl is not guaranteed there, so the guest-side
	// check tries both. This is the one place a platform-specific client is still named.
	if !strings.Contains(src, "fetch -qo -") {
		t.Log("note: the FreeBSD proof path falls back to `fetch`; if that changed, verify the guest " +
			"command in internal/run/actions.go still names something FreeBSD ships")
	}
}
