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

// MountShare makes the guest mount the share on the bridge.
//
// IT IS ONE `mount -t cifs` WITH THE CREDENTIALS ON THE COMMAND LINE, because a credentials FILE
// would have to be copied into the guest first and that is a second thing that can fail. The share
// account has no password - `null passwords = yes` in the config - so there is no secret to leak
// by putting it in argv.
func (d *Driver) MountShare(family string) error {
	d.mu.Lock()
	share := d.share
	user := d.pass
	d.mu.Unlock()
	if share == nil {
		return fmt.Errorf("no share is being served; a `share_serve:` step must come first")
	}
	_ = user

	client := d.Guest()
	if client == nil {
		return fmt.Errorf("no ssh connection; a `require_ssh:` step must come first")
	}

	// THE MOUNT POINT IS THE GUEST'S, AND SO IS THE COMMAND: this is a `ssh:` wearing a key's name,
	// because "mount it" is one action and giving it a name is what lets a file say WHAT it wants
	// rather than HOW.
	cmd := strings.Join([]string{
		"set -e",
		"mkdir -p ~/shared",
		"sudo umount ~/shared 2>/dev/null || true",
		"sudo mount -t cifs //172.30.0.1/shared ~/shared -o username=guest,password=,vers=3.0,uid=$(id -u),gid=$(id -g),iocharset=utf8",
		"ls -la ~/shared | head -5",
	}, " && ")
	res, err := client.Run("sh -c "+shellQuote(cmd), 120*time.Second)
	if err != nil {
		return fmt.Errorf("mounting the share in the guest: %w (%s)", err, firstLineOf(res.Combined()))
	}
	d.logf("  share: the guest mounted //172.30.0.1/shared at ~/shared")
	return nil
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
