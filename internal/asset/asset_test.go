package asset

import "testing"

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
