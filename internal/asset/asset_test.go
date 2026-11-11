package asset

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// THREE OF THIRTEEN LINUX IMAGES ARE PUBLISHED UNDER A VENDOR'S EDITION NAME, AND A GUESS WOULD HAVE
// TOLD THOSE THREE THAT THEIR INSTALLER DID NOT EXIST.
//
// Every image IS in the package - easydiskbake's artifacts-publish.yml has been filling it for months -
// but the tag carries what the pinned URL names:
//
//	iso-opensuse-tumbleweed-kde    not iso-opensuse-tumbleweed
//	iso-endeavouros                not iso-endeavour
//	iso-ghostbsd-mate              not iso-ghostbsd
//
// THESE WERE VERIFIED AGAINST THE REGISTRY, which is the only way to know a name rather than infer one.
// The test pins them so a rename in the packaging is a failure here rather than a 404 on a leg.
func TestTheLinuxISOTagsThatAreNotTheDistributionName(t *testing.T) {
	cases := map[string]string{
		"ubuntu":              "iso-ubuntu",
		"kubuntu":             "iso-kubuntu",
		"fedora-workstation":  "iso-fedora-workstation",
		"mint":                "iso-mint",
		"cachyos":             "iso-cachyos",
		"manjaro":             "iso-manjaro",
		"zorin":               "iso-zorin",
		"mx":                  "iso-mx",
		"popos":               "iso-popos",
		"nixos":               "iso-nixos",
		"opensuse-tumbleweed": "iso-opensuse-tumbleweed-kde",
		"endeavour":           "iso-endeavouros",
		"ghostbsd":            "iso-ghostbsd-mate",
	}
	for release, want := range cases {
		if got := linuxISOTag(release); got != want {
			t.Errorf("linuxISOTag(%q) = %q, want %q", release, got, want)
		}
	}
	// AND A NAME NOT IN THE TABLE IS USED AS ITSELF, so a fourteenth distribution needs no map entry.
	if got := linuxISOTag("some-new-distro"); got != "iso-some-new-distro" {
		t.Fatalf("an unmapped release should be `iso-<release>`; got %q", got)
	}
}

// XCODE CANNOT COME FROM APPLE, AND THE PIN IS APPLE'S OWN NUMBER REPUBLISHED.
//
// The developer portal answers an unauthenticated request with a redirect to /unauthorized, so this
// repository takes archive.org's copy of Apple's .xip and verifies it against the sha1 Apple's release
// index publishes. If the two disagree the download is refused, which is the only thing that turns
// "a mirror" into "Apple's file".
//
// THE VERSION IS THE TOP OF A RANGE, NOT "LATEST": Apple's installer enforces a minimum macOS, so
// Xcode 26.4 refuses to install on a macOS 15 disk after a 2.7 GiB transfer.
func TestTheXcodePinIsTheOneApplePublishes(t *testing.T) {
	pin, ok := xcodePins["15"]
	if !ok {
		t.Fatal("no Xcode pin for macOS 15, which the macOS dev stage needs")
	}
	// APPLE'S RELEASE INDEX AND ARCHIVE.ORG AGREE ON THIS NUMBER; a change here is a change of file.
	if pin.SHA1 != "cebf05ec2920111830af5e08a45f07961e36bfd6" {
		t.Fatalf("the macOS 15 pin is sha1 %s, and Apple's index says "+
			"cebf05ec2920111830af5e08a45f07961e36bfd6", pin.SHA1)
	}
	if pin.Version != "26.3" {
		t.Fatalf("the macOS 15 pin is Xcode %s; 26.3 is the LAST release that runs on Sequoia "+
			"(26.4 requires macOS 26.2+)", pin.Version)
	}
	if pin.Bytes < 2000<<20 {
		t.Fatalf("the pinned size is %d bytes, which is not a full .xip", pin.Bytes)
	}
	// AND THE URL IS THE FILE THE HASH DESCRIBES, not a listing page.
	if !strings.HasSuffix(pin.URL, pin.File) {
		t.Fatalf("the pin's URL %q does not end in its file %q", pin.URL, pin.File)
	}
}

// AN UNKNOWN RELEASE IS REFUSED WITH THE ONES THAT ARE KNOWN, rather than defaulting to a version
// that would fail inside the guest after the transfer.
func TestAnUnknownXcodeReleaseIsRefused(t *testing.T) {
	_, err := fetchXcode(Spec{Kind: Xcode, Release: "26", Work: t.TempDir()})
	if err == nil {
		t.Fatal("a release with no pin must be refused")
	}
	if !strings.Contains(err.Error(), "15") {
		t.Fatalf("the refusal should name the releases this build knows; got %v", err)
	}
}

// THE AGENT'S MSI IS UNDER `guest-agent/`, AND WRITING IT AS IF THE ISO WERE FLAT COST A WINDOWS LEG.
//
// `7z l` ON THE REAL 836 MiB IMAGE:
//
//	guest-agent/qemu-ga-i386.msi
//	guest-agent/qemu-ga-x86_64.msi
//
// The extraction named `qemu-ga-x86_64.msi` alone and reported
//
//	fetch the agent and its driver on the host: the ISO has no qemu-ga-x86_64.msi; the archive is
//	not the one this stage expects
//
// WHICH SENT A READER LOOKING FOR A DIFFERENT ISO. The archive was exactly the right one; the member
// path was written as if the ISO had no directories. The driver tree was correct -
// `vioserial/w10/amd64/` - which is why only one of the two members failed.
func TestTheAgentMSIIsUnderGuestAgent(t *testing.T) {
	iso := os.Getenv("VIRTIO_ISO")
	if iso == "" {
		iso = "/tmp/vio/virtio-win.iso"
	}
	if _, err := os.Stat(iso); err != nil {
		t.Skipf("no virtio-win ISO at %s (set VIRTIO_ISO)", iso)
	}
	// THE SIZE IS CHECKED FIRST, because fetchWindowsAgent refuses a small file and a stub in its
	// place would make this test pass without testing anything.
	if fi, err := os.Stat(iso); err != nil || fi.Size() < 100<<20 {
		t.Skipf("%s is not the full image", iso)
	}

	work := t.TempDir()
	// THE ISO IS READ WHERE THE STEP EXPECTS IT, without copying 836 MiB.
	if err := os.Symlink(iso, filepath.Join(work, "virtio-win.iso")); err != nil {
		t.Skipf("cannot link the ISO into a temp dir: %v", err)
	}
	res, err := fetchWindowsAgent(work)
	if err != nil {
		t.Fatalf("fetching the agent from the real image: %v", err)
	}
	if !strings.HasSuffix(res.Main, "qemu-ga-x86_64.msi") {
		t.Fatalf("main is %q, not the agent MSI", res.Main)
	}
	fi, err := os.Stat(res.Main)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() < 5<<20 {
		t.Fatalf("the agent MSI is %d bytes, which is not a 11 MB MSI", fi.Size())
	}
	// AND THE WHOLE DRIVER CAME OUT, WHICH IS THREE FILES AND NOT ONE.
	//
	// `vioser.inf` DECLARES THE DEVICE; `vioser.sys` IS THE DRIVER AND `vioser.cat` IS WHAT LETS
	// WINDOWS ACCEPT ITS SIGNATURE. Extracting only the .inf left `pnputil /add-driver` failing inside
	// the guest about a catalogue, which reads as a signature problem rather than a missing file.
	// Measured on windows-vm-configure step 6 of 20: `no file to copy at vioserial/vioser.sys`.
	if len(res.Extra) != 3 {
		t.Fatalf("the vioserial driver is %d file(s); it is inf, sys and cat", len(res.Extra))
	}
	for _, want := range []string{"vioser.inf", "vioser.sys", "vioser.cat"} {
		found := false
		for _, p := range res.Extra {
			if filepath.Base(p) == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s was not extracted; the driver is incomplete and the guest cannot bind the "+
				"device (got %v)", want, res.Extra)
		}
	}
}

// APPLE'S OWN GUEST AGENT DOES NOT LOAD UNDER QEMU, SO THE macOS AGENT IS A DIFFERENT BINARY.
//
// `fetch: guest_agent` with `family: macos` used to answer
//
//	fetching the macOS guest agent is not implemented yet; the step that needs it is "the next scp"
//
// and the step after it copied `guest-agent/mac-guest-agent` - a path that existed nowhere, because the
// name was the ISO's shape for Windows applied to a macOS download. `macos-vm-configure` stops at that
// copy, and `shutdown: guest_agent` on a Mac has nothing behind it.
//
// mav2287/mac-guest-agent is a native macOS binary speaking the same protocol over the ISA serial
// chardev the machine model gives a Mac - which is why `qga.go` has that device branch at all.
func TestTheMacGuestAgentIsImplemented(t *testing.T) {
	body, err := os.ReadFile("asset.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	i := strings.Index(src, "case GuestAgentMacOS:")
	if i < 0 {
		t.Fatal("no GuestAgentMacOS case")
	}
	rest := src[i:]
	if end := strings.Index(rest[1:], "\n	case "); end > 0 {
		rest = rest[:end+1]
	}
	if strings.Contains(rest, "not implemented") {
		t.Fatal("`fetch: guest_agent` for macOS is still unimplemented, so a macOS vm-configure " +
			"stops at the copy that needs it")
	}
	if !strings.Contains(rest, "fetchMacGuestAgent") {
		t.Fatal("the macOS case does not reach its own fetcher")
	}

	// AND THE FETCHER MAKES IT EXECUTABLE, because a copy into the guest without the bit fails there
	// with a permission error that reads as a broken download.
	agent, err := os.ReadFile("macagent.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(agent), "0o755") {
		t.Fatal("the macOS agent is not chmod 0755, so the copy into the guest is not executable")
	}
	// AND A TRUNCATED DOWNLOAD IS REFUSED. No hash is published for this release, so size is the only
	// integrity check available - and a truncated binary fails at launch, inside the guest.
	if !strings.Contains(string(agent), "macGuestAgentMinBytes") {
		t.Fatal("the macOS agent's download has no size floor, so a truncated asset is copied in")
	}
}
