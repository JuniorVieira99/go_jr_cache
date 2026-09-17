package jr_cache

import (
	"errors"
	"hash/maphash"
	"sync"
	"time"
)

// Errors related to the sharded cache map implementation.
var (
	ErrInvalidShardCount = errors.New("shard count must be greater than 0 and not exceed max capacity")
)

// structs for the sharded cache map implementation.

// ShardedCacheMap splits a cache into shards selected by hashing the key, so
// goroutines working on different shards never contend for the same lock.
// It runs in one of two modes, chosen by ShardedCacheMapConfig.GlobalOrder.
//
// Per-shard order (the default): every shard is an independent CacheMap
// with its own slice of the capacity and its own eviction. Nothing is shared
// between shards, so it scales best, but eviction is local — an LRU cache
// evicts the least recently used entry of the shard the new key lands in,
// not of the whole cache — and Top, Bottom, PopTop and PopBottom act on the
// fullest shard.
//
// Global order: a single CacheMap holds the order, the capacity and the
// TTLs of every key, and the shards only hold the values. Eviction and
// Top/Bottom are then exact across the whole cache, at the cost of one
// shared lock that every operation takes briefly; value reads and writes
// still spread over the shard locks.
type ShardedCacheMap[Key comparable, Value any] struct {
	// Per-shard mode.
	shards []*CacheMap[Key, Value]
	// Global-order mode: order is the index of every key (with the policy,
	// capacity and TTLs applied), values holds the entries themselves.
	order  *CacheMap[Key, struct{}]
	values *ShardedMap[Key, Value]

	hash        func(Key) uint64
	eviction    EvictionPolicy
	maxCapacity uint64
	onEvict     func(key Key, value Value)

	locked bool
	lock   sync.RWMutex
}

type ShardedCacheMapConfig[Key comparable] struct {
	Eviction    EvictionPolicy
	MaxCapacity uint64
	ShardCount  uint64
	Locked      bool
	// GlobalOrder keeps one shared order across all shards, so eviction is
	// exact for the whole cache instead of per shard. See ShardedCacheMap.
	GlobalOrder bool
	// Hash picks the shard for a key. Leave nil to hash any comparable key
	// with hash/maphash.
	Hash func(Key) uint64
}

// interface for interacting with the sharded cache map.

type ShardedCacheMapInterface[Key comparable, Value any] interface {
	CacheMapInterface[Key, Value]
	PopulateThreaded(entries map[Key]Value, threads int)
	ShardCount() int
	ShardIndex(key Key) int
	GlobalOrder() bool
}

// Compile-time check that ShardedCacheMap satisfies its interface.
var _ ShardedCacheMapInterface[int, int] = (*ShardedCacheMap[int, int])(nil)

// constructors for interacting with the sharded cache map.

func NewShardedCacheMap[Key comparable, Value any](config ShardedCacheMapConfig[Key]) (*ShardedCacheMap[Key, Value], error) {
	if config.MaxCapacity == 0 {
		return nil, ErrInvalidMaxCapacity
	}
	if !config.Eviction.IsValid() {
		return nil, ErrInvalidEvictionPolicy
	}
	// With a global order the capacity is not split, so any shard count works.
	if config.ShardCount == 0 || (!config.GlobalOrder && config.ShardCount > config.MaxCapacity) {
		return nil, ErrInvalidShardCount
	}

	hash := config.Hash
	if hash == nil {
		hash = default_hash[Key]()
	}

	scm := &ShardedCacheMap[Key, Value]{
		hash:        hash,
		eviction:    config.Eviction,
		maxCapacity: config.MaxCapacity,
		locked:      config.Locked,
		lock:        sync.RWMutex{},
	}

	if config.GlobalOrder {
		order, err := NewCacheMap[Key, struct{}](CacheMapConfig{
			Eviction:    config.Eviction,
			MaxCapacity: config.MaxCapacity,
			Locked:      config.Locked,
		})
		if err != nil {
			return nil, err
		}
		values, err := NewShardedMap[Key, Value](ShardedMapConfig[Key]{
			ShardCount: config.ShardCount,
			Hash:       hash,
		})
		if err != nil {
			return nil, err
		}
		// Whatever the index evicts or expires, the value goes with it.
		order.SetOnEvict(func(key Key, _ struct{}) {
			value, ok := values.Pop(key)
			if ok && scm.onEvict != nil {
				scm.onEvict(key, value)
			}
		})
		scm.order = order
		scm.values = values
		return scm, nil
	}

	scm.shards = make([]*CacheMap[Key, Value], config.ShardCount)
	for i, capacity := range shard_capacities(config.MaxCapacity, config.ShardCount) {
		shard, err := NewCacheMap[Key, Value](CacheMapConfig{
			Eviction:    config.Eviction,
			MaxCapacity: capacity,
			Locked:      config.Locked,
		})
		if err != nil {
			return nil, err
		}
		scm.shards[i] = shard
	}
	return scm, nil
}

// default_hash hashes any comparable key with a seed that is fixed for the
// lifetime of the cache.
func default_hash[Key comparable]() func(Key) uint64 {
	seed := maphash.MakeSeed()
	return func(key Key) uint64 {
		return maphash.Comparable(seed, key)
	}
}

// shard_capacities splits total across count shards as evenly as possible;
// the first total%count shards get one extra slot so the sum is exactly total.
func shard_capacities(total uint64, count uint64) []uint64 {
	capacities := make([]uint64, count)
	base, extra := total/count, total%count
	for i := range capacities {
		capacities[i] = base
		if uint64(i) < extra {
			capacities[i]++
		}
	}
	return capacities
}

// private methods for interacting with the sharded cache map.

// global reports whether the cache runs with a shared order.
func (scm *ShardedCacheMap[Key, Value]) global() bool {
	return scm.order != nil
}

func (scm *ShardedCacheMap[Key, Value]) shard_index(key Key) int {
	if scm.global() {
		return scm.values.ShardIndex(key)
	}
	return int(scm.hash(key) % uint64(len(scm.shards)))
}

func (scm *ShardedCacheMap[Key, Value]) shard(key Key) *CacheMap[Key, Value] {
	return scm.shards[scm.shard_index(key)]
}

// fullest_shard returns the shard holding the most entries (lowest index on ties).
func (scm *ShardedCacheMap[Key, Value]) fullest_shard() *CacheMap[Key, Value] {
	fullest, fullestLen := scm.shards[0], scm.shards[0].Len()
	for _, shard := range scm.shards[1:] {
		if n := shard.Len(); n > fullestLen {
			fullest, fullestLen = shard, n
		}
	}
	return fullest
}

// value_of looks up the value for a key the index reported. A missing value
// means the key was removed between the two lookups; the index entry is
// dropped so it does not linger.
func (scm *ShardedCacheMap[Key, Value]) value_of(key Key, ok bool) (Value, bool) {
	if !ok {
		var zero Value
		return zero, false
	}
	value, ok := scm.values.Get(key)
	if !ok {
		scm.order.Delete(key)
	}
	return value, ok
}

// pop_value removes and returns the value for a key the index gave up. If
// the index did not have the key, any value left behind for it is dropped
// as well.
func (scm *ShardedCacheMap[Key, Value]) pop_value(key Key, ok bool) (Key, Value, bool) {
	if !ok {
		scm.values.Delete(key)
		var zero Value
		return key, zero, false
	}
	value, ok := scm.values.Pop(key)
	return key, value, ok
}

// public methods for interacting with the sharded cache map.

func (scm *ShardedCacheMap[Key, Value]) EvictionPolicy() EvictionPolicy {
	return scm.eviction
}

// GlobalOrder reports whether eviction is exact across the whole cache
// (true) or per shard (false).
func (scm *ShardedCacheMap[Key, Value]) GlobalOrder() bool {
	return scm.global()
}

func (scm *ShardedCacheMap[Key, Value]) ShardCount() int {
	if scm.global() {
		return scm.values.ShardCount()
	}
	return len(scm.shards)
}

// ShardIndex returns the index of the shard that holds (or would hold) key.
func (scm *ShardedCacheMap[Key, Value]) ShardIndex(key Key) int {
	return scm.shard_index(key)
}

// MaxCapacity returns the total capacity across all shards.
func (scm *ShardedCacheMap[Key, Value]) MaxCapacity() uint64 {
	if scm.locked {
		scm.lock.RLock()
		defer scm.lock.RUnlock()
	}
	return scm.maxCapacity
}

// SetMaxCapacity changes the total capacity, evicting whatever is now over
// it. In per-shard mode the capacity is redistributed across the shards and
// must be at least the shard count so every shard keeps one slot.
func (scm *ShardedCacheMap[Key, Value]) SetMaxCapacity(maxCapacity uint64) error {
	if maxCapacity == 0 {
		return ErrInvalidMaxCapacity
	}
	if !scm.global() && maxCapacity < uint64(len(scm.shards)) {
		return ErrInvalidShardCount
	}
	if scm.locked {
		scm.lock.Lock()
		defer scm.lock.Unlock()
	}
	scm.maxCapacity = maxCapacity
	if scm.global() {
		return scm.order.SetMaxCapacity(maxCapacity)
	}
	for i, capacity := range shard_capacities(maxCapacity, uint64(len(scm.shards))) {
		if err := scm.shards[i].SetMaxCapacity(capacity); err != nil {
			return err
		}
	}
	return nil
}

// SetOnEvict installs a hook called for every entry the cache evicts or
// expires on its own; see CacheMap.SetOnEvict for the exact contract.
func (scm *ShardedCacheMap[Key, Value]) SetOnEvict(fn func(key Key, value Value)) {
	if scm.locked {
		scm.lock.Lock()
		defer scm.lock.Unlock()
	}
	scm.onEvict = fn
	if !scm.global() {
		for _, shard := range scm.shards {
			shard.SetOnEvict(fn)
		}
	}
	// In global mode the index's own hook consults scm.onEvict.
}

// Len returns the number of entries across all shards.
func (scm *ShardedCacheMap[Key, Value]) Len() int {
	if scm.global() {
		return scm.order.Len()
	}
	total := 0
	for _, shard := range scm.shards {
		total += shard.Len()
	}
	return total
}

func (scm *ShardedCacheMap[Key, Value]) Has(key Key) bool {
	if scm.global() {
		return scm.order.Has(key)
	}
	return scm.shard(key).Has(key)
}

func (scm *ShardedCacheMap[Key, Value]) Get(key Key) (Value, bool) {
	if scm.global() {
		_, ok := scm.order.Get(key)
		return scm.value_of(key, ok)
	}
	return scm.shard(key).Get(key)
}

func (scm *ShardedCacheMap[Key, Value]) Set(key Key, value Value) {
	if scm.global() {
		// Value first, so a concurrent eviction of this key cannot leave a
		// value behind without an index entry.
		scm.values.Set(key, value)
		scm.order.Set(key, struct{}{})
		return
	}
	scm.shard(key).Set(key, value)
}

func (scm *ShardedCacheMap[Key, Value]) SetOrGet(key Key, value Value) (Value, bool) {
	if scm.global() {
		if existing, ok := scm.Get(key); ok {
			return existing, true
		}
		scm.Set(key, value)
		return value, false
	}
	return scm.shard(key).SetOrGet(key, value)
}

func (scm *ShardedCacheMap[Key, Value]) Update(key Key, value Value) bool {
	if scm.global() {
		if !scm.order.Update(key, struct{}{}) {
			return false
		}
		return scm.values.Update(key, value)
	}
	return scm.shard(key).Update(key, value)
}

// Populate stores every entry, filling each shard under one lock acquisition.
func (scm *ShardedCacheMap[Key, Value]) Populate(entries map[Key]Value) {
	scm.PopulateThreaded(entries, 1)
}

// PopulateThreaded stores every entry using up to threads goroutines.
// Entries are first partitioned by shard, then each shard is filled under a
// single lock acquisition by one worker.
func (scm *ShardedCacheMap[Key, Value]) PopulateThreaded(entries map[Key]Value, threads int) {
	if scm.global() {
		keys := make(map[Key]struct{}, len(entries))
		for key := range entries {
			keys[key] = struct{}{}
		}
		scm.values.Populate(entries, threads)
		scm.order.Populate(keys)
		return
	}
	parts := partition_by_shard(entries, len(scm.shards), scm.shard_index)
	run_per_shard(parts, threads, func(index int, part map[Key]Value) {
		scm.shards[index].Populate(part)
	})
}

func (scm *ShardedCacheMap[Key, Value]) SetWithTTL(key Key, value Value, ttl time.Duration) {
	if scm.global() {
		scm.values.Set(key, value)
		scm.order.SetWithTTL(key, struct{}{}, ttl)
		return
	}
	scm.shard(key).SetWithTTL(key, value, ttl)
}

func (scm *ShardedCacheMap[Key, Value]) GetTTL(key Key) (time.Duration, bool) {
	if scm.global() {
		return scm.order.GetTTL(key)
	}
	return scm.shard(key).GetTTL(key)
}

func (scm *ShardedCacheMap[Key, Value]) UpdateTTL(key Key, ttl time.Duration) bool {
	if scm.global() {
		return scm.order.UpdateTTL(key, ttl)
	}
	return scm.shard(key).UpdateTTL(key, ttl)
}

func (scm *ShardedCacheMap[Key, Value]) RemoveTTL(key Key) bool {
	if scm.global() {
		return scm.order.RemoveTTL(key)
	}
	return scm.shard(key).RemoveTTL(key)
}

// DeleteExpired sweeps every shard and returns the total number of entries removed.
func (scm *ShardedCacheMap[Key, Value]) DeleteExpired() int {
	if scm.global() {
		return scm.order.DeleteExpired()
	}
	removed := 0
	for _, shard := range scm.shards {
		removed += shard.DeleteExpired()
	}
	return removed
}

func (scm *ShardedCacheMap[Key, Value]) Pop(key Key) (Value, bool) {
	if scm.global() {
		_, ok := scm.order.Pop(key)
		_, value, ok := scm.pop_value(key, ok)
		return value, ok
	}
	return scm.shard(key).Pop(key)
}

func (scm *ShardedCacheMap[Key, Value]) Delete(key Key) {
	if scm.global() {
		scm.order.Delete(key)
		scm.values.Delete(key)
		return
	}
	scm.shard(key).Delete(key)
}

// Top returns the entry at the "least" end: of the whole cache with a
// global order, of the fullest shard otherwise.
func (scm *ShardedCacheMap[Key, Value]) Top() (Key, Value, bool) {
	if scm.global() {
		key, _, ok := scm.order.Top()
		value, ok := scm.value_of(key, ok)
		return key, value, ok
	}
	return scm.fullest_shard().Top()
}

// Bottom returns the entry at the "most" end: of the whole cache with a
// global order, of the fullest shard otherwise.
func (scm *ShardedCacheMap[Key, Value]) Bottom() (Key, Value, bool) {
	if scm.global() {
		key, _, ok := scm.order.Bottom()
		value, ok := scm.value_of(key, ok)
		return key, value, ok
	}
	return scm.fullest_shard().Bottom()
}

func (scm *ShardedCacheMap[Key, Value]) TopKey() (Key, bool) {
	if scm.global() {
		return scm.order.TopKey()
	}
	return scm.fullest_shard().TopKey()
}

func (scm *ShardedCacheMap[Key, Value]) BottomKey() (Key, bool) {
	if scm.global() {
		return scm.order.BottomKey()
	}
	return scm.fullest_shard().BottomKey()
}

// PopTop removes and returns the entry at the "least" end (see Top).
func (scm *ShardedCacheMap[Key, Value]) PopTop() (Key, Value, bool) {
	if scm.global() {
		key, _, ok := scm.order.PopTop()
		return scm.pop_value(key, ok)
	}
	return scm.fullest_shard().PopTop()
}

// PopBottom removes and returns the entry at the "most" end (see Bottom).
func (scm *ShardedCacheMap[Key, Value]) PopBottom() (Key, Value, bool) {
	if scm.global() {
		key, _, ok := scm.order.PopBottom()
		return scm.pop_value(key, ok)
	}
	return scm.fullest_shard().PopBottom()
}

func (scm *ShardedCacheMap[Key, Value]) Clear() {
	if scm.global() {
		scm.order.Clear()
		scm.values.Clear()
		return
	}
	for _, shard := range scm.shards {
		shard.Clear()
	}
}
