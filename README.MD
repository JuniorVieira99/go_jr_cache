# jr_cache

Generic, in-memory caching primitives for Go: an ordered map, a frequency
bucket map, a bounded cache with six eviction policies and per-entry TTLs,
and sharded variants of both the map and the cache — all sharing one API.

![Go](https://img.shields.io/badge/go-1.26-00ADD8?logo=go) ![Generics](https://img.shields.io/badge/generics-yes-success) ![Dependencies](https://img.shields.io/badge/dependencies-none-success) ![License](https://img.shields.io/badge/license-MIT-blue)

## Index

- [Overview](#overview)
- [Features](#features)
- [Installation](#installation)
- [Quick start](#quick-start)
- [Structures](#structures)
  - [The shared API](#the-shared-api)
  - [`CacheMap`](#cachemap)
  - [`ShardedCacheMap`](#shardedcachemap)
  - [`ShardedMap`](#shardedmap)
  - [`OrderedMap`](#orderedmap)
  - [`BucketMap`](#bucketmap)
  - [`GCManager`](#gcmanager)
- [Eviction policies](#eviction-policies)
- [TTL semantics](#ttl-semantics)
- [Concurrency](#concurrency)
- [Comparison with other libraries](#comparison-with-other-libraries)
- [Benchmarks](#benchmarks)
- [Design notes and caveats](#design-notes-and-caveats)
- [Testing](#testing)
- [Project layout](#project-layout)
- [Roadmap](#roadmap)
- [Contributing](#contributing)
- [License](#license)

## Overview

Most cache libraries hand you one finished cache. `jr_cache` hands you the
finished cache *and* the pieces it is made of, so you can pick the layer you
need:

| You need… | Use |
|---|---|
| A bounded cache with LRU / LFU / FIFO / … eviction and TTLs | `CacheMap` |
| The same, but hammered by many goroutines | `ShardedCacheMap` |
| A concurrent map with no eviction | `ShardedMap` |
| An insertion-ordered map with O(1) move-to-front / move-to-back | `OrderedMap` |
| Keys grouped by a counter (frequency, priority, generation) | `BucketMap` |
| A background sweeper on a timer, memory or date trigger | `GCManager` |

Every structure exposes the same method names — `Get`, `Set`, `Has`,
`Update`, `Pop`, `Delete`, `Top`, `Bottom`, `PopTop`, `PopBottom`, `Len`,
`Clear` — so swapping one for another is a type change, not a rewrite.

## Features

- **Six eviction policies**: LRU, MRU, FIFO, LIFO, LFU, MFU. LFU/MFU break
  ties by recency (classic LFU with LRU tie-break).
- **Per-entry TTL** with lazy expiry, `GetTTL` / `UpdateTTL` / `RemoveTTL`,
  and a `DeleteExpired` sweep.
- **Sharding** by key hash for both the plain map and the cache, with a
  pluggable hash function and `hash/maphash` as the default for any
  comparable key.
- **Generics** end to end: `CacheMap[string, []byte]`, `OrderedMap[int, MyStruct]`, …
- **Opt-in locking**: every structure takes a `Locked` flag; unlocked
  structures have zero synchronisation overhead.
- **Ordered access**: `Top` / `Bottom` / `PopTop` / `PopBottom` give the
  "least" and "most" end of every structure, whatever the policy.
- **Global or per-shard eviction** on the sharded cache: exact LRU/LFU
  across every shard behind one shared order lock, or fully independent
  shards for maximum scaling.
- **Eviction hook** (`SetOnEvict`) called with the key and value of every
  entry the cache evicts or expires on its own.
- **Background sweeper** (`GCManager`) that collects on an interval, a
  memory threshold, an elapsed-time threshold or a one-shot date — and
  works on anything, not only these caches. It can also switch Go's own
  collector off and back on for a bounded latency-critical section.
- **Atomic read-modify-write** on the sharded map (`Modify`) and bulk
  `Populate` that fills each shard under a single lock.
- **Zero dependencies** outside the standard library.
- **Compile-time interface checks** on every type, and a benchmark suite
  covering every operation at 500 → 50,000 entries, plus a
  [comparison against nine other Go caches](docs/comparison.md).

## Installation

The module is named `jr_cache` and the package lives in `code/`, so it is
imported with an alias:

```go
import jr_cache "jr_cache/code"
```

Until it is published under a VCS path, use it as a local module from your
own project's `go.mod`:

```
require jr_cache v0.0.0
replace jr_cache => ../jr_cache
```

To publish it, rename the module (for example `module github.com/you/jr_cache`)
and consumers can `go get` it as usual. Go 1.26 or newer is required
(`maphash.Comparable`, `clear`).

## Quick start

```go
package main

import (
	"fmt"
	"time"

	jr_cache "jr_cache/code"
)

func main() {
	cache, err := jr_cache.NewCacheMap[string, int](jr_cache.CacheMapConfig{
		Eviction:    jr_cache.LRU,
		MaxCapacity: 1000,
		Locked:      true, // safe to share between goroutines
	})
	if err != nil {
		panic(err)
	}

	cache.Set("a", 1)
	cache.SetWithTTL("session", 42, 10*time.Minute)

	if v, ok := cache.Get("a"); ok { // records an access: "a" is now most recent
		fmt.Println(v)
	}

	v, loaded := cache.SetOrGet("b", 2) // 2, false — inserted
	v, loaded = cache.SetOrGet("b", 3)  // 2, true  — already there
	fmt.Println(v, loaded)

	key, value, ok := cache.Top() // the least recently used entry, without touching it
	fmt.Println(key, value, ok)

	cache.Delete("a")
	fmt.Println(cache.Len(), cache.MaxCapacity())
}
```

A sharded cache for many goroutines is the same API with a shard count:

```go
sc, err := jr_cache.NewShardedCacheMap[string, []byte](jr_cache.ShardedCacheMapConfig[string]{
	Eviction:    jr_cache.LFU,
	MaxCapacity: 100_000, // split evenly across the shards
	ShardCount:  16,
	Locked:      true,
	// Hash: func(k string) uint64 { ... }  // optional; maphash by default
})
```

## Structures

### The shared API

All five types implement `CommonMapInterface` (or, for the unordered
`ShardedMap`, everything in it that does not need an order):

| Method | Meaning |
|---|---|
| `Get(key) (Value, bool)` | Read; on a cache this counts as an access |
| `Has(key) bool` | Presence check; never counts as an access |
| `Update(key, value) bool` | Replace the value in place; keeps order / frequency / TTL |
| `Pop(key) (Value, bool)` / `Delete(key)` | Remove |
| `Top() / Bottom() (Key, Value, bool)` | Peek at either end without touching it |
| `TopKey() / BottomKey() (Key, bool)` | Same, key only |
| `PopTop() / PopBottom() (Key, Value, bool)` | Remove from either end |
| `Len() int` / `Clear()` | Size and reset |

**Top is always the "least" end and Bottom the "most" end**: oldest / least
recently used / least frequently used at the top; newest / most recent /
most frequent at the bottom. That holds for every policy, so
`cache.PopTop()` is "evict one" under LRU, FIFO and LFU, and
`cache.PopBottom()` is "evict one" under MRU, LIFO and MFU.

### `CacheMap`

A bounded key-value store. Recency policies keep entries in an `OrderedMap`,
frequency policies in a `BucketMap` keyed by access count.

```go
cm, _ := jr_cache.NewCacheMap[int, string](jr_cache.CacheMapConfig{Eviction: jr_cache.LFU, MaxCapacity: 3})
cm.Set(1, "a"); cm.Set(2, "b"); cm.Set(3, "c")
cm.Get(1); cm.Get(1); cm.Get(2)      // frequencies: 1→3, 2→2, 3→1
cm.Set(4, "d")                       // full: evicts key 3 (least frequent)
cm.SetMaxCapacity(2)                 // shrinking evicts down to the new size
cm.Populate(map[int]string{5: "e"})  // bulk insert under one lock
```

- `Set` on an existing key updates the value **and** counts as an access
  (moves to most-recent / bumps frequency). `Update` changes only the value.
- `Set` without a TTL clears any TTL the key had; `Update` keeps it.
- `SetOrGet` is `sync.Map.LoadOrStore`: returns the existing value and
  `true`, or stores and returns `false`.
- `SetOnEvict(func(key, value))` installs a hook that is called for every
  entry the cache removes **on its own** — a capacity eviction or an
  expired entry being purged — but not for `Delete`, `Pop`, `PopTop`,
  `PopBottom` or `Clear`. It runs under the cache's lock, so it must not
  call back into the cache. The sharded cache has the same method.

### `ShardedCacheMap`

`ShardCount` independent `CacheMap`s selected by `hash(key) % ShardCount`.
Each shard gets `MaxCapacity / ShardCount` slots (the remainder goes to the
first shards, so the total is exact) and evicts on its own. `ShardCount`
must be between 1 and `MaxCapacity`.

```go
sc.Set("k", v)                              // routes to one shard
sc.PopulateThreaded(entries, runtime.NumCPU()) // partitions by shard, one lock per shard
sc.ShardIndex("k")                          // which shard "k" lives in
```

By default each shard evicts on its own, and `Top`, `Bottom`, `PopTop`,
`PopBottom` act on the **fullest** shard while `Len` sums every shard —
see [caveats](#design-notes-and-caveats). Set `GlobalOrder: true` for
exact behaviour across the whole cache:

```go
sc, _ := jr_cache.NewShardedCacheMap[string, []byte](jr_cache.ShardedCacheMapConfig[string]{
	Eviction: jr_cache.LRU, MaxCapacity: 100_000, ShardCount: 16, Locked: true,
	GlobalOrder: true,
})
```

With a global order one `CacheMap[Key, struct{}]` holds the order, capacity
and TTLs of every key behind a single lock, and the shards hold only the
values. Eviction is then the true global LRU/LFU, `Top`/`Bottom` are exact,
`Len` is O(1), and the capacity is no longer split (so `ShardCount` may
exceed `MaxCapacity`). The price is that every operation takes the shared
lock briefly; value reads and writes still spread across the shard locks.
Pick per-shard order for throughput, global order for hit rate.

### `ShardedMap`

A concurrent map with no order and no eviction; the same sharding without
the cache on top.

```go
sm, _ := jr_cache.NewShardedMap[string, int](jr_cache.ShardedMapConfig[string]{ShardCount: 32})
sm.Set("hits", 0)
sm.Modify("hits", func(v int, exists bool) (int, bool) { return v + 1, true }) // atomic
sm.Range(func(k string, v int) bool { fmt.Println(k, v); return true })
sm.Populate(bigMap, 8) // partitions by shard first, then fills each under one lock
```

`Modify` runs its callback under the shard's write lock — return `false` to
delete the key. `Range` snapshots each shard before visiting it, so the
callback may safely call back into the map.

### `OrderedMap`

A hash map plus a doubly linked list: O(1) lookup, O(1) insert at either
end or next to any key, O(1) move to either end.

```go
om := jr_cache.NewOrderedMap[string, int](false) // false: no locking
om.SetBottom("first", 1)
om.SetBottom("second", 2)
om.SetTop("zero", 0)                      // zero, first, second
om.SetAfter("first", "1.5", 15)           // zero, first, 1.5, second
om.MoveToBottom("zero")                   // first, 1.5, second, zero
for k, ok := om.TopKey(); ok; k, ok = om.NextKey(k) {
	v, _ := om.Get(k)
	fmt.Println(k, v)
}
```

### `BucketMap`

Keys grouped into buckets by a `uint64` index (a frequency, a priority, a
generation), with buckets kept in ascending index order and each bucket's
entries in arrival order. Empty buckets are removed immediately, so `Top`
is always a real entry: lowest index, then oldest arrival.

```go
bm := jr_cache.NewBucketMap[string, int](false)
bm.Set(1, "a", 10)                 // bucket 1
bm.Set(1, "b", 20)
idx, _ := bm.GetBucketIndex("a")
bm.Set(idx+1, "a", 10)             // bump "a" to bucket 2
k, v, ok := bm.Top()               // "b" — lowest bucket, oldest there
bm.WarmUp(64)                      // pre-allocate buckets for bursty workloads
```

Buckets are pooled: emptied buckets are kept for reuse (`DefaultWarmBuckets`
= 16, grow it with `WarmUp`), so moving a key between buckets does not
allocate a bucket.

### `GCManager`

Expiry in `CacheMap` is lazy, so entries nobody touches keep their slot
until they are evicted or swept. `GCManager` runs that sweep on a
background goroutine:

```go
gc, err := jr_cache.NewCacheGCManager(cache, jr_cache.GCConfig{
	Interval:        30 * time.Second, // how often to check
	TimeThreshold:   5 * time.Minute,  // ...but collect at most this often
	MemoryThreshold: 512 << 20,        // or as soon as the heap passes 512 MB
	// DateThreshold: t,               // or once, at a given instant
})
if err != nil {
	panic(err)
}
gc.Start()
defer gc.Stop()      // ends the goroutine and waits for it

n := gc.GCCollect()  // sweep right now, whatever the triggers say
stats := gc.Stats()  // Collections, Released, LastRun, Running
```

On each tick the manager collects if **any** enabled trigger fires; a zero
value disables that trigger, and with none set it collects every tick. The
date trigger fires once (`DateThresholdFired` reports it, `SetDateThreshold`
re-arms it). Every setter is safe while running — `SetInterval` resets the
running ticker.

`DisableGCCompletely` stops the sweeper **and** turns Go's own collector off
(`SetGCPercent(-1)` plus a `math.MaxInt64` memory limit, since a memory limit
triggers collections on its own). It returns the previous settings, and the
manager remembers them:

```go
percent, limit := gc.DisableGCCompletely()
defer gc.RestoreGC()                       // or gc.EnableGCCompletely(percent, limit)
// ... latency-critical section with no GC pauses ...
```

Read the warnings before reaching for it: these are **process-global**
runtime settings, so a library must not flip them on a caller's behalf, and
with the collector off **nothing is ever reclaimed** — the heap only grows.
It is for a bounded piece of work, not a mode to run in. `GCDisabled()`
reports the state; a second `DisableGCCompletely` is a no-op that returns the
same saved settings rather than saving "already off" over them; and
`EnableGCCompletely` restarts the sweeper only if it was running when the
collector was turned off.

Whether it is worth it is measured in
[docs/benchmark.md](docs/benchmark.md#turning-the-collector-off): on a
cache-churn workload it is roughly a wash — around 10 % faster at best,
sometimes slower — for hundreds of megabytes of heap. Raising `GOGC` is the
better lever.

It is not cache-specific: `NewGCManager(object, collect, config)` takes any
object and any `func(Object) int` that performs one collection and returns
how many items it released, so the same timer/memory/date logic drives a
connection pool, a temp-file directory or anything else. `NewCacheGCManager`
and `NewShardedCacheGCManager` are thin wrappers that pass `DeleteExpired`.
The collect function runs **without** the manager's lock, so it may call
back into the manager — but calling `Stop` from inside it is a caller
error.

## Eviction policies

| Policy | Evicts | Access (`Get`, `Set` on existing key, `SetOrGet` hit) | Backed by |
|---|---|---|---|
| `LRU` | least recently used (top) | moves the key to the bottom | `OrderedMap` |
| `MRU` | most recently used (bottom) | moves the key to the bottom | `OrderedMap` |
| `FIFO` | oldest inserted (top) | no effect | `OrderedMap` |
| `LIFO` | newest inserted (bottom) | no effect | `OrderedMap` |
| `LFU` | lowest frequency, then oldest at that frequency (top) | frequency + 1 | `BucketMap` |
| `MFU` | highest frequency, then newest at that frequency (bottom) | frequency + 1 | `BucketMap` |

`EvictionPolicy` has `String()` and `IsValid()`; an unknown policy makes the
constructor return `ErrInvalidEvictionPolicy`.

## TTL semantics

```go
cache.SetWithTTL("k", v, 5*time.Minute)
remaining, ok := cache.GetTTL("k")   // time left; false if no TTL, absent, or expired
cache.UpdateTTL("k", time.Hour)      // new deadline from now; works on keys without a TTL too
cache.RemoveTTL("k")                 // persist until evicted
n := cache.DeleteExpired()           // sweep; returns how many were removed
```

- Expiry is **lazy**: an expired entry is removed the next time any
  operation touches it, and is never returned or reported by `Has`.
  `Top`/`Bottom`/`PopTop`/`PopBottom` skip past expired entries.
- A TTL `<= 0` expires the entry immediately.
- `Set` on an expired key inserts it fresh (frequency 1 / newest position).
- `Len()` may include expired entries nobody has touched; call
  `DeleteExpired()` first for an exact count. Expired entries still occupy
  capacity until touched, evicted, or swept.

## Concurrency

- `NewOrderedMap(locked)`, `NewBucketMap(locked)` and the `Locked` field of
  `CacheMapConfig` / `ShardedCacheMapConfig` turn on an internal
  `sync.RWMutex`. Unlocked structures do no synchronisation at all.
- A structure that wraps another never double-locks: a `CacheMap` guards
  its inner map with its own lock; each `ShardedCacheMap` shard is a locked
  `CacheMap`; `BucketMap` buckets rely on the bucket map's lock.
- `ShardedMap` is always locked per shard — that is its purpose.
- Reads (`Get` on a plain map, `Has`, `Len`, `Top` on a map) take the read
  lock. On a cache, `Get` takes the **write** lock because it records the
  access, and `Top`/`Bottom` do too because they may purge expired entries.

## Comparison with other libraries

Feature comparison against commonly used Go caches, taken from each
project's documentation. **[docs/comparison.md](docs/comparison.md)** has
the full picture: the same libraries driven through one harness for hit
ratio and throughput, plus a 15-row feature matrix.

| Library | Eviction policies | Bounded by | TTL | Sharded | Generics | Ordered access (`Top`/`Bottom`) | Building blocks exposed |
|---|---|---|---|---|---|---|---|
| **jr_cache** | LRU, MRU, FIFO, LIFO, LFU, MFU | entry count | per entry, lazy | optional (`ShardedCacheMap`) | yes | yes | yes (ordered map, bucket map, sharded map) |
| hashicorp/golang-lru v2 | LRU, 2Q, ARC | entry count | via `expirable` package | no | yes | oldest / peek | no |
| dgraph-io/ristretto | TinyLFU admission + sampled LFU | cost | per entry | internal | yes (v2) | no | no |
| maypok86/otter v2 | adaptive W-TinyLFU | entries or weight | per entry | internal | yes | no | no |
| bluele/gcache | LRU, LFU, ARC, simple | entry count | per entry | no | no (`interface{}`) | no | no |
| patrickmn/go-cache | none (TTL only) | unbounded | per entry + janitor | no | no (`interface{}`) | no | no |
| allegro/bigcache | oldest-first within a life window | bytes | global window | yes | no (`[]byte`) | no | no |
| orcaman/concurrent-map | none | unbounded | no | yes | yes (v2) | no | — (it *is* the block) |

Where `jr_cache` fits, measured: single-threaded its LRU is as fast as
hashicorp/golang-lru and ahead of every other bounded cache; under
contention its sharded LRU beats every mutex-based cache, but the
lock-free readers (otter, theine, ristretto) serve parallel hits 10–30×
faster because a `jr_cache` `Get` takes a write lock to record the
access. Its exact LFU matches W-TinyLFU hit ratios within a point at
3–5× the cost. Choose it for a **choice of policy** in one API, **ordered
access** to the least/most end, or the **underlying structures** as
first-class types; choose otter/theine when many goroutines hammer one
hot cache; bigcache/freecache to keep hundreds of millions of entries out
of the GC's sight.

## Benchmarks

Numbers from `docs/benchmark.md` (Go 1.26, Intel Core Ultra 9 275HX,
Windows, `-benchtime=500ms`), at **10,000 entries**; `Parallel` rows are a
3:1 get/set mix from 24 goroutines. See the report for every operation at
500 → 50,000 entries.

| Structure | Operation | ns/op | allocs/op |
|---|---|---:|---:|
| `OrderedMap` | Get | 10.7 | 0 |
| `OrderedMap` | Set (move existing) | 14.3 | 0 |
| `BucketMap` | Get | 41.5 | 0 |
| `BucketMap` | Bump (move to next bucket) | 97.7 | 0 |
| `CacheMap` LRU | Get | 18.1 | 0 |
| `CacheMap` LRU | Set (evicting) | 91.7 | 1 |
| `CacheMap` LRU | Get on a key with a TTL | 27.3 | 0 |
| `CacheMap` LFU | Get | 77.6 | 0 |
| `CacheMap` LFU | Set (evicting) | 171 | 1 |
| `CacheMap` LRU, locked | Get | 29.8 | 0 |
| `CacheMap` LRU, locked | **Parallel** | 67.6 | 0 |
| `CacheMap` LFU, locked | **Parallel** | 202 | 0 |
| `ShardedCacheMap` LRU, 16 shards | Get (single thread) | 43.2 | 0 |
| `ShardedCacheMap` LRU, 16 shards | **Parallel** | 46.1 | 0 |
| `ShardedCacheMap` LFU, 16 shards | **Parallel** | 80.4 | 0 |
| `ShardedCacheMap` LRU, global order | Get (single thread) | 66.9 | 0 |
| `ShardedCacheMap` LRU, global order | **Parallel** | 161 | 0 |
| `ShardedCacheMap` LRU, global order | Top / Len | 46.4 / 11.2 | 0 |
| `ShardedMap` 16 shards | Get | 23.1 | 0 |
| `ShardedMap` 16 shards | Set | 29.3 | 0 |
| `ShardedMap` 16 shards | Modify | 33.1 | 0 |
| `ShardedMap` 16 shards | **Parallel** | 49.9 | 0 |

Takeaways:

- LRU reads and writes are in the tens of nanoseconds with no allocation.
- LFU costs about 2.5× LRU per `Get` and allocates nothing: every read
  moves the key to the next frequency bucket, but buckets are pooled and
  the key's list node moves with it.
- A global order on the sharded cache makes eviction exact and `Top`/`Len`
  O(1), but every operation takes the shared index lock: ~3.5× slower than
  per-shard order under contention (161 vs 46 ns for LRU), and slower than
  a single locked `CacheMap` too, since each call takes two locks.
- Sharding is slower single-threaded (hashing + indirection) and 1.5× (LRU)
  to 2.4× (LFU) faster under contention. For cheap keys, supply an
  identity-style `Hash` to remove most of the single-threaded penalty.
- A TTL adds ~10 ns to a `Get` and ~20–35 ns to a `Set`.

Run them yourself:

```
go test ./tests/bench -bench . -benchmem -benchtime=500ms          # all
go test ./tests/bench -bench 'BenchmarkCacheMap/LRU/Get/'           # one op, all sizes
cd tests/compare && go test -run xxx -bench . -benchmem             # against other libraries
cd tests/compare && go test -run HitRatio -v                        # eviction-policy hit ratios
```

## Design notes and caveats

- **Per-shard eviction (default).** A sharded cache evicts the least/most
  entry *of the shard the new key lands in*, not of the whole cache. This
  is the usual trade-off for sharded caches; expect hit rates slightly
  below a single global LRU, or set `GlobalOrder: true` to get exactly that
  at the cost of one shared lock.
- **`Top`/`Bottom` on a per-shard cache** use the fullest shard (lowest
  index on ties) — an approximation, since there is no global order. They
  and `Len` visit every shard (~170–200 ns for 16 shards). With a global
  order they are exact and O(1).
- **Global order and races.** In global-order mode a `Set` writes the value
  to its shard and then the key to the shared index; a concurrent eviction
  of that key in between is repaired lazily (the next `Get`/`Pop` drops the
  stray half). `SetOrGet` is a `Get` followed by a `Set`, not one atomic
  step, in this mode.
- **Capacity is by entry count**, not bytes. Size your `Value` type
  accordingly or wrap the cache if you need a byte budget.
- **LFU is one bucket per distinct frequency.** With adversarial access
  patterns (every key at a different count) that is one bucket per key;
  the bucket pool and node reuse keep this allocation-free but not free.
- **`Populate` vs `PopulateThreaded`.** Partitioning by shard costs an
  allocation per entry, so the threaded form only wins when per-entry work
  is heavier than a map insert — the LFU cache, yes; a bare `ShardedMap`,
  no. Numbers are in the benchmark report.
- **`GetAllBuckets`** returns shallow copies that share their entry maps
  with the live buckets: treat them as read-only snapshots.
- **No background goroutines.** Nothing runs unless you call it; expired
  entries are reclaimed lazily or by `DeleteExpired`.

## Testing

```
go test ./...                 # 94 tests, ~2 s
go vet ./... && gofmt -l .    # both clean
```

Tests live in `tests/` as an external package and cover every public
method, the eviction order of each policy, TTL edge cases, bucket pooling,
5,000-operation randomised invariant checks on the bucket map, goroutine
hammering of every locked structure, and the `GCManager` — its triggers,
start/stop lifecycle, goroutine-leak freedom, and the disable/restore round
trip (which puts the process GC settings back even when a test fails). The race detector is worth
running where a C toolchain is available: `go test -race ./...`.

## Project layout

```
jr_cache/
├── code/
│   ├── utilities.go           EvictionPolicy, CommonMapInterface, shard helpers
│   ├── ordered_map.go         OrderedMap
│   ├── bucket_map.go          BucketMap (+ bucket pool)
│   ├── cache_map.go           CacheMap (+ TTL)
│   ├── cache_sharded_map.go   ShardedCacheMap
│   ├── sharded_map.go         ShardedMap
│   └── gc_comp.go             GCManager (background sweeper)
├── tests/
│   ├── *_test.go              unit tests (package tests)
│   ├── bench/                 benchmark suite (package bench)
│   └── compare/               comparison against other libraries (own module)
├── docs/
│   ├── benchmark.md           full benchmark report
│   ├── benchmark_raw.txt      raw `go test -bench` output for benchstat
│   ├── comparison.md          hit-ratio, throughput and feature comparison
│   └── comparison_raw.txt     raw output of the comparison run
├── go.mod
├── LICENSE
└── README.MD
```

## Roadmap

Ideas that fall naturally out of the current design, none started:

- Cost-based capacity (bytes) alongside entry count.
- An atomic `SetOrGet` in global-order mode (needs a loader-style
  `GetOrCompute` on the index).
- Generate `docs/benchmark.md` from a checked-in script.

Done: allocation-free LFU `Get` (bucket pool + node reuse), `SetOnEvict`
hook, `GlobalOrder` for the sharded cache, `GCManager` (background sweeper
and runtime GC control).

## Contributing

1. Keep the shared method names and signatures — every structure should
   stay a drop-in for the others where the semantics allow.
2. Add a compile-time interface assertion for any new type.
3. Add tests under `tests/` and a benchmark under `tests/bench/` for any
   new operation; run `go vet ./...` and `gofmt -l .` before opening a PR.
4. If a change moves a benchmark by more than noise (~5 %), update
   `docs/benchmark.md` with a fresh run and note the cause.

## License

[MIT](LICENSE) — use it, change it, ship it; keep the copyright notice.
