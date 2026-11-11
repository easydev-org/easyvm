package run

import (
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/pkg/sftp"

	"github.com/easydev-org/easyvm/internal/guest"
)

// scpFile copies a local file to a path in the guest.
//
// IT USES SFTP OVER THE SESSION WE ALREADY HAVE, not the `scp` binary and not `scp -O`. Three
// reasons, in order of how much they cost:
//
//  1. THE SESSION IS ALREADY AUTHENTICATED. A separate `scp` process would have to log in again,
//     and the Python implementation's version needed `sshpass` on the host plus `-o
//     StrictHostKeyChecking=no` to make that work at all.
//  2. THE REMOTE PATH IS THE GUEST'S, NOT A SHELL'S. `scp` hands its argument to the remote shell,
//     so a Windows path with a backslash or a space is re-parsed there; SFTP takes it verbatim.
//     That is the same class of bug as the `--override` quotes.
//  3. A FAILED COPY IS AN ERROR WITH A STATUS. `scp` reports "lost connection" for a full disk, a
//     missing directory and a bad path alike.
func scpFile(client *guest.Client, local, remote string) error {
	src, err := os.Open(local)
	if err != nil {
		return fmt.Errorf("open %s: %w", local, err)
	}
	defer src.Close()

	conn := client.Raw()
	if conn == nil {
		return fmt.Errorf("the ssh connection is closed")
	}
	sc, err := sftp.NewClient(conn)
	if err != nil {
		return fmt.Errorf("sftp: %w", err)
	}
	defer sc.Close()

	// THE PARENT DIRECTORY IS NOT CREATED FOR YOU. A copy to a directory that does not exist
	// fails with a bare "no such file", which reads as a missing source rather than a missing
	// destination - so it is created, and only its failure is reported.
	dir := path.Dir(strings.ReplaceAll(remote, `\`, "/"))
	if dir != "" && dir != "." && dir != "/" {
		_ = sc.MkdirAll(dir)
	}

	dst, err := sc.Create(remote)
	if err != nil {
		return fmt.Errorf("create %s: %w", remote, err)
	}
	defer dst.Close()

	n, err := dst.ReadFrom(src)
	if err != nil {
		return fmt.Errorf("copy %s: %w", remote, err)
	}

	// THE SIZE IS CHECKED ON BOTH SIDES, AND THE WRITE COUNT IS NOT ENOUGH.
	//
	// `n` is what this process SENT. A middlebox that cuts the stream after the SFTP layer has
	// acknowledged the bytes produces a file that is short ON THE GUEST and complete as far as the
	// writer knows - and a truncated .xip then fails at `xip -x` twenty minutes later with a
	// message about a corrupt archive, which is the wrong place to learn about a proxy.
	//
	// So the remote file is asked for its own size, with a stat, and the two are compared:
	//
	//	local stat    what the file should be
	//	write count   what was handed to the channel
	//	remote stat   what arrived
	//
	// All three agreeing is the only version of "the copy worked" that does not depend on trusting
	// one end.
	localSize := int64(-1)
	if fi, statErr := src.Stat(); statErr == nil {
		localSize = fi.Size()
		if n != localSize {
			return fmt.Errorf("%s: wrote %d of %d bytes", remote, n, localSize)
		}
	}
	remoteInfo, statErr := sc.Stat(remote)
	if statErr != nil {
		return fmt.Errorf("%s: the copy finished but the file cannot be stat'ed: %w", remote, statErr)
	}
	if localSize >= 0 && remoteInfo.Size() != localSize {
		return fmt.Errorf("%s: %d bytes arrived of %d; a proxy or the guest cut the stream, and "+
			"a truncated file fails much later with a message about a corrupt archive",
			remote, remoteInfo.Size(), localSize)
	}
	return nil
}
