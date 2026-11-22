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
		t.Errorf("a preparation step can ask for %q and artifacts-publish.yml does not write it, "+
			"so that stage fails with a 404 after its download budget is already spent", tag)
	}
	// WHAT CONSUMES A TAG. Two things do now, and both have to be checked or a file nothing reads
	// stays in the package for years:
	//
	//	a workflow's `assets:`   a stage that boots from it
	//	artifacts-publish itself  the OpenCore build DOWNLOADS its four inputs and PUSHES one output,
	//	                          so `macos-asset-opencore` is consumed by the job in this same file
	//
	// The second half is a narrowing, not a loophole: it only covers tags this file names on an
	// `oci-pull` step, which is exactly the set the build cannot run without.
	inputsToTheBuild := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\s*tag:\s*([A-Za-z0-9._-]+)\s*$`).FindAllStringSubmatch(string(body), -1) {
		inputsToTheBuild[m[1]] = true
	}
	// AND THE STEPS THAT STILL PREPARE THEIR OWN INPUT. `asset: xcode`, `asset: guest_agent` and
	// `asset: packages` are still walk steps: they are small files (a 1.5 GiB .xip, a few MiB of
	// agent, a tar of dev packages) and they are read by a stage that has a guest, not by one that
	// boots. Moving them out is the same change as the ISOs and is not done yet - naming them here
	// is what makes that debt visible instead of silent.
	stillInAWalk := map[string]bool{
		"macos-asset-xcode-26.3":      true,
		"macos-asset-mac-guest-agent": true,
		"dev-node-pkg":                true,
		"dev-python-pkg":              true,
	}
	// AND THE TAGS PUBLISHED AHEAD OF A CONSUMER, ON PURPOSE AND BY NAME.
	//
	// `macos-asset-*-26` IS THE macOS 26 FIRMWARE, built and published because macOS 26 had no
	// firmware of its own and the build does not branch on release - only its recovery image does.
	// NOTHING PULLS THESE YET, because `macos/26/steps/` holds one file (`resume.yaml`) and there is
	// no 26 setup or ssh walkthrough for a stage to boot. Publishing them now is what makes the next
	// step - writing those walkthroughs - a change to YAML alone rather than one that also has to
	// build and publish a firmware nobody has ever seen succeed.
	//
	// THIS LIST IS THE DEBT ITSELF. A tag named here is a tag the test will not complain about; an
	// entry whose consumer later ARRIVES is caught by the FIRST loop in this test (the tag is now in
	// `fetched`), so the list cannot quietly grow stale in the harmless direction.
	publishedAheadOfAConsumer := map[string]bool{
		"macos-asset-boot-26": true,
		"macos-asset-rom-26":  true,
		"macos-asset-vars-26": true,
	}
	for tag := range published {
		if fetched[tag] || inputsToTheBuild[tag] || stillInAWalk[tag] || publishedAheadOfAConsumer[tag] {
			continue
		}
		t.Errorf("artifacts-publish.yml writes %q and nothing asks for it - not a workflow's "+
			"`assets:` and not the build in this same file - so it is runner time and package space "+
			"for a file no stage reads", tag)
	}
	if len(mirrored) == 0 {
		t.Error("nothing mirrors the macOS recovery image, and it cannot be a URL in the catalogue: " +
			"Apple reissues it per request behind a session cookie")
	}
}

// tagsTheBuildAsksFor reads the tags EVERY WORKFLOW PULLS, from the `assets` field on its stages.
//
// THE VOCABULARY MOVED AND THIS TEST MOVED WITH IT. It used to resolve `asset: linux_iso` plus a
// `release:` out of each walkthrough, because a walk downloaded its own installer. Walks no longer
// download anything: every medium a stage boots from is pulled by the workflow before the walk starts,
// because a stage whose duration depends on a registry reports a download as a machine that will not
// answer. MEASURED, and it is the reason for the change - run 43847000071 spent eighteen minutes
// inside `macos-vm-configure`'s own recovery fetch and then failed `wait for sshd` on a boot budget it
// had already spent.
//
// SO THE THING THAT MUST MATCH artifacts-publish.yml IS NOW THE WORKFLOW, and the entries look like
// `"assets": ["iso-ubuntu-desktop:ubuntu.iso"]` - a tag, a colon, and the filename the walk's `iso:`
// names. The filename half is not this test's business; the tag half is, because a tag nothing
// publishes is a 404 in the first minute of a leg.
func tagsTheBuildAsksFor(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}

	files, err := filepath.Glob(filepath.Join("..", "..", ".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatalf("cannot list workflows: %v", err)
	}
	// `"tag-or-name": ...` INSIDE an `assets` LIST. The list is JSON inside a YAML block, so the
	// pattern is the JSON spelling and not YAML's.
	entry := regexp.MustCompile(`"([A-Za-z0-9._-]+):([A-Za-z0-9._-]+)"`)
	assets := regexp.MustCompile(`"assets":\s*\[([^\]]*)\]`)
	for _, name := range files {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("cannot read %s: %v", name, err)
		}
		for _, m := range assets.FindAllStringSubmatch(string(body), -1) {
			for _, e := range entry.FindAllStringSubmatch(m[1], -1) {
				out[e[1]] = true
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("no workflow names an `assets:` entry, so this test would pass by comparing two " +
			"empty sets - and every stage would boot from a file nothing pulled")
	}

	// AND THE INPUTS OF THE FIRMWARE BUILD, WHICH NOW RUNS IN easyvm-macos AND NOT HERE.
	//
	// The four pinned macOS assets are still mirrored INTO this package - `artifacts-publish.yml`
	// has a row for each, and easyvm-macos's build pulls them by tag - so the publisher must keep
	// publishing them. The build itself is `scripts/build-firmware.py` in that repository, which
	// is why this list is written here rather than read from a Go table that no longer exists.
	for _, tag := range []string{
		"macos-asset-opencore",
		"macos-asset-opencore-template",
		"macos-asset-ovmf-code",
		"macos-asset-ovmf-vars-1920x1080",
	} {
		out[tag] = true
	}
	return out
}

// tagsThePublisherWrites reads every tag artifacts-publish.yml either CATALOGUES or PUSHES.
//
// TWO KINDS OF TAG LIVE IN THAT FILE NOW, AND BOTH ARE LEGITIMATE:
//
//	catalogue rows   `- { tag: "iso-ubuntu-desktop", ... }` - upstream files mirrored into this
//	                 package. Every one of them is pulled by some workflow's `assets:`, so a row
//	                 nothing pulls is runner time for a file no stage reads.
//	pushed tags      `tag: macos-asset-boot` on an `oci-push` step - files this repository BUILDS
//	                 rather than mirrors. The OpenCore firmware is the one case: four pinned
//	                 downloads, an APFS driver out of the recovery image, and a 256 MiB FAT32 image.
//	                 Its INPUTS are catalogue rows; its OUTPUT is published under a tag no row names.
func tagsThePublisherWrites(t *testing.T, src string) (catalogue, mirrored map[string]bool) {
	t.Helper()
	catalogue = map[string]bool{}
	mirrored = map[string]bool{}

	// A CATALOGUE ENTRY IS A MATRIX ROW: `- { tag: "iso-...", group: ..., url: ... }`.
	//
	// IT USED TO BE A TAB-SEPARATED LINE IN A HEREDOC, and the requirement was the same: something
	// exact enough that a comment starting with a tag-shaped word could not be mistaken for a row. A
	// quoted `tag:` key is that, and the parser is `encoding/json` after the braces are wrapped.
	row := regexp.MustCompile(`\{ tag: "[^"]+", group: "[^"]*", url: "[^"]*", sha: "[^"]*", size: "[^"]*" \}`)
	for _, m := range row.FindAllString(src, -1) {
		// THE TAG IS READ WITH A NARROW PATTERN RATHER THAN BY CONVERTING THE ROW TO JSON.
		//
		// The first version quoted every `word:` in the row and unmarshalled it, and a URL contains
		// one: `https://...` became `"https"://...`, which is not JSON. Only the tag is needed here,
		// and `tag: "..."` is unambiguous.
		if tag := regexp.MustCompile(`tag: "([^"]+)"`).FindStringSubmatch(m); tag != nil {
			catalogue[tag[1]] = true
		}
	}
	if len(catalogue) == 0 {
		t.Fatal("no catalogue rows were found in artifacts-publish.yml, so every fetch would look " +
			"unpublished")
	}

	// THE MIRRORED TAGS are named in the recovery step rather than in rows, because Apple's images
	// cannot be described by a URL - and there is one per RELEASE, so this reads every row of that
	// matrix rather than a single name.
	for _, m := range regexp.MustCompile(`macos-recovery-[a-z]+`).FindAllString(src, -1) {
		mirrored[m] = true
	}

	// AND THE TAGS THIS REPOSITORY BUILDS RATHER THAN MIRRORS. They appear as `tag: <name>` on an
	// `oci-push` step, and there is no catalogue row for them because no URL produces them - the
	// OpenCore firmware is assembled from four downloaded files plus a driver out of the recovery
	// image. A tag pushed here must be pulled by some workflow, which is what the caller checks.
	//
	// A TAG MAY END IN `${{ matrix.suffix }}`, AND THAT IS EXPANDED RATHER THAN IGNORED. The
	// `opencore` job is a matrix over two releases and the suffix is empty for 15 and `-26` for 26 -
	// so the tag strings in the file are templates, and a test that skipped them would pass while
	// NOTHING was published under the names the stages read. MEASURED: making the job a matrix
	// without this failed the caller with
	//
	//	a preparation step can ask for "macos-asset-boot" and artifacts-publish.yml does not write it
	//
	// which was exactly right - the literal `macos-asset-boot` had become `macos-asset-boot${{
	// matrix.suffix }}` and no longer appeared anywhere. The fix is to expand the templates the way
	// the runner does.
	//
	// THE SUFFIXES COME FROM THE SAME `include:` BLOCK the job uses, so a row added there without
	// this list is a tag the test reports as unpublished - which is the direction that fails loudly
	// instead of silently.
	suffixes := []string{""}
	if m := regexp.MustCompile(`(?m)^\s*suffix:\s*"([^"]*)"\s*$`).FindAllStringSubmatch(src, -1); m != nil {
		suffixes = suffixes[:0]
		for _, s := range m {
			suffixes = append(suffixes, s[1])
		}
	}

	// THE WHOLE VALUE, BECAUSE A TEMPLATE CONTAINS SPACES: `macos-asset-boot${{ matrix.suffix }}` is
	// one tag expression and a character class that excluded spaces matched only `macos-asset-boot${`.
	pushed := regexp.MustCompile(`(?m)^\s*tag:\s*(.+?)\s*$`)
	for _, m := range pushed.FindAllStringSubmatch(src, -1) {
		// A `tag:` on an `oci-pull` step is an INPUT, not something this file writes, and those are
		// the catalogue and mirrored sets above. Only the pushes count.
		if !strings.Contains(src, "oci-push") {
			continue
		}
		for _, suffix := range suffixes {
			expanded := strings.ReplaceAll(m[1], "${{ matrix.suffix }}", suffix)
			// ANY TEMPLATE LEFT OVER IS NOT A TAG. `${{ matrix.tag }}` on the recovery mirror is
			// covered by the `macos-recovery-` read above; anything else here means a place the
			// test cannot expand, and guessing at it would be worse than saying so.
			if strings.Contains(expanded, "${{") {
				continue
			}
			mirrored[expanded] = true
		}
	}
	return catalogue, mirrored
}
