package tests

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	jr_cache "jr_cache/code"
)

// formats and compressions are every combination the round-trip tests run.
var (
	formats      = []jr_cache.SerializationFormat{jr_cache.JSONFormat, jr_cache.GobFormat}
	compressions = []jr_cache.CompressionAlgorithm{
		jr_cache.NoCompression, jr_cache.GzipCompression, jr_cache.ZlibCompression,
	}
)

// forEachCodec runs fn for every format/compression pair.
func forEachCodec(t *testing.T, fn func(t *testing.T, format jr_cache.SerializationFormat, algorithm jr_cache.CompressionAlgorithm)) {
	t.Helper()
	for _, format := range formats {
		for _, algorithm := range compressions {
			t.Run(format.String()+"/"+algorithm.String(), func(t *testing.T) {
				fn(t, format, algorithm)
			})
		}
	}
}

func TestSerialize_RoundTrip(t *testing.T) {
	type payload struct {
		Name  string         `json:"name"`
		Count int            `json:"count"`
		Tags  []string       `json:"tags"`
		Meta  map[string]int `json:"meta"`
	}
	want := payload{Name: "cache", Count: 42, Tags: []string{"a", "b"}, Meta: map[string]int{"x": 1}}

	forEachCodec(t, func(t *testing.T, format jr_cache.SerializationFormat, algorithm jr_cache.CompressionAlgorithm) {
		data, err := jr_cache.Serialize(want, format, algorithm)
		if err != nil {
			t.Fatalf("Serialize error: %v", err)
		}
		if len(data) == 0 {
			t.Fatal("Serialize returned no bytes")
		}
		got, err := jr_cache.Deserialize[payload](data, format, algorithm)
		if err != nil {
			t.Fatalf("Deserialize error: %v", err)
		}
		if got.Name != want.Name || got.Count != want.Count || len(got.Tags) != 2 || got.Meta["x"] != 1 {
			t.Fatalf("round trip gave %+v, want %+v", got, want)
		}
	})
}

func TestSerialize_CompressionIsActuallyApplied(t *testing.T) {
	// A long repetitive payload must come out smaller once compressed, which
	// is only true if the compressor is reached at all.
	value := make([]int, 4000)
	plain, err := jr_cache.Serialize(value, jr_cache.JSONFormat, jr_cache.NoCompression)
	if err != nil {
		t.Fatal(err)
	}
	for _, algorithm := range []jr_cache.CompressionAlgorithm{jr_cache.GzipCompression, jr_cache.ZlibCompression} {
		squeezed, err := jr_cache.Serialize(value, jr_cache.JSONFormat, algorithm)
		if err != nil {
			t.Fatalf("%v: %v", algorithm, err)
		}
		if len(squeezed) >= len(plain) {
			t.Fatalf("%v produced %d bytes, no smaller than the %d uncompressed", algorithm, len(squeezed), len(plain))
		}
		back, err := jr_cache.Deserialize[[]int](squeezed, jr_cache.JSONFormat, algorithm)
		if err != nil {
			t.Fatalf("%v: Deserialize error: %v", algorithm, err)
		}
		if len(back) != len(value) {
			t.Fatalf("%v: round trip gave %d values, want %d", algorithm, len(back), len(value))
		}
	}
}

func TestSerialize_Errors(t *testing.T) {
	if _, err := jr_cache.Deserialize[int](nil, jr_cache.JSONFormat, jr_cache.NoCompression); !errors.Is(err, jr_cache.ErrNilData) {
		t.Fatalf("nil data: err = %v, want ErrNilData", err)
	}
	if _, err := jr_cache.Deserialize[int]([]byte{}, jr_cache.JSONFormat, jr_cache.NoCompression); !errors.Is(err, jr_cache.ErrEmptyData) {
		t.Fatalf("empty data: err = %v, want ErrEmptyData", err)
	}
	if _, err := jr_cache.Serialize(1, jr_cache.JSONFormat, jr_cache.CompressionAlgorithm(99)); !errors.Is(err, jr_cache.ErrUnsupportedCompression) {
		t.Fatalf("bad compression: err = %v, want ErrUnsupportedCompression", err)
	}
	if _, err := jr_cache.Serialize(1, jr_cache.SerializationFormat(99), jr_cache.NoCompression); !errors.Is(err, jr_cache.ErrUnsupportedFormat) {
		t.Fatalf("bad format: err = %v, want ErrUnsupportedFormat", err)
	}
	// An unknown algorithm on the way back must be an error, not a silent
	// pass-through of bytes that are still compressed.
	data, _ := jr_cache.Serialize(1, jr_cache.JSONFormat, jr_cache.NoCompression)
	if _, err := jr_cache.Deserialize[int](data, jr_cache.JSONFormat, jr_cache.CompressionAlgorithm(99)); !errors.Is(err, jr_cache.ErrUnsupportedCompression) {
		t.Fatalf("bad compression on decode: err = %v, want ErrUnsupportedCompression", err)
	}
	// Decoding with the wrong compression must fail rather than return junk.
	squeezed, _ := jr_cache.Serialize([]int{1, 2, 3}, jr_cache.JSONFormat, jr_cache.GzipCompression)
	if _, err := jr_cache.Deserialize[[]int](squeezed, jr_cache.JSONFormat, jr_cache.NoCompression); err == nil {
		t.Fatal("decoding gzip bytes as uncompressed succeeded")
	}
}

func TestSerialize_StructKeysNeedGob(t *testing.T) {
	type compositeKey struct {
		Region string
		ID     int
	}
	snapshot := &jr_cache.OrderedMapSnapshot[compositeKey, string]{
		Entries: []jr_cache.Entry[compositeKey, string]{
			{Key: compositeKey{"eu", 1}, Value: "first"},
			{Key: compositeKey{"us", 2}, Value: "second"},
		},
	}
	// Entries are a slice, not a map, so even JSON handles struct keys.
	for _, format := range formats {
		data, err := jr_cache.Serialize(snapshot, format, jr_cache.NoCompression)
		if err != nil {
			t.Fatalf("%v: Serialize error: %v", format, err)
		}
		back, err := jr_cache.Deserialize[*jr_cache.OrderedMapSnapshot[compositeKey, string]](data, format, jr_cache.NoCompression)
		if err != nil {
			t.Fatalf("%v: Deserialize error: %v", format, err)
		}
		if len(back.Entries) != 2 || back.Entries[0].Key.Region != "eu" || back.Entries[1].Value != "second" {
			t.Fatalf("%v: round trip gave %+v", format, back.Entries)
		}
	}
}

func TestSaveToFile_RoundTripAndAtomicity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.bin")

	forEachCodec(t, func(t *testing.T, format jr_cache.SerializationFormat, algorithm jr_cache.CompressionAlgorithm) {
		want := []string{"alpha", "beta", "gamma"}
		if err := jr_cache.SaveToFile(want, path, format, algorithm, nil); err != nil {
			t.Fatalf("SaveToFile error: %v", err)
		}
		got, err := jr_cache.LoadFromFile[[]string](path, format, algorithm)
		if err != nil {
			t.Fatalf("LoadFromFile error: %v", err)
		}
		if len(got) != 3 || got[0] != "alpha" || got[2] != "gamma" {
			t.Fatalf("round trip gave %v, want %v", got, want)
		}
		// No temporary files may be left behind.
		files, _ := os.ReadDir(dir)
		for _, f := range files {
			if f.Name() != "data.bin" {
				t.Fatalf("SaveToFile left %q behind", f.Name())
			}
		}
	})

	if _, err := jr_cache.LoadFromFile[[]string](filepath.Join(dir, "missing.bin"), jr_cache.JSONFormat, jr_cache.NoCompression); err == nil {
		t.Fatal("LoadFromFile on a missing file returned no error")
	}
}

func TestOrderedMap_SnapshotKeepsOrder(t *testing.T) {
	om := jr_cache.NewOrderedMap[string, int](false)
	for i, key := range []string{"a", "b", "c", "d"} {
		om.SetBottom(key, i)
	}
	om.MoveToTop("d") // d, a, b, c

	forEachCodec(t, func(t *testing.T, format jr_cache.SerializationFormat, algorithm jr_cache.CompressionAlgorithm) {
		data, err := jr_cache.Serialize(om.Snapshot(), format, algorithm)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := jr_cache.Deserialize[*jr_cache.OrderedMapSnapshot[string, int]](data, format, algorithm)
		if err != nil {
			t.Fatal(err)
		}
		restored, err := jr_cache.NewOrderedMapFromSnapshot(snapshot, false)
		if err != nil {
			t.Fatal(err)
		}
		assertOrder(t, restored, "d", "a", "b", "c")
		if v, ok := restored.Get("b"); !ok || v != 1 {
			t.Fatalf("Get(b) = %d, %v; want 1, true", v, ok)
		}
	})
}

func TestOrderedMap_ToMapFromMap(t *testing.T) {
	om := jr_cache.NewOrderedMap[string, int](true)
	om.SetBottom("a", 1)
	om.SetBottom("b", 2)

	got := om.ToMap()
	if len(got) != 2 || got["a"] != 1 || got["b"] != 2 {
		t.Fatalf("ToMap() = %v", got)
	}

	// FromMap on a locked map must not deadlock.
	other := jr_cache.NewOrderedMap[string, int](true)
	done := make(chan struct{})
	go func() {
		other.FromMap(got)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("FromMap deadlocked on a locked map")
	}
	if other.Len() != 2 {
		t.Fatalf("Len() after FromMap = %d, want 2", other.Len())
	}
}

func TestOrderedMap_RestoreSnapshotReplacesContents(t *testing.T) {
	om := jr_cache.NewOrderedMap[string, int](false)
	om.SetBottom("old", 99)
	snapshot := &jr_cache.OrderedMapSnapshot[string, int]{
		Entries: []jr_cache.Entry[string, int]{{Key: "new", Value: 1}},
	}
	if err := om.RestoreSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	if om.Len() != 1 || om.Has("old") {
		t.Fatalf("RestoreSnapshot did not replace the contents: Len = %d, Has(old) = %v", om.Len(), om.Has("old"))
	}
	if err := om.RestoreSnapshot(nil); !errors.Is(err, jr_cache.ErrNilSnapshot) {
		t.Fatalf("RestoreSnapshot(nil) = %v, want ErrNilSnapshot", err)
	}
}

func TestBucketMap_SnapshotKeepsBuckets(t *testing.T) {
	bm := jr_cache.NewBucketMap[string, int](false)
	bm.Set(1, "a", 1)
	bm.Set(1, "b", 2)
	bm.Set(5, "c", 3)
	bm.Set(3, "d", 4)

	forEachCodec(t, func(t *testing.T, format jr_cache.SerializationFormat, algorithm jr_cache.CompressionAlgorithm) {
		data, err := jr_cache.Serialize(bm.Snapshot(), format, algorithm)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := jr_cache.Deserialize[*jr_cache.BucketMapSnapshot[string, int]](data, format, algorithm)
		if err != nil {
			t.Fatal(err)
		}
		restored, err := jr_cache.NewBucketMapFromSnapshot(snapshot, false)
		if err != nil {
			t.Fatal(err)
		}
		assertIndices(t, restored, 1, 3, 5)
		for key, wantIndex := range map[string]uint64{"a": 1, "b": 1, "c": 5, "d": 3} {
			if index, ok := restored.GetBucketIndex(key); !ok || index != wantIndex {
				t.Fatalf("%q is in bucket %d, %v; want %d", key, index, ok, wantIndex)
			}
		}
		// Arrival order inside a bucket survives too.
		if k, _, _ := restored.Top(); k != "a" {
			t.Fatalf("Top() = %q, want a (oldest in the lowest bucket)", k)
		}
		checkInvariants(t, restored)
	})
}

func TestBucketMap_FromMapDoesNotDeadlock(t *testing.T) {
	bm := jr_cache.NewBucketMap[string, int](true)
	done := make(chan struct{})
	go func() {
		bm.FromMap(map[string]int{"a": 1, "b": 2})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("FromMap deadlocked on a locked bucket map")
	}
	if bm.Len() != 2 {
		t.Fatalf("Len() after FromMap = %d, want 2", bm.Len())
	}
	if got := bm.ToMap(); len(got) != 2 || got["a"] != 1 {
		t.Fatalf("ToMap() = %v", got)
	}
}

func TestCacheMap_SnapshotKeepsPolicyOrderAndTTL(t *testing.T) {
	for _, policy := range []jr_cache.EvictionPolicy{jr_cache.LRU, jr_cache.LFU, jr_cache.FIFO} {
		t.Run(policy.String(), func(t *testing.T) {
			cm := newCache(t, policy, 4)
			cm.Set("a", 1)
			cm.Set("b", 2)
			cm.Set("c", 3)
			cm.Get("a") // a is now most-recent / most-frequent
			cm.SetWithTTL("d", 4, time.Hour)

			wantTop, _, _ := cm.Top()
			wantBottom, _, _ := cm.Bottom()

			data, err := jr_cache.Serialize(cm.Snapshot(), jr_cache.GobFormat, jr_cache.GzipCompression)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := jr_cache.Deserialize[*jr_cache.CacheMapSnapshot[string, int]](data, jr_cache.GobFormat, jr_cache.GzipCompression)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := jr_cache.NewCacheMapFromSnapshot(snapshot)
			if err != nil {
				t.Fatal(err)
			}

			if restored.EvictionPolicy() != policy || restored.MaxCapacity() != 4 {
				t.Fatalf("config came back as %v, capacity %d", restored.EvictionPolicy(), restored.MaxCapacity())
			}
			if restored.Len() != 4 {
				t.Fatalf("Len() = %d, want 4", restored.Len())
			}
			if k, _, _ := restored.Top(); k != wantTop {
				t.Fatalf("Top() = %q, want %q (eviction order was not preserved)", k, wantTop)
			}
			if k, _, _ := restored.Bottom(); k != wantBottom {
				t.Fatalf("Bottom() = %q, want %q", k, wantBottom)
			}
			if ttl, ok := restored.GetTTL("d"); !ok || ttl <= 0 || ttl > time.Hour {
				t.Fatalf("GetTTL(d) = %v, %v; want a positive TTL", ttl, ok)
			}
			if _, ok := restored.GetTTL("a"); ok {
				t.Fatal("an entry that had no TTL came back with one")
			}
		})
	}
}

func TestCacheMap_SnapshotDropsExpiredEntries(t *testing.T) {
	cm := newCache(t, jr_cache.LRU, 10)
	cm.Set("live", 1)
	cm.SetWithTTL("dead", 2, shortTTL)
	waitForExpiry()

	snapshot := cm.Snapshot()
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Key != "live" {
		t.Fatalf("snapshot holds %+v, want only the live entry", snapshot.Entries)
	}
	// Snapshot must not mutate the cache: expiry stays lazy.
	if cm.Len() != 2 {
		t.Fatalf("Snapshot removed entries: Len() = %d, want 2", cm.Len())
	}

	// An entry whose deadline passed while the snapshot sat on disk is dropped.
	stale := &jr_cache.CacheMapSnapshot[string, int]{
		Eviction:    jr_cache.LRU,
		MaxCapacity: 10,
		Entries: []jr_cache.CacheEntry[string, int]{
			{Key: "fresh", Value: 1, ExpiresAt: time.Now().Add(time.Hour)},
			{Key: "stale", Value: 2, ExpiresAt: time.Now().Add(-time.Hour)},
		},
	}
	restored, err := jr_cache.NewCacheMapFromSnapshot(stale)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Len() != 1 || !restored.Has("fresh") || restored.Has("stale") {
		t.Fatalf("Len() = %d, Has(fresh) = %v, Has(stale) = %v", restored.Len(), restored.Has("fresh"), restored.Has("stale"))
	}
}

func TestCacheMap_ToMapDoesNotMutateAndFromMapDoesNotDeadlock(t *testing.T) {
	cm := newCache(t, jr_cache.LRU, 10)
	cm.Set("live", 1)
	cm.SetWithTTL("dead", 2, shortTTL)
	waitForExpiry()

	got := cm.ToMap()
	if len(got) != 1 || got["live"] != 1 {
		t.Fatalf("ToMap() = %v, want only the live entry", got)
	}
	// Reading must not evict: ToMap used to purge under a read lock.
	if cm.Len() != 2 {
		t.Fatalf("ToMap removed entries: Len() = %d, want 2", cm.Len())
	}

	locked, err := jr_cache.NewCacheMap[string, int](jr_cache.CacheMapConfig{
		Eviction: jr_cache.LRU, MaxCapacity: 10, Locked: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		locked.FromMap(map[string]int{"a": 1, "b": 2})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("FromMap deadlocked on a locked cache")
	}
	if locked.Len() != 2 {
		t.Fatalf("Len() after FromMap = %d, want 2", locked.Len())
	}
}

func TestCacheMap_SaveAndLoadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.gob")
	cm := newCache(t, jr_cache.LFU, 5)
	cm.Set("a", 1)
	cm.Set("b", 2)
	cm.Get("b")
	cm.Get("b")

	if err := cm.SaveToFile(path, jr_cache.GobFormat, jr_cache.ZlibCompression, nil); err != nil {
		t.Fatalf("SaveToFile error: %v", err)
	}
	restored, err := jr_cache.LoadCacheMapFromFile[string, int](path, jr_cache.GobFormat, jr_cache.ZlibCompression)
	if err != nil {
		t.Fatalf("LoadCacheMapFromFile error: %v", err)
	}
	if restored.Len() != 2 || restored.EvictionPolicy() != jr_cache.LFU {
		t.Fatalf("Len() = %d, policy = %v", restored.Len(), restored.EvictionPolicy())
	}
	// b was read twice, so it must still be the most frequently used.
	if k, _, _ := restored.Bottom(); k != "b" {
		t.Fatalf("Bottom() = %q, want b (frequency was not preserved)", k)
	}

	// Loading into an existing cache replaces its contents.
	target := newCache(t, jr_cache.LFU, 5)
	target.Set("old", 99)
	if err := target.LoadFromFile(path, jr_cache.GobFormat, jr_cache.ZlibCompression); err != nil {
		t.Fatal(err)
	}
	if target.Has("old") || target.Len() != 2 {
		t.Fatalf("LoadFromFile did not replace the contents: Has(old) = %v, Len = %d", target.Has("old"), target.Len())
	}
}

func TestShardedMap_SnapshotRoundTrip(t *testing.T) {
	sm := newShardedMap(t, 4)
	for k := 0; k < 20; k++ {
		sm.Set(k, k*k)
	}

	forEachCodec(t, func(t *testing.T, format jr_cache.SerializationFormat, algorithm jr_cache.CompressionAlgorithm) {
		data, err := jr_cache.Serialize(sm.Snapshot(), format, algorithm)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := jr_cache.Deserialize[*jr_cache.ShardedMapSnapshot[int, int]](data, format, algorithm)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.ShardCount != 4 {
			t.Fatalf("ShardCount = %d, want 4", snapshot.ShardCount)
		}
		restored, err := jr_cache.NewShardedMapFromSnapshot(snapshot, jr_cache.ShardedMapConfig[int]{})
		if err != nil {
			t.Fatal(err)
		}
		if restored.Len() != 20 || restored.ShardCount() != 4 {
			t.Fatalf("Len() = %d, ShardCount() = %d", restored.Len(), restored.ShardCount())
		}
		for k := 0; k < 20; k++ {
			if v, ok := restored.Get(k); !ok || v != k*k {
				t.Fatalf("Get(%d) = %d, %v; want %d, true", k, v, ok, k*k)
			}
		}
	})
}

func TestShardedCacheMap_SnapshotRoundTrip(t *testing.T) {
	for _, global := range []bool{false, true} {
		name := "per-shard"
		if global {
			name = "global-order"
		}
		t.Run(name, func(t *testing.T) {
			scm, err := jr_cache.NewShardedCacheMap[int, int](jr_cache.ShardedCacheMapConfig[int]{
				Eviction: jr_cache.LRU, MaxCapacity: 40, ShardCount: 4, GlobalOrder: global,
			})
			if err != nil {
				t.Fatal(err)
			}
			for k := 0; k < 20; k++ {
				scm.Set(k, k*2)
			}
			scm.SetWithTTL(100, 100, time.Hour)

			path := filepath.Join(t.TempDir(), "sharded.bin")
			if err := scm.SaveToFile(path, jr_cache.GobFormat, jr_cache.GzipCompression, nil); err != nil {
				t.Fatalf("SaveToFile error: %v", err)
			}
			restored, err := jr_cache.LoadShardedCacheMapFromFile[int, int](path, jr_cache.GobFormat, jr_cache.GzipCompression, nil)
			if err != nil {
				t.Fatalf("LoadShardedCacheMapFromFile error: %v", err)
			}

			if restored.Len() != 21 {
				t.Fatalf("Len() = %d, want 21", restored.Len())
			}
			if restored.GlobalOrder() != global || restored.ShardCount() != 4 || restored.MaxCapacity() != 40 {
				t.Fatalf("config came back as global=%v shards=%d capacity=%d",
					restored.GlobalOrder(), restored.ShardCount(), restored.MaxCapacity())
			}
			for k := 0; k < 20; k++ {
				if v, ok := restored.Get(k); !ok || v != k*2 {
					t.Fatalf("Get(%d) = %d, %v; want %d, true", k, v, ok, k*2)
				}
			}
			if ttl, ok := restored.GetTTL(100); !ok || ttl <= 0 {
				t.Fatalf("GetTTL(100) = %v, %v; want a positive TTL", ttl, ok)
			}
		})
	}
}

func TestSnapshot_NilIsRejectedEverywhere(t *testing.T) {
	om := jr_cache.NewOrderedMap[string, int](false)
	if err := om.RestoreSnapshot(nil); !errors.Is(err, jr_cache.ErrNilSnapshot) {
		t.Fatalf("OrderedMap: %v", err)
	}
	bm := jr_cache.NewBucketMap[string, int](false)
	if err := bm.RestoreSnapshot(nil); !errors.Is(err, jr_cache.ErrNilSnapshot) {
		t.Fatalf("BucketMap: %v", err)
	}
	cm := newCache(t, jr_cache.LRU, 4)
	if err := cm.RestoreSnapshot(nil); !errors.Is(err, jr_cache.ErrNilSnapshot) {
		t.Fatalf("CacheMap: %v", err)
	}
	if _, err := jr_cache.NewCacheMapFromSnapshot[string, int](nil); !errors.Is(err, jr_cache.ErrNilSnapshot) {
		t.Fatalf("NewCacheMapFromSnapshot: %v", err)
	}
	sm := newShardedMap(t, 2)
	if err := sm.RestoreSnapshot(nil); !errors.Is(err, jr_cache.ErrNilSnapshot) {
		t.Fatalf("ShardedMap: %v", err)
	}
	scm := newShardedCache(t, jr_cache.LRU, 8, 2)
	if err := scm.RestoreSnapshot(nil); !errors.Is(err, jr_cache.ErrNilSnapshot) {
		t.Fatalf("ShardedCacheMap: %v", err)
	}
}
