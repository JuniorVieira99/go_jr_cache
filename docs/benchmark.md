# Benchmark Report

Full run of the `tests/bench` suite: every module, every basic operation
(populate, get, has, set, update, pop, delete) plus each module's own
operations, at 500 / 1,000 / 5,000 / 10,000 / 50,000 entries. All rows come
from one session on an idle machine; a previous session on the same
machine measured ~30–40 % slower across unchanged code, so compare rows
within this report rather than against older copies of it.

## Highlights

**Baseline costs.** `OrderedMap` is the floor everything else builds on:
8–13 ns per `Get`, 11–14 ns per `Set` on small maps, zero allocations in
steady state, and one 32-byte node per inserted entry. `BucketMap.Has` and
`GetBucketIndex` (5–12 ns) cost the same because both are a single map
lookup; `BucketMap.Get` (37–51 ns) pays for three lookups — key → bucket
index → bucket → entry.

**Scaling with size is flat until the working set leaves cache.** Most
per-op rows grow only 30–50 % from 500 to 50,000 entries. Anything that
touches list pointers on every call — `OrderedMap.Set`, `MoveToBottom`,
`SetAfter` — roughly doubles once the list no longer fits in L2; that is
pointer chasing, not an algorithmic change.

**LFU `Get` is allocation-free.** `CacheMap/LFU` serves a `Get` in 60–95 ns
with zero allocations, against 16–21 ns for LRU. Every LFU read moves the
key to the frequency `f+1` bucket; `BucketMap` takes that bucket from a
pool of warm buckets (`DefaultWarmBuckets` = 16, `WarmUp(n)` to grow it)
and moves the key's list node with it (`OrderedMap.detach` /
`attach_bottom`), so nothing is allocated. Before the pool an LFU `Get`
was 170–210 ns and ~90 B; with the pool but a fresh node, 110–160 ns and
32 B. `Update`, which does not bump, costs 19–29 ns under LFU, and `Bump`
on a bare `BucketMap` is 72–101 ns. The only LFU operation that still
allocates is inserting a *new* key (`SetEvicting`, one node).

**What a mutex costs uncontended.** `RLock`/`RUnlock` adds ~4 ns to reads
and `Lock`/`Unlock` ~12 ns to writes (`OrderedMap` vs `OrderedMapLocked`);
the cache's lock adds ~10–12 ns to both `Get` and `Set`.

**Sharding pays off only under contention.** Single-threaded, the sharded
cache is *slower* than the locked cache (`Get` 37–43 ns vs 28–30 ns; `Set`
76–107 ns vs 30–35 ns): that is the default `maphash` on the key plus the
extra indirection. With 24 goroutines it flips — the `Parallel` rows show
`ShardedCacheMap/LRU` at 43–47 ns per op versus `CacheMapLocked/LRU` at
66–70 ns (1.5×), and 80–92 ns versus 179–224 ns for LFU (2.4×). For
integer or otherwise cheap keys, passing a custom `Hash` in the config
removes most of the single-threaded penalty. In per-shard mode `Top`,
`Bottom` and `Len` visit every shard (~170–200 ns for 16 shards).

**Global order: exact eviction for one shared lock.** `ShardedCacheMapGlobal`
keeps a single `CacheMap[Key, struct{}]` as the index of every key and uses
the shards only for values. Eviction is then the true global LRU/LFU,
`Top` is exact and cheap (46 ns vs ~197 ns per-shard) and `Len` is O(1)
(11 ns vs ~171 ns). The price is the lock every operation takes: a
single-threaded `Get` goes from 43 to 67 ns (LRU), and under 24-goroutine
contention the `Parallel` row is 157–174 ns against 43–47 ns per-shard for
LRU and 211–288 ns against 80–92 ns for LFU — about 3.5× slower, and
roughly 2.4× slower than a plain locked `CacheMap` because each operation
now takes the index lock *and* a shard lock. `SetEvicting` also pays for
two structures (200–248 ns vs 76–107 ns). Choose global order when hit
rate or exact `Top`/`Len` matter more than throughput; per-shard order for
the opposite.

**`ShardedMap` is close to a plain locked map.** 21–26 ns `Get`, 26–41 ns
`Set`, 32–36 ns `Modify`, 47–51 ns per op with 24 goroutines. `Range` is
~27–32 ns/entry, dominated by the per-shard snapshot copy (24 B/entry).

**Threaded populate: partition first, then one lock per shard.**
`Populate(entries, threads)` groups the input by shard and fills each shard
under a single lock acquisition. It is not faster than the plain sequential
loop on a bare `ShardedMap` (98–128 vs 50–66 ns/entry): building the
per-shard partitions allocates ~95 B/entry, more than the ~25 ns insert it
parallelises. It does pay where the per-entry work is heavier —
`ShardedCacheMap/LFU/PopulateThreaded` is 81–122 ns/entry against
136–160 ns/entry sequential, and LRU roughly breaks even. Use the threaded
form for large batches into a cache, the plain form for a bare map.

**`Clear` keeps its storage.** `OrderedMap.Clear` empties the hash map in
place instead of allocating a new one, so a map that is cleared and
refilled (a benchmark iteration, or a pooled bucket) does not re-grow its
table: `OrderedMap/Populate` is 22–41 ns/entry and 32 B/entry (one node).

**TTL is cheap.** `SetWithTTL` adds ~20–25 ns over `Set` (a map write and
`time.Now()`), a `Get` on a key with a TTL adds ~6–14 ns for the deadline
check, and `DeleteExpired` sweeps at 30–60 ns/entry.

## Reproducing

```
go test ./tests/bench -run xxx -bench . -benchmem -benchtime=500ms
```

This report was produced at `-benchtime=500ms`, one chunk per top-level
benchmark run back to back on an otherwise idle machine. Expect run-to-run
noise of roughly ±5 % on the per-op numbers, more on the `Parallel` and
batch rows, and larger shifts between sessions as the machine's clock and
background load change; use `benchstat` over several `-count` runs before
reading anything into a difference smaller than that. The raw output this
report was generated from is in `benchmark_raw.txt` next to it.

## Environment

- Date: 2026-09-17
- go version go1.26.2 windows/amd64
- OS/arch: windows/amd64
- CPU: Intel(R) Core(TM) Ultra 9 275HX
- Command: `go test ./tests/bench -run xxx -bench . -benchmem`
- Entry counts: 500, 1,000, 5,000, 10,000, 50,000
- Benchmarks run: 835

## How to read the tables

- Each cell is **ns per operation** against a map already holding that many entries (keys cycle through `0 .. N-1`).
- Rows marked **\*** are *batch* benchmarks: one pass over all N entries per iteration, so the cell is **ns per entry** and the `B/op` / `allocs/op` columns are also per entry.
- `B/op` and `allocs/op` are taken at the largest entry count.
- `Parallel` rows run a 3:1 Get/Set mix from `GOMAXPROCS` goroutines via `b.RunParallel`; the number is wall-clock ns per operation across all goroutines, so lower means better scaling, not less CPU work.
- Modules suffixed `Locked` take their mutex on every call; the plain variant does not.

## Results

- [OrderedMap](#orderedmap)
- [OrderedMapLocked](#orderedmaplocked)
- [BucketMap](#bucketmap)
- [BucketMapLocked](#bucketmaplocked)
- [CacheMap — LRU](#cachemap-lru)
- [CacheMap — LFU](#cachemap-lfu)
- [CacheMapLocked — LRU](#cachemaplocked-lru)
- [CacheMapLocked — LFU](#cachemaplocked-lfu)
- [ShardedCacheMap — LRU](#shardedcachemap-lru)
- [ShardedCacheMap — LFU](#shardedcachemap-lfu)
- [ShardedMap](#shardedmap)
- [ShardedCacheMapGlobal — LRU](#shardedcachemapglobal-lru)
- [ShardedCacheMapGlobal — LFU](#shardedcachemapglobal-lfu)

### OrderedMap

| Operation | 500 | 1,000 | 5,000 | 10,000 | 50,000 | B/op | allocs/op |
|---|---:|---:|---:|---:|---:|---:|---:|
| Populate \* | 22.2 | 27.6 | 36.0 | 37.3 | 41.2 | 32.2 | 1.00 |
| Get | 8.29 | 9.15 | 10.2 | 10.7 | 12.6 | 0.00 | 0.00 |
| Has | 7.87 | 8.42 | 9.71 | 10.1 | 11.9 | 0.00 | 0.00 |
| Set | 10.8 | 12.6 | 13.8 | 14.3 | 30.6 | 0.00 | 0.00 |
| Update | 8.53 | 9.33 | 10.4 | 10.7 | 13.2 | 0.00 | 0.00 |
| PopAll \* | 27.5 | 32.6 | 34.8 | 37.9 | 41.2 | 0.00 | 0.00 |
| DeleteAll \* | 27.2 | 36.1 | 37.5 | 39.6 | 40.1 | 0.00 | 0.00 |
| MoveToBottom | 9.98 | 11.4 | 12.7 | 27.5 | 30.6 | 0.00 | 0.00 |
| SetTop | 10.6 | 12.6 | 13.7 | 14.2 | 30.1 | 0.00 | 0.00 |
| SetAfter | 16.4 | 18.7 | 21.6 | 22.6 | 38.9 | 0.00 | 0.00 |
| Rotate | 48.4 | 49.5 | 64.7 | 66.6 | 76.6 | 32.0 | 1.00 |
| Top | 2.86 | 2.85 | 2.85 | 2.85 | 2.86 | 0.00 | 0.00 |
| PopTopAll \* | 16.2 | 16.9 | 20.5 | 19.9 | 26.9 | 0.00 | 0.00 |
| Walk \* | 17.5 | 20.1 | 22.2 | 23.0 | 25.7 | 0.00 | 0.00 |

### OrderedMapLocked

| Operation | 500 | 1,000 | 5,000 | 10,000 | 50,000 | B/op | allocs/op |
|---|---:|---:|---:|---:|---:|---:|---:|
| Populate \* | 35.6 | 36.7 | 45.0 | 51.9 | 52.2 | 32.2 | 1.00 |
| Get | 12.5 | 13.3 | 14.4 | 14.8 | 16.9 | 0.00 | 0.00 |
| Has | 12.2 | 12.9 | 14.1 | 14.3 | 16.1 | 0.00 | 0.00 |
| Set | 24.2 | 25.2 | 26.3 | 26.8 | 32.2 | 0.00 | 0.00 |
| Update | 21.5 | 21.8 | 22.4 | 22.4 | 24.4 | 0.00 | 0.00 |
| PopAll \* | 33.6 | 37.3 | 39.2 | 40.2 | 44.5 | 0.00 | 0.00 |
| DeleteAll \* | 31.0 | 33.0 | 38.1 | 40.2 | 46.7 | 0.00 | 0.00 |
| Parallel | 38.2 | 42.0 | 46.9 | 49.2 | 57.7 | 0.00 | 0.00 |

### BucketMap

| Operation | 500 | 1,000 | 5,000 | 10,000 | 50,000 | B/op | allocs/op |
|---|---:|---:|---:|---:|---:|---:|---:|
| Populate \* | 76.0 | 74.0 | 80.4 | 104 | 116 | 79.8 | 1.01 |
| Get | 37.1 | 38.3 | 41.0 | 41.5 | 51.2 | 0.00 | 0.00 |
| Has | 8.00 | 8.53 | 9.61 | 10.2 | 11.9 | 0.00 | 0.00 |
| Set | 27.5 | 29.4 | 32.3 | 34.2 | 43.3 | 0.00 | 0.00 |
| Update | 38.2 | 39.1 | 42.8 | 42.9 | 52.2 | 0.00 | 0.00 |
| PopAll \* | 80.6 | 84.0 | 87.3 | 87.7 | 112 | 0.00 | 0.00 |
| DeleteAll \* | 79.2 | 83.4 | 87.0 | 67.8 | 99.8 | 0.00 | 0.00 |
| Bump | 71.9 | 78.5 | 93.2 | 97.7 | 101 | 0.00 | 0.00 |
| SetFarBucket | 49.6 | 68.9 | 85.2 | 91.2 | 99.1 | 0.00 | 0.00 |
| Top | 4.14 | 4.20 | 4.12 | 4.15 | 4.13 | 0.00 | 0.00 |
| GetBucketIndex | 5.08 | 5.28 | 5.86 | 6.21 | 7.89 | 0.00 | 0.00 |
| PopTopAll \* | 29.6 | 32.4 | 36.8 | 34.8 | 49.2 | 0.00 | 0.00 |
| GetBucketIndices | 268 | 267 | 278 | 308 | 244 | 128 | 1.00 |

### BucketMapLocked

| Operation | 500 | 1,000 | 5,000 | 10,000 | 50,000 | B/op | allocs/op |
|---|---:|---:|---:|---:|---:|---:|---:|
| Populate \* | 87.0 | 88.2 | 83.6 | 88.3 | 95.2 | 79.7 | 1.01 |
| Get | 29.2 | 30.0 | 32.8 | 34.7 | 42.0 | 0.00 | 0.00 |
| Has | 11.9 | 12.0 | 12.0 | 12.2 | 13.3 | 0.00 | 0.00 |
| Set | 45.8 | 46.6 | 49.6 | 51.1 | 56.2 | 0.00 | 0.00 |
| Update | 36.1 | 37.7 | 40.4 | 43.3 | 50.3 | 0.00 | 0.00 |
| PopAll \* | 63.1 | 66.8 | 69.5 | 71.0 | 96.8 | 0.00 | 0.00 |
| DeleteAll \* | 63.3 | 66.3 | 71.8 | 71.7 | 97.3 | 0.00 | 0.00 |
| Parallel | 98.8 | 119 | 108 | 116 | 140 | 0.00 | 0.00 |

### CacheMap — LRU

| Operation | 500 | 1,000 | 5,000 | 10,000 | 50,000 | B/op | allocs/op |
|---|---:|---:|---:|---:|---:|---:|---:|
| Populate \* | 33.6 | 33.2 | 33.5 | 38.5 | 42.3 | 32.2 | 1.00 |
| Get | 15.6 | 16.2 | 17.6 | 18.1 | 20.8 | 0.00 | 0.00 |
| Has | 7.63 | 8.31 | 9.09 | 9.39 | 11.4 | 0.00 | 0.00 |
| Set | 20.4 | 21.7 | 22.3 | 22.8 | 26.2 | 0.00 | 0.00 |
| Update | 8.46 | 9.66 | 10.2 | 10.6 | 12.7 | 0.00 | 0.00 |
| PopAll \* | 20.0 | 24.9 | 27.6 | 26.9 | 34.9 | 0.00 | 0.00 |
| DeleteAll \* | 18.4 | 22.4 | 25.7 | 25.1 | 32.4 | 0.00 | 0.00 |
| SetEvicting | 75.0 | 79.4 | 88.7 | 91.7 | 90.8 | 32.0 | 1.00 |
| SetOrGet | 14.8 | 15.8 | 17.0 | 17.5 | 20.3 | 0.00 | 0.00 |
| SetWithTTL | 35.4 | 37.2 | 41.6 | 43.2 | 51.6 | 0.00 | 0.00 |
| GetWithTTL | 21.0 | 22.7 | 26.0 | 27.3 | 34.6 | 0.00 | 0.00 |
| Top | 5.28 | 5.29 | 5.31 | 5.26 | 5.26 | 0.00 | 0.00 |
| DeleteExpired \* | 30.7 | 35.9 | 35.2 | 34.3 | 45.0 | 0.00 | 0.00 |

### CacheMap — LFU

| Operation | 500 | 1,000 | 5,000 | 10,000 | 50,000 | B/op | allocs/op |
|---|---:|---:|---:|---:|---:|---:|---:|
| Populate \* | 86.2 | 86.4 | 81.4 | 88.3 | 99.4 | 79.7 | 1.01 |
| Get | 60.4 | 68.1 | 75.8 | 77.6 | 94.5 | 0.00 | 0.00 |
| Has | 7.90 | 8.15 | 9.06 | 9.41 | 11.6 | 0.00 | 0.00 |
| Set | 68.1 | 75.3 | 85.2 | 88.4 | 104 | 0.00 | 0.00 |
| Update | 19.0 | 21.1 | 23.1 | 23.9 | 29.0 | 0.00 | 0.00 |
| PopAll \* | 42.2 | 51.2 | 54.7 | 55.5 | 70.6 | 0.00 | 0.00 |
| DeleteAll \* | 40.5 | 48.7 | 53.3 | 54.5 | 68.7 | 0.00 | 0.00 |
| SetEvicting | 130 | 140 | 159 | 171 | 169 | 33.0 | 1.00 |
| SetOrGet | 58.9 | 66.1 | 75.3 | 77.3 | 92.7 | 0.00 | 0.00 |
| SetWithTTL | 83.4 | 92.2 | 105 | 110 | 138 | 1.00 | 0.00 |
| GetWithTTL | 65.8 | 77.4 | 84.2 | 85.4 | 110 | 0.00 | 0.00 |
| Top | 7.63 | 7.63 | 7.64 | 7.64 | 7.66 | 0.00 | 0.00 |
| DeleteExpired \* | 43.1 | 50.1 | 60.2 | 47.7 | 62.6 | 0.00 | 0.00 |

### CacheMapLocked — LRU

| Operation | 500 | 1,000 | 5,000 | 10,000 | 50,000 | B/op | allocs/op |
|---|---:|---:|---:|---:|---:|---:|---:|
| Populate \* | 47.0 | 49.0 | 49.3 | 51.6 | 58.3 | 32.2 | 1.00 |
| Get | 27.8 | 28.4 | 30.4 | 29.8 | 30.1 | 0.00 | 0.00 |
| Has | 13.8 | 14.0 | 15.4 | 15.1 | 15.6 | 0.00 | 0.00 |
| Set | 30.3 | 31.0 | 33.5 | 33.0 | 34.8 | 0.00 | 0.00 |
| Update | 23.2 | 27.1 | 26.5 | 24.6 | 24.5 | 0.00 | 0.00 |
| PopAll \* | 30.9 | 32.1 | 40.3 | 45.6 | 52.8 | 0.00 | 0.00 |
| DeleteAll \* | 36.6 | 41.8 | 43.5 | 43.9 | 52.2 | 0.00 | 0.00 |
| SetEvicting | 107 | 114 | 130 | 131 | 129 | 32.0 | 1.00 |
| SetOrGet | 33.1 | 35.0 | 36.2 | 36.5 | 38.6 | 0.00 | 0.00 |
| SetWithTTL | 65.9 | 65.8 | 71.7 | 68.4 | 81.7 | 0.00 | 0.00 |
| GetWithTTL | 44.0 | 46.7 | 47.4 | 48.0 | 53.8 | 0.00 | 0.00 |
| Top | 22.9 | 23.0 | 22.9 | 23.0 | 22.9 | 0.00 | 0.00 |
| DeleteExpired \* | 39.1 | 36.3 | 36.0 | 41.1 | 50.5 | 0.00 | 0.00 |
| Parallel | 65.5 | 68.1 | 72.7 | 67.6 | 70.0 | 0.00 | 0.00 |

### CacheMapLocked — LFU

| Operation | 500 | 1,000 | 5,000 | 10,000 | 50,000 | B/op | allocs/op |
|---|---:|---:|---:|---:|---:|---:|---:|
| Populate \* | 128 | 128 | 127 | 130 | 138 | 79.9 | 1.01 |
| Get | 106 | 113 | 130 | 120 | 144 | 0.00 | 0.00 |
| Has | 15.8 | 16.9 | 18.8 | 19.4 | 21.9 | 0.00 | 0.00 |
| Set | 107 | 116 | 122 | 124 | 150 | 0.00 | 0.00 |
| Update | 46.0 | 49.7 | 52.3 | 53.2 | 60.8 | 0.00 | 0.00 |
| PopAll \* | 69.2 | 73.1 | 74.5 | 79.1 | 93.4 | 0.00 | 0.00 |
| DeleteAll \* | 64.4 | 68.7 | 71.6 | 76.9 | 90.6 | 0.00 | 0.00 |
| SetEvicting | 177 | 192 | 225 | 227 | 221 | 33.0 | 1.00 |
| SetOrGet | 96.4 | 105 | 111 | 117 | 145 | 0.00 | 0.00 |
| SetWithTTL | 132 | 142 | 152 | 163 | 206 | 2.00 | 0.00 |
| GetWithTTL | 107 | 115 | 121 | 130 | 178 | 0.00 | 0.00 |
| Top | 25.3 | 25.3 | 25.3 | 25.3 | 25.2 | 0.00 | 0.00 |
| DeleteExpired \* | 44.9 | 54.5 | 54.4 | 65.6 | 72.0 | 0.00 | 0.00 |
| Parallel | 179 | 178 | 192 | 202 | 224 | 3.00 | 0.00 |

### ShardedCacheMap — LRU

| Operation | 500 | 1,000 | 5,000 | 10,000 | 50,000 | B/op | allocs/op |
|---|---:|---:|---:|---:|---:|---:|---:|
| Populate \* | 52.0 | 52.0 | 51.5 | 66.5 | 82.8 | 32.3 | 1.00 |
| Get | 41.6 | 41.2 | 43.0 | 43.2 | 37.4 | 0.00 | 0.00 |
| Has | 19.4 | 18.9 | 19.3 | 25.8 | 33.9 | 0.00 | 0.00 |
| Set | 76.2 | 79.8 | 88.9 | 94.5 | 107 | 12.0 | 0.00 |
| Update | 34.0 | 34.6 | 36.7 | 37.2 | 43.4 | 0.00 | 0.00 |
| PopAll \* | 51.8 | 50.9 | 52.8 | 53.7 | 62.2 | 0.00 | 0.00 |
| DeleteAll \* | 36.6 | 36.2 | 36.3 | 37.2 | 49.3 | 0.00 | 0.00 |
| SetEvicting | 85.6 | 88.2 | 103 | 140 | 144 | 32.0 | 1.00 |
| SetOrGet | 80.2 | 79.2 | 80.2 | 85.6 | 96.1 | 12.0 | 0.00 |
| SetWithTTL | 109 | 111 | 123 | 126 | 168 | 15.0 | 0.00 |
| Top | 197 | 198 | 197 | 197 | 197 | 0.00 | 0.00 |
| Len | 171 | 171 | 171 | 171 | 171 | 0.00 | 0.00 |
| PopulateThreaded \* | 90.6 | 76.0 | 65.5 | 69.5 | 66.8 | 79.6 | 1.01 |
| Parallel | 43.5 | 41.9 | 44.5 | 46.1 | 47.2 | 0.00 | 0.00 |

### ShardedCacheMap — LFU

| Operation | 500 | 1,000 | 5,000 | 10,000 | 50,000 | B/op | allocs/op |
|---|---:|---:|---:|---:|---:|---:|---:|
| Populate \* | 136 | 147 | 145 | 151 | 160 | 80.1 | 1.01 |
| Get | 115 | 116 | 125 | 123 | 158 | 0.00 | 0.00 |
| Has | 27.7 | 28.3 | 29.0 | 29.4 | 33.7 | 0.00 | 0.00 |
| Set | 153 | 166 | 166 | 173 | 222 | 14.0 | 0.00 |
| Update | 54.5 | 56.2 | 58.2 | 58.8 | 71.4 | 0.00 | 0.00 |
| PopAll \* | 89.5 | 93.1 | 89.6 | 90.3 | 111 | 0.00 | 0.00 |
| DeleteAll \* | 87.5 | 89.7 | 87.7 | 112 | 110 | 0.00 | 0.00 |
| SetEvicting | 190 | 201 | 235 | 241 | 251 | 33.0 | 1.00 |
| SetOrGet | 150 | 147 | 162 | 163 | 204 | 14.0 | 0.00 |
| SetWithTTL | 193 | 196 | 209 | 227 | 302 | 19.0 | 0.00 |
| Top | 200 | 200 | 200 | 199 | 199 | 0.00 | 0.00 |
| Len | 171 | 171 | 171 | 171 | 171 | 0.00 | 0.00 |
| PopulateThreaded \* | 122 | 107 | 104 | 94.8 | 81.4 | 127 | 1.02 |
| Parallel | 92.0 | 85.0 | 77.3 | 80.4 | 83.4 | 2.00 | 0.00 |

### ShardedMap

| Operation | 500 | 1,000 | 5,000 | 10,000 | 50,000 | B/op | allocs/op |
|---|---:|---:|---:|---:|---:|---:|---:|
| Populate \* | 49.8 | 52.3 | 48.2 | 64.6 | 65.5 | 47.5 | 0.01 |
| Get | 21.4 | 21.3 | 22.8 | 23.1 | 26.0 | 0.00 | 0.00 |
| Has | 20.8 | 20.3 | 22.3 | 22.9 | 25.9 | 0.00 | 0.00 |
| Set | 25.8 | 27.4 | 28.6 | 29.3 | 40.7 | 0.00 | 0.00 |
| Update | 29.7 | 30.0 | 31.8 | 32.3 | 42.0 | 0.00 | 0.00 |
| PopAll \* | 39.8 | 40.5 | 42.4 | 42.1 | 49.4 | 0.00 | 0.00 |
| DeleteAll \* | 40.2 | 39.6 | 42.2 | 42.0 | 49.2 | 0.00 | 0.00 |
| SetOrGet | 25.5 | 25.1 | 26.1 | 26.6 | 29.4 | 0.00 | 0.00 |
| Modify | 31.9 | 30.8 | 32.1 | 33.1 | 35.6 | 0.00 | 0.00 |
| ShardIndex | 6.49 | 6.49 | 6.48 | 6.47 | 6.48 | 0.00 | 0.00 |
| Len | 151 | 154 | 152 | 160 | 161 | 0.00 | 0.00 |
| Range \* | 32.2 | 29.4 | 26.3 | 26.8 | 28.9 | 23.7 | 0.00 |
| PopulateThreaded \* | 101 | 90.0 | 81.8 | 128 | 98.1 | 94.8 | 0.02 |
| Parallel | 50.6 | 48.2 | 49.1 | 49.9 | 47.5 | 0.00 | 0.00 |

### ShardedCacheMapGlobal — LRU

| Operation | 500 | 1,000 | 5,000 | 10,000 | 50,000 | B/op | allocs/op |
|---|---:|---:|---:|---:|---:|---:|---:|
| Populate \* | 96.4 | 101 | 104 | 130 | 137 | 72.0 | 1.01 |
| Get | 59.6 | 62.6 | 66.4 | 66.9 | 74.3 | 0.00 | 0.00 |
| Has | 17.2 | 17.5 | 19.7 | 19.6 | 21.4 | 0.00 | 0.00 |
| Set | 72.8 | 75.3 | 79.0 | 77.8 | 91.3 | 0.00 | 0.00 |
| Update | 56.7 | 57.5 | 60.8 | 60.6 | 66.8 | 0.00 | 0.00 |
| PopAll \* | 81.0 | 86.2 | 88.4 | 90.1 | 107 | 0.00 | 0.00 |
| DeleteAll \* | 78.1 | 77.0 | 83.2 | 81.5 | 98.3 | 0.00 | 0.00 |
| SetEvicting | 200 | 213 | 235 | 248 | 238 | 25.0 | 1.00 |
| SetOrGet | 60.0 | 64.6 | 68.2 | 68.8 | 81.5 | 0.00 | 0.00 |
| SetWithTTL | 91.9 | 95.7 | 98.4 | 99.1 | 127 | 1.00 | 0.00 |
| Top | 46.2 | 46.1 | 46.5 | 46.4 | 48.3 | 0.00 | 0.00 |
| Len | 11.2 | 11.1 | 11.1 | 11.2 | 11.2 | 0.00 | 0.00 |
| PopulateThreaded \* | 170 | 171 | 168 | 168 | 164 | 143 | 1.02 |
| Parallel | 157 | 153 | 158 | 161 | 174 | 0.00 | 0.00 |

### ShardedCacheMapGlobal — LFU

| Operation | 500 | 1,000 | 5,000 | 10,000 | 50,000 | B/op | allocs/op |
|---|---:|---:|---:|---:|---:|---:|---:|
| Populate \* | 198 | 204 | 203 | 212 | 212 | 120 | 1.02 |
| Get | 126 | 133 | 149 | 141 | 183 | 0.00 | 0.00 |
| Has | 17.1 | 18.6 | 19.1 | 19.7 | 21.5 | 0.00 | 0.00 |
| Set | 142 | 164 | 156 | 164 | 193 | 0.00 | 0.00 |
| Update | 72.7 | 75.8 | 79.7 | 79.8 | 92.5 | 0.00 | 0.00 |
| PopAll \* | 115 | 112 | 122 | 121 | 148 | 0.00 | 0.00 |
| DeleteAll \* | 110 | 114 | 123 | 119 | 145 | 0.00 | 0.00 |
| SetEvicting | 283 | 303 | 339 | 345 | 357 | 28.0 | 1.00 |
| SetOrGet | 126 | 133 | 140 | 148 | 185 | 0.00 | 0.00 |
| SetWithTTL | 165 | 174 | 181 | 201 | 237 | 3.00 | 0.00 |
| Top | 50.8 | 50.6 | 50.7 | 50.7 | 52.5 | 0.00 | 0.00 |
| Len | 11.1 | 11.1 | 11.1 | 11.1 | 11.1 | 0.00 | 0.00 |
| PopulateThreaded \* | 236 | 238 | 250 | 243 | 219 | 191 | 1.02 |
| Parallel | 211 | 228 | 229 | 238 | 288 | 5.00 | 0.00 |
