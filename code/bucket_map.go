package jr_cache

import (
	"os"
	"sync"
)

// structs for the bucket map implementation

// bucketNode is one entry of a bucket map. It sits in the list of the bucket
// that holds it and points back at that bucket, so moving it to a
// neighbouring bucket needs no lookup at all.
type bucketNode[Key comparable, Value any] struct {
	key    Key
	value  Value
	bucket *bucket[Key, Value]
	prev   *bucketNode[Key, Value]
	next   *bucketNode[Key, Value]
}

// Structure representing a single bucket within the bucket map.
// Each bucket has an index and a list of entries ordered by arrival: a key
// that moves into the bucket goes to its bottom, so the top of a bucket is
// the entry that has been in it the longest. Buckets are themselves linked
// to their neighbours in ascending index order.
type bucket[Key comparable, Value any] struct {
	index uint64
	size  int
	head  *bucketNode[Key, Value]
	tail  *bucketNode[Key, Value]
	prev  *bucket[Key, Value]
	next  *bucket[Key, Value]
}

// Structure representing the entire bucket map.
// Every key maps straight to its node and every node knows its bucket, so
// reading a key is one map lookup, and moving it to the next bucket up — what
// an LFU hit does — needs no further lookup unless a bucket has to be created
// or dropped. Buckets form a list sorted by index (frequency) in ascending
// order: the head is the lowest index, the tail the highest. A bucket is
// removed as soon as it becomes empty, so every bucket in the list holds at
// least one entry.
type BucketMap[Key comparable, Value any] struct {
	entries map[Key]*bucketNode[Key, Value]
	// buckets indexes the linked buckets by index, for the operations that
	// name an arbitrary bucket. Moving between neighbours never consults it.
	buckets map[uint64]*bucket[Key, Value]
	head    *bucket[Key, Value]
	tail    *bucket[Key, Value]
	// pool holds warm, empty buckets ready to be linked in, so moving a key
	// to a new index does not have to allocate a bucket.
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
	return &bucket[Key, Value]{index: index}
}

func NewBucketMap[Key comparable, Value any](locked bool) *BucketMap[Key, Value] {
	bm := &BucketMap[Key, Value]{
		entries: make(map[Key]*bucketNode[Key, Value]),
		buckets: make(map[uint64]*bucket[Key, Value]),
		locked:  locked,
		lock:    sync.RWMutex{},
	}
	bm.warm_up(DefaultWarmBuckets)
	return bm
}

// private methods for a single bucket.

// push links n at the bottom of b.
func (b *bucket[Key, Value]) push(n *bucketNode[Key, Value]) {
	n.bucket = b
	n.prev = b.tail
	n.next = nil
	if b.tail != nil {
		b.tail.next = n
	} else {
		b.head = n
	}
	b.tail = n
	b.size++
}

// remove unlinks n from b. The node keeps its key and value.
func (b *bucket[Key, Value]) remove(n *bucketNode[Key, Value]) {
	if n.prev != nil {
		n.prev.next = n.next
	} else {
		b.head = n.next
	}
	if n.next != nil {
		n.next.prev = n.prev
	} else {
		b.tail = n.prev
	}
	n.prev, n.next, n.bucket = nil, nil, nil
	b.size--
}

// entry returns the key and value held by n, or zero values for nil.
func (n *bucketNode[Key, Value]) entry() (Key, Value, bool) {
	if n == nil {
		var zeroKey Key
		var zeroValue Value
		return zeroKey, zeroValue, false
	}
	return n.key, n.value, true
}

// private methods for interacting with the bucket map.

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
		*b = bucket[Key, Value]{}
		bm.pool = append(bm.pool, b)
	}
}

// link_bucket_after links b into the bucket list right after prev (at the
// head when prev is nil) and indexes it.
func (bm *BucketMap[Key, Value]) link_bucket_after(prev *bucket[Key, Value], b *bucket[Key, Value]) {
	b.prev = prev
	if prev != nil {
		b.next = prev.next
		prev.next = b
	} else {
		b.next = bm.head
		bm.head = b
	}
	if b.next != nil {
		b.next.prev = b
	} else {
		bm.tail = b
	}
	bm.buckets[b.index] = b
}

// drop_bucket removes an empty bucket from the list and the index, keeping
// the "no empty buckets" invariant, and returns it to the pool.
func (bm *BucketMap[Key, Value]) drop_bucket(b *bucket[Key, Value]) {
	if b.prev != nil {
		b.prev.next = b.next
	} else {
		bm.head = b.next
	}
	if b.next != nil {
		b.next.prev = b.prev
	} else {
		bm.tail = b.prev
	}
	delete(bm.buckets, b.index)
	bm.release_bucket(b)
}

// insert_bucket creates a bucket for index, which must not have one yet, and
// links it at its sorted position. near, when not nil, is a linked bucket the
// search starts from; callers pass the key's current bucket, which for
// frequency counting is usually adjacent.
func (bm *BucketMap[Key, Value]) insert_bucket(index uint64, near *bucket[Key, Value]) *bucket[Key, Value] {
	var prev *bucket[Key, Value]
	switch {
	case bm.head == nil || index < bm.head.index:
		// New lowest bucket: prev stays nil.
	case index > bm.tail.index:
		prev = bm.tail
	default:
		// head.index < index < tail.index, so both walks stop before
		// running off either end of the list.
		prev = near
		if prev == nil {
			prev = bm.head
		}
		for prev.index > index {
			prev = prev.prev
		}
		for prev.next.index < index {
			prev = prev.next
		}
	}
	b := bm.acquire_bucket(index)
	bm.link_bucket_after(prev, b)
	return b
}

// bucket_for returns the bucket at index, creating it if needed. near is a
// linked bucket (or nil) checked, with its neighbours, before the index.
func (bm *BucketMap[Key, Value]) bucket_for(index uint64, near *bucket[Key, Value]) *bucket[Key, Value] {
	if near != nil {
		if near.index == index {
			return near
		}
		if near.next != nil && near.next.index == index {
			return near.next
		}
		if near.prev != nil && near.prev.index == index {
			return near.prev
		}
	}
	if b, exists := bm.buckets[index]; exists {
		return b
	}
	return bm.insert_bucket(index, near)
}

// link indexes a detached node under its key and places it at the bottom of
// the bucket at index. The key must not already be present.
func (bm *BucketMap[Key, Value]) link(n *bucketNode[Key, Value], index uint64) {
	bm.bucket_for(index, bm.head).push(n)
	bm.entries[n.key] = n
}

// unlink removes n from its bucket and from the index, dropping the bucket if
// that emptied it. The node keeps its key and value, so it can be reused.
func (bm *BucketMap[Key, Value]) unlink(n *bucketNode[Key, Value]) {
	b := n.bucket
	b.remove(n)
	delete(bm.entries, n.key)
	if b.size == 0 {
		bm.drop_bucket(b)
	}
}

// move places n at the bottom of the bucket at index. A node already in that
// bucket keeps its position.
func (bm *BucketMap[Key, Value]) move(n *bucketNode[Key, Value], index uint64) {
	old := n.bucket
	if old.index == index {
		return
	}
	// The only entry of a bucket can take the bucket along when no other
	// bucket lies between the two indices: renumbering keeps the list sorted
	// and saves dropping one bucket and linking another.
	if old.size == 1 && (old.prev == nil || old.prev.index < index) && (old.next == nil || old.next.index > index) {
		delete(bm.buckets, old.index)
		old.index = index
		bm.buckets[index] = old
		return
	}
	// Find the target before leaving the old bucket, so the old bucket is
	// still linked to serve as the starting point of the search.
	target := bm.bucket_for(index, old)
	old.remove(n)
	if old.size == 0 {
		bm.drop_bucket(old)
	}
	target.push(n)
}

// bump records an access to n: it moves to the next bucket up.
func (bm *BucketMap[Key, Value]) bump(n *bucketNode[Key, Value]) {
	bm.move(n, n.bucket.index+1)
}

// end returns the oldest node of the lowest bucket (top) or the newest node
// of the highest bucket (bottom), or nil when the map is empty.
func (bm *BucketMap[Key, Value]) end(top bool) *bucketNode[Key, Value] {
	if top {
		if bm.head == nil {
			return nil
		}
		return bm.head.head
	}
	if bm.tail == nil {
		return nil
	}
	return bm.tail.tail
}

// clear is Clear without the lock, for callers that already hold it.
func (bm *BucketMap[Key, Value]) clear() {
	for b := bm.head; b != nil && len(bm.pool) < bm.poolSize; {
		next := b.next
		bm.release_bucket(b)
		b = next
	}
	bm.head, bm.tail = nil, nil
	bm.entries = make(map[Key]*bucketNode[Key, Value])
	clear(bm.buckets)
}

func (bm *BucketMap[Key, Value]) get_all_buckets() []bucket[Key, Value] {
	buckets := make([]bucket[Key, Value], 0, len(bm.buckets))
	for b := bm.head; b != nil; b = b.next {
		buckets = append(buckets, *b)
	}
	return buckets
}

func (bm *BucketMap[Key, Value]) get_bucket_indices() []uint64 {
	indices := make([]uint64, 0, len(bm.buckets))
	for b := bm.head; b != nil; b = b.next {
		indices = append(indices, b.index)
	}
	return indices
}

// send_to_bucket stores value for key in the bucket at index. A key already
// in that bucket keeps its position; one coming from another bucket, or a new
// one, becomes the newest entry of the bucket.
func (bm *BucketMap[Key, Value]) send_to_bucket(index uint64, key Key, value Value) {
	if n, exists := bm.entries[key]; exists {
		n.value = value
		bm.move(n, index)
		return
	}
	bm.link(&bucketNode[Key, Value]{key: key, value: value}, index)
}

// send_to_end places key in the top or bottom bucket, creating bucket 0 when
// the map is empty.
func (bm *BucketMap[Key, Value]) send_to_end(key Key, value Value, top bool) {
	var index uint64
	if top && bm.head != nil {
		index = bm.head.index
	} else if !top && bm.tail != nil {
		index = bm.tail.index
	}
	bm.send_to_bucket(index, key, value)
}

func (bm *BucketMap[Key, Value]) get_entry(key Key) (Value, bool) {
	if n, exists := bm.entries[key]; exists {
		return n.value, true
	}
	var zero Value
	return zero, false
}

func (bm *BucketMap[Key, Value]) pop_entry(key Key) (Value, bool) {
	if n, exists := bm.entries[key]; exists {
		bm.unlink(n)
		return n.value, true
	}
	var zero Value
	return zero, false
}

// pop_node removes n (which may be nil) and returns its entry.
func (bm *BucketMap[Key, Value]) pop_node(n *bucketNode[Key, Value]) (Key, Value, bool) {
	if n != nil {
		bm.unlink(n)
	}
	return n.entry()
}

// public methods for interacting with the bucket map.

func (bm *BucketMap[Key, Value]) Has(key Key) bool {
	if bm.locked {
		bm.lock.RLock()
		defer bm.lock.RUnlock()
	}
	_, exists := bm.entries[key]
	return exists
}

func (bm *BucketMap[Key, Value]) Len() int {
	if bm.locked {
		bm.lock.RLock()
		defer bm.lock.RUnlock()
	}
	return len(bm.entries)
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
	n, exists := bm.entries[key]
	if !exists {
		return false
	}
	n.value = value
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
	return bm.end(true).entry()
}

// Bottom returns the newest entry of the bucket with the highest index.
func (bm *BucketMap[Key, Value]) Bottom() (Key, Value, bool) {
	if bm.locked {
		bm.lock.RLock()
		defer bm.lock.RUnlock()
	}
	return bm.end(false).entry()
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
	return bm.pop_node(bm.end(true))
}

// PopBottom removes and returns the newest entry of the bucket with the highest index.
func (bm *BucketMap[Key, Value]) PopBottom() (Key, Value, bool) {
	if bm.locked {
		bm.lock.Lock()
		defer bm.lock.Unlock()
	}
	return bm.pop_node(bm.end(false))
}

// GetAnyFromBucket returns the value of the oldest entry in the bucket at index.
func (bm *BucketMap[Key, Value]) GetAnyFromBucket(index uint64) (Value, bool) {
	if bm.locked {
		bm.lock.RLock()
		defer bm.lock.RUnlock()
	}
	if b, exists := bm.buckets[index]; exists {
		return b.head.value, true
	}
	var zero Value
	return zero, false
}

// PopAnyFromBucket removes and returns the value of the oldest entry in the bucket at index.
func (bm *BucketMap[Key, Value]) PopAnyFromBucket(index uint64) (Value, bool) {
	if bm.locked {
		bm.lock.Lock()
		defer bm.lock.Unlock()
	}
	if b, exists := bm.buckets[index]; exists {
		_, value, ok := bm.pop_node(b.head)
		return value, ok
	}
	var zero Value
	return zero, false
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
	return len(bm.buckets)
}

// GetBucketIndex returns the index of the bucket that currently holds key.
func (bm *BucketMap[Key, Value]) GetBucketIndex(key Key) (uint64, bool) {
	if bm.locked {
		bm.lock.RLock()
		defer bm.lock.RUnlock()
	}
	if n, exists := bm.entries[key]; exists {
		return n.bucket.index, true
	}
	return 0, false
}

func (bm *BucketMap[Key, Value]) Clear() {
	if bm.locked {
		bm.lock.Lock()
		defer bm.lock.Unlock()
	}
	// Keep as many of the buckets as the pool can hold; the rest go to the GC.
	bm.clear()
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
	return b.size
}

func (b bucket[Key, Value]) Keys() []Key {
	keys := make([]Key, 0, b.size)
	for n := b.head; n != nil; n = n.next {
		keys = append(keys, n.key)
	}
	return keys
}

// Serialization for the bucket map implementation

// BucketMapSnapshot is a serializable picture of a bucket map. Entries are
// listed bucket by bucket in ascending index order and, within a bucket, in
// arrival order, so restoring one reproduces both orders exactly. Each
// entry's Frequency is the index of the bucket it belongs to.
type BucketMapSnapshot[Key comparable, Value any] struct {
	Entries []CacheEntry[Key, Value] `json:"entries"`
	Locked  bool                     `json:"locked"`
}

// ToMap returns every entry as a plain map. Bucket indices and order are
// lost; use Snapshot to keep them.
func (bm *BucketMap[Key, Value]) ToMap() map[Key]Value {
	if bm.locked {
		bm.lock.RLock()
		defer bm.lock.RUnlock()
	}
	result := make(map[Key]Value, len(bm.entries))
	for key, n := range bm.entries {
		result[key] = n.value
	}
	return result
}

// FromMap adds every entry to the bottom bucket, in the arbitrary order Go
// iterates the source map.
func (bm *BucketMap[Key, Value]) FromMap(entries map[Key]Value) {
	if bm.locked {
		bm.lock.Lock()
		defer bm.lock.Unlock()
	}
	for key, value := range entries {
		bm.send_to_end(key, value, false)
	}
}

// Snapshot captures the map, lowest bucket first.
func (bm *BucketMap[Key, Value]) Snapshot() *BucketMapSnapshot[Key, Value] {
	if bm.locked {
		bm.lock.RLock()
		defer bm.lock.RUnlock()
	}
	entries := make([]CacheEntry[Key, Value], 0, len(bm.entries))
	for b := bm.head; b != nil; b = b.next {
		for n := b.head; n != nil; n = n.next {
			entries = append(entries, CacheEntry[Key, Value]{Key: n.key, Value: n.value, Frequency: b.index})
		}
	}
	return &BucketMapSnapshot[Key, Value]{Entries: entries, Locked: bm.locked}
}

// RestoreSnapshot replaces the contents of the map with the snapshot, putting
// every entry back in the bucket it came from.
func (bm *BucketMap[Key, Value]) RestoreSnapshot(snapshot *BucketMapSnapshot[Key, Value]) error {
	if snapshot == nil {
		return ErrNilSnapshot
	}
	if bm.locked {
		bm.lock.Lock()
		defer bm.lock.Unlock()
	}
	bm.clear()
	for _, entry := range snapshot.Entries {
		bm.send_to_bucket(entry.Frequency, entry.Key, entry.Value)
	}
	return nil
}

// NewBucketMapFromSnapshot builds a map from a snapshot.
func NewBucketMapFromSnapshot[Key comparable, Value any](snapshot *BucketMapSnapshot[Key, Value], locked bool) (*BucketMap[Key, Value], error) {
	bm := NewBucketMap[Key, Value](locked)
	if err := bm.RestoreSnapshot(snapshot); err != nil {
		return nil, err
	}
	return bm, nil
}

// SaveToFile writes a snapshot of the map to filename.
func (bm *BucketMap[Key, Value]) SaveToFile(filename string, format SerializationFormat, algorithm CompressionAlgorithm, mode *os.FileMode) error {
	return SaveToFile(bm.Snapshot(), filename, format, algorithm, mode)
}

// LoadFromFile replaces the contents of the map with a snapshot read from
// filename.
func (bm *BucketMap[Key, Value]) LoadFromFile(filename string, format SerializationFormat, algorithm CompressionAlgorithm) error {
	snapshot, err := LoadFromFile[*BucketMapSnapshot[Key, Value]](filename, format, algorithm)
	if err != nil {
		return err
	}
	return bm.RestoreSnapshot(snapshot)
}
