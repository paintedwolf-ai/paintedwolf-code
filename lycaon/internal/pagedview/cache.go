package pagedview

import "container/list"

type cached[K comparable, V any] struct {
	key   K
	value V
	bytes int64
}

// Cache requires serialized access. Eviction preserves projection coordinates.
type Cache[K comparable, V any] struct {
	entries      map[K]*list.Element
	order        list.List
	bytes, limit int64
	capacity     int
}

func NewCache[K comparable, V any](capacity int, bytes int64) *Cache[K, V] {
	return &Cache[K, V]{entries: make(map[K]*list.Element), capacity: max(0, capacity), limit: max(0, bytes)}
}
func (c *Cache[K, V]) Get(key K) (V, bool) {
	if item := c.entries[key]; item != nil {
		c.order.MoveToFront(item)
		return item.Value.(cached[K, V]).value, true
	}
	var zero V
	return zero, false
}
func (c *Cache[K, V]) Put(key K, value V, bytes int64) {
	if old := c.entries[key]; old != nil {
		c.remove(old)
	}
	if bytes < 0 || bytes > c.limit || c.capacity == 0 {
		return
	}
	for len(c.entries) >= c.capacity || bytes > c.limit-c.bytes {
		c.remove(c.order.Back())
	}
	c.entries[key] = c.order.PushFront(cached[K, V]{key, value, bytes})
	c.bytes += bytes
}
func (c *Cache[K, V]) Delete(key K) {
	if item := c.entries[key]; item != nil {
		c.remove(item)
	}
}
func (c *Cache[K, V]) remove(item *list.Element) {
	value := item.Value.(cached[K, V])
	delete(c.entries, value.key)
	c.bytes -= value.bytes
	c.order.Remove(item)
}
func (c *Cache[K, V]) Bytes() int64 { return c.bytes }
func (c *Cache[K, V]) Len() int     { return len(c.entries) }
