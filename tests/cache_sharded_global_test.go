package tests

import (
	"errors"
	"testing"

	jr_cache "jr_cache/code"
)

func newGlobalShardedCache(t *testing.T, policy jr_cache.EvictionPolicy, capacity uint64, shards uint64) *jr_cache.ShardedCacheMap[int, int] {
	t.Helper()
	scm, err := jr_cache.NewShardedCacheMap[int, int](jr_cache.ShardedCacheMapConfig[int]{
		Eviction:    policy,
		MaxCapacity: capacity,
		ShardCount:  shards,
		GlobalOrder: true,
		Hash:        identityHash,
	})
	if err != nil {
		t.Fatalf("NewShardedCacheMap(global, %v, %d, %d) error: %v", policy, capacity, shards, err)
	}
	return scm
}

func TestShardedCacheMap_GlobalOrderConstructor(t *testing.T) {
	// A global order does not split the capacity, so more shards than slots is fine.
	scm := newGlobalShardedCache(t, jr_cache.LRU, 2, 8)
	if !scm.GlobalOrder() || scm.ShardCount() != 8 || scm.MaxCapacity() != 2 {
		t.Fatalf("GlobalOrder() = %v, ShardCount() = %d, MaxCapacity() = %d", scm.GlobalOrder(), scm.ShardCount(), scm.MaxCapacity())
	}
	if newShardedCache(t, jr_cache.LRU, 8, 2).GlobalOrder() {
		t.Fatal("per-shard cache reports a global order")
	}
	if scm.ShardIndex(13) != 13%8 {
		t.Fatalf("ShardIndex(13) = %d, want %d", scm.ShardIndex(13), 13%8)
	}
}

func TestShardedCacheMap_GlobalOrderEvictsAcrossShards(t *testing.T) {
	// 2 shards, 3 slots in total. Even keys land in shard 0, odd in shard 1.
	scm := newGlobalShardedCache(t, jr_cache.LRU, 3, 2)
	scm.Set(0, 0)
	scm.Set(2, 2)
	scm.Set(4, 4) // shard 0 holds all three: allowed, the capacity is global
	assertShardedKeys(t, scm, []int{0, 2, 4}, nil)

	scm.Set(1, 1) // evicts 0, the least recently used of the whole cache
	assertShardedKeys(t, scm, []int{2, 4, 1}, []int{0})
	scm.Get(2)    // 4 is now the least recently used
	scm.Set(3, 3) // evicts 4
	assertShardedKeys(t, scm, []int{2, 1, 3}, []int{0, 4})

	// Top and Bottom are exact across the whole cache.
	if k, v, ok := scm.Top(); !ok || k != 1 || v != 1 {
		t.Fatalf("Top() = %d, %d, %v; want 1, 1, true", k, v, ok)
	}
	if k, v, ok := scm.Bottom(); !ok || k != 3 || v != 3 {
		t.Fatalf("Bottom() = %d, %d, %v; want 3, 3, true", k, v, ok)
	}
	if k, _ := scm.TopKey(); k != 1 {
		t.Fatalf("TopKey() = %d, want 1", k)
	}
	if k, _ := scm.BottomKey(); k != 3 {
		t.Fatalf("BottomKey() = %d, want 3", k)
	}
	if k, v, ok := scm.PopTop(); !ok || k != 1 || v != 1 {
		t.Fatalf("PopTop() = %d, %d, %v; want 1, 1, true", k, v, ok)
	}
	if k, v, ok := scm.PopBottom(); !ok || k != 3 || v != 3 {
		t.Fatalf("PopBottom() = %d, %d, %v; want 3, 3, true", k, v, ok)
	}
	assertShardedKeys(t, scm, []int{2}, []int{0, 1, 3, 4})
}

func TestShardedCacheMap_GlobalOrderLFU(t *testing.T) {
	scm := newGlobalShardedCache(t, jr_cache.LFU, 3, 4)
	scm.Set(0, 0)
	scm.Set(1, 1)
	scm.Set(2, 2)
	scm.Get(0)
	scm.Get(0)
	scm.Get(1)
	scm.Set(3, 3) // frequencies 0:3, 1:2, 2:1 → evicts 2
	assertShardedKeys(t, scm, []int{0, 1, 3}, []int{2})
	if k, _, _ := scm.Top(); k != 3 {
		t.Fatalf("Top() = %d, want 3 (least frequent)", k)
	}
	if k, _, _ := scm.Bottom(); k != 0 {
		t.Fatalf("Bottom() = %d, want 0 (most frequent)", k)
	}
}

func TestShardedCacheMap_GlobalOrderSharedAPI(t *testing.T) {
	scm := newGlobalShardedCache(t, jr_cache.LRU, 4, 2)
	scm.Set(0, 10)
	if v, loaded := scm.SetOrGet(0, 99); !loaded || v != 10 {
		t.Fatalf("SetOrGet(0) on a hit = %d, %v; want 10, true", v, loaded)
	}
	if v, loaded := scm.SetOrGet(1, 11); loaded || v != 11 {
		t.Fatalf("SetOrGet(1) on a miss = %d, %v; want 11, false", v, loaded)
	}
	if !scm.Update(1, 12) || scm.Update(5, 0) {
		t.Fatal("Update returned the wrong result")
	}
	if v, _ := scm.Get(1); v != 12 {
		t.Fatalf("Get(1) = %d, want 12", v)
	}
	if v, ok := scm.Pop(0); !ok || v != 10 {
		t.Fatalf("Pop(0) = %d, %v; want 10, true", v, ok)
	}
	if _, ok := scm.Pop(0); ok {
		t.Fatal("Pop on a removed key reported a value")
	}
	scm.Delete(1)
	scm.Delete(1)
	assertShardedKeys(t, scm, nil, []int{0, 1})

	scm.Set(2, 2)
	scm.Set(3, 3)
	scm.Clear()
	assertShardedKeys(t, scm, nil, []int{2, 3})
	if _, _, ok := scm.Top(); ok {
		t.Fatal("Top() on an empty cache reported an entry")
	}

	// SetMaxCapacity below the shard count is fine with a global order.
	scm.Set(2, 2)
	scm.Set(3, 3)
	scm.Set(4, 4)
	if err := scm.SetMaxCapacity(1); err != nil {
		t.Fatalf("SetMaxCapacity(1) err = %v", err)
	}
	assertShardedKeys(t, scm, []int{4}, []int{2, 3})
	if err := scm.SetMaxCapacity(0); !errors.Is(err, jr_cache.ErrInvalidMaxCapacity) {
		t.Fatalf("SetMaxCapacity(0) err = %v, want ErrInvalidMaxCapacity", err)
	}

	scm.PopulateThreaded(map[int]int{5: 5, 6: 6, 7: 7}, 4)
	if scm.Len() != 1 {
		t.Fatalf("Len() after populating past the capacity = %d, want 1", scm.Len())
	}
}

func TestShardedCacheMap_GlobalOrderTTL(t *testing.T) {
	scm := newGlobalShardedCache(t, jr_cache.LRU, 8, 2)
	scm.SetWithTTL(0, 0, shortTTL)
	scm.SetWithTTL(1, 1, shortTTL)
	scm.Set(2, 2)
	if !scm.UpdateTTL(2, longTTL) {
		t.Fatal("UpdateTTL(2) returned false")
	}
	if _, ok := scm.GetTTL(2); !ok {
		t.Fatal("GetTTL(2) reported no TTL")
	}
	if !scm.RemoveTTL(2) {
		t.Fatal("RemoveTTL(2) returned false")
	}
	waitForExpiry()
	if scm.Has(0) {
		t.Fatal("expired key 0 is still reported as cached")
	}
	if _, ok := scm.Get(0); ok {
		t.Fatal("Get on an expired key returned a value")
	}
	if n := scm.DeleteExpired(); n != 1 {
		t.Fatalf("DeleteExpired() = %d, want 1 (key 1; key 0 was purged by Get)", n)
	}
	assertShardedKeys(t, scm, []int{2}, []int{0, 1})
	// The value of an expired key must be gone from the shards too.
	scm.Set(0, 100)
	if v, _ := scm.Get(0); v != 100 {
		t.Fatalf("Get(0) after re-set = %d, want 100", v)
	}
}

func TestShardedCacheMap_OnEvict(t *testing.T) {
	for _, global := range []bool{false, true} {
		var scm *jr_cache.ShardedCacheMap[int, int]
		if global {
			scm = newGlobalShardedCache(t, jr_cache.FIFO, 2, 2)
		} else {
			scm = newShardedCache(t, jr_cache.FIFO, 2, 2)
		}
		evicted := map[int]int{}
		scm.SetOnEvict(func(key int, value int) { evicted[key] = value })
		scm.Set(0, 10)
		scm.Set(2, 12) // per-shard: shard 0 is full at one slot, evicts 0; global: no eviction yet
		scm.Set(4, 14) // evicts the oldest even key either way
		if v, ok := evicted[0]; !ok || v != 10 {
			t.Fatalf("global=%v: OnEvict saw %v, want key 0 with value 10", global, evicted)
		}
		scm.Pop(4)
		scm.Delete(2)
		if len(evicted) > 2 {
			t.Fatalf("global=%v: Pop/Delete fired OnEvict: %v", global, evicted)
		}
		if scm.Len() != 0 {
			t.Fatalf("global=%v: Len() = %d, want 0", global, scm.Len())
		}
	}
}

func TestShardedCacheMap_GlobalOrderThreadSafe(t *testing.T) {
	for _, policy := range []jr_cache.EvictionPolicy{jr_cache.LRU, jr_cache.LFU} {
		scm, err := jr_cache.NewShardedCacheMap[int, int](jr_cache.ShardedCacheMapConfig[int]{
			Eviction:    policy,
			MaxCapacity: 32,
			ShardCount:  4,
			Locked:      true,
			GlobalOrder: true,
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
		// Every indexed key must have a value once the cache is quiescent.
		for k := 0; k < 101; k++ {
			if scm.Has(k) {
				if _, ok := scm.Get(k); !ok {
					t.Fatalf("%v: key %d is indexed but has no value", policy, k)
				}
			}
		}
	}
}
