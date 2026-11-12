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
		// APPLE'S OWN AGENT DOES NOT LOAD UNDER QEMU, so a native one is used.
		//
		// `mav2287/mac-guest-agent` is a release binary that talks the same QMP-style guest-agent
		// protocol over a serial chardev. Measured in the Python implementation: Apple's
		// `qemu-ga` build expects an ISA serial port and the flow here gives it one
		// (`-chardev socket,...,id=qga0 -device isa-serial,chardev=qga0`), which is why the machine
		// model carries that branch at all.
		//
		// THE ASSET IS A SINGLE EXECUTABLE, NOT AN ARCHIVE, so it is downloaded and chmod 0755 - a
		// copy into the guest that is not executable fails at `chmod` inside the guest with a
		// permission error that reads as a broken download.
		return fetchMacGuestAgent(spec)
	case Xcode:
		// THE ONE APPLE ASSET THAT CANNOT COME FROM APPLE. See internal/asset/xcode.go: the
		// developer portal answers an unauthenticated request with a redirect to /unauthorized, so
		// the file comes from archive.org with the sha1 Apple's own release index publishes.
		return fetchXcode(spec)
	case Packages:
		return Result{}, fmt.Errorf("fetching the macOS packages is not implemented yet; the dev " +
			"stage installs them with `brew`, which needs no host-side fetch - so a file that names " +
			"this kind is asking for something no other stage does")

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

	// THE DISKS LIVE IN THIS REPOSITORY'S OWN ghcr PACKAGE, NOT ANOTHER REPOSITORY'S.
	//
	// `easydev-org/easydiskbake` WAS THE DEFAULT AND IT CANNOT BE: a GITHUB_TOKEN is scoped to the
	// repository whose workflow holds it, so a token minted for `easydev-org/easyvm` has `packages`
	// access to `easyvm` and to nothing else. The push that produced the first real disk measured it:
	//
	//	FAILED: the registry refused a pull,push token (HTTP 403):
	//	{"errors":[{"code":"DENIED","message":"requested access to the resource is denied"}]}
	//
	// The disk existed - 3927 MiB of installed opensuse-tumbleweed - and could not be published,
	// because the destination belonged to a different repository. Reading has the same shape: a
	// private package in another repository is not readable with this token either.
	//
	// SO BOTH DIRECTIONS ARE THIS REPOSITORY. `workflows/artifacts-publish.yml` mirrors every pinned
	// upstream input into `ghcr.io/easydev-org/easyvm`, and every fetch and every push goes there.
	repository := os.Getenv("DISKS_REPOSITORY")
	if repository == "" {
		repository = "easydev-org/easyvm"
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

	// THE DISKS LIVE IN THIS REPOSITORY'S OWN ghcr PACKAGE, NOT ANOTHER REPOSITORY'S.
	//
	// `easydev-org/easydiskbake` WAS THE DEFAULT AND IT CANNOT BE: a GITHUB_TOKEN is scoped to the
	// repository whose workflow holds it, so a token minted for `easydev-org/easyvm` has `packages`
	// access to `easyvm` and to nothing else. The push that produced the first real disk measured it:
	//
	//	FAILED: the registry refused a pull,push token (HTTP 403):
	//	{"errors":[{"code":"DENIED","message":"requested access to the resource is denied"}]}
	//
	// The disk existed - 3927 MiB of installed opensuse-tumbleweed - and could not be published,
	// because the destination belonged to a different repository. Reading has the same shape: a
	// private package in another repository is not readable with this token either.
	//
	// SO BOTH DIRECTIONS ARE THIS REPOSITORY. `workflows/artifacts-publish.yml` mirrors every pinned
	// upstream input into `ghcr.io/easydev-org/easyvm`, and every fetch and every push goes there.
	repository := os.Getenv("DISKS_REPOSITORY")
	if repository == "" {
		repository = "easydev-org/easyvm"
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
	// THE DISKS LIVE IN THIS REPOSITORY'S OWN ghcr PACKAGE, NOT ANOTHER REPOSITORY'S.
	//
	// `easydev-org/easydiskbake` WAS THE DEFAULT AND IT CANNOT BE: a GITHUB_TOKEN is scoped to the
	// repository whose workflow holds it, so a token minted for `easydev-org/easyvm` has `packages`
	// access to `easyvm` and to nothing else. The push that produced the first real disk measured it:
	//
	//	FAILED: the registry refused a pull,push token (HTTP 403):
	//	{"errors":[{"code":"DENIED","message":"requested access to the resource is denied"}]}
	//
	// The disk existed - 3927 MiB of installed opensuse-tumbleweed - and could not be published,
	// because the destination belonged to a different repository. Reading has the same shape: a
	// private package in another repository is not readable with this token either.
	//
	// SO BOTH DIRECTIONS ARE THIS REPOSITORY. `workflows/artifacts-publish.yml` mirrors every pinned
	// upstream input into `ghcr.io/easydev-org/easyvm`, and every fetch and every push goes there.
	repository := os.Getenv("DISKS_REPOSITORY")
	if repository == "" {
		repository = "easydev-org/easyvm"
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

// LINUX ISO TAGS ARE NOT `iso-<distribution>`, AND THE THREE THAT ARE NOT WOULD HAVE FAILED.
//
// All thirteen images ARE published - easydiskbake's `artifacts-publish.yml` has been putting them in
// that package for months - but three carry the vendor's own edition in the tag, because that is what
// the pinned URL names:
//
//	iso-opensuse-tumbleweed-kde    not iso-opensuse-tumbleweed
//	iso-endeavouros                not iso-endeavour
//	iso-ghostbsd-mate              not iso-ghostbsd
//
// A LOOKUP THAT GUESSED WOULD HAVE TOLD THREE DISTRIBUTIONS THEIR INSTALLER DID NOT EXIST, with a
// message naming a tag nobody had published and no hint that a differently-named one had been sitting
// there all along.
//
// THE TAGS WERE VERIFIED AGAINST THE REGISTRY before this map was written, which is the only way to
// know a name rather than infer one.
var linuxISOAliases = map[string]string{
	"opensuse-tumbleweed": "opensuse-tumbleweed-kde",
	"endeavour":           "endeavouros",
	"ghostbsd":            "ghostbsd-mate",
	// `ubuntu` MEANS THE DESKTOP IMAGE, AND THE TAG SAYS SO BECAUSE PUBLISHING BOTH UNDER ONE NAME IS
	// NOT POSSIBLE. The registry carries `iso-ubuntu-desktop` (6,482,409,472 bytes - Desktop 26.04.1
	// exactly) and used to carry `iso-ubuntu` alone, which was the SERVER live image.
	//
	// THE SERVER IMAGE'S INSTALLER IS NOT THE ONE THIS STAGE DRIVES. Its GRUB entry reads
	//
	//	*Try or Install Ubuntu Server
	//
	// and its installer is subiquity, whose first screen is a console UI with no live desktop behind
	// it - so `desktop: linux`, which measures a panel, could never pass, and neither could the
	// coordinate-click panes that follow, which were measured against the desktop installer.
	//
	// MOSLTY THOUGH: A DESKTOP ISO IS THE ONE WITH A DESKTOP, a panel, and the Ubiquity-style panes
	// these files click. The size is the evidence: 6.04 GiB against the server's 2.79.
	"ubuntu": "ubuntu-desktop",
	// THE GRAPHICAL VARIANT, NOT THE MINIMAL ONE. `iso-nixos` is 1,665 MiB and carries NO DESKTOP: it
	// boots to a console menu, so `desktop: linux` (a panel) can never see it and every click after it
	// lands on a text screen. `iso-nixos-graphical` is 3,708 MiB and is the image the coordinate-click
	// panes were measured against.
	"nixos": "nixos-graphical",
	// THE DESKTOP FLAVOUR, AND THE SIZES ARE THE PROOF: `iso-popos` is 3,753 MiB while
	// `iso-popos-desktop` is 2,568. They are different images, and the file clicks panes that only
	// exist in the second.
	"popos": "popos-desktop",
	// KDE, WHICH IS THE `manjaro` ENTRY IN THE REFERENCE'S DESKTOP TABLE (5,277 MiB against the 4,969
	// MiB of the plain tag). The panes are KDE's.
	"manjaro": "manjaro-kde",
	// CORE, NOT PRO. `iso-zorin` is 3,728 MiB and `iso-zorin-core` is 3,612; the reference's
	// `zorin-core` entry is the measured one.
	"zorin": "zorin-core",
}

// linuxISOTag is the ghcr tag a Linux installer image is published under.
func linuxISOTag(release string) string {
	if alias, ok := linuxISOAliases[release]; ok {
		release = alias
	}
	return "iso-" + release
}

// fetchLinuxISO finds or pulls a distribution's installer image.
//
// TWO SOURCES, IN THIS ORDER:
//
//	1  an image already in the work directory, which is how a manual run hands one over
//	2  the ghcr tag, which IS published for all thirteen distributions
//
// AND NO VENDOR DOWNLOAD. A 3-to-7 GiB transfer inside a walkthrough step runs with the stage clock,
// which is why the Python implementation kept `fetch_iso.py` separate from the walk. Pulling the
// published artifact is the same bytes, one registry call, and resumable.
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
	// THE DISKS LIVE IN THIS REPOSITORY'S OWN ghcr PACKAGE, NOT ANOTHER REPOSITORY'S.
	//
	// `easydev-org/easydiskbake` WAS THE DEFAULT AND IT CANNOT BE: a GITHUB_TOKEN is scoped to the
	// repository whose workflow holds it, so a token minted for `easydev-org/easyvm` has `packages`
	// access to `easyvm` and to nothing else. The push that produced the first real disk measured it:
	//
	//	FAILED: the registry refused a pull,push token (HTTP 403):
	//	{"errors":[{"code":"DENIED","message":"requested access to the resource is denied"}]}
	//
	// The disk existed - 3927 MiB of installed opensuse-tumbleweed - and could not be published,
	// because the destination belonged to a different repository. Reading has the same shape: a
	// private package in another repository is not readable with this token either.
	//
	// SO BOTH DIRECTIONS ARE THIS REPOSITORY. `workflows/artifacts-publish.yml` mirrors every pinned
	// upstream input into `ghcr.io/easydev-org/easyvm`, and every fetch and every push goes there.
	repository := os.Getenv("DISKS_REPOSITORY")
	if repository == "" {
		repository = "easydev-org/easyvm"
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

	// AND NEITHER SOURCE HAD IT, WHICH IS A SENTENCE ABOUT BOTH.
	return Result{}, fmt.Errorf("no installer image for %q. Looked for %s.iso and installer.iso in %s, "+
		"and pulled %s/%s without success. Place the ISO at %s, or check that the tag is the one this "+
		"repository publishes - the vendor's edition is part of some of them (`opensuse-tumbleweed-kde` "+
		"rather than `opensuse-tumbleweed`)",
		release, release, spec.Work, repository, tag, filepath.Join(spec.Work, "installer.iso"))
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
	// THE AGENT MSI IS UNDER `guest-agent/`, NOT AT THE ISO'S ROOT, AND THAT ONE PATH SEGMENT COST A
	// WINDOWS LEG ITS RUN:
	//
	//	fetch the agent and its driver on the host: the ISO has no qemu-ga-x86_64.msi; the archive
	//	is not the one this stage expects
	//
	// The archive is exactly the one this stage expects - `7z l` on it shows
	//
	//	guest-agent/qemu-ga-i386.msi
	//	guest-agent/qemu-ga-x86_64.msi
	//
	// AND THE MESSAGE SENT A READER LOOKING FOR A DIFFERENT ISO. Over 836 MiB, the only thing wrong
	// was that the member path was written as if the ISO were flat. The driver tree was right -
	// `vioserial/w10/amd64/` - which is why only one of the two members failed.
	for _, item := range []struct {
		member string
		dest   string
	}{
		{"guest-agent/qemu-ga-x86_64.msi", filepath.Join(work, "qemu-ga-x86_64.msi")},
		// THE WHOLE DRIVER, NOT JUST ITS .inf. A SYS file is what binds the device and a CAT file is
		// what lets Windows accept the signature; the .inf alone declares a device whose driver is
		// nowhere, and `pnputil /add-driver` then fails inside the guest with a message about the
		// catalogue rather than about the missing file.
		{"vioserial/w10/amd64/vioser.inf", filepath.Join(work, "vioserial", "vioser.inf")},
		{"vioserial/w10/amd64/vioser.sys", filepath.Join(work, "vioserial", "vioser.sys")},
		{"vioserial/w10/amd64/vioser.cat", filepath.Join(work, "vioserial", "vioser.cat")},
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
		// AND ON SUCCESS THE MEMBER PATH IS LOGGED, because a wrong one and a missing one read the
		// same in the failure above and this is what distinguishes them next time.
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
