package oci

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// A TRANSPORT FAILURE IS RETRIED, A MISSING ARTIFACT IS NOT.
//
// A 20 GiB blob over a container proxy gets its stream cut. Measured on run 43050175134's macos-dev
// leg, which pulled for ten minutes and then reported:
//
//	copying the blob: stream error: stream ID 1; PROTOCOL_ERROR; received from peer
//
// The file was left as disk.qcow2.part, so a second attempt continues from where the first stopped -
// which is the whole value of retrying. Retrying a 404 would spend minutes learning the same fact.
func TestRetryableTellsTransportFromFact(t *testing.T) {
	retry := []string{
		"copying the blob: stream error: stream ID 1; PROTOCOL_ERROR; received from peer",
		"copying the blob: unexpected EOF",
		"copying the blob: read tcp: connection reset by peer",
		"copying the blob: net/http: TLS handshake timeout",
		"copying the blob: server closed idle connection",
		"copying the blob: EOF",
	}
	for _, msg := range retry {
		if !retryable(errors.New(msg)) {
			t.Fatalf("%q must be retried; the part file means a second attempt keeps what the "+
				"first already pulled", msg)
		}
	}

	permanent := []string{
		"ghcr.io/x/y:z is not in the registry (404). Check the tag",
		"blob sha256:abc: HTTP 403",
		"ghcr.io/x/y:z has 2 layers; this build expects one - the disk",
		"ghcr.io/x/y:z has no layers; this is not a disk artifact",
		"manifest for ghcr.io/x/y:z is not JSON: unexpected end of JSON input",
		"the layer has no digest",
	}
	for _, msg := range permanent {
		if retryable(errors.New(msg)) {
			t.Fatalf("%q must NOT be retried; the answer will be the same and the minutes are "+
				"spent learning nothing", msg)
		}
	}
}

// AND THE RETRY IS BOUNDED, SO A DEAD REGISTRY DOES NOT HOLD A JOB.
func TestTheRetryLoopIsBounded(t *testing.T) {
	body, err := readFileString("oci.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "const attempts = 4") {
		t.Fatal("the retry count is not a named constant, so it cannot be seen or reasoned about")
	}
	if !strings.Contains(body, "gave up after %d attempts") {
		t.Fatal("exhausting the attempts must say so; a silent failure after four transfers is " +
			"indistinguishable from one")
	}
}

func readFileString(name string) (string, error) {
	b, err := os.ReadFile(name)
	return string(b), err
}
