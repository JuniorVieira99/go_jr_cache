package tests

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jr_cache "jr_cache/code"
)

// counter is a trivial collectable: every collection releases whatever has
// been added since the last one.
type counter struct {
	pending atomic.Int64
	runs    atomic.Int64
}

func (c *counter) collect() int {
	c.runs.Add(1)
	return int(c.pending.Swap(0))
}

func newCounterGC(t *testing.T, config jr_cache.GCConfig) (*jr_cache.GCManager[*counter], *counter) {
	t.Helper()
	c := &counter{}
	gc, err := jr_cache.NewGCManager(c, (*counter).collect, config)
	if err != nil {
		t.Fatalf("NewGCManager error: %v", err)
	}
	return gc, c
}

// waitFor polls until cond is true or the deadline passes.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return cond()
}

func TestGCManager_Constructor(t *testing.T) {
	if _, err := jr_cache.NewGCManager[*counter](nil, nil, jr_cache.GCConfig{}); !errors.Is(err, jr_cache.ErrNilCollector) {
		t.Fatalf("nil collector: err = %v, want ErrNilCollector", err)
	}
	if _, err := jr_cache.NewGCManager(&counter{}, (*counter).collect, jr_cache.GCConfig{Interval: -time.Second}); !errors.Is(err, jr_cache.ErrInvalidInterval) {
		t.Fatalf("negative interval: err = %v, want ErrInvalidInterval", err)
	}

	date := time.Now().Add(time.Hour)
	gc, _ := newCounterGC(t, jr_cache.GCConfig{
		Interval:        time.Second,
		TimeThreshold:   2 * time.Second,
		DateThreshold:   date,
		MemoryThreshold: 1 << 30,
	})
	if gc.GetInterval() != time.Second || gc.GetTimeThreshold() != 2*time.Second ||
		!gc.GetDateThreshold().Equal(date) || gc.GetMemoryThreshold() != 1<<30 {
		t.Fatal("constructor did not carry the configuration through")
	}
	if gc.Running() {
		t.Fatal("a new manager should not be running")
	}
}

func TestGCManager_GCCollect(t *testing.T) {
	gc, c := newCounterGC(t, jr_cache.GCConfig{})
	c.pending.Store(7)
	if n := gc.GCCollect(); n != 7 {
		t.Fatalf("GCCollect() = %d, want 7", n)
	}
	if n := gc.GCCollect(); n != 0 {
		t.Fatalf("second GCCollect() = %d, want 0", n)
	}
	stats := gc.Stats()
	if stats.Collections != 2 || stats.Released != 7 || stats.LastRun.IsZero() || stats.Running {
		t.Fatalf("Stats() = %+v", stats)
	}
}

func TestGCManager_StartStop(t *testing.T) {
	gc, c := newCounterGC(t, jr_cache.GCConfig{})

	// Without an interval there is nothing to tick.
	if err := gc.Start(); !errors.Is(err, jr_cache.ErrInvalidInterval) {
		t.Fatalf("Start without an interval: err = %v, want ErrInvalidInterval", err)
	}
	if err := gc.SetInterval(0); !errors.Is(err, jr_cache.ErrInvalidInterval) {
		t.Fatalf("SetInterval(0) = %v, want ErrInvalidInterval", err)
	}
	if err := gc.SetInterval(5 * time.Millisecond); err != nil {
		t.Fatalf("SetInterval error: %v", err)
	}

	if err := gc.Start(); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	if !gc.Running() {
		t.Fatal("Running() is false after Start")
	}
	if err := gc.Start(); !errors.Is(err, jr_cache.ErrAlreadyRunning) {
		t.Fatalf("second Start: err = %v, want ErrAlreadyRunning", err)
	}
	if !waitFor(t, time.Second, func() bool { return c.runs.Load() >= 2 }) {
		t.Fatalf("the loop collected %d times, want at least 2", c.runs.Load())
	}

	gc.Stop()
	if gc.Running() {
		t.Fatal("Running() is true after Stop")
	}
	// Once stopped the loop must not collect again.
	after := c.runs.Load()
	time.Sleep(40 * time.Millisecond)
	if c.runs.Load() != after {
		t.Fatalf("the loop kept collecting after Stop: %d then %d", after, c.runs.Load())
	}

	// Stop is idempotent and a stopped manager can be restarted.
	gc.Stop()
	if err := gc.Start(); err != nil {
		t.Fatalf("restart error: %v", err)
	}
	if !waitFor(t, time.Second, func() bool { return c.runs.Load() > after }) {
		t.Fatal("the loop did not collect after a restart")
	}
	gc.Stop()
}

func TestGCManager_TimeThreshold(t *testing.T) {
	// Ticks every 5 ms but collects at most every 50 ms.
	gc, c := newCounterGC(t, jr_cache.GCConfig{
		Interval:      5 * time.Millisecond,
		TimeThreshold: 50 * time.Millisecond,
	})
	if err := gc.Start(); err != nil {
		t.Fatal(err)
	}
	defer gc.Stop()

	time.Sleep(120 * time.Millisecond)
	runs := c.runs.Load()
	if runs == 0 {
		t.Fatal("the time threshold never fired")
	}
	if runs > 6 {
		t.Fatalf("collected %d times in ~120 ms with a 50 ms threshold; the tick is not being throttled", runs)
	}
}

func TestGCManager_DateThresholdFiresOnce(t *testing.T) {
	gc, c := newCounterGC(t, jr_cache.GCConfig{
		Interval:      5 * time.Millisecond,
		DateThreshold: time.Now().Add(20 * time.Millisecond),
	})
	if err := gc.Start(); err != nil {
		t.Fatal(err)
	}
	defer gc.Stop()

	if !waitFor(t, time.Second, func() bool { return c.runs.Load() >= 1 }) {
		t.Fatal("the date threshold never fired")
	}
	time.Sleep(60 * time.Millisecond)
	if runs := c.runs.Load(); runs != 1 {
		t.Fatalf("the date threshold fired %d times, want exactly 1", runs)
	}
	if !gc.DateThresholdFired() {
		t.Fatal("DateThresholdFired() is false after the trigger went off")
	}
	// A spent date trigger still counts as configured: the manager must not
	// fall back to collecting on every tick.
	if got := gc.GetDateThreshold(); got.IsZero() {
		t.Fatal("GetDateThreshold() was cleared; it should be kept and marked as fired")
	}

	// Re-arming makes it fire once more.
	gc.SetDateThreshold(time.Now().Add(10 * time.Millisecond))
	if gc.DateThresholdFired() {
		t.Fatal("DateThresholdFired() is true right after re-arming")
	}
	if !waitFor(t, time.Second, func() bool { return c.runs.Load() >= 2 }) {
		t.Fatal("the re-armed date threshold never fired")
	}
	time.Sleep(60 * time.Millisecond)
	if runs := c.runs.Load(); runs != 2 {
		t.Fatalf("after re-arming the date threshold fired a total of %d times, want 2", runs)
	}
}

func TestGCManager_NoThresholdsCollectEveryTick(t *testing.T) {
	gc, c := newCounterGC(t, jr_cache.GCConfig{Interval: 5 * time.Millisecond})
	if err := gc.Start(); err != nil {
		t.Fatal(err)
	}
	defer gc.Stop()
	if !waitFor(t, time.Second, func() bool { return c.runs.Load() >= 3 }) {
		t.Fatalf("collected %d times, want at least 3", c.runs.Load())
	}
}

func TestGCManager_MemoryThreshold(t *testing.T) {
	// A threshold of 1 byte is always exceeded, so this fires every tick.
	gc, c := newCounterGC(t, jr_cache.GCConfig{
		Interval:        5 * time.Millisecond,
		MemoryThreshold: 1,
	})
	if err := gc.Start(); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, time.Second, func() bool { return c.runs.Load() >= 2 }) {
		t.Fatal("a 1-byte memory threshold never fired")
	}
	gc.Stop()

	// An unreachable threshold must never fire.
	gc2, c2 := newCounterGC(t, jr_cache.GCConfig{
		Interval:        5 * time.Millisecond,
		MemoryThreshold: 1 << 62,
	})
	if err := gc2.Start(); err != nil {
		t.Fatal(err)
	}
	defer gc2.Stop()
	time.Sleep(60 * time.Millisecond)
	if runs := c2.runs.Load(); runs != 0 {
		t.Fatalf("an unreachable memory threshold fired %d times", runs)
	}
}

func TestGCManager_SetIntervalWhileRunning(t *testing.T) {
	gc, c := newCounterGC(t, jr_cache.GCConfig{Interval: time.Hour})
	if err := gc.Start(); err != nil {
		t.Fatal(err)
	}
	defer gc.Stop()

	time.Sleep(20 * time.Millisecond)
	if c.runs.Load() != 0 {
		t.Fatal("collected before the first tick of a one-hour interval")
	}
	// Shortening the interval must take effect on the running loop.
	if err := gc.SetInterval(5 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, time.Second, func() bool { return c.runs.Load() >= 2 }) {
		t.Fatal("SetInterval did not reset the running ticker")
	}
	if gc.GetInterval() != 5*time.Millisecond {
		t.Fatalf("GetInterval() = %v", gc.GetInterval())
	}
}

func TestGCManager_SettersAndObjectSwap(t *testing.T) {
	gc, first := newCounterGC(t, jr_cache.GCConfig{})
	second := &counter{}
	second.pending.Store(5)

	if gc.GetObject() != first {
		t.Fatal("GetObject did not return the constructed object")
	}
	gc.SetObject(second)
	if gc.GetObject() != second {
		t.Fatal("SetObject did not take effect")
	}
	if n := gc.GCCollect(); n != 5 {
		t.Fatalf("GCCollect() after SetObject = %d, want 5", n)
	}
	if first.runs.Load() != 0 {
		t.Fatal("the old object was collected after SetObject")
	}

	// A nil collector is ignored rather than breaking the manager.
	gc.SetCollector(nil)
	second.pending.Store(3)
	if n := gc.GCCollect(); n != 3 {
		t.Fatalf("GCCollect() after SetCollector(nil) = %d, want 3", n)
	}
	gc.SetCollector(func(*counter) int { return 42 })
	if n := gc.GCCollect(); n != 42 {
		t.Fatalf("GCCollect() after SetCollector = %d, want 42", n)
	}

	date := time.Now().Add(time.Minute)
	gc.SetDateThreshold(date)
	gc.SetTimeThreshold(time.Second)
	gc.SetMemoryThreshold(123)
	if !gc.GetDateThreshold().Equal(date) || gc.GetTimeThreshold() != time.Second || gc.GetMemoryThreshold() != 123 {
		t.Fatal("a setter did not take effect")
	}
}

func TestGCManager_NegativeReleaseCountIsClamped(t *testing.T) {
	c := &counter{}
	gc, err := jr_cache.NewGCManager(c, func(*counter) int { return -5 }, jr_cache.GCConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if n := gc.GCCollect(); n != 0 {
		t.Fatalf("GCCollect() = %d, want 0 for a negative release count", n)
	}
	if stats := gc.Stats(); stats.Released != 0 {
		t.Fatalf("Released = %d, want 0", stats.Released)
	}
}

func TestGCManager_CollectorMayCallBackIn(t *testing.T) {
	// The collect function runs without the manager's lock, so reading the
	// manager from inside it must not deadlock.
	c := &counter{}
	var gc *jr_cache.GCManager[*counter]
	gc, err := jr_cache.NewGCManager(c, func(*counter) int {
		gc.Stats()
		gc.GetInterval()
		gc.SetTimeThreshold(time.Millisecond)
		return 1
	}, jr_cache.GCConfig{Interval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() { done <- gc.GCCollect() }()
	select {
	case n := <-done:
		if n != 1 {
			t.Fatalf("GCCollect() = %d, want 1", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("GCCollect deadlocked when the collector called back into the manager")
	}
}

func TestGCManager_CacheSweepsExpiredEntries(t *testing.T) {
	cm := newCache(t, jr_cache.LRU, 100)
	gc, err := jr_cache.NewCacheGCManager(cm, jr_cache.GCConfig{Interval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}

	cm.SetWithTTL("a", 1, shortTTL)
	cm.SetWithTTL("b", 2, shortTTL)
	cm.Set("c", 3)
	if cm.Len() != 3 {
		t.Fatalf("Len() = %d, want 3", cm.Len())
	}

	if err := gc.Start(); err != nil {
		t.Fatal(err)
	}
	defer gc.Stop()

	if !waitFor(t, 2*time.Second, func() bool { return cm.Len() == 1 }) {
		t.Fatalf("the sweeper left Len() = %d, want 1", cm.Len())
	}
	if !cm.Has("c") {
		t.Fatal("the sweeper removed an entry that had no TTL")
	}
	if stats := gc.Stats(); stats.Released != 2 {
		t.Fatalf("Released = %d, want 2", stats.Released)
	}
}

func TestGCManager_ShardedCacheSweep(t *testing.T) {
	scm := newShardedCache(t, jr_cache.LRU, 100, 4)
	gc, err := jr_cache.NewShardedCacheGCManager(scm, jr_cache.GCConfig{Interval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	scm.SetWithTTL(1, 1, shortTTL)
	scm.SetWithTTL(2, 2, shortTTL)
	scm.Set(3, 3)

	if err := gc.Start(); err != nil {
		t.Fatal(err)
	}
	defer gc.Stop()

	if !waitFor(t, 2*time.Second, func() bool { return scm.Len() == 1 }) {
		t.Fatalf("the sweeper left Len() = %d, want 1", scm.Len())
	}
	if !scm.Has(3) {
		t.Fatal("the sweeper removed an entry that had no TTL")
	}
}

func TestGCManager_ConcurrentUse(t *testing.T) {
	gc, c := newCounterGC(t, jr_cache.GCConfig{Interval: time.Millisecond})
	if err := gc.Start(); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				switch (g + i) % 8 {
				case 0:
					c.pending.Add(1)
					gc.GCCollect()
				case 1:
					gc.Stats()
				case 2:
					gc.SetTimeThreshold(time.Duration(i%3) * time.Millisecond)
				case 3:
					gc.SetMemoryThreshold(uint64(i % 2))
				case 4:
					gc.SetInterval(time.Millisecond)
				case 5:
					gc.SetDateThreshold(time.Now().Add(time.Hour))
				case 6:
					gc.Running()
				case 7:
					gc.GetObject()
				}
			}
		}(g)
	}
	wg.Wait()
	gc.Stop()
	if gc.Running() {
		t.Fatal("Running() is true after Stop")
	}
	if stats := gc.Stats(); stats.Collections == 0 {
		t.Fatal("no collections were recorded")
	}
}

func TestGCManager_StopWithoutStart(t *testing.T) {
	gc, _ := newCounterGC(t, jr_cache.GCConfig{Interval: time.Millisecond})
	gc.Stop() // must not panic or hang
	gc.Stop()
	if gc.Running() {
		t.Fatal("Running() is true on a manager that never started")
	}
}
