package bench

import (
	"testing"
	"time"

	jr_cache "jr_cache/code"
)

type shardedCacheMap = *jr_cache.ShardedCacheMap[int, int]

// shardCount is used by every sharded benchmark.
const shardCount = 16

// newShardedCacheMap builds a locked sharded cache with capacity equal to the
// benchmark size, spread over shardCount shards, using the default hash.
func newShardedCacheMap(policy jr_cache.EvictionPolicy) func(size int) shardedCacheMap {
	return newShardedCacheMapMode(policy, false)
}

// newShardedCacheMapMode is newShardedCacheMap with a choice of per-shard or
// global order.
func newShardedCacheMapMode(policy jr_cache.EvictionPolicy, global bool) func(size int) shardedCacheMap {
	return func(size int) shardedCacheMap {
		scm, err := jr_cache.NewShardedCacheMap[int, int](jr_cache.ShardedCacheMapConfig[int]{
			Eviction:    policy,
			MaxCapacity: uint64(size),
			ShardCount:  shardCount,
			Locked:      true,
			GlobalOrder: global,
		})
		if err != nil {
			panic(err)
		}
		return scm
	}
}

func shardedCacheSet(m shardedCacheMap, key int, value int) {
	m.Set(key, value)
}

func shardedCachePopulate(m shardedCacheMap, size int) {
	for k := 0; k < size; k++ {
		m.Set(k, k)
	}
}

func BenchmarkShardedCacheMap(b *testing.B) {
	for _, policy := range cachePolicies {
		b.Run(policy.String(), func(b *testing.B) {
			newMap := newShardedCacheMap(policy)
			benchBasics(b, newMap, shardedCacheSet)

			b.Run("SetEvicting", func(b *testing.B) {
				perOp(b, newMap, shardedCachePopulate, func(m shardedCacheMap, i int, size int) {
					m.Set(size+i, i)
				})
			})
			b.Run("SetOrGet", func(b *testing.B) {
				perOp(b, newMap, shardedCachePopulate, func(m shardedCacheMap, i int, size int) {
					m.SetOrGet(i%size, i)
				})
			})
			b.Run("SetWithTTL", func(b *testing.B) {
				perOp(b, newMap, shardedCachePopulate, func(m shardedCacheMap, i int, size int) {
					m.SetWithTTL(i%size, i, time.Hour)
				})
			})
			// Top has to find the fullest shard first.
			b.Run("Top", func(b *testing.B) {
				perOp(b, newMap, shardedCachePopulate, func(m shardedCacheMap, _ int, _ int) {
					m.Top()
				})
			})
			// Len sums every shard.
			b.Run("Len", func(b *testing.B) {
				perOp(b, newMap, shardedCachePopulate, func(m shardedCacheMap, _ int, _ int) {
					m.Len()
				})
			})
			// Populate with a worker per shard versus the plain loop.
			b.Run("PopulateThreaded", func(b *testing.B) {
				batch(b, newMap, func(m shardedCacheMap, _ int) { m.Clear() }, func(m shardedCacheMap, size int) {
					entries := make(map[int]int, size)
					for k := 0; k < size; k++ {
						entries[k] = k
					}
					m.PopulateThreaded(entries, shardCount)
				})
			})
			b.Run("Parallel", func(b *testing.B) {
				parallel(b, newMap, shardedCachePopulate,
					func(m shardedCacheMap, key int) { m.Get(key) },
					shardedCacheSet)
			})
		})
	}
}

// BenchmarkShardedCacheMapGlobal is the sharded cache with one shared order:
// exact eviction across shards at the cost of a lock every operation takes.
func BenchmarkShardedCacheMapGlobal(b *testing.B) {
	for _, policy := range cachePolicies {
		b.Run(policy.String(), func(b *testing.B) {
			newMap := newShardedCacheMapMode(policy, true)
			benchBasics(b, newMap, shardedCacheSet)

			b.Run("SetEvicting", func(b *testing.B) {
				perOp(b, newMap, shardedCachePopulate, func(m shardedCacheMap, i int, size int) {
					m.Set(size+i, i)
				})
			})
			b.Run("SetOrGet", func(b *testing.B) {
				perOp(b, newMap, shardedCachePopulate, func(m shardedCacheMap, i int, size int) {
					m.SetOrGet(i%size, i)
				})
			})
			b.Run("SetWithTTL", func(b *testing.B) {
				perOp(b, newMap, shardedCachePopulate, func(m shardedCacheMap, i int, size int) {
					m.SetWithTTL(i%size, i, time.Hour)
				})
			})
			// With a global order Top and Len are exact and O(1).
			b.Run("Top", func(b *testing.B) {
				perOp(b, newMap, shardedCachePopulate, func(m shardedCacheMap, _ int, _ int) {
					m.Top()
				})
			})
			b.Run("Len", func(b *testing.B) {
				perOp(b, newMap, shardedCachePopulate, func(m shardedCacheMap, _ int, _ int) {
					m.Len()
				})
			})
			b.Run("PopulateThreaded", func(b *testing.B) {
				batch(b, newMap, func(m shardedCacheMap, _ int) { m.Clear() }, func(m shardedCacheMap, size int) {
					entries := make(map[int]int, size)
					for k := 0; k < size; k++ {
						entries[k] = k
					}
					m.PopulateThreaded(entries, shardCount)
				})
			})
			b.Run("Parallel", func(b *testing.B) {
				parallel(b, newMap, shardedCachePopulate,
					func(m shardedCacheMap, key int) { m.Get(key) },
					shardedCacheSet)
			})
		})
	}
}
