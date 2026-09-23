# jr_cache compared with other Go caches

A side-by-side of `jr_cache` and nine widely used Go in-memory caches and
concurrent maps, on the same machine, driving every library through the same
workload. Two kinds of measurement: **hit ratio** (how good the eviction
policy is) and **throughput** (how fast each operation is, alone and under
contention). A feature comparison follows the numbers.

- [Setup](#setup)
- [Hit ratio](#hit-ratio)
- [Throughput](#throughput)
- [What the numbers say](#what-the-numbers-say)
- [Feature comparison](#feature-comparison)
- [Which one to pick](#which-one-to-pick)
- [Reproducing](#reproducing)

## Setup

| | |
|---|---|
| Machine | Intel Core Ultra 9 275HX (24 threads), Windows, Go 1.26.2 |
| Harness | `tests/compare` — its own module so the third-party dependencies never touch `jr_cache`'s `go.mod` |
| Keys / values | `string` keys (`key:<n>`, pre-built) and `int` values; byte-oriented caches get the value as 8 little-endian bytes |
| Capacities | 10,000 and 100,000 entries |
| Traces | Pre-computed and identical for every library: a Zipf trace (s = 1.01) and a uniform trace over a key space 2× (throughput) or 10× (hit ratio) the capacity |
| Timing | `-benchtime=500ms`, one chunk per benchmark, machine otherwise idle |

Libraries and how each was configured:

| Library | Version | Configuration |
|---|---|---|
| **jr_cache** `CacheMap` | this repo | LRU and LFU, `Locked: true` |
| **jr_cache** `ShardedCacheMap` | this repo | LRU and LFU, 16 shards, `Locked: true`; LRU also with `GlobalOrder: true` |
| **jr_cache** `ShardedMap` | this repo | 16 shards (unbounded) |
| hashicorp/golang-lru/v2 | v2.0.7 | `lru.New` (LRU) and `lru.New2Q` |
| dgraph-io/ristretto/v2 | v2.4.2 | `NumCounters: 10×n`, `MaxCost: n`, cost 1 per entry, `IgnoreInternalCost: true`, `BufferItems: 64` |
| maypok86/otter/v2 | v2.3.0 | `MaximumSize: n` |
| Yiling-J/theine-go | v0.6.2 | `NewBuilder(n)`, cost 1 per entry |
| bluele/gcache | v0.0.2 | `New(n).LRU()` and `.LFU()` |
| allegro/bigcache/v3 † | v3.2.0 | 16 shards, `HardMaxCacheSize` ≈ n × 40 B rounded up to whole MB, 1 h life window |
| coocood/freecache † | v1.2.7 | `NewCache(n × 40 B)` |
| patrickmn/go-cache ‡ | v2.1.0 | `New(NoExpiration, 0)` |
| orcaman/concurrent-map/v2 ‡ | v2.0.1 | `New[int]()` (32 shards) |

† bounded by a **byte budget**, so the entry count is only approximately the
requested capacity. At 10,000 entries bigcache's hard limit rounds up to its
1 MB minimum, which holds roughly 2.5× the requested entries — read its
10,000-entry hit ratios with that in mind; the 100,000-entry rows are fair.
‡ **unbounded**: never evicts, so it skips the eviction benchmark and the
hit-ratio test. Included as the baseline of "a concurrent map with no policy".

Things that make a comparison like this only approximately fair, and that
you should keep in mind when reading:

- **What a `Get` does differs.** In `jr_cache`, golang-lru and gcache a hit
  updates the LRU/LFU order under a mutex. ristretto, otter and theine
  record hits in per-goroutine buffers and apply them to the policy later,
  which is why their reads scale so much better and why a hit counts
  slightly differently.
- **Writes can be asynchronous.** ristretto buffers `Set`s and may drop
  them; the hit-ratio test calls `Wait()` after every miss so its numbers
  reflect the policy, but its throughput rows do not include that wait.
- **Admission is not eviction.** TinyLFU-based caches (ristretto, otter,
  theine) may refuse to admit a new key at all; a "Set that evicts" for
  them is sometimes a "Set that is rejected".
- **Byte caches serialise.** bigcache and freecache copy keys and values
  into byte slabs; part of their cost is the encode/decode this harness
  does for them.

## Hit ratio

Each request reads a key and, on a miss, writes it. The table shows the
share of requests served from the cache. Higher is better; the eviction
policy is the only thing that differs between rows with the same capacity.

#### Capacity 10,000, key space 100,000, 1,048,576 requests

| Library | Zipf (s = 1.01) | Uniform |
|---|---:|---:|
| **jr_cache/CacheMap-LRU** | 74.73 % | 10.00 % |
| **jr_cache/CacheMap-LFU** | 78.70 % | 9.97 % |
| **jr_cache/Sharded-LRU** | 74.72 % | 10.00 % |
| **jr_cache/Sharded-LFU** | 78.69 % | 9.97 % |
| **jr_cache/Sharded-LRU-global** | 74.73 % | 10.00 % |
| hashicorp/golang-lru | 74.73 % | 10.00 % |
| hashicorp/golang-lru-2Q | 77.79 % | 9.98 % |
| dgraph-io/ristretto | 77.64 % | 9.98 % |
| maypok86/otter | 78.75 % | 13.10 % |
| Yiling-J/theine | 79.63 % | 10.02 % |
| bluele/gcache-LRU | 74.73 % | 10.00 % |
| bluele/gcache-LFU | 78.56 % | 9.99 % |
| allegro/bigcache † | 82.85 % | 28.80 % |
| coocood/freecache † | 73.95 % | 12.75 % |

#### Capacity 100,000, key space 1,000,000, 1,048,576 requests

| Library | Zipf (s = 1.01) | Uniform |
|---|---:|---:|
| **jr_cache/CacheMap-LRU** | 76.96 % | 9.49 % |
| **jr_cache/CacheMap-LFU** | 77.47 % | 9.48 % |
| **jr_cache/Sharded-LRU** | 76.95 % | 9.49 % |
| **jr_cache/Sharded-LFU** | 77.48 % | 9.48 % |
| **jr_cache/Sharded-LRU-global** | 76.96 % | 9.49 % |
| hashicorp/golang-lru | 76.96 % | 9.49 % |
| hashicorp/golang-lru-2Q | 77.52 % | 9.47 % |
| dgraph-io/ristretto | 77.49 % | 9.46 % |
| maypok86/otter | 76.92 % | 9.48 % |
| Yiling-J/theine | 77.36 % | 9.47 % |
| bluele/gcache-LRU | 76.96 % | 9.49 % |
| bluele/gcache-LFU | 77.38 % | 9.49 % |
| allegro/bigcache † | 73.87 % | 8.15 % |
| coocood/freecache † | 74.62 % | 9.07 % |

Reading it:

- Every plain LRU lands on the same number, as it must: `jr_cache` LRU (all
  three variants), golang-lru and gcache-LRU agree to the second decimal.
- On the skewed trace at 10,000 entries the frequency-aware policies win:
  theine 79.6 %, `jr_cache` LFU 78.7 %, otter 78.8 %, gcache-LFU 78.6 %,
  2Q 77.8 %, ristretto 77.6 %, against 74.7 % for LRU. `jr_cache`'s LFU
  (exact counts, LRU tie-break) is within a point of the W-TinyLFU
  implementations at a fraction of their complexity.
- On the uniform trace nothing can beat the 10 % that the capacity / key
  space ratio dictates; otter's 13 % comes from its admission window
  retaining recent keys a little longer, bigcache's 28.8 % from holding
  ~2.5× the entries (see †).
- At 100,000 entries with one million requests the traces are too short
  for the policies to separate; all bounded caches sit at 77 ± 0.5 %.

## Throughput

`ns/op` per library and capacity, allocations per operation, and the
100,000-entry cost relative to `jr_cache/CacheMap-LRU`. Lower is better.

### Get, key present (1 goroutine)

| Library | 10,000 ns/op | 100,000 ns/op | 10,000 allocs | 100,000 allocs | vs jr_cache LRU |
|---|---:|---:|---:|---:|---:|
| **jr_cache/CacheMap-LRU** | 34.8 | 39.5 | 0.00 | 0.00 | 1.00× |
| **jr_cache/CacheMap-LFU** | 102 | 144 | 0.00 | 0.00 | 3.65× |
| **jr_cache/Sharded-LRU** | 55.6 | 67.5 | 0.00 | 0.00 | 1.71× |
| **jr_cache/Sharded-LFU** | 168 | 228 | 0.00 | 0.00 | 5.79× |
| **jr_cache/Sharded-LRU-global** | 85.3 | 114 | 0.00 | 0.00 | 2.88× |
| hashicorp/golang-lru | 27.6 | 40.3 | 0.00 | 0.00 | 1.02× |
| hashicorp/golang-lru-2Q | 28.8 | 43.5 | 0.00 | 0.00 | 1.10× |
| dgraph-io/ristretto | 57.0 | 76.9 | 0.00 | 0.00 | 1.95× |
| maypok86/otter | 42.5 | 48.6 | 0.00 | 0.00 | 1.23× |
| Yiling-J/theine | 113 | 146 | 1.00 | 1.00 | 3.71× |
| bluele/gcache-LRU | 56.0 | 73.5 | 1.00 | 1.00 | 1.86× |
| bluele/gcache-LFU | 144 | 194 | 1.00 | 1.00 | 4.93× |
| allegro/bigcache † | 55.7 | 65.1 | 2.00 | 1.00 | 1.65× |
| coocood/freecache † | 63.2 | 91.9 | 0.00 | 0.00 | 2.33× |
| patrickmn/go-cache ‡ | 16.0 | 21.4 | 0.00 | 0.00 | 0.54× |
| orcaman/concurrent-map ‡ | 22.5 | 28.1 | 0.00 | 0.00 | 0.71× |
| **jr_cache/ShardedMap** ‡ | 28.4 | 34.2 | 0.00 | 0.00 | 0.87× |

### Get, key absent (1 goroutine)

| Library | 10,000 ns/op | 100,000 ns/op | 10,000 allocs | 100,000 allocs | vs jr_cache LRU |
|---|---:|---:|---:|---:|---:|
| **jr_cache/CacheMap-LRU** | 27.4 | 33.7 | 0.00 | 0.00 | 1.00× |
| **jr_cache/CacheMap-LFU** | 27.5 | 38.6 | 0.00 | 0.00 | 1.14× |
| **jr_cache/Sharded-LRU** | 39.0 | 53.9 | 0.00 | 0.00 | 1.60× |
| **jr_cache/Sharded-LFU** | 41.2 | 54.6 | 0.00 | 0.00 | 1.62× |
| **jr_cache/Sharded-LRU-global** | 37.8 | 49.7 | 0.00 | 0.00 | 1.47× |
| hashicorp/golang-lru | 25.5 | 33.8 | 0.00 | 0.00 | 1.00× |
| hashicorp/golang-lru-2Q | 27.8 | 38.1 | 0.00 | 0.00 | 1.13× |
| dgraph-io/ristretto | 52.8 | 70.3 | 0.00 | 0.00 | 2.09× |
| maypok86/otter | 13.9 | 15.3 | 0.00 | 0.00 | 0.45× |
| Yiling-J/theine | 47.2 | 62.0 | 0.00 | 0.00 | 1.84× |
| bluele/gcache-LRU | 51.8 | 65.8 | 1.00 | 1.00 | 1.95× |
| bluele/gcache-LFU | 51.2 | 65.2 | 1.00 | 1.00 | 1.93× |
| allegro/bigcache † | 26.1 | 35.7 | 0.00 | 0.00 | 1.06× |
| coocood/freecache † | 41.6 | 55.9 | 0.00 | 0.00 | 1.66× |
| patrickmn/go-cache ‡ | 15.9 | 27.9 | 0.00 | 0.00 | 0.83× |
| orcaman/concurrent-map ‡ | 24.0 | 39.0 | 0.00 | 0.00 | 1.16× |
| **jr_cache/ShardedMap** ‡ | 29.1 | 42.1 | 0.00 | 0.00 | 1.25× |

### Set, key present (1 goroutine)

| Library | 10,000 ns/op | 100,000 ns/op | 10,000 allocs | 100,000 allocs | vs jr_cache LRU |
|---|---:|---:|---:|---:|---:|
| **jr_cache/CacheMap-LRU** | 40.8 | 44.9 | 0.00 | 0.00 | 1.00× |
| **jr_cache/CacheMap-LFU** | 122 | 191 | 0.00 | 0.00 | 4.25× |
| **jr_cache/Sharded-LRU** | 104 | 139 | 0.00 | 0.00 | 3.10× |
| **jr_cache/Sharded-LFU** | 218 | 296 | 0.00 | 0.00 | 6.60× |
| **jr_cache/Sharded-LRU-global** | 104 | 134 | 0.00 | 0.00 | 2.98× |
| hashicorp/golang-lru | 33.3 | 40.6 | 0.00 | 0.00 | 0.90× |
| hashicorp/golang-lru-2Q | 37.8 | 46.4 | 0.00 | 0.00 | 1.03× |
| dgraph-io/ristretto | 224 | 221 | 1.00 | 1.00 | 4.91× |
| maypok86/otter | 154 | 150 | 1.00 | 1.00 | 3.34× |
| Yiling-J/theine | 148 | 193 | 0.00 | 0.00 | 4.31× |
| bluele/gcache-LRU | 58.5 | 77.3 | 1.00 | 1.00 | 1.72× |
| bluele/gcache-LFU | 56.0 | 72.5 | 1.00 | 1.00 | 1.62× |
| allegro/bigcache † | 79.9 | 120 | 0.00 | 0.00 | 2.67× |
| coocood/freecache † | 50.9 | 115 | 0.00 | 0.00 | 2.55× |
| patrickmn/go-cache ‡ | 35.6 | 46.5 | 0.00 | 0.00 | 1.03× |
| orcaman/concurrent-map ‡ | 43.9 | 49.6 | 0.00 | 0.00 | 1.10× |
| **jr_cache/ShardedMap** ‡ | 35.1 | 51.8 | 0.00 | 0.00 | 1.15× |

### Set, new key on a full cache — every write evicts (1 goroutine)

| Library | 10,000 ns/op | 100,000 ns/op | 10,000 allocs | 100,000 allocs | vs jr_cache LRU |
|---|---:|---:|---:|---:|---:|
| **jr_cache/CacheMap-LRU** | 113 | 92.3 | 1.00 | 1.00 | 1.00× |
| **jr_cache/CacheMap-LFU** | 196 | 211 | 1.00 | 1.00 | 2.29× |
| **jr_cache/Sharded-LRU** | 160 | 152 | 1.00 | 1.00 | 1.64× |
| **jr_cache/Sharded-LFU** | 288 | 290 | 1.00 | 1.00 | 3.14× |
| **jr_cache/Sharded-LRU-global** | 293 | 299 | 1.00 | 1.00 | 3.24× |
| hashicorp/golang-lru | 123 | 118 | 1.00 | 1.00 | 1.28× |
| hashicorp/golang-lru-2Q | 317 | 280 | 2.00 | 2.00 | 3.03× |
| dgraph-io/ristretto | 137 | 140 | 1.00 | 1.00 | 1.51× |
| maypok86/otter | 165 | 186 | 1.00 | 1.00 | 2.01× |
| Yiling-J/theine | 164 | 219 | 0.00 | 0.00 | 2.37× |
| bluele/gcache-LRU | 181 | 170 | 3.00 | 4.00 | 1.85× |
| bluele/gcache-LFU | 232 | 383 | 2.00 | 2.00 | 4.15× |
| allegro/bigcache † | 94.2 | 128 | 0.00 | 0.00 | 1.39× |
| coocood/freecache † | 97.6 | 132 | 0.00 | 0.00 | 1.43× |

### Mixed 90 % Get / 10 % Set, Zipf keys over 2× capacity (1 goroutine)

| Library | 10,000 ns/op | 100,000 ns/op | 10,000 allocs | 100,000 allocs | vs jr_cache LRU |
|---|---:|---:|---:|---:|---:|
| **jr_cache/CacheMap-LRU** | 39.4 | 56.3 | 0.00 | 0.00 | 1.00× |
| **jr_cache/CacheMap-LFU** | 207 | 304 | 0.00 | 0.00 | 5.39× |
| **jr_cache/Sharded-LRU** | 67.2 | 99.5 | 0.00 | 0.00 | 1.77× |
| **jr_cache/Sharded-LFU** | 302 | 362 | 0.00 | 0.00 | 6.42× |
| **jr_cache/Sharded-LRU-global** | 86.5 | 120 | 0.00 | 0.00 | 2.13× |
| hashicorp/golang-lru | 35.5 | 54.8 | 0.00 | 0.00 | 0.97× |
| hashicorp/golang-lru-2Q | 42.2 | 67.5 | 0.00 | 0.00 | 1.20× |
| dgraph-io/ristretto | 72.4 | 89.4 | 0.00 | 0.00 | 1.59× |
| maypok86/otter | 63.0 | 84.1 | 0.00 | 0.00 | 1.49× |
| Yiling-J/theine | 131 | 183 | 0.00 | 0.00 | 3.25× |
| bluele/gcache-LRU | 71.2 | 110 | 1.00 | 1.00 | 1.95× |
| bluele/gcache-LFU | 137 | 171 | 1.00 | 1.00 | 3.03× |
| allegro/bigcache † | 66.7 | 77.1 | 1.00 | 1.00 | 1.37× |
| coocood/freecache † | 75.2 | 98.6 | 0.00 | 0.00 | 1.75× |
| patrickmn/go-cache ‡ | 20.2 | 38.3 | 0.00 | 0.00 | 0.68× |
| orcaman/concurrent-map ‡ | 27.5 | 36.5 | 0.00 | 0.00 | 0.65× |
| **jr_cache/ShardedMap** ‡ | 31.7 | 40.0 | 0.00 | 0.00 | 0.71× |

### Get, key present, 24 goroutines

| Library | 10,000 ns/op | 100,000 ns/op | 10,000 allocs | 100,000 allocs | vs jr_cache LRU |
|---|---:|---:|---:|---:|---:|
| **jr_cache/CacheMap-LRU** | 91.9 | 93.3 | 0.00 | 0.00 | 1.00× |
| **jr_cache/CacheMap-LFU** | 269 | 312 | 0.00 | 0.00 | 3.34× |
| **jr_cache/Sharded-LRU** | 41.1 | 45.6 | 0.00 | 0.00 | 0.49× |
| **jr_cache/Sharded-LFU** | 82.1 | 86.4 | 0.00 | 0.00 | 0.93× |
| **jr_cache/Sharded-LRU-global** | 147 | 206 | 0.00 | 0.00 | 2.21× |
| hashicorp/golang-lru | 70.0 | 82.5 | 0.00 | 0.00 | 0.88× |
| hashicorp/golang-lru-2Q | 69.3 | 84.7 | 0.00 | 0.00 | 0.91× |
| dgraph-io/ristretto | 6.49 | 7.00 | 0.00 | 0.00 | 0.07× |
| maypok86/otter | 2.92 | 3.53 | 0.00 | 0.00 | 0.04× |
| Yiling-J/theine | 4.52 | 4.95 | 0.00 | 0.00 | 0.05× |
| bluele/gcache-LRU | 86.6 | 110 | 1.00 | 1.00 | 1.18× |
| bluele/gcache-LFU | 151 | 225 | 1.00 | 1.00 | 2.41× |
| allegro/bigcache † | 11.6 | 12.3 | 2.00 | 1.00 | 0.13× |
| coocood/freecache † | 13.4 | 13.7 | 0.00 | 0.00 | 0.15× |
| patrickmn/go-cache ‡ | 63.2 | 61.6 | 0.00 | 0.00 | 0.66× |
| orcaman/concurrent-map ‡ | 12.7 | 13.1 | 0.00 | 0.00 | 0.14× |
| **jr_cache/ShardedMap** ‡ | 15.8 | 15.8 | 0.00 | 0.00 | 0.17× |

### Mixed 90 % Get / 10 % Set, Zipf keys, 24 goroutines

| Library | 10,000 ns/op | 100,000 ns/op | 10,000 allocs | 100,000 allocs | vs jr_cache LRU |
|---|---:|---:|---:|---:|---:|
| **jr_cache/CacheMap-LRU** | 106 | 119 | 0.00 | 0.00 | 1.00× |
| **jr_cache/CacheMap-LFU** | 379 | 409 | 0.00 | 0.00 | 3.44× |
| **jr_cache/Sharded-LRU** | 60.4 | 60.3 | 0.00 | 0.00 | 0.51× |
| **jr_cache/Sharded-LFU** | 176 | 192 | 0.00 | 0.00 | 1.62× |
| **jr_cache/Sharded-LRU-global** | 172 | 214 | 0.00 | 0.00 | 1.80× |
| hashicorp/golang-lru | 97.4 | 110 | 0.00 | 0.00 | 0.93× |
| hashicorp/golang-lru-2Q | 103 | 122 | 0.00 | 0.00 | 1.03× |
| dgraph-io/ristretto | 49.9 | 42.7 | 0.00 | 0.00 | 0.36× |
| maypok86/otter | 26.8 | 32.7 | 0.00 | 0.00 | 0.28× |
| Yiling-J/theine | 48.0 | 46.2 | 0.00 | 0.00 | 0.39× |
| bluele/gcache-LRU | 139 | 155 | 1.00 | 1.00 | 1.31× |
| bluele/gcache-LFU | 200 | 219 | 1.00 | 1.00 | 1.84× |
| allegro/bigcache † | 63.8 | 63.1 | 1.00 | 1.00 | 0.53× |
| coocood/freecache † | 30.0 | 25.8 | 0.00 | 0.00 | 0.22× |
| patrickmn/go-cache ‡ | 112 | 140 | 0.00 | 0.00 | 1.18× |
| orcaman/concurrent-map ‡ | 41.2 | 41.1 | 0.00 | 0.00 | 0.35× |
| **jr_cache/ShardedMap** ‡ | 55.9 | 54.0 | 0.00 | 0.00 | 0.45× |

## What the numbers say

**Single-threaded, `jr_cache` LRU is as fast as the fastest bounded
cache.** Its `Get` (35–40 ns), `Set` (41–45 ns) and mixed Zipf loop
(39–56 ns) are within a few nanoseconds of hashicorp/golang-lru, and ahead
of every other bounded library: ristretto, otter and theine pay 1.5–3.5×
for their buffers and admission filters when there is no contention to
amortise them against. Eviction (`SetEvict`, 92–113 ns) is likewise the
cheapest of the bounded group.

**Under contention the picture splits in two.** With 24 goroutines
reading, `jr_cache`'s sharded LRU (41–46 ns) beats every *mutex-based*
cache — golang-lru (70–83 ns), gcache, go-cache — and the single locked
`CacheMap` (92 ns). But the caches that record accesses without a lock are
in another class: otter serves a parallel hit in 3 ns, theine in 4.5 ns,
ristretto in 6.5 ns, bigcache/freecache/concurrent-map in 12–14 ns. The
reason is structural: every `jr_cache` cache `Get` takes the shard's
**write** lock so it can move the entry in the LRU list or bump its
frequency bucket. Sharding divides that contention by 16; the lock-free
designs remove it. On the mixed parallel workload the gap narrows (otter
27–33 ns, ristretto/theine 43–50 ns, `jr_cache` sharded LRU 60 ns) because
writes force the other libraries to touch their policies too.

**`jr_cache`'s LFU is the expensive one.** Exact LFU moves the entry to a
new frequency bucket on every hit; with string keys that is 100–145 ns per
`Get` single-threaded and 3–5× LRU on the mixed loops. It buys a hit ratio
on par with W-TinyLFU, but the frequency-sketch designs get that hit ratio
for ~1.5× LRU cost instead. If you want frequency-aware eviction *and*
speed, `jr_cache` LFU is the wrong tool and theine/otter are the right one.

**Global order costs what it says.** `Sharded-LRU-global` is 2–3× the
per-shard variant on every row, and slower than the plain locked `CacheMap`
under contention (172–214 vs 106–119 ns), since each call takes the index
lock and a shard lock. It exists for exact eviction and O(1) `Top`/`Len`,
not for speed.

**As a plain concurrent map, `jr_cache/ShardedMap` is competitive.**
16–29 ns single-threaded reads and 16 ns parallel reads, against
concurrent-map's 22–28 / 13 ns — the difference is `maphash` on the key
versus concurrent-map's fnv32. Nothing here allocates.

**Allocations.** `jr_cache` allocates nothing on any hit or overwrite and
one node per inserted key. gcache allocates on every call (`interface{}`
boxing), theine on every hit, bigcache on every read (the returned slice).

## Feature comparison

Taken from each library's documentation and API at the versions above.

| | jr_cache | golang-lru v2 | ristretto v2 | otter v2 | theine | gcache | bigcache | freecache | go-cache | concurrent-map v2 |
|---|---|---|---|---|---|---|---|---|---|---|
| Eviction policies | LRU, MRU, FIFO, LIFO, LFU, MFU | LRU, 2Q (ARC in a sub-module) | TinyLFU admission + sampled LFU | adaptive W-TinyLFU | adaptive W-TinyLFU | LRU, LFU, ARC, simple | oldest-first within a life window | approximate LRU | none | none |
| Capacity bound | entry count | entry count | cost (any unit) | entries or weight | cost (any unit) | entry count | bytes | bytes | none | none |
| Per-entry TTL | yes, lazy + optional background sweeper (`GCManager`) | via `expirable` package (one TTL per cache) | yes | yes (`ExpiryCalculator`) | yes | yes | no (global life window) | yes (seconds) | yes + janitor goroutine | no |
| Generics | `[K comparable, V any]` | `[K comparable, V any]` | `[K Key, V any]` (K restricted) | `[K comparable, V any]` | `[K comparable, V any]` | `interface{}` | `string` → `[]byte` | `[]byte` → `[]byte` | `string` → `interface{}` | `[K comparable, V any]` |
| Concurrency model | opt-in mutex; sharded variant (per-shard or global order) | one mutex | lock-free reads, buffered writes, 256 shards | lock-free reads, buffered policy | lock-free reads, buffered policy | one mutex | per-shard mutex | per-segment mutex | one RWMutex | per-shard RWMutex |
| Ordered access (`Top`/`Bottom`, pop either end) | yes, every structure | `GetOldest`, `RemoveOldest` | no | no | no | no | no | no | no | no |
| Eviction / removal hook | `SetOnEvict` | `NewWithEvict` | `OnEvict`, `OnReject`, `OnExit` | `OnDeletion` | `RemovalListener` | `EvictedFunc`, `PurgeVisitorFunc` | `OnRemove` | no | `OnEvicted` | no |
| Loader / get-or-compute | `SetOrGet` (value, not loader) | no | no | `Get` with `Loader` | `LoadingCache` | `LoaderFunc` | no | `GetOrSet` | no | `Upsert`, `SetIfAbsent` |
| Atomic read-modify-write | `Modify` (sharded map) | no | no | `Compute` | no | no | no | no | `Increment*` | `Upsert`, `RemoveCb` |
| Bulk load | `Populate` (per-shard, threaded) | no | no | no (`BulkGet` only) | no | no | no | no | `Load` (gob) | `MSet` |
| Iteration | `Range` (sharded map), `NextKey` walk (ordered map) | `Keys`, `Values` | no | `All`, `Keys` (iterators) | `Range` | `GetALL`, `Keys` | `Iterator` | `NewIterator` | `Items` | `IterBuffered`, `Keys` |
| Stats / metrics | no | no | `Metrics` | `StatsRecorder` | `Stats` | `HitRate` etc. | `Stats` | `HitRate` etc. | `ItemCount` | `Count` |
| Serialization / persistence | snapshots to JSON or gob, gzip/zlib, order + TTLs preserved | no | no | no | `HybridCache` (disk tier) | `SerializeFunc`/`DeserializeFunc` hooks | no | no | `Save`/`SaveFile`/`LoadFile` (gob) | no |
| Runtime GC control | `DisableGCCompletely` / `RestoreGC` (process-global; see the caveats) | no | no | no | no | no | no | no | no | no |
| Building blocks exposed | ordered map, bucket map, sharded map | `simplelru` | no | no | no | no | no | no | no | it is the block |
| Background goroutines | none unless a `GCManager` is started | `expirable` starts one | yes (policy workers) | none by default | yes | none | yes (clean window) | none | yes (janitor) | none |
| Dependencies | none | none | 8 | 3 | 10 | none | none | none | none | none |
| Zero-GC storage | no | no | no | no | no | no | yes (byte slabs) | yes (ring buffer) | no | no |

## Which one to pick

- **You want one API with a choice of policy, exact LRU/LFU semantics,
  ordered access to the least/most end, or the underlying structures as
  first-class types** — `jr_cache`. Single-threaded it is as fast as
  anything bounded; sharded it is the fastest of the mutex-based caches.
- **Your bottleneck is many goroutines reading the same hot cache** —
  otter or theine. Their lock-free read path is 10–30× faster than any
  mutex design, `jr_cache` included, and their hit ratios are the best in
  the table. ristretto is in the same family with a longer track record
  and a cost-based bound.
- **You need to hold tens of millions of entries and keep the GC out of
  it** — bigcache or freecache, and accept `[]byte` values and an
  approximate policy.
- **You need a plain concurrent map** — `jr_cache/ShardedMap` and
  concurrent-map are equivalent; pick by API (`Modify` + `Populate` vs
  `Upsert` + `IterBuffered`).
- **golang-lru** remains the smallest correct LRU if that is all you need;
  `jr_cache` matches it on speed and adds the rest.

## Reproducing

```
cd tests/compare
go test -run xxx -bench . -benchmem -benchtime=500ms   # throughput (~4 min)
go test -run HitRatio -v                                # hit ratios (~20 s)
```

The raw benchmark output this report was generated from is in
`comparison_raw.txt` next to it. Results shift by ±5 % between runs and
more between sessions on the same machine; treat differences under 10 % as
noise. To add a library, implement the five-method `cache` adapter in
`tests/compare/adapters_test.go` and append it to `candidates`.
