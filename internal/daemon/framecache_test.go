package daemon

import (
	"errors"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// THE CACHE EXISTS BECAUSE THREE READERS FOUGHT OVER ONE QMP SOCKET.
//
// MEASURED on macos-setup: a `check:` gate ran `poll: 10` for `timeout: 1800` - about 180 attempts -
// and its log holds FOURTEEN frames. The recording sampler won the other 166 and the gate continued
// in silence, because a gate that reads nothing looks exactly like a gate that is not up yet.
//
// SO THESE TESTS ARE ABOUT THE ARITHMETIC OF SHARING: many readers, one capture a second, and every
// reader getting a frame that is NEW ENOUGH for what it is about to decide.

// TestManyReadersShareOneCapturePerSecond is the property the cache was built for.
func TestManyReadersShareOneCapturePerSecond(t *testing.T) {
	var captures atomic.Int64
	c := newFrameCache(func() (cachedFrame, error) {
		n := captures.Add(1)
		return cachedFrame{path: "frame-" + itoa(int(n)) + ".png", w: 1920, h: 1080, at: time.Now()}, nil
	})
	defer close(c.stop)

	// FORTY READERS ASK, FOUR TIMES OVER, WHILE THE TICKER RUNS. Under the old code every one of
	// these was its own screendump; here the count is bounded by the seconds that passed.
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 4; j++ {
				if _, err := c.latest(); err != nil {
					// An empty cache before the first tick is allowed; the test is about capture
					// COUNT, not about every read succeeding.
					continue
				}
				time.Sleep(20 * time.Millisecond)
			}
		}()
	}
	wg.Wait()
	time.Sleep(1100 * time.Millisecond)

	if n := captures.Load(); n > 6 {
		t.Fatalf("%d captures for 160 reads over about a second. The cache is supposed to make the "+
			"reader count irrelevant - one a second regardless", n)
	}
	// AT LEAST ONE TICK. The window is a little over a second, so exactly one is the expected
	// number and the point is that 160 READS produced it rather than 160 captures.
	if captures.Load() < 1 {
		t.Fatal("no capture at all in over a second; the ticker is not running")
	}
}

// TestTheFirstCallerWaitsForTheFirstTick is the recorder's own case.
//
// ITS SAMPLER STARTS AT STEP ONE, before the cache's ticker has run once, and a nil frame there is
// reported as "the steps never looked at the screen" - true of the timing and false about the run.
func TestTheFirstCallerWaitsForTheFirstTick(t *testing.T) {
	c := newFrameCache(func() (cachedFrame, error) {
		return cachedFrame{path: "f.png", w: 100, h: 100, at: time.Now()}, nil
	})
	defer close(c.stop)

	// NOT SEEDED: waitForFirst has to wait for the ticker.
	f, ok := c.waitForFirst(3 * time.Second)
	if !ok {
		t.Fatal("no frame arrived within three seconds; the ticker is not running")
	}
	if f.path == "" {
		t.Fatal("a frame with no path")
	}
}

// TestTheNewestFrameIsTheOneAReaderGets is the whole contract now that `wait:` is the file's job.
//
// THE TIMESTAMP DOES NOT PICK THE FRAME ANY MORE. `wait: 3` means "look three seconds from now", and
// the frame it looks at is whatever the one-a-second capture has by then. An earlier version asked
// for a frame stamped at or after the current instant, which made every `wait: 0` wait for the next
// tick - 151 of the tree's 194 waits are `wait: 0`.
func TestTheNewestFrameIsTheOneAReaderGets(t *testing.T) {
	c := newFrameCache(func() (cachedFrame, error) {
		return cachedFrame{path: "f.png", at: time.Now()}, nil
	})
	defer close(c.stop)
	c.captureOnce()
	c.captureOnce()
	f, err := c.latest()
	if err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	count := len(c.frames)
	newest := c.frames[count-1].at
	c.mu.Unlock()
	if count != 2 {
		t.Fatalf("the cache holds %d captures after two, so the newest is not being kept", count)
	}
	if !f.at.Equal(newest) {
		t.Fatal("latest returned a capture that is not the newest one")
	}
}

// TestAnEmptyCacheSaysWhy covers the first call, before any tick: the error names the capture failure
// rather than a bare "not exist".
func TestAnEmptyCacheSaysWhy(t *testing.T) {
	sentinel := errors.New("qmp: no machine")
	c := newFrameCache(func() (cachedFrame, error) { return cachedFrame{}, sentinel })
	defer close(c.stop)

	if _, err := c.latest(); !errors.Is(err, osErrNotExist()) && err == nil {
		t.Fatal("an empty cache returned no error")
	}
	// ONE CAPTURE ATTEMPT, SO THE REASON IS RECORDED.
	c.captureOnce()
	if _, err := c.latest(); !errors.Is(err, sentinel) {
		t.Fatalf("the error is %v, and the capture's own failure was %v - a reader cannot tell an "+
			"empty cache from a broken machine", err, sentinel)
	}
}

// TestTheCacheIsBounded keeps a long run from filling the disk with debug frames.
func TestTheCacheIsBounded(t *testing.T) {
	c := newFrameCache(func() (cachedFrame, error) {
		return cachedFrame{path: "f.png", at: time.Now()}, nil
	})
	defer close(c.stop)
	for i := 0; i < frameHistory+20; i++ {
		c.captureOnce()
	}
	c.mu.Lock()
	n := len(c.frames)
	c.mu.Unlock()
	if n != frameHistory {
		t.Fatalf("the cache holds %d frames, and its ceiling is %d - a 90-minute install would "+
			"write ~540 MB of debug frames", n, frameHistory)
	}
}

func itoa(n int) string    { return strconv.Itoa(n) }
func osErrNotExist() error { return os.ErrNotExist }
