package daemon

// --- the host share -------------------------------------------------------------------
//
// THE SHARE IS ALWAYS ON, AND THAT RETIRED FOUR KEYS.
//
// `share_mount:` was the name that made this look like filesystem work. It was not, and had not been
// for a long time: the SMB transport that once justified it was replaced by HTTP, with the
// implementation's own comment saying "IT USED TO MOUNT AN SMB SHARE". What the step actually did
// was run `curl` in the guest over the ssh session the file already had - a command a file can write
// itself, and one the very next step in windows/vm-configure already wrote by hand.
//
// Around it stood three more keys, each existing only to configure the one before it:
//
//	share_serve   set d.shareDir           - a variable, on a listener that was already running
//	share_family  chose curl vs Invoke-WebRequest, with `macos` and `linux` taking the same branch
//	share_dir     named the directory       - used ZERO times in the whole tree
//
// SO ALL FOUR ARE GONE. The directory is a fact about the daemon rather than something a file
// decides: one run has one share, it is `~/shared`, and it is created when first asked for. A file
// that wants to read from it writes the URL, which is the same thing every other host service in
// this vocabulary makes a file do.

import (
	"fmt"
	"os"
	"path/filepath"
)

// sharedDirName is the directory under the user's home that a guest fetches from.
//
// IT IS ONE NAME IN ONE PLACE because `internal/bootstrap` writes the same path into the launcher it
// generates, and the two must not drift: a launcher that fetches `/shared/bootstrap.sh` from a
// directory the daemon calls something else fails at login, hours after the stage that wrote it
// reported success.
const sharedDirName = "shared"

// defaultShareDir is where the share lives when nobody has said otherwise.
func defaultShareDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, sharedDirName)
}

// ServeShare is retained for the driver interface and for a caller that wants a directory OTHER
// than the default - a test, or a future mode that publishes a build output.
//
// IT NO LONGER HAS A STEP BEHIND IT. No file in this repository writes `share_serve:` any more, so
// the only callers are Go callers, which is why the error below can name a second call rather than a
// step.
func (d *Driver) ServeShare(dir string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if dir == "" {
		dir = defaultShareDir()
	}
	if d.shareDir != "" && d.shareDir != dir {
		return fmt.Errorf("this run is already serving %s; one run has one share, so a second "+
			"directory has to be a new run", d.shareDir)
	}
	if err := prepareShareDir(dir); err != nil {
		return err
	}
	d.shareDir = dir
	d.logf(fmt.Sprintf("  share: %s is served at %s", dir, shareURL(0)))
	return nil
}

// prepareShareDir makes the directory exist and be readable by a guest.
//
// 0777 IS DELIBERATE AND IT IS NOT A PERMISSION BUG: the guest is a different machine with a
// different uid, and it reaches this directory over HTTP, so the mode is what lets the daemon's own
// process read what a stage wrote. Nothing in it is a secret - a bootstrap script and build outputs.
func prepareShareDir(dir string) error {
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return fmt.Errorf("creating the shared directory %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		return fmt.Errorf("making %s writable: %w", dir, err)
	}
	return nil
}

// ShareDir is the directory being served, creating the default on first ask.
//
// IT LAZILY CREATES RATHER THAN RETURNING "" SO THAT `handleShare` NEVER HAS TO SAY "a share_serve:
// step must run first" - that step no longer exists, so the message would tell a reader to write a
// key the vocabulary would refuse.
func (d *Driver) ShareDir() string {
	d.mu.Lock()
	dir := d.shareDir
	d.mu.Unlock()
	if dir != "" {
		return dir
	}

	// THE DEFAULT IS ADOPTED UNDER THE SAME LOCK THAT PUBLISHES IT, so two guests asking at once
	// cannot create two directories and serve one of them.
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.shareDir != "" {
		return d.shareDir
	}
	dir = defaultShareDir()
	if err := prepareShareDir(dir); err != nil {
		// A DIRECTORY THAT CANNOT BE CREATED IS REPORTED BY THE HANDLER, which is where a caller can
		// turn it into a 503 with a reason. Returning the path anyway would turn a permission problem
		// into "404 not found", which reads like the file and hides the cause.
		d.logf(fmt.Sprintf("  share: %s cannot be prepared: %v", dir, err))
		return dir
	}
	d.shareDir = dir
	d.logf(fmt.Sprintf("  share: %s is served at %s", dir, shareURL(0)))
	return dir
}

// ShareURL is where a guest fetches the shared directory, or "" when the daemon has no address yet.
//
// IT IS A METHOD RATHER THAN A CONSTANT because the port is the daemon's own, and a test that
// listens elsewhere has to get the URL that matches.
func (d *Driver) ShareURL() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return shareURL(0)
}
