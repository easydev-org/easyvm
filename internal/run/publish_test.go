package run

import (
	"os"
	"strings"
	"testing"
)

// A FAILED WALK MUST STILL WRITE ITS RECORDING, BECAUSE THAT IS THE ONE THAT IS LOOKED AT.
//
// MEASURED ON popos-install, which died at step 6 of 26:
//
//	linux-popos-install-logs   1,693 bytes
//	linux-popos-install-disk     518 bytes
//	(no -video artifact at all)
//
// THE FRAMES HAD BEEN CAPTURED ONCE PER SECOND FOR FOUR MINUTES and were thrown away, because `encode`
// was only ever called by the `record: false` STEP - and a walk that fails before its last step never
// reaches it. The question "what was on screen" had no answer in the run, which is the whole reason a
// recording exists.
//
// THE FIX IS IN `Run`'s defer: `finishRecording` stops the sampler AND writes the video.
func TestAFailedWalkStillWritesItsRecording(t *testing.T) {
	exec, err := os.ReadFile("executor.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(exec)
	i := strings.Index(src, "func (e *Executor) Run(")
	if i < 0 {
		t.Fatal("no Run")
	}
	rest := src[i:]
	if end := strings.Index(rest[1:], "\nfunc "); end > 0 {
		rest = rest[:end+1]
	}
	code := stripCommentsForTest(rest)
	if !strings.Contains(code, "defer e.finishRecording()") {
		t.Fatal("`Run` does not finish the recording on the way out, so a walk that fails before its " +
			"`record: false` step leaves no video - measured on popos-install, where that is exactly " +
			"what happened")
	}
	if strings.Contains(code, "defer e.stopSampler()") {
		t.Fatal("`Run` still only STOPS the sampler; the frames are captured and the encode never runs")
	}

	// AND `finishRecording` ENCODES, rather than merely stopping.
	pub, err := os.ReadFile("publish.go")
	if err != nil {
		t.Fatal(err)
	}
	p := strings.Index(string(pub), "func (e *Executor) finishRecording(")
	if p < 0 {
		t.Fatal("no finishRecording")
	}
	body := string(pub)[p:]
	if end := strings.Index(body[1:], "\nfunc "); end > 0 {
		body = body[:end+1]
	}
	bodyCode := stripCommentsForTest(body)
	if !strings.Contains(bodyCode, "rec.encode(") {
		t.Fatal("`finishRecording` stops the sampler without encoding, so the frames are discarded")
	}
	if !strings.Contains(bodyCode, "e.stopSampler()") {
		t.Fatal("`finishRecording` does not stop the sampler, so the encoder reads a file still being " +
			"written")
	}
}
