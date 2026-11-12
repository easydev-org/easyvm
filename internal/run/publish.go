package run

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/easydev-org/easyvm/internal/walk"
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

// The recording's size, and FHD is the default because every guest here draws at 1920x1080.
//
// IT IS NOT A DOWNSCALE: a frame that is already 1920x1080 passes through unchanged, and the only
// frames that are padded are the ones qemu drew smaller - the boot menu at 640x480. Without a fixed
// size the encoder takes whatever the FIRST frame happens to be, which is how a 424-second recording
// of a 1920x1080 desktop came out 640x480.
const (
	RecordWidth  = 1920
	RecordHeight = 1080
)

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

	// THE FRAMES ARE RENUMBERED INTO A SCRATCH DIRECTORY AS ONE UNIFORM FORMAT, WHICH IS THE ONLY WAY
	// THE ENCODER CAN BE TOLD WHICH FRAMES AND IN WHAT ORDER.
	//
	// MEASURED ON popos-install: a 424-SECOND RECORDING OF A 1920x1080 GUEST CAME OUT 640x480.
	//
	// `screendump` WRITES ONE DIRECTORY WITH ONE NUMBER SEQUENCE, shared by the sampler and by every
	// `until:`/`desktop:`/`expect:` that reads the screen. THE FIRST FRAME OF THE RUN IS THE BOOT MENU,
	// which qemu draws at 640x480 - and the encode used
	//
	//	-pattern_type glob -i frame-*.ppm
	//
	// which takes its size from the first file the glob returns. So a desktop at 1920x1080 was
	// letterboxed into a boot menu's 640x480 for the whole video.
	//
	// THE REFERENCE IMPLEMENTATION SOLVED THIS AND ITS NOTE IS WHY: it stages every frame as a JPEG
	// with a fresh `frame-%04d` number, then encodes the sequence. That fixes three things at once -
	// only the sampler's frames are encoded, their order is the sampler's, and a mixed set (PPM from
	// one step, JPEG from another) has one extension.
	//
	// AND IT RECORDS THE GLOB TRAP: `-pattern_type glob -i 'frame-*'` with no extension is rejected by
	// ffmpeg's image2 demuxer, so the staged files are all one type and the input is a NUMBER SEQUENCE.
	stage, err := os.MkdirTemp(filepath.Dir(video), "frames-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)

	n := 0
	for _, src := range frames {
		dst := filepath.Join(stage, fmt.Sprintf("frame-%04d.jpg", n))
		// CONVERTED **AND SCALED TO THE GUEST'S OWN SCREEN**, because a mixed-size sequence is encoded
		// at the FIRST frame's size whatever the later frames are.
		//
		// MEASURED, TWICE: a 640x480 first frame followed by 1920x1080 frames produced a 640x480 video
		// BOTH with `-pattern_type glob` and with a numbered sequence. ffmpeg's image2 demuxer fixes
		// the stream size when it reads the first file; the rest are then resized into it. So the
		// encoder cannot be told "use the desktop's size" - the FRAMES must be made uniform first.
		//
		// THE BOOT MENU IS GENUINELY 640x480 and cannot be upscaled into detail it does not have, so
		// those frames are letterboxed rather than stretched: `force_original_aspect_ratio=decrease`
		// shrinks the frame to fit and `pad` centres it in the FHD frame. The result is honest - a
		// small picture in a large frame - and the desktop frames that follow are native 1920x1080.
		//
		// `-q:v 4` IS THE REFERENCE IMPLEMENTATION'S NUMBER for the intermediate JPEG.
		cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-nostdin", "-y",
			"-i", src,
			"-vf", fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease,"+
				"pad=%d:%d:(ow-iw)/2:(oh-ih)/2", RecordWidth, RecordHeight, RecordWidth, RecordHeight),
			"-q:v", "4", dst)
		if out, err := cmd.CombinedOutput(); err != nil {
			log(fmt.Sprintf("  recording: could not convert %s: %s", filepath.Base(src),
				firstLineOf(string(out))))
			continue
		}
		n++
	}
	if n == 0 {
		return fmt.Errorf("no frame could be converted, so there is no video to write")
	}

	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "warning", "-nostdin", "-y",
		"-framerate", "1", "-start_number", "0",
		"-i", filepath.Join(stage, "frame-%04d.jpg"),
		"-c:v", "libx264", "-preset", "ultrafast",
		"-pix_fmt", "yuv420p", "-movflags", "+faststart",
		video)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg: %w (%s)", err, firstLineOf(string(out)))
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

// --- waiting for a disk to stop growing ------------------------------------------------

// doDiskSettle waits until the target stops growing, once it has been written to at all.
//
// AN INSTALLATION WRITES GIGABYTES INTO THE TARGET QCOW2 AND A GUEST THAT HAS STOPPED WRITES
// NOTHING. That is the only measurement that can tell SLOW from STOPPED - and the alternative, a
// sentence on screen, does not: `Finalizing installation may take a few moments` is printed WHILE
// it is still writing, so ending a wait on it declares the install done at the moment the last files
// are being copied.
//
// `disk_min_mb` IS THE FLOOR, and it is not decoration: an untouched sparse image has a few MiB of
// metadata and would look settled immediately. 512 MiB is well above that and well below any real
// install.
//
// `disk_settle: 180` COVERS ONE QCOW2 FLUSH without spending minutes afterwards. The check is the
// file's ALLOCATED size - `st_blocks`, not the apparent size - because a sparse qcow2 reports its
// full virtual size from the first second.
func (e *Executor) doDiskSettle(s walk.Step) error {
	quiet, err := s.Float("disk_settle", 60)
	if err != nil || quiet <= 0 {
		return fail(s.Name, "`disk_settle:` must be a positive number of quiet seconds")
	}
	floor, _ := s.Float("disk_min_mb", 0)
	timeout := seconds(s, "timeout", 3600)
	poll := seconds(s, "poll", 20)

	path := e.bootDisk()
	if path == "" {
		return fail(s.Name, "no disk is known; a `disk:` step must come first")
	}

	e.Driver.Log(fmt.Sprintf("  waiting until %s stops growing for %.0fs, after %s has %.0f MiB",
		filepath.Base(path), quiet, filepath.Base(path), floor))

	start := time.Now()
	var lastSize int64 = -1
	var quietSince time.Time
	for time.Since(start) < timeout {
		size := allocatedBytes(path)
		if size < 0 {
			return fail(s.Name, "no disk at %s", path)
		}
		mb := float64(size) / (1 << 20)
		switch {
		case mb < floor:
			// NOT WRITTEN TO YET. The floor is what stops an untouched image looking settled.
			quietSince = time.Time{}
		case size != lastSize:
			quietSince = time.Time{}
		case quietSince.IsZero():
			quietSince = time.Now()
		case time.Since(quietSince) >= time.Duration(quiet*float64(time.Second)):
			e.Driver.Log(fmt.Sprintf("  %.0f MiB, unchanged for %.0fs after %s - the write is "+
				"finished", mb, quiet, time.Since(start).Round(time.Second)))
			return nil
		}
		lastSize = size
		if s.Has("keep_alive") {
			// A GUEST THAT BLANKS ITS OWN SCREEN wakes on a click, and a blank screen during an
			// install is normal rather than a failure.
			_ = e.Driver.Click(400+(int(time.Since(start).Seconds())%40), 300)
		}
		time.Sleep(poll)
	}
	return failWithScreen(s.Name, e.Driver,
		"the disk never stopped growing within %s; it is at %.0f MiB. A disk still moving after an "+
			"hour is a slow install; one that has not reached the floor is an install that never "+
			"started", timeout, float64(allocatedBytes(path))/(1<<20))
}

// allocatedBytes is st_blocks * 512: what the file actually occupies, which is what grows on a
// sparse qcow2. Stat().Size() is the virtual size from the first second and says nothing.
func allocatedBytes(path string) int64 {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return -1
	}
	return st.Blocks * 512
}

// finishRecording stops the sampler and writes the video, so a FAILED walk still leaves a recording.
//
// IT IS THE `defer` OF `Run`, and it exists because the `record: false` STEP IS UNREACHABLE WHEN A STEP
// BEFORE IT FAILS. Measured on popos-install, which died at step 6 of 26 with a 1,693-byte `-logs`
// artifact and NO `-video`: the frames had been captured once per second for four minutes and were
// thrown away, and the question "what was on screen" had no answer in the run.
//
// IT DOES NOT REPORT AN ERROR. The walk is already returning one, and a failed encode would replace a
// useful message with a cosmetic one - but it DOES log, because a recording that silently did not
// happen is the failure this whole mechanism was rewritten to stop.
func (e *Executor) finishRecording() {
	rec := e.rec
	video := e.recVideo
	e.rec = nil

	e.stopSampler()
	if rec == nil {
		return
	}
	if video == "" {
		video = "install.mp4"
	}
	if !filepath.IsAbs(video) {
		video = filepath.Join(e.Work, video)
	}
	if err := rec.encode(video, e.Driver.Log); err != nil {
		e.Driver.Log(fmt.Sprintf("  recording NOT written: %v", err))
	}
}
