package tests

import (
	"errors"
	"testing"
	"time"

	jr_cache "jr_cache/code"
)

func newCache(t *testing.T, policy jr_cache.EvictionPolicy, capacity uint64) *jr_cache.CacheMap[string, int] {
	t.Helper()
	cm, err := jr_cache.NewCacheMap[string, int](jr_cache.CacheMapConfig{
		Eviction:    policy,
		MaxCapacity: capacity,
	})
	if err != nil {
		t.Fatalf("NewCacheMap(%v, %d) error: %v", policy, capacity, err)
	}
	return cm
}

func assertKeys(t *testing.T, cm *jr_cache.CacheMap[string, int], present []string, absent []string) {
	t.Helper()
	for _, k := range present {
		if !cm.Has(k) {
			t.Fatalf("%v: key %q should be cached", cm.EvictionPolicy(), k)
		}
	}
	for _, k := range absent {
		if cm.Has(k) {
			t.Fatalf("%v: key %q should have been evicted", cm.EvictionPolicy(), k)
		}
	}
	if cm.Len() != len(present) {
		t.Fatalf("%v: Len() = %d, want %d", cm.EvictionPolicy(), cm.Len(), len(present))
	}
}

func TestCacheMap_Constructor(t *testing.T) {
	_, err := jr_cache.NewCacheMap[string, int](jr_cache.CacheMapConfig{Eviction: jr_cache.LRU})
	if !errors.Is(err, jr_cache.ErrInvalidMaxCapacity) {
		t.Fatalf("MaxCapacity 0: err = %v, want ErrInvalidMaxCapacity", err)
	}
	_, err = jr_cache.NewCacheMap[string, int](jr_cache.CacheMapConfig{Eviction: 42, MaxCapacity: 1})
	if !errors.Is(err, jr_cache.ErrInvalidEvictionPolicy) {
		t.Fatalf("bad policy: err = %v, want ErrInvalidEvictionPolicy", err)
	}
	cm := newCache(t, jr_cache.LFU, 3)
	if cm.EvictionPolicy() != jr_cache.LFU || cm.MaxCapacity() != 3 {
		t.Fatalf("EvictionPolicy() = %v, MaxCapacity() = %d", cm.EvictionPolicy(), cm.MaxCapacity())
	}
}

func TestCacheMap_LRU(t *testing.T) {
	cm := newCache(t, jr_cache.LRU, 3)
	cm.Set("a", 1)
	cm.Set("b", 2)
	cm.Set("c", 3)
	cm.Get("a") // a is now the most recently used; b is the least.
	cm.Set("d", 4)
	assertKeys(t, cm, []string{"a", "c", "d"}, []string{"b"})
	if k, _, _ := cm.Top(); k != "c" {
		t.Fatalf("Top() = %q, want c (least recently used)", k)
	}
	if k, _, _ := cm.Bottom(); k != "d" {
		t.Fatalf("Bottom() = %q, want d (most recently used)", k)
	}
	// Writing an existing key also counts as an access.
	cm.Set("c", 30)
	cm.Set("e", 5)
	assertKeys(t, cm, []string{"c", "d", "e"}, []string{"a"})
	if v, _ := cm.Get("c"); v != 30 {
		t.Fatalf("Get(c) = %d, want 30", v)
	}
}

func TestCacheMap_MRU(t *testing.T) {
	cm := newCache(t, jr_cache.MRU, 3)
	cm.Set("a", 1)
	cm.Set("b", 2)
	cm.Set("c", 3)
	cm.Get("a") // a is now the most recently used and the eviction victim.
	cm.Set("d", 4)
	assertKeys(t, cm, []string{"b", "c", "d"}, []string{"a"})
}

func TestCacheMap_FIFO(t *testing.T) {
	cm := newCache(t, jr_cache.FIFO, 3)
	cm.Set("a", 1)
	cm.Set("b", 2)
	cm.Set("c", 3)
	cm.Get("a")     // Reads do not affect FIFO order...
	cm.Set("a", 10) // ...and neither do writes to existing keys.
	cm.Set("d", 4)
	assertKeys(t, cm, []string{"b", "c", "d"}, []string{"a"})
	cm.Set("e", 5)
	assertKeys(t, cm, []string{"c", "d", "e"}, []string{"a", "b"})
}

func TestCacheMap_LIFO(t *testing.T) {
	cm := newCache(t, jr_cache.LIFO, 3)
	cm.Set("a", 1)
	cm.Set("b", 2)
	cm.Set("c", 3)
	cm.Get("a")
	cm.Set("d", 4) // Evicts c, the last one in.
	assertKeys(t, cm, []string{"a", "b", "d"}, []string{"c"})
	cm.Set("e", 5) // Evicts d.
	assertKeys(t, cm, []string{"a", "b", "e"}, []string{"c", "d"})
}

func TestCacheMap_LFU(t *testing.T) {
	cm := newCache(t, jr_cache.LFU, 3)
	cm.Set("a", 1)
	cm.Set("b", 2)
	cm.Set("c", 3)
	cm.Get("a")
	cm.Get("a")
	cm.Get("b")
	// Frequencies: a=3, b=2, c=1.
	cm.Set("d", 4)
	assertKeys(t, cm, []string{"a", "b", "d"}, []string{"c"})
	if k, _, _ := cm.Top(); k != "d" {
		t.Fatalf("Top() = %q, want d (least frequently used)", k)
	}
	if k, _, _ := cm.Bottom(); k != "a" {
		t.Fatalf("Bottom() = %q, want a (most frequently used)", k)
	}
	// d=1 is still the least used; a Set on b bumps it, not d.
	cm.Set("b", 20)
	cm.Set("e", 5)
	assertKeys(t, cm, []string{"a", "b", "e"}, []string{"c", "d"})
}

func TestCacheMap_LFUTieBreaksByRecency(t *testing.T) {
	cm := newCache(t, jr_cache.LFU, 3)
	cm.Set("a", 1)
	cm.Set("b", 2)
	cm.Set("c", 3)
	// All at frequency 1: the least recently promoted (a) goes first.
	cm.Set("d", 4)
	assertKeys(t, cm, []string{"b", "c", "d"}, []string{"a"})

	// Promote c, then b, then d to frequency 2: c is now the oldest there,
	// so it is the one evicted when e comes in.
	cm.Get("c")
	cm.Get("b")
	cm.Get("d")
	cm.Set("e", 5)
	assertKeys(t, cm, []string{"b", "d", "e"}, []string{"a", "c"})
	// e (frequency 1) goes before the frequency-2 entries regardless of age.
	cm.Set("f", 6)
	assertKeys(t, cm, []string{"b", "d", "f"}, []string{"a", "c", "e"})
	// Once f is promoted, frequency 2 holds b, d, f in that order: b goes next.
	cm.Get("f")
	cm.Set("g", 7)
	assertKeys(t, cm, []string{"d", "f", "g"}, []string{"a", "b", "c", "e"})
}

func TestCacheMap_MFUTieBreaksByRecency(t *testing.T) {
	cm := newCache(t, jr_cache.MFU, 3)
	cm.Set("a", 1)
	cm.Set("b", 2)
	cm.Set("c", 3)
	// All at frequency 1: the most recently promoted (c) goes first.
	cm.Set("d", 4)
	assertKeys(t, cm, []string{"a", "b", "d"}, []string{"c"})
}

func TestCacheMap_MFU(t *testing.T) {
	cm := newCache(t, jr_cache.MFU, 3)
	cm.Set("a", 1)
	cm.Set("b", 2)
	cm.Set("c", 3)
	cm.Get("a")
	cm.Get("a")
	cm.Get("b")
	// Frequencies: a=3, b=2, c=1; MFU evicts a.
	cm.Set("d", 4)
	assertKeys(t, cm, []string{"b", "c", "d"}, []string{"a"})
}

func TestCacheMap_SetOrGet(t *testing.T) {
	cm := newCache(t, jr_cache.LRU, 2)
	if v, loaded := cm.SetOrGet("a", 1); loaded || v != 1 {
		t.Fatalf("SetOrGet(a) on a miss = %d, %v; want 1, false", v, loaded)
	}
	if v, loaded := cm.SetOrGet("a", 99); !loaded || v != 1 {
		t.Fatalf("SetOrGet(a) on a hit = %d, %v; want 1, true", v, loaded)
	}
	// The hit counted as an access, so b is evicted before a.
	cm.SetOrGet("b", 2)
	cm.SetOrGet("a", 0)
	cm.SetOrGet("c", 3)
	assertKeys(t, cm, []string{"a", "c"}, []string{"b"})
}

func TestCacheMap_PopDeleteUpdateClear(t *testing.T) {
	cm := newCache(t, jr_cache.LFU, 3)
	cm.Set("a", 1)
	cm.Set("b", 2)
	if v, ok := cm.Pop("a"); !ok || v != 1 {
		t.Fatalf("Pop(a) = %d, %v; want 1, true", v, ok)
	}
	if _, ok := cm.Pop("a"); ok {
		t.Fatal("Pop on a removed key reported a value")
	}
	// Update changes the value without counting as an access.
	cm.Set("c", 3)
	cm.Get("c")
	if !cm.Update("b", 20) {
		t.Fatal("Update(b) returned false")
	}
	if k, v, _ := cm.Top(); k != "b" || v != 20 {
		t.Fatalf("Top() = %q, %d; want b, 20 (Update must not bump frequency)", k, v)
	}
	if cm.Update("missing", 0) {
		t.Fatal("Update on a missing key returned true")
	}
	cm.Delete("b")
	cm.Delete("missing")
	assertKeys(t, cm, []string{"c"}, []string{"a", "b"})
	cm.Clear()
	assertKeys(t, cm, nil, []string{"c"})
	cm.Set("d", 4)
	assertKeys(t, cm, []string{"d"}, nil)
}

func TestCacheMap_PopTopBottom(t *testing.T) {
	cm := newCache(t, jr_cache.FIFO, 3)
	cm.Set("a", 1)
	cm.Set("b", 2)
	cm.Set("c", 3)
	if k, v, ok := cm.PopTop(); !ok || k != "a" || v != 1 {
		t.Fatalf("PopTop() = %q, %d, %v; want a, 1, true", k, v, ok)
	}
	if k, v, ok := cm.PopBottom(); !ok || k != "c" || v != 3 {
		t.Fatalf("PopBottom() = %q, %d, %v; want c, 3, true", k, v, ok)
	}
	if k, ok := cm.TopKey(); !ok || k != "b" {
		t.Fatalf("TopKey() = %q, %v; want b, true", k, ok)
	}
	if k, ok := cm.BottomKey(); !ok || k != "b" {
		t.Fatalf("BottomKey() = %q, %v; want b, true", k, ok)
	}
	cm.PopTop()
	if _, _, ok := cm.PopTop(); ok {
		t.Fatal("PopTop() on an empty cache reported an entry")
	}
}

func TestCacheMap_SetMaxCapacity(t *testing.T) {
	cm := newCache(t, jr_cache.FIFO, 4)
	for _, k := range []string{"a", "b", "c", "d"} {
		cm.Set(k, 0)
	}
	if err := cm.SetMaxCapacity(0); !errors.Is(err, jr_cache.ErrInvalidMaxCapacity) {
		t.Fatalf("SetMaxCapacity(0) err = %v, want ErrInvalidMaxCapacity", err)
	}
	if err := cm.SetMaxCapacity(2); err != nil {
		t.Fatalf("SetMaxCapacity(2) err = %v", err)
	}
	// Shrinking evicts the oldest entries.
	assertKeys(t, cm, []string{"c", "d"}, []string{"a", "b"})
	if cm.MaxCapacity() != 2 {
		t.Fatalf("MaxCapacity() = %d, want 2", cm.MaxCapacity())
	}
	cm.SetMaxCapacity(3)
	cm.Set("e", 0)
	assertKeys(t, cm, []string{"c", "d", "e"}, []string{"a", "b"})
}

func TestCacheMap_CapacityOne(t *testing.T) {
	for _, policy := range []jr_cache.EvictionPolicy{
		jr_cache.LIFO, jr_cache.FIFO, jr_cache.LFU, jr_cache.MFU, jr_cache.LRU, jr_cache.MRU,
	} {
		cm := newCache(t, policy, 1)
		cm.Set("a", 1)
		cm.Set("b", 2)
		assertKeys(t, cm, []string{"b"}, []string{"a"})
		cm.Get("b")
		cm.Set("c", 3)
		assertKeys(t, cm, []string{"c"}, []string{"a", "b"})
	}
}

func TestCacheMap_ThreadSafe(t *testing.T) {
	for _, policy := range []jr_cache.EvictionPolicy{jr_cache.LRU, jr_cache.LFU} {
		cm, err := jr_cache.NewCacheMap[int, int](jr_cache.CacheMapConfig{
			Eviction:    policy,
			MaxCapacity: 16,
			Locked:      true,
		})
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		for g := range 4 {
			go func(g int) {
				defer func() { done <- struct{}{} }()
				for i := range 2000 {
					k := (g*7 + i) % 40
					cm.Set(k, i)
					cm.Get(k)
					cm.SetOrGet(k+1, i)
					cm.Top()
					cm.Len()
					if i%5 == 0 {
						cm.Pop(k)
					}
				}
			}(g)
		}
		for range 4 {
			<-done
		}
		if cm.Len() > 16 {
			t.Fatalf("%v: Len() = %d exceeds capacity 16", policy, cm.Len())
		}
	}
}

func TestCacheMap_Populate(t *testing.T) {
	cm := newCache(t, jr_cache.FIFO, 3)
	cm.SetWithTTL("a", 0, time.Hour)
	cm.Populate(map[string]int{"a": 1, "b": 2})
	if v, _ := cm.Get("a"); v != 1 {
		t.Fatalf("Get(a) = %d, want 1", v)
	}
	if _, ok := cm.GetTTL("a"); ok {
		t.Fatal("Populate kept the TTL of an overwritten key")
	}
	// Populating past the capacity evicts like Set does.
	cm.Populate(map[string]int{"c": 3, "d": 4})
	if cm.Len() != 3 {
		t.Fatalf("Len() = %d, want 3", cm.Len())
	}
	if cm.Has("a") {
		t.Fatal("a (oldest) should have been evicted")
	}
}
