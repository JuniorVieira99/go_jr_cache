package tests

import (
	"errors"
	"sort"
	"sync"
	"testing"

	jr_cache "jr_cache/code"
)

func newShardedMap(t *testing.T, shards uint64) *jr_cache.ShardedMap[int, int] {
	t.Helper()
	sm, err := jr_cache.NewShardedMap[int, int](jr_cache.ShardedMapConfig[int]{
		ShardCount: shards,
		Hash:       identityHash,
	})
	if err != nil {
		t.Fatalf("NewShardedMap(%d) error: %v", shards, err)
	}
	return sm
}

func TestShardedMap_Constructor(t *testing.T) {
	if _, err := jr_cache.NewShardedMap[int, int](jr_cache.ShardedMapConfig[int]{}); !errors.Is(err, jr_cache.ErrInvalidShardCount) {
		t.Fatalf("ShardCount 0: err = %v, want ErrInvalidShardCount", err)
	}
	sm := newShardedMap(t, 4)
	if sm.ShardCount() != 4 {
		t.Fatalf("ShardCount() = %d, want 4", sm.ShardCount())
	}
	for _, k := range []int{0, 1, 5, 7} {
		if idx := sm.ShardIndex(k); idx != k%4 {
			t.Fatalf("ShardIndex(%d) = %d, want %d", k, idx, k%4)
		}
	}
}

func TestShardedMap_BasicOperations(t *testing.T) {
	sm := newShardedMap(t, 3)
	if _, ok := sm.Get(1); ok || sm.Has(1) || sm.Len() != 0 {
		t.Fatal("empty map reported an entry")
	}

	sm.Set(1, 10)
	sm.Set(2, 20)
	sm.Set(3, 30)
	if v, ok := sm.Get(1); !ok || v != 10 {
		t.Fatalf("Get(1) = %d, %v; want 10, true", v, ok)
	}
	if !sm.Has(2) || sm.Len() != 3 {
		t.Fatalf("Has(2) = %v, Len() = %d; want true, 3", sm.Has(2), sm.Len())
	}

	// Set overwrites; Update only touches existing keys.
	sm.Set(1, 11)
	if v, _ := sm.Get(1); v != 11 {
		t.Fatalf("Get(1) after Set = %d, want 11", v)
	}
	if !sm.Update(2, 22) {
		t.Fatal("Update(2) returned false")
	}
	if v, _ := sm.Get(2); v != 22 {
		t.Fatalf("Get(2) after Update = %d, want 22", v)
	}
	if sm.Update(9, 0) || sm.Has(9) {
		t.Fatal("Update on a missing key returned true or inserted it")
	}

	if v, loaded := sm.SetOrGet(3, 99); !loaded || v != 30 {
		t.Fatalf("SetOrGet(3) on a hit = %d, %v; want 30, true", v, loaded)
	}
	if v, loaded := sm.SetOrGet(4, 40); loaded || v != 40 {
		t.Fatalf("SetOrGet(4) on a miss = %d, %v; want 40, false", v, loaded)
	}

	if v, ok := sm.Pop(3); !ok || v != 30 {
		t.Fatalf("Pop(3) = %d, %v; want 30, true", v, ok)
	}
	if _, ok := sm.Pop(3); ok {
		t.Fatal("Pop on a removed key reported a value")
	}
	sm.Delete(4)
	sm.Delete(4)
	if sm.Has(3) || sm.Has(4) || sm.Len() != 2 {
		t.Fatalf("after Pop/Delete: Has(3) = %v, Has(4) = %v, Len() = %d", sm.Has(3), sm.Has(4), sm.Len())
	}

	sm.Clear()
	if sm.Len() != 0 || sm.Has(1) {
		t.Fatal("Clear() left entries behind")
	}
	sm.Set(1, 1)
	if sm.Len() != 1 {
		t.Fatal("map is not usable after Clear()")
	}
}

func TestShardedMap_Range(t *testing.T) {
	sm := newShardedMap(t, 4)
	for k := 0; k < 10; k++ {
		sm.Set(k, k*k)
	}

	var seen []int
	sm.Range(func(key int, value int) bool {
		if value != key*key {
			t.Fatalf("Range gave key %d with value %d", key, value)
		}
		seen = append(seen, key)
		return true
	})
	sort.Ints(seen)
	if len(seen) != 10 {
		t.Fatalf("Range visited %d entries, want 10: %v", len(seen), seen)
	}
	for i, k := range seen {
		if k != i {
			t.Fatalf("Range visited %v, want 0..9", seen)
		}
	}

	// Returning false stops the walk.
	visited := 0
	sm.Range(func(int, int) bool {
		visited++
		return visited < 3
	})
	if visited != 3 {
		t.Fatalf("Range kept going after false: visited %d", visited)
	}

	// The callback may modify the map without deadlocking.
	sm.Range(func(key int, _ int) bool {
		sm.Delete(key)
		return true
	})
	if sm.Len() != 0 {
		t.Fatalf("Len() after deleting inside Range = %d, want 0", sm.Len())
	}
}

func TestShardedMap_Populate(t *testing.T) {
	for _, threads := range []int{0, 1, 3, 16} {
		sm := newShardedMap(t, 4)
		sm.Set(1, -1) // overwritten by the populate
		entries := make(map[int]int, 100)
		for k := 0; k < 100; k++ {
			entries[k] = k * 2
		}
		sm.Populate(entries, threads)
		if sm.Len() != 100 {
			t.Fatalf("threads=%d: Len() = %d, want 100", threads, sm.Len())
		}
		for k, want := range entries {
			if v, ok := sm.Get(k); !ok || v != want {
				t.Fatalf("threads=%d: Get(%d) = %d, %v; want %d, true", threads, k, v, ok, want)
			}
			if sm.ShardIndex(k) != k%4 {
				t.Fatalf("threads=%d: key %d landed in shard %d, want %d", threads, k, sm.ShardIndex(k), k%4)
			}
		}
	}
	// Empty input and inputs that only touch some shards are fine.
	sm := newShardedMap(t, 4)
	sm.Populate(nil, 8)
	sm.Populate(map[int]int{4: 4, 8: 8}, 8)
	if sm.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", sm.Len())
	}
}

func TestShardedMap_DefaultHashDistributes(t *testing.T) {
	sm, err := jr_cache.NewShardedMap[string, int](jr_cache.ShardedMapConfig[string]{ShardCount: 4})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int]bool{}
	for i := 0; i < 200; i++ {
		key := string(rune('a'+i%26)) + string(rune('a'+i/26))
		sm.Set(key, i)
		seen[sm.ShardIndex(key)] = true
		if v, ok := sm.Get(key); !ok || v != i {
			t.Fatalf("Get(%q) = %d, %v; want %d, true", key, v, ok, i)
		}
	}
	if len(seen) != 4 {
		t.Fatalf("200 keys only reached %d of 4 shards", len(seen))
	}
	if sm.Len() != 200 {
		t.Fatalf("Len() = %d, want 200", sm.Len())
	}
}

func TestShardedMap_ThreadSafe(t *testing.T) {
	sm := newShardedMap(t, 4)
	done := make(chan struct{})
	for g := 0; g < 8; g++ {
		go func(g int) {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 2000; i++ {
				k := (g*7 + i) % 100
				sm.Set(k, i)
				sm.Get(k)
				sm.SetOrGet(k+1, i)
				sm.Update(k, i+1)
				sm.Len()
				if i%5 == 0 {
					sm.Pop(k)
				}
				if i%100 == 0 {
					sm.Range(func(int, int) bool { return true })
				}
			}
		}(g)
	}
	for g := 0; g < 8; g++ {
		<-done
	}
	if sm.Len() > 101 {
		t.Fatalf("Len() = %d exceeds the 101 distinct keys used", sm.Len())
	}
}

func TestShardedMap_Modify(t *testing.T) {
	sm := newShardedMap(t, 2)

	// Absent key, fn keeps: inserted.
	if v, ok := sm.Modify(1, func(v int, exists bool) (int, bool) {
		if exists {
			t.Fatal("Modify reported an absent key as present")
		}
		return 10, true
	}); !ok || v != 10 {
		t.Fatalf("Modify insert = %d, %v; want 10, true", v, ok)
	}
	// Present key, fn changes: updated.
	if v, ok := sm.Modify(1, func(v int, exists bool) (int, bool) {
		if !exists || v != 10 {
			t.Fatalf("Modify gave %d, %v; want 10, true", v, exists)
		}
		return v + 1, true
	}); !ok || v != 11 {
		t.Fatalf("Modify update = %d, %v; want 11, true", v, ok)
	}
	// Present key, fn drops: deleted.
	if _, ok := sm.Modify(1, func(int, bool) (int, bool) { return 0, false }); ok {
		t.Fatal("Modify drop reported the key as present")
	}
	if sm.Has(1) {
		t.Fatal("key survived Modify returning false")
	}
	// Absent key, fn drops: nothing happens.
	if _, ok := sm.Modify(1, func(int, bool) (int, bool) { return 0, false }); ok || sm.Len() != 0 {
		t.Fatal("Modify drop on an absent key changed the map")
	}

	// Modify is atomic: concurrent increments are never lost.
	sm.Set(7, 0)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				sm.Modify(7, func(v int, _ bool) (int, bool) { return v + 1, true })
			}
		}()
	}
	wg.Wait()
	if v, _ := sm.Get(7); v != 8000 {
		t.Fatalf("after 8000 concurrent increments Get(7) = %d", v)
	}
}
