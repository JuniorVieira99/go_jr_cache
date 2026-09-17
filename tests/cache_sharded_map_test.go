package tests

import (
	"errors"
	"testing"
	"time"

	jr_cache "jr_cache/code"
)

// identityHash makes the shard of an int key predictable: key % ShardCount.
func identityHash(key int) uint64 {
	return uint64(key)
}

func newShardedCache(t *testing.T, policy jr_cache.EvictionPolicy, capacity uint64, shards uint64) *jr_cache.ShardedCacheMap[int, int] {
	t.Helper()
	scm, err := jr_cache.NewShardedCacheMap[int, int](jr_cache.ShardedCacheMapConfig[int]{
		Eviction:    policy,
		MaxCapacity: capacity,
		ShardCount:  shards,
		Hash:        identityHash,
	})
	if err != nil {
		t.Fatalf("NewShardedCacheMap(%v, %d, %d) error: %v", policy, capacity, shards, err)
	}
	return scm
}

func assertShardedKeys(t *testing.T, scm *jr_cache.ShardedCacheMap[int, int], present []int, absent []int) {
	t.Helper()
	for _, k := range present {
		if !scm.Has(k) {
			t.Fatalf("key %d should be cached", k)
		}
	}
	for _, k := range absent {
		if scm.Has(k) {
			t.Fatalf("key %d should have been evicted", k)
		}
	}
	if scm.Len() != len(present) {
		t.Fatalf("Len() = %d, want %d", scm.Len(), len(present))
	}
}

func TestShardedCacheMap_Constructor(t *testing.T) {
	cases := []struct {
		name   string
		config jr_cache.ShardedCacheMapConfig[int]
		want   error
	}{
		{"zero capacity", jr_cache.ShardedCacheMapConfig[int]{Eviction: jr_cache.LRU, ShardCount: 1}, jr_cache.ErrInvalidMaxCapacity},
		{"bad policy", jr_cache.ShardedCacheMapConfig[int]{Eviction: 42, MaxCapacity: 4, ShardCount: 1}, jr_cache.ErrInvalidEvictionPolicy},
		{"zero shards", jr_cache.ShardedCacheMapConfig[int]{Eviction: jr_cache.LRU, MaxCapacity: 4}, jr_cache.ErrInvalidShardCount},
		{"more shards than capacity", jr_cache.ShardedCacheMapConfig[int]{Eviction: jr_cache.LRU, MaxCapacity: 2, ShardCount: 3}, jr_cache.ErrInvalidShardCount},
	}
	for _, c := range cases {
		if _, err := jr_cache.NewShardedCacheMap[int, int](c.config); !errors.Is(err, c.want) {
			t.Fatalf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}

	scm := newShardedCache(t, jr_cache.LFU, 10, 4)
	if scm.EvictionPolicy() != jr_cache.LFU || scm.MaxCapacity() != 10 || scm.ShardCount() != 4 {
		t.Fatalf("EvictionPolicy() = %v, MaxCapacity() = %d, ShardCount() = %d", scm.EvictionPolicy(), scm.MaxCapacity(), scm.ShardCount())
	}
}

func TestShardedCacheMap_ShardIndex(t *testing.T) {
	scm := newShardedCache(t, jr_cache.LRU, 8, 4)
	for _, k := range []int{0, 1, 2, 3, 4, 5, 6, 7} {
		if idx := scm.ShardIndex(k); idx != k%4 {
			t.Fatalf("ShardIndex(%d) = %d, want %d", k, idx, k%4)
		}
	}
}

func TestShardedCacheMap_CapacityIsSplitAcrossShards(t *testing.T) {
	// 10 slots over 4 shards: shards 0 and 1 get 3, shards 2 and 3 get 2.
	scm := newShardedCache(t, jr_cache.FIFO, 10, 4)
	for k := 0; k < 100; k++ {
		scm.Set(k, k)
	}
	if scm.Len() != 10 {
		t.Fatalf("Len() = %d, want 10 once every shard is full", scm.Len())
	}
	// Shard 0 (keys 0, 4, 8, ...) keeps its last three: 88, 92, 96.
	assertShardedKeys(t, scm,
		[]int{88, 92, 96, 89, 93, 97, 94, 98, 95, 99},
		[]int{0, 4, 84, 85, 90, 91})
}

func TestShardedCacheMap_EvictionIsPerShard(t *testing.T) {
	scm := newShardedCache(t, jr_cache.LRU, 4, 2)
	// Shard 0 holds even keys, shard 1 odd keys, two slots each.
	scm.Set(0, 0)
	scm.Set(2, 2)
	scm.Set(1, 1)
	scm.Set(3, 3)
	scm.Get(0) // 2 is now the least recently used even key.
	scm.Set(4, 4)
	assertShardedKeys(t, scm, []int{0, 4, 1, 3}, []int{2})
	// The odd shard is untouched by pressure on the even one.
	scm.Set(6, 6)
	assertShardedKeys(t, scm, []int{4, 6, 1, 3}, []int{0, 2})
}

func TestShardedCacheMap_SharedAPI(t *testing.T) {
	scm := newShardedCache(t, jr_cache.LRU, 4, 2)
	scm.Set(0, 10)
	if v, loaded := scm.SetOrGet(0, 99); !loaded || v != 10 {
		t.Fatalf("SetOrGet(0) on a hit = %d, %v; want 10, true", v, loaded)
	}
	if v, loaded := scm.SetOrGet(1, 11); loaded || v != 11 {
		t.Fatalf("SetOrGet(1) on a miss = %d, %v; want 11, false", v, loaded)
	}
	if !scm.Update(1, 12) {
		t.Fatal("Update(1) returned false")
	}
	if v, _ := scm.Get(1); v != 12 {
		t.Fatalf("Get(1) = %d, want 12", v)
	}
	if scm.Update(5, 0) {
		t.Fatal("Update on a missing key returned true")
	}
	if v, ok := scm.Pop(0); !ok || v != 10 {
		t.Fatalf("Pop(0) = %d, %v; want 10, true", v, ok)
	}
	scm.Delete(1)
	scm.Delete(1)
	assertShardedKeys(t, scm, nil, []int{0, 1})

	scm.Set(2, 2)
	scm.Set(3, 3)
	scm.Clear()
	assertShardedKeys(t, scm, nil, []int{2, 3})
	scm.Set(2, 2)
	assertShardedKeys(t, scm, []int{2}, nil)
}

func TestShardedCacheMap_TopBottomUseFullestShard(t *testing.T) {
	scm := newShardedCache(t, jr_cache.FIFO, 6, 2)
	if _, _, ok := scm.Top(); ok {
		t.Fatal("Top() on an empty cache reported an entry")
	}
	scm.Set(1, 1) // shard 1
	scm.Set(0, 0) // shard 0
	scm.Set(2, 2) // shard 0 now has two entries and is the fullest.
	if k, _, _ := scm.Top(); k != 0 {
		t.Fatalf("Top() = %d, want 0 (oldest in the fullest shard)", k)
	}
	if k, _, _ := scm.Bottom(); k != 2 {
		t.Fatalf("Bottom() = %d, want 2 (newest in the fullest shard)", k)
	}
	if k, _ := scm.TopKey(); k != 0 {
		t.Fatalf("TopKey() = %d, want 0", k)
	}
	if k, _ := scm.BottomKey(); k != 2 {
		t.Fatalf("BottomKey() = %d, want 2", k)
	}
	if k, v, ok := scm.PopTop(); !ok || k != 0 || v != 0 {
		t.Fatalf("PopTop() = %d, %d, %v; want 0, 0, true", k, v, ok)
	}
	// Shards are tied at one entry each; the lowest index wins.
	if k, v, ok := scm.PopBottom(); !ok || k != 2 || v != 2 {
		t.Fatalf("PopBottom() = %d, %d, %v; want 2, 2, true", k, v, ok)
	}
	assertShardedKeys(t, scm, []int{1}, []int{0, 2})
}

func TestShardedCacheMap_SetMaxCapacity(t *testing.T) {
	scm := newShardedCache(t, jr_cache.FIFO, 8, 2)
	for k := 0; k < 8; k++ {
		scm.Set(k, k)
	}
	if err := scm.SetMaxCapacity(0); !errors.Is(err, jr_cache.ErrInvalidMaxCapacity) {
		t.Fatalf("SetMaxCapacity(0) err = %v, want ErrInvalidMaxCapacity", err)
	}
	if err := scm.SetMaxCapacity(1); !errors.Is(err, jr_cache.ErrInvalidShardCount) {
		t.Fatalf("SetMaxCapacity(1) err = %v, want ErrInvalidShardCount", err)
	}
	// 3 slots over 2 shards: shard 0 keeps 2, shard 1 keeps 1, oldest evicted.
	if err := scm.SetMaxCapacity(3); err != nil {
		t.Fatalf("SetMaxCapacity(3) err = %v", err)
	}
	if scm.MaxCapacity() != 3 {
		t.Fatalf("MaxCapacity() = %d, want 3", scm.MaxCapacity())
	}
	assertShardedKeys(t, scm, []int{4, 6, 7}, []int{0, 1, 2, 3, 5})
	// Growing again gives each shard four slots without evicting anything.
	scm.SetMaxCapacity(8)
	scm.Set(8, 8)
	scm.Set(9, 9)
	assertShardedKeys(t, scm, []int{4, 6, 7, 8, 9}, []int{0, 1, 2, 3, 5})
	// Shard 0 (4, 6, 8) fills with 10 and then evicts its oldest for 12.
	scm.Set(10, 10)
	scm.Set(12, 12)
	assertShardedKeys(t, scm, []int{6, 8, 10, 12, 7, 9}, []int{0, 1, 2, 3, 4, 5})
}

func TestShardedCacheMap_Populate(t *testing.T) {
	for _, threads := range []int{1, 2, 8} {
		// 6 slots over 2 shards: 3 each. Even keys go to shard 0, odd to shard 1.
		scm := newShardedCache(t, jr_cache.FIFO, 6, 2)
		scm.SetWithTTL(0, -1, time.Hour) // overwritten, and its TTL dropped
		entries := map[int]int{0: 0, 1: 1, 2: 2, 3: 3, 4: 4, 5: 5, 6: 6, 7: 7}
		scm.PopulateThreaded(entries, threads)
		if scm.Len() != 6 {
			t.Fatalf("threads=%d: Len() = %d, want 6 (each shard evicts down to 3)", threads, scm.Len())
		}
		for _, k := range []int{0, 1, 2, 3, 4, 5, 6, 7} {
			if v, ok := scm.Get(k); ok && v != k {
				t.Fatalf("threads=%d: Get(%d) = %d, want %d", threads, k, v, k)
			}
		}
		if _, ok := scm.GetTTL(0); ok {
			t.Fatalf("threads=%d: Populate kept the TTL of an overwritten key", threads)
		}
	}
	scm := newShardedCache(t, jr_cache.LRU, 4, 2)
	scm.Populate(map[int]int{1: 1, 2: 2})
	assertShardedKeys(t, scm, []int{1, 2}, nil)
}

func TestShardedCacheMap_DefaultHashDistributes(t *testing.T) {
	scm, err := jr_cache.NewShardedCacheMap[string, int](jr_cache.ShardedCacheMapConfig[string]{
		Eviction:    jr_cache.LRU,
		MaxCapacity: 400,
		ShardCount:  4,
	})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int]bool{}
	for i := 0; i < 200; i++ {
		key := string(rune('a'+i%26)) + string(rune('a'+i/26))
		scm.Set(key, i)
		seen[scm.ShardIndex(key)] = true
		// A key must keep hashing to the same shard.
		if !scm.Has(key) {
			t.Fatalf("key %q did not land in a stable shard", key)
		}
	}
	if len(seen) != 4 {
		t.Fatalf("200 keys only reached %d of 4 shards", len(seen))
	}
	if scm.Len() != 200 {
		t.Fatalf("Len() = %d, want 200", scm.Len())
	}
}

func TestShardedCacheMap_ThreadSafe(t *testing.T) {
	for _, policy := range []jr_cache.EvictionPolicy{jr_cache.LRU, jr_cache.LFU} {
		scm, err := jr_cache.NewShardedCacheMap[int, int](jr_cache.ShardedCacheMapConfig[int]{
			Eviction:    policy,
			MaxCapacity: 32,
			ShardCount:  4,
			Locked:      true,
		})
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		for g := 0; g < 8; g++ {
			go func(g int) {
				defer func() { done <- struct{}{} }()
				for i := 0; i < 2000; i++ {
					k := (g*7 + i) % 100
					scm.Set(k, i)
					scm.Get(k)
					scm.SetOrGet(k+1, i)
					scm.Top()
					scm.Len()
					if i%5 == 0 {
						scm.Pop(k)
					}
					if i%500 == 0 {
						scm.SetMaxCapacity(uint64(16 + i%17))
					}
				}
			}(g)
		}
		for g := 0; g < 8; g++ {
			<-done
		}
		if scm.Len() > 32 {
			t.Fatalf("%v: Len() = %d exceeds capacity 32", policy, scm.Len())
		}
	}
}
