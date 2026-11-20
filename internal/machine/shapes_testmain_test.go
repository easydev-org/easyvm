package machine

import (
	"os"
	"testing"
)

// TestMain LOADS THE SHAPES THE BINARY SHIPS WITH, ONCE, FOR EVERY TEST IN THIS PACKAGE.
//
// IT IS NOT A CONVENIENCE. `Boot.shape()` resolves a name through the registry, so a package whose
// tests ran without loading one would have every builder return the zero shape: no firmware, no
// devices, no adapter - and the tests would pass by asserting that an empty machine is empty.
//
// AND IT PARSES EVERY EMBEDDED FILE ON EVERY RUN, which is how a malformed shape is caught by `go
// test` rather than by the first guest that tries to boot.
func TestMain(m *testing.M) {
	MustLoadEmbedded()
	os.Exit(m.Run())
}
