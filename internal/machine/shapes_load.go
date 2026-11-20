package machine

import (
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/easydev-org/easyvm/internal/machine/shapes"
)

// LoadEmbedded registers the shapes compiled into this binary.
//
// IT IS WHAT MAKES A CONTAINER DAEMON WORK. The image carries the binary and the walkthroughs, not a
// source tree, so a loader that read only from disk would start no guest at all.
//
// A SHAPE FILE THAT DOES NOT PARSE IS A BUILD ERROR, and it is reported as one: `go test` runs this,
// so a malformed shape is caught by the suite rather than by the first guest that tries to boot.
func LoadEmbedded() error {
	entries, err := fs.ReadDir(shapes.Files, ".")
	if err != nil {
		return fmt.Errorf("embedded shapes: %w", err)
	}
	var n int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		raw, err := fs.ReadFile(shapes.Files, path.Join(".", e.Name()))
		if err != nil {
			return fmt.Errorf("embedded shape %s: %w", e.Name(), err)
		}
		if err := defaultShapes.LoadBytes(e.Name(), raw); err != nil {
			return err
		}
		n++
	}
	if n == 0 {
		return fmt.Errorf("this build has no embedded shapes, so it could start no machine at all")
	}
	return nil
}

// MustLoadEmbedded is LoadEmbedded for package initialisation and tests.
func MustLoadEmbedded() {
	if err := LoadEmbedded(); err != nil {
		panic(err)
	}
}
