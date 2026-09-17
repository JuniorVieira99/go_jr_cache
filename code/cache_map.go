package jr_cache

import (
	"errors"
	"sync"
	"time"
)

// Errors related to the cache map implementation.
var (
	ErrInvalidMaxCapacity    = errors.New("max capacity must be greater than 0")
	ErrInvalidEvictionPolicy = errors.New("unknown eviction policy")
)

// structs for the cache map implementation.

// CacheMap is a bounded key-value store that evicts entries according to an
// EvictionPolicy once it is full.
//
// Recency based policies (LRU, MRU, FIFO, LIFO) keep their entries in an
// ordered map; frequency based ones (LFU, MFU) keep them in a bucket map
// indexed by access count. In both cases the top is the "least" end (oldest
// or least used) and the bottom the "most" end (newest or most used), so
// Top and Bottom mean the same thing whatever the policy:
//
//	LRU / FIFO / LFU evict from the top
//	MRU / LIFO / MFU evict from the bottom
//
// Entries may carry a TTL. Expiry is lazy: an expired entry is removed the
// next time any operation touches it, and is never returned or reported as
// present. Expired entries that nobody touches keep occupying capacity until
// they are evicted by the policy or swept by DeleteExpired.
type CacheMap[Key comparable, Value any] struct {
	entries    CommonMapInterface[Key, Value]
	bucketMap  *BucketMap[Key, Value]
	orderedMap *OrderedMap[Key, Value]
	// expiry holds the deadline of every key that was given a TTL.
	expiry      map[Key]time.Time
	eviction    EvictionPolicy
	maxCapacity uint64
	// onEvict, when set, is told about every entry the cache removes on its
	// own: capacity evictions and expired entries it purges.
	onEvict func(key Key, value Value)

	locked bool
	lock   sync.RWMutex
}

type CacheMapConfig struct {
	Eviction    EvictionPolicy
	MaxCapacity uint64
	Locked      bool
}

// interface for interacting with the cache map.

type CacheMapInterface[Key comparable, Value any] interface {
	CommonMapInterface[Key, Value]
	Set(key Key, value Value)
	SetOrGet(key Key, value Value) (Value, bool)
	Populate(entries map[Key]Value)
	SetWithTTL(key Key, value Value, ttl time.Duration)
	GetTTL(key Key) (time.Duration, bool)
	UpdateTTL(key Key, ttl time.Duration) bool
	RemoveTTL(key Key) bool
	DeleteExpired() int
	EvictionPolicy() EvictionPolicy
	MaxCapacity() uint64
	SetMaxCapacity(maxCapacity uint64) error
	SetOnEvict(fn func(key Key, value Value))
}

// Compile-time check that CacheMap satisfies its interface.
var _ CacheMapInterface[int, int] = (*CacheMap[int, int])(nil)

// constructors for interacting with the cache map.

func NewCacheMap[Key comparable, Value any](config CacheMapConfig) (*CacheMap[Key, Value], error) {
	if config.MaxCapacity == 0 {
		return nil, ErrInvalidMaxCapacity
	}
	if !config.Eviction.IsValid() {
		return nil, ErrInvalidEvictionPolicy
	}

	cm := &CacheMap[Key, Value]{
		expiry:      make(map[Key]time.Time),
		eviction:    config.Eviction,
		maxCapacity: config.MaxCapacity,
		locked:      config.Locked,
		lock:        sync.RWMutex{},
	}
	// The cache guards every operation with its own lock, so the inner map
	// never needs to lock on its own.
	if cm.uses_buckets() {
		cm.bucketMap = NewBucketMap[Key, Value](false)
		cm.entries = cm.bucketMap
	} else {
		cm.orderedMap = NewOrderedMap[Key, Value](false)
		cm.entries = cm.orderedMap
	}
	return cm, nil
}

// private methods for interacting with the cache map.

// uses_buckets reports whether the policy orders entries by access count.
func (cm *CacheMap[Key, Value]) uses_buckets() bool {
	return cm.eviction == LFU || cm.eviction == MFU
}

// evicts_bottom reports whether the policy evicts from the "most" end.
func (cm *CacheMap[Key, Value]) evicts_bottom() bool {
	return cm.eviction == MRU || cm.eviction == LIFO || cm.eviction == MFU
}

// expired reports whether key has a TTL that has already passed.
func (cm *CacheMap[Key, Value]) expired(key Key) bool {
	deadline, ok := cm.expiry[key]
	return ok && !time.Now().Before(deadline)
}

// remove drops key together with its TTL and returns its value.
func (cm *CacheMap[Key, Value]) remove(key Key) (Value, bool) {
	delete(cm.expiry, key)
	return cm.entries.Pop(key)
}

// evicted reports an entry the cache removed on its own to the hook, if any.
func (cm *CacheMap[Key, Value]) evicted(key Key, value Value) {
	if cm.onEvict != nil {
		cm.onEvict(key, value)
	}
}

// purge_if_expired removes key if its TTL has passed and reports whether it did.
func (cm *CacheMap[Key, Value]) purge_if_expired(key Key) bool {
	if !cm.expired(key) {
		return false
	}
	value, ok := cm.remove(key)
	if ok {
		cm.evicted(key, value)
	}
	return true
}

// has reports whether key is cached and not expired, without removing it.
func (cm *CacheMap[Key, Value]) has(key Key) bool {
	return cm.entries.Has(key) && !cm.expired(key)
}

// touch records an access to an existing key.
func (cm *CacheMap[Key, Value]) touch(key Key, value Value) {
	switch cm.eviction {
	case LRU, MRU:
		cm.orderedMap.MoveToBottom(key)
	case LFU, MFU:
		index, _ := cm.bucketMap.GetBucketIndex(key)
		cm.bucketMap.Set(index+1, key, value)
	}
	// FIFO and LIFO only care about insertion order.
}

func (cm *CacheMap[Key, Value]) get(key Key) (Value, bool) {
	if cm.purge_if_expired(key) {
		var zero Value
		return zero, false
	}
	value, ok := cm.entries.Get(key)
	if ok {
		cm.touch(key, value)
	}
	return value, ok
}

// evict removes the entry the policy designates and reports whether one was removed.
func (cm *CacheMap[Key, Value]) evict() bool {
	var key Key
	var value Value
	var ok bool
	if cm.evicts_bottom() {
		key, value, ok = cm.entries.PopBottom()
	} else {
		key, value, ok = cm.entries.PopTop()
	}
	if ok {
		delete(cm.expiry, key)
		cm.evicted(key, value)
	}
	return ok
}

// make_room evicts until there is space for one more entry.
func (cm *CacheMap[Key, Value]) make_room() {
	for uint64(cm.entries.Len()) >= cm.maxCapacity {
		if !cm.evict() {
			return
		}
	}
}

// insert adds a key that is not yet in the cache, evicting first if needed.
func (cm *CacheMap[Key, Value]) insert(key Key, value Value) {
	cm.make_room()
	if cm.uses_buckets() {
		cm.bucketMap.Set(1, key, value)
	} else {
		cm.orderedMap.SetBottom(key, value)
	}
}

// set stores value for key. An expired key is replaced as if it were new.
func (cm *CacheMap[Key, Value]) set(key Key, value Value) {
	cm.purge_if_expired(key)
	if cm.entries.Has(key) {
		// Writing an existing key counts as an access.
		cm.entries.Update(key, value)
		cm.touch(key, value)
		return
	}
	cm.insert(key, value)
}

// set_ttl gives key a deadline ttl from now. A ttl <= 0 expires it immediately.
func (cm *CacheMap[Key, Value]) set_ttl(key Key, ttl time.Duration) {
	cm.expiry[key] = time.Now().Add(ttl)
}

// peek_end returns the live entry at the top or bottom, purging expired
// entries it runs into on the way.
func (cm *CacheMap[Key, Value]) peek_end(top bool) (Key, Value, bool) {
	for {
		var key Key
		var value Value
		var ok bool
		if top {
			key, value, ok = cm.entries.Top()
		} else {
			key, value, ok = cm.entries.Bottom()
		}
		if !ok || !cm.purge_if_expired(key) {
			return key, value, ok
		}
	}
}

// pop_end removes and returns the live entry at the top or bottom, discarding
// expired entries it runs into on the way.
func (cm *CacheMap[Key, Value]) pop_end(top bool) (Key, Value, bool) {
	for {
		var key Key
		var value Value
		var ok bool
		if top {
			key, value, ok = cm.entries.PopTop()
		} else {
			key, value, ok = cm.entries.PopBottom()
		}
		if !ok {
			return key, value, false
		}
		wasExpired := cm.expired(key)
		delete(cm.expiry, key)
		if !wasExpired {
			return key, value, true
		}
		cm.evicted(key, value)
	}
}

// public methods for interacting with the cache map.

func (cm *CacheMap[Key, Value]) EvictionPolicy() EvictionPolicy {
	return cm.eviction
}

func (cm *CacheMap[Key, Value]) MaxCapacity() uint64 {
	if cm.locked {
		cm.lock.RLock()
		defer cm.lock.RUnlock()
	}
	return cm.maxCapacity
}

// SetOnEvict installs a hook that is called for every entry the cache
// removes on its own — a capacity eviction or an expired entry being purged —
// but not for entries removed by Delete, Pop, PopTop, PopBottom or Clear.
// The hook runs while the cache's lock is held and must not call back into
// the cache. Pass nil to remove it.
func (cm *CacheMap[Key, Value]) SetOnEvict(fn func(key Key, value Value)) {
	if cm.locked {
		cm.lock.Lock()
		defer cm.lock.Unlock()
	}
	cm.onEvict = fn
}

// SetMaxCapacity changes the capacity, evicting entries if the cache is now
// over it.
func (cm *CacheMap[Key, Value]) SetMaxCapacity(maxCapacity uint64) error {
	if maxCapacity == 0 {
		return ErrInvalidMaxCapacity
	}
	if cm.locked {
		cm.lock.Lock()
		defer cm.lock.Unlock()
	}
	cm.maxCapacity = maxCapacity
	for uint64(cm.entries.Len()) > cm.maxCapacity {
		if !cm.evict() {
			break
		}
	}
	return nil
}

// Len returns the number of stored entries. It may include expired entries
// that have not been touched yet; call DeleteExpired first for an exact count.
func (cm *CacheMap[Key, Value]) Len() int {
	if cm.locked {
		cm.lock.RLock()
		defer cm.lock.RUnlock()
	}
	return cm.entries.Len()
}

// Has reports whether key is cached and has not expired.
func (cm *CacheMap[Key, Value]) Has(key Key) bool {
	if cm.locked {
		cm.lock.RLock()
		defer cm.lock.RUnlock()
	}
	return cm.has(key)
}

// Get returns the value for key and records the access.
func (cm *CacheMap[Key, Value]) Get(key Key) (Value, bool) {
	if cm.locked {
		cm.lock.Lock()
		defer cm.lock.Unlock()
	}
	return cm.get(key)
}

// Set stores value for key without a TTL, evicting an entry first if the
// cache is full. Any TTL the key had is removed.
func (cm *CacheMap[Key, Value]) Set(key Key, value Value) {
	if cm.locked {
		cm.lock.Lock()
		defer cm.lock.Unlock()
	}
	cm.set(key, value)
	delete(cm.expiry, key)
}

// Populate stores every entry without a TTL under a single lock acquisition.
// Entries beyond the capacity evict as Set would.
func (cm *CacheMap[Key, Value]) Populate(entries map[Key]Value) {
	if cm.locked {
		cm.lock.Lock()
		defer cm.lock.Unlock()
	}
	for key, value := range entries {
		cm.set(key, value)
		delete(cm.expiry, key)
	}
}

// SetWithTTL stores value for key and expires it ttl from now.
func (cm *CacheMap[Key, Value]) SetWithTTL(key Key, value Value, ttl time.Duration) {
	if cm.locked {
		cm.lock.Lock()
		defer cm.lock.Unlock()
	}
	cm.set(key, value)
	cm.set_ttl(key, ttl)
}

// GetTTL returns the time left before key expires. It reports false if key
// has no TTL, is not cached, or has already expired.
func (cm *CacheMap[Key, Value]) GetTTL(key Key) (time.Duration, bool) {
	if cm.locked {
		cm.lock.RLock()
		defer cm.lock.RUnlock()
	}
	deadline, ok := cm.expiry[key]
	if !ok {
		return 0, false
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return 0, false
	}
	return remaining, true
}

// UpdateTTL gives an existing key a new deadline ttl from now, whether or
// not it had one. Returns false if key is not cached or has expired.
func (cm *CacheMap[Key, Value]) UpdateTTL(key Key, ttl time.Duration) bool {
	if cm.locked {
		cm.lock.Lock()
		defer cm.lock.Unlock()
	}
	if cm.purge_if_expired(key) || !cm.entries.Has(key) {
		return false
	}
	cm.set_ttl(key, ttl)
	return true
}

// RemoveTTL makes key persist until evicted. Returns false if key is not
// cached or has expired.
func (cm *CacheMap[Key, Value]) RemoveTTL(key Key) bool {
	if cm.locked {
		cm.lock.Lock()
		defer cm.lock.Unlock()
	}
	if cm.purge_if_expired(key) || !cm.entries.Has(key) {
		return false
	}
	delete(cm.expiry, key)
	return true
}

// DeleteExpired removes every expired entry and returns how many it removed.
func (cm *CacheMap[Key, Value]) DeleteExpired() int {
	if cm.locked {
		cm.lock.Lock()
		defer cm.lock.Unlock()
	}
	now := time.Now()
	removed := 0
	for key, deadline := range cm.expiry {
		if !now.Before(deadline) {
			if value, ok := cm.remove(key); ok {
				cm.evicted(key, value)
			}
			removed++
		}
	}
	return removed
}

// SetOrGet returns the existing value and true if key is already cached,
// otherwise it stores value (without a TTL) and returns it with false.
func (cm *CacheMap[Key, Value]) SetOrGet(key Key, value Value) (Value, bool) {
	if cm.locked {
		cm.lock.Lock()
		defer cm.lock.Unlock()
	}
	if existing, ok := cm.get(key); ok {
		return existing, true
	}
	cm.insert(key, value)
	return value, false
}

// Update replaces the value of an existing key without recording an access
// and without changing its TTL. Returns false if key is not cached or has
// expired.
func (cm *CacheMap[Key, Value]) Update(key Key, value Value) bool {
	if cm.locked {
		cm.lock.Lock()
		defer cm.lock.Unlock()
	}
	if cm.purge_if_expired(key) {
		return false
	}
	return cm.entries.Update(key, value)
}

func (cm *CacheMap[Key, Value]) Pop(key Key) (Value, bool) {
	if cm.locked {
		cm.lock.Lock()
		defer cm.lock.Unlock()
	}
	if cm.purge_if_expired(key) {
		var zero Value
		return zero, false
	}
	return cm.remove(key)
}

func (cm *CacheMap[Key, Value]) Delete(key Key) {
	if cm.locked {
		cm.lock.Lock()
		defer cm.lock.Unlock()
	}
	cm.remove(key)
}

// Top returns the live entry at the "least" end without recording an access.
func (cm *CacheMap[Key, Value]) Top() (Key, Value, bool) {
	if cm.locked {
		cm.lock.Lock()
		defer cm.lock.Unlock()
	}
	return cm.peek_end(true)
}

// Bottom returns the live entry at the "most" end without recording an access.
func (cm *CacheMap[Key, Value]) Bottom() (Key, Value, bool) {
	if cm.locked {
		cm.lock.Lock()
		defer cm.lock.Unlock()
	}
	return cm.peek_end(false)
}

// TopKey returns the key of the live entry at the "least" end.
func (cm *CacheMap[Key, Value]) TopKey() (Key, bool) {
	if cm.locked {
		cm.lock.Lock()
		defer cm.lock.Unlock()
	}
	key, _, ok := cm.peek_end(true)
	return key, ok
}

// BottomKey returns the key of the live entry at the "most" end.
func (cm *CacheMap[Key, Value]) BottomKey() (Key, bool) {
	if cm.locked {
		cm.lock.Lock()
		defer cm.lock.Unlock()
	}
	key, _, ok := cm.peek_end(false)
	return key, ok
}

// PopTop removes and returns the live entry at the "least" end.
func (cm *CacheMap[Key, Value]) PopTop() (Key, Value, bool) {
	if cm.locked {
		cm.lock.Lock()
		defer cm.lock.Unlock()
	}
	return cm.pop_end(true)
}

// PopBottom removes and returns the live entry at the "most" end.
func (cm *CacheMap[Key, Value]) PopBottom() (Key, Value, bool) {
	if cm.locked {
		cm.lock.Lock()
		defer cm.lock.Unlock()
	}
	return cm.pop_end(false)
}

// Clear removes all entries from the cache.
func (cm *CacheMap[Key, Value]) Clear() {
	if cm.locked {
		cm.lock.Lock()
		defer cm.lock.Unlock()
	}
	cm.entries.Clear()
	cm.expiry = make(map[Key]time.Time)
}
