package bench

import (
	"testing"

	jr_cache "jr_cache/code"
)

type shardedMap = *jr_cache.ShardedMap[int, int]

// newShardedMap builds a sharded map over shardCount shards with the default hash.
func newShardedMap(int) shardedMap {
	sm, err := jr_cache.NewShardedMap[int, int](jr_cache.ShardedMapConfig[int]{ShardCount: shardCount})
	if err != nil {
		panic(err)
	}
	return sm
}

func shardedSet(m shardedMap, key int, value int) {
	m.Set(key, value)
}

func shardedPopulate(m shardedMap, size int) {
	for k := 0; k < size; k++ {
		m.Set(k, k)
	}
}

func BenchmarkShardedMap(b *testing.B) {
	benchBasics(b, newShardedMap, shardedSet)

	b.Run("SetOrGet", func(b *testing.B) {
		perOp(b, newShardedMap, shardedPopulate, func(m shardedMap, i int, size int) {
			m.SetOrGet(i%size, i)
		})
	})
	b.Run("Modify", func(b *testing.B) {
		perOp(b, newShardedMap, shardedPopulate, func(m shardedMap, i int, size int) {
			m.Modify(i%size, func(v int, _ bool) (int, bool) { return v + 1, true })
		})
	})
	b.Run("ShardIndex", func(b *testing.B) {
		perOp(b, newShardedMap, shardedPopulate, func(m shardedMap, i int, size int) {
			m.ShardIndex(i % size)
		})
	})
	b.Run("Len", func(b *testing.B) {
		perOp(b, newShardedMap, shardedPopulate, func(m shardedMap, _ int, _ int) {
			m.Len()
		})
	})
	// Range snapshots every shard before visiting it.
	b.Run("Range", func(b *testing.B) {
		batch(b, newShardedMap, func(m shardedMap, size int) {
			if m.Len() != size {
				shardedPopulate(m, size)
			}
		}, func(m shardedMap, _ int) {
			m.Range(func(int, int) bool { return true })
		})
	})
	// Populate with a worker per shard versus the plain loop above.
	b.Run("PopulateThreaded", func(b *testing.B) {
		batch(b, newShardedMap, func(m shardedMap, _ int) { m.Clear() }, func(m shardedMap, size int) {
			entries := make(map[int]int, size)
			for k := 0; k < size; k++ {
				entries[k] = k
			}
			m.Populate(entries, shardCount)
		})
	})
	b.Run("Parallel", func(b *testing.B) {
		parallel(b, newShardedMap, shardedPopulate,
			func(m shardedMap, key int) { m.Get(key) },
			shardedSet)
	})
}
