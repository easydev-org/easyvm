package oci

// WHERE THE WORKFLOWS THAT DECLARE STAGES LIVE.
//
// EVERY `ubuntu.yml`, `windows.yml`, `macos.yml` AND THE REST BELONGS TO THE REPOSITORY THAT RUNS IT.
// This repository used to carry a copy of all of them, and the copy is what made two files disagree -
// one caller granting `packages: write` while its twin did not, one chain with a fallback and its twin
// without. The five sub-repositories are the authority now, so the tests read them.
//
// THEY ARE FOUND, OR THE TEST SKIPS. A checkout without them has nothing to compare, and saying so is
// better than a green run over an empty set. EASYVM_STAGES overrides where they are looked for.

import (
	"os"
	"path/filepath"
	"strings"
)

// subRepositories is the names a staged checkout may use.
func subRepositories() []string {
	return []string{
		"easyvm-linux", "easyvm-windows", "easyvm-macos", "easyvm-bsd", "easyvm-android", "easyvm-microvm",
		"sd-linux", "sd-windows", "sd-macos", "sd-bsd", "sd-android", "sd-microvm",
	}
}

// workflowFiles is every workflow the chain tests look at: this repository's own, then each
// sub-repository's.
//
// THIS REPOSITORY'S FILES ARE STILL INCLUDED because `stage.yml`, `image.yml`, `sandbox.yml` and
// `artifacts-publish.yml` live here and the tests check things about them too - what is gone is the
// seventeen per-OS callers, which have no meaning without the stages they drive.
func workflowFiles() []string {
	var files []string
	if own, err := filepath.Glob(filepath.Join("..", "..", ".github", "workflows", "*.yml")); err == nil {
		files = append(files, own...)
	}
	for _, base := range stageBases() {
		for _, repo := range subRepositories() {
			m, _ := filepath.Glob(filepath.Join(base, repo, ".github", "workflows", "*.yml"))
			files = append(files, m...)
		}
	}
	return files
}

// stageBases is where a sibling checkout may be, from this package's directory.
func stageBases() []string {
	if env := os.Getenv("EASYVM_STAGES"); env != "" {
		var out []string
		for _, one := range strings.Split(env, string(os.PathListSeparator)) {
			if one != "" {
				out = append(out, one)
			}
		}
		return out
	}
	// `internal/oci` -> the repository root is two levels up, its siblings three.
	return []string{"../..", "..", "../../.."}
}
