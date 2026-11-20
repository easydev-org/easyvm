package daemon

// THE FRAME CACHE: ONE CAPTURE PER SECOND, SHARED BY EVERY READER.
//
// THREE READERS USED TO ASK QEMU FOR A SCREEN AT THE SAME TIME - the recording sampler (1/s), a
// `check:` gate (1/10s) and the browser page (1/s) - and they SHARED ONE QMP CONNECTION. Measured on
// macos-setup: the gate ran `poll: 10` for `timeout: 1800`, which is about 180 attempts, and its own
// log holds FOURTEEN. The other 166 returned an error and the loop continued in silence, because a
// gate that reads nothing is indistinguishable from a gate that is not up yet.
//
// SO THE SCREEN IS CAPTURED ONCE AND READ MANY TIMES. One ticker takes a frame a second and keeps the
// last `frameHistory` of them; a reader asks for a frame and gets the newest one, or waits for the
// next tick. That is the same number of captures the recorder alone used to make.
//
// IT IS A CACHE OF FACT, NOT OF PIXELS, and the distinction matters: a reader that asks at time T
// wants a frame from T or later, because the click it just made has to be ON the screen it reads. A
// cache that handed back "the newest" would serve the frame from before the click and the step would
// fail against a screen that had already changed.

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

// frameHistory is how many captures are kept, at one a second.
//
// SIXTY SECONDS IS A DEBUGGING WINDOW, NOT A RECORDING. A step that failed leaves the minute before
// it on disk, which is what a person needs to see what the screen was doing; the recording itself is
// the recorder's job and keeps every frame it was given. At ~100 KB a frame this is about 6 MB.
const frameHistory = 60

// cachedFrame is one capture.
type cachedFrame struct {
	path string
	w, h int
	at   time.Time
}

func (f cachedFrame) Path() string     { return f.path }
func (f cachedFrame) Size() (int, int) { return f.w, f.h }
func (f cachedFrame) Words() []string  { return nil }

// frameCache holds the captures and the goroutine that makes them.
type frameCache struct {
	mu     sync.Mutex
	frames []cachedFrame // oldest first
	// seq is a counter so two captures in one second do not collide on a filename.
	seq int
	// wake is closed and replaced to tell a waiter that a new frame arrived.
	wake chan struct{}
	// stop ends the ticker.
	stop chan struct{}
	// capture is what takes a frame; it is a field so the tests can drive the cache without qemu.
	capture func() (cachedFrame, error)
	// lastErr is why the last capture failed, for a reader's message.
	lastErr error
}

// newFrameCache starts the ticker. `capture` is called once a second.
func newFrameCache(capture func() (cachedFrame, error)) *frameCache {
	c := &frameCache{wake: make(chan struct{}), stop: make(chan struct{}), capture: capture}
	go c.run()
	return c
}

func (c *frameCache) run() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-ticker.C:
			c.captureOnce()
		}
	}
}

func (c *frameCache) captureOnce() {
	f, err := c.capture()
	c.mu.Lock()
	if err != nil {
		// A MACHINE THAT HAS GONE IS NOT A FAILED CAPTURE, and the difference is a lie in a log: a
		// `record:` before a `boot:` and a walk that failed both leave the ticker running with no
		// machine, and counting those as failures would report a reader's view that is simply absent.
		c.lastErr = err
		c.mu.Unlock()
		return
	}
	c.lastErr = nil
	c.seq++
	c.frames = append(c.frames, f)
	if len(c.frames) > frameHistory {
		// THE OLDEST IS REMOVED FROM THE CACHE BUT NOT FROM DISK. A recording may still be reading
		// it, and the file is the artifact; what is bounded is the slice, not the evidence.
		c.frames = c.frames[len(c.frames)-frameHistory:]
	}
	close(c.wake)
	c.wake = make(chan struct{})
	c.mu.Unlock()
}

// latest returns the newest capture, or an error naming why there is none.
func (c *frameCache) latest() (cachedFrame, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.frames) == 0 {
		if c.lastErr != nil {
			return cachedFrame{}, c.lastErr
		}
		return cachedFrame{}, os.ErrNotExist
	}
	return c.frames[len(c.frames)-1], nil
}

// age reports how old the newest capture is, for `/api/state`.
func (c *frameCache) age() (time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.frames) == 0 {
		return 0, false
	}
	return time.Since(c.frames[len(c.frames)-1].at), true
}

// recent returns the paths of the last n captures, oldest first - what a failed step prints.
func (c *frameCache) recent(n int) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if n <= 0 || n > len(c.frames) {
		n = len(c.frames)
	}
	out := make([]string, 0, n)
	for _, f := range c.frames[len(c.frames)-n:] {
		out = append(out, filepath.Base(f.path)+" ("+f.at.Format("15:04:05")+")")
	}
	return out
}

// sortedFrames is for tests that want to compare a set without depending on capture order.
func sortedFrames(paths []string) []string {
	sort.Strings(paths)
	return paths
}

// frameSeqName is the filename a capture gets. It is a number rather than a timestamp because qemu
// writes the file and two captures inside one second must not collide.
func frameSeqName(dir string, seq int) string {
	return filepath.Join(dir, "frame-"+strconv.Itoa(seq)+".png")
}

// waitForFirst blocks until the cache has at least one capture, or the budget expires.
//
// IT IS THE STARTUP CASE ONLY. A recorder that begins at step one asks for a frame before the first
// tick has happened, and a nil frame would be reported as "the steps never looked at the screen" -
// which is true of the caller's timing and false about the run.
func (c *frameCache) waitForFirst(budget time.Duration) (cachedFrame, bool) {
	deadline := time.Now().Add(budget)
	for {
		c.mu.Lock()
		if len(c.frames) > 0 {
			f := c.frames[len(c.frames)-1]
			c.mu.Unlock()
			return f, true
		}
		wake := c.wake
		c.mu.Unlock()
		if time.Now().After(deadline) {
			return cachedFrame{}, false
		}
		select {
		case <-wake:
		case <-time.After(100 * time.Millisecond):
		}
	}
}
