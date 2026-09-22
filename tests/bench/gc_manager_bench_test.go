package bench

import (
	"runtime"
	"runtime/debug"
	"testing"
	"time"

	jr_cache "jr_cache/code"
)

// valueBytes is the size of each cached value. Values large enough to be
// heap-allocated are what makes the collector's work visible; a cache of
// ints would barely allocate at all.
const valueBytes = 256

// gcSizes are the cache capacities the GC benchmarks run at. A bigger cache
// means a bigger live set for the collector to scan on every cycle.
var gcSizes = []int{10_000, 100_000}

// churn is the workload: it overwrites keys with freshly allocated values and
// inserts new keys that evict old ones, so every iteration produces garbage.
func churn(cm *jr_cache.CacheMap[int, []byte], i int, size int) {
	value := make([]byte, valueBytes)
	value[0] = byte(i)
	cm.Set(i%(size*2), value)
	cm.Get(i % size)
}

func newGCBenchCache(size int) *jr_cache.CacheMap[int, []byte] {
	cm, err := jr_cache.NewCacheMap[int, []byte](jr_cache.CacheMapConfig{
		Eviction: jr_cache.LRU, MaxCapacity: uint64(size), Locked: true,
	})
	if err != nil {
		panic(err)
	}
	for k := 0; k < size; k++ {
		cm.Set(k, make([]byte, valueBytes))
	}
	return cm
}

// runChurn times the workload and reports what the collector did during it.
// With disable set it turns the runtime collector off for the timed section
// through the GC manager, and puts it back afterwards.
func runChurn(b *testing.B, size int, disable bool) {
	cm := newGCBenchCache(size)
	manager, err := jr_cache.NewCacheGCManager(cm, jr_cache.GCConfig{Interval: time.Minute})
	if err != nil {
		b.Fatal(err)
	}

	if disable {
		percent, limit := manager.DisableGCCompletely()
		// Always put the process back, even if the benchmark fails: these
		// are global settings and would otherwise leak into later runs.
		defer func() {
			if err := manager.EnableGCCompletely(percent, limit); err != nil {
				b.Error(err)
			}
			runtime.GC()
		}()
	}

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		churn(cm, i, size)
	}
	b.StopTimer()

	runtime.ReadMemStats(&after)
	b.ReportMetric(float64(after.NumGC-before.NumGC), "GCs")
	b.ReportMetric(float64(after.PauseTotalNs-before.PauseTotalNs)/1e6, "pause-ms")
	b.ReportMetric(float64(after.HeapAlloc)/(1<<20), "heap-MB")
	runtime.KeepAlive(cm)
}

// BenchmarkGCEnabledVsDisabled answers whether turning the collector off
// makes the cache faster. Each pair of rows is the same workload with the Go
// collector on and off; compare ns/op, and read the GCs, pause-ms and heap-MB
// metrics next to it for what it costs.
//
// Run it with a short benchtime — with the collector off nothing is ever
// reclaimed, so a long run just grows the heap:
//
//	go test ./tests/bench -run xxx -bench GCEnabledVsDisabled -benchtime=200ms
func BenchmarkGCEnabledVsDisabled(b *testing.B) {
	for _, size := range gcSizes {
		b.Run("enabled/"+itoa(size), func(b *testing.B) { runChurn(b, size, false) })
		b.Run("disabled/"+itoa(size), func(b *testing.B) { runChurn(b, size, true) })
	}
}

// BenchmarkGCPercent shows the middle ground between the two: instead of
// turning the collector off, let it run less often. A higher percentage
// means fewer cycles and more heap.
func BenchmarkGCPercent(b *testing.B) {
	const size = 100_000
	for _, percent := range []int{100, 400, 800} {
		b.Run("percent"+itoa(percent), func(b *testing.B) {
			previous := debug.SetGCPercent(percent)
			defer func() {
				debug.SetGCPercent(previous)
				runtime.GC()
			}()
			runChurn(b, size, false)
		})
	}
}

// BenchmarkSweeperOverhead measures what the manager's own background
// sweeper costs the workload. The cache holds no entries with a TTL, so
// every sweep finds nothing: this is the floor, the price of the ticker and
// the DeleteExpired call itself.
func BenchmarkSweeperOverhead(b *testing.B) {
	const size = 10_000
	for _, interval := range []time.Duration{0, time.Millisecond, 100 * time.Millisecond} {
		name := "off"
		if interval > 0 {
			name = "every-" + interval.String()
		}
		b.Run(name, func(b *testing.B) {
			cm := newGCBenchCache(size)
			if interval > 0 {
				manager, err := jr_cache.NewCacheGCManager(cm, jr_cache.GCConfig{Interval: interval})
				if err != nil {
					b.Fatal(err)
				}
				if err := manager.Start(); err != nil {
					b.Fatal(err)
				}
				defer manager.Stop()
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				churn(cm, i, size)
			}
		})
	}
}

// itoa keeps the sub-benchmark names free of strconv in the hot path.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits [20]byte
	i := len(digits)
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	return string(digits[i:])
}
