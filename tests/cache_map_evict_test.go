package tests

import (
	"testing"

	jr_cache "jr_cache/code"
)

func TestCacheMap_OnEvict(t *testing.T) {
	cm := newCache(t, jr_cache.FIFO, 2)
	var evicted []string
	cm.SetOnEvict(func(key string, value int) {
		evicted = append(evicted, key)
		if value != len(key) {
			t.Fatalf("OnEvict(%q) got value %d, want %d", key, value, len(key))
		}
	})

	// Capacity eviction.
	cm.Set("a", 1)
	cm.Set("bb", 2)
	cm.Set("ccc", 3) // evicts a
	if len(evicted) != 1 || evicted[0] != "a" {
		t.Fatalf("after a capacity eviction OnEvict saw %v, want [a]", evicted)
	}

	// Explicit removals are not evictions.
	cm.Pop("bb")
	cm.Delete("ccc")
	cm.Set("dddd", 4)
	cm.PopTop()
	cm.Set("eeeee", 5)
	cm.Clear()
	if len(evicted) != 1 {
		t.Fatalf("Pop/Delete/PopTop/Clear fired OnEvict: %v", evicted)
	}

	// Expired entries purged on access, by Top skipping past them, and by DeleteExpired.
	cm.SetWithTTL("f", 1, 0)
	cm.Get("f")
	cm.SetWithTTL("gg", 2, 0)
	cm.Set("h", 1)
	cm.Top() // skips gg
	cm.SetWithTTL("iii", 3, 0)
	cm.DeleteExpired()
	if len(evicted) != 4 || evicted[1] != "f" || evicted[2] != "gg" || evicted[3] != "iii" {
		t.Fatalf("after expiry purges OnEvict saw %v, want [a f gg iii]", evicted)
	}

	// Shrinking the capacity evicts through the hook too.
	cm.Set("jj", 2)
	cm.SetMaxCapacity(1) // h is older than jj
	if len(evicted) != 5 || evicted[4] != "h" {
		t.Fatalf("after SetMaxCapacity OnEvict saw %v, want [... h]", evicted)
	}

	// nil removes the hook.
	cm.SetOnEvict(nil)
	cm.Set("kkk", 3)
	if len(evicted) != 5 {
		t.Fatalf("OnEvict fired after being removed: %v", evicted)
	}
}
