package daemon

// THE SHARE IS HTTP, NOT SMB, AND THIS FILE IS THE WHOLE OF IT.
//
// WHY IT WAS REWRITTEN. SMB cost each guest family a different client, and one of them could not be
// reached at all:
//
//	linux    apt-get install cifs-utils, then `mount -t cifs`
//	macos    the base mount_smbfs, which is Apple's own SMB2/3 implementation
//	windows  net use
//	bsd      NOTHING IN THE BASE SYSTEM COULD CONNECT. Measured with a capture on the guest's own
//	         vtnet0: FreeBSD's mount_smbfs dials NetBIOS 139 because netsmb implements SMB1, and
//	         smbd listens on 445 with SMB3 - so the kernel got a TCP RST and the library reported it
//	         as `syserr = Connection refused`, which reads like a network fault. The man page says it
//	         outright: "mount_smbfs offers support for SMB/CIFS/SMB1. It does not support newer
//	         versions of the protocol like SMB2 and SMB3."
//
//	         Working around it took a ports package (fusefs-smbnetfs), a base filesystem (nullfs), a
//	         root-only mount option (`allow_other`) and a mount table that names the bind rather than
//	         the share. That is four mechanisms to make one guest read a directory.
//
//	         It worked. It was measured in both directions. It was also the most fragile thing in the
//	         chain, and every one of those four mechanisms was a place a later change could break it.
//
// SO THE TRANSPORT IS THE ONE EVERY GUEST ALREADY HAS: HTTP, on the daemon's own port, which is the
// same listener that serves `/api/state`, `/screen.png` and the console. No client package, no
// kernel module, no mount, no privilege. A guest fetches a file with whatever the platform ships:
//
//	linux/bsd   curl -fsS http://172.30.0.1:8080/shared/x -o /tmp/x
//	macos       the same curl
//	windows     Invoke-WebRequest, which is PowerShell's own
//
// AND THE ONE CONSUMER THAT MATTERED - the launcher running `bootstrap.sh` at login - becomes a
// download instead of a mount. See `internal/bootstrap`: the script it used to wait for in ~/shared
// is now fetched from this endpoint, with a retry, because a login trigger can fire before the
// daemon is ready and "the file is not there yet" and "the file is not there" must not look alike.

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	easyvmnet "github.com/easydev-org/easyvm/internal/net"
	"github.com/easydev-org/easyvm/internal/run"
)

// SharePath is the URL prefix the shared directory is served under.
//
// IT IS A PREFIX WITH A TRAILING SLASH because `http.FileServer` and `http.StripPrefix` both need
// one: without it, `/sharedfoo` would be routed here and stripped to `foo`.
const SharePath = "/shared/"

// shareURL is the address a guest fetches from. The bridge address is the one every guest can
// reach - it is the same address the ssh forward and the SMB server used.
//
// IT CARRIES THE PORT BECAUSE THERE IS NO LONGER A SECOND LISTENER. The daemon already has one, and
// a shared directory does not need its own.
func shareURL(port int) string {
	// ZERO MEANS "THE DAEMON'S OWN PORT", which is the only one there is: the share rides the same
	// listener as `/api/state` and `/screen.png`. The parameter exists so a test can name a port.
	if port == 0 {
		port = 8080
	}
	return fmt.Sprintf("http://%s:%d%s", easyvmnet.BridgeIP, port, SharePath)
}

// handleShare serves the shared directory.
//
// READ-ONLY, AND THAT IS A DECISION RATHER THAN AN OMISSION. The directory exists to hand a guest
// something to run; a guest that could also write into it would be able to leave a file the NEXT
// stage inherits, which is exactly the failure `internal/bootstrap` already guards against on the
// old SMB side ("the log and the marker are not in the share, because the share is what is being
// served: a file written back into it would be one the next stage inherits").
//
// A GUEST THAT MUST RETURN SOMETHING HAS `ssh:` AND DOES NOT NEED THIS. Every walkthrough in this
// repository already drives its guest over ssh, which is bidirectional and needs no share at all.
func (d *Daemon) handleShare(w http.ResponseWriter, r *http.Request) {
	// ONLY A READ IS A SHARE. Anything else is refused BY NAME, because a 405 with no explanation is
	// the kind of answer that sends someone looking for a client bug.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "the share is read-only: fetch the file and run it, or use `ssh:` to return data",
			http.StatusMethodNotAllowed)
		return
	}

	// THE DIRECTORY IS ASKED OF THE MACHINE THAT IS SERVING IT, so a daemon with no machine answers
	// 503 rather than serving whatever a previous run left behind.
	//
	// THE MESSAGE NO LONGER TELLS A READER TO WRITE A `share_serve:` STEP. That key is gone: the
	// share is always on, so "no directory" now means one of two things a reader can act on - there
	// is no machine in this daemon yet, or the directory could not be created (a permission problem,
	// which `ShareDir` logs with the underlying error).
	var dir string
	if err := d.withMachine(func(dr run.Driver) error {
		if sv, ok := dr.(interface{ ShareDir() string }); ok {
			dir = sv.ShareDir()
		}
		return nil
	}); err != nil {
		http.Error(w, "no machine is running, so there is nothing to serve a share from",
			http.StatusServiceUnavailable)
		return
	}
	if dir == "" {
		http.Error(w, "the shared directory could not be prepared; see the daemon log",
			http.StatusServiceUnavailable)
		return
	}

	// A FILE SERVER WITH A PHONE BOOK'S WORTH OF WARNINGS is still the right tool: it handles ranges,
	// If-Modified-Since, directory listings and content types, and every one of those is a thing a
	// hand-written handler gets subtly wrong.
	//
	// THE PREFIX IS STRIPPED HERE RATHER THAN AT REGISTRATION so that this handler owns the decision
	// and the registration stays one line.
	http.StripPrefix(strings.TrimSuffix(SharePath, "/"),
		http.FileServer(http.Dir(dir))).ServeHTTP(w, r)
}

// ShareFile is the path of one file in the share, or an error that says why it is not there.
//
// IT REFUSES A NAME THAT ESCAPES THE DIRECTORY, and that is not theoretical: `http.FileServer`
// rejects `..` in a URL, but a caller building a path by hand - a launcher, a test - does not, and a
// traversal here would read the host's filesystem into a guest.
func (d *Driver) ShareFile(name string) (string, error) {
	dir := d.ShareDir()
	if dir == "" {
		return "", fmt.Errorf("no directory is being shared; a `share_serve:` step must run first")
	}
	if name == "" {
		return "", fmt.Errorf("no file name given")
	}
	// `filepath.Clean` FIRST, THEN THE PREFIX TEST, because `x/../../etc/passwd` is only recognisable
	// as an escape after it has been resolved.
	full := filepath.Join(dir, filepath.Clean("/"+name))
	if full != dir && !strings.HasPrefix(full, dir+string(os.PathSeparator)) {
		return "", fmt.Errorf("%q escapes the shared directory", name)
	}
	return full, nil
}
