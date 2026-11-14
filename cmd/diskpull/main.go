// Command diskpull fetches published disks from the registry into a directory.
//
// IT IS A THIN MAIN AROUND `internal/oci`, WHICH ALREADY DOES THE HARD PART: one HTTP request per
// chunk with `Range:` on the blob, an append to `<dest>.part`, and four attempts that CONTINUE the
// transfer rather than restarting it. `oras pull` has none of that - a 14 GiB transfer through a
// container proxy gets reset, and oras starts again from byte zero.
//
//	diskpull -dest /disks -repository easydev-org/easyvm macos15:macos15.qcow2 ...
//
// Each argument is `tag:filename`. The filename is what the disk is written as, because the layer's
// own title is `macos15.qcow2` for one artifact and `disk.qcow2` for another and a caller who wants
// them side by side has to name them.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/easydev-org/easyvm/internal/oci"
)

func main() {
	dest := flag.String("dest", "", "directory to write the disks into")
	repository := flag.String("repository", "easydev-org/easyvm", "registry repository")
	registry := flag.String("registry", "ghcr.io", "registry host")
	skipExisting := flag.Bool("skip-existing", false,
		"leave a disk that is already there and looks complete")
	flag.Parse()

	if *dest == "" || flag.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "usage: diskpull -dest DIR tag:filename[:bytes] ...\n")
		os.Exit(2)
	}
	if err := os.MkdirAll(*dest, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "FAILED: %v\n", err)
		os.Exit(1)
	}

	failed := 0
	for _, spec := range flag.Args() {
		parts := strings.SplitN(spec, ":", 2)
		if len(parts) != 2 {
			fmt.Fprintf(os.Stderr, "FAILED: %q is not tag:filename\n", spec)
			failed++
			continue
		}
		tag, name := parts[0], parts[1]
		path := filepath.Join(*dest, name)

		if *skipExisting {
			if fi, err := os.Stat(path); err == nil && fi.Size() > 512<<20 {
				fmt.Printf("[diskpull] %s is already here (%d MiB), skipping\n", name, fi.Size()>>20)
				continue
			}
		}

		// THE TOKEN IS OPTIONAL AND READ FROM THE ENVIRONMENT, so a private package works without
		// this program ever holding a credential of its own. ghcr answers 401 for a private
		// artifact and the error says so rather than looking like a network failure.
		a := oci.Artifact{
			Registry:   *registry,
			Repository: *repository,
			Tag:        tag,
			Token:      os.Getenv("GHCR_TOKEN"),
		}
		fmt.Printf("[diskpull] %s -> %s\n", a.Repository+":"+tag, path)
		start := time.Now()
		if err := oci.Pull(context.Background(), a, path, func(msg string) {
			fmt.Println("[diskpull]  " + msg)
		}); err != nil {
			fmt.Fprintf(os.Stderr, "FAILED %s: %v\n", tag, err)
			failed++
			continue
		}
		if fi, err := os.Stat(path); err == nil {
			fmt.Printf("[diskpull] %s: %d MiB in %s\n", name, fi.Size()>>20,
				time.Since(start).Round(time.Second))
		}
	}
	if failed > 0 {
		fmt.Fprintf(os.Stderr, "FAILED: %d of %d disk(s)\n", failed, flag.NArg())
		os.Exit(1)
	}
}
