package bench

import (
	"testing"
	"time"

	jr_cache "jr_cache/code"
)

type cacheMap = *jr_cache.CacheMap[int, int]

// cachePolicies are the policies benchmarked. FIFO/LIFO share the ordered
// map path with LRU/MRU minus the move, and MFU mirrors LFU, so one of each
// family is enough to see the difference.
var cachePolicies = []jr_cache.EvictionPolicy{jr_cache.LRU, jr_cache.LFU}

// newCacheMap builds a cache whose capacity equals the benchmark size, so
// populating never evicts and the eviction benchmarks evict on every set.
func newCacheMap(policy jr_cache.EvictionPolicy, locked bool) func(size int) cacheMap {
	return func(size int) cacheMap {
		cm, err := jr_cache.NewCacheMap[int, int](jr_cache.CacheMapConfig{
			Eviction:    policy,
			MaxCapacity: uint64(size),
			Locked:      locked,
		})
		if err != nil {
			panic(err)
		}
		return cm
	}
}

func cacheSet(m cacheMap, key int, value int) {
	m.Set(key, value)
}

func cachePopulate(m cacheMap, size int) {
	for k := 0; k < size; k++ {
		m.Set(k, k)
	}
}

func benchCacheMap(b *testing.B, newMap func(size int) cacheMap) {
	benchBasics(b, newMap, cacheSet)

	// Every set of a new key on a full cache evicts one entry.
	b.Run("SetEvicting", func(b *testing.B) {
		perOp(b, newMap, cachePopulate, func(m cacheMap, i int, size int) {
			m.Set(size+i, i)
		})
	})
	b.Run("SetOrGet", func(b *testing.B) {
		perOp(b, newMap, cachePopulate, func(m cacheMap, i int, size int) {
			m.SetOrGet(i%size, i)
		})
	})
	b.Run("SetWithTTL", func(b *testing.B) {
		perOp(b, newMap, cachePopulate, func(m cacheMap, i int, size int) {
			m.SetWithTTL(i%size, i, time.Hour)
		})
	})
	// Get on a key that carries a TTL pays the expiry check.
	b.Run("GetWithTTL", func(b *testing.B) {
		perOp(b, newMap, func(m cacheMap, size int) {
			for k := 0; k < size; k++ {
				m.SetWithTTL(k, k, time.Hour)
			}
		}, func(m cacheMap, i int, size int) {
			m.Get(i % size)
		})
	})
	b.Run("Top", func(b *testing.B) {
		perOp(b, newMap, cachePopulate, func(m cacheMap, _ int, _ int) {
			m.Top()
		})
	})
	b.Run("DeleteExpired", func(b *testing.B) {
		batch(b, newMap, func(m cacheMap, size int) {
			m.Clear()
			for k := 0; k < size; k++ {
				// Half the entries are already expired, half never will be.
				if k%2 == 0 {
					m.SetWithTTL(k, k, 0)
				} else {
					m.SetWithTTL(k, k, time.Hour)
				}
			}
		}, func(m cacheMap, _ int) {
			m.DeleteExpired()
		})
	})
}

func BenchmarkCacheMap(b *testing.B) {
	for _, policy := range cachePolicies {
		b.Run(policy.String(), func(b *testing.B) {
			benchCacheMap(b, newCacheMap(policy, false))
		})
	}
}

// BenchmarkCacheMapLocked is the single-lock cache under contention; compare
// with BenchmarkShardedCacheMap/Parallel.
func BenchmarkCacheMapLocked(b *testing.B) {
	for _, policy := range cachePolicies {
		b.Run(policy.String(), func(b *testing.B) {
			newMap := newCacheMap(policy, true)
			benchCacheMap(b, newMap)
			b.Run("Parallel", func(b *testing.B) {
				parallel(b, newMap, cachePopulate,
					func(m cacheMap, key int) { m.Get(key) },
					cacheSet)
			})
		})
	}
}
