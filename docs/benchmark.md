# Benchmark Report

Full run of the `tests/bench` suite: every module, every basic operation
(populate, get, has, set, update, pop, delete) plus each module's own
operations, at 500 / 1,000 / 5,000 / 10,000 / 50,000 entries. The
`GCManager` benchmarks run a different workload at different sizes, so they
have their own section ([Turning the collector off](#turning-the-collector-off))
and are not in the per-module tables below. All rows come
from one session on an idle machine; sessions on the same machine have
differed by 30–40 % across unchanged code, so compare rows within this
report rather than against older copies of it. Where this report quotes a
before/after for a code change, the two sides were measured in the same
session, alternating the old and new builds.

## Highlights

**Baseline costs.** `OrderedMap` is the floor everything else builds on:
6–9 ns per `Get`, 9–14 ns per `Set`, zero allocations in steady state, and
one 32-byte node per inserted entry. `BucketMap.Get` (6–9 ns) now costs the
same as `OrderedMap.Get`: every key maps straight to its list node, so a
read is one hash lookup. `GetBucketIndex` (5–8 ns) reads the index through
the node's bucket pointer.

**Scaling with size is mostly flat.** Most per-op rows grow 30–60 % from
500 to 50,000 entries. Rows that touch list pointers on every call —
`OrderedMap.Set`, `MoveToBottom`, `SetAfter` — are sensitive to where the
allocator happened to place the nodes once the list no longer fits in L2:
the same `MoveToBottom/50000` measured 12 ns in this run and ~30 ns in
another run of the same session. That is pointer chasing, not an
algorithmic change.

**LFU costs about what LRU costs.** `CacheMap/LFU` serves a `Get` in
13–18 ns against 10–17 ns for LRU, with zero allocations. Every LFU read
moves the key to the frequency `f+1` bucket. `BucketMap` keeps one map from
key to list node; each node points at its bucket, and the buckets form a
list sorted by index, so moving a key to the neighbouring bucket is a few
pointer writes with no hashing at all. A key that is alone in its bucket
takes the bucket along (it is renumbered in place), and buckets that empty
out go back to a pool (`DefaultWarmBuckets` = 16, `WarmUp(n)` to grow it).
`Bump` on a bare `BucketMap` is 14–19 ns. History of an LFU `Get`, each
measured against the build before it: 170–210 ns with a fresh bucket per
move, 110–160 ns with pooled buckets, 60–95 ns with the node moved between
per-bucket hash maps, and 13–18 ns with the linked buckets above (−72 to
−77 % against the previous build, same session).

**Evicting inserts allocate nothing.** When the cache is full, `Set` of a
new key evicts one entry and reuses its node for the new key, so
`SetEvicting` is 47–60 ns (LRU) and 53–70 ns (LFU) with zero allocations;
against the previous build in the same session that is −37 % (LRU) and
−62 % (LFU), down from one 32-byte node per insert. The only operations
that still allocate are inserts into a cache that is not yet full.

**One lookup per cache operation.** `CacheMap.Get` and `Set` work on the
backing map's nodes directly instead of going through its public methods:
an LRU `Get` used to look its key up twice (read, then move) and a `Set` on
an existing key three times. Against the previous build in the same
session: LRU `Get` −22 to −29 %, LRU `Set` −36 to −38 %.

**What a mutex costs uncontended.** `RLock`/`RUnlock` adds ~5 ns to reads
and `Lock`/`Unlock` ~12–14 ns to writes (`OrderedMap` vs `OrderedMapLocked`);
the cache's lock adds ~12–15 ns to both `Get` and `Set`.

**Sharding pays off only under contention.** Single-threaded, the sharded
cache is *slower* than the locked cache (`Get` 29–35 ns vs 25–30 ns; `Set`
43–64 ns vs 25–30 ns): that is the default `maphash` on the key plus the
extra indirection. The `Set` gap is wider than the `Get` gap partly because
splitting the capacity across shards and hashing keys unevenly means a
"full" sharded cache holds 95–99 % of the keys the benchmark cycles
through, so some of those `Set`s evict. With 24 goroutines it flips — the
`Parallel` rows show `ShardedCacheMap/LRU` at 39–42 ns per op versus
`CacheMapLocked/LRU` at 85–88 ns (2.1×), and 34–38 ns versus 82–92 ns for
LFU (2.4×). For integer or otherwise cheap keys, passing a custom `Hash` in
the config removes most of the single-threaded penalty. In per-shard mode
`Top`, `Bottom` and `Len` visit every shard (~170–210 ns for 16 shards).

**Global order: exact eviction for one shared lock.** `ShardedCacheMapGlobal`
keeps a single `CacheMap[Key, struct{}]` as the index of every key and uses
the shards only for values. Eviction is then the true global LRU/LFU,
`Top` is exact and cheap (36 ns vs ~210 ns per-shard) and `Len` is O(1)
(11 ns vs ~173 ns). The price is the lock every operation takes: a
single-threaded `Get` goes from 29–33 to 39–48 ns (LRU), and under
24-goroutine contention the `Parallel` row is 196–227 ns against 39–42 ns
per-shard for LRU and 200–223 ns against 34–38 ns for LFU — about 5× slower,
and more than 2× slower than a plain locked `CacheMap` because each
operation now takes the index lock *and* a shard lock. `SetEvicting` also
pays for two structures (122–162 ns vs 56–80 ns). Choose global order when
hit rate or exact `Top`/`Len` matter more than throughput; per-shard order
for the opposite.

**`ShardedMap` is close to a plain locked map.** 16–19 ns `Get`, 24–27 ns
`Set`, 26–28 ns `Modify`, 49–55 ns per op with 24 goroutines. `Range` is
~19–28 ns/entry, dominated by the per-shard snapshot copy (24 B/entry).

**Threaded populate: partition first, then one lock per shard.**
`Populate(entries, threads)` groups the input by shard and fills each shard
under a single lock acquisition. It is not faster than the plain sequential
loop on a bare `ShardedMap` (69–110 vs 46–50 ns/entry): building the
per-shard partitions allocates ~95 B/entry, more than the ~25 ns insert it
parallelises. Now that inserting into an LFU cache is cheap, the same holds
for the caches: `ShardedCacheMap/LRU/PopulateThreaded` is 54–75 ns/entry
against 42–53 ns sequential, and LFU roughly breaks even (70–117 vs
76–110 ns/entry, winning only on the largest batch). Prefer the plain form
unless batches are very large.

**`Clear` keeps its storage.** `OrderedMap.Clear` empties the hash map in
place instead of allocating a new one, so a map that is cleared and
refilled (a benchmark iteration, or a pooled bucket) does not re-grow its
table: `OrderedMap/Populate` is 23–33 ns/entry and 32 B/entry (one node).
A `BucketMap` entry costs ~95 B/entry to populate: a 48-byte node (key,
value, bucket pointer, two list pointers) plus the index growing.

**TTL is cheap.** `SetWithTTL` adds ~15–25 ns over `Set` (a map write and
`time.Now()`), a `Get` on a key with a TTL adds ~6–12 ns for the deadline
check, and `DeleteExpired` sweeps at 29–53 ns/entry.

## Turning the collector off

`GCManager.DisableGCCompletely` turns Go's collector off for the whole
process. `BenchmarkGCEnabledVsDisabled` measures whether that is worth it:
the same cache-churn workload (256-byte values, every iteration allocates
one and evicts another) with the collector on and off, at 10,000 and
100,000 entries. `-benchtime=200ms`, three runs each. Since evicting
inserts reuse the evicted node, the 256-byte value is the workload's only
allocation.

| Capacity | Collector | ns/op (3 runs) | GC cycles | Pause total | Heap at the end |
|---|---|---:|---:|---:|---:|
| 10,000 | on | 155 / 151 / 166 | ~90 | 4.5–5.8 ms | 5–8 MB |
| 10,000 | **off** | 145 / 143 / 141 | 0 | 0 | **428–445 MB** |
| 100,000 | on | 127 / 125 / 138 | ~14 | 0 ms | 43–52 MB |
| 100,000 | **off** | 137 / 130 / 123 | 0 | 0 | **479–583 MB** |

Turning the collector off is **not reliably faster**. At 10,000 entries it
gains 5–15 %, at 100,000 the two are indistinguishable, and the spread
across runs (123–166 ns) is as large as any difference. What it does buy is
the pause column: exactly zero, against 4.5–5.8 ms per 200 ms of work at
10,000 entries. That is the real reason to use it — a bounded
latency-critical section — and it costs hundreds of megabytes of heap that
are never given back.

`BenchmarkGCPercent` shows the middle lever, which is the better one for
almost every case: raising `GOGC` rather than switching the collector off.

| GOGC | ns/op (3 runs) | GC cycles | Heap at the end |
|---|---:|---:|---:|
| 100 (default) | 124 / 121 / 135 | 13–15 | 48–65 MB |
| 400 | 107 / 107 / 116 | 4 | 104–148 MB |
| 800 | 112 / 107 / 118 | 1–2 | 60–307 MB |

`GOGC=400` gets most of the available speed-up for a bounded heap, and
unlike disabling the collector it keeps the program's memory use finite.

`BenchmarkSweeperOverhead` measures the manager's own background sweep on a
cache with no TTLs, where every sweep finds nothing: 145–158 ns/op with no
sweeper, 149–164 ns/op sweeping every millisecond, 145–158 ns/op every
100 ms. At any sane interval the sweeper is lost in the noise.

## Reproducing

```
go test ./tests/bench -run xxx -bench . -benchmem -benchtime=500ms
```

The GC benchmarks want a short run — with the collector off nothing is
reclaimed, so a long one just grows the heap:

```
go test ./tests/bench -run xxx -bench 'GCEnabledVsDisabled|GCPercent|SweeperOverhead' -benchtime=200ms
```

This report was produced at `-benchtime=500ms`, one chunk per top-level
benchmark run back to back on an otherwise idle machine. Expect run-to-run
noise of roughly ±5 % on the per-op numbers, more on the `Parallel` and
batch rows, and larger shifts between sessions as the machine's clock and
background load change; use `benchstat` over several `-count` runs before
reading anything into a difference smaller than that. The raw output this
report was generated from is in `benchmark_raw.txt` next to it.

## Environment

- Date: 2026-10-14
- go version go1.26.2 windows/amd64
- OS/arch: windows/amd64
- CPU: Intel(R) Core(TM) Ultra 9 275HX
- Command: `go test ./tests/bench -run xxx -bench . -benchmem` (GC section: `-benchtime=200ms`)
- Entry counts: 500, 1,000, 5,000, 10,000, 50,000
- Benchmarks run: 845 (835 in the per-module tables, 10 in the GC section)

## How to read the tables

- Each cell is **ns per operation** against a map already holding that many entries (keys cycle through `0 .. N-1`).
- Rows marked **\*** are *batch* benchmarks: one pass over all N entries per iteration, so the cell is **ns per entry** and the `B/op` / `allocs/op` columns are also per entry.
- `B/op` and `allocs/op` are taken at the largest entry count.
- `Parallel` rows run a 3:1 Get/Set mix from `GOMAXPROCS` goroutines via `b.RunParallel`; the number is wall-clock ns per operation across all goroutines, so lower means better scaling, not less CPU work.
- Modules suffixed `Locked` take their mutex on every call; the plain variant does not.
- The GC section reports its own metrics instead — `GCs`, `pause-ms` and `heap-MB` — since what matters there is what the collector did, not just the per-operation cost.

## Results

- [OrderedMap](#orderedmap)
- [OrderedMapLocked](#orderedmaplocked)
- [BucketMap](#bucketmap)
- [BucketMapLocked](#bucketmaplocked)
- [CacheMap — LRU](#cachemap--lru)
- [CacheMap — LFU](#cachemap--lfu)
- [CacheMapLocked — LRU](#cachemaplocked--lru)
- [CacheMapLocked — LFU](#cachemaplocked--lfu)
- [ShardedCacheMap — LRU](#shardedcachemap--lru)
- [ShardedCacheMap — LFU](#shardedcachemap--lfu)
- [ShardedMap](#shardedmap)
- [ShardedCacheMapGlobal — LRU](#shardedcachemapglobal--lru)
- [ShardedCacheMapGlobal — LFU](#shardedcachemapglobal--lfu)

### OrderedMap

| Operation | 500 | 1,000 | 5,000 | 10,000 | 50,000 | B/op | allocs/op |
|---|---:|---:|---:|---:|---:|---:|---:|
| Populate \* | 23.2 | 24.1 | 23.4 | 25.7 | 32.9 | 32.1 | 1.00 |
| Get | 5.85 | 6.29 | 7.63 | 7.31 | 8.91 | 0.00 | 0.00 |
| Has | 5.28 | 5.86 | 6.36 | 6.60 | 8.37 | 0.00 | 0.00 |
| Set | 8.77 | 9.22 | 10.7 | 11.2 | 13.6 | 0.00 | 0.00 |
| Update | 5.73 | 6.32 | 7.10 | 7.62 | 9.76 | 0.00 | 0.00 |
| PopAll \* | 16.2 | 20.8 | 23.2 | 23.2 | 30.8 | 0.00 | 0.00 |
| DeleteAll \* | 16.1 | 19.8 | 23.1 | 23.1 | 30.5 | 0.00 | 0.00 |
| MoveToBottom | 8.61 | 9.17 | 9.63 | 10.0 | 12.4 | 0.00 | 0.00 |
| SetTop | 9.06 | 10.3 | 10.3 | 10.7 | 12.4 | 0.00 | 0.00 |
| SetAfter | 15.0 | 14.1 | 17.2 | 18.6 | 23.5 | 0.00 | 0.00 |
| Rotate | 33.6 | 40.0 | 46.2 | 46.8 | 64.1 | 32.0 | 1.00 |
| Top | 1.58 | 1.60 | 1.59 | 1.59 | 1.58 | 0.00 | 0.00 |
| PopTopAll \* | 12.1 | 14.2 | 16.3 | 16.4 | 24.9 | 0.00 | 0.00 |
| Walk \* | 13.9 | 15.5 | 16.7 | 17.6 | 21.2 | 0.00 | 0.00 |

### OrderedMapLocked

| Operation | 500 | 1,000 | 5,000 | 10,000 | 50,000 | B/op | allocs/op |
|---|---:|---:|---:|---:|---:|---:|---:|
| Populate \* | 36.4 | 35.8 | 36.2 | 38.7 | 45.1 | 32.2 | 1.00 |
| Get | 11.9 | 12.1 | 12.5 | 12.6 | 13.9 | 0.00 | 0.00 |
| Has | 11.9 | 11.9 | 12.1 | 12.1 | 13.2 | 0.00 | 0.00 |
| Set | 23.2 | 23.5 | 23.8 | 24.2 | 25.3 | 0.00 | 0.00 |
| Update | 21.5 | 21.6 | 23.1 | 21.9 | 22.9 | 0.00 | 0.00 |
| PopAll \* | 28.9 | 29.6 | 32.3 | 32.9 | 40.9 | 0.00 | 0.00 |
| DeleteAll \* | 28.5 | 34.7 | 32.2 | 33.0 | 39.9 | 0.00 | 0.00 |
| Parallel | 49.7 | 50.4 | 51.6 | 51.4 | 54.3 | 0.00 | 0.00 |

### BucketMap

| Operation | 500 | 1,000 | 5,000 | 10,000 | 50,000 | B/op | allocs/op |
|---|---:|---:|---:|---:|---:|---:|---:|
| Populate \* | 63.2 | 64.5 | 59.7 | 63.2 | 71.0 | 95.3 | 1.01 |
| Get | 5.55 | 6.24 | 6.88 | 7.14 | 8.94 | 0.00 | 0.00 |
| Has | 5.06 | 5.56 | 6.02 | 6.33 | 7.84 | 0.00 | 0.00 |
| Set | 7.82 | 7.82 | 8.86 | 9.16 | 11.1 | 0.00 | 0.00 |
| Update | 5.74 | 6.64 | 7.22 | 7.52 | 9.27 | 0.00 | 0.00 |
| PopAll \* | 23.6 | 26.3 | 30.4 | 29.2 | 33.9 | 0.00 | 0.00 |
| DeleteAll \* | 23.3 | 26.4 | 29.8 | 33.4 | 34.4 | 0.00 | 0.00 |
| Bump | 14.0 | 16.1 | 16.8 | 16.7 | 19.4 | 0.00 | 0.00 |
| SetFarBucket | 13.9 | 15.4 | 16.1 | 16.8 | 20.1 | 0.00 | 0.00 |
| Top | 1.75 | 1.75 | 1.75 | 1.75 | 1.76 | 0.00 | 0.00 |
| GetBucketIndex | 5.47 | 5.83 | 6.46 | 6.78 | 8.41 | 0.00 | 0.00 |
| PopTopAll \* | 16.1 | 18.2 | 22.1 | 31.7 | 30.4 | 0.00 | 0.00 |
| GetBucketIndices | 91.6 | 79.0 | 139 | 105 | 56.3 | 128 | 1.00 |

### BucketMapLocked

| Operation | 500 | 1,000 | 5,000 | 10,000 | 50,000 | B/op | allocs/op |
|---|---:|---:|---:|---:|---:|---:|---:|
| Populate \* | 75.2 | 78.5 | 73.4 | 78.5 | 82.9 | 95.3 | 1.01 |
| Get | 11.9 | 12.2 | 13.6 | 12.4 | 13.8 | 0.00 | 0.00 |
| Has | 11.8 | 11.9 | 13.0 | 12.0 | 13.0 | 0.00 | 0.00 |
| Set | 22.0 | 22.1 | 24.7 | 22.2 | 23.3 | 0.00 | 0.00 |
| Update | 21.2 | 21.4 | 24.3 | 21.6 | 22.8 | 0.00 | 0.00 |
| PopAll \* | 34.5 | 35.2 | 41.6 | 41.2 | 43.5 | 0.00 | 0.00 |
| DeleteAll \* | 34.0 | 35.7 | 38.6 | 40.0 | 43.6 | 0.00 | 0.00 |
| Parallel | 52.3 | 52.1 | 58.0 | 52.2 | 60.2 | 0.00 | 0.00 |

### CacheMap — LRU

| Operation | 500 | 1,000 | 5,000 | 10,000 | 50,000 | B/op | allocs/op |
|---|---:|---:|---:|---:|---:|---:|---:|
| Populate \* | 28.8 | 29.6 | 28.1 | 29.8 | 37.3 | 32.1 | 1.00 |
| Get | 10.4 | 12.0 | 13.4 | 14.2 | 17.4 | 0.00 | 0.00 |
| Has | 8.14 | 8.48 | 9.43 | 10.1 | 12.0 | 0.00 | 0.00 |
| Set | 11.1 | 13.9 | 14.6 | 15.1 | 17.6 | 0.00 | 0.00 |
| Update | 8.89 | 9.58 | 10.5 | 10.7 | 13.0 | 0.00 | 0.00 |
| PopAll \* | 20.4 | 23.6 | 27.1 | 27.8 | 34.4 | 0.00 | 0.00 |
| DeleteAll \* | 18.5 | 22.4 | 24.6 | 24.7 | 32.3 | 0.00 | 0.00 |
| SetEvicting | 47.3 | 52.7 | 55.3 | 60.1 | 59.1 | 0.00 | 0.00 |
| SetOrGet | 10.1 | 11.4 | 12.7 | 13.6 | 16.1 | 0.00 | 0.00 |
| SetWithTTL | 26.3 | 29.7 | 32.8 | 34.4 | 42.7 | 0.00 | 0.00 |
| GetWithTTL | 16.3 | 18.4 | 20.9 | 22.2 | 29.2 | 0.00 | 0.00 |
| Top | 5.40 | 5.36 | 5.40 | 5.42 | 5.33 | 0.00 | 0.00 |
| DeleteExpired \* | 28.8 | 36.9 | 34.2 | 34.9 | 47.4 | 0.00 | 0.00 |

### CacheMap — LFU

| Operation | 500 | 1,000 | 5,000 | 10,000 | 50,000 | B/op | allocs/op |
|---|---:|---:|---:|---:|---:|---:|---:|
| Populate \* | 65.3 | 64.6 | 59.4 | 65.2 | 94.5 | 95.3 | 1.01 |
| Get | 14.2 | 13.4 | 15.3 | 16.6 | 18.3 | 0.00 | 0.00 |
| Has | 7.82 | 8.51 | 9.11 | 9.47 | 11.5 | 0.00 | 0.00 |
| Set | 13.0 | 13.7 | 15.4 | 16.3 | 19.0 | 0.00 | 0.00 |
| Update | 8.39 | 9.22 | 10.4 | 10.7 | 12.8 | 0.00 | 0.00 |
| PopAll \* | 25.8 | 31.2 | 37.2 | 39.5 | 45.0 | 0.00 | 0.00 |
| DeleteAll \* | 26.0 | 30.1 | 34.1 | 35.5 | 42.2 | 0.00 | 0.00 |
| SetEvicting | 53.2 | 58.6 | 59.4 | 69.5 | 61.2 | 0.00 | 0.00 |
| SetOrGet | 12.6 | 13.1 | 15.0 | 15.4 | 17.9 | 0.00 | 0.00 |
| SetWithTTL | 28.2 | 32.8 | 36.0 | 38.4 | 44.8 | 0.00 | 0.00 |
| GetWithTTL | 21.4 | 21.6 | 25.1 | 26.1 | 34.3 | 0.00 | 0.00 |
| Top | 6.02 | 6.01 | 5.98 | 5.98 | 5.96 | 0.00 | 0.00 |
| DeleteExpired \* | 37.8 | 32.5 | 50.6 | 41.3 | 52.5 | 0.00 | 0.00 |

### CacheMapLocked — LRU

| Operation | 500 | 1,000 | 5,000 | 10,000 | 50,000 | B/op | allocs/op |
|---|---:|---:|---:|---:|---:|---:|---:|
| Populate \* | 40.2 | 41.8 | 43.6 | 44.1 | 50.8 | 32.2 | 1.00 |
| Get | 25.0 | 26.7 | 28.9 | 29.6 | 29.6 | 0.00 | 0.00 |
| Has | 14.5 | 14.7 | 15.7 | 15.5 | 16.3 | 0.00 | 0.00 |
| Set | 24.6 | 25.2 | 29.1 | 28.6 | 26.7 | 0.00 | 0.00 |
| Update | 23.1 | 23.3 | 27.3 | 27.1 | 27.0 | 0.00 | 0.00 |
| PopAll \* | 33.4 | 34.2 | 36.6 | 37.2 | 43.9 | 0.00 | 0.00 |
| DeleteAll \* | 32.7 | 33.8 | 37.4 | 36.4 | 44.2 | 0.00 | 0.00 |
| SetEvicting | 59.5 | 61.8 | 66.5 | 67.7 | 64.8 | 0.00 | 0.00 |
| SetOrGet | 24.3 | 24.4 | 27.3 | 28.0 | 26.0 | 0.00 | 0.00 |
| SetWithTTL | 41.6 | 47.7 | 48.8 | 47.3 | 50.1 | 0.00 | 0.00 |
| GetWithTTL | 27.9 | 28.4 | 31.5 | 31.7 | 38.0 | 0.00 | 0.00 |
| Top | 23.0 | 23.0 | 23.0 | 22.9 | 22.9 | 0.00 | 0.00 |
| DeleteExpired \* | 27.9 | 34.6 | 33.3 | 41.5 | 45.4 | 0.00 | 0.00 |
| Parallel | 84.9 | 87.0 | 88.3 | 87.0 | 84.7 | 0.00 | 0.00 |

### CacheMapLocked — LFU

| Operation | 500 | 1,000 | 5,000 | 10,000 | 50,000 | B/op | allocs/op |
|---|---:|---:|---:|---:|---:|---:|---:|
| Populate \* | 78.4 | 84.2 | 78.4 | 84.6 | 112 | 95.3 | 1.01 |
| Get | 26.5 | 26.8 | 29.0 | 27.1 | 29.2 | 0.00 | 0.00 |
| Has | 13.8 | 14.2 | 15.2 | 14.5 | 16.7 | 0.00 | 0.00 |
| Set | 25.8 | 26.0 | 28.6 | 26.7 | 29.7 | 0.00 | 0.00 |
| Update | 23.2 | 23.4 | 25.8 | 23.8 | 25.4 | 0.00 | 0.00 |
| PopAll \* | 36.2 | 41.1 | 42.7 | 53.9 | 53.4 | 0.00 | 0.00 |
| DeleteAll \* | 34.4 | 38.6 | 39.5 | 50.8 | 50.9 | 0.00 | 0.00 |
| SetEvicting | 58.7 | 62.4 | 67.0 | 71.6 | 68.3 | 0.00 | 0.00 |
| SetOrGet | 25.9 | 26.1 | 28.5 | 26.5 | 29.7 | 0.00 | 0.00 |
| SetWithTTL | 42.3 | 45.1 | 46.8 | 47.5 | 53.5 | 0.00 | 0.00 |
| GetWithTTL | 29.2 | 32.0 | 32.6 | 32.7 | 40.9 | 0.00 | 0.00 |
| Top | 22.9 | 23.0 | 22.9 | 23.0 | 23.0 | 0.00 | 0.00 |
| DeleteExpired \* | 32.8 | 32.1 | 46.2 | 37.8 | 52.2 | 0.00 | 0.00 |
| Parallel | 88.2 | 91.2 | 87.2 | 82.0 | 91.9 | 0.00 | 0.00 |

### ShardedCacheMap — LRU

| Operation | 500 | 1,000 | 5,000 | 10,000 | 50,000 | B/op | allocs/op |
|---|---:|---:|---:|---:|---:|---:|---:|
| Populate \* | 43.5 | 42.5 | 42.2 | 44.1 | 53.3 | 31.9 | 0.99 |
| Get | 28.9 | 29.1 | 28.6 | 29.6 | 32.6 | 0.00 | 0.00 |
| Has | 19.6 | 19.3 | 19.6 | 19.9 | 23.7 | 0.00 | 0.00 |
| Set | 43.7 | 42.7 | 45.8 | 45.3 | 63.0 | 0.00 | 0.00 |
| Update | 26.7 | 26.6 | 27.4 | 27.7 | 30.1 | 0.00 | 0.00 |
| PopAll \* | 37.8 | 37.2 | 38.3 | 37.7 | 54.2 | 0.00 | 0.00 |
| DeleteAll \* | 36.1 | 35.4 | 35.7 | 36.4 | 49.8 | 0.00 | 0.00 |
| SetEvicting | 56.1 | 60.0 | 72.3 | 74.9 | 73.5 | 0.00 | 0.00 |
| SetOrGet | 42.7 | 41.7 | 48.3 | 44.3 | 63.6 | 0.00 | 0.00 |
| SetWithTTL | 61.8 | 62.7 | 68.6 | 77.8 | 110 | 0.00 | 0.00 |
| Top | 211 | 211 | 211 | 211 | 210 | 0.00 | 0.00 |
| Len | 174 | 173 | 174 | 171 | 174 | 0.00 | 0.00 |
| PopulateThreaded \* | 74.7 | 62.5 | 54.3 | 55.7 | 63.4 | 79.4 | 1.00 |
| Parallel | 42.0 | 40.1 | 39.2 | 39.1 | 40.5 | 0.00 | 0.00 |

### ShardedCacheMap — LFU

| Operation | 500 | 1,000 | 5,000 | 10,000 | 50,000 | B/op | allocs/op |
|---|---:|---:|---:|---:|---:|---:|---:|
| Populate \* | 76.4 | 83.1 | 79.6 | 91.4 | 110 | 95.1 | 1.00 |
| Get | 32.2 | 30.9 | 31.5 | 32.6 | 35.4 | 0.00 | 0.00 |
| Has | 18.9 | 19.3 | 19.5 | 19.9 | 23.6 | 0.00 | 0.00 |
| Set | 45.7 | 46.3 | 49.7 | 49.2 | 64.4 | 0.00 | 0.00 |
| Update | 27.1 | 27.1 | 27.9 | 27.9 | 30.9 | 0.00 | 0.00 |
| PopAll \* | 45.5 | 46.0 | 47.0 | 53.1 | 63.1 | 0.00 | 0.00 |
| DeleteAll \* | 44.3 | 45.7 | 44.4 | 48.2 | 59.2 | 0.00 | 0.00 |
| SetEvicting | 60.0 | 63.9 | 77.6 | 79.9 | 77.2 | 0.00 | 0.00 |
| SetOrGet | 46.1 | 46.8 | 48.3 | 50.3 | 64.0 | 0.00 | 0.00 |
| SetWithTTL | 66.6 | 68.0 | 71.7 | 81.2 | 117 | 0.00 | 0.00 |
| Top | 211 | 209 | 211 | 212 | 210 | 0.00 | 0.00 |
| Len | 174 | 174 | 173 | 172 | 174 | 0.00 | 0.00 |
| PopulateThreaded \* | 96.5 | 89.9 | 89.1 | 117 | 70.1 | 143 | 1.01 |
| Parallel | 37.8 | 35.5 | 34.4 | 33.7 | 34.8 | 0.00 | 0.00 |

### ShardedMap

| Operation | 500 | 1,000 | 5,000 | 10,000 | 50,000 | B/op | allocs/op |
|---|---:|---:|---:|---:|---:|---:|---:|
| Populate \* | 49.2 | 49.5 | 45.8 | 47.5 | 46.5 | 47.5 | 0.01 |
| Get | 16.4 | 15.7 | 16.3 | 16.8 | 19.3 | 0.00 | 0.00 |
| Has | 15.2 | 14.3 | 15.5 | 15.7 | 17.8 | 0.00 | 0.00 |
| Set | 23.7 | 24.3 | 24.5 | 24.7 | 26.5 | 0.00 | 0.00 |
| Update | 27.6 | 28.5 | 27.8 | 28.2 | 29.5 | 0.00 | 0.00 |
| PopAll \* | 30.4 | 31.2 | 32.0 | 30.9 | 38.1 | 0.00 | 0.00 |
| DeleteAll \* | 30.4 | 30.5 | 30.7 | 31.2 | 36.5 | 0.00 | 0.00 |
| SetOrGet | 23.9 | 23.8 | 24.5 | 24.2 | 24.6 | 0.00 | 0.00 |
| Modify | 26.8 | 26.2 | 27.8 | 28.0 | 28.4 | 0.00 | 0.00 |
| ShardIndex | 3.12 | 3.12 | 3.13 | 3.15 | 3.12 | 0.00 | 0.00 |
| Len | 165 | 167 | 164 | 168 | 166 | 0.00 | 0.00 |
| Range \* | 27.9 | 25.2 | 19.6 | 19.4 | 22.5 | 23.7 | 0.00 |
| PopulateThreaded \* | 86.3 | 77.8 | 69.0 | 110 | 104 | 94.8 | 0.02 |
| Parallel | 54.8 | 50.3 | 51.0 | 49.3 | 51.9 | 0.00 | 0.00 |

### ShardedCacheMapGlobal — LRU

| Operation | 500 | 1,000 | 5,000 | 10,000 | 50,000 | B/op | allocs/op |
|---|---:|---:|---:|---:|---:|---:|---:|
| Populate \* | 89.5 | 94.0 | 94.0 | 96.0 | 102 | 71.9 | 1.01 |
| Get | 39.4 | 44.4 | 43.3 | 41.9 | 47.7 | 0.00 | 0.00 |
| Has | 14.1 | 14.4 | 15.6 | 14.9 | 17.0 | 0.00 | 0.00 |
| Set | 48.1 | 50.4 | 52.7 | 50.8 | 58.7 | 0.00 | 0.00 |
| Update | 48.6 | 53.5 | 51.9 | 50.6 | 55.5 | 0.00 | 0.00 |
| PopAll \* | 65.5 | 71.4 | 70.9 | 72.2 | 89.6 | 0.00 | 0.00 |
| DeleteAll \* | 63.1 | 66.3 | 69.0 | 70.3 | 84.4 | 0.00 | 0.00 |
| SetEvicting | 122 | 134 | 144 | 158 | 157 | 1.00 | 0.00 |
| SetOrGet | 40.2 | 45.1 | 44.7 | 42.1 | 47.9 | 0.00 | 0.00 |
| SetWithTTL | 65.8 | 68.5 | 71.3 | 73.2 | 85.3 | 0.00 | 0.00 |
| Top | 36.4 | 36.6 | 36.5 | 36.7 | 36.7 | 0.00 | 0.00 |
| Len | 11.4 | 11.4 | 11.4 | 11.4 | 11.4 | 0.00 | 0.00 |
| PopulateThreaded \* | 139 | 135 | 133 | 134 | 143 | 143 | 1.02 |
| Parallel | 196 | 203 | 212 | 227 | 220 | 0.00 | 0.00 |

### ShardedCacheMapGlobal — LFU

| Operation | 500 | 1,000 | 5,000 | 10,000 | 50,000 | B/op | allocs/op |
|---|---:|---:|---:|---:|---:|---:|---:|
| Populate \* | 131 | 137 | 131 | 138 | 159 | 127 | 1.02 |
| Get | 41.4 | 41.9 | 46.6 | 43.2 | 50.0 | 0.00 | 0.00 |
| Has | 20.3 | 14.3 | 15.6 | 14.8 | 16.3 | 0.00 | 0.00 |
| Set | 49.7 | 50.0 | 54.5 | 53.5 | 60.7 | 0.00 | 0.00 |
| Update | 48.1 | 48.9 | 51.5 | 54.6 | 58.1 | 0.00 | 0.00 |
| PopAll \* | 76.7 | 77.1 | 84.2 | 78.5 | 92.0 | 0.00 | 0.00 |
| DeleteAll \* | 76.4 | 74.9 | 84.2 | 78.7 | 89.1 | 0.00 | 0.00 |
| SetEvicting | 124 | 131 | 142 | 158 | 162 | 1.00 | 0.00 |
| SetOrGet | 40.5 | 41.0 | 44.4 | 42.6 | 48.1 | 0.00 | 0.00 |
| SetWithTTL | 71.2 | 69.2 | 71.8 | 72.1 | 85.3 | 0.00 | 0.00 |
| Top | 36.6 | 36.5 | 36.4 | 36.5 | 36.4 | 0.00 | 0.00 |
| Len | 11.3 | 11.3 | 11.3 | 11.4 | 11.3 | 0.00 | 0.00 |
| PopulateThreaded \* | 186 | 175 | 179 | 216 | 176 | 198 | 1.02 |
| Parallel | 210 | 205 | 209 | 200 | 223 | 0.00 | 0.00 |
