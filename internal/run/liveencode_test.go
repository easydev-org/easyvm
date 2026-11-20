package run

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// THE LIVE ENCODER IS THE ONE PART OF THE RECORDING THAT CANNOT BE CHECKED BY "DID THE RUN PASS".
//
// A walkthrough that finishes and leaves a `work/*.mp4` behind proves the file EXISTS. It says
// nothing about whether the video has the run's frames in it, whether it is the size the file asked
// for, or whether it is a zero-frame file ffmpeg accepted - and every one of those is a way this can
// be wrong while CI goes green.
//
// SO THESE TESTS RUN THE REAL ENCODER and ask `ffprobe` what came out. ffmpeg is a build dependency
// of the recorder, so a machine without it skips rather than fails.

func ffmpegOrSkip(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is not installed")
	}
}

// probeStream reports the width, height and frame count of a video.
func probeStream(t *testing.T, path string) (int, int, int) {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height,nb_frames",
		"-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	fields := strings.Split(strings.TrimSpace(string(out)), ",")
	if len(fields) < 3 {
		t.Fatalf("ffprobe said %q, which is not width,height,frames", strings.TrimSpace(string(out)))
	}
	w, _ := strconv.Atoi(fields[0])
	h, _ := strconv.Atoi(fields[1])
	n, _ := strconv.Atoi(fields[2])
	return w, h, n
}

// makeFrame writes a solid-colour PNG of the given size.
func makeFrame(t *testing.T, dir, name, colour string, w, h int) string {
	t.Helper()
	path := filepath.Join(dir, name)
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "color=c="+colour+":s="+itoa(w)+"x"+itoa(h)+":d=1:r=1",
		"-frames:v", "1", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("making a test frame: %v (%s)", err, out)
	}
	return path
}

func itoa(n int) string { return strconv.Itoa(n) }

// TestTheLiveEncoderWritesEveryFrameAtTheRequestedSize is the whole contract in one test.
func TestTheLiveEncoderWritesEveryFrameAtTheRequestedSize(t *testing.T) {
	ffmpegOrSkip(t)
	dir := t.TempDir()
	video := filepath.Join(dir, "out.mp4")

	r := &recorder{}
	if err := r.startLiveEncoder(video, 1280, 800); err != nil {
		t.Fatalf("starting the live encoder: %v", err)
	}
	// THREE FRAMES, ONE OF THEM A DIFFERENT SIZE - which is what a real run does at the firmware.
	frames := []string{
		makeFrame(t, dir, "a.png", "blue", 1280, 800),
		makeFrame(t, dir, "b.png", "green", 640, 480),
		makeFrame(t, dir, "c.png", "red", 1280, 800),
	}
	for _, f := range frames {
		r.feedLive(f)
	}
	if err := r.closeLiveEncoder(); err != nil {
		t.Fatalf("closing the live encoder: %v", err)
	}

	w, h, n := probeStream(t, video)
	if w != 1280 || h != 800 {
		t.Fatalf("the video is %dx%d, and the file asked for 1280x800. A frame of another size "+
			"fixes the stream size at the FIRST frame ffmpeg reads, which is how a 640x480 boot menu "+
			"turned a 1920x1080 desktop recording into a 640x480 video", w, h)
	}
	if n != len(frames) {
		t.Fatalf("%d frames in the video and %d were fed. A frame that reached the writer but not "+
			"the encoder is a gap in the video nothing reports", n, len(frames))
	}
}

// TestTheLiveEncoderKeepsTheSizeWhenTheFirstFrameIsSmall is the measured failure, on its own.
//
// MEASURED TWICE on the old end-of-run encoder: a 640x480 boot menu followed by 1920x1080 frames
// produced a 640x480 video, with `-pattern_type glob` AND with a numbered sequence. The normaliser
// is what fixes it, and this test is the regression.
func TestTheLiveEncoderKeepsTheSizeWhenTheFirstFrameIsSmall(t *testing.T) {
	ffmpegOrSkip(t)
	dir := t.TempDir()
	video := filepath.Join(dir, "out.mp4")

	r := &recorder{}
	if err := r.startLiveEncoder(video, 1920, 1080); err != nil {
		t.Fatalf("starting: %v", err)
	}
	r.feedLive(makeFrame(t, dir, "boot.png", "black", 640, 480))
	r.feedLive(makeFrame(t, dir, "desk.png", "white", 1920, 1080))
	if err := r.closeLiveEncoder(); err != nil {
		t.Fatalf("closing: %v", err)
	}

	w, h, n := probeStream(t, video)
	if w != 1920 || h != 1080 {
		t.Fatalf("the video is %dx%d and the file asked for 1920x1080 - the first frame was "+
			"640x480 and it fixed the size", w, h)
	}
	if n != 2 {
		t.Fatalf("%d frames, want 2", n)
	}
}

// TestASmallFileMakesASmallVideo is the `recording_resolution:` half.
//
// THE MICROVM DRAWS 720x400 and used to be recorded into 1920x1080 - a small picture in a large
// black field, and nothing in the file said so. This proves the size reaches ffmpeg rather than
// being a constant anywhere.
func TestASmallFileMakesASmallVideo(t *testing.T) {
	ffmpegOrSkip(t)
	dir := t.TempDir()
	video := filepath.Join(dir, "microvm.mp4")

	r := &recorder{}
	if err := r.startLiveEncoder(video, 720, 400); err != nil {
		t.Fatalf("starting: %v", err)
	}
	r.feedLive(makeFrame(t, dir, "console.png", "black", 720, 400))
	if err := r.closeLiveEncoder(); err != nil {
		t.Fatalf("closing: %v", err)
	}
	w, h, _ := probeStream(t, video)
	if w != 720 || h != 400 {
		t.Fatalf("the video is %dx%d, want the file's 720x400", w, h)
	}
}

// TestTheLiveVideoIsPlayable checks the index, which is what a killed process costs.
//
// An mp4 with frames and no `moov` atom plays in nothing. Closing the encoder's input is what makes
// ffmpeg write it, so a video that exists but does not open is the failure this catches.
func TestTheLiveVideoIsPlayable(t *testing.T) {
	ffmpegOrSkip(t)
	dir := t.TempDir()
	video := filepath.Join(dir, "out.mp4")

	r := &recorder{}
	if err := r.startLiveEncoder(video, 640, 400); err != nil {
		t.Fatalf("starting: %v", err)
	}
	for i := 0; i < 5; i++ {
		r.feedLive(makeFrame(t, dir, "f"+itoa(i)+".png", "blue", 640, 400))
	}
	if err := r.closeLiveEncoder(); err != nil {
		t.Fatalf("closing: %v", err)
	}

	// A FULL DECODE, WHICH IS WHAT "PLAYABLE" MEANS. `ffprobe` reads the header; this reads the
	// frames, so a truncated stream is an error rather than a short video.
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-v", "error",
		"-i", video, "-f", "null", "-")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the video does not decode: %v (%s)", err, out)
	}
}

// TestTheLiveEncoderIsIdempotent covers `finishRecording` running after an earlier close.
func TestTheLiveEncoderIsIdempotent(t *testing.T) {
	ffmpegOrSkip(t)
	dir := t.TempDir()
	video := filepath.Join(dir, "out.mp4")

	r := &recorder{}
	if err := r.startLiveEncoder(video, 320, 240); err != nil {
		t.Fatalf("starting: %v", err)
	}
	r.feedLive(makeFrame(t, dir, "a.png", "blue", 320, 240))
	if err := r.closeLiveEncoder(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	// A SECOND CLOSE IS WHAT A FAILED WALK LOOKS LIKE: `finishRecording` runs its own close while
	// the sampler's goroutine is already gone.
	if err := r.closeLiveEncoder(); err != nil {
		t.Fatalf("second close: %v, and it must be a no-op rather than an error nobody can act on", err)
	}
	if _, _, n := probeStream(t, video); n != 1 {
		t.Fatalf("%d frames after a second close; the file was rewritten", n)
	}
	// AND THE FILE IS STILL THERE AND PLAYABLE.
	if _, err := os.Stat(video); err != nil {
		t.Fatalf("the video is gone: %v", err)
	}
}
