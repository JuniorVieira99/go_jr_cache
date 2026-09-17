package bench

import (
	"testing"

	jr_cache "jr_cache/code"
)

type bucketMap = *jr_cache.BucketMap[int, int]

// bucketSpread is how many distinct buckets the populate step uses, so the
// benchmarks exercise the ordered map of buckets rather than one big bucket.
const bucketSpread = 16

func newBucketMap(locked bool) func(size int) bucketMap {
	return func(int) bucketMap {
		return jr_cache.NewBucketMap[int, int](locked)
	}
}

func bucketSet(m bucketMap, key int, value int) {
	m.Set(uint64(key%bucketSpread)+1, key, value)
}

func bucketPopulate(m bucketMap, size int) {
	for k := 0; k < size; k++ {
		bucketSet(m, k, k)
	}
}

func BenchmarkBucketMap(b *testing.B) {
	newMap := newBucketMap(false)
	benchBasics(b, newMap, bucketSet)

	// Bump moves a key to the next bucket: the LFU access pattern.
	b.Run("Bump", func(b *testing.B) {
		perOp(b, newMap, bucketPopulate, func(m bucketMap, i int, size int) {
			key := i % size
			index, _ := m.GetBucketIndex(key)
			m.Set(index+1, key, i)
		})
	})
	// Moving a key to a far-away bucket makes the bucket search walk.
	b.Run("SetFarBucket", func(b *testing.B) {
		perOp(b, newMap, bucketPopulate, func(m bucketMap, i int, size int) {
			m.Set(uint64(i%(bucketSpread*4))+1, i%size, i)
		})
	})
	b.Run("Top", func(b *testing.B) {
		perOp(b, newMap, bucketPopulate, func(m bucketMap, _ int, _ int) {
			m.Top()
		})
	})
	b.Run("GetBucketIndex", func(b *testing.B) {
		perOp(b, newMap, bucketPopulate, func(m bucketMap, i int, size int) {
			m.GetBucketIndex(i % size)
		})
	})
	b.Run("PopTopAll", func(b *testing.B) {
		batch(b, newMap, func(m bucketMap, size int) {
			m.Clear()
			bucketPopulate(m, size)
		}, func(m bucketMap, size int) {
			for k := 0; k < size; k++ {
				m.PopTop()
			}
		})
	})
	b.Run("GetBucketIndices", func(b *testing.B) {
		perOp(b, newMap, bucketPopulate, func(m bucketMap, _ int, _ int) {
			m.GetBucketIndices()
		})
	})
}

func BenchmarkBucketMapLocked(b *testing.B) {
	newMap := newBucketMap(true)
	benchBasics(b, newMap, bucketSet)
	b.Run("Parallel", func(b *testing.B) {
		parallel(b, newMap, bucketPopulate,
			func(m bucketMap, key int) { m.Get(key) },
			bucketSet)
	})
}
