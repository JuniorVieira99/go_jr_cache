package tests

import (
	"math/rand"
	"sort"
	"testing"
	"time"

	jr_cache "jr_cache/code"
)

// These tests drive a structure and a deliberately naive reference model with
// the same random operations and compare the full contents — keys, values,
// bucket indices and order — after every step.

// modelEntry is one entry of the reference model. Entries are ordered by
// (index, arrival): index is the bucket (0 for the recency policies) and
// arrival a counter bumped whenever an entry lands at the bottom of its
// bucket.
type modelEntry struct {
	key     int
	value   int
	index   uint64
	arrival int
}

type model struct {
	entries []modelEntry
	clock   int
}

func (m *model) find(key int) int {
	for i, e := range m.entries {
		if e.key == key {
			return i
		}
	}
	return -1
}

// sorted returns the entries top first.
func (m *model) sorted() []modelEntry {
	out := append([]modelEntry(nil), m.entries...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].index != out[j].index {
			return out[i].index < out[j].index
		}
		return out[i].arrival < out[j].arrival
	})
	return out
}

// end returns the position of the top or bottom entry, or -1 when empty.
func (m *model) end(top bool) int {
	if len(m.entries) == 0 {
		return -1
	}
	s := m.sorted()
	want := s[len(s)-1]
	if top {
		want = s[0]
	}
	return m.find(want.key)
}

func (m *model) remove(i int) modelEntry {
	e := m.entries[i]
	m.entries = append(m.entries[:i], m.entries[i+1:]...)
	return e
}

// place moves (or adds) key to the bottom of bucket index; a key already in
// that bucket only gets its value replaced.
func (m *model) place(index uint64, key int, value int) {
	if i := m.find(key); i >= 0 {
		m.entries[i].value = value
		if m.entries[i].index != index {
			m.clock++
			m.entries[i].index, m.entries[i].arrival = index, m.clock
		}
		return
	}
	m.clock++
	m.entries = append(m.entries, modelEntry{key: key, value: value, index: index, arrival: m.clock})
}

func compareEntries(t *testing.T, step int, got []jr_cache.CacheEntry[int, int], want []modelEntry, withIndex bool) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("step %d: %d entries, want %d\ngot  %v\nwant %v", step, len(got), len(want), got, want)
	}
	for i := range want {
		if got[i].Key != want[i].key || got[i].Value != want[i].value || (withIndex && got[i].Frequency != want[i].index) {
			t.Fatalf("step %d: entry %d is %+v, want %+v\ngot  %v\nwant %v", step, i, got[i], want[i], got, want)
		}
	}
}

func TestBucketMap_MatchesModel(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	bm := jr_cache.NewBucketMap[int, int](false)
	ref := &model{}
	for step := 0; step < 20000; step++ {
		key := rng.Intn(40)
		value := step
		switch op := rng.Intn(12); op {
		case 0, 1, 2:
			// Mostly neighbouring indices, as frequency counting produces,
			// with the odd far jump.
			var index uint64
			if i := ref.find(key); i >= 0 && rng.Intn(4) > 0 {
				index = ref.entries[i].index + 1
				if rng.Intn(3) == 0 && index > 1 {
					index -= 2
				}
			} else {
				index = uint64(rng.Intn(30))
			}
			bm.Set(index, key, value)
			ref.place(index, key, value)
		case 3:
			var index uint64
			if i := ref.end(true); i >= 0 {
				index = ref.entries[i].index
			}
			bm.SetTop(key, value)
			ref.place(index, key, value)
		case 4:
			var index uint64
			if i := ref.end(false); i >= 0 {
				index = ref.entries[i].index
			}
			bm.SetBottom(key, value)
			ref.place(index, key, value)
		case 5:
			ok := bm.Update(key, value)
			i := ref.find(key)
			if ok != (i >= 0) {
				t.Fatalf("step %d: Update(%d) = %v", step, key, ok)
			}
			if i >= 0 {
				ref.entries[i].value = value
			}
		case 6:
			got, ok := bm.Pop(key)
			i := ref.find(key)
			if ok != (i >= 0) || (ok && got != ref.remove(i).value) {
				t.Fatalf("step %d: Pop(%d) = %d, %v", step, key, got, ok)
			}
		case 7, 8:
			top := op == 7
			var k, v int
			var ok bool
			if top {
				k, v, ok = bm.PopTop()
			} else {
				k, v, ok = bm.PopBottom()
			}
			i := ref.end(top)
			if ok != (i >= 0) {
				t.Fatalf("step %d: PopTop/PopBottom ok = %v", step, ok)
			}
			if ok {
				if e := ref.remove(i); e.key != k || e.value != v {
					t.Fatalf("step %d: popped %d=%d, want %d=%d", step, k, v, e.key, e.value)
				}
			}
		case 9, 10:
			index := uint64(rng.Intn(30))
			oldest := -1
			for i, e := range ref.entries {
				if e.index == index && (oldest < 0 || e.arrival < ref.entries[oldest].arrival) {
					oldest = i
				}
			}
			var v int
			var ok bool
			if op == 9 {
				v, ok = bm.GetAnyFromBucket(index)
			} else {
				v, ok = bm.PopAnyFromBucket(index)
			}
			if ok != (oldest >= 0) || (ok && v != ref.entries[oldest].value) {
				t.Fatalf("step %d: Get/PopAnyFromBucket(%d) = %d, %v", step, index, v, ok)
			}
			if ok && op == 10 {
				ref.remove(oldest)
			}
		case 11:
			if rng.Intn(50) == 0 {
				bm.Clear()
				ref.entries = nil
			}
		}

		want := ref.sorted()
		compareEntries(t, step, bm.Snapshot().Entries, want, true)
		checkInvariants(t, bm)
		var wantIndices []uint64
		for _, e := range want {
			if len(wantIndices) == 0 || wantIndices[len(wantIndices)-1] != e.index {
				wantIndices = append(wantIndices, e.index)
			}
		}
		if got := bm.GetBucketIndices(); len(got) != len(wantIndices) || bm.GetBucketCount() != len(wantIndices) {
			t.Fatalf("step %d: GetBucketIndices() = %v (count %d), want %v", step, got, bm.GetBucketCount(), wantIndices)
		}
	}
}

// An evicted key must leave nothing behind: inserting it again, by any
// route, gives a fresh entry with no TTL.
func TestCacheMap_EvictionDropsTTL(t *testing.T) {
	policies := []jr_cache.EvictionPolicy{jr_cache.LRU, jr_cache.MRU, jr_cache.FIFO, jr_cache.LIFO, jr_cache.LFU, jr_cache.MFU}
	for _, policy := range policies {
		t.Run(policy.String(), func(t *testing.T) {
			cm := newCache(t, policy, 1)
			cm.SetWithTTL("a", 1, time.Hour)
			cm.Set("b", 2) // evicts a
			if cm.Has("a") {
				t.Fatal("a was not evicted")
			}
			cm.Pop("b")
			if _, existed := cm.SetOrGet("a", 3); existed {
				t.Fatal("SetOrGet found the evicted a")
			}
			if ttl, ok := cm.GetTTL("a"); ok {
				t.Fatalf("re-inserted a kept the evicted entry's TTL (%v left)", ttl)
			}
		})
	}
}

func TestCacheMap_MatchesModel(t *testing.T) {
	policies := []jr_cache.EvictionPolicy{jr_cache.LRU, jr_cache.MRU, jr_cache.FIFO, jr_cache.LIFO, jr_cache.LFU, jr_cache.MFU}
	for _, policy := range policies {
		t.Run(policy.String(), func(t *testing.T) {
			const capacity = 12
			cm, err := jr_cache.NewCacheMap[int, int](jr_cache.CacheMapConfig{Eviction: policy, MaxCapacity: capacity})
			if err != nil {
				t.Fatal(err)
			}
			var evicted, wantEvicted []modelEntry
			cm.SetOnEvict(func(key int, value int) {
				evicted = append(evicted, modelEntry{key: key, value: value})
			})

			frequency := policy == jr_cache.LFU || policy == jr_cache.MFU
			recency := policy == jr_cache.LRU || policy == jr_cache.MRU
			evictTop := policy == jr_cache.LRU || policy == jr_cache.FIFO || policy == jr_cache.LFU
			ref := &model{}
			touch := func(i int) {
				switch {
				case frequency:
					ref.place(ref.entries[i].index+1, ref.entries[i].key, ref.entries[i].value)
				case recency:
					ref.clock++
					ref.entries[i].arrival = ref.clock
				}
			}
			insert := func(key int, value int) {
				for len(ref.entries) >= capacity {
					e := ref.remove(ref.end(evictTop))
					wantEvicted = append(wantEvicted, modelEntry{key: e.key, value: e.value})
				}
				var index uint64
				if frequency {
					index = 1
				}
				ref.place(index, key, value)
			}

			rng := rand.New(rand.NewSource(int64(policy) + 1))
			for step := 0; step < 20000; step++ {
				key := rng.Intn(30)
				value := step
				switch rng.Intn(7) {
				case 0, 1:
					got, ok := cm.Get(key)
					i := ref.find(key)
					if ok != (i >= 0) || (ok && got != ref.entries[i].value) {
						t.Fatalf("step %d: Get(%d) = %d, %v", step, key, got, ok)
					}
					if i >= 0 {
						touch(i)
					}
				case 2, 3:
					cm.Set(key, value)
					if i := ref.find(key); i >= 0 {
						ref.entries[i].value = value
						touch(i)
					} else {
						insert(key, value)
					}
				case 4:
					got, existed := cm.SetOrGet(key, value)
					i := ref.find(key)
					if existed != (i >= 0) {
						t.Fatalf("step %d: SetOrGet(%d) existed = %v", step, key, existed)
					}
					if i >= 0 {
						if got != ref.entries[i].value {
							t.Fatalf("step %d: SetOrGet(%d) = %d, want %d", step, key, got, ref.entries[i].value)
						}
						touch(i)
					} else {
						insert(key, value)
					}
				case 5:
					ok := cm.Update(key, value)
					i := ref.find(key)
					if ok != (i >= 0) {
						t.Fatalf("step %d: Update(%d) = %v", step, key, ok)
					}
					if i >= 0 {
						ref.entries[i].value = value
					}
				case 6:
					got, ok := cm.Pop(key)
					i := ref.find(key)
					if ok != (i >= 0) || (ok && got != ref.remove(i).value) {
						t.Fatalf("step %d: Pop(%d) = %d, %v", step, key, got, ok)
					}
				}

				compareEntries(t, step, cm.Snapshot().Entries, ref.sorted(), frequency)
				if len(evicted) != len(wantEvicted) {
					t.Fatalf("step %d: %d evictions, want %d", step, len(evicted), len(wantEvicted))
				}
				for i := range wantEvicted {
					if evicted[i] != wantEvicted[i] {
						t.Fatalf("step %d: eviction %d was %+v, want %+v", step, i, evicted[i], wantEvicted[i])
					}
				}
			}
		})
	}
}
