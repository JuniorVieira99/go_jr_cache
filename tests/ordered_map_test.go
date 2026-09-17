package tests

import (
	"testing"

	jr_cache "jr_cache/code"
)

func keysTopDown[Key comparable, Value any](om *jr_cache.OrderedMap[Key, Value]) []Key {
	var keys []Key
	for k, ok := om.TopKey(); ok; k, ok = om.NextKey(k) {
		keys = append(keys, k)
	}
	return keys
}

func keysBottomUp[Key comparable, Value any](om *jr_cache.OrderedMap[Key, Value]) []Key {
	var keys []Key
	for k, ok := om.BottomKey(); ok; k, ok = om.PrevKey(k) {
		keys = append(keys, k)
	}
	return keys
}

func assertOrder(t *testing.T, om *jr_cache.OrderedMap[string, int], want ...string) {
	t.Helper()
	got := keysTopDown(om)
	if len(got) != len(want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
	// The backward links must agree with the forward links.
	back := keysBottomUp(om)
	for i := range want {
		if back[len(back)-1-i] != want[i] {
			t.Fatalf("reverse order = %v, want reverse of %v", back, want)
		}
	}
	if om.Len() != len(want) {
		t.Fatalf("Len() = %d, want %d", om.Len(), len(want))
	}
}

func TestOrderedMap_DeleteKeepsSizeConsistent(t *testing.T) {
	om := jr_cache.NewOrderedMap[string, int](false)
	om.SetBottom("a", 1)
	om.SetBottom("b", 2)
	om.Delete("a")
	if om.Len() != 1 {
		t.Fatalf("Len() after Delete = %d, want 1", om.Len())
	}
	om.Delete("missing")
	if om.Len() != 1 {
		t.Fatalf("Len() after deleting a missing key = %d, want 1", om.Len())
	}
	om.Delete("b")
	if om.Len() != 0 {
		t.Fatalf("Len() after deleting everything = %d, want 0", om.Len())
	}
	if _, _, ok := om.Top(); ok {
		t.Fatal("Top() on an empty map reported a value")
	}
}

func TestOrderedMap_TopBottom(t *testing.T) {
	om := jr_cache.NewOrderedMap[string, int](false)
	om.SetBottom("a", 1)
	om.SetBottom("b", 2)
	if k, v, ok := om.Top(); !ok || k != "a" || v != 1 {
		t.Fatalf("Top() = %q, %d, %v; want a, 1, true", k, v, ok)
	}
	if k, v, ok := om.Bottom(); !ok || k != "b" || v != 2 {
		t.Fatalf("Bottom() = %q, %d, %v; want b, 2, true", k, v, ok)
	}
	if k, ok := om.TopKey(); !ok || k != "a" {
		t.Fatalf("TopKey() = %q, %v; want a, true", k, ok)
	}
	if k, ok := om.BottomKey(); !ok || k != "b" {
		t.Fatalf("BottomKey() = %q, %v; want b, true", k, ok)
	}
	// Peeking must not reorder anything.
	assertOrder(t, om, "a", "b")
}

func TestOrderedMap_UpdateKeepsPosition(t *testing.T) {
	om := jr_cache.NewOrderedMap[string, int](false)
	om.SetBottom("a", 1)
	om.SetBottom("b", 2)
	if !om.Update("a", 10) {
		t.Fatal("Update(a) returned false")
	}
	if v, _ := om.Get("a"); v != 10 {
		t.Fatalf("Get(a) = %d, want 10", v)
	}
	assertOrder(t, om, "a", "b")
	if om.Update("missing", 0) {
		t.Fatal("Update on a missing key returned true")
	}
	assertOrder(t, om, "a", "b")
}

func TestOrderedMap_Clear(t *testing.T) {
	om := jr_cache.NewOrderedMap[string, int](false)
	om.SetBottom("a", 1)
	om.SetBottom("b", 2)
	om.Clear()
	assertOrder(t, om)
	if om.Has("a") {
		t.Fatal("Has(a) after Clear() is true")
	}
	// The map must be fully usable again.
	om.SetBottom("c", 3)
	assertOrder(t, om, "c")
}

func TestOrderedMap_PopTopBottom(t *testing.T) {
	om := jr_cache.NewOrderedMap[string, int](false)
	om.SetBottom("a", 1)
	om.SetBottom("b", 2)
	om.SetBottom("c", 3)

	if k, v, ok := om.PopTop(); !ok || k != "a" || v != 1 {
		t.Fatalf("PopTop() = %q, %d, %v; want a, 1, true", k, v, ok)
	}
	if k, v, ok := om.PopBottom(); !ok || k != "c" || v != 3 {
		t.Fatalf("PopBottom() = %q, %d, %v; want c, 3, true", k, v, ok)
	}
	assertOrder(t, om, "b")
	if om.Has("a") || om.Has("c") {
		t.Fatal("popped keys are still present")
	}
	om.PopTop()
	if _, _, ok := om.PopBottom(); ok {
		t.Fatal("PopBottom() on an empty map reported a value")
	}
	assertOrder(t, om)
}

func TestOrderedMap_SetMovesExisting(t *testing.T) {
	om := jr_cache.NewOrderedMap[string, int](false)
	om.SetBottom("a", 1)
	om.SetBottom("b", 2)
	om.SetBottom("c", 3)
	om.SetTop("c", 30)
	assertOrder(t, om, "c", "a", "b")
	if v, _ := om.Get("c"); v != 30 {
		t.Fatalf("Get(c) = %d, want 30", v)
	}
	om.MoveToBottom("c")
	assertOrder(t, om, "a", "b", "c")
	om.MoveToTop("b")
	assertOrder(t, om, "b", "a", "c")
}

func TestOrderedMap_SetAfterBefore(t *testing.T) {
	om := jr_cache.NewOrderedMap[string, int](false)
	if om.SetAfter("missing", "x", 0) {
		t.Fatal("SetAfter with a missing anchor returned true")
	}
	om.SetBottom("a", 1)
	om.SetBottom("c", 3)

	if !om.SetAfter("a", "b", 2) {
		t.Fatal("SetAfter(a, b) returned false")
	}
	assertOrder(t, om, "a", "b", "c")

	if !om.SetBefore("a", "z", 0) {
		t.Fatal("SetBefore(a, z) returned false")
	}
	assertOrder(t, om, "z", "a", "b", "c")

	// Inserting at the very bottom must update the tail.
	om.SetAfter("c", "d", 4)
	assertOrder(t, om, "z", "a", "b", "c", "d")

	// Existing keys are moved, not duplicated.
	om.SetAfter("d", "z", 26)
	assertOrder(t, om, "a", "b", "c", "d", "z")
	if v, _ := om.Get("z"); v != 26 {
		t.Fatalf("Get(z) = %d, want 26", v)
	}

	// Anchoring a key on itself only updates the value.
	om.SetBefore("b", "b", 20)
	assertOrder(t, om, "a", "b", "c", "d", "z")
	if v, _ := om.Get("b"); v != 20 {
		t.Fatalf("Get(b) = %d, want 20", v)
	}
}

func TestOrderedMap_NextPrevKey(t *testing.T) {
	om := jr_cache.NewOrderedMap[string, int](false)
	om.SetBottom("a", 1)
	om.SetBottom("b", 2)
	if k, ok := om.NextKey("a"); !ok || k != "b" {
		t.Fatalf("NextKey(a) = %q, %v; want b, true", k, ok)
	}
	if _, ok := om.NextKey("b"); ok {
		t.Fatal("NextKey on the bottom key reported a key")
	}
	if k, ok := om.PrevKey("b"); !ok || k != "a" {
		t.Fatalf("PrevKey(b) = %q, %v; want a, true", k, ok)
	}
	if _, ok := om.PrevKey("a"); ok {
		t.Fatal("PrevKey on the top key reported a key")
	}
	if _, ok := om.NextKey("missing"); ok {
		t.Fatal("NextKey on a missing key reported a key")
	}
}

func TestOrderedMap_ThreadSafe(t *testing.T) {
	om := jr_cache.NewOrderedMap[int, int](true)
	done := make(chan struct{})
	for g := 0; g < 4; g++ {
		go func(g int) {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 1000; i++ {
				k := g*1000 + i
				om.SetTop(k, i)
				om.MoveToBottom(k)
				om.Get(k)
				om.Delete(k)
			}
		}(g)
	}
	for g := 0; g < 4; g++ {
		<-done
	}
	if om.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", om.Len())
	}
}
