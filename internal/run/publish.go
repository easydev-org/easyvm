package run

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// --- recording ------------------------------------------------------------------------

// recorder keeps the frames a `record:` step is between, and encodes them at the end.
//
// WHY THE STEP STARTS ITS OWN SAMPLER: the Python implementation's `record:` only ENCODED, and
// `lib/live.py` returns without writing anything when there are no frames. A stage whose steps are
// all `key:` and `ssh:` never screenshots, so the shots directory stayed empty and the run uploaded
// nothing - while the log said "recording: ...install.mp4". The step that says "record this" is the
// one that has to take the pictures.
type recorder struct {
	mu     sync.Mutex
	frames []string
	ready  bool
}

func (e *Executor) pendingRecorder() *recorder {
	if e.rec == nil {
		e.rec = &recorder{}
	}
	return e.rec
}

// encode writes the frames as a video.
//
// IT REFUSES WITH A SENTENCE WHEN THERE IS NOTHING TO ENCODE. Returning silently is what made the
// Python implementation's first version upload a 303-byte screenshot instead of a recording.
func (r *recorder) encode(video string, log func(string)) error {
	r.mu.Lock()
	frames := append([]string(nil), r.frames...)
	r.mu.Unlock()

	if len(frames) == 0 {
		return fmt.Errorf("no frames were captured, so there is no video to write; the steps " +
			"before this one never looked at the screen (a `record:` step takes the pictures " +
			"itself for exactly this reason)")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return fmt.Errorf("ffmpeg is not installed, so the frames cannot be encoded: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(video), 0o755); err != nil {
		return err
	}
	// The frames are numbered by the sampler, so a glob plus a fixed rate is enough; `-framerate 1`
	// matches the one-frame-per-second sampler rather than the step's own pace.
	pattern := filepath.Join(filepath.Dir(frames[0]), "frame-*.ppm")
	cmd := exec.Command("ffmpeg", "-y", "-loglevel", "error",
		"-framerate", "1", "-pattern_type", "glob", "-i", pattern,
		"-c:v", "libx264", "-pix_fmt", "yuv420p", video)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	log(fmt.Sprintf("  recording: %d frame(s) -> %s", len(frames), video))
	return nil
}

// --- publishing -----------------------------------------------------------------------

// Compress converts the disk into a compressed qcow2 at dest.
//
// ZSTD OR NOTHING. The Python implementation had a quiet fallback to zlib for one revision and it
// produced disks 10-20% larger under a log line that read like a note rather than a defect. If this
// qemu-img cannot write zstd then this qemu-img is wrong, and the stage says so.
//
// IT WRITES TO A TEMPORARY NAME FIRST, because qemu-img writes in place: a failure part-way would
// leave a truncated file with the FINAL name, and the publish step would push it.
func compressDisk(source, dest string, log func(string)) error {
	if _, err := os.Stat(source); err != nil {
		return fmt.Errorf("no disk to compress at %s: %w", source, err)
	}
	qemuImg, err := exec.LookPath("qemu-img")
	if err != nil {
		return fmt.Errorf("qemu-img is not installed: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	part := dest + ".part"
	_ = os.Remove(part)

	log(fmt.Sprintf("  compressing with zstd (%s)", qemuImg))
	cmd := exec.Command(qemuImg, "convert", "-O", "qcow2", "-c",
		"-o", "compression_type=zstd", source, part)
	out, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.Remove(part)
		return fmt.Errorf("qemu-img convert: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	if err := os.Rename(part, dest); err != nil {
		_ = os.Remove(part)
		return err
	}
	if fi, err := os.Stat(dest); err == nil {
		log(fmt.Sprintf("  %s: %d MiB", filepath.Base(dest), fi.Size()>>20))
	}
	return nil
}

// sampler takes one frame a second into dir, for as long as the recording runs.
func (e *Executor) sampler(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	e.recDone = make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-e.recDone:
				return
			case <-ticker.C:
				f, err := e.Driver.Frame()
				if err != nil {
					continue
				}
				e.pendingRecorder().mu.Lock()
				e.pendingRecorder().frames = append(e.pendingRecorder().frames, f.Path())
				e.pendingRecorder().mu.Unlock()
			}
		}
	}()
	return nil
}

func (e *Executor) stopSampler() {
	if e.recDone != nil {
		close(e.recDone)
		e.recDone = nil
	}
}
