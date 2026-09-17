package jr_cache

import "sync"

// Structs for the ordered map implementation

type node[Key comparable, Value any] struct {
	Key   Key
	Value Value
	Prev  *node[Key, Value]
	Next  *node[Key, Value]
}

// OrderedMap is a doubly linked list with a hash map for fast lookups.
// The list maintains the order of insertion, and the map allows O(1) access to nodes by key.
type OrderedMap[Key comparable, Value any] struct {
	size  uint64
	head  *node[Key, Value]
	tail  *node[Key, Value]
	nodes map[Key]*node[Key, Value]

	locked bool
	lock   sync.RWMutex
}

// Interface for the ordered map implementation

type OrderedMapInterface[Key comparable, Value any] interface {
	CommonMapInterface[Key, Value]
	SetTop(key Key, value Value)
	SetBottom(key Key, value Value)
	SetAfter(anchor Key, key Key, value Value) bool
	SetBefore(anchor Key, key Key, value Value) bool
	NextKey(key Key) (Key, bool)
	PrevKey(key Key) (Key, bool)
	MoveToTop(key Key)
	MoveToBottom(key Key)
}

// Compile-time check that OrderedMap satisfies its interface.
var _ OrderedMapInterface[int, int] = (*OrderedMap[int, int])(nil)

// Constructors and Destructors for the ordered map implementation

func newNode[Key comparable, Value any](key Key, value Value, prev *node[Key, Value], next *node[Key, Value]) *node[Key, Value] {
	return &node[Key, Value]{
		Key:   key,
		Value: value,
		Prev:  prev,
		Next:  next,
	}
}

func NewOrderedMap[Key comparable, Value any](locked bool) *OrderedMap[Key, Value] {
	return &OrderedMap[Key, Value]{
		size:   0,
		head:   nil,
		tail:   nil,
		nodes:  make(map[Key]*node[Key, Value]),
		locked: locked,
		lock:   sync.RWMutex{},
	}
}

// Private methods for the ordered map implementation

// unlink_node detaches node from the list and decrements size.
// It does not touch om.nodes; callers that remove the entry must do that.
func (om *OrderedMap[Key, Value]) unlink_node(node *node[Key, Value]) {
	if node.Prev != nil {
		node.Prev.Next = node.Next
	} else {
		om.head = node.Next
	}
	if node.Next != nil {
		node.Next.Prev = node.Prev
	} else {
		om.tail = node.Prev
	}
	node.Prev = nil
	node.Next = nil
	om.size--
}

func (om *OrderedMap[Key, Value]) link_node_at_top(node *node[Key, Value]) {
	node.Next = om.head
	node.Prev = nil
	if om.head != nil {
		om.head.Prev = node
	} else {
		om.tail = node
	}
	om.head = node
	om.size++
}

func (om *OrderedMap[Key, Value]) link_node_at_bottom(node *node[Key, Value]) {
	node.Prev = om.tail
	node.Next = nil
	if om.tail != nil {
		om.tail.Next = node
	} else {
		om.head = node
	}
	om.tail = node
	om.size++
}

func (om *OrderedMap[Key, Value]) link_node_after(anchor *node[Key, Value], node *node[Key, Value]) {
	node.Prev = anchor
	node.Next = anchor.Next
	if anchor.Next != nil {
		anchor.Next.Prev = node
	} else {
		om.tail = node
	}
	anchor.Next = node
	om.size++
}

func (om *OrderedMap[Key, Value]) link_node_before(anchor *node[Key, Value], node *node[Key, Value]) {
	node.Next = anchor
	node.Prev = anchor.Prev
	if anchor.Prev != nil {
		anchor.Prev.Next = node
	} else {
		om.head = node
	}
	anchor.Prev = node
	om.size++
}

func (om *OrderedMap[Key, Value]) move_node_to_top(node *node[Key, Value]) {
	om.unlink_node(node)
	om.link_node_at_top(node)
}

func (om *OrderedMap[Key, Value]) move_node_to_bottom(node *node[Key, Value]) {
	om.unlink_node(node)
	om.link_node_at_bottom(node)
}

// pop removes key from both the list and the index and returns its value.
func (om *OrderedMap[Key, Value]) pop(key Key) (Value, bool) {
	node, exists := om.nodes[key]
	if !exists {
		var zero Value
		return zero, false
	}
	om.unlink_node(node)
	delete(om.nodes, key)
	return node.Value, true
}

// pop_node removes node from both the list and the index and returns its entry.
func (om *OrderedMap[Key, Value]) pop_node(node *node[Key, Value]) (Key, Value, bool) {
	if node == nil {
		var zeroKey Key
		var zeroValue Value
		return zeroKey, zeroValue, false
	}
	om.unlink_node(node)
	delete(om.nodes, node.Key)
	return node.Key, node.Value, true
}

// detach unlinks key and removes it from the index but keeps the node, so
// the caller can attach it to another map without allocating. Returns nil
// if key is absent. Callers must hold the lock (or use an unlocked map).
func (om *OrderedMap[Key, Value]) detach(key Key) *node[Key, Value] {
	node, exists := om.nodes[key]
	if !exists {
		return nil
	}
	om.unlink_node(node)
	delete(om.nodes, key)
	return node
}

// attach_bottom links a node obtained from detach at the tail and indexes
// it. If the node's key is already present, the existing entry is replaced.
// Callers must hold the lock (or use an unlocked map).
func (om *OrderedMap[Key, Value]) attach_bottom(node *node[Key, Value]) {
	if existing, exists := om.nodes[node.Key]; exists {
		om.unlink_node(existing)
	}
	om.nodes[node.Key] = node
	om.link_node_at_bottom(node)
}

// entry returns the key and value held by node, or zero values for nil.
func entry[Key comparable, Value any](node *node[Key, Value]) (Key, Value, bool) {
	if node == nil {
		var zeroKey Key
		var zeroValue Value
		return zeroKey, zeroValue, false
	}
	return node.Key, node.Value, true
}

// set_relative inserts (or moves) key next to anchor. after selects which
// side of the anchor the node ends up on. Returns false if anchor is absent.
func (om *OrderedMap[Key, Value]) set_relative(anchor Key, key Key, value Value, after bool) bool {
	anchorNode, exists := om.nodes[anchor]
	if !exists {
		return false
	}
	node, exists := om.nodes[key]
	if exists {
		node.Value = value
		if node == anchorNode {
			return true
		}
		om.unlink_node(node)
	} else {
		node = newNode(key, value, nil, nil)
		om.nodes[key] = node
	}
	if after {
		om.link_node_after(anchorNode, node)
	} else {
		om.link_node_before(anchorNode, node)
	}
	return true
}

// Public methods for the ordered map implementation

func (om *OrderedMap[Key, Value]) Len() int {
	if om.locked {
		om.lock.RLock()
		defer om.lock.RUnlock()
	}
	return int(om.size)
}

// Top returns the entry at the head of the map without moving it.
func (om *OrderedMap[Key, Value]) Top() (Key, Value, bool) {
	if om.locked {
		om.lock.RLock()
		defer om.lock.RUnlock()
	}
	return entry(om.head)
}

// Bottom returns the entry at the tail of the map without moving it.
func (om *OrderedMap[Key, Value]) Bottom() (Key, Value, bool) {
	if om.locked {
		om.lock.RLock()
		defer om.lock.RUnlock()
	}
	return entry(om.tail)
}

func (om *OrderedMap[Key, Value]) TopKey() (Key, bool) {
	if om.locked {
		om.lock.RLock()
		defer om.lock.RUnlock()
	}
	key, _, ok := entry(om.head)
	return key, ok
}

func (om *OrderedMap[Key, Value]) BottomKey() (Key, bool) {
	if om.locked {
		om.lock.RLock()
		defer om.lock.RUnlock()
	}
	key, _, ok := entry(om.tail)
	return key, ok
}

// PopTop removes and returns the entry at the head of the map.
func (om *OrderedMap[Key, Value]) PopTop() (Key, Value, bool) {
	if om.locked {
		om.lock.Lock()
		defer om.lock.Unlock()
	}
	return om.pop_node(om.head)
}

// PopBottom removes and returns the entry at the tail of the map.
func (om *OrderedMap[Key, Value]) PopBottom() (Key, Value, bool) {
	if om.locked {
		om.lock.Lock()
		defer om.lock.Unlock()
	}
	return om.pop_node(om.tail)
}

// NextKey returns the key that follows key (towards the bottom).
func (om *OrderedMap[Key, Value]) NextKey(key Key) (Key, bool) {
	if om.locked {
		om.lock.RLock()
		defer om.lock.RUnlock()
	}
	node, exists := om.nodes[key]
	if !exists || node.Next == nil {
		var zero Key
		return zero, false
	}
	return node.Next.Key, true
}

// PrevKey returns the key that precedes key (towards the top).
func (om *OrderedMap[Key, Value]) PrevKey(key Key) (Key, bool) {
	if om.locked {
		om.lock.RLock()
		defer om.lock.RUnlock()
	}
	node, exists := om.nodes[key]
	if !exists || node.Prev == nil {
		var zero Key
		return zero, false
	}
	return node.Prev.Key, true
}

func (om *OrderedMap[Key, Value]) MoveToBottom(key Key) {
	if om.locked {
		om.lock.Lock()
		defer om.lock.Unlock()
	}
	node, exists := om.nodes[key]
	if !exists {
		return
	}
	om.move_node_to_bottom(node)
}

func (om *OrderedMap[Key, Value]) MoveToTop(key Key) {
	if om.locked {
		om.lock.Lock()
		defer om.lock.Unlock()
	}
	node, exists := om.nodes[key]
	if !exists {
		return
	}
	om.move_node_to_top(node)
}

func (om *OrderedMap[Key, Value]) Get(key Key) (Value, bool) {
	if om.locked {
		om.lock.RLock()
		defer om.lock.RUnlock()
	}
	node, exists := om.nodes[key]
	if !exists {
		var zero Value
		return zero, false
	}
	return node.Value, true
}

func (om *OrderedMap[Key, Value]) Has(key Key) bool {
	if om.locked {
		om.lock.RLock()
		defer om.lock.RUnlock()
	}
	_, exists := om.nodes[key]
	return exists
}

// Update replaces the value of an existing key without changing its position.
// Returns false (and does nothing) if key is not in the map.
func (om *OrderedMap[Key, Value]) Update(key Key, value Value) bool {
	if om.locked {
		om.lock.Lock()
		defer om.lock.Unlock()
	}
	node, exists := om.nodes[key]
	if !exists {
		return false
	}
	node.Value = value
	return true
}

func (om *OrderedMap[Key, Value]) SetTop(key Key, value Value) {
	if om.locked {
		om.lock.Lock()
		defer om.lock.Unlock()
	}
	node, exists := om.nodes[key]
	if exists {
		node.Value = value
		om.move_node_to_top(node)
	} else {
		node = newNode(key, value, nil, nil)
		om.nodes[key] = node
		om.link_node_at_top(node)
	}
}

func (om *OrderedMap[Key, Value]) SetBottom(key Key, value Value) {
	if om.locked {
		om.lock.Lock()
		defer om.lock.Unlock()
	}
	node, exists := om.nodes[key]
	if exists {
		node.Value = value
		om.move_node_to_bottom(node)
	} else {
		node = newNode(key, value, nil, nil)
		om.nodes[key] = node
		om.link_node_at_bottom(node)
	}
}

// SetAfter inserts key directly below anchor, moving it if it already exists.
// Returns false (and does nothing) if anchor is not in the map.
func (om *OrderedMap[Key, Value]) SetAfter(anchor Key, key Key, value Value) bool {
	if om.locked {
		om.lock.Lock()
		defer om.lock.Unlock()
	}
	return om.set_relative(anchor, key, value, true)
}

// SetBefore inserts key directly above anchor, moving it if it already exists.
// Returns false (and does nothing) if anchor is not in the map.
func (om *OrderedMap[Key, Value]) SetBefore(anchor Key, key Key, value Value) bool {
	if om.locked {
		om.lock.Lock()
		defer om.lock.Unlock()
	}
	return om.set_relative(anchor, key, value, false)
}

func (om *OrderedMap[Key, Value]) Delete(key Key) {
	if om.locked {
		om.lock.Lock()
		defer om.lock.Unlock()
	}
	om.pop(key)
}

func (om *OrderedMap[Key, Value]) Pop(key Key) (Value, bool) {
	if om.locked {
		om.lock.Lock()
		defer om.lock.Unlock()
	}
	return om.pop(key)
}

func (om *OrderedMap[Key, Value]) Clear() {
	if om.locked {
		om.lock.Lock()
		defer om.lock.Unlock()
	}
	om.head = nil
	om.tail = nil
	om.size = 0
	// clear keeps the map's storage, so a map that is emptied and refilled
	// (a pooled bucket, for instance) does not allocate again.
	clear(om.nodes)
}
