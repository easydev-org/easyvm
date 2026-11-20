// Package shapes holds the machine shapes this build ships with.
//
// THEY ARE EMBEDDED SO A BINARY IS SELF-CONTAINED: `easyvm` in a container has no source tree beside
// it, and a daemon that could not start a guest until somebody mounted a directory would be a daemon
// that starts no guests. `--shapes` replaces them, and the files here are what a reader opens to see
// what this build can start.
package shapes

import "embed"

// Files is every yaml in this directory.
//
//go:embed *.yaml
var Files embed.FS
