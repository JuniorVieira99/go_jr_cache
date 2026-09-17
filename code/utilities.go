package jr_cache

import "sync"

type EvictionPolicy int

const (
	LIFO EvictionPolicy = iota
	FIFO
	LFU
	MFU
	LRU
	MRU
)

// IsValid reports whether p is one of the declared eviction policies.
func (p EvictionPolicy) IsValid() bool {
	return p >= LIFO && p <= MRU
}

func (p EvictionPolicy) String() string {
	switch p {
	case LIFO:
		return "LIFO"
	case FIFO:
		return "FIFO"
	case LFU:
		return "LFU"
	case MFU:
		return "MFU"
	case LRU:
		return "LRU"
	case MRU:
		return "MRU"
	}
	return "unknown"
}

// CommonMapInterface is the API shared by every map in this package. The
// ordered map, the bucket map and the cache map all expose these methods with
// the same names and signatures, so callers can switch between them freely.
//
// "Top" and "Bottom" are the two ends of whatever order a map keeps; for the
// bucket map that is the lowest and highest index, for the ordered map the
// head and the tail.
type CommonMapInterface[Key comparable, Value any] interface {
	Get(key Key) (Value, bool)
	Has(key Key) bool
	Update(key Key, value Value) bool
	Pop(key Key) (Value, bool)
	Delete(key Key)
	Top() (Key, Value, bool)
	Bottom() (Key, Value, bool)
	TopKey() (Key, bool)
	BottomKey() (Key, bool)
	PopTop() (Key, Value, bool)
	PopBottom() (Key, Value, bool)
	Len() int
	Clear()
}

// Helpers shared by the sharded structures.

// partition_by_shard groups entries by the shard index shardOf assigns to
// their key, so each shard can be filled in one go.
func partition_by_shard[Key comparable, Value any](entries map[Key]Value, shards int, shardOf func(Key) int) []map[Key]Value {
	parts := make([]map[Key]Value, shards)
	for key, value := range entries {
		index := shardOf(key)
		if parts[index] == nil {
			parts[index] = make(map[Key]Value, len(entries)/shards+1)
		}
		parts[index][key] = value
	}
	return parts
}

// run_per_shard hands each non-empty partition to one of up to threads
// workers; a shard is only ever handled by a single worker at a time.
func run_per_shard[Key comparable, Value any](parts []map[Key]Value, threads int, fn func(index int, part map[Key]Value)) {
	if threads <= 0 {
		threads = 1
	}
	if threads > len(parts) {
		threads = len(parts)
	}
	work := make(chan int, len(parts))
	for index, part := range parts {
		if len(part) > 0 {
			work <- index
		}
	}
	close(work)

	var wg sync.WaitGroup
	for i := 0; i < threads; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range work {
				fn(index, parts[index])
			}
		}()
	}
	wg.Wait()
}
