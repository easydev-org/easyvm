package asset

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/easydev-org/easyvm/internal/oci"
)

// --- ONE WAY IN: THIS REPOSITORY'S OWN ghcr PACKAGE --------------------------------
//
// EVERY HOST-SIDE INPUT A STAGE NEEDS ARRIVES THE SAME WAY. That is not tidiness - it is what makes
// the build reproducible and what makes a token work.
//
// THE TOKEN IS THE ARGUMENT THAT SETTLED IT. A `GITHUB_TOKEN` is scoped to the repository whose
// workflow holds it, so a stage running in `easydev-org/easyvm` can read `easydev-org/easyvm` and
// nothing else. The first install leg that finished a real disk measured what ignoring that costs:
//
//	FAILED: the registry refused a pull,push token (HTTP 403):
//	{"errors":[{"code":"DENIED","message":"requested access to the resource is denied"}]}
//
// REPRODUCIBILITY IS THE OTHER HALF. Three kinds used to come off the open internet at run time:
// Xcode from archive.org, the macOS guest agent from a GitHub release, and the developer tarballs
// from wherever their projects publish. Each was a live dependency on somebody else's uptime and
// somebody else's retention policy, in the middle of a stage that is trying to measure a guest. A
// tag in this repository's package is a thing the build owns.
//
// SO `workflows/artifacts-publish.yml` DOWNLOADS EACH ONE ONCE, VERIFIES IT, AND PUSHES IT, and every
// fetch below is a pull. A URL still appears in the code - in the pin tables - but only the publisher
// reads it.
//
// THE ORDER IS A BOOTSTRAP AND IT IS NOT OPTIONAL. An empty package is not a broken dispatch - it is
// an empty package, and every stage fails on the first `fetch:` while looking like fifteen unrelated
// failures. MEASURED ON THE FIRST RUN AFTER THIS SWITCHED REPOSITORIES, which dispatched all fifteen
// workflows at once and got:
//
//	15/15 failed, and the reason was one line per leg:
//	  zorin-install   fetch the installer image: ... pulled easydev-org/easyvm/iso-zorin-core without success
//	  zorin-ssh       FAILED: ghcr.io/easydev-org/easyvm:linux-zorin-ssh is not in the registry (404)
//
//	so:
//	  1. artifacts-publish   fills the source tags (ISOs, firmware, Xcode, packages)   ~40 GiB
//	  2. the `install` legs   produce the first disks, because their `pull:` is empty
//	  3. the `ssh`/`vm` legs  have a disk to pull, and so does anything after them
//
// STEP 2 IS WHY AN INSTALL HAS NO `pull:`: its first step creates the disk it writes to. Every other
// leg starts from a published one, so a package with no disks has nothing for them at all.

// packageTag names one artifact in this repository's package.
//
// THE PREFIXES ARE A CONVENTION, NOT DECORATION: `iso-` for an installer image, `macos-asset-` for a
// firmware or agent file, `macos-recovery-` for Apple's recovery image, `dev-` for a developer
// tarball. `artifacts-publish.yml` groups by the same prefixes, so a tag's name says which job
// publishes it.
func packageTag(prefix, name string) string { return prefix + name }

// disksRepository is where every artifact is read from and written to.
//
// IT IS ONE FUNCTION so there is one answer. `DISKS_REPOSITORY` overrides it, because a fork must be
// able to point at its own package rather than be told it may not run.
func disksRepository() string {
	if r := os.Getenv("DISKS_REPOSITORY"); r != "" {
		return strings.ToLower(r)
	}
	return "easydev-org/easyvm"
}

// pullTag puts one artifact from this repository's package at dest.
//
// `minBytes` IS THE CHEAP CHECK AND IT IS NOT OPTIONAL. A truncated pull is the failure that matters
// here - a proxy cuts the stream, the pull retries and gives up, and a half image then reaches a
// guest and fails somewhere that reads as a broken guest rather than a broken transfer. Every caller
// passes a floor below the real size rather than an exact one, so a republished artifact of a
// slightly different size still works.
func pullTag(tag, dest string, minBytes int64, timeout time.Duration, purpose string) error {
	art := oci.Artifact{
		Repository: disksRepository(),
		Tag:        tag,
		Token:      os.Getenv("GITHUB_TOKEN"),
	}
	ctx, cancel := oci.WithTimeout(timeout)
	defer cancel()
	if err := oci.Pull(ctx, art, dest, func(string) {}); err != nil {
		return fmt.Errorf("pulling %s from %s: %w. %s", tag, disksRepository(), err, purpose)
	}
	fi, err := os.Stat(dest)
	if err != nil {
		return err
	}
	if fi.Size() < minBytes {
		_ = os.Remove(dest)
		return fmt.Errorf("%s from %s is %d bytes, below the %d floor: the pull is truncated or "+
			"the tag holds something else", tag, disksRepository(), fi.Size(), minBytes)
	}
	return nil
}
