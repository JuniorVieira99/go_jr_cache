package tests

import (
	"testing"
	"time"

	jr_cache "jr_cache/code"
)

const (
	shortTTL = 40 * time.Millisecond
	longTTL  = time.Hour
)

func waitForExpiry() {
	time.Sleep(shortTTL * 2)
}

func TestCacheMapTTL_EntriesExpire(t *testing.T) {
	cm := newCache(t, jr_cache.LRU, 4)
	cm.SetWithTTL("a", 1, shortTTL)
	cm.Set("b", 2)
	if !cm.Has("a") {
		t.Fatal("a should be cached before its TTL passes")
	}
	if v, ok := cm.Get("a"); !ok || v != 1 {
		t.Fatalf("Get(a) before expiry = %d, %v; want 1, true", v, ok)
	}

	waitForExpiry()
	if cm.Has("a") {
		t.Fatal("Has(a) after expiry is true")
	}
	if _, ok := cm.Get("a"); ok {
		t.Fatal("Get(a) after expiry reported a value")
	}
	if cm.Len() != 1 {
		t.Fatalf("Len() after the expired entry was touched = %d, want 1", cm.Len())
	}
	if !cm.Has("b") {
		t.Fatal("b (no TTL) should still be cached")
	}
}

func TestCacheMapTTL_ZeroTTLExpiresImmediately(t *testing.T) {
	cm := newCache(t, jr_cache.FIFO, 4)
	cm.SetWithTTL("a", 1, 0)
	cm.SetWithTTL("b", 2, -time.Second)
	if cm.Has("a") || cm.Has("b") {
		t.Fatal("entries with a TTL <= 0 should be expired at once")
	}
	if _, ok := cm.GetTTL("a"); ok {
		t.Fatal("GetTTL on an expired key reported a TTL")
	}
}

func TestCacheMapTTL_GetTTLCountsDown(t *testing.T) {
	cm := newCache(t, jr_cache.LRU, 4)
	cm.SetWithTTL("a", 1, longTTL)
	cm.Set("b", 2)

	remaining, ok := cm.GetTTL("a")
	if !ok || remaining <= 0 || remaining > longTTL {
		t.Fatalf("GetTTL(a) = %v, %v; want (0, %v], true", remaining, ok, longTTL)
	}
	time.Sleep(10 * time.Millisecond)
	later, _ := cm.GetTTL("a")
	if later >= remaining {
		t.Fatalf("GetTTL(a) did not decrease: %v then %v", remaining, later)
	}
	if _, ok := cm.GetTTL("b"); ok {
		t.Fatal("GetTTL on a key without a TTL reported a TTL")
	}
	if _, ok := cm.GetTTL("missing"); ok {
		t.Fatal("GetTTL on a missing key reported a TTL")
	}
}

func TestCacheMapTTL_UpdateAndRemoveTTL(t *testing.T) {
	cm := newCache(t, jr_cache.LRU, 4)
	cm.Set("a", 1)

	// UpdateTTL must be able to add a TTL to a key that had none.
	if !cm.UpdateTTL("a", shortTTL) {
		t.Fatal("UpdateTTL on a key without a TTL returned false")
	}
	if _, ok := cm.GetTTL("a"); !ok {
		t.Fatal("GetTTL(a) after UpdateTTL reported no TTL")
	}
	// Extending it keeps the entry alive past the original deadline.
	if !cm.UpdateTTL("a", longTTL) {
		t.Fatal("UpdateTTL on a key with a TTL returned false")
	}
	waitForExpiry()
	if !cm.Has("a") {
		t.Fatal("a expired despite its TTL being extended")
	}

	// RemoveTTL makes the key persist.
	if !cm.RemoveTTL("a") {
		t.Fatal("RemoveTTL(a) returned false")
	}
	if _, ok := cm.GetTTL("a"); ok {
		t.Fatal("GetTTL(a) after RemoveTTL reported a TTL")
	}

	if cm.UpdateTTL("missing", longTTL) || cm.RemoveTTL("missing") {
		t.Fatal("UpdateTTL/RemoveTTL on a missing key returned true")
	}
	cm.SetWithTTL("z", 26, 0)
	if cm.UpdateTTL("z", longTTL) {
		t.Fatal("UpdateTTL on an expired key returned true")
	}
	if cm.Has("z") {
		t.Fatal("expired key z survived UpdateTTL")
	}
}

func TestCacheMapTTL_SetClearsTTLUpdateKeepsIt(t *testing.T) {
	cm := newCache(t, jr_cache.LRU, 4)
	cm.SetWithTTL("a", 1, shortTTL)
	cm.Set("a", 10)
	if _, ok := cm.GetTTL("a"); ok {
		t.Fatal("Set without a TTL kept the old TTL")
	}
	waitForExpiry()
	if v, ok := cm.Get("a"); !ok || v != 10 {
		t.Fatalf("Get(a) = %d, %v; want 10, true (Set removed the TTL)", v, ok)
	}

	cm.SetWithTTL("b", 2, longTTL)
	if !cm.Update("b", 20) {
		t.Fatal("Update(b) returned false")
	}
	if _, ok := cm.GetTTL("b"); !ok {
		t.Fatal("Update dropped the TTL")
	}
	if v, _ := cm.Get("b"); v != 20 {
		t.Fatalf("Get(b) = %d, want 20", v)
	}
}

func TestCacheMapTTL_RemovalsDropTTL(t *testing.T) {
	// A key that leaves the cache must not carry its TTL into a later life.
	cm := newCache(t, jr_cache.FIFO, 2)

	cm.SetWithTTL("a", 1, shortTTL)
	cm.Pop("a")
	cm.Set("a", 1)
	waitForExpiry()
	if !cm.Has("a") {
		t.Fatal("a inherited a TTL from before it was popped")
	}

	cm.SetWithTTL("b", 2, shortTTL)
	cm.Delete("b")
	cm.Set("b", 2)
	waitForExpiry()
	if !cm.Has("b") {
		t.Fatal("b inherited a TTL from before it was deleted")
	}

	// Eviction: a and b fill the cache, c evicts a.
	cm.Clear()
	cm.SetWithTTL("a", 1, shortTTL)
	cm.Set("b", 2)
	cm.Set("c", 3)
	if cm.Has("a") {
		t.Fatal("a should have been evicted")
	}
	cm.Pop("b")
	cm.Set("a", 1)
	waitForExpiry()
	if !cm.Has("a") {
		t.Fatal("a inherited a TTL from before it was evicted")
	}

	// PopTop / PopBottom.
	cm.Clear()
	cm.SetWithTTL("a", 1, shortTTL)
	cm.SetWithTTL("b", 2, shortTTL)
	cm.PopTop()
	cm.PopBottom()
	cm.Set("a", 1)
	cm.Set("b", 2)
	waitForExpiry()
	if !cm.Has("a") || !cm.Has("b") {
		t.Fatal("a or b inherited a TTL from before PopTop/PopBottom")
	}

	// Clear.
	cm.SetWithTTL("a", 1, shortTTL)
	cm.Clear()
	cm.Set("a", 1)
	waitForExpiry()
	if !cm.Has("a") {
		t.Fatal("a inherited a TTL from before Clear")
	}
}

func TestCacheMapTTL_ExpiredEntriesAreNeverObserved(t *testing.T) {
	cm := newCache(t, jr_cache.FIFO, 4)
	cm.SetWithTTL("a", 1, shortTTL)
	cm.Set("b", 2)
	cm.SetWithTTL("c", 3, shortTTL)
	waitForExpiry()

	if k, _, ok := cm.Top(); !ok || k != "b" {
		t.Fatalf("Top() = %q, %v; want b (a is expired)", k, ok)
	}
	if k, _, ok := cm.Bottom(); !ok || k != "b" {
		t.Fatalf("Bottom() = %q, %v; want b (c is expired)", k, ok)
	}
	if cm.Len() != 1 {
		t.Fatalf("Len() after peeking past expired entries = %d, want 1", cm.Len())
	}
	if _, ok := cm.Pop("missing"); ok {
		t.Fatal("Pop on a missing key reported a value")
	}
	if cm.Update("a", 0) {
		t.Fatal("Update on an expired key returned true")
	}

	cm.SetWithTTL("d", 4, 0)
	if k, v, ok := cm.PopBottom(); !ok || k != "b" || v != 2 {
		t.Fatalf("PopBottom() = %q, %d, %v; want b, 2, true (d is expired)", k, v, ok)
	}
	if _, _, ok := cm.PopTop(); ok {
		t.Fatal("PopTop() on a cache holding only expired entries reported an entry")
	}
	if cm.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", cm.Len())
	}
}

func TestCacheMapTTL_SetOnExpiredKeyStartsFresh(t *testing.T) {
	cm := newCache(t, jr_cache.LFU, 4)
	cm.SetWithTTL("a", 1, shortTTL)
	cm.Get("a")
	cm.Get("a") // a is at frequency 3
	cm.Set("b", 2)
	waitForExpiry()

	cm.Set("a", 10) // expired: re-inserted at frequency 1, no TTL
	if k, _, _ := cm.Top(); k != "b" {
		t.Fatalf("Top() = %q, want b (a and b both at frequency 1, b is older)", k)
	}
	cm.Get("b")
	if k, _, _ := cm.Top(); k != "a" {
		t.Fatalf("Top() = %q, want a (re-inserted at frequency 1)", k)
	}
	if _, ok := cm.GetTTL("a"); ok {
		t.Fatal("re-inserted a still has a TTL")
	}

	// SetOrGet on an expired key stores the new value.
	cm.SetWithTTL("c", 3, 0)
	if v, loaded := cm.SetOrGet("c", 30); loaded || v != 30 {
		t.Fatalf("SetOrGet(c) on an expired key = %d, %v; want 30, false", v, loaded)
	}
}

func TestCacheMapTTL_DeleteExpired(t *testing.T) {
	cm := newCache(t, jr_cache.LRU, 8)
	cm.SetWithTTL("a", 1, shortTTL)
	cm.SetWithTTL("b", 2, shortTTL)
	cm.SetWithTTL("c", 3, longTTL)
	cm.Set("d", 4)
	if n := cm.DeleteExpired(); n != 0 {
		t.Fatalf("DeleteExpired() before expiry = %d, want 0", n)
	}
	waitForExpiry()
	if cm.Len() != 4 {
		t.Fatalf("Len() before the sweep = %d, want 4 (expiry is lazy)", cm.Len())
	}
	if n := cm.DeleteExpired(); n != 2 {
		t.Fatalf("DeleteExpired() = %d, want 2", n)
	}
	if cm.Len() != 2 || !cm.Has("c") || !cm.Has("d") {
		t.Fatalf("after the sweep: Len() = %d, Has(c) = %v, Has(d) = %v", cm.Len(), cm.Has("c"), cm.Has("d"))
	}
	if n := cm.DeleteExpired(); n != 0 {
		t.Fatalf("second DeleteExpired() = %d, want 0", n)
	}
}

func TestShardedCacheMap_TTL(t *testing.T) {
	scm := newShardedCache(t, jr_cache.LRU, 8, 2)
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
	if scm.Has(0) || scm.Has(1) {
		t.Fatal("expired keys are still reported as cached")
	}
	if n := scm.DeleteExpired(); n != 2 {
		t.Fatalf("DeleteExpired() = %d, want 2 across shards", n)
	}
	assertShardedKeys(t, scm, []int{2}, []int{0, 1})
}
