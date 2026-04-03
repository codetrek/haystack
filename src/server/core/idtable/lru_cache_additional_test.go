package idtable

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLRUCache_BasicOps(t *testing.T) {
	c := NewLRUCache(3)

	c.Put("a", 1)
	c.Put("b", 2)
	c.Put("c", 3)

	v, ok := c.Get("a")
	assert.True(t, ok)
	assert.Equal(t, int64(1), v)

	v, ok = c.Get("b")
	assert.True(t, ok)
	assert.Equal(t, int64(2), v)
}

func TestLRUCache_Eviction_Simple(t *testing.T) {
	c := NewLRUCache(2)

	c.Put("a", 1)
	c.Put("b", 2)
	c.Put("c", 3) // should evict "a"

	_, ok := c.Get("a")
	assert.False(t, ok)

	v, ok := c.Get("b")
	assert.True(t, ok)
	assert.Equal(t, int64(2), v)

	v, ok = c.Get("c")
	assert.True(t, ok)
	assert.Equal(t, int64(3), v)
}

func TestLRUCache_Update(t *testing.T) {
	c := NewLRUCache(2)

	c.Put("a", 1)
	c.Put("a", 10) // update

	v, ok := c.Get("a")
	assert.True(t, ok)
	assert.Equal(t, int64(10), v)
}

func TestLRUCache_Delete_Simple(t *testing.T) {
	c := NewLRUCache(3)

	c.Put("a", 1)
	c.Put("b", 2)

	c.Delete("a")
	_, ok := c.Get("a")
	assert.False(t, ok)

	v, ok := c.Get("b")
	assert.True(t, ok)
	assert.Equal(t, int64(2), v)
}

func TestLRUCache_DeleteNonExistent(t *testing.T) {
	c := NewLRUCache(3)
	c.Delete("nonexistent") // should not panic
}

func TestLRUCache_Clear_Simple(t *testing.T) {
	c := NewLRUCache(3)

	c.Put("a", 1)
	c.Put("b", 2)
	c.Clear()

	_, ok := c.Get("a")
	assert.False(t, ok)
}

func TestLRUCache_ZeroCapacity(t *testing.T) {
	c := NewLRUCache(0)

	c.Put("a", 1)
	_, ok := c.Get("a")
	assert.False(t, ok) // zero capacity never stores
}

func TestLRUCache_NegativeCapacity(t *testing.T) {
	c := NewLRUCache(-1)
	assert.Equal(t, 0, c.capacity)
}

func TestLRUCache_GetMiss_Simple(t *testing.T) {
	c := NewLRUCache(3)

	v, ok := c.Get("miss")
	assert.False(t, ok)
	assert.Equal(t, int64(-1), v)
}

func TestLRUCache_LRUOrdering(t *testing.T) {
	c := NewLRUCache(3)

	c.Put("a", 1)
	c.Put("b", 2)
	c.Put("c", 3)

	// Access "a" to make it most recently used
	c.Get("a")

	// Add "d" — should evict "b" (least recently used)
	c.Put("d", 4)

	_, ok := c.Get("b")
	assert.False(t, ok, "b should have been evicted")

	_, ok = c.Get("a")
	assert.True(t, ok, "a should still be present")
}
