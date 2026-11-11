// Package bootstrap writes the files that make a share run itself at login.
//
// THE SPLIT IS THE SAFETY PROPERTY OF THIS PACKAGE: it only WRITES. The `scp:` step after each one
// moves the file in, and the `ssh:` step after that runs it. A single step that wrote, copied and
// ran would report `the launcher install exited 1` and name neither the file nor the action - which
// is what the Python implementation's first version did.
//
// THE SCRIPT COMES FROM THE SHARE; THE LOG AND THE MARKER DO NOT. Anything the launcher writes goes
// under the guest's own home, never into the share, because the share is what is being served and a
// file written back into it would be a file the next stage inherits.
package bootstrap

import (
	"fmt"
	"strings"
)

// Family is which guest the files are for.
type Family string

const (
	Windows Family = "windows"
	MacOS   Family = "macos"
	Linux   Family = "linux"
)

// ScriptName is the file a guest looks for in the share.
func ScriptName(f Family) string {
	if f == Windows {
		return "bootstrap.ps1"
	}
	return "bootstrap.sh"
}

// LauncherName is the launcher written on the host and copied in.
func LauncherName(f Family) string {
	if f == Windows {
		return "bootstrap-launcher.ps1"
	}
	return "bootstrap-launcher.sh"
}

// The two files the launcher keeps in the guest's own home, never in the share.
const (
	LogName    = "bootstrap.log"
	MarkerName = "bootstrap.done"
)

// Options is everything a launcher needs to know, and every field is a fact about the run.
type Options struct {
	// Family is which guest this is for.
	Family Family
	// Host is the address the share is served on - the bridge, 172.30.0.1.
	Host string
	// Share is the share's name.
	Share string
	// User and Password are the share account. The password is on the command line deliberately:
	// the guest is a throwaway VM on a private bridge, and the alternative - a credential file - is
	// one more thing to copy in before the mount can be attempted.
	User     string
	Password string
}

// Launcher is the script a guest runs at login. It waits for the share, runs the script on it once,
// and records what happened.
//
// EVERY FAILURE IS WRITTEN DOWN AND NONE OF THEM FAIL LOUDLY. A login hook that does nothing is the
// correct behaviour for a machine nobody has configured yet, and a hook that fails visibly on a
// published disk reads as a broken disk rather than as a share that was not there.
func Launcher(o Options) string {
	if o.Family == Windows {
		return windowsLauncher(o)
	}
	return posixLauncher(o)
}

// posixLauncher is the macOS and Linux form.
func posixLauncher(o Options) string {
	script := ScriptName(o.Family)
	return fmt.Sprintf(`#!/bin/sh
# Run the share's %[1]s at login, once, and leave a log beside it.
#
# INSTALLED BY THIS REPOSITORY. It is deliberately a shell script in the guest: the disk carries no
# Python, and an interpreter would be one more thing the mount has to wait for.
#
# THE SHARE IS MOUNTED BEFORE THIS RUNS. A macOS LaunchDaemon does that as root, because
# mount_smbfs refuses a non-root client against this server; a Linux guest mounts it with cifs.
# What is left here is the wait and the run.
#
# THE LOG AND THE MARKER ARE NOT IN THE SHARE, because the share is what is being served: a file
# written back into it would be one the next stage inherits.
LOG="$HOME/%[2]s"
MARKER="$HOME/%[3]s"
SHARED="$HOME/shared"
SCRIPT="$SHARED/%[1]s"

say() { echo "$@" >> "$LOG"; }

# A MARKER FROM A PREVIOUS LOGIN MEANS THIS HAS ALREADY RUN. Removing it is how a stage asks for a
# second run, and nothing else does.
[ -f "$MARKER" ] && exit 0

# THE SHARE MAY NOT BE READY YET. A login trigger can fire before the mount finishes, so this
# waits for the file rather than checking once.
i=0
while [ ! -f "$SCRIPT" ] && [ "$i" -lt 30 ]; do
    i=$((i + 1))
    sleep 1
done

# THE FALLBACK: NO SCRIPT, NO RUN - and say so, because "the hook ran and nothing happened" is
# otherwise indistinguishable from "the hook never ran".
if [ ! -f "$SCRIPT" ]; then
    say "[$(date)] no $SCRIPT on the share; nothing to run"
    exit 0
fi

say "[$(date)] running $SCRIPT"
if /bin/sh "$SCRIPT" >> "$LOG" 2>&1; then
    # THE MARKER IS WRITTEN ONLY ON SUCCESS, so a failing bootstrap tries again at the next login.
    date > "$MARKER"
    say "[$(date)] ok"
else
    say "[$(date)] FAILED; it will run again at the next login"
fi
`, script, LogName, MarkerName)
}

// windowsLauncher is the Windows form.
//
// A NETWORK LOGON CANNOT REUSE A STORED CREDENTIAL. `cmdkey` answers ERROR_NO_SUCH_LOGON_SESSION -
// "Network logon sessions do not have an associated credential set" - and Win32-OpenSSH hardcodes
// that logon type, so a credential written from an ssh session could not exist.
//
// WHAT IT CAN DO IS OPEN A SESSION WITH AN EXPLICIT ONE, inside this process. That is why the
// credential is here and not in the credential vault, and why the mount is `New-SmbMapping` rather
// than `net use`: the account has no password, and a positional empty argument collapses in
// PowerShell - every argument after it shifts left and `net use` reads a switch as the password.
func windowsLauncher(o Options) string {
	unc := `\\` + o.Host + `\` + o.Share
	script := ScriptName(o.Family)
	return fmt.Sprintf(`# Run the share's %[1]s at logon, once, and leave a log beside it.
#
# INSTALLED BY THIS REPOSITORY, started by Task Scheduler through
# `+"`powershell.exe -WindowStyle Hidden`"+`, so no console appears.
#
# NO DRIVE LETTER AND NO VAULT. A drive letter belongs to the logon session that made it, and a
# vault entry cannot be written from an ssh session at all - see the note on the logon type.
$SmbUser = '%[2]s'
$Base    = '%[3]s'
$Script  = '%[3]s\%[1]s'

# THE SCRIPT COMES FROM THE SHARE; THE LOG AND THE MARKER DO NOT.
$Log     = Join-Path $env:USERPROFILE '%[4]s'
$Marker  = Join-Path $env:USERPROFILE '%[5]s'

function Say($message) {
    Add-Content -Path $Log -Value "$(Get-Date -Format 'yyyy-MM-dd HH:mm:ss') $message" `+"`"+`
        -ErrorAction SilentlyContinue
}

# THE SESSION, WITH ITS ANSWER KEPT. `+"`Out-Null`"+` used to discard this, and then every way of
# failing to reach the share produced one sentence.
function UseShare {
    $null = Get-SmbMapping -RemotePath $Base -ErrorAction SilentlyContinue |
        Remove-SmbMapping -Force -ErrorAction SilentlyContinue
    try {
        $null = New-SmbMapping -RemotePath $Base -UserName $SmbUser -Password '' `+"`"+`
            -Persistent $false -ErrorAction Stop
        Say "smb mapping -> ok"
        return 0
    } catch {
        Say ("smb mapping -> FAILED: " + $_.Exception.Message)
        return 1
    }
}

$null = UseShare

# A MARKER FROM A PREVIOUS LOGON MEANS THIS HAS ALREADY RUN, and is checked AFTER the session is
# opened so that a mount failure is still reported.
if (Test-Path $Marker) { exit 0 }

$found = $false
for ($i = 0; $i -lt 60; $i++) {
    if (Test-Path $Script) { $found = $true; break }
    if ($i %% 10 -eq 9) { $null = UseShare }
    Start-Sleep -Seconds 1
}

if (-not $found) {
    Say "no $Script on the share after 60s; nothing to run (see the smb mapping lines above)"
    exit 0
}

Say "running $Script"
try {
    & powershell.exe -NoProfile -ExecutionPolicy Bypass -File $Script *>&1 |
        ForEach-Object { Say $_ }
    if ($LASTEXITCODE -ne 0) { throw "exit $LASTEXITCODE" }
    (Get-Date).ToString('o') | Set-Content -Path $Marker
    Say "ok"
} catch {
    Say ("FAILED: " + $_.Exception.Message + "; it will run again at the next logon")
}
`, script, o.User, unc, LogName, MarkerName)
}

// MacOSAgentPlist is the LaunchAgent that runs the launcher at login.
//
// `ProgramArguments` NAMES /bin/sh AND THE LAUNCHER rather than the launcher alone: launchd runs a
// programme and does not consult a shebang.
//
// TWO THINGS IN HERE ARE LOAD-BEARING AND BOTH WERE MEASURED:
//
//	RunAtLoad     fires at one instant, and that instant is not always usable - a Keyboard Setup
//	              Assistant appeared 36 seconds after the password went in on the recorded run.
//	StartInterval 90 is that retry. It is PAST the launcher's own one-minute wait for ~/shared, so
//	              the retry cannot overlap the first attempt, and the probe waits 180s, so at most
//	              two fit. A shorter interval would start a second copy while the first waits.
func MacOSAgentPlist(launcherPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>org.vm.bootstrap</string>
    <key>ProgramArguments</key>
    <array>
        <string>/bin/sh</string>
        <string>%s</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>StartInterval</key>
    <integer>90</integer>
    <key>KeepAlive</key>
    <false/>
    <key>StandardOutPath</key>
    <string>/tmp/org.vm.bootstrap.out</string>
    <key>StandardErrorPath</key>
    <string>/tmp/org.vm.bootstrap.err</string>
</dict>
</plist>
`, launcherPath)
}

// MacOSMountScript is the LaunchDaemon's script: mount the share at boot, as root.
//
// A USER CANNOT MOUNT THIS SHARE. `mount_smbfs` as a normal account against this server answers
// "Authentication error" while the same credential from root succeeds, which is why the mount is a
// daemon and the launcher is an agent.
//
// AND AN EXISTING MOUNT IS UNMOUNTED, NOT SKIPPED. `mount_smbfs` refuses a mount point that is
// still a mount with the SAME `File exists` it gives for a bare directory, so the two states produce
// one message and want opposite fixes.
func MacOSMountScript(o Options) string {
	unc := "//" + o.User + ":@" + o.Host + "/" + o.Share
	body := []string{
		"#!/bin/sh",
		"# Mount the share at boot, as root. Installed by this repository.",
		"#",
		"# ROOT IS REQUIRED. mount_smbfs as a normal account against this server answers",
		"# \"Authentication error\" while the same credential from root succeeds.",
		"#",
		"# THE PASSWORD IS NOT IN THIS FILE: the account has none, and the colon after the user is",
		"# the man page spelling for \"this user, no password\". The form without the colon would mean",
		"# ask for one, and with no tty that gives up before any session setup.",
		"p=\"$HOME/shared\"",
		"mounted=no",
		"mount | grep -q \" on $p \" && mounted=yes",
		"",
		"# AN EXISTING MOUNT IS UNMOUNTED FIRST. mount_smbfs says File exists for a live mount AND",
		"# for a bare directory, so the two want opposite handling and produce one message.",
		"if [ \"$mounted\" = yes ]; then",
		"    umount \"$p\" || { echo \"BUSY $p\"; mount | grep \" on $p \"; exit 1; }",
		"fi",
		"if [ -d \"$p\" ]; then",
		"    rmdir \"$p\" || { echo \"NOT_EMPTY $p\"; ls -la \"$p\"; exit 1; }",
		"fi",
		"mkdir -p \"$p\"",
		"mount_smbfs -f 0666 -d 0777 '" + unc + "' \"$p\"",
	}
	return strings.Join(body, "\n") + "\n"
}

// MacOSMountPlist is the LaunchDaemon that runs the mount script at boot.
func MacOSMountPlist(scriptPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>org.vm.smbmount</string>
    <key>ProgramArguments</key>
    <array>
        <string>/bin/sh</string>
        <string>%s</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <false/>
    <key>StandardOutPath</key>
    <string>/tmp/org.vm.smbmount.out</string>
    <key>StandardErrorPath</key>
    <string>/tmp/org.vm.smbmount.err</string>
</dict>
</plist>
`, scriptPath)
}

// WindowsTaskScript registers the logon task.
//
// `/RL LIMITED` AND NOT `/RL HIGHEST`: the hook does not need elevation - it writes to the user's own
// profile and to the share - and a task that asked for it would fail to start for a user who was not
// in the administrators group.
func WindowsTaskScript(launcherPath string) string {
	// THE BACKTICK IS POWERSHELL'S LINE-CONTINUATION AND QUOTE-ESCAPE, and Go's raw strings cannot
	// contain one - so the body is built by joining lines and the two backticks are named.
	bt := "`"
	lines := []string{
		"# Register the logon task that runs the share's bootstrap once.",
		"#",
		"# INSTALLED BY THIS REPOSITORY.",
		"$ErrorActionPreference = 'Stop'",
		"",
		`$launcher = "` + launcherPath + `"`,
		`$action = "powershell.exe -NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File ` +
			bt + `"$launcher` + bt + `""`,
		"",
		"schtasks /Create /F /TN \"vm-bootstrap\" /TR $action /SC ONLOGON /RL LIMITED | Out-String | Write-Output",
		"",
		"if ($LASTEXITCODE -ne 0) {",
		"    Write-Output \"SCHTASKS_FAILED: $LASTEXITCODE\"",
		"    exit 1",
		"}",
		"Write-Output \"BOOTSTRAP_TASK_REGISTERED\"",
	}
	// /RL LIMITED AND NOT /RL HIGHEST: the hook does not need elevation - it writes to the user's own
	// profile and to the share - and a task that asked for it would fail to start for a user who was
	// not in the administrators group.
	return strings.Join(lines, "\n") + "\n"
}
