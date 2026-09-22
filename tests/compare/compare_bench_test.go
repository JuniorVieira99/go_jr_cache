package compare

import (
	"math/rand"
	"strconv"
	"sync/atomic"
	"testing"
)

// capacities are the cache sizes every benchmark is run at.
var capacities = []int{10_000, 100_000}

// keySpace is how many distinct keys the workloads draw from, as a multiple
// of the capacity: with 2× the cache can hold at most half the working set.
const keySpace = 2

// traceLen is the length of the pre-computed access trace.
const traceLen = 1 << 20

// hitRatioCapacity and hitRatioSpace size the hit-ratio test. The capacity
// is large enough that bigcache's whole-megabyte hard limit is a fair bound.
const (
	hitRatioCapacity = 100_000
	hitRatioSpace    = hitRatioCapacity * 10
)

// keys holds pre-built key strings so no benchmark pays for formatting; it
// is sized for the largest workload.
var keys = func() []string {
	n := max(capacities[len(capacities)-1]*keySpace, hitRatioSpace)
	ks := make([]string, n)
	for i := range ks {
		ks[i] = "key:" + strconv.Itoa(i)
	}
	return ks
}()

// zipfTrace returns traceLen key indices in [0, n) drawn from a Zipf
// distribution (s = 1.01), the usual stand-in for a skewed real workload.
// The same seed makes every library see the identical sequence.
func zipfTrace(n int) []int32 {
	r := rand.New(rand.NewSource(42))
	z := rand.NewZipf(r, 1.01, 1, uint64(n-1))
	trace := make([]int32, traceLen)
	for i := range trace {
		trace[i] = int32(z.Uint64())
	}
	return trace
}

// uniformTrace returns traceLen key indices in [0, n) drawn uniformly.
func uniformTrace(n int) []int32 {
	r := rand.New(rand.NewSource(7))
	trace := make([]int32, traceLen)
	for i := range trace {
		trace[i] = int32(r.Intn(n))
	}
	return trace
}

func populate(c cache, n int) {
	for i := 0; i < n; i++ {
		c.Set(keys[i], i)
	}
	c.Sync()
}

// forCandidates runs fn as a sub-benchmark per library and capacity.
func forCandidates(b *testing.B, boundedOnly bool, fn func(b *testing.B, c cache, n int)) {
	for _, cand := range candidates {
		if boundedOnly && !cand.bounded {
			continue
		}
		b.Run(cand.name, func(b *testing.B) {
			for _, n := range capacities {
				b.Run(strconv.Itoa(n), func(b *testing.B) {
					c := cand.build(n)
					defer c.Close()
					fn(b, c, n)
				})
			}
		})
	}
}

// BenchmarkGetHit reads keys that are all present, one goroutine.
func BenchmarkGetHit(b *testing.B) {
	forCandidates(b, false, func(b *testing.B, c cache, n int) {
		populate(c, n)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			c.Get(keys[i%n])
		}
	})
}

// BenchmarkGetMiss reads keys that are all absent, one goroutine.
func BenchmarkGetMiss(b *testing.B) {
	forCandidates(b, false, func(b *testing.B, c cache, n int) {
		populate(c, n)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			c.Get(keys[n+i%n])
		}
	})
}

// BenchmarkSetOverwrite writes keys that are all present, one goroutine.
func BenchmarkSetOverwrite(b *testing.B) {
	forCandidates(b, false, func(b *testing.B, c cache, n int) {
		populate(c, n)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			c.Set(keys[i%n], i)
		}
	})
}

// BenchmarkSetEvict scans sequentially through twice the capacity, so on a
// full cache every write is a new key that has to evict one (or be refused
// by an admission policy). Unbounded maps are skipped: they would only grow.
func BenchmarkSetEvict(b *testing.B) {
	forCandidates(b, true, func(b *testing.B, c cache, n int) {
		populate(c, n)
		span := n * keySpace
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			c.Set(keys[(n+i)%span], i)
		}
	})
}

// BenchmarkMixedZipf is the closest thing to a real workload: a Zipf trace
// over twice the capacity, 90 % reads and 10 % writes, one goroutine.
func BenchmarkMixedZipf(b *testing.B) {
	traces := map[int][]int32{}
	for _, n := range capacities {
		traces[n] = zipfTrace(n * keySpace)
	}
	forCandidates(b, false, func(b *testing.B, c cache, n int) {
		trace := traces[n]
		populate(c, n)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			k := keys[trace[i%traceLen]]
			if i%10 == 0 {
				c.Set(k, i)
			} else {
				c.Get(k)
			}
		}
	})
}

// BenchmarkMixedZipfParallel is BenchmarkMixedZipf from GOMAXPROCS goroutines,
// each starting at its own offset into the trace.
func BenchmarkMixedZipfParallel(b *testing.B) {
	traces := map[int][]int32{}
	for _, n := range capacities {
		traces[n] = zipfTrace(n * keySpace)
	}
	forCandidates(b, false, func(b *testing.B, c cache, n int) {
		trace := traces[n]
		populate(c, n)
		var next int64
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			i := int(atomic.AddInt64(&next, traceLen/13))
			for pb.Next() {
				k := keys[trace[i%traceLen]]
				if i%10 == 0 {
					c.Set(k, i)
				} else {
					c.Get(k)
				}
				i++
			}
		})
	})
}

// BenchmarkGetHitParallel reads present keys from GOMAXPROCS goroutines:
// pure read scaling.
func BenchmarkGetHitParallel(b *testing.B) {
	forCandidates(b, false, func(b *testing.B, c cache, n int) {
		populate(c, n)
		var next int64
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			i := int(atomic.AddInt64(&next, int64(n)/13))
			for pb.Next() {
				c.Get(keys[i%n])
				i++
			}
		})
	})
}

// TestHitRatio replays a Zipf and a uniform trace over a key space ten times
// the capacity, reading each key and writing it on a miss, and reports the
// hit ratio every bounded library achieves. This measures the eviction
// policy, not speed. Run with: go test -run HitRatio -v
func TestHitRatio(t *testing.T) {
	type workload struct {
		name  string
		trace []int32
	}
	for _, capacity := range []int{10_000, hitRatioCapacity} {
		space := capacity * 10
		workloads := []workload{
			{"zipf", zipfTrace(space)},
			{"uniform", uniformTrace(space)},
		}
		t.Logf("hit ratio, capacity %d, key space %d, %d requests", capacity, space, traceLen)
		t.Logf("%-32s %8s %8s", "library", "zipf", "uniform")
		for _, cand := range candidates {
			if !cand.bounded {
				continue
			}
			ratios := make([]float64, len(workloads))
			for w, wl := range workloads {
				c := cand.build(capacity)
				hits := 0
				for i, idx := range wl.trace {
					k := keys[idx]
					if _, ok := c.Get(k); ok {
						hits++
					} else {
						c.Set(k, i)
						c.Sync()
					}
				}
				c.Close()
				ratios[w] = float64(hits) / float64(len(wl.trace))
			}
			note := ""
			if cand.approximate {
				note = " (byte budget, approximate capacity)"
			}
			t.Logf("%-32s %7.2f%% %7.2f%%%s", cand.name, ratios[0]*100, ratios[1]*100, note)
		}
	}
}

// TestAdapters sanity-checks every adapter round-trips a value.
func TestAdapters(t *testing.T) {
	for _, cand := range candidates {
		c := cand.build(1000)
		c.Set("k", 42)
		c.Sync()
		if v, ok := c.Get("k"); !ok || v != 42 {
			t.Errorf("%s: Get after Set = %d, %v; want 42, true", cand.name, v, ok)
		}
		if _, ok := c.Get("missing"); ok {
			t.Errorf("%s: Get on a missing key reported a value", cand.name)
		}
		c.Close()
	}
}
