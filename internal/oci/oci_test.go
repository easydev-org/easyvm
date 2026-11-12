package oci

import (
	"errors"
	"os"
	"path/filepath"
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
// PUBLISHING MOVED OUT OF THIS PROGRAM, AND THE REASON IS THE SAME ONE `pull` STAYS.
//
// `easyvm push` was 246 lines of registry protocol - token exchange, blob upload, manifest PUT - with
// exactly ONE caller, which was `stage.yml`. It also shipped to every user of the binary, where it was
// a command that could only work inside CI: it needs `GITHUB_TOKEN` and a repository.
//
// PUBLISHING IS A REGISTRY TRANSFER AND `oras` IS THAT TOOL. The payload is a bare qcow2, not a
// rootfs, and oras stores arbitrary files as OCI artifacts without inventing a Dockerfile.
//
// THE PULL SIDE CANNOT FOLLOW IT, and this test is where that is written down: `internal/oci` is how
// `fetch:` resolves every installer image and every macOS asset AT RUN TIME, inside the daemon, with
// no external tool on PATH. A walkthrough that needs oras to start would be a walkthrough that cannot
// run in this image.
func TestPublishingIsOrasAndPullingStaysHere(t *testing.T) {
	// THE PULL SIDE IS THIS PACKAGE.
	body, err := os.ReadFile("oci.go")
	if err != nil {
		t.Fatal("no oci.go: nothing resolves a `fetch:` step")
	}
	if !strings.Contains(string(body), "func Pull(") {
		t.Fatal("`Pull` is gone. Every `fetch:` step - the installer images, OpenCore, the recovery " +
			"image, the developer packages - reads a tag in this repository's package, and this is " +
			"the only implementation of that")
	}

	// THE PUSH SIDE IS NOT.
	if _, err := os.Stat("push.go"); err == nil {
		t.Fatal("push.go is back. Publishing belongs to `oras push` in stage.yml: one caller, a " +
			"real artifact format, and no second registry implementation to keep correct")
	}

	// AND THE CLI HAS NO `push` SUBCOMMAND.
	main, err := os.ReadFile(filepath.Join("..", "..", "cmd", "easyvm", "main.go"))
	if err != nil {
		t.Skipf("no main.go here (%v)", err)
	}
	if strings.Contains(string(main), `case "push":`) {
		t.Fatal("`easyvm push` is dispatched again; publishing is oras, not this program")
	}
}

// AND THE WORKFLOW CALLS ORAS.
//
// The name it pushes under is `matrix.publish` - the stage AFTER this one pulls it - and oras needs a
// bare filename from the file's own directory, because it rejects an absolute path in a
// `file:media:type` reference and the flag that disables that check also disables the ones protecting
// a mistyped variable.
func TestTheWorkflowPublishesWithOras(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "stage.yml"))
	if err != nil {
		t.Skipf("no stage.yml here (%v)", err)
	}
	src := string(body)
	if !strings.Contains(src, "oras push") {
		t.Fatal("stage.yml never runs `oras push`, so no stage's disk is published and every later " +
			"stage starts from a package that has nothing in it")
	}
	// COMMENTS MAY NAME IT - the file explains what was removed and why - so only a line that
	// actually RUNS something counts.
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.Contains(trimmed, "easyvm push") {
			t.Fatalf("stage.yml still runs `easyvm push`, which no longer exists: %q", trimmed)
		}
	}
	if !strings.Contains(src, `tag="${{ matrix.publish }}"`) {
		t.Fatal("the publish step ignores `matrix.publish`, so an install leg writes its own job name " +
			"and the stage after it pulls a tag nothing publishes")
	}
	if !strings.Contains(src, `basename "$out"`) {
		t.Fatal("oras is given a path rather than a bare filename from the file's own directory, " +
			"which it refuses - and the flag that would allow it also disables the checks that stop " +
			"a mistyped variable pushing a key file")
	}
	// IT MUST TOLERATE A MISSING FILE: an `ssh` leg's product is the disk it started from, and
	// several stages write no disk at all.
	if !strings.Contains(src, "no disk to publish at") {
		t.Fatal("the publish step does not tolerate a stage that writes no disk")
	}
}
