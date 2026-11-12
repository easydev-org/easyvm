package asset

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// THE PUBLISHER AND THE FETCHER MUST NAME THE SAME TAGS, AND NOTHING ELSE CHECKS BOTH ENDS.
//
// A tag this build asks for and the publisher never writes is a 404 in the middle of a stage that has
// already downloaded six gigabytes. A tag the publisher writes and nothing fetches is hours of runner
// time and a package full of files no stage reads. Both are silent: the Go build compiles, the tests
// pass, and the failure arrives as `pulling iso-something: ... 404` on a leg.
//
// THIS TEST WAS WRITTEN BECAUSE BOTH HAPPENED. The catalogue named `iso-kubuntu-desktop`,
// `iso-mint-cinnamon`, `iso-mx-xfce` and `iso-cachyos-desktop` while `linuxISOAliases` resolved
// `kubuntu`, `mint`, `mx` and `cachyos` to `iso-kubuntu`, `iso-mint`, `iso-mx` and `iso-cachyos` -
// names no publisher has ever written. The alias table and the catalogue were each internally
// consistent and disagreed with each other, which is exactly the gap a per-file test cannot see.
func TestEveryFetchedTagIsPublishedAndEveryPublishedTagIsFetched(t *testing.T) {
	fetched := tagsTheBuildAsksFor(t)

	body, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "artifacts-publish.yml"))
	if err != nil {
		t.Fatalf("cannot read artifacts-publish.yml: %v", err)
	}
	published, mirrored := tagsThePublisherWrites(t, string(body))

	for tag := range fetched {
		if published[tag] || mirrored[tag] {
			continue
		}
		t.Errorf("`fetch:` can ask for %q and artifacts-publish.yml does not write it, so that "+
			"stage fails with a 404 after its download budget is already spent", tag)
	}
	for tag := range published {
		if fetched[tag] {
			continue
		}
		t.Errorf("artifacts-publish.yml writes %q and no `fetch:` step asks for it, so publishing "+
			"it is runner time and package space for a file no stage reads", tag)
	}
	if len(mirrored) == 0 {
		t.Error("nothing mirrors the macOS recovery image, and it cannot be a URL in the catalogue: " +
			"Apple reissues it per request behind a session cookie")
	}
}

// tagsTheBuildAsksFor resolves every `fetch:` in every walkthrough through the alias table.
func tagsTheBuildAsksFor(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}

	// THE LINUX AND BSD INSTALLER IMAGES, by the release each file names.
	files, err := filepath.Glob(filepath.Join("..", "..", "*", "*", "steps", "*.yaml"))
	if err != nil {
		t.Fatalf("cannot list walkthroughs: %v", err)
	}
	release := regexp.MustCompile(`(?m)^\s*release:\s*"?([A-Za-z0-9._-]+)"?\s*$`)
	for _, name := range files {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("cannot read %s: %v", name, err)
		}
		src := string(body)
		if !strings.Contains(src, "fetch: linux_iso") {
			continue
		}
		for _, m := range release.FindAllStringSubmatch(src, -1) {
			out[linuxISOTag(m[1])] = true
		}
	}
	// A FILE THAT NAMES `fetch: linux_iso` MUST NAME A RELEASE, and if none was found the loop above
	// silently contributes nothing - so one is asserted rather than assumed.
	if len(out) == 0 {
		t.Fatal("no linux_iso release could be resolved from any walkthrough, so this test would " +
			"pass by comparing two empty sets")
	}

	// THE FIXED TAGS, stated once. These are the kinds whose tag does not depend on a `release:`.
	for _, tag := range []string{
		virtioWinTag,
		windowsISOTag,
		"macos-asset-opencore",
		"macos-asset-opencore-template",
		"macos-asset-ovmf-code",
		"macos-asset-ovmf-vars-1920x1080",
	} {
		out[tag] = true
	}
	// FROM THE PINS, not from a second list: a tag derived from the pin is a tag that cannot drift.
	for _, pin := range xcodePins {
		out[packageTag("macos-asset-", "xcode-"+pin.Version)] = true
	}
	out[packageTag("macos-asset-", "mac-guest-agent")] = true
	for _, p := range devPackages {
		out[p.Tag] = true
	}
	out[recoveryMacOSTag("15")] = true
	return out
}

// tagsThePublisherWrites reads the catalogue rows and the mirrored tags out of the workflow.
func tagsThePublisherWrites(t *testing.T, src string) (catalogue, mirrored map[string]bool) {
	t.Helper()
	catalogue = map[string]bool{}
	mirrored = map[string]bool{}

	// A CATALOGUE ROW IS `tag<TAB>group<TAB>url<TAB>sha256<TAB>bytes`. Requiring the tab is what tells
	// a row from a comment that happens to start with a tag-shaped word.
	for _, line := range strings.Split(src, "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "\t") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 5 {
			continue
		}
		catalogue[fields[0]] = true
	}
	if len(catalogue) == 0 {
		t.Fatal("no catalogue rows were found in artifacts-publish.yml, so every fetch would look " +
			"unpublished")
	}

	// THE MIRRORED TAG is named in the recovery step rather than in a row, because Apple's image
	// cannot be described by a URL.
	for _, m := range regexp.MustCompile(`macos-recovery-[a-z]+`).FindAllString(src, -1) {
		mirrored[m] = true
	}
	return catalogue, mirrored
}
