package bench

import (
	"testing"

	jr_cache "jr_cache/code"
)

type orderedMap = *jr_cache.OrderedMap[int, int]

func newOrderedMap(locked bool) func(size int) orderedMap {
	return func(int) orderedMap {
		return jr_cache.NewOrderedMap[int, int](locked)
	}
}

func orderedSet(m orderedMap, key int, value int) {
	m.SetBottom(key, value)
}

func orderedPopulate(m orderedMap, size int) {
	for k := 0; k < size; k++ {
		m.SetBottom(k, k)
	}
}

func BenchmarkOrderedMap(b *testing.B) {
	newMap := newOrderedMap(false)
	benchBasics(b, newMap, orderedSet)

	// Moving an existing key to either end is the LRU access pattern.
	b.Run("MoveToBottom", func(b *testing.B) {
		perOp(b, newMap, orderedPopulate, func(m orderedMap, i int, size int) {
			m.MoveToBottom(i % size)
		})
	})
	b.Run("SetTop", func(b *testing.B) {
		perOp(b, newMap, orderedPopulate, func(m orderedMap, i int, size int) {
			m.SetTop(i%size, i)
		})
	})
	// Positional insert next to an arbitrary anchor.
	b.Run("SetAfter", func(b *testing.B) {
		perOp(b, newMap, orderedPopulate, func(m orderedMap, i int, size int) {
			m.SetAfter((i*7)%size, i%size, i)
		})
	})
	// PopTop + SetBottom keeps the size constant, like a queue being cycled.
	b.Run("Rotate", func(b *testing.B) {
		perOp(b, newMap, orderedPopulate, func(m orderedMap, i int, size int) {
			key, value, _ := m.PopTop()
			m.SetBottom(key, value)
		})
	})
	b.Run("Top", func(b *testing.B) {
		perOp(b, newMap, orderedPopulate, func(m orderedMap, _ int, _ int) {
			m.Top()
		})
	})
	b.Run("PopTopAll", func(b *testing.B) {
		batch(b, newMap, func(m orderedMap, size int) {
			m.Clear()
			orderedPopulate(m, size)
		}, func(m orderedMap, size int) {
			for k := 0; k < size; k++ {
				m.PopTop()
			}
		})
	})
	b.Run("Walk", func(b *testing.B) {
		batch(b, newMap, func(m orderedMap, size int) {
			if m.Len() != size {
				orderedPopulate(m, size)
			}
		}, func(m orderedMap, _ int) {
			for k, ok := m.TopKey(); ok; k, ok = m.NextKey(k) {
			}
		})
	})
}

// BenchmarkOrderedMapLocked shows the cost of the mutex on the same operations.
func BenchmarkOrderedMapLocked(b *testing.B) {
	newMap := newOrderedMap(true)
	benchBasics(b, newMap, orderedSet)
	b.Run("Parallel", func(b *testing.B) {
		parallel(b, newMap, orderedPopulate,
			func(m orderedMap, key int) { m.Get(key) },
			func(m orderedMap, key int, value int) { m.SetBottom(key, value) })
	})
}
