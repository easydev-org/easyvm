package asset

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// --- the macOS guest agent ------------------------------------------------------------
//
// APPLE'S OWN qemu-guest-agent DOES NOT LOAD UNDER QEMU ON macOS. The binary that ships with QEMU is
// built for a Linux guest: it opens `/dev/virtio-ports/...` and expects a virtio-serial port, while
// macOS under QEMU is given an ISA serial chardev because the virtio-serial driver is not part of what
// the guest can see before the agent is running. Chicken and egg, and the resolution is a different
// agent.
//
// `mav2287/mac-guest-agent` IS A NATIVE macOS BINARY THAT SPEAKS THE SAME PROTOCOL over an ISA serial
// port. It is the only thing that makes `shutdown: guest_agent` work on a Mac - and it is what the
// Python implementation used, with the reason recorded beside the URL:
//
//	macOS    mav2287/mac-guest-agent - a native agent for macOS under QEMU. Apple's own does not
//	         load here; see the note in macos/15/steps/vm-configure.yaml.

// macGuestAgent is the pinned name and the release URL.
//
// THE URL IS `releases/latest/download/<name>` RATHER THAN A VERSIONED ASSET, because that is the
// shape the project publishes and a version in the path would pin a release Apple's tooling does not
// track. The file is a single executable with no published hash, so its SIZE is the only integrity
// check available - and a truncated download is the failure that matters here, not substitution.
const (
	macGuestAgentURL  = "https://github.com/mav2287/mac-guest-agent/releases/latest/download/mac-guest-agent"
	macGuestAgentName = "mac-guest-agent"
	// macGuestAgentMinBytes is a floor, not an exact size: the release is rebuilt and an exact number
	// would make every rebuild a failing fetch. Anything smaller than this is not the binary.
	macGuestAgentMinBytes = 512 << 10
)

func fetchMacGuestAgent(spec Spec) (Result, error) {
	if err := os.MkdirAll(spec.Work, 0o755); err != nil {
		return Result{}, err
	}
	dest := filepath.Join(spec.Work, macGuestAgentName)

	// A PREVIOUS COPY IS REUSED WHEN IT IS THE RIGHT SIZE - the same rule the Xcode fetch follows,
	// minus the hash this project does not publish.
	if fi, err := os.Stat(dest); err == nil && fi.Size() >= macGuestAgentMinBytes {
		return Result{Main: dest}, nil
	}

	// FROM THIS REPOSITORY'S PACKAGE, NOT FROM A GITHUB RELEASE AT RUN TIME.
	//
	// THE RELEASE URL IS STILL THE PIN - it is what `artifacts-publish.yml` downloads from - but a
	// stage pulls `macos-asset-mac-guest-agent`, so the bake does not depend on a third-party
	// repository's release page at the moment it needs a shutdown to work.
	//
	// AND THE TAG IS THE PIN IN A WAY THE URL NEVER WAS. `releases/latest/download/<name>` resolves
	// to whatever the project published most recently, which is a different binary on a different
	// day; a tag in this package is the file this build verified, once.
	if err := pullTag(packageTag("macos-asset-", "mac-guest-agent"), dest, macGuestAgentMinBytes,
		10*time.Minute, "Apple's own agent does not load under QEMU, so this is the binary that "+
			"makes `shutdown: guest_agent` work on a Mac"); err != nil {
		return Result{}, err
	}

	fi, err := os.Stat(dest)
	if err != nil {
		return Result{}, err
	}
	if fi.Size() < macGuestAgentMinBytes {
		_ = os.Remove(dest)
		return Result{}, fmt.Errorf("%s is %d bytes and a release binary is at least %d; the "+
			"download is truncated or the URL answered with something other than the asset",
			macGuestAgentName, fi.Size(), macGuestAgentMinBytes)
	}
	// AND IT IS MADE EXECUTABLE HERE, because a copy that arrives without the bit fails inside the
	// guest with a permission error that reads as a broken download.
	if err := os.Chmod(dest, 0o755); err != nil {
		return Result{}, fmt.Errorf("making %s executable: %w", dest, err)
	}
	return Result{Main: dest}, nil
}
