package tests

import (
	"runtime"
	"testing"
	"time"

	jr_cache "jr_cache/code"
)

// TestGCManager_NoGoroutineLeak checks that Start/Stop cycles leave no
// goroutines behind. A manager that never stopped its loop, or that lost the
// handle to it, would show up here.
func TestGCManager_NoGoroutineLeak(t *testing.T) {
	runtime.GC()
	time.Sleep(20 * time.Millisecond)
	before := runtime.NumGoroutine()

	for i := 0; i < 50; i++ {
		gc, _ := newCounterGC(t, jr_cache.GCConfig{Interval: time.Millisecond})
		if err := gc.Start(); err != nil {
			t.Fatal(err)
		}
		gc.Stop()
	}

	// Give any straggler a moment to exit.
	for i := 0; i < 50 && runtime.NumGoroutine() > before; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before+2 {
		t.Fatalf("goroutines: %d before, %d after 50 start/stop cycles", before, after)
	}
}
