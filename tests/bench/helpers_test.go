// Package bench holds the benchmarks for every map in jr_cache/code.
//
// Run everything with
//
//	go test ./tests/bench -bench . -benchmem
//
// or a single module / operation / size with a regexp, for example
//
//	go test ./tests/bench -bench 'BenchmarkCacheMap/LRU/Get/10000'
//
// Batch benchmarks (Populate, DeleteAll, PopAll) do one pass over all entries
// per iteration and additionally report ns/entry; the others time one
// operation against a map already holding the given number of entries.
package bench

import (
	"strconv"
	"sync/atomic"
	"testing"
)

// sizes is the number of entries each benchmark is run against.
var sizes = []int{500, 1000, 5000, 10000, 50000}

// basicMap is the subset of the shared API every module implements.
type basicMap interface {
	Get(key int) (int, bool)
	Has(key int) bool
	Update(key int, value int) bool
	Pop(key int) (int, bool)
	Delete(key int)
	Len() int
	Clear()
}

// forSizes runs fn as a sub-benchmark named after each size.
func forSizes(b *testing.B, fn func(b *testing.B, size int)) {
	for _, size := range sizes {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			fn(b, size)
		})
	}
}

// perOp prepopulates a map with size entries and times op once per iteration.
func perOp[M any](b *testing.B, newMap func(size int) M, populate func(m M, size int), op func(m M, i int, size int)) {
	forSizes(b, func(b *testing.B, size int) {
		m := newMap(size)
		populate(m, size)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			op(m, i, size)
		}
	})
}

// batch times fn over all size entries once per iteration. setup runs
// untimed before every iteration so fn always starts from the same state.
func batch[M any](b *testing.B, newMap func(size int) M, setup func(m M, size int), fn func(m M, size int)) {
	forSizes(b, func(b *testing.B, size int) {
		m := newMap(size)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			setup(m, size)
			b.StartTimer()
			fn(m, size)
		}
		b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(size), "ns/entry")
	})
}

// parallel prepopulates a map and hammers it from GOMAXPROCS goroutines,
// each walking the key space from its own offset so they do not all touch
// the same keys at once. Every fourth operation is a set, the rest are gets.
func parallel[M any](b *testing.B, newMap func(size int) M, populate func(m M, size int), get func(m M, key int), set func(m M, key int, value int)) {
	forSizes(b, func(b *testing.B, size int) {
		m := newMap(size)
		populate(m, size)
		var next int64
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			i := int(atomic.AddInt64(&next, int64(size)/7))
			for pb.Next() {
				key := i % size
				if i%4 == 0 {
					set(m, key, i)
				} else {
					get(m, key)
				}
				i++
			}
		})
	})
}

// benchBasics runs the operations shared by every module: a full populate,
// steady-state Get / Has / Set / Update, and a full Pop and Delete.
func benchBasics[M basicMap](b *testing.B, newMap func(size int) M, set func(m M, key int, value int)) {
	populate := func(m M, size int) {
		for k := 0; k < size; k++ {
			set(m, k, k)
		}
	}
	clearAndPopulate := func(m M, size int) {
		m.Clear()
		populate(m, size)
	}

	b.Run("Populate", func(b *testing.B) {
		batch(b, newMap, func(m M, _ int) { m.Clear() }, populate)
	})
	b.Run("Get", func(b *testing.B) {
		perOp(b, newMap, populate, func(m M, i int, size int) { m.Get(i % size) })
	})
	b.Run("Has", func(b *testing.B) {
		perOp(b, newMap, populate, func(m M, i int, size int) { m.Has(i % size) })
	})
	b.Run("Set", func(b *testing.B) {
		perOp(b, newMap, populate, func(m M, i int, size int) { set(m, i%size, i) })
	})
	b.Run("Update", func(b *testing.B) {
		perOp(b, newMap, populate, func(m M, i int, size int) { m.Update(i%size, i) })
	})
	b.Run("PopAll", func(b *testing.B) {
		batch(b, newMap, clearAndPopulate, func(m M, size int) {
			for k := 0; k < size; k++ {
				m.Pop(k)
			}
		})
	})
	b.Run("DeleteAll", func(b *testing.B) {
		batch(b, newMap, clearAndPopulate, func(m M, size int) {
			for k := 0; k < size; k++ {
				m.Delete(k)
			}
		})
	})
}
