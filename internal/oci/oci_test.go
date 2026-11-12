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

// `pull` HAD NO COUNTERPART, AND THAT WAS A STRUCTURAL GAP RATHER THAN AN OMISSION.
//
// Every stage AFTER an install starts from a published disk: the workflow leg says
// `"pull": "linux-<distro>-<stage>"` and `easyvm pull` fetches it. NOTHING EVER PUSHED. The install
// legs declared `"out": "linux-<distro>.qcow2"` and produced it, and then the file travelled as a
// 4-to-8 GiB GitHub artifact that nothing downstream read.
//
// MEASURED: five install legs sat in `upload-artifact` for an hour AFTER their walkthroughs had
// finished - and a run whose install step list is
//
//	follow it, what it produced, stop the daemon, upload-artifact x3, ...
//
// is a run that has DONE ITS WORK and is paying to move a disk nobody will read.
//
// `Push` IS THE MIRROR OF `Pull`: one layer holding the disk, an empty config blob, and a manifest
// naming both. The test asserts the shape rather than the network, because a unit test cannot reach a
// registry - but the shape is what the protocol requires, and a typo in it fails on the first real
// push with a message the test would have made unnecessary.
func TestPushExistsAndAsksForWriteAccess(t *testing.T) {
	body, err := os.ReadFile("push.go")
	if err != nil {
		t.Fatal("no push.go: `easyvm pull` has no counterpart, so nothing can publish a disk for the " +
			"stage after it - which is why five install legs uploaded for an hour")
	}
	src := string(body)

	// A PUSH NEEDS `pull,push` SCOPE; a pull's token is `pull` alone and is refused.
	if !strings.Contains(src, `"pull,push"`) {
		t.Fatal("`Push` does not ask for `pull,push` scope, so the registry refuses the upload with " +
			"a `denied` that reads like a permissions problem on the package")
	}
	// AND THE THREE PIECES OF AN OCI MANIFEST.
	for _, want := range []string{
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.oci.image.config.v1+json",
		"schemaVersion",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("the manifest does not carry %q", want)
		}
	}
	// AND AN EMPTY FILE IS REFUSED, because a stage that produced nothing must not publish it.
	if !strings.Contains(src, "is empty; a stage that produced nothing must not publish it") {
		t.Fatal("`Push` would publish an empty file, so a stage that produced nothing would " +
			"overwrite a good disk with nothing")
	}
}

// AND THE WORKFLOW CALLS IT.
func TestTheWorkflowPublishesEachStagesDisk(t *testing.T) {
	body, err := os.ReadFile("../../.github/workflows/stage.yml")
	if err != nil {
		t.Skipf("no stage.yml here (%v)", err)
	}
	src := string(body)
	if !strings.Contains(src, "easyvm push --tag") {
		t.Fatal("the workflow never runs `easyvm push`, so no stage's disk is published and every " +
			"later stage starts from whatever `ssh` uploaded")
	}
	// IT MUST TOLERATE A MISSING FILE: an `ssh` leg's product is the disk it started from, and several
	// stages write no disk at all.
	if !strings.Contains(src, "no disk to publish at") {
		t.Fatal("the publish step does not tolerate a stage that writes no disk, so every ssh leg " +
			"would fail on a step about a file it was never meant to produce")
	}
}
