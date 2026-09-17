package jr_cache

import "sync"

// structs for the bucket map implementation

// Structure representing a single bucket within the bucket map.
// Each bucket has an index and an ordered map of key-value pairs, ordered by
// arrival: a key that moves into the bucket goes to its bottom, so the top of
// a bucket is the entry that has been in it the longest.
type bucket[Key comparable, Value any] struct {
	index uint64
	data  *OrderedMap[Key, Value]
}

// Structure representing the entire bucket map.
// Buckets live in an ordered map sorted by index (frequency) in ascending
// order: the top of the ordered map is the lowest index, the bottom is the
// highest. A bucket is removed as soon as it becomes empty, so every bucket
// in the ordered map holds at least one entry.
type BucketMap[Key comparable, Value any] struct {
	buckets *OrderedMap[uint64, *bucket[Key, Value]]
	mapping map[Key]uint64
	// pool holds warm, empty buckets ready to be linked in, so moving a key
	// to a new index does not have to allocate a bucket and its inner map.
	// Buckets that empty out go back here until the pool holds poolSize.
	pool     []*bucket[Key, Value]
	poolSize int

	locked bool
	lock   sync.RWMutex
}

// DefaultWarmBuckets is how many buckets NewBucketMap prepares up front.
const DefaultWarmBuckets = 16

// interface for interacting with the bucket map.

type BucketMapInterface[Key comparable, Value any] interface {
	CommonMapInterface[Key, Value]
	Set(index uint64, key Key, value Value)
	SetTop(key Key, value Value)
	SetBottom(key Key, value Value)
	PopAnyFromBucket(index uint64) (Value, bool)
	GetAnyFromBucket(index uint64) (Value, bool)
	GetBucketIndices() []uint64
	GetAllBuckets() []bucket[Key, Value]
	GetBucketCount() int
	GetBucketIndex(key Key) (uint64, bool)
	WarmUp(count int)
	WarmBuckets() int
}

// Compile-time check that BucketMap satisfies its interface.
var _ BucketMapInterface[int, int] = (*BucketMap[int, int])(nil)

// constructors for the bucket map.

func newBucket[Key comparable, Value any](index uint64) *bucket[Key, Value] {
	// The bucket map's lock covers its buckets, so they never lock on their own.
	return &bucket[Key, Value]{
		index: index,
		data:  NewOrderedMap[Key, Value](false),
	}
}

func NewBucketMap[Key comparable, Value any](locked bool) *BucketMap[Key, Value] {
	// The bucket map guards every operation with its own lock, so the inner
	// ordered map never needs to lock on its own.
	bm := &BucketMap[Key, Value]{
		buckets: NewOrderedMap[uint64, *bucket[Key, Value]](false),
		mapping: make(map[Key]uint64),
		locked:  locked,
		lock:    sync.RWMutex{},
	}
	bm.warm_up(DefaultWarmBuckets)
	return bm
}

// private methods for interacting with the bucket map.

func (bm *BucketMap[Key, Value]) has_bucket(index uint64) bool {
	return bm.buckets.Has(index)
}

func (bm *BucketMap[Key, Value]) bucket_count() int {
	return bm.buckets.Len()
}

// warm_up makes sure the pool can hold count buckets and is full.
func (bm *BucketMap[Key, Value]) warm_up(count int) {
	if count > bm.poolSize {
		bm.poolSize = count
	}
	for len(bm.pool) < count {
		bm.pool = append(bm.pool, newBucket[Key, Value](0))
	}
}

// acquire_bucket returns an empty bucket for index, from the pool if one is
// available.
func (bm *BucketMap[Key, Value]) acquire_bucket(index uint64) *bucket[Key, Value] {
	if n := len(bm.pool); n > 0 {
		b := bm.pool[n-1]
		bm.pool = bm.pool[:n-1]
		b.index = index
		return b
	}
	return newBucket[Key, Value](index)
}

// release_bucket returns an unlinked bucket to the pool if there is room.
func (bm *BucketMap[Key, Value]) release_bucket(b *bucket[Key, Value]) {
	if len(bm.pool) < bm.poolSize {
		b.data.Clear()
		bm.pool = append(bm.pool, b)
	}
}

// delete_bucket_if_empty drops the bucket from the ordered map once its last
// entry is gone, keeping the "no empty buckets" invariant.
func (bm *BucketMap[Key, Value]) delete_bucket_if_empty(b *bucket[Key, Value]) {
	if b.data.Len() == 0 {
		bm.buckets.Delete(b.index)
		bm.release_bucket(b)
	}
}

// insert_bucket creates a bucket for index and links it into the ordered map
// at its sorted position. hint, when hasHint is set, is the index of an
// existing bucket used as the starting point of the search; callers pass the
// key's previous bucket, which for frequency counting is usually adjacent.
func (bm *BucketMap[Key, Value]) insert_bucket(index uint64, hint uint64, hasHint bool) *bucket[Key, Value] {
	b := bm.acquire_bucket(index)

	// If index is smaller than the top index, it should be inserted at the top.
	topIndex, ok := bm.buckets.TopKey()
	if !ok || index < topIndex {
		bm.buckets.SetTop(index, b)
		return b
	}

	// If index is larger than the bottom index, it should be inserted at the bottom.
	bottomIndex, _ := bm.buckets.BottomKey()
	if index > bottomIndex {
		bm.buckets.SetBottom(index, b)
		return b
	}

	// Otherwise, we need to find the correct position for the new bucket.
	if hasHint && hint > index && bm.has_bucket(hint) {
		// Walk upwards from the hint until the previous bucket is smaller.
		cursor := hint
		for {
			prev, ok := bm.buckets.PrevKey(cursor)
			if !ok || prev < index {
				break
			}
			cursor = prev
		}
		bm.buckets.SetBefore(cursor, index, b)
		return b
	}

	// Walk downwards from the hint (or the top) until the next bucket is bigger.
	cursor := topIndex
	if hasHint && hint < index && bm.has_bucket(hint) {
		cursor = hint
	}
	for {
		next, ok := bm.buckets.NextKey(cursor)
		if !ok || next > index {
			break
		}
		cursor = next
	}
	bm.buckets.SetAfter(cursor, index, b)
	return b
}

func (bm *BucketMap[Key, Value]) get_all_buckets() []bucket[Key, Value] {
	buckets := make([]bucket[Key, Value], 0, bm.buckets.Len())
	for index, ok := bm.buckets.TopKey(); ok; index, ok = bm.buckets.NextKey(index) {
		b, _ := bm.buckets.Get(index)
		buckets = append(buckets, *b)
	}
	return buckets
}

func (bm *BucketMap[Key, Value]) get_bucket_indices() []uint64 {
	indices := make([]uint64, 0, bm.buckets.Len())
	for index, ok := bm.buckets.TopKey(); ok; index, ok = bm.buckets.NextKey(index) {
		indices = append(indices, index)
	}
	return indices
}

func (bm *BucketMap[Key, Value]) send_to_bucket(index uint64, key Key, value Value) {
	oldIndex, hadOld := bm.mapping[key]
	if hadOld && oldIndex == index {
		// Same bucket: update in place, keeping the key's position in it.
		b, _ := bm.buckets.Get(index)
		b.data.Update(key, value)
		return
	}

	b, exists := bm.buckets.Get(index)
	if !exists {
		// Insert the new bucket before removing the key from the old one so
		// the old bucket is still around to serve as a search hint.
		b = bm.insert_bucket(index, oldIndex, hadOld)
	}
	bm.mapping[key] = index

	// A key arriving in a bucket is its newest entry. When it comes from
	// another bucket its list node moves with it, so the move allocates
	// nothing.
	if old, ok := bm.buckets.Get(oldIndex); hadOld && ok {
		if node := old.data.detach(key); node != nil {
			node.Value = value
			b.data.attach_bottom(node)
			bm.delete_bucket_if_empty(old)
			return
		}
	}
	b.data.SetBottom(key, value)
}

// send_to_end places key in the top or bottom bucket, creating bucket 0 when
// the map is empty.
func (bm *BucketMap[Key, Value]) send_to_end(key Key, value Value, top bool) {
	var index uint64
	var exists bool
	if top {
		index, exists = bm.buckets.TopKey()
	} else {
		index, exists = bm.buckets.BottomKey()
	}
	if !exists {
		index = 0
	}
	bm.send_to_bucket(index, key, value)
}

func (bm *BucketMap[Key, Value]) get_entry(key Key) (Value, bool) {
	if index, exists := bm.mapping[key]; exists {
		if b, exists := bm.buckets.Get(index); exists {
			return b.data.Get(key)
		}
	}
	var zero Value
	return zero, false
}

func (bm *BucketMap[Key, Value]) pop_entry(key Key) (Value, bool) {
	if index, exists := bm.mapping[key]; exists {
		if b, exists := bm.buckets.Get(index); exists {
			value, ok := b.data.Pop(key)
			if ok {
				delete(bm.mapping, key)
				bm.delete_bucket_if_empty(b)
				return value, true
			}
		}
	}
	var zero Value
	return zero, false
}

// end_of_bucket returns the oldest (top) or newest (bottom) entry of b.
func end_of_bucket[Key comparable, Value any](b *bucket[Key, Value], ok bool, top bool) (Key, Value, bool) {
	if !ok {
		var zeroKey Key
		var zeroValue Value
		return zeroKey, zeroValue, false
	}
	if top {
		return b.data.Top()
	}
	return b.data.Bottom()
}

// pop_end_of_bucket removes and returns the oldest (top) or newest (bottom) entry of b.
func (bm *BucketMap[Key, Value]) pop_end_of_bucket(b *bucket[Key, Value], ok bool, top bool) (Key, Value, bool) {
	if !ok {
		var zeroKey Key
		var zeroValue Value
		return zeroKey, zeroValue, false
	}
	var key Key
	var value Value
	if top {
		key, value, ok = b.data.PopTop()
	} else {
		key, value, ok = b.data.PopBottom()
	}
	if ok {
		delete(bm.mapping, key)
		bm.delete_bucket_if_empty(b)
	}
	return key, value, ok
}

// public methods for interacting with the bucket map.

func (bm *BucketMap[Key, Value]) Has(key Key) bool {
	if bm.locked {
		bm.lock.RLock()
		defer bm.lock.RUnlock()
	}
	_, exists := bm.mapping[key]
	return exists
}

func (bm *BucketMap[Key, Value]) Len() int {
	if bm.locked {
		bm.lock.RLock()
		defer bm.lock.RUnlock()
	}
	return len(bm.mapping)
}

func (bm *BucketMap[Key, Value]) Get(key Key) (Value, bool) {
	if bm.locked {
		bm.lock.RLock()
		defer bm.lock.RUnlock()
	}
	return bm.get_entry(key)
}

// Update replaces the value of an existing key without changing its bucket.
// Returns false (and does nothing) if key is not in the map.
func (bm *BucketMap[Key, Value]) Update(key Key, value Value) bool {
	if bm.locked {
		bm.lock.Lock()
		defer bm.lock.Unlock()
	}
	index, exists := bm.mapping[key]
	if !exists {
		return false
	}
	bm.send_to_bucket(index, key, value)
	return true
}

func (bm *BucketMap[Key, Value]) Pop(key Key) (Value, bool) {
	if bm.locked {
		bm.lock.Lock()
		defer bm.lock.Unlock()
	}
	return bm.pop_entry(key)
}

func (bm *BucketMap[Key, Value]) Delete(key Key) {
	if bm.locked {
		bm.lock.Lock()
		defer bm.lock.Unlock()
	}
	bm.pop_entry(key)
}

// Set places key in the bucket at index, moving it out of its current
// bucket if it already lives in a different one.
func (bm *BucketMap[Key, Value]) Set(index uint64, key Key, value Value) {
	if bm.locked {
		bm.lock.Lock()
		defer bm.lock.Unlock()
	}
	bm.send_to_bucket(index, key, value)
}

// SetTop places key in the bucket with the lowest index (bucket 0 if empty).
func (bm *BucketMap[Key, Value]) SetTop(key Key, value Value) {
	if bm.locked {
		bm.lock.Lock()
		defer bm.lock.Unlock()
	}
	bm.send_to_end(key, value, true)
}

// SetBottom places key in the bucket with the highest index (bucket 0 if empty).
func (bm *BucketMap[Key, Value]) SetBottom(key Key, value Value) {
	if bm.locked {
		bm.lock.Lock()
		defer bm.lock.Unlock()
	}
	bm.send_to_end(key, value, false)
}

// Top returns the oldest entry of the bucket with the lowest index.
func (bm *BucketMap[Key, Value]) Top() (Key, Value, bool) {
	if bm.locked {
		bm.lock.RLock()
		defer bm.lock.RUnlock()
	}
	_, b, ok := bm.buckets.Top()
	return end_of_bucket(b, ok, true)
}

// Bottom returns the newest entry of the bucket with the highest index.
func (bm *BucketMap[Key, Value]) Bottom() (Key, Value, bool) {
	if bm.locked {
		bm.lock.RLock()
		defer bm.lock.RUnlock()
	}
	_, b, ok := bm.buckets.Bottom()
	return end_of_bucket(b, ok, false)
}

// TopKey returns the key of the oldest entry of the bucket with the lowest index.
func (bm *BucketMap[Key, Value]) TopKey() (Key, bool) {
	key, _, ok := bm.Top()
	return key, ok
}

// BottomKey returns the key of the newest entry of the bucket with the highest index.
func (bm *BucketMap[Key, Value]) BottomKey() (Key, bool) {
	key, _, ok := bm.Bottom()
	return key, ok
}

// PopTop removes and returns the oldest entry of the bucket with the lowest index.
func (bm *BucketMap[Key, Value]) PopTop() (Key, Value, bool) {
	if bm.locked {
		bm.lock.Lock()
		defer bm.lock.Unlock()
	}
	_, b, ok := bm.buckets.Top()
	return bm.pop_end_of_bucket(b, ok, true)
}

// PopBottom removes and returns the newest entry of the bucket with the highest index.
func (bm *BucketMap[Key, Value]) PopBottom() (Key, Value, bool) {
	if bm.locked {
		bm.lock.Lock()
		defer bm.lock.Unlock()
	}
	_, b, ok := bm.buckets.Bottom()
	return bm.pop_end_of_bucket(b, ok, false)
}

// GetAnyFromBucket returns the value of the oldest entry in the bucket at index.
func (bm *BucketMap[Key, Value]) GetAnyFromBucket(index uint64) (Value, bool) {
	if bm.locked {
		bm.lock.RLock()
		defer bm.lock.RUnlock()
	}
	b, ok := bm.buckets.Get(index)
	_, value, ok := end_of_bucket(b, ok, true)
	return value, ok
}

// PopAnyFromBucket removes and returns the value of the oldest entry in the bucket at index.
func (bm *BucketMap[Key, Value]) PopAnyFromBucket(index uint64) (Value, bool) {
	if bm.locked {
		bm.lock.Lock()
		defer bm.lock.Unlock()
	}
	b, ok := bm.buckets.Get(index)
	_, value, ok := bm.pop_end_of_bucket(b, ok, true)
	return value, ok
}

// GetBucketIndices returns the bucket indices in ascending order.
func (bm *BucketMap[Key, Value]) GetBucketIndices() []uint64 {
	if bm.locked {
		bm.lock.RLock()
		defer bm.lock.RUnlock()
	}
	return bm.get_bucket_indices()
}

// GetAllBuckets returns the buckets in ascending index order.
func (bm *BucketMap[Key, Value]) GetAllBuckets() []bucket[Key, Value] {
	if bm.locked {
		bm.lock.RLock()
		defer bm.lock.RUnlock()
	}
	return bm.get_all_buckets()
}

func (bm *BucketMap[Key, Value]) GetBucketCount() int {
	if bm.locked {
		bm.lock.RLock()
		defer bm.lock.RUnlock()
	}
	return bm.bucket_count()
}

// GetBucketIndex returns the index of the bucket that currently holds key.
func (bm *BucketMap[Key, Value]) GetBucketIndex(key Key) (uint64, bool) {
	if bm.locked {
		bm.lock.RLock()
		defer bm.lock.RUnlock()
	}
	index, exists := bm.mapping[key]
	return index, exists
}

func (bm *BucketMap[Key, Value]) Clear() {
	if bm.locked {
		bm.lock.Lock()
		defer bm.lock.Unlock()
	}
	// Keep as many of the buckets as the pool can hold; the rest go to the GC.
	for _, b, ok := bm.buckets.PopTop(); ok && len(bm.pool) < bm.poolSize; _, b, ok = bm.buckets.PopTop() {
		bm.release_bucket(b)
	}
	bm.buckets.Clear()
	bm.mapping = make(map[Key]uint64)
}

// WarmUp prepares count empty buckets ahead of time and lets the pool keep
// that many when buckets empty out. It only ever grows the pool.
func (bm *BucketMap[Key, Value]) WarmUp(count int) {
	if bm.locked {
		bm.lock.Lock()
		defer bm.lock.Unlock()
	}
	bm.warm_up(count)
}

// WarmBuckets returns how many empty buckets are currently ready for reuse.
func (bm *BucketMap[Key, Value]) WarmBuckets() int {
	if bm.locked {
		bm.lock.RLock()
		defer bm.lock.RUnlock()
	}
	return len(bm.pool)
}

// read-only accessors for buckets handed out by GetAllBuckets. Keys are
// returned oldest first.

func (b bucket[Key, Value]) Index() uint64 {
	return b.index
}

func (b bucket[Key, Value]) Len() int {
	return b.data.Len()
}

func (b bucket[Key, Value]) Keys() []Key {
	keys := make([]Key, 0, b.data.Len())
	for key, ok := b.data.TopKey(); ok; key, ok = b.data.NextKey(key) {
		keys = append(keys, key)
	}
	return keys
}
