package tests

import (
	"math/rand"
	"sort"
	"testing"

	jr_cache "jr_cache/code"
)

func assertIndices(t *testing.T, bm *jr_cache.BucketMap[string, int], want ...uint64) {
	t.Helper()
	got := bm.GetBucketIndices()
	if len(got) != len(want) {
		t.Fatalf("GetBucketIndices() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("GetBucketIndices() = %v, want %v", got, want)
		}
	}
	if bm.GetBucketCount() != len(want) {
		t.Fatalf("GetBucketCount() = %d, want %d", bm.GetBucketCount(), len(want))
	}
}

// checkInvariants verifies that the key→bucket index agrees with the bucket
// contents, that no bucket is empty, and that buckets are in ascending order.
func checkInvariants[Key comparable, Value any](t *testing.T, bm *jr_cache.BucketMap[Key, Value]) {
	t.Helper()
	total := 0
	var prev uint64
	first := true
	for _, b := range bm.GetAllBuckets() {
		if b.Len() == 0 {
			t.Fatalf("bucket %d is empty but still present", b.Index())
		}
		if !first && b.Index() <= prev {
			t.Fatalf("bucket %d follows bucket %d; not ascending", b.Index(), prev)
		}
		first, prev = false, b.Index()
		for _, key := range b.Keys() {
			if idx, ok := bm.GetBucketIndex(key); !ok || idx != b.Index() {
				t.Fatalf("key %v is in bucket %d but GetBucketIndex says %d (present=%v)", key, b.Index(), idx, ok)
			}
		}
		total += b.Len()
	}
	if total != bm.Len() {
		t.Fatalf("buckets hold %d entries but Len() = %d", total, bm.Len())
	}
}

func TestBucketMap_PopAnyReturnsValue(t *testing.T) {
	bm := jr_cache.NewBucketMap[string, int](false)
	bm.Set(1, "a", 42)
	v, ok := bm.PopAnyFromBucket(1)
	if !ok || v != 42 {
		t.Fatalf("PopAnyFromBucket(1) = %d, %v; want 42, true", v, ok)
	}
	if bm.Len() != 0 || bm.Has("a") {
		t.Fatal("popped entry is still present")
	}
	assertIndices(t, bm)
	if _, ok := bm.PopAnyFromBucket(1); ok {
		t.Fatal("PopAnyFromBucket on a removed bucket reported a value")
	}
}

func TestBucketMap_SetMovesBetweenBuckets(t *testing.T) {
	bm := jr_cache.NewBucketMap[string, int](false)
	bm.Set(1, "a", 1)
	bm.Set(1, "b", 2)
	bm.Set(2, "a", 10)
	checkInvariants(t, bm)
	assertIndices(t, bm, 1, 2)
	if v, _ := bm.Get("a"); v != 10 {
		t.Fatalf("Get(a) = %d, want 10", v)
	}
	if idx, ok := bm.GetBucketIndex("a"); !ok || idx != 2 {
		t.Fatalf("GetBucketIndex(a) = %d, %v; want 2, true", idx, ok)
	}
	if bm.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", bm.Len())
	}

	// The old bucket must not still hold "a".
	if v, ok := bm.GetAnyFromBucket(1); !ok || v != 2 {
		t.Fatalf("GetAnyFromBucket(1) = %d, %v; want 2, true (only b should remain)", v, ok)
	}

	// Moving the last entry out of a bucket removes that bucket.
	bm.Set(2, "b", 20)
	checkInvariants(t, bm)
	assertIndices(t, bm, 2)

	// Setting in the same bucket just updates the value.
	bm.Set(2, "b", 21)
	checkInvariants(t, bm)
	if v, _ := bm.Get("b"); v != 21 {
		t.Fatalf("Get(b) = %d, want 21", v)
	}
}

func TestBucketMap_PopRemovesEmptyBucket(t *testing.T) {
	bm := jr_cache.NewBucketMap[string, int](false)
	bm.Set(3, "a", 1)
	bm.Set(3, "b", 2)
	if v, ok := bm.Pop("a"); !ok || v != 1 {
		t.Fatalf("Pop(a) = %d, %v; want 1, true", v, ok)
	}
	assertIndices(t, bm, 3)
	bm.Pop("b")
	assertIndices(t, bm)
	if _, ok := bm.Pop("b"); ok {
		t.Fatal("Pop on a missing key reported a value")
	}
	if _, ok := bm.GetAnyFromBucket(3); ok {
		t.Fatal("GetAnyFromBucket on a removed bucket reported a value")
	}
	if _, ok := bm.GetBucketIndex("b"); ok {
		t.Fatal("GetBucketIndex on a popped key reported an index")
	}
}

func TestBucketMap_OrderingAndTopBottom(t *testing.T) {
	bm := jr_cache.NewBucketMap[string, int](false)
	if _, _, ok := bm.Top(); ok {
		t.Fatal("Top() on an empty map reported an entry")
	}
	if _, _, ok := bm.Bottom(); ok {
		t.Fatal("Bottom() on an empty map reported an entry")
	}

	// Insert out of order: bottom, top, then the middle with no hint.
	bm.Set(5, "e", 5)
	bm.Set(1, "a", 1)
	bm.Set(3, "c", 3)
	bm.Set(4, "d", 4)
	bm.Set(2, "b", 2)
	assertIndices(t, bm, 1, 2, 3, 4, 5)
	checkInvariants(t, bm)

	if k, v, ok := bm.Top(); !ok || k != "a" || v != 1 {
		t.Fatalf("Top() = %q, %d, %v; want a, 1, true", k, v, ok)
	}
	if k, v, ok := bm.Bottom(); !ok || k != "e" || v != 5 {
		t.Fatalf("Bottom() = %q, %d, %v; want e, 5, true", k, v, ok)
	}

	// Frequency bump: the hint (old bucket) sits right before the new one.
	bm.Set(6, "e", 6)
	assertIndices(t, bm, 1, 2, 3, 4, 6)
	bm.Set(5, "a", 1)
	assertIndices(t, bm, 2, 3, 4, 5, 6)

	// Move down with a hint above the target.
	bm.Set(1, "e", 6)
	assertIndices(t, bm, 1, 2, 3, 4, 5)
	if k, _, ok := bm.Top(); !ok || k != "e" {
		t.Fatalf("Top() = %q, want e", k)
	}
	checkInvariants(t, bm)
}

func TestBucketMap_LFUStyleUsage(t *testing.T) {
	bm := jr_cache.NewBucketMap[string, int](false)
	// Simulate an LFU cache: every access bumps the key's frequency by one.
	bump := func(key string) {
		idx, ok := bm.GetBucketIndex(key)
		if !ok {
			bm.Set(1, key, 0)
			return
		}
		v, _ := bm.Get(key)
		bm.Set(idx+1, key, v+1)
	}
	for _, key := range []string{"x", "y", "z", "x", "x", "y", "x"} {
		bump(key)
	}
	checkInvariants(t, bm)
	assertIndices(t, bm, 1, 2, 4)
	if k, _, ok := bm.Top(); !ok || k != "z" {
		t.Fatalf("Top() = %q, want z (least frequently used)", k)
	}
	if k, _, ok := bm.Bottom(); !ok || k != "x" {
		t.Fatalf("Bottom() = %q, want x (most frequently used)", k)
	}
	// Evict the least frequently used entry.
	k, _, _ := bm.Top()
	bm.Pop(k)
	assertIndices(t, bm, 2, 4)
	checkInvariants(t, bm)
}

func TestBucketMap_SharedAPI(t *testing.T) {
	bm := jr_cache.NewBucketMap[string, int](false)
	bm.Set(1, "a", 1)
	bm.Set(2, "b", 2)
	bm.Set(3, "c", 3)

	// Update keeps the key in its bucket.
	if !bm.Update("b", 20) {
		t.Fatal("Update(b) returned false")
	}
	if idx, _ := bm.GetBucketIndex("b"); idx != 2 {
		t.Fatalf("Update moved b to bucket %d", idx)
	}
	if v, _ := bm.Get("b"); v != 20 {
		t.Fatalf("Get(b) = %d, want 20", v)
	}
	if bm.Update("missing", 0) {
		t.Fatal("Update on a missing key returned true")
	}

	if k, ok := bm.TopKey(); !ok || k != "a" {
		t.Fatalf("TopKey() = %q, %v; want a, true", k, ok)
	}
	if k, ok := bm.BottomKey(); !ok || k != "c" {
		t.Fatalf("BottomKey() = %q, %v; want c, true", k, ok)
	}

	if k, v, ok := bm.PopTop(); !ok || k != "a" || v != 1 {
		t.Fatalf("PopTop() = %q, %d, %v; want a, 1, true", k, v, ok)
	}
	if k, v, ok := bm.PopBottom(); !ok || k != "c" || v != 3 {
		t.Fatalf("PopBottom() = %q, %d, %v; want c, 3, true", k, v, ok)
	}
	assertIndices(t, bm, 2)
	checkInvariants(t, bm)

	bm.Delete("b")
	bm.Delete("missing")
	assertIndices(t, bm)
	if _, _, ok := bm.PopTop(); ok {
		t.Fatal("PopTop() on an empty map reported an entry")
	}

	// SetTop/SetBottom on an empty map go to bucket 0, afterwards to the ends.
	bm.SetTop("x", 0)
	assertIndices(t, bm, 0)
	bm.Set(5, "y", 5)
	bm.SetBottom("z", 5)
	bm.SetTop("w", 0)
	if idx, _ := bm.GetBucketIndex("z"); idx != 5 {
		t.Fatalf("SetBottom put z in bucket %d, want 5", idx)
	}
	if idx, _ := bm.GetBucketIndex("w"); idx != 0 {
		t.Fatalf("SetTop put w in bucket %d, want 0", idx)
	}
	checkInvariants(t, bm)

	bm.Clear()
	if bm.Len() != 0 || bm.GetBucketCount() != 0 {
		t.Fatalf("after Clear(): Len() = %d, GetBucketCount() = %d", bm.Len(), bm.GetBucketCount())
	}
	bm.Set(1, "a", 1)
	checkInvariants(t, bm)
}

func TestBucketMap_OrderWithinBucket(t *testing.T) {
	bm := jr_cache.NewBucketMap[string, int](false)
	bm.Set(1, "a", 1)
	bm.Set(1, "b", 2)
	bm.Set(1, "c", 3)

	// Entries in a bucket are ordered by arrival, oldest first.
	if k, _, _ := bm.Top(); k != "a" {
		t.Fatalf("Top() = %q, want a (oldest in bucket 1)", k)
	}
	if k, _, _ := bm.Bottom(); k != "c" {
		t.Fatalf("Bottom() = %q, want c (newest in bucket 1)", k)
	}
	keys := bm.GetAllBuckets()[0].Keys()
	if len(keys) != 3 || keys[0] != "a" || keys[1] != "b" || keys[2] != "c" {
		t.Fatalf("Keys() = %v, want [a b c]", keys)
	}

	// Updating in place keeps the position; moving out and back in does not.
	bm.Set(1, "a", 10)
	if k, _, _ := bm.Top(); k != "a" {
		t.Fatalf("Top() after in-place update = %q, want a", k)
	}
	bm.Set(2, "a", 10)
	bm.Set(1, "a", 10)
	if k, _, _ := bm.Bottom(); k != "a" {
		t.Fatalf("Bottom() after re-entering bucket 1 = %q, want a", k)
	}
	if k, _, _ := bm.Top(); k != "b" {
		t.Fatalf("Top() after a re-entered = %q, want b", k)
	}
	checkInvariants(t, bm)

	// GetAnyFromBucket/PopAnyFromBucket work on the oldest entry of the bucket.
	if v, _ := bm.GetAnyFromBucket(1); v != 2 {
		t.Fatalf("GetAnyFromBucket(1) = %d, want 2 (b, oldest)", v)
	}
	if v, _ := bm.PopAnyFromBucket(1); v != 2 {
		t.Fatalf("PopAnyFromBucket(1) = %d, want 2 (b, oldest)", v)
	}
	if k, _, _ := bm.PopTop(); k != "c" {
		t.Fatalf("PopTop() = %q, want c", k)
	}
	if k, _, _ := bm.PopBottom(); k != "a" {
		t.Fatalf("PopBottom() = %q, want a", k)
	}
	assertIndices(t, bm)
}

func TestBucketMap_WarmBuckets(t *testing.T) {
	bm := jr_cache.NewBucketMap[string, int](false)
	if bm.WarmBuckets() != jr_cache.DefaultWarmBuckets {
		t.Fatalf("WarmBuckets() on a new map = %d, want %d", bm.WarmBuckets(), jr_cache.DefaultWarmBuckets)
	}

	// Creating a bucket takes one from the pool; emptying it gives it back.
	bm.Set(1, "a", 1)
	if bm.WarmBuckets() != jr_cache.DefaultWarmBuckets-1 {
		t.Fatalf("WarmBuckets() after creating a bucket = %d, want %d", bm.WarmBuckets(), jr_cache.DefaultWarmBuckets-1)
	}
	bm.Set(2, "a", 1) // bucket 1 empties, bucket 2 is created: net zero
	if bm.WarmBuckets() != jr_cache.DefaultWarmBuckets-1 {
		t.Fatalf("WarmBuckets() after a bump = %d, want %d", bm.WarmBuckets(), jr_cache.DefaultWarmBuckets-1)
	}
	bm.Pop("a")
	if bm.WarmBuckets() != jr_cache.DefaultWarmBuckets {
		t.Fatalf("WarmBuckets() after the last bucket emptied = %d, want %d", bm.WarmBuckets(), jr_cache.DefaultWarmBuckets)
	}

	// A recycled bucket must come back clean.
	bm.Set(1, "a", 1)
	bm.Set(1, "b", 2)
	bm.Set(5, "a", 1)
	bm.Set(5, "b", 2) // bucket 1 is now empty and pooled
	bm.Set(1, "c", 3) // reuses it
	if keys := bm.GetAllBuckets()[0].Keys(); len(keys) != 1 || keys[0] != "c" {
		t.Fatalf("recycled bucket holds %v, want [c]", keys)
	}
	checkInvariants(t, bm)

	// WarmUp grows the pool and its retention limit; it never shrinks.
	bm.WarmUp(40)
	if bm.WarmBuckets() != 40 {
		t.Fatalf("WarmBuckets() after WarmUp(40) = %d, want 40", bm.WarmBuckets())
	}
	bm.WarmUp(4)
	if bm.WarmBuckets() != 40 {
		t.Fatalf("WarmUp(4) shrank the pool to %d", bm.WarmBuckets())
	}

	// The pool never retains more than its limit.
	for i := 0; i < 100; i++ {
		bm.Set(uint64(i), "k"+string(rune('a'+i%26))+string(rune('a'+i/26)), i)
	}
	bm.Clear()
	if bm.WarmBuckets() != 40 {
		t.Fatalf("WarmBuckets() after Clear = %d, want 40 (the limit)", bm.WarmBuckets())
	}
	if bm.Len() != 0 || bm.GetBucketCount() != 0 {
		t.Fatal("Clear left entries or buckets behind")
	}
	bm.Set(3, "z", 26)
	checkInvariants(t, bm)
	if k, v, ok := bm.Top(); !ok || k != "z" || v != 26 {
		t.Fatalf("Top() after reuse = %q, %d, %v; want z, 26, true", k, v, ok)
	}
}

func TestBucketMap_RandomOperationsKeepInvariants(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	bm := jr_cache.NewBucketMap[int, int](true)
	reference := map[int]uint64{}
	for i := 0; i < 5000; i++ {
		key := rng.Intn(50)
		switch rng.Intn(4) {
		case 0, 1:
			idx := uint64(rng.Intn(10))
			bm.Set(idx, key, i)
			reference[key] = idx
		case 2:
			_, ok := bm.Pop(key)
			_, want := reference[key]
			if ok != want {
				t.Fatalf("Pop(%d) reported %v, want %v", key, ok, want)
			}
			delete(reference, key)
		case 3:
			// Bump like an LFU cache would.
			if idx, ok := reference[key]; ok {
				bm.Set(idx+1, key, i)
				reference[key] = idx + 1
			}
		}
		checkInvariants(t, bm)
	}

	wantIndices := map[uint64]bool{}
	for _, idx := range reference {
		wantIndices[idx] = true
	}
	var want []uint64
	for idx := range wantIndices {
		want = append(want, idx)
	}
	sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
	got := bm.GetBucketIndices()
	if len(got) != len(want) {
		t.Fatalf("GetBucketIndices() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("GetBucketIndices() = %v, want %v", got, want)
		}
	}
	if bm.Len() != len(reference) {
		t.Fatalf("Len() = %d, want %d", bm.Len(), len(reference))
	}
}
