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

// A `record:` VALUE IS A NAME, NOT A PATH, AND THE FILE MUST LAND IN THE WORK DIRECTORY.
//
// MEASURED ON zorin-install, WHICH PASSED AND LEFT NO VIDEO:
//
//	=== stop recording
//	  recording: 2643 frame(s) -> zorin-install.mp4
//	path: work/*.mp4
//	##[warning]No files were found with the provided path: work/*.mp4
//
// THE ENCODE SUCCEEDED - 2643 frames - AND WROTE TO THE PROCESS'S OWN DIRECTORY. The artifact step
// uploads `work/*.mp4`, so it found nothing, and the run reported a recording that does not exist.
//
// `record: zorin-install.mp4` is a NAME. Everything the daemon writes goes in `work/`, because that is
// the directory the artifact step and every other step in the workflow look at.
func TestARecordingLandsInTheWorkDirectory(t *testing.T) {
	for _, name := range []string{"actions.go", "publish.go"} {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		src := stripCommentsForTest(string(body))
		// EVERY PLACE THAT DECIDES THE VIDEO PATH MUST JOIN IT ONTO e.Work.
		for _, marker := range []string{"e.recVideo", `"install.mp4"`} {
			i := strings.Index(src, marker)
			if i < 0 {
				continue
			}
			// The join must appear within the next 400 characters, which is where the path is built.
			window := src[i:min(i+400, len(src))]
			if !strings.Contains(window, "filepath.Join(e.Work") {
				t.Errorf("%s: the video path built from %q is not joined onto the work directory, so "+
					"the file lands beside the daemon's cwd and `work/*.mp4` finds nothing - measured "+
					"on zorin-install as a successful 2643-frame encode with no artifact", name, marker)
			}
		}
	}
}

// STOPPING THE SAMPLER IS A JOIN, NOT A CLOSE.
//
// CLOSING THE DONE CHANNEL DOES NOT STOP A TICK THAT IS ALREADY IN FLIGHT. The goroutine can be
// inside `Driver.Frame()` - up to five seconds, because a screendump waits for the file to settle -
// while `finishRecording` clears `e.rec` and starts encoding. Two things went wrong from that, and
// `go test -race` reported both:
//
//	WARNING: DATA RACE
//	Read at ... by goroutine 14:  (*Executor).sampler.func1()      publish.go:198
//	Previous write ... by goroutine 13:  (*Executor).stopSampler()  publish.go:227
//
//	WARNING: DATA RACE
//	Read at ... by goroutine 14:  (*Executor).pendingRecorder()    publish.go:34
//	Previous write ... by goroutine 13:  (*Executor).finishRecording() publish.go:324
//
// ...and `panic: close of nil channel`, because the goroutine's deferred close read a field the
// stopper had just set to nil.
//
// THE SYMPTOM IN A RUN IS THE ONE THAT MATTERS: `pendingRecorder()` CREATES a recorder when `e.rec`
// is nil, so a late tick made a SECOND one, appended its frame to that, and the frame was dropped.
// The last second of every recording was racing the encoder.
func TestStoppingTheSamplerWaitsForTheGoroutine(t *testing.T) {
	src, err := os.ReadFile("publish.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)

	i := strings.Index(s, "func (e *Executor) stopSampler(")
	if i < 0 {
		t.Fatal("stopSampler is gone")
	}
	body := s[i:]
	if end := strings.Index(body[1:], "\nfunc "); end > 0 {
		body = body[:end+1]
	}
	code := stripCommentsForTest(body)
	if !strings.Contains(code, "<-stopped") {
		t.Error("`stopSampler` closes the channel and returns. A tick already inside `Frame()` then " +
			"outlives `finishRecording`, which makes a second recorder and drops the frame - and " +
			"`go test -race` flags it")
	}
	if !strings.Contains(code, "close(done)") {
		t.Error("`stopSampler` does not close the done channel the goroutine listens on")
	}

	// AND THE GOROUTINE TAKES ITS OWN CHANNEL VALUES, not the fields. Reading `e.recStopped` in a
	// deferred close is what panicked with `close of nil channel`.
	j := strings.Index(s, "e.recDone = make(chan struct{})")
	if j < 0 {
		t.Fatal("the sampler no longer creates its channel")
	}
	head := s[j:]
	if end := strings.Index(head, "go func()"); end > 0 {
		head = head[:end]
	}
	if !strings.Contains(head, "stopped := ") && !strings.Contains(head, "stopped,") {
		t.Error("the sampler goroutine reads its channel from the Executor in a deferred close; the " +
			"stopper nils that field, and the goroutine then panics with `close of nil channel`")
	}
}
