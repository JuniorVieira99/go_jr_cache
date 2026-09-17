package jr_cache

import "sync"

// Structs for the sharded map implementation

// shard is one partition of a ShardedMap: a plain map behind its own lock.
type shard[Key comparable, Value any] struct {
	lock sync.RWMutex
	data map[Key]Value
}

// ShardedMap is a concurrent map split into independent shards selected by
// hashing the key, so goroutines working on different shards never contend
// for the same lock. It keeps no order and never evicts.
type ShardedMap[Key comparable, Value any] struct {
	shards []*shard[Key, Value]
	hash   func(key Key) uint64
}

type ShardedMapConfig[Key comparable] struct {
	ShardCount uint64
	// Hash picks the shard for a key. Leave nil to hash any comparable key
	// with hash/maphash.
	Hash func(Key) uint64
}

// Interface for the sharded map implementation

// ShardedMapInterface is the unordered subset of CommonMapInterface: the
// same names and signatures, minus the methods that need a top and a bottom.
type ShardedMapInterface[Key comparable, Value any] interface {
	Get(key Key) (Value, bool)
	Set(key Key, value Value)
	SetOrGet(key Key, value Value) (Value, bool)
	Has(key Key) bool
	Update(key Key, value Value) bool
	Modify(key Key, fn func(value Value, exists bool) (Value, bool)) (Value, bool)
	Pop(key Key) (Value, bool)
	Delete(key Key)
	Len() int
	Clear()
	Range(fn func(key Key, value Value) bool)
	Populate(entries map[Key]Value, threads int)
	ShardCount() int
	ShardIndex(key Key) int
}

// Compile-time check that ShardedMap satisfies its interface.
var _ ShardedMapInterface[int, int] = (*ShardedMap[int, int])(nil)

// Constructors for a new shard and the sharded map implementation

func newShard[Key comparable, Value any]() *shard[Key, Value] {
	return &shard[Key, Value]{
		data: make(map[Key]Value),
	}
}

func NewShardMapConfig[Key comparable](shards uint64, hash func(Key) uint64) ShardedMapConfig[Key] {
	return ShardedMapConfig[Key]{
		ShardCount: shards,
		Hash:       hash,
	}
}

func NewShardedMapDefault[Key comparable, Value any](shards uint64) (*ShardedMap[Key, Value], error) {
	return NewShardedMap[Key, Value](ShardedMapConfig[Key]{
		ShardCount: shards,
		Hash:       default_hash[Key](),
	})
}

func NewShardedMap[Key comparable, Value any](config ShardedMapConfig[Key]) (*ShardedMap[Key, Value], error) {
	if config.ShardCount == 0 {
		return nil, ErrInvalidShardCount
	}
	hash := config.Hash
	if hash == nil {
		hash = default_hash[Key]()
	}
	sm := &ShardedMap[Key, Value]{
		shards: make([]*shard[Key, Value], config.ShardCount),
		hash:   hash,
	}
	for i := range sm.shards {
		sm.shards[i] = newShard[Key, Value]()
	}
	return sm, nil
}

// Private methods for the sharded map implementation

func (sm *ShardedMap[Key, Value]) shard_index(key Key) int {
	return int(sm.hash(key) % uint64(len(sm.shards)))
}

func (sm *ShardedMap[Key, Value]) shard(key Key) *shard[Key, Value] {
	return sm.shards[sm.shard_index(key)]
}

// Shard methods; each takes its own lock.

func (s *shard[Key, Value]) set(key Key, value Value) {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.data[key] = value
}

// set_all stores every entry of part under one lock acquisition.
func (s *shard[Key, Value]) set_all(part map[Key]Value) {
	s.lock.Lock()
	defer s.lock.Unlock()
	for key, value := range part {
		s.data[key] = value
	}
}

func (s *shard[Key, Value]) set_or_get(key Key, value Value) (Value, bool) {
	s.lock.Lock()
	defer s.lock.Unlock()
	if existing, ok := s.data[key]; ok {
		return existing, true
	}
	s.data[key] = value
	return value, false
}

func (s *shard[Key, Value]) get(key Key) (Value, bool) {
	s.lock.RLock()
	defer s.lock.RUnlock()
	value, ok := s.data[key]
	return value, ok
}

func (s *shard[Key, Value]) has(key Key) bool {
	s.lock.RLock()
	defer s.lock.RUnlock()
	_, ok := s.data[key]
	return ok
}

func (s *shard[Key, Value]) update(key Key, value Value) bool {
	s.lock.Lock()
	defer s.lock.Unlock()
	if _, ok := s.data[key]; !ok {
		return false
	}
	s.data[key] = value
	return true
}

func (s *shard[Key, Value]) pop(key Key) (Value, bool) {
	s.lock.Lock()
	defer s.lock.Unlock()
	value, ok := s.data[key]
	if ok {
		delete(s.data, key)
	}
	return value, ok
}

// modify runs fn on key under the write lock; see ShardedMap.Modify.
func (s *shard[Key, Value]) modify(key Key, fn func(value Value, exists bool) (Value, bool)) (Value, bool) {
	s.lock.Lock()
	defer s.lock.Unlock()
	current, exists := s.data[key]
	value, keep := fn(current, exists)
	if keep {
		s.data[key] = value
		return value, true
	}
	if exists {
		delete(s.data, key)
	}
	var zero Value
	return zero, false
}

func (s *shard[Key, Value]) len() int {
	s.lock.RLock()
	defer s.lock.RUnlock()
	return len(s.data)
}

func (s *shard[Key, Value]) clear() {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.data = make(map[Key]Value)
}

// snapshot copies the shard's entries so callers can iterate them without
// holding the shard's lock.
func (s *shard[Key, Value]) snapshot() map[Key]Value {
	s.lock.RLock()
	defer s.lock.RUnlock()
	copied := make(map[Key]Value, len(s.data))
	for key, value := range s.data {
		copied[key] = value
	}
	return copied
}

// Public methods for the sharded map implementation

func (sm *ShardedMap[Key, Value]) ShardCount() int {
	return len(sm.shards)
}

// ShardIndex returns the index of the shard that holds (or would hold) key.
func (sm *ShardedMap[Key, Value]) ShardIndex(key Key) int {
	return sm.shard_index(key)
}

func (sm *ShardedMap[Key, Value]) Set(key Key, value Value) {
	sm.shard(key).set(key, value)
}

// SetOrGet returns the existing value and true if key is present, otherwise
// it stores value and returns it with false.
func (sm *ShardedMap[Key, Value]) SetOrGet(key Key, value Value) (Value, bool) {
	return sm.shard(key).set_or_get(key, value)
}

func (sm *ShardedMap[Key, Value]) Get(key Key) (Value, bool) {
	return sm.shard(key).get(key)
}

func (sm *ShardedMap[Key, Value]) Has(key Key) bool {
	return sm.shard(key).has(key)
}

// Update replaces the value of an existing key. Returns false (and does
// nothing) if key is not in the map.
func (sm *ShardedMap[Key, Value]) Update(key Key, value Value) bool {
	return sm.shard(key).update(key, value)
}

// Modify atomically reads, changes and writes back key. fn receives the
// current value and whether key exists, and returns the value to store and
// whether to keep it; returning false removes key (if it existed). Modify
// returns the stored value and whether key is present afterwards. fn runs
// under the shard's write lock and must not call back into the map.
func (sm *ShardedMap[Key, Value]) Modify(key Key, fn func(value Value, exists bool) (Value, bool)) (Value, bool) {
	return sm.shard(key).modify(key, fn)
}

func (sm *ShardedMap[Key, Value]) Pop(key Key) (Value, bool) {
	return sm.shard(key).pop(key)
}

func (sm *ShardedMap[Key, Value]) Delete(key Key) {
	sm.shard(key).pop(key)
}

// Len returns the number of entries across all shards.
func (sm *ShardedMap[Key, Value]) Len() int {
	total := 0
	for _, s := range sm.shards {
		total += s.len()
	}
	return total
}

func (sm *ShardedMap[Key, Value]) Clear() {
	for _, s := range sm.shards {
		s.clear()
	}
}

// Range calls fn for every entry, shard by shard, until fn returns false.
// Each shard is snapshotted before its entries are visited, so fn may safely
// call back into the map and entries changed concurrently may or may not be
// seen.
func (sm *ShardedMap[Key, Value]) Range(fn func(key Key, value Value) bool) {
	for _, s := range sm.shards {
		for key, value := range s.snapshot() {
			if !fn(key, value) {
				return
			}
		}
	}
}

// Populate Methods

// Populate stores every entry, using up to threads goroutines. Entries are
// first partitioned by shard and each shard is then filled under a single
// lock acquisition, so the cost is one hash per entry plus one lock per
// shard rather than one lock per entry.
func (sm *ShardedMap[Key, Value]) Populate(entries map[Key]Value, threads int) {
	parts := partition_by_shard(entries, len(sm.shards), sm.shard_index)
	run_per_shard(parts, threads, func(index int, part map[Key]Value) {
		sm.shards[index].set_all(part)
	})
}
