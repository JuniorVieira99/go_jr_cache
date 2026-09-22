// Package compare benchmarks jr_cache against other Go in-memory caches
// behind one minimal adapter, so every library runs the exact same workload.
//
// It is a separate module (see go.mod) so the third-party dependencies never
// touch the library's own go.mod. Run it from this directory:
//
//	go test -run xxx -bench . -benchmem
//	go test -run HitRatio -v
package compare

import (
	"context"
	"encoding/binary"
	"time"

	"github.com/Yiling-J/theine-go"
	"github.com/allegro/bigcache/v3"
	"github.com/bluele/gcache"
	"github.com/coocood/freecache"
	"github.com/dgraph-io/ristretto/v2"
	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/maypok86/otter/v2"
	cmap "github.com/orcaman/concurrent-map/v2"
	gocache "github.com/patrickmn/go-cache"

	jr_cache "jr_cache/code"
)

// cache is the least common denominator every library is driven through.
type cache interface {
	Set(key string, value int)
	Get(key string) (int, bool)
	// Sync blocks until buffered writes are visible (a no-op for most).
	Sync()
	Close()
}

// candidate describes one library configuration under test.
type candidate struct {
	name string
	// bounded reports whether the library evicts at the requested capacity;
	// unbounded ones only take part in benchmarks that never exceed it.
	bounded bool
	// approximate marks libraries whose capacity is a byte budget rather
	// than an entry count, so the bound is only roughly the requested one.
	approximate bool
	build       func(capacity int) cache
}

// candidates lists every configuration, jr_cache first.
var candidates = []candidate{
	{"jr_cache/CacheMap-LRU", true, false, func(n int) cache { return newJR(jr_cache.LRU, n) }},
	{"jr_cache/CacheMap-LFU", true, false, func(n int) cache { return newJR(jr_cache.LFU, n) }},
	{"jr_cache/Sharded-LRU", true, false, func(n int) cache { return newJRSharded(jr_cache.LRU, n, false) }},
	{"jr_cache/Sharded-LFU", true, false, func(n int) cache { return newJRSharded(jr_cache.LFU, n, false) }},
	{"jr_cache/Sharded-LRU-global", true, false, func(n int) cache { return newJRSharded(jr_cache.LRU, n, true) }},
	{"hashicorp/golang-lru", true, false, newGolangLRU},
	{"hashicorp/golang-lru-2Q", true, false, newGolangLRU2Q},
	{"dgraph-io/ristretto", true, false, newRistretto},
	{"maypok86/otter", true, false, newOtter},
	{"Yiling-J/theine", true, false, newTheine},
	{"bluele/gcache-LRU", true, false, func(n int) cache { return newGCache(n, false) }},
	{"bluele/gcache-LFU", true, false, func(n int) cache { return newGCache(n, true) }},
	{"allegro/bigcache", true, true, newBigCache},
	{"coocood/freecache", true, true, newFreeCache},
	{"patrickmn/go-cache", false, false, newGoCache},
	{"orcaman/concurrent-map", false, false, newConcurrentMap},
	{"jr_cache/ShardedMap", false, false, newJRShardedMap},
}

// shardCount is used by every sharded configuration.
const shardCount = 16

// --- jr_cache ---------------------------------------------------------------

type jrCache struct {
	c *jr_cache.CacheMap[string, int]
}

func newJR(policy jr_cache.EvictionPolicy, n int) cache {
	c, err := jr_cache.NewCacheMap[string, int](jr_cache.CacheMapConfig{
		Eviction: policy, MaxCapacity: uint64(n), Locked: true,
	})
	if err != nil {
		panic(err)
	}
	return jrCache{c}
}
func (j jrCache) Set(k string, v int)      { j.c.Set(k, v) }
func (j jrCache) Get(k string) (int, bool) { return j.c.Get(k) }
func (j jrCache) Sync()                    {}
func (j jrCache) Close()                   {}

type jrSharded struct {
	c *jr_cache.ShardedCacheMap[string, int]
}

func newJRSharded(policy jr_cache.EvictionPolicy, n int, global bool) cache {
	c, err := jr_cache.NewShardedCacheMap[string, int](jr_cache.ShardedCacheMapConfig[string]{
		Eviction: policy, MaxCapacity: uint64(n), ShardCount: shardCount, Locked: true, GlobalOrder: global,
	})
	if err != nil {
		panic(err)
	}
	return jrSharded{c}
}
func (j jrSharded) Set(k string, v int)      { j.c.Set(k, v) }
func (j jrSharded) Get(k string) (int, bool) { return j.c.Get(k) }
func (j jrSharded) Sync()                    {}
func (j jrSharded) Close()                   {}

type jrShardedMap struct {
	c *jr_cache.ShardedMap[string, int]
}

func newJRShardedMap(int) cache {
	c, err := jr_cache.NewShardedMap[string, int](jr_cache.ShardedMapConfig[string]{ShardCount: shardCount})
	if err != nil {
		panic(err)
	}
	return jrShardedMap{c}
}
func (j jrShardedMap) Set(k string, v int)      { j.c.Set(k, v) }
func (j jrShardedMap) Get(k string) (int, bool) { return j.c.Get(k) }
func (j jrShardedMap) Sync()                    {}
func (j jrShardedMap) Close()                   {}

// --- hashicorp/golang-lru ---------------------------------------------------

type golangLRU struct{ c *lru.Cache[string, int] }

func newGolangLRU(n int) cache {
	c, err := lru.New[string, int](n)
	if err != nil {
		panic(err)
	}
	return golangLRU{c}
}
func (g golangLRU) Set(k string, v int)      { g.c.Add(k, v) }
func (g golangLRU) Get(k string) (int, bool) { return g.c.Get(k) }
func (g golangLRU) Sync()                    {}
func (g golangLRU) Close()                   {}

type golangLRU2Q struct {
	c *lru.TwoQueueCache[string, int]
}

func newGolangLRU2Q(n int) cache {
	c, err := lru.New2Q[string, int](n)
	if err != nil {
		panic(err)
	}
	return golangLRU2Q{c}
}
func (g golangLRU2Q) Set(k string, v int)      { g.c.Add(k, v) }
func (g golangLRU2Q) Get(k string) (int, bool) { return g.c.Get(k) }
func (g golangLRU2Q) Sync()                    {}
func (g golangLRU2Q) Close()                   {}

// --- dgraph-io/ristretto ----------------------------------------------------

type ristrettoCache struct{ c *ristretto.Cache[string, int] }

func newRistretto(n int) cache {
	c, err := ristretto.NewCache(&ristretto.Config[string, int]{
		NumCounters: int64(n) * 10, // as recommended by the ristretto docs
		MaxCost:     int64(n),
		BufferItems: 64,
		// Count entries, not entries plus their ~50 bytes of metadata;
		// otherwise MaxCost n holds only a few hundred items.
		IgnoreInternalCost: true,
	})
	if err != nil {
		panic(err)
	}
	return ristrettoCache{c}
}
func (r ristrettoCache) Set(k string, v int)      { r.c.Set(k, v, 1) }
func (r ristrettoCache) Get(k string) (int, bool) { return r.c.Get(k) }
func (r ristrettoCache) Sync()                    { r.c.Wait() }
func (r ristrettoCache) Close()                   { r.c.Close() }

// --- maypok86/otter ---------------------------------------------------------

type otterCache struct{ c *otter.Cache[string, int] }

func newOtter(n int) cache {
	return otterCache{otter.Must(&otter.Options[string, int]{MaximumSize: n})}
}
func (o otterCache) Set(k string, v int)      { o.c.Set(k, v) }
func (o otterCache) Get(k string) (int, bool) { return o.c.GetIfPresent(k) }
func (o otterCache) Sync()                    {}
func (o otterCache) Close()                   {}

// --- Yiling-J/theine --------------------------------------------------------

type theineCache struct{ c *theine.Cache[string, int] }

func newTheine(n int) cache {
	c, err := theine.NewBuilder[string, int](int64(n)).Build()
	if err != nil {
		panic(err)
	}
	return theineCache{c}
}
func (t theineCache) Set(k string, v int)      { t.c.Set(k, v, 1) }
func (t theineCache) Get(k string) (int, bool) { return t.c.Get(k) }
func (t theineCache) Sync()                    {}
func (t theineCache) Close()                   { t.c.Close() }

// --- bluele/gcache ----------------------------------------------------------

type gcacheCache struct{ c gcache.Cache }

func newGCache(n int, lfu bool) cache {
	b := gcache.New(n)
	if lfu {
		b = b.LFU()
	} else {
		b = b.LRU()
	}
	return gcacheCache{b.Build()}
}
func (g gcacheCache) Set(k string, v int) { _ = g.c.Set(k, v) }
func (g gcacheCache) Get(k string) (int, bool) {
	v, err := g.c.GetIFPresent(k)
	if err != nil {
		return 0, false
	}
	return v.(int), true
}
func (g gcacheCache) Sync()  {}
func (g gcacheCache) Close() {}

// --- byte-oriented caches: allegro/bigcache, coocood/freecache -------------

// entryBytes is the rough per-entry footprint (header + ~10-byte key +
// 8-byte value) used to turn an entry count into a byte budget for the
// byte-oriented caches. bigcache additionally rounds its hard limit up to
// whole megabytes, so at small capacities it holds more than requested.
const entryBytes = 40

func encode(v int) []byte {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], uint64(v))
	return b[:]
}

func decode(b []byte) int { return int(binary.LittleEndian.Uint64(b)) }

type bigCache struct{ c *bigcache.BigCache }

func newBigCache(n int) cache {
	cfg := bigcache.DefaultConfig(time.Hour)
	cfg.Shards = shardCount
	cfg.MaxEntriesInWindow = n
	cfg.MaxEntrySize = entryBytes
	cfg.HardMaxCacheSize = max(1, n*entryBytes/(1<<20)) // MB
	cfg.Verbose = false
	c, err := bigcache.New(context.Background(), cfg)
	if err != nil {
		panic(err)
	}
	return bigCache{c}
}
func (b bigCache) Set(k string, v int) { _ = b.c.Set(k, encode(v)) }
func (b bigCache) Get(k string) (int, bool) {
	v, err := b.c.Get(k)
	if err != nil {
		return 0, false
	}
	return decode(v), true
}
func (b bigCache) Sync()  {}
func (b bigCache) Close() { _ = b.c.Close() }

type freeCache struct{ c *freecache.Cache }

func newFreeCache(n int) cache {
	return freeCache{freecache.NewCache(n * entryBytes)}
}
func (f freeCache) Set(k string, v int) { _ = f.c.Set([]byte(k), encode(v), 0) }
func (f freeCache) Get(k string) (int, bool) {
	v, err := f.c.Get([]byte(k))
	if err != nil {
		return 0, false
	}
	return decode(v), true
}
func (f freeCache) Sync()  {}
func (f freeCache) Close() {}

// --- unbounded maps: patrickmn/go-cache, orcaman/concurrent-map -----------

type goCache struct{ c *gocache.Cache }

func newGoCache(int) cache            { return goCache{gocache.New(gocache.NoExpiration, 0)} }
func (g goCache) Set(k string, v int) { g.c.Set(k, v, gocache.NoExpiration) }
func (g goCache) Get(k string) (int, bool) {
	v, ok := g.c.Get(k)
	if !ok {
		return 0, false
	}
	return v.(int), true
}
func (g goCache) Sync()  {}
func (g goCache) Close() {}

type concurrentMap struct {
	c cmap.ConcurrentMap[string, int]
}

func newConcurrentMap(int) cache                 { return concurrentMap{cmap.New[int]()} }
func (c concurrentMap) Set(k string, v int)      { c.c.Set(k, v) }
func (c concurrentMap) Get(k string) (int, bool) { return c.c.Get(k) }
func (c concurrentMap) Sync()                    {}
func (c concurrentMap) Close()                   {}
