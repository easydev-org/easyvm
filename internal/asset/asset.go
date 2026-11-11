// Package asset fetches the host-side files a stage copies into a guest.
//
// EVERYTHING HERE IS HOST WORK, and that is why it is a step of its own rather than a `script:`. A
// `script:` body runs INSIDE the guest; these are downloads and extractions on the machine driving
// it. Mixing the two would mean a guest needed network access to fetch a file that is then copied
// to it.
//
// THE FILES ARE NAMED BY PURPOSE, NOT BY URL. A walkthrough says `fetch: guest_agent`, and this
// package knows which archive that is - so a vendor moving a URL is one line here rather than a
// change to every file that needs the agent.
package asset

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Kind is a file a stage needs on the host before it can copy it in.
type Kind string

const (
	// GuestAgentWindows is the qemu guest agent MSI and its virtio-serial driver, both out of the
	// virtio-win ISO.
	GuestAgentWindows Kind = "guest_agent_windows"
	// GuestAgentMacOS is the agent bundle for a macOS guest.
	GuestAgentMacOS Kind = "guest_agent_macos"
	// Xcode is the .xip from archive.org, checksummed.
	Xcode Kind = "xcode"
	// Packages are the tarballs the macOS dev stage installs.
	Packages Kind = "packages"
)

// Result is what one fetch produced, so the next step can be compared against it.
type Result struct {
	// Main is the file the following `scp:` is expected to name.
	Main string
	// Extra are the other files produced, for a log.
	Extra []string
}

// Fetch produces the asset in work.
//
// A MISSING TOOL IS AN ERROR, NOT A FALLBACK. `7z` extracts the virtio-win ISO because that file is
// an ISO9660 image with a Joliet tree, and reading it by hand is how a stage ends up copying a
// truncated filename. If 7z is absent the build is wrong and this says so.
func Fetch(kind Kind, family, work string) (Result, error) {
	switch kind {
	case GuestAgentWindows:
		return fetchWindowsAgent(work)
	case GuestAgentMacOS:
		return Result{}, fmt.Errorf("fetching the macOS guest agent is not implemented yet; the "+
			"step that needs it is %q", "the next scp in the file")
	case Xcode:
		return Result{}, fmt.Errorf("fetching Xcode is not implemented yet: it is a 2.7 GiB " +
			"transfer from archive.org with a SHA-1 check, and a half-implemented version would " +
			"fail inside the guest rather than here")
	case Packages:
		return Result{}, fmt.Errorf("fetching the macOS packages is not implemented yet")
	}
	return Result{}, fmt.Errorf("%q is not an asset this build knows; the kinds are %s",
		kind, strings.Join([]string{
			string(GuestAgentWindows), string(GuestAgentMacOS), string(Xcode), string(Packages),
		}, ", "))
}

// fetchWindowsAgent extracts the agent MSI and the virtio-serial driver from the virtio-win ISO.
//
// THE MSI AND ITS DRIVER COME FROM THE SAME ISO, and they must: the driver's .inf names hardware IDs
// the MSI's service binds through, and a driver from another release is a device that installs and
// never starts.
func fetchWindowsAgent(work string) (Result, error) {
	if err := os.MkdirAll(work, 0o755); err != nil {
		return Result{}, err
	}

	isoPath := filepath.Join(work, "virtio-win.iso")
	// A PREVIOUS RUN'S ISO IS REUSED: it is 700 MiB and the network is the slow part. The file's
	// presence is not proof it is intact, so its size is checked against a floor rather than
	// trusted - a truncated ISO extracts a truncated file.
	if fi, err := os.Stat(isoPath); err != nil || fi.Size() < 100<<20 {
		return Result{}, fmt.Errorf("no virtio-win ISO at %s (need one of at least 100 MiB; a "+
			"truncated ISO extracts a truncated driver and the guest fails at the device rather "+
			"than here). Fetch it into %s first", isoPath, work)
	}

	sevenZip, err := exec.LookPath("7z")
	if err != nil {
		return Result{}, fmt.Errorf("7z is not installed: the virtio-win ISO carries a Joliet " +
			"tree, and extracting it by hand produces 8.3 filenames that do not match the driver " +
			"the file names")
	}

	var out Result
	// THE AGENT MSI IS AT THE ISO'S ROOT. The driver is under a directory per architecture.
	for _, item := range []struct {
		member string
		dest   string
	}{
		{"qemu-ga-x86_64.msi", filepath.Join(work, "qemu-ga-x86_64.msi")},
		{"vioserial/w10/amd64/vioser.inf", filepath.Join(work, "vioserial", "vioser.inf")},
	} {
		if err := os.MkdirAll(filepath.Dir(item.dest), 0o755); err != nil {
			return Result{}, err
		}
		_ = os.Remove(item.dest)
		cmd := exec.Command(sevenZip, "e", "-y", "-o"+filepath.Dir(item.dest),
			isoPath, item.member)
		if out7z, err := cmd.CombinedOutput(); err != nil {
			return Result{}, fmt.Errorf("extracting %s from the ISO: %v (%s)",
				item.member, err, firstLine(string(out7z)))
		}
		if _, err := os.Stat(item.dest); err != nil {
			// 7Z EXITS ZERO WHEN A MEMBER IS ABSENT IN SOME VERSIONS, so the file is checked.
			return Result{}, fmt.Errorf("the ISO has no %s; the archive is not the one this "+
				"stage expects", item.member)
		}
		if out.Main == "" {
			out.Main = item.dest
		} else {
			out.Extra = append(out.Extra, item.dest)
		}
	}
	return out, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 160 {
		s = s[:160] + "..."
	}
	return s
}
