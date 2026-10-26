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
| **jr_cache/Sharded-LFU** | 78.70 % | 9.98 % |
| **jr_cache/Sharded-LRU-global** | 74.73 % | 10.00 % |
| hashicorp/golang-lru | 74.73 % | 10.00 % |
| hashicorp/golang-lru-2Q | 77.79 % | 9.98 % |
| dgraph-io/ristretto | 77.65 % | 9.95 % |
| maypok86/otter | 78.50 % | 12.99 % |
| Yiling-J/theine | 79.60 % | 9.98 % |
| bluele/gcache-LRU | 74.73 % | 10.00 % |
| bluele/gcache-LFU | 78.53 % | 9.97 % |
| allegro/bigcache † | 82.85 % | 28.80 % |
| coocood/freecache † | 73.95 % | 12.75 % |

#### Capacity 100,000, key space 1,000,000, 1,048,576 requests

| Library | Zipf (s = 1.01) | Uniform |
|---|---:|---:|
| **jr_cache/CacheMap-LRU** | 76.96 % | 9.49 % |
| **jr_cache/CacheMap-LFU** | 77.47 % | 9.48 % |
| **jr_cache/Sharded-LRU** | 76.96 % | 9.49 % |
| **jr_cache/Sharded-LFU** | 77.47 % | 9.47 % |
| **jr_cache/Sharded-LRU-global** | 76.96 % | 9.49 % |
| hashicorp/golang-lru | 76.96 % | 9.49 % |
| hashicorp/golang-lru-2Q | 77.52 % | 9.47 % |
| dgraph-io/ristretto | 77.49 % | 9.51 % |
| maypok86/otter | 76.79 % | 9.51 % |
| Yiling-J/theine | 77.36 % | 9.46 % |
| bluele/gcache-LRU | 76.96 % | 9.49 % |
| bluele/gcache-LFU | 77.40 % | 9.49 % |
| allegro/bigcache † | 73.87 % | 8.15 % |
| coocood/freecache † | 74.62 % | 9.07 % |

Reading it:

- Every plain LRU lands on the same number, as it must: `jr_cache` LRU (all
  three variants), golang-lru and gcache-LRU agree to the second decimal.
- On the skewed trace at 10,000 entries the frequency-aware policies win:
  theine 79.6 %, `jr_cache` LFU 78.7 %, otter 78.5 %, gcache-LFU 78.5 %,
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
| **jr_cache/CacheMap-LRU** | 30.4 | 32.3 | 0.00 | 0.00 | 1.00× |
| **jr_cache/CacheMap-LFU** | 30.7 | 35.2 | 0.00 | 0.00 | 1.09× |
| **jr_cache/Sharded-LRU** | 35.7 | 42.8 | 0.00 | 0.00 | 1.32× |
| **jr_cache/Sharded-LFU** | 37.0 | 43.4 | 0.00 | 0.00 | 1.34× |
| **jr_cache/Sharded-LRU-global** | 49.6 | 64.7 | 0.00 | 0.00 | 2.00× |
| hashicorp/golang-lru | 26.1 | 28.6 | 0.00 | 0.00 | 0.88× |
| hashicorp/golang-lru-2Q | 25.7 | 30.4 | 0.00 | 0.00 | 0.94× |
| dgraph-io/ristretto | 34.6 | 48.4 | 0.00 | 0.00 | 1.50× |
| maypok86/otter | 32.6 | 41.4 | 0.00 | 0.00 | 1.28× |
| Yiling-J/theine | 82.5 | 107 | 1.00 | 1.00 | 3.32× |
| bluele/gcache-LRU | 45.2 | 58.4 | 1.00 | 1.00 | 1.81× |
| bluele/gcache-LFU | 104 | 149 | 1.00 | 1.00 | 4.60× |
| allegro/bigcache † | 42.9 | 53.2 | 2.00 | 1.00 | 1.65× |
| coocood/freecache † | 53.0 | 89.0 | 0.00 | 0.00 | 2.75× |
| patrickmn/go-cache ‡ | 13.4 | 17.5 | 0.00 | 0.00 | 0.54× |
| orcaman/concurrent-map ‡ | 18.0 | 23.6 | 0.00 | 0.00 | 0.73× |
| **jr_cache/ShardedMap** ‡ | 18.3 | 25.1 | 0.00 | 0.00 | 0.78× |

### Get, key absent (1 goroutine)

| Library | 10,000 ns/op | 100,000 ns/op | 10,000 allocs | 100,000 allocs | vs jr_cache LRU |
|---|---:|---:|---:|---:|---:|
| **jr_cache/CacheMap-LRU** | 25.4 | 31.9 | 0.00 | 0.00 | 1.00× |
| **jr_cache/CacheMap-LFU** | 24.6 | 32.1 | 0.00 | 0.00 | 1.01× |
| **jr_cache/Sharded-LRU** | 30.2 | 41.1 | 0.00 | 0.00 | 1.29× |
| **jr_cache/Sharded-LFU** | 29.9 | 40.9 | 0.00 | 0.00 | 1.28× |
| **jr_cache/Sharded-LRU-global** | 26.0 | 34.4 | 0.00 | 0.00 | 1.08× |
| hashicorp/golang-lru | 23.1 | 29.8 | 0.00 | 0.00 | 0.93× |
| hashicorp/golang-lru-2Q | 24.5 | 33.2 | 0.00 | 0.00 | 1.04× |
| dgraph-io/ristretto | 32.5 | 39.1 | 0.00 | 0.00 | 1.23× |
| maypok86/otter | 8.85 | 11.8 | 0.00 | 0.00 | 0.37× |
| Yiling-J/theine | 35.1 | 48.4 | 0.00 | 0.00 | 1.52× |
| bluele/gcache-LRU | 39.8 | 52.0 | 1.00 | 1.00 | 1.63× |
| bluele/gcache-LFU | 39.5 | 52.2 | 1.00 | 1.00 | 1.63× |
| allegro/bigcache † | 20.5 | 30.2 | 0.00 | 0.00 | 0.94× |
| coocood/freecache † | 36.1 | 52.8 | 0.00 | 0.00 | 1.65× |
| patrickmn/go-cache ‡ | 13.5 | 24.3 | 0.00 | 0.00 | 0.76× |
| orcaman/concurrent-map ‡ | 19.8 | 33.7 | 0.00 | 0.00 | 1.06× |
| **jr_cache/ShardedMap** ‡ | 20.8 | 33.8 | 0.00 | 0.00 | 1.06× |

### Set, key present (1 goroutine)

| Library | 10,000 ns/op | 100,000 ns/op | 10,000 allocs | 100,000 allocs | vs jr_cache LRU |
|---|---:|---:|---:|---:|---:|
| **jr_cache/CacheMap-LRU** | 29.7 | 33.0 | 0.00 | 0.00 | 1.00× |
| **jr_cache/CacheMap-LFU** | 29.5 | 35.7 | 0.00 | 0.00 | 1.08× |
| **jr_cache/Sharded-LRU** | 58.5 | 74.8 | 0.00 | 0.00 | 2.27× |
| **jr_cache/Sharded-LFU** | 60.5 | 83.5 | 0.00 | 0.00 | 2.53× |
| **jr_cache/Sharded-LRU-global** | 58.0 | 70.8 | 0.00 | 0.00 | 2.15× |
| hashicorp/golang-lru | 26.4 | 27.8 | 0.00 | 0.00 | 0.84× |
| hashicorp/golang-lru-2Q | 29.9 | 35.7 | 0.00 | 0.00 | 1.08× |
| dgraph-io/ristretto | 309 | 344 | 1.00 | 1.00 | 10.42× |
| maypok86/otter | 172 | 167 | 1.00 | 1.00 | 5.06× |
| Yiling-J/theine | 178 | 197 | 0.00 | 0.00 | 5.97× |
| bluele/gcache-LRU | 52.0 | 63.9 | 1.00 | 1.00 | 1.94× |
| bluele/gcache-LFU | 49.0 | 59.3 | 1.00 | 1.00 | 1.80× |
| allegro/bigcache † | 61.1 | 94.5 | 0.00 | 0.00 | 2.86× |
| coocood/freecache † | 42.3 | 112 | 0.00 | 0.00 | 3.40× |
| patrickmn/go-cache ‡ | 31.2 | 35.0 | 0.00 | 0.00 | 1.06× |
| orcaman/concurrent-map ‡ | 24.8 | 32.5 | 0.00 | 0.00 | 0.98× |
| **jr_cache/ShardedMap** ‡ | 28.1 | 33.1 | 0.00 | 0.00 | 1.00× |

### Set, new key on a full cache — every write evicts (1 goroutine)

| Library | 10,000 ns/op | 100,000 ns/op | 10,000 allocs | 100,000 allocs | vs jr_cache LRU |
|---|---:|---:|---:|---:|---:|
| **jr_cache/CacheMap-LRU** | 83.6 | 61.5 | 0.00 | 0.00 | 1.00× |
| **jr_cache/CacheMap-LFU** | 84.5 | 66.1 | 0.00 | 0.00 | 1.08× |
| **jr_cache/Sharded-LRU** | 90.5 | 82.4 | 0.00 | 0.00 | 1.34× |
| **jr_cache/Sharded-LFU** | 94.2 | 83.6 | 0.00 | 0.00 | 1.36× |
| **jr_cache/Sharded-LRU-global** | 179 | 173 | 0.00 | 0.00 | 2.81× |
| hashicorp/golang-lru | 99.2 | 92.4 | 1.00 | 1.00 | 1.50× |
| hashicorp/golang-lru-2Q | 244 | 209 | 2.00 | 2.00 | 3.40× |
| dgraph-io/ristretto | 271 | 204 | 2.00 | 2.00 | 3.33× |
| maypok86/otter | 191 | 193 | 1.00 | 1.00 | 3.14× |
| Yiling-J/theine | 247 | 261 | 0.00 | 0.00 | 4.25× |
| bluele/gcache-LRU | 137 | 135 | 3.00 | 4.00 | 2.19× |
| bluele/gcache-LFU | 197 | 340 | 2.00 | 2.00 | 5.54× |
| allegro/bigcache † | 70.2 | 94.0 | 0.00 | 0.00 | 1.53× |
| coocood/freecache † | 94.0 | 126 | 0.00 | 0.00 | 2.06× |

### Mixed 90 % Get / 10 % Set, Zipf keys over 2× capacity (1 goroutine)

| Library | 10,000 ns/op | 100,000 ns/op | 10,000 allocs | 100,000 allocs | vs jr_cache LRU |
|---|---:|---:|---:|---:|---:|
| **jr_cache/CacheMap-LRU** | 34.2 | 46.9 | 0.00 | 0.00 | 1.00× |
| **jr_cache/CacheMap-LFU** | 70.2 | 84.5 | 0.00 | 0.00 | 1.80× |
| **jr_cache/Sharded-LRU** | 40.0 | 56.8 | 0.00 | 0.00 | 1.21× |
| **jr_cache/Sharded-LFU** | 84.1 | 102 | 0.00 | 0.00 | 2.19× |
| **jr_cache/Sharded-LRU-global** | 50.6 | 74.8 | 0.00 | 0.00 | 1.60× |
| hashicorp/golang-lru | 29.8 | 49.4 | 0.00 | 0.00 | 1.05× |
| hashicorp/golang-lru-2Q | 33.1 | 57.9 | 0.00 | 0.00 | 1.23× |
| dgraph-io/ristretto | 59.4 | 87.2 | 0.00 | 0.00 | 1.86× |
| maypok86/otter | 62.3 | 82.2 | 0.00 | 0.00 | 1.76× |
| Yiling-J/theine | 128 | 170 | 0.00 | 0.00 | 3.62× |
| bluele/gcache-LRU | 57.4 | 105 | 1.00 | 1.00 | 2.23× |
| bluele/gcache-LFU | 118 | 151 | 1.00 | 1.00 | 3.22× |
| allegro/bigcache † | 48.3 | 66.2 | 1.00 | 1.00 | 1.41× |
| coocood/freecache † | 65.5 | 93.4 | 0.00 | 0.00 | 1.99× |
| patrickmn/go-cache ‡ | 17.2 | 33.5 | 0.00 | 0.00 | 0.71× |
| orcaman/concurrent-map ‡ | 20.5 | 29.4 | 0.00 | 0.00 | 0.63× |
| **jr_cache/ShardedMap** ‡ | 21.5 | 28.7 | 0.00 | 0.00 | 0.61× |

### Get, key present, 24 goroutines

| Library | 10,000 ns/op | 100,000 ns/op | 10,000 allocs | 100,000 allocs | vs jr_cache LRU |
|---|---:|---:|---:|---:|---:|
| **jr_cache/CacheMap-LRU** | 99.5 | 89.4 | 0.00 | 0.00 | 1.00× |
| **jr_cache/CacheMap-LFU** | 88.9 | 90.7 | 0.00 | 0.00 | 1.01× |
| **jr_cache/Sharded-LRU** | 39.0 | 42.7 | 0.00 | 0.00 | 0.48× |
| **jr_cache/Sharded-LFU** | 33.5 | 36.1 | 0.00 | 0.00 | 0.40× |
| **jr_cache/Sharded-LRU-global** | 164 | 231 | 0.00 | 0.00 | 2.59× |
| hashicorp/golang-lru | 90.0 | 96.3 | 0.00 | 0.00 | 1.08× |
| hashicorp/golang-lru-2Q | 93.9 | 89.1 | 0.00 | 0.00 | 1.00× |
| dgraph-io/ristretto | 5.05 | 5.70 | 0.00 | 0.00 | 0.06× |
| maypok86/otter | 3.32 | 3.68 | 0.00 | 0.00 | 0.04× |
| Yiling-J/theine | 4.01 | 4.78 | 0.00 | 0.00 | 0.05× |
| bluele/gcache-LRU | 196 | 218 | 1.00 | 1.00 | 2.44× |
| bluele/gcache-LFU | 253 | 321 | 1.00 | 1.00 | 3.59× |
| allegro/bigcache † | 11.4 | 12.1 | 2.00 | 1.00 | 0.13× |
| coocood/freecache † | 12.7 | 13.7 | 0.00 | 0.00 | 0.15× |
| patrickmn/go-cache ‡ | 62.4 | 68.2 | 0.00 | 0.00 | 0.76× |
| orcaman/concurrent-map ‡ | 8.31 | 9.01 | 0.00 | 0.00 | 0.10× |
| **jr_cache/ShardedMap** ‡ | 19.3 | 13.7 | 0.00 | 0.00 | 0.15× |

### Mixed 90 % Get / 10 % Set, Zipf keys, 24 goroutines

| Library | 10,000 ns/op | 100,000 ns/op | 10,000 allocs | 100,000 allocs | vs jr_cache LRU |
|---|---:|---:|---:|---:|---:|
| **jr_cache/CacheMap-LRU** | 104 | 115 | 0.00 | 0.00 | 1.00× |
| **jr_cache/CacheMap-LFU** | 152 | 166 | 0.00 | 0.00 | 1.45× |
| **jr_cache/Sharded-LRU** | 63.9 | 66.4 | 0.00 | 0.00 | 0.58× |
| **jr_cache/Sharded-LFU** | 77.2 | 72.4 | 0.00 | 0.00 | 0.63× |
| **jr_cache/Sharded-LRU-global** | 215 | 264 | 0.00 | 0.00 | 2.31× |
| hashicorp/golang-lru | 116 | 130 | 0.00 | 0.00 | 1.14× |
| hashicorp/golang-lru-2Q | 141 | 138 | 0.00 | 0.00 | 1.21× |
| dgraph-io/ristretto | 50.6 | 43.7 | 0.00 | 0.00 | 0.38× |
| maypok86/otter | 27.0 | 29.6 | 0.00 | 0.00 | 0.26× |
| Yiling-J/theine | 50.5 | 44.2 | 0.00 | 0.00 | 0.39× |
| bluele/gcache-LRU | 217 | 252 | 1.00 | 1.00 | 2.20× |
| bluele/gcache-LFU | 298 | 327 | 1.00 | 1.00 | 2.86× |
| allegro/bigcache † | 70.8 | 66.3 | 1.00 | 1.00 | 0.58× |
| coocood/freecache † | 33.2 | 27.9 | 0.00 | 0.00 | 0.24× |
| patrickmn/go-cache ‡ | 138 | 169 | 0.00 | 0.00 | 1.48× |
| orcaman/concurrent-map ‡ | 49.2 | 47.3 | 0.00 | 0.00 | 0.41× |
| **jr_cache/ShardedMap** ‡ | 65.5 | 62.5 | 0.00 | 0.00 | 0.55× |

## What the numbers say

**Single-threaded, `jr_cache` LRU is as fast as the fastest bounded
cache.** Its `Get` (30–32 ns), `Set` (30–33 ns) and mixed Zipf loop
(34–47 ns) are within a few nanoseconds of hashicorp/golang-lru, and ahead
of every other bounded library: ristretto, otter and theine pay 1.3–10×
for their buffers and admission filters when there is no contention to
amortise them against. Eviction (`SetEvict`, 61–84 ns) allocates nothing —
the evicted entry's node is reused for the new one — and is the cheapest
of the bounded group at 100,000 entries; at 10,000 only bigcache (70 ns)
is faster.

**Under contention the picture splits in two.** With 24 goroutines
reading, `jr_cache`'s sharded caches (34–43 ns) beat every *mutex-based*
cache — golang-lru (90–96 ns), gcache, go-cache — and the single locked
`CacheMap` (89–99 ns). But the caches that record accesses without a lock
are in another class: otter serves a parallel hit in 3.3–3.7 ns, theine in
4–5 ns, ristretto in 5–6 ns, concurrent-map in 8–9 ns and
bigcache/freecache in 11–14 ns. The reason is structural: every `jr_cache`
cache `Get` takes the shard's **write** lock so it can move the entry in
the LRU list or bump its frequency bucket. Sharding divides that contention
by 16; the lock-free designs remove it. On the mixed parallel workload the
gap narrows (otter 27–30 ns, ristretto/theine 44–51 ns, `jr_cache` sharded
LRU 64–66 ns, sharded LFU 72–77 ns) because writes force the other
libraries to touch their policies too.

**`jr_cache`'s LFU now costs about what its LRU costs.** Exact LFU moves
the entry to the next frequency bucket on every hit. The buckets are a
sorted linked list and every node points at its bucket, so that move is a
few pointer writes after a single hash lookup: 31–35 ns per `Get` with
string keys (LRU 30–32 ns), 70–85 ns on the mixed Zipf loop, and 34–36 ns
per parallel hit on the sharded cache. That is a hit ratio on par with
W-TinyLFU at LRU cost single-threaded — faster than otter (33–41 ns) and
theine (83–107 ns) on a `Get`, and level with otter on the mixed loop.
Under heavy read contention on one hot cache the lock-free designs are
still an order of magnitude ahead.

**Global order costs what it says.** `Sharded-LRU-global` is 1.2–2.8× the
per-shard variant on every row, and slower than the plain locked `CacheMap`
under contention (164–264 vs 89–115 ns), since each call takes the index
lock and a shard lock. It exists for exact eviction and O(1) `Top`/`Len`,
not for speed.

**As a plain concurrent map, `jr_cache/ShardedMap` is competitive.**
18–25 ns single-threaded reads and 14–19 ns parallel reads, against
concurrent-map's 18–24 / 8–9 ns — the difference is `maphash` on the key
versus concurrent-map's fnv32. Nothing here allocates.

**Allocations.** `jr_cache` allocates nothing on any hit, any overwrite,
or any insert that evicts; only an insert into a cache that still has room
allocates (one node). gcache allocates on every call (`interface{}`
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
