package idtable

import (
	"container/list"
	"sync"
)

// lruCache implements a thread-safe, fixed-capacity LRU cache that maps
// string keys to int64 ids. It is used internally by Allocator to avoid
// redundant store look-ups for recently allocated ids.
type lruCache struct {
	capacity int
	cache    map[string]*list.Element
	list     *list.List
	mu       sync.RWMutex
}

// cacheEntry represents an entry in the LRU cache
type cacheEntry struct {
	key   string
	value int64
}

// newLRUCache creates a new LRU cache with the specified capacity
func newLRUCache(capacity int) *lruCache {
	if capacity <= 0 {
		capacity = 0
	}
	return &lruCache{
		capacity: capacity,
		cache:    make(map[string]*list.Element),
		list:     list.New(),
	}
}

// Get retrieves a value from the cache
func (c *lruCache) Get(key string) (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.capacity == 0 {
		return -1, false
	}

	if elem, ok := c.cache[key]; ok {
		c.list.MoveToFront(elem)
		return elem.Value.(*cacheEntry).value, true
	}
	return -1, false
}

// Put adds or updates a value in the cache
func (c *lruCache) Put(key string, value int64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.capacity == 0 {
		return
	}

	if elem, ok := c.cache[key]; ok {
		c.list.MoveToFront(elem)
		elem.Value.(*cacheEntry).value = value
		return
	}

	if c.list.Len() >= c.capacity {
		// Remove the least recently used item
		last := c.list.Back()
		if last != nil {
			delete(c.cache, string(last.Value.(*cacheEntry).key))
			c.list.Remove(last)
		}
	}

	entry := &cacheEntry{key: key, value: value}
	elem := c.list.PushFront(entry)
	c.cache[key] = elem
}

// Delete removes a value from the cache
func (c *lruCache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, ok := c.cache[key]; ok {
		delete(c.cache, key)
		c.list.Remove(elem)
	}
}

// Clear removes all values from the cache
func (c *lruCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cache = make(map[string]*list.Element)
	c.list.Init()
}
