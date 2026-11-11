package main

import (
	"os"
	"strings"
	"testing"
)

// EVERY TOOL THE BUILD ASSERTS MUST BE IN AN INSTALL LINE.
//
// SWTPM WAS NOT, and the build found it:
//
//	#10 0.114 + swtpm --version
//	#10 0.114 /bin/sh: 1: swtpm: not found
//	ERROR: ... did not complete successfully: exit code: 127
//
// It had been described in the comment at the top of the file and asserted at the bottom, which is
// exactly the pair that makes a reader believe it is installed. The rewrite had dropped it from the
// apt list. Asserting a tool is not installing it, and installing it is not asserting it - both have
// to be true, and nothing was comparing the two lists.
func TestAssertedToolsAreInstalled(t *testing.T) {
	installs, asserts := splitInstallAndAssert(t)

	// THE COMMANDS THE ASSERTION BLOCK RUNS must each come from a package that is installed.
	// A handful of commands are provided by the base image or by a dependency and are listed here
	// rather than guessed at.
	from := map[string]string{
		"qemu-system-x86": "qemu-system-x86",
		"qemu-img":        "qemu-utils",
		"ovmf":            "ovmf",
		"swtpm":           "swtpm",
		"smbd":            "samba-common-bin",
		"dnsmasq":         "dnsmasq",
		"ip":              "iproute2",
		"iptables":        "iptables",
		"sysctl":          "procps",
		"ffmpeg":          "ffmpeg",
		"tesseract":       "tesseract-ocr",
		"debugfs":         "e2fsprogs",
		"xorriso":         "xorriso",
		"genisoimage":     "genisoimage",
		"mformat":         "mtools",
		"7z":              "p7zip-full",
		"aria2c":          "aria2",
		"unzstd":          "zstd",
		"wimlib-imagex":   "wimtools",
	}

	for tool, pkg := range from {
		if !strings.Contains(asserts, tool) {
			continue // not asserted; nothing to compare
		}
		if !strings.Contains(installs, pkg) {
			t.Errorf("the build asserts %q but package %q is not in any install line. "+
				"Asserting a tool does not install it - that is how swtpm was missed", tool, pkg)
		}
	}

}

// splitInstallAndAssert returns the text of every line that INSTALLS a package and every line that
// ASSERTS a binary, so the two lists can be compared.
//
// IT DOES NOT PARSE RUN BLOCKS. A line-continued RUN puts the command on one line and the package
// names on the next dozen, so "which packages does this file install" is answered by collecting
// every line that is not a comment and is not the assertion block - the apt lists are the only
// place a package name can legally appear.
func splitInstallAndAssert(t *testing.T) (installs, asserts string) {
	t.Helper()
	body, err := os.ReadFile("../../Containerfile")
	if err != nil {
		t.Fatal(err)
	}

	var install, assert strings.Builder
	inAssert := false
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		// PROSE IS NOT A PACKAGE LIST. The header explains every package at length, and a grep over
		// the whole file would find `swtpm` there and call it installed. That mistake has been made
		// twice already in this repository's checks.
		if strings.HasPrefix(trimmed, "#") || trimmed == "" {
			continue
		}
		// THE ASSERTION RUN STARTS at the first --version and runs to its closing backslash-less
		// line; everything before it is installation.
		if strings.Contains(line, "--version") || strings.Contains(line, "command -v") {
			inAssert = true
		}
		if inAssert {
			assert.WriteString(line)
			assert.WriteString("\n")
			continue
		}
		install.WriteString(line)
		install.WriteString("\n")
	}
	return install.String(), assert.String()
}
