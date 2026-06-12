package idtable

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLRUCache_NewWithZeroCapacity(t *testing.T) {
	cache := newLRUCache(0)
	assert.NotNil(t, cache)
	assert.Equal(t, 0, cache.capacity)

	// Put and Get should be no-ops on a zero-capacity cache.
	cache.Put("key", 42)
	v, ok := cache.Get("key")
	assert.False(t, ok)
	assert.Equal(t, int64(-1), v)
}

func TestLRUCache_NewWithNegativeCapacity(t *testing.T) {
	cache := newLRUCache(-5)
	assert.NotNil(t, cache)
	assert.Equal(t, 0, cache.capacity)

	// Negative capacity should behave like zero capacity.
	cache.Put("a", 1)
	v, ok := cache.Get("a")
	assert.False(t, ok)
	assert.Equal(t, int64(-1), v)
}

func TestLRUCache_PutAndGet(t *testing.T) {
	cache := newLRUCache(3)

	cache.Put("a", 1)
	cache.Put("b", 2)
	cache.Put("c", 3)

	v, ok := cache.Get("a")
	assert.True(t, ok)
	assert.Equal(t, int64(1), v)

	v, ok = cache.Get("b")
	assert.True(t, ok)
	assert.Equal(t, int64(2), v)

	v, ok = cache.Get("c")
	assert.True(t, ok)
	assert.Equal(t, int64(3), v)
}

func TestLRUCache_PutUpdateExisting(t *testing.T) {
	cache := newLRUCache(3)

	cache.Put("a", 1)
	cache.Put("a", 99)

	v, ok := cache.Get("a")
	assert.True(t, ok)
	assert.Equal(t, int64(99), v)

	// Only one entry should be in the cache.
	assert.Equal(t, 1, cache.list.Len())
}

func TestLRUCache_Eviction(t *testing.T) {
	cache := newLRUCache(2)

	cache.Put("a", 1)
	cache.Put("b", 2)

	// "a" is the least recently used. Adding "c" should evict "a".
	cache.Put("c", 3)

	_, ok := cache.Get("a")
	assert.False(t, ok, "a should have been evicted")

	v, ok := cache.Get("b")
	assert.True(t, ok)
	assert.Equal(t, int64(2), v)

	v, ok = cache.Get("c")
	assert.True(t, ok)
	assert.Equal(t, int64(3), v)
}

func TestLRUCache_EvictionWithAccess(t *testing.T) {
	cache := newLRUCache(2)

	cache.Put("a", 1)
	cache.Put("b", 2)

	// Access "a" to make it recently used; "b" becomes LRU.
	cache.Get("a")

	cache.Put("c", 3)

	_, ok := cache.Get("b")
	assert.False(t, ok, "b should have been evicted")

	v, ok := cache.Get("a")
	assert.True(t, ok)
	assert.Equal(t, int64(1), v)

	v, ok = cache.Get("c")
	assert.True(t, ok)
	assert.Equal(t, int64(3), v)
}

func TestLRUCache_GetMiss(t *testing.T) {
	cache := newLRUCache(5)

	v, ok := cache.Get("nonexistent")
	assert.False(t, ok)
	assert.Equal(t, int64(-1), v)
}

func TestLRUCache_Delete(t *testing.T) {
	cache := newLRUCache(5)

	cache.Put("a", 1)
	cache.Put("b", 2)
	cache.Put("c", 3)

	cache.Delete("b")

	_, ok := cache.Get("b")
	assert.False(t, ok, "b should have been deleted")

	// Other entries should still be present.
	v, ok := cache.Get("a")
	assert.True(t, ok)
	assert.Equal(t, int64(1), v)

	v, ok = cache.Get("c")
	assert.True(t, ok)
	assert.Equal(t, int64(3), v)

	assert.Equal(t, 2, cache.list.Len())
}

func TestLRUCache_DeleteNonexistent(t *testing.T) {
	cache := newLRUCache(5)

	cache.Put("a", 1)
	// Should not panic.
	cache.Delete("nonexistent")

	v, ok := cache.Get("a")
	assert.True(t, ok)
	assert.Equal(t, int64(1), v)
}

func TestLRUCache_Clear(t *testing.T) {
	cache := newLRUCache(5)

	cache.Put("a", 1)
	cache.Put("b", 2)

	cache.Clear()

	_, ok := cache.Get("a")
	assert.False(t, ok)
	_, ok = cache.Get("b")
	assert.False(t, ok)

	assert.Equal(t, 0, cache.list.Len())
	assert.Equal(t, 0, len(cache.cache))
}

func TestLRUCache_SingleCapacity(t *testing.T) {
	cache := newLRUCache(1)

	cache.Put("a", 1)
	v, ok := cache.Get("a")
	assert.True(t, ok)
	assert.Equal(t, int64(1), v)

	// Adding a second key should evict the first.
	cache.Put("b", 2)
	_, ok = cache.Get("a")
	assert.False(t, ok, "a should be evicted from single-capacity cache")

	v, ok = cache.Get("b")
	assert.True(t, ok)
	assert.Equal(t, int64(2), v)
}

func TestLRUCache_EvictionOrder(t *testing.T) {
	// Verify correct eviction order when multiple items are added.
	cache := newLRUCache(3)

	cache.Put("a", 1) // oldest
	cache.Put("b", 2)
	cache.Put("c", 3) // newest

	// Cache is full. Adding "d" should evict "a" (LRU).
	cache.Put("d", 4)

	_, ok := cache.Get("a")
	assert.False(t, ok, "a should have been evicted as LRU")

	// "b", "c", "d" should still be present.
	for _, key := range []string{"b", "c", "d"} {
		_, ok = cache.Get(key)
		assert.True(t, ok, "%s should still be in cache", key)
	}

	// Now "b" is the least recently used (we just accessed b, c, d via Get,
	// but the Get reorders, so let's be explicit).
	// After the Gets above, the order is: d (LRU) -> b -> c (most recent was "d" get first, then "c" last)
	// Actually the last Get was "d", so order from LRU perspective:
	// We need to add two more items to evict "b" (which was accessed, making it not LRU).
	// Let's verify by adding "e" - "d" might be LRU now since it was first in the Get sequence.
	// This is complex, let's test a cleaner scenario:
	cache2 := newLRUCache(2)
	cache2.Put("x", 10)
	cache2.Put("y", 20)
	// "x" is LRU. Update "x" to make "y" the LRU.
	cache2.Put("x", 11)
	cache2.Put("z", 30) // Should evict "y" (LRU).

	_, ok = cache2.Get("y")
	assert.False(t, ok, "y should have been evicted after x was updated")

	v, ok := cache2.Get("x")
	assert.True(t, ok)
	assert.Equal(t, int64(11), v)

	v, ok = cache2.Get("z")
	assert.True(t, ok)
	assert.Equal(t, int64(30), v)
}

func TestLRUCache_DeleteThenPut(t *testing.T) {
	cache := newLRUCache(2)

	cache.Put("a", 1)
	cache.Put("b", 2)

	// Delete "a", freeing a slot.
	cache.Delete("a")
	assert.Equal(t, 1, cache.list.Len())

	// Adding "c" should not cause eviction since there's room.
	cache.Put("c", 3)
	assert.Equal(t, 2, cache.list.Len())

	v, ok := cache.Get("b")
	assert.True(t, ok)
	assert.Equal(t, int64(2), v)

	v, ok = cache.Get("c")
	assert.True(t, ok)
	assert.Equal(t, int64(3), v)

	// "a" should still be gone.
	_, ok = cache.Get("a")
	assert.False(t, ok)
}

func TestLRUCache_ClearThenReuse(t *testing.T) {
	cache := newLRUCache(3)

	cache.Put("a", 1)
	cache.Put("b", 2)
	cache.Clear()

	// Cache should be usable after clear.
	cache.Put("c", 3)
	v, ok := cache.Get("c")
	assert.True(t, ok)
	assert.Equal(t, int64(3), v)

	// Old entries should not reappear.
	_, ok = cache.Get("a")
	assert.False(t, ok)
}

func TestLRUCache_DeleteAllEntries(t *testing.T) {
	cache := newLRUCache(3)

	cache.Put("a", 1)
	cache.Put("b", 2)
	cache.Put("c", 3)

	cache.Delete("a")
	cache.Delete("b")
	cache.Delete("c")

	assert.Equal(t, 0, cache.list.Len())
	assert.Equal(t, 0, len(cache.cache))

	// Should still work after deleting everything.
	cache.Put("d", 4)
	v, ok := cache.Get("d")
	assert.True(t, ok)
	assert.Equal(t, int64(4), v)
}

func TestLRUCache_PutZeroCapacityIsNoop(t *testing.T) {
	cache := newLRUCache(0)

	// Multiple puts should all be no-ops.
	cache.Put("a", 1)
	cache.Put("b", 2)
	cache.Put("c", 3)

	assert.Equal(t, 0, cache.list.Len())
	assert.Equal(t, 0, len(cache.cache))
}

func TestLRUCache_RepeatedEvictions(t *testing.T) {
	cache := newLRUCache(1)

	// Each put should evict the previous entry.
	for i := int64(0); i < 10; i++ {
		key := string(rune('a' + i))
		cache.Put(key, i)

		v, ok := cache.Get(key)
		assert.True(t, ok, "key %s should be in cache", key)
		assert.Equal(t, i, v)
		assert.Equal(t, 1, cache.list.Len())
	}
}
