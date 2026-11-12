package asset

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// --- Xcode, the one Apple asset that cannot come from Apple ---------------------------
//
// `brew install --cask xcode` DOWNLOADS FROM APPLE'S DEVELOPER PORTAL, AND THE PORTAL ANSWERS AN
// UNAUTHENTICATED REQUEST WITH A REDIRECT:
//
//	$ curl -I https://download.developer.apple.com/.../Xcode_26.3_Universal.xip
//	HTTP/2 302
//	location: https://developer.apple.com/unauthorized/
//
// So an Apple ID is required, and this repository has no credentials it is willing to carry for that:
// a CI job that needs a person's Apple password and a 2FA code is a job that cannot run unattended.
//
// ARCHIVE.ORG KEEPS A COPY OF APPLE'S OWN .xip, BYTE FOR BYTE, and records a sha1 per file. That
// number is the same one Apple's release index publishes, so it is an IDENTITY CHECK rather than a
// promise - and it is the only thing that turns "a mirror" into "Apple's file".

// xcodePin is one release's Xcode.
type xcodePin struct {
	// Version and Build are what a person says when they mean a particular Xcode.
	Version string
	Build   string
	// File is the archive's own name for it.
	File string
	// URL is where archive.org keeps it.
	URL string
	// SHA1 is Apple's own hash of that file, republished by archive.org.
	SHA1 string
	// Bytes is the exact size, which is a cheap first check before hashing 2.7 GiB.
	Bytes int64
	// Why records which disk this version belongs to, because the choice is not "latest".
	Why string
}

// xcodePins are the releases this build knows.
//
// THE VERSION IS THE TOP OF A RANGE, NOT "LATEST". Apple raises Xcode's minimum macOS with almost
// every major release and the installer enforces it as a hard limit:
//
//	Xcode 26.3   requires macOS 15.6+   <- the last release that runs on Sequoia
//	Xcode 26.4   requires macOS 26.2+
//
// So a macOS 15 disk tops out at 26.3, and `release:` selects a version instead of the file naming
// one - the number is a fact about the disk being prepared.
var xcodePins = map[string]xcodePin{
	"15": {
		Version: "26.3",
		Build:   "17C529",
		File:    "Xcode_26.3_Universal.xip",
		URL:     "https://archive.org/download/xcode-26.3/Xcode_26.3_Universal.xip",
		SHA1:    "cebf05ec2920111830af5e08a45f07961e36bfd6",
		Bytes:   2871807039, // 2738 MiB, read from archive.org metadata
		Why:     "the last Xcode that installs on macOS 15 (it requires 15.6+)",
	},
}

// fetchXcode downloads the pinned .xip for a release and verifies its sha1.
//
// IT RESUMES. The file is 2.7 GiB and a container proxy cuts streams - the same failure this
// package's OCI pull already retries - so a partial file is kept and the next attempt continues
// from its size rather than starting over.
func fetchXcode(spec Spec) (Result, error) {
	release := spec.Release
	if release == "" {
		release = "15"
	}
	pin, ok := xcodePins[release]
	if !ok {
		known := make([]string, 0, len(xcodePins))
		for k := range xcodePins {
			known = append(known, k)
		}
		return Result{}, fmt.Errorf("no Xcode pin for macOS %s; this build knows %s. A version is a "+
			"fact about the DISK - Apple's installer enforces a minimum macOS, so guessing one "+
			"produces an Xcode that refuses to install after a 2.7 GiB transfer",
			release, strings.Join(known, ", "))
	}
	if err := os.MkdirAll(spec.Work, 0o755); err != nil {
		return Result{}, err
	}
	dest := filepath.Join(spec.Work, pin.File)

	// A COMPLETE, CORRECT FILE IS REUSED WITHOUT HASHING 2.7 GiB EVERY RUN - the size is compared
	// first, and the hash only when it matches.
	if fi, err := os.Stat(dest); err == nil && fi.Size() == pin.Bytes {
		if got, err := sha1File(dest); err == nil && got == pin.SHA1 {
			return Result{Main: dest}, nil
		}
		// A file of the right size with the wrong hash is a corrupted transfer, and it must not be
		// resumed onto.
		_ = os.Remove(dest)
	}

	// FROM THIS REPOSITORY'S PACKAGE, NOT FROM Archive.org AT RUN TIME.
	//
	// THE ARCHIVE.ORG URL IS STILL IN THE PIN because that is what `artifacts-publish.yml`
	// downloads from - it is the one place the open internet is touched, once, by a job whose whole
	// subject is verifying it. A stage pulls the tag, so a 2.7 GiB transfer off somebody else's
	// server is no longer in the middle of a macOS bake, and a re-run is not exposed to archive.org
	// being slow, rate-limited or gone.
	//
	// THE SHA1 IS STILL CHECKED, and it is the same number: the publisher verified it before the
	// tag existed, and checking it here means a tag that was overwritten by something else is
	// refused rather than installed.
	if err := pullTag(packageTag("macos-asset-", "xcode-"+pin.Version), dest, pin.Bytes*9/10,
		40*time.Minute, "It is Apple's own .xip, mirrored into this repository's package because "+
			"the developer portal requires an Apple ID and a 2FA code"); err != nil {
		return Result{}, err
	}

	fi, err := os.Stat(dest)
	if err != nil {
		return Result{}, err
	}
	if fi.Size() != pin.Bytes {
		return Result{}, fmt.Errorf("%s is %d bytes and the pin says %d", dest, fi.Size(), pin.Bytes)
	}
	got, err := sha1File(dest)
	if err != nil {
		return Result{}, err
	}
	if got != pin.SHA1 {
		_ = os.Remove(dest)
		return Result{}, fmt.Errorf("%s: sha1 %s but the pin says %s. Apple's release index and "+
			"archive.org agree on that number, so a mismatch means the file is not the one either "+
			"of them published", pin.File, got, pin.SHA1)
	}
	return Result{Main: dest}, nil
}

// downloadResumable fetches a URL to dest, continuing a partial file.
//
// THE PART FILE IS THE RESUME, the same rule the OCI pull uses: a truncated file with the FINAL name
// is one a later step would install.
func downloadResumable(url, dest string, want int64, timeout time.Duration) error {
	part := dest + ".part"
	var offset int64
	if fi, err := os.Stat(part); err == nil {
		offset = fi.Size()
		if offset > want {
			// A file larger than the artifact is not a resume, it is a different file.
			_ = os.Remove(part)
			offset = 0
		}
	}
	if offset == want && want > 0 {
		return os.Rename(part, dest)
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if offset > 0 && resp.StatusCode != http.StatusPartialContent {
		// THE SERVER IGNORED THE RANGE, so the only correct move is to start again rather than
		// append a whole body to a partial file.
		_ = os.Remove(part)
		return fmt.Errorf("the server answered %d to a range request; a resume is not possible, "+
			"and the %d bytes already here will be discarded on the next attempt", resp.StatusCode, offset)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("copying (%d of %d bytes are on disk and will be resumed): %w",
			offset, want, err)
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return os.Rename(part, dest)
}

func sha1File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
