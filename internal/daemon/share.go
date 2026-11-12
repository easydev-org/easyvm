package daemon

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/easydev-org/easyvm/internal/guest"
)

// --- the host share -------------------------------------------------------------------
//
// `share_serve:` puts a directory on the bridge address so a guest can mount it, and `share_mount:`
// makes the guest do it. THE TWO ARE ONE MECHANISM IN TWO STEPS because they fail differently: a
// server that never bound is a host problem, and a guest that cannot mount is a guest problem.
//
// THE SERVER IS smbd, AND IT BINDS THE BRIDGE ADDRESS. A guest reaches it over the tap at
// 172.30.0.1 - the same address the ssh forwards use - so `-netdev user,smb=` is not an option even
// in principle: that option makes qemu fork its own Samba with a generated config, which is a second
// server nobody configured.

// shareServer is the smbd this driver started, so it can be stopped with the machine.
type shareServer struct {
	dir  string
	conf string
	proc *exec.Cmd
	once sync.Once
}

// ServeShare creates the directory, writes a config for it, and starts smbd on the bridge.
//
// IT WAITS FOR THE PORT TO ANSWER. A server that is starting is not a server, and the `share_mount:`
// step that follows would otherwise race it - measured in the Python implementation as an smbd
// "DID NOT BIND 172.30.0.1:445 in 10s" against a share that started perfectly and had bound to no
// interface.
func (d *Driver) ServeShare(dir string) error {
	d.mu.Lock()
	if d.share != nil {
		existing := d.share.dir
		d.mu.Unlock()
		// A SECOND CALL FOR THE SAME DIRECTORY IS NOT AN ERROR: a file may have a `share_serve:`
		// per stage and the daemon outlives them.
		if existing == dir {
			return nil
		}
		return fmt.Errorf("this run is already serving %s; one guest has one share, so a second "+
			"`share_serve:` must name the same directory or in a new stage", existing)
	}
	d.mu.Unlock()

	if err := os.MkdirAll(dir, 0o777); err != nil {
		return fmt.Errorf("creating the shared directory %s: %w", dir, err)
	}
	// WORLD-WRITABLE, BECAUSE THE GUEST IS ANOTHER MACHINE WITH ANOTHER UID. A 0755 directory is a
	// share the guest can read and not write, and the failure reads as a permissions problem inside
	// the guest rather than here.
	if err := os.Chmod(dir, 0o777); err != nil {
		return fmt.Errorf("making %s writable: %w", dir, err)
	}

	smbd, err := exec.LookPath("smbd")
	if err != nil {
		return fmt.Errorf("smbd is not installed: %w. The share is Samba over the bridge, and a "+
			"guest that mounts it needs this server to be the one listening on 172.30.0.1", err)
	}

	// THE CONFIG IS WRITTEN PER RUN, beside the work directory, so two runs never share one.
	conf := filepath.Join(d.work, "smb.conf")
	if err := os.WriteFile(conf, []byte(shareConfig(dir)), 0o644); err != nil {
		return err
	}

	// A PREVIOUS smbd IS LEFT ALONE IF IT IS THE SAME DIRECTORY, and this daemon owns only what it
	// started - `pkill smbd` would kill a server another run is using.
	cmd := exec.Command(smbd, "--foreground", "--no-process-group", "--configfile="+conf)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting smbd: %w", err)
	}

	d.mu.Lock()
	d.share = &shareServer{dir: dir, conf: conf, proc: cmd}
	d.mu.Unlock()

	// AND THE PORT IS WAITED FOR, because "smbd started" and "a guest can connect" are different
	// facts and the second is the one the next step needs.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", "172.30.0.1:445", time.Second)
		if err == nil {
			_ = conn.Close()
			d.logf(fmt.Sprintf("  share: %s served on 172.30.0.1:445", dir))
			return nil
		}
		// A SERVER THAT EXITED IS NOT GOING TO BIND, and waiting out the deadline would delay the
		// real message by ten seconds.
		if cmd.ProcessState != nil {
			return fmt.Errorf("smbd exited before it bound 172.30.0.1:445 (%s); the config is %s",
				cmd.ProcessState, conf)
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("smbd did not bind 172.30.0.1:445 within 10s, so a guest cannot mount %s. "+
		"The config is %s; samba's own account of why is on this log", dir, conf)
}

// ShareDir is the directory this run serves, or "" when nothing is served.
//
// IT EXISTS SO A PROOF CAN LOOK ON THE HOST SIDE. A guest that writes to `~/shared` when the mount is
// ABSENT writes into a local directory, and reading the file back from that same directory succeeds -
// which is a proof of the guest's own filesystem and not of the share. Measured: `read back:
// written-by-the-guest` passed while `mount` listed nothing and the mount point held only `..`.
func (d *Driver) ShareDir() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.share == nil {
		return ""
	}
	return d.share.dir
}

// MountShare makes the guest mount the share on the bridge.
//
// IT IS ONE `mount -t cifs` WITH THE CREDENTIALS ON THE COMMAND LINE, because a credentials FILE
// would have to be copied into the guest first and that is a second thing that can fail. The share
// account has no password - `null passwords = yes` in the config - so there is no secret to leak
// by putting it in argv.
func (d *Driver) MountShare(family string) error {
	d.mu.Lock()
	share := d.share
	d.mu.Unlock()
	if share == nil {
		return fmt.Errorf("no share is being served; a `share_serve:` step must come first")
	}

	client := d.Guest()
	if client == nil {
		return fmt.Errorf("no ssh connection; a `require_ssh:` step must come first")
	}

	// ONE COMMAND PER CALL, AND THE DECISIONS ARE IN GO.
	//
	// THIS USED TO BE A SHELL SCRIPT PER PLATFORM - `sh -c` with `&&` chains and a `while` loop for
	// macOS, a `&`-separated cmd.exe line for Windows. Every one of those was a small program whose
	// control flow lived in a STRING, and each had already produced a bug that took a run to find:
	//
	//	the POSIX one reported success after a failed mount, because the last command in its `&&`
	//	chain was `ls` on an empty directory
	//
	//	a `&&` chain with a `while` loop inside it re-groups as `((a && b) || c) && d` in cmd and as
	//	`(a || true && b); while` in sh, so the loop's failure decided nothing
	//
	// RETRY IS A LOOP IN GO. A failed mount is a RESULT, not a shell exit status, so `if` is Go's.
	const attempts = 30
	var last error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			time.Sleep(2 * time.Second)
		}
		if err := d.mountOnce(client, family, share.dir); err == nil {
			last = nil
			break
		} else {
			last = err
		}
	}
	if last != nil {
		return fmt.Errorf("mounting %s in the %s guest: %w", share.dir, family, last)
	}

	// AND THE MOUNT IS PROVEN HERE, BECAUSE THE SHELL THAT USED TO PROVE IT COULD NOT BE TRUSTED.
	//
	// The old script ended with `mount | grep -q ... || { echo MOUNT_NOT_PRESENT; exit 1; }`, and that
	// check has two failure modes of its own: the grep's pattern has to match the platform's own
	// spelling of the mount - `//172.30.0.1/shared` on Linux, `//guest@172.30.0.1/shared` on macOS,
	// `\\172.30.0.1\shared` on Windows - and a pattern that does not match reports a mount that IS
	// there as missing.
	//
	// SO THE TABLE IS READ AND MATCHED IN GO, where the three spellings are three constants and a
	// mistake is a compile-time neighbour rather than a string inside a shell.
	table, err := client.Run(tableCommand(family), 60*time.Second)
	if err != nil {
		return fmt.Errorf("reading the %s guest's mount table: %w", family, err)
	}
	if !mountsShare(family, table.Combined()) {
		return fmt.Errorf("the %s guest does not list //172.30.0.1/shared in its mount table after "+
			"a successful mount; the table is:\n%s", family, firstLines(table.Combined(), 10))
	}

	listed, err := client.Run(listCommand(family), 60*time.Second)
	if out := strings.TrimSpace(listed.Combined()); out != "" {
		for _, line := range strings.Split(out, "\n") {
			d.logf("  share: " + line)
		}
	}
	if err != nil {
		return fmt.Errorf("listing the mount in the %s guest: %w", family, err)
	}
	d.logf(fmt.Sprintf("  share: the %s guest mounted //172.30.0.1/shared", family))
	return nil
}

// mountOnce makes one attempt at mounting the share, as ONE command in the guest's own vocabulary.
//
// NO SHELL SYNTAX BEYOND WHAT THE PROGRAM ITSELF NEEDS: no `&&`, no `||`, no loops. The single
// exception is `sudo -S` on POSIX, because an ssh exec has no terminal and sudo requires a password on
// stdin - and that is a PIPE, not a control structure.
func (d *Driver) mountOnce(client *guest.Client, family, dir string) error {
	var cmd string
	switch family {
	case "windows":
		// `net use` IS WINDOWS'S MOUNT. An empty password is spelled `""` because cmd's argument
		// splitting would otherwise drop it, and Samba then answers NT_STATUS_LOGON_FAILURE, which
		// reads as a wrong password rather than a missing argument.
		cmd = `net use "%USERPROFILE%\shared" \\172.30.0.1\shared /user:guest "" /persistent:no`
	case "macos":
		// `mount_smbfs`, NOT `mount -t cifs`: macOS has no cifs filesystem type.
		//
		// THE EMPTY PASSWORD IS THE ONLY THING THAT WORKS, and the Python implementation's note says
		// why in full: mount_smbfs sends one and cannot be made to send anything else from a script -
		// not from the URL, not from nsmb.conf (no `password` keyword exists), not from a pipe (a pipe
		// is not a tty, so the prompt reads EOF), not through `-N`. So the ACCOUNT has no password and
		// `null passwords = yes` makes smbd accept it. `user:` with the colon and nothing after it is
		// the man page's own spelling for "this user, no password".
		//
		// `-f 0666 -d 0777` makes the mount usable by the unprivileged user whose files these are: an
		// SMB mount is owned by root by default.
		cmd = `printf '%s\n' ` + shellQuote(d.pass) +
			` | sudo -S -p '' mount_smbfs -f 0666 -d 0777 '//guest:@172.30.0.1/shared' ~/shared`
	default:
		// `mount -t cifs` WITH THE CREDENTIALS ON THE COMMAND LINE, because a credentials FILE would
		// have to be copied into the guest first and that is a second thing that can fail. The share
		// account has no password, so there is no secret to leak in argv.
		cmd = `printf '%s\n' ` + shellQuote(d.pass) +
			` | sudo -S -p '' mount -t cifs //172.30.0.1/shared ~/shared -o ` +
			`username=guest,password=,vers=3.0,uid=$(id -u),gid=$(id -g),iocharset=utf8`
	}
	// THE SAME SHAPE `ssh:` LOGS, so this step reads like every other command in the walkthrough
	// rather than like a black box: the command, its exit status, and its own output. A mount that
	// failed said nothing before this and the step a person was reading reported only its own summary.
	res, err := client.Run(cmd, 120*time.Second)
	d.logf(fmt.Sprintf("  share: %s -> %d", oneLine(cmd), res.Status))
	for _, line := range headTail(res.Combined(), 12, 4) {
		d.logf("    " + line)
	}
	return err
}

// oneLine flattens a command for a log line without hiding that it was shortened.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 160 {
		return s[:160] + "..."
	}
	return s
}

// headTail is the beginning AND the end of a long output, because a mount's useful line is often last.
func headTail(s string, head, tail int) []string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= head+tail {
		return lines
	}
	out := append([]string{}, lines[:head]...)
	out = append(out, fmt.Sprintf("... %d line(s) omitted ...", len(lines)-head-tail))
	return append(out, lines[len(lines)-tail:]...)
}

// tableCommand reports the system's own view of the mounts, as one command per platform.
func tableCommand(family string) string {
	if family == "windows" {
		return "net use"
	}
	return "mount"
}

// mountsShare reports whether the platform's own mount table names the share.
//
// THE THREE SPELLINGS SIT TOGETHER HERE ON PURPOSE: Linux prints the DEVICE as `//172.30.0.1/shared`,
// macOS prints it as `//guest@172.30.0.1/shared` because mount_smbfs includes the user, and Windows
// prints an UNC path with backslashes. A check that knew only one of them reported a working mount as
// missing on the other two.
func mountsShare(family, table string) bool {
	switch family {
	case "windows":
		return strings.Contains(strings.ToLower(table), `\\172.30.0.1\shared`) ||
			strings.Contains(table, "172.30.0.1")
	case "macos":
		return strings.Contains(table, "//guest@172.30.0.1/shared")
	default:
		return strings.Contains(table, "//172.30.0.1/shared")
	}
}

// firstLines is the head of a command's output, for an error message that must stay readable.
func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// listCommand lists the mounted directory in the guest's own vocabulary.
func listCommand(family string) string {
	if family == "windows" {
		return `dir "%USERPROFILE%\shared"`
	}
	return "ls -la ~/shared"
}

// shareConfig is Samba's configuration for one directory on one bridge address.
//
// THREE LINES ARE LOAD-BEARING AND EACH HAS A FAILURE BEHIND IT:
//
//	interfaces / bind interfaces only   smbd must listen on 172.30.0.1 and not on every interface.
//	                                    The Python implementation lost a macOS ssh stage to an smbd
//	                                    that bound nothing while logging "waiting for connections".
//	null passwords = yes                the guest account has no password, and Samba refuses an empty
//	                                    one unless this says otherwise - with "NT_STATUS_LOGON_FAILURE",
//	                                    which reads as a wrong password.
//	map to guest = bad user             the guest name is not a host account, and without this Samba
//	                                    rejects it before the share is consulted.
func shareConfig(dir string) string {
	return "[global]\n" +
		"   workgroup = WORKGROUP\n" +
		"   server string = easyvm\n" +
		"   security = user\n" +
		"   map to guest = bad user\n" +
		"   null passwords = yes\n" +
		"   interfaces = 172.30.0.1/24\n" +
		"   bind interfaces only = yes\n" +
		"   smb ports = 445\n" +
		"   load printers = no\n" +
		"   printing = bsd\n" +
		"   disable spoolss = yes\n" +
		"   log file = /tmp/easydiskbake-smbd.log\n" +
		"\n[shared]\n" +
		"   path = " + dir + "\n" +
		"   browseable = yes\n" +
		"   read only = no\n" +
		"   guest ok = yes\n" +
		"   force user = root\n" +
		"   create mask = 0666\n" +
		"   directory mask = 0777\n"
}

// stopShare stops the smbd this driver started, and only that one.
func (d *Driver) stopShare() {
	d.mu.Lock()
	share := d.share
	d.share = nil
	d.mu.Unlock()
	if share == nil || share.proc == nil || share.proc.Process == nil {
		return
	}
	_ = share.proc.Process.Kill()
	_ = share.proc.Wait()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func firstLineOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}
