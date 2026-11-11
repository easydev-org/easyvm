package bootstrap

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"howett.net/plist"
)

// THE GENERATED FILES ARE SHELL AND PLIST, so they are CHECKED AS SUCH rather than by substring.
// A launcher with a missing `fi` is accepted by every string comparison and fails on the guest,
// where the failure is a login hook that silently does nothing - the exact symptom the Python
// implementation spent runs distinguishing from "the hook never ran".

func macOSOptions() Options {
	return Options{Family: MacOS, Host: "172.30.0.1", Share: "shared", User: "guest"}
}

func windowsOptions() Options {
	return Options{Family: Windows, Host: "172.30.0.1", Share: "shared", User: "guest"}
}

func writeTemp(t *testing.T, name, body string) string {
	t.Helper()
	path := t.TempDir() + "/" + name
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// A SHELL SCRIPT THAT DOES NOT PARSE IS A HOOK THAT DOES NOTHING.
func TestShellScriptsParse(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no /bin/sh here to check with")
	}
	for _, tc := range []struct{ name, body string }{
		{"launcher.sh", Launcher(macOSOptions())},
		{"mount.sh", MacOSMountScript(macOSOptions())},
		{"launcher-linux.sh", Launcher(Options{Family: Linux})},
	} {
		path := writeTemp(t, tc.name, tc.body)
		out, err := exec.Command(sh, "-n", path).CombinedOutput()
		if err != nil {
			t.Fatalf("%s does not parse: %v\n%s\n--- the script ---\n%s",
				tc.name, err, out, tc.body)
		}
	}
}

// A PLIST THAT DOES NOT PARSE IS AN AGENT LAUNCHD IGNORES.
func TestPlistsParse(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"agent.plist", MacOSAgentPlist("/Users/vmuser/bootstrap-launcher.sh")},
		{"mount.plist", MacOSMountPlist("/usr/local/bin/vm-smbmount.sh")},
	} {
		var d map[string]any
		_, err := plist.Unmarshal([]byte(tc.body), &d)
		if err != nil {
			t.Fatalf("%s does not parse: %v\n--- the plist ---\n%s", tc.name, err, tc.body)
		}
		if d["Label"] == "" {
			t.Fatalf("%s has no Label", tc.name)
		}
		if d["RunAtLoad"] != true {
			t.Fatalf("%s must RunAtLoad; a login agent that does not is one that never fires",
				tc.name)
		}
		// KEEPALIVE FALSE, BECAUSE A BOOTSTRAP IS ONE SHOT. A supervisor would restart a failing
		// bootstrap forever, which on a published disk is a machine that never settles.
		if d["KeepAlive"] != false {
			t.Fatalf("%s must not KeepAlive", tc.name)
		}
	}
}

// THE AGENT RETRIES ONCE, AND THE INTERVAL IS PAST THE LAUNCHER'S OWN WAIT.
//
// `RunAtLoad` fires at one instant and that instant is not always usable: on run 42971367742 a
// Keyboard Setup Assistant appeared 36 seconds AFTER the password went in, so the session was
// still assembling itself when launchd ran the job. A shorter StartInterval would start a second
// copy while the first is still waiting for ~/shared.
func TestTheAgentRetriesPastTheLaunchersOwnWait(t *testing.T) {
	const launcherWait = 30 // seconds the launcher sleeps for, per the script
	var d map[string]any
	if _, err := plist.Unmarshal([]byte(MacOSAgentPlist("/x")), &d); err != nil {
		t.Fatal(err)
	}
	interval, ok := asInt(d["StartInterval"])
	if !ok {
		t.Fatalf("StartInterval is %T, want an integer", d["StartInterval"])
	}
	if interval <= launcherWait {
		t.Fatalf("StartInterval is %d, not past the launcher's own %d-second wait: the retry "+
			"would start a second copy while the first is still waiting", interval, launcherWait)
	}
}

// THE MOUNT POINT IS UNMOUNTED, NOT SKIPPED, AND AN EMPTY LEFTOVER IS CLEARED.
//
// `mount_smbfs` refuses a mount point that is STILL A MOUNT with the same `File exists` it gives for
// a bare directory. The two states produce one message and want opposite handling, which is why both
// branches are in the script and both are asserted.
func TestTheMountScriptTellsTheTwoStatesApart(t *testing.T) {
	body := MacOSMountScript(macOSOptions())

	for _, want := range []struct{ text, why string }{
		{`mount | grep -q " on $p "`, "an already-mounted path must be detected, not assumed"},
		{`umount "$p"`, "a live mount must be unmounted; skipping it leaves the old mount in place"},
		{`rmdir "$p"`, "an empty leftover directory must be removed before the mount"},
		{"NOT_EMPTY", "a non-empty directory must be refused by name rather than deleted"},
		{"//guest:@172.30.0.1/shared", "the account has no password; the colon is the spelling"},
	} {
		if !strings.Contains(body, want.text) {
			t.Fatalf("the mount script lost %q: %s", want.text, want.why)
		}
	}
	// AND IT MUST NOT CARRY A PASSWORD. The account has none, and a secret in a boot script is one
	// that is readable from the disk it is on.
	if strings.Contains(body, "admin") {
		t.Fatal("the mount script contains a password literal; this account has none, and the " +
			"colon spelling is what the man page documents")
	}
}

// THE LOG AND THE MARKER ARE NOT IN THE SHARE.
//
// The share is what is being served, so a file written back into it is one the next stage inherits -
// and a marker inherited that way makes a second run skip its own work.
func TestTheLogAndMarkerAreInTheProfileNotTheShare(t *testing.T) {
	for _, fam := range []Family{MacOS, Windows} {
		body := Launcher(Options{Family: fam, Host: "h", Share: "s", User: "guest"})
		if !strings.Contains(body, LogName) || !strings.Contains(body, MarkerName) {
			t.Fatalf("%s launcher does not name its log and marker", fam)
		}
		// The paths are under the home/profile, and the share is a separate variable.
		if fam == MacOS {
			if !strings.Contains(body, `LOG="$HOME/`+LogName+`"`) {
				t.Fatalf("the macOS log is not under $HOME:\n%s", body)
			}
		} else {
			if !strings.Contains(body, "Join-Path $env:USERPROFILE") {
				t.Fatalf("the Windows log is not under the profile:\n%s", body)
			}
		}
	}
}

// A FAILED LAUNCH MUST SAY WHAT `New-SmbMapping` ANSWERED.
//
// `Out-Null` was used for this once, and then every way of failing to reach the share produced one
// sentence: "no bootstrap.ps1 on the share; nothing to run". That reads as a missing file and was
// three different faults.
func TestTheWindowsLauncherRecordsTheMountAnswer(t *testing.T) {
	body := Launcher(windowsOptions())
	for _, want := range []struct{ text, why string }{
		{"New-SmbMapping", "net use cannot express an account with no password positionally"},
		{"-ErrorAction Stop", "a refusal must be an exception, not a warning the status never sees"},
		{`Say ("smb mapping -> FAILED: "`, "the failure must reach the log"},
		{"smb mapping -> ok", "and so must the success, or a silent failure looks the same"},
	} {
		if !strings.Contains(body, want.text) {
			t.Fatalf("the windows launcher lost %q: %s", want.text, want.why)
		}
	}
	if strings.Contains(body, "net use ") {
		t.Fatal("the launcher still uses `net use`; the account has no password and a positional " +
			"empty argument collapses in PowerShell")
	}
}

// THE TASK IS LIMITED, NOT HIGHEST, AND IT SAYS SO WHEN SCHTASKS REFUSES.
func TestTheTaskIsLimitedAndReportsRefusal(t *testing.T) {
	body := WindowsTaskScript(`C:/Users/vmuser/bootstrap-launcher.ps1`)
	if !strings.Contains(body, "/RL LIMITED") {
		t.Fatal("the task must be /RL LIMITED: it writes to the profile and the share, and " +
			"/RL HIGHEST would refuse to start for a non-administrator")
	}
	if !strings.Contains(body, "/SC ONLOGON") {
		t.Fatal("the task must be ONLOGON; the launcher runs when a session exists")
	}
	if !strings.Contains(body, "SCHTASKS_FAILED") {
		t.Fatal("a refused schtasks must be named; a task that was never registered otherwise " +
			"reads as a hook that did nothing")
	}
	// AND THE ACTION QUOTES THE PATH, because the launcher's path is under a profile that may
	// contain a space.
	if !strings.Contains(body, "-File `\"$launcher`\"") {
		t.Fatalf("the action must quote the launcher path; got:\n%s", body)
	}
}

func asInt(v any) (int, bool) {
	switch t := v.(type) {
	case int64:
		return int(t), true
	case uint64:
		return int(t), true
	case int:
		return t, true
	}
	return 0, false
}
