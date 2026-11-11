package asset

import (
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
