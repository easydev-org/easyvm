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
	"time"

	"github.com/easydev-org/easyvm/internal/answer"
	"github.com/easydev-org/easyvm/internal/oci"
	"github.com/easydev-org/easyvm/internal/opencore"
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
	// AnswerISO is Windows' answer CD: Autounattend.xml at the root plus the virtio drivers in
	// $WinPEDriver$. It is an asset like the others - a host-side file a `boot:` names - and it is
	// produced rather than downloaded, which is the only difference.
	AnswerISO Kind = "answer_iso"
	// VirtioWin is the vendor's driver ISO, the input the answer CD's drivers come out of.
	VirtioWin Kind = "virtio_win"
	// WindowsISO is the Windows installer image. It is 7.7 GiB and does not live in this
	// repository; a `fetch:` either finds it already in the work directory or pulls it.
	WindowsISO Kind = "windows_iso"
	// RecoveryMacOS is the recovery image a macOS guest installs from - and the file Apple's APFS
	// driver is extracted out of.
	RecoveryMacOS Kind = "recovery_macos"
	// OpenCore is the firmware a macOS guest boots through: a boot image, an OVMF code file and a
	// writable variables copy, all built from four pinned assets.
	OpenCore Kind = "opencore"
	// LinuxISO is a distribution's installer image, named by its own `release:`.
	LinuxISO Kind = "linux_iso"
)

// Result is what one fetch produced, so the next step can be compared against it.
type Result struct {
	// Main is the file the following `scp:` is expected to name.
	Main string
	// Extra are the other files produced, for a log.
	Extra []string
}

// Spec is what a `fetch:` step says, in the shape the asset layer needs.
//
// IT IS A STRUCT RATHER THAN MORE ARGUMENTS because the kinds do not share a shape: an ISO is one
// download, an answer CD is that ISO's contents rewritten into a new image, and the agent MSI is
// two files out of the same ISO. A parameter list that tried to serve all three would be mostly
// empty strings.
type Spec struct {
	// Kind is which asset.
	Kind Kind
	// Release is the OS version, for an asset that is per-release (a macOS recovery image).
	Release string
	// Recovery is the recovery image a macOS build extracts its APFS driver out of.
	Recovery string
	// Family is the guest's OS, for an asset whose content depends on it.
	Family string
	// Work is where the file goes.
	Work string
	// Values are the placeholders an answer file needs substituted.
	Values map[string]string
	// Template is the answer file to build from, when the kind needs one.
	Template string
}

// Fetch produces the asset in spec.Work.
//
// A MISSING TOOL IS AN ERROR, NOT A FALLBACK. `7z` extracts the virtio-win ISO because that file is
// an ISO9660 image with a Joliet tree, and reading it by hand is how a stage ends up copying a
// truncated filename. If 7z is absent the build is wrong and this says so.
func Fetch(spec Spec) (Result, error) {
	kind, work := spec.Kind, spec.Work

	// `guest_agent` IS THE FILE'S NAME FOR IT, AND THE FAMILY PICKS THE BINARY.
	//
	// TWO FILES WRITE `fetch: guest_agent` - windows/11/steps/vm-configure.yaml and
	// macos/15/steps/vm-configure.yaml - and both were refused with
	//
	//	"guest_agent" is not an asset this build knows; the kinds are guest_agent_windows,
	//	guest_agent_macos, ...
	//
	// WHICH IS A VOCABULARY THE CALLER DID NOT NEED TO KNOW. The family is already on the step (or
	// on the walkthrough), so the suffix is a fact the implementation can resolve rather than one a
	// file should have to repeat. The two explicit kinds still work - a file that wants to be
	// unambiguous may say one - and this is the default.
	if kind == "guest_agent" {
		switch spec.Family {
		case "macos":
			kind = GuestAgentMacOS
		default:
			kind = GuestAgentWindows
		}
	}
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

	// THE VENDOR'S DRIVER ISO, PULLED FROM THIS REPOSITORY'S OWN PACKAGE.
	//
	// It is 700 MiB and the registry still has it, so a previous run's copy is reused - with its
	// size checked, because a truncated ISO extracts a truncated driver and the guest then fails
	// at the device rather than here.
	case VirtioWin:
		return fetchVirtioWin(work)

	// THE ANSWER CD, BUILT RATHER THAN DOWNLOADED: the template is in the repository and the
	// drivers are extracted out of the vendor ISO.
	case AnswerISO:
		return fetchAnswerISO(spec)

	// THE INSTALLER ITSELF. Named by `iso:` on a boot step; this fetches or finds it.
	case WindowsISO:
		return fetchWindowsISO(work)

	// macOS'S TWO INPUTS, IN THE ORDER THEY ARE NEEDED: the recovery image first, because the
	// firmware is built FROM it - Apple's APFS driver is inside that image, and OpenCore cannot read
	// an APFS partition without it.
	case RecoveryMacOS:
		return fetchRecoveryMacOS(spec)
	case OpenCore:
		return fetchOpenCore(spec)
	case LinuxISO:
		return fetchLinuxISO(spec)
	}
	return Result{}, fmt.Errorf("%q is not an asset this build knows; the kinds are %s",
		kind, strings.Join([]string{
			string(GuestAgentWindows), string(GuestAgentMacOS), string(Xcode), string(Packages),
			string(VirtioWin), string(AnswerISO), string(WindowsISO),
			string(RecoveryMacOS), string(OpenCore), string(LinuxISO),
		}, ", "))
}

// virtioWinTag is the artifact this repository publishes the vendor ISO under.
//
// ONE PACKAGE AND A PREFIXED TAG, which is the convention every artifact here follows: a tag says
// what kind of thing it is, and there is one place to look.
const virtioWinTag = "iso-virtio-win"

// virtioWinMinSize is the floor a usable ISO has to clear.
//
// THE UPSTREAM ISO IS 836 MiB and a drivers-only repack is 56 MiB. Both are usable for the answer
// CD's $WinPEDriver$ tree; the repack has no guest-agent directory at all, which is a DIFFERENT
// question and one this build answers later (vm-configure fetches the agent separately). 40 MiB
// rejects a truncated file and an HTML error page, and accepts both real shapes.
const virtioWinMinSize = 40 << 20

// fetchVirtioWin pulls the vendor driver ISO, reusing one that is already there.
func fetchVirtioWin(work string) (Result, error) {
	if err := os.MkdirAll(work, 0o755); err != nil {
		return Result{}, err
	}
	dest := filepath.Join(work, "virtio-win.iso")
	if fi, err := os.Stat(dest); err == nil && fi.Size() >= virtioWinMinSize {
		return Result{Main: dest}, nil
	}

	repository := os.Getenv("DISKS_REPOSITORY")
	if repository == "" {
		repository = "easydev-org/easydiskbake"
	}
	art := oci.Artifact{
		Repository: strings.ToLower(repository),
		Tag:        virtioWinTag,
		Token:      os.Getenv("GITHUB_TOKEN"),
	}
	ctx, cancel := oci.WithTimeout(30 * time.Minute)
	defer cancel()
	if err := oci.Pull(ctx, art, dest, func(msg string) { _ = msg }); err != nil {
		return Result{}, fmt.Errorf("pulling %s from %s: %w. Without the driver ISO the answer CD "+
			"carries no $WinPEDriver$ tree, windowsPE lists no disks, and Setup stops at a dialog "+
			"nobody clicks", virtioWinTag, repository, err)
	}
	fi, err := os.Stat(dest)
	if err != nil {
		return Result{}, err
	}
	if fi.Size() < virtioWinMinSize {
		return Result{}, fmt.Errorf("%s is %d MiB, below the %d MiB floor: a truncated ISO "+
			"extracts a truncated driver", dest, fi.Size()>>20, virtioWinMinSize>>20)
	}
	return Result{Main: dest}, nil
}

// windowsISOTag is the artifact the installer image is published under.
const windowsISOTag = "iso-windows11"

// windowsISOMinSize is the floor a usable installer image has to clear.
//
// THE PINNED IMAGE IS 7736125440 BYTES. The floor is well below it so a later release that is
// slightly smaller still passes, and well above the size of a truncated download or an error page.
const windowsISOMinSize = 2000 << 20

// fetchWindowsISO finds or pulls the Windows installer image.
func fetchWindowsISO(work string) (Result, error) {
	if err := os.MkdirAll(work, 0o755); err != nil {
		return Result{}, err
	}
	dest := filepath.Join(work, "windows11.iso")
	if fi, err := os.Stat(dest); err == nil && fi.Size() >= windowsISOMinSize {
		return Result{Main: dest}, nil
	}

	repository := os.Getenv("DISKS_REPOSITORY")
	if repository == "" {
		repository = "easydev-org/easydiskbake"
	}
	art := oci.Artifact{
		Repository: strings.ToLower(repository),
		Tag:        windowsISOTag,
		Token:      os.Getenv("GITHUB_TOKEN"),
	}
	ctx, cancel := oci.WithTimeout(60 * time.Minute)
	defer cancel()
	if err := oci.Pull(ctx, art, dest, func(msg string) { _ = msg }); err != nil {
		return Result{}, fmt.Errorf("pulling %s from %s: %w. The installer image is 7.7 GiB and "+
			"is not in this repository; it must be published under that tag, or placed at %s "+
			"before the run", windowsISOTag, repository, err, dest)
	}
	fi, err := os.Stat(dest)
	if err != nil {
		return Result{}, err
	}
	if fi.Size() < windowsISOMinSize {
		return Result{}, fmt.Errorf("%s is %d MiB, below the %d MiB floor: a truncated installer "+
			"is an ISO that boots to the firmware and stops", dest, fi.Size()>>20,
			windowsISOMinSize>>20)
	}
	return Result{Main: dest}, nil
}

// recoveryMacOSTag is the artifact the recovery image is published under.
//
// IT IS PER RELEASE AND NOT A PREFIX SCAN: easydev-org publishes macos-recovery-sequoia for 15, and
// a caller that guessed would be a second place the release name lives.
func recoveryMacOSTag(release string) string {
	if release == "" {
		release = "15"
	}
	names := map[string]string{
		"15": "sequoia",
		"26": "tahoe",
	}
	if name, ok := names[release]; ok {
		return "macos-recovery-" + name
	}
	return "macos-recovery-" + release
}

// fetchRecoveryMacOS pulls the recovery image for a release.
//
// IT IS AN INPUT EVEN TO A STAGE THAT DOES NOT BOOT FROM IT - opencore.Build extracts Apple's APFS
// driver out of it - which is why it is an asset rather than part of a boot step.
func fetchRecoveryMacOS(spec Spec) (Result, error) {
	if err := os.MkdirAll(spec.Work, 0o755); err != nil {
		return Result{}, err
	}
	dest := filepath.Join(spec.Work, "recovery.dmg")
	// THE RECOVERY IMAGE IS REISSUED BY APPLE ON EVERY REQUEST, so there is no sha256 to pin -
	// unlike every other asset here. What IS checked is that it is a plausible image, which catches
	// a truncated transfer and an HTML error page.
	if fi, err := os.Stat(dest); err == nil && fi.Size() > 200<<20 {
		return Result{Main: dest}, nil
	}
	repository := os.Getenv("DISKS_REPOSITORY")
	if repository == "" {
		repository = "easydev-org/easydiskbake"
	}
	tag := recoveryMacOSTag(spec.Release)
	art := oci.Artifact{
		Repository: strings.ToLower(repository),
		Tag:        tag,
		Token:      os.Getenv("GITHUB_TOKEN"),
	}
	ctx, cancel := oci.WithTimeout(45 * time.Minute)
	defer cancel()
	if err := oci.Pull(ctx, art, dest, func(string) {}); err != nil {
		return Result{}, fmt.Errorf("pulling %s from %s: %w. The recovery image is the installer "+
			"AND the source of Apple's APFS driver, so a macOS stage cannot start without it",
			tag, repository, err)
	}
	return Result{Main: dest}, nil
}

// fetchOpenCore builds the firmware a macOS guest boots through.
//
// THREE OUTPUTS, NOT ONE, and the caller names all three: the boot image is what the firmware
// starts, the ROM is pflash code, and the variables file is per-guest NVRAM that qemu WRITES to - so
// it must be a copy, never the pinned template.
func fetchOpenCore(spec Spec) (Result, error) {
	out, err := opencore.Build(opencore.Spec{
		Release:  spec.Release,
		Work:     spec.Work,
		Recovery: spec.Recovery,
	}, func(string) {})
	if err != nil {
		return Result{}, err
	}
	return Result{
		Main:  out.Boot,
		Extra: []string{out.ROM, out.Vars},
	}, nil
}

// linuxISOTag is the ghcr tag a Linux installer image would be published under.
//
// NONE OF THEM IS PUBLISHED YET, and that is a fact rather than a bug in the lookup: the Python
// workflow downloaded each ISO from the vendor on every run, because a 3-to-7 GiB image per stage was
// not worth a package. The tag convention exists so publishing them is a publish and not a code
// change.
func linuxISOTag(release string) string { return "iso-" + release }

// fetchLinuxISO finds or pulls a distribution's installer image.
//
// THREE SOURCES, IN THIS ORDER, AND THE ORDER IS THE POINT:
//
//	1  an image already in the work directory, which is how CI hands one over
//	2  the ghcr tag `iso-<release>`, if this repository has published it
//	3  NOTHING. A vendor download here would be a 7 GiB transfer inside a walkthrough step, with the
//	   stage clock running, and the Python implementation kept that in a separate `fetch_iso.py`
//	   precisely so it was not part of a walk.
//
// A MISSING IMAGE IS A SENTENCE ABOUT THE FILE, not a boot that hangs: `boot: iso: installer_iso`
// resolves to the path this step records, and Validate reports a missing installer by name.
func fetchLinuxISO(spec Spec) (Result, error) {
	if err := os.MkdirAll(spec.Work, 0o755); err != nil {
		return Result{}, err
	}
	release := spec.Release
	if release == "" {
		return Result{}, fmt.Errorf("`fetch: linux_iso` needs a `release:` - the distribution's name, " +
			"which is also the tag its image would be published under")
	}

	// 1. ALREADY HERE. The workflow puts it in the work directory, and so does a manual run.
	for _, name := range []string{release + ".iso", "installer.iso"} {
		path := filepath.Join(spec.Work, name)
		if fi, err := os.Stat(path); err == nil && fi.Size() > 500<<20 {
			return Result{Main: path}, nil
		}
	}

	// 2. THE PUBLISHED ARTIFACT, IF THERE IS ONE.
	repository := os.Getenv("DISKS_REPOSITORY")
	if repository == "" {
		repository = "easydev-org/easydiskbake"
	}
	tag := linuxISOTag(release)
	dest := filepath.Join(spec.Work, "installer.iso")
	art := oci.Artifact{
		Repository: strings.ToLower(repository), Tag: tag, Token: os.Getenv("GITHUB_TOKEN"),
	}
	ctx, cancel := oci.WithTimeout(45 * time.Minute)
	defer cancel()
	if err := oci.Pull(ctx, art, dest, func(string) {}); err == nil {
		if fi, err := os.Stat(dest); err == nil && fi.Size() > 500<<20 {
			return Result{Main: dest}, nil
		}
	}

	// 3. NOTHING, SAID CLEARLY. The two ways to fix it are named, because a reader who sees this in a
	// log needs to know whether to publish the artifact or to place a file.
	return Result{}, fmt.Errorf("no installer image for %q. Looked for %s and %s in %s, and for the "+
		"published tag %s/%s. Place the ISO at %s, or publish it as %s - this step does not download "+
		"from a vendor, because a 3-to-7 GiB transfer inside a walkthrough runs with the stage clock",
		release, release+".iso", "installer.iso", spec.Work, repository, tag,
		filepath.Join(spec.Work, "installer.iso"), tag)
}

// fetchAnswerISO builds Windows' answer CD.
//
// THE DRIVERS COME OUT OF THE SAME ISO THIS REPOSITORY PUBLISHES, so this either uses one already
// in the work directory or pulls it - and a failure to get it is a failure to build a CD that can
// install Windows at all.
func fetchAnswerISO(spec Spec) (Result, error) {
	if spec.Template == "" {
		return Result{}, fmt.Errorf("`fetch: answer_iso` needs a `template:` - the answer file to " +
			"substitute into")
	}
	virtio, err := fetchVirtioWin(spec.Work)
	if err != nil {
		return Result{}, err
	}
	out := filepath.Join(spec.Work, "answer.iso")
	err = answer.Build(answer.Spec{
		Template:  spec.Template,
		Out:       out,
		VirtioISO: virtio.Main,
		Values:    spec.Values,
		Label:     "ANSWER",
	}, func(msg string) { _ = msg })
	if err != nil {
		return Result{}, err
	}
	return Result{Main: out, Extra: []string{virtio.Main}}, nil
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
	// THE ISO IS PULLED WHEN IT IS NOT THERE, and it used to be a caller's problem:
	//
	//	error: fetch the agent and its driver on the host: no virtio-win ISO at .../virtio-win.iso
	//	       (need one of at least 100 MiB ...). Fetch it into .../work first
	//
	// - a sentence that tells a CI job to do something it has no step for. The ISO is in this
	// repository's own package, `iso-virtio-win`, and `fetchVirtioWin` already knows how to pull it
	// with resume. The agent's MSI and the vioserial driver come out of THAT ISO, so the two kinds
	// of `fetch:` share one file.
	//
	// A PREVIOUS RUN'S ISO IS REUSED: it is 700 MiB and the network is the slow part. The file's
	// presence is not proof it is intact, so its size is checked against a floor rather than
	// trusted - a truncated ISO extracts a truncated file.
	if fi, err := os.Stat(isoPath); err != nil || fi.Size() < 100<<20 {
		if _, err := fetchVirtioWin(work); err != nil {
			return Result{}, fmt.Errorf("the virtio-win ISO is not here and could not be fetched: %w. "+
				"The agent's MSI and its vioserial driver both come out of that one image", err)
		}
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
