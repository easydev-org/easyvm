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
