package tests

import (
	"math"
	"runtime/debug"
	"testing"
	"time"

	jr_cache "jr_cache/code"
)

// currentGCSettings reads the process-wide GC settings without changing them:
// SetGCPercent has to be called twice (there is no read-only form), and
// SetMemoryLimit returns the current limit for any negative input.
func currentGCSettings() (int, int64) {
	percent := debug.SetGCPercent(-1)
	debug.SetGCPercent(percent)
	return percent, debug.SetMemoryLimit(-1)
}

// restoreGCSettings puts the process back the way the test found it, so a
// failure in one test cannot leave the collector off for the whole run.
func restoreGCSettings(t *testing.T, percent int, limit int64) {
	t.Helper()
	debug.SetGCPercent(percent)
	debug.SetMemoryLimit(limit)
}

func TestGCManager_DisableEnableRoundTrip(t *testing.T) {
	percent, limit := currentGCSettings()
	defer restoreGCSettings(t, percent, limit)

	gc, _ := newCounterGC(t, jr_cache.GCConfig{Interval: 5 * time.Millisecond})
	if gc.GCDisabled() {
		t.Fatal("GCDisabled() is true on a fresh manager")
	}

	gotPercent, gotLimit := gc.DisableGCCompletely()
	if gotPercent != percent || gotLimit != limit {
		t.Fatalf("DisableGCCompletely returned %d, %d; want the previous %d, %d", gotPercent, gotLimit, percent, limit)
	}
	if !gc.GCDisabled() {
		t.Fatal("GCDisabled() is false after DisableGCCompletely")
	}
	// The runtime collector must actually be off.
	nowPercent, nowLimit := currentGCSettings()
	if nowPercent != -1 {
		t.Fatalf("GC percent = %d after disabling, want -1", nowPercent)
	}
	if nowLimit != math.MaxInt64 {
		t.Fatalf("memory limit = %d after disabling, want math.MaxInt64", nowLimit)
	}

	if err := gc.EnableGCCompletely(gotPercent, gotLimit); err != nil {
		t.Fatalf("EnableGCCompletely error: %v", err)
	}
	if gc.GCDisabled() {
		t.Fatal("GCDisabled() is true after EnableGCCompletely")
	}
	backPercent, backLimit := currentGCSettings()
	if backPercent != percent || backLimit != limit {
		t.Fatalf("after restoring: %d, %d; want %d, %d", backPercent, backLimit, percent, limit)
	}
	gc.Stop()
}

func TestGCManager_DisableStopsAndEnableRestartsTheSweeper(t *testing.T) {
	percent, limit := currentGCSettings()
	defer restoreGCSettings(t, percent, limit)

	gc, c := newCounterGC(t, jr_cache.GCConfig{Interval: 5 * time.Millisecond})
	if err := gc.Start(); err != nil {
		t.Fatal(err)
	}
	defer gc.Stop()
	if !waitFor(t, time.Second, func() bool { return c.runs.Load() >= 1 }) {
		t.Fatal("the sweeper never ran")
	}

	p, l := gc.DisableGCCompletely()
	if gc.Running() {
		t.Fatal("the sweeper is still running after DisableGCCompletely")
	}
	after := c.runs.Load()
	time.Sleep(40 * time.Millisecond)
	if c.runs.Load() != after {
		t.Fatalf("the sweeper kept collecting after being disabled: %d then %d", after, c.runs.Load())
	}

	if err := gc.EnableGCCompletely(p, l); err != nil {
		t.Fatal(err)
	}
	if !gc.Running() {
		t.Fatal("the sweeper was not restarted by EnableGCCompletely")
	}
	if !waitFor(t, time.Second, func() bool { return c.runs.Load() > after }) {
		t.Fatal("the restarted sweeper never collected")
	}
}

func TestGCManager_EnableDoesNotStartASweeperThatWasNotRunning(t *testing.T) {
	percent, limit := currentGCSettings()
	defer restoreGCSettings(t, percent, limit)

	// The sweeper was never started, so restoring the collector must not
	// start one behind the caller's back.
	gc, c := newCounterGC(t, jr_cache.GCConfig{Interval: 5 * time.Millisecond})
	p, l := gc.DisableGCCompletely()
	if err := gc.EnableGCCompletely(p, l); err != nil {
		t.Fatalf("EnableGCCompletely error: %v", err)
	}
	if gc.Running() {
		t.Fatal("EnableGCCompletely started a sweeper that had never run")
	}
	time.Sleep(30 * time.Millisecond)
	if runs := c.runs.Load(); runs != 0 {
		t.Fatalf("a sweeper that was never started collected %d times", runs)
	}
}

func TestGCManager_EnableReportsStartErrors(t *testing.T) {
	percent, limit := currentGCSettings()
	defer restoreGCSettings(t, percent, limit)

	gc, _ := newCounterGC(t, jr_cache.GCConfig{Interval: 5 * time.Millisecond})
	if err := gc.Start(); err != nil {
		t.Fatal(err)
	}
	p, l := gc.DisableGCCompletely()

	// Clearing the interval makes the restart impossible; the error must
	// reach the caller instead of being swallowed.
	if err := gc.SetInterval(-1); err == nil {
		t.Fatal("SetInterval(-1) should have been rejected")
	}
	gc2, _ := newCounterGC(t, jr_cache.GCConfig{})
	gc2.DisableGCCompletely()
	if err := gc2.EnableGCCompletely(p, l); err != nil {
		t.Fatalf("a manager whose sweeper was never running should restore cleanly: %v", err)
	}

	if err := gc.EnableGCCompletely(p, l); err != nil {
		t.Fatalf("EnableGCCompletely error: %v", err)
	}
	gc.Stop()
}

func TestGCManager_DisableIsIdempotent(t *testing.T) {
	percent, limit := currentGCSettings()
	defer restoreGCSettings(t, percent, limit)

	gc, _ := newCounterGC(t, jr_cache.GCConfig{Interval: 5 * time.Millisecond})
	if err := gc.Start(); err != nil {
		t.Fatal(err)
	}

	firstPercent, firstLimit := gc.DisableGCCompletely()
	// A second call must return the original settings, not "already off".
	secondPercent, secondLimit := gc.DisableGCCompletely()
	if secondPercent != firstPercent || secondLimit != firstLimit {
		t.Fatalf("second DisableGCCompletely returned %d, %d; want the original %d, %d",
			secondPercent, secondLimit, firstPercent, firstLimit)
	}
	if secondPercent == -1 {
		t.Fatal("the saved GC percentage was overwritten with the disabled value")
	}

	if err := gc.RestoreGC(); err != nil {
		t.Fatal(err)
	}
	backPercent, backLimit := currentGCSettings()
	if backPercent != percent || backLimit != limit {
		t.Fatalf("after RestoreGC: %d, %d; want %d, %d", backPercent, backLimit, percent, limit)
	}
	if !gc.Running() {
		t.Fatal("RestoreGC did not restart the sweeper")
	}
	gc.Stop()
}

func TestGCManager_RestoreGC(t *testing.T) {
	percent, limit := currentGCSettings()
	defer restoreGCSettings(t, percent, limit)

	gc, _ := newCounterGC(t, jr_cache.GCConfig{Interval: 5 * time.Millisecond})

	// Restoring without having disabled anything is a no-op, not a reset of
	// the process GC settings to zero values.
	if err := gc.RestoreGC(); err != nil {
		t.Fatalf("RestoreGC on an untouched manager: %v", err)
	}
	stillPercent, stillLimit := currentGCSettings()
	if stillPercent != percent || stillLimit != limit {
		t.Fatalf("RestoreGC changed the settings to %d, %d; want %d, %d untouched",
			stillPercent, stillLimit, percent, limit)
	}

	gc.DisableGCCompletely()
	if err := gc.RestoreGC(); err != nil {
		t.Fatal(err)
	}
	if gc.GCDisabled() {
		t.Fatal("GCDisabled() is true after RestoreGC")
	}
	// Restoring twice is harmless.
	if err := gc.RestoreGC(); err != nil {
		t.Fatal(err)
	}
	backPercent, backLimit := currentGCSettings()
	if backPercent != percent || backLimit != limit {
		t.Fatalf("after a second RestoreGC: %d, %d; want %d, %d", backPercent, backLimit, percent, limit)
	}
}

func TestGCManager_DisableDoesNotDeadlock(t *testing.T) {
	percent, limit := currentGCSettings()
	defer restoreGCSettings(t, percent, limit)

	// Disable stops the sweeper and Enable starts it again; both take the
	// manager's lock internally, so a naive implementation deadlocks here.
	gc, _ := newCounterGC(t, jr_cache.GCConfig{Interval: 5 * time.Millisecond})
	if err := gc.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		p, l := gc.DisableGCCompletely()
		done <- gc.EnableGCCompletely(p, l)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("round trip error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Disable/Enable deadlocked")
	}
	gc.Stop()
}

func TestGCManager_DisableWithCacheSweeper(t *testing.T) {
	percent, limit := currentGCSettings()
	defer restoreGCSettings(t, percent, limit)

	cm := newCache(t, jr_cache.LRU, 100)
	gc, err := jr_cache.NewCacheGCManager(cm, jr_cache.GCConfig{Interval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := gc.Start(); err != nil {
		t.Fatal(err)
	}

	p, l := gc.DisableGCCompletely()
	cm.SetWithTTL("a", 1, shortTTL)
	waitForExpiry()
	// The sweeper is off, so the expired entry is still taking up its slot.
	if cm.Len() != 1 {
		t.Fatalf("Len() = %d while the sweeper is disabled, want 1", cm.Len())
	}

	if err := gc.EnableGCCompletely(p, l); err != nil {
		t.Fatal(err)
	}
	defer gc.Stop()
	if !waitFor(t, 2*time.Second, func() bool { return cm.Len() == 0 }) {
		t.Fatalf("the restarted sweeper left Len() = %d, want 0", cm.Len())
	}
}

func TestGCManager_DisableEnableUnderConcurrency(t *testing.T) {
	percent, limit := currentGCSettings()
	defer restoreGCSettings(t, percent, limit)

	gc, _ := newCounterGC(t, jr_cache.GCConfig{Interval: time.Millisecond})
	if err := gc.Start(); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	for g := 0; g < 4; g++ {
		go func(g int) {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 100; i++ {
				switch (g + i) % 4 {
				case 0:
					p, l := gc.DisableGCCompletely()
					gc.EnableGCCompletely(p, l)
				case 1:
					gc.GCDisabled()
				case 2:
					gc.RestoreGC()
				case 3:
					gc.Stats()
				}
			}
		}(g)
	}
	for g := 0; g < 4; g++ {
		<-done
	}
	// Whatever order they ran in, the collector must end up on again.
	if err := gc.RestoreGC(); err != nil {
		t.Fatal(err)
	}
	if gc.GCDisabled() {
		t.Fatal("GCDisabled() is true after the final RestoreGC")
	}
	if p, _ := currentGCSettings(); p == -1 {
		t.Fatal("the process was left with the collector disabled")
	}
	gc.Stop()
}
