package asset

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// --- the macOS developer packages ------------------------------------------------------
//
// HOMEBREW CANNOT INSTALL ON THIS GUEST, AND THAT IS MEASURED RATHER THAN PREFERRED. Its installer
// refuses a non-arm64 macOS outright:
//
//	install.sh:  if [[ "${UNAME_MACHINE}" != "arm64" ]]; then abort; fi
//
// so `brew install node python` is not a command that can work here, whatever the network does. Each
// tool instead comes from the publisher that still ships an Intel build, as a .pkg, which the guest
// installs with its own `installer -pkg ... -target /`.
//
// TWO FILES, TWO PINS, AND BOTH HASHES WERE MEASURED RATHER THAN ASSUMED. A URL like
// `nodejs.org/dist/v26.13.0/node-v26.13.0.pkg` names one file and its bytes do not change - which is
// what made "the URL is the pin" an acceptable claim when this lived in Python. It is not acceptable
// from inside a stage: a macOS dev run that reaches nodejs.org and python.org at run time depends on
// two more servers being up, and the reference implementation's own note says the download happens on
// the host precisely because the guest's network cannot be trusted with it.
//
// SO THEY ARE TAGGED LIKE EVERY OTHER INPUT. `artifacts-publish.yml` downloads them once with the
// hashes below, and this file pulls `dev-node-pkg` and `dev-python-pkg`.

// devPackage is one .pkg the dev stage installs.
type devPackage struct {
	// Tag is what the artifact is published under in this repository's package.
	Tag string
	// File is the name written into the work directory, which is the name the walkthrough's `scp:`
	// step uses. THE TWO HAVE TO AGREE: `macos/15/steps/dev.yaml` copies `/tmp/node.pkg` inside the
	// guest from the host's `node.pkg`, so a different name here is a copy that names a file nothing
	// produced.
	File string
	// Version, URL and SHA256 are the pin. The URL is read only by `artifacts-publish.yml`; the hash
	// is checked there before the tag exists.
	Version string
	URL     string
	SHA256  string
	// MinBytes is the floor for the pull. A .pkg is tens of megabytes; a number this small only
	// catches a truncated transfer or an error page stored under the right tag.
	MinBytes int64
	// What says why this tool is here rather than in Homebrew.
	What string
}

// devPackages is the two the stage needs.
//
// THE VERSIONS ARE THE REFERENCE IMPLEMENTATION'S, because they are the ones measured to install on
// an Intel Sequoia guest. A newer Node would probably work; "probably" is not what a stage that has
// never passed should be built on.
var devPackages = []devPackage{
	{
		Tag:      "dev-node-pkg",
		File:     "node.pkg",
		Version:  "26.13.0",
		URL:      "https://nodejs.org/dist/v26.13.0/node-v26.13.0.pkg",
		SHA256:   "445b202997c959dddbd54ad8c82956371b495b03c73d56044a6f82dfb3249c46",
		MinBytes: 100 << 20,
		What:     "Node.js, the official x64 .pkg - links node, npm and npx into /usr/local/bin",
	},
	{
		Tag:      "dev-python-pkg",
		File:     "python.pkg",
		Version:  "3.13.9",
		URL:      "https://www.python.org/ftp/python/3.13.9/python-3.13.9-macos11.pkg",
		SHA256:   "dafebf62b8737f341f6b7eca2d6d1153a50a6b1bfbfa4b8aaf0e62adc73e5017",
		MinBytes: 60 << 20,
		What:     "Python, python.org's macos11 build - the one that still installs on Intel",
	},
}

// fetchDevPackages puts both .pkg files in the work directory.
//
// EACH ONE IS ITS OWN PULL, so a failure names WHICH package and the log is a list rather than a
// sentence. The stage copies both, so both have to be here - but a failure on the second should not
// hide that the first arrived.
func fetchDevPackages(spec Spec) (Result, error) {
	if err := os.MkdirAll(spec.Work, 0o755); err != nil {
		return Result{}, err
	}
	var main string
	var extra []string
	for _, p := range devPackages {
		dest := filepath.Join(spec.Work, p.File)
		// A PREVIOUS COPY OF THE RIGHT SIZE IS REUSED, which is what makes a resumed stage cheap.
		if fi, err := os.Stat(dest); err == nil && fi.Size() >= p.MinBytes {
			if main == "" {
				main = dest
			} else {
				extra = append(extra, dest)
			}
			continue
		}
		if err := pullTag(p.Tag, dest, p.MinBytes, 20*time.Minute,
			fmt.Sprintf("%s (%s). Homebrew refuses a non-arm64 macOS, so this is the publisher's "+
				"own Intel build", p.What, p.Version)); err != nil {
			return Result{}, err
		}
		if main == "" {
			main = dest
		} else {
			extra = append(extra, dest)
		}
	}
	if main == "" {
		return Result{}, fmt.Errorf("no developer package was produced, and the dev stage copies " +
			"both node.pkg and python.pkg into the guest")
	}
	return Result{Main: main, Extra: extra}, nil
}
