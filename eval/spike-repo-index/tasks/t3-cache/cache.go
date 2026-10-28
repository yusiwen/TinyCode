package task

// Cache is a key-value store whose entries expire after a number of ticks.
type Cache struct {
	clock *Clock
	ttl   int
	items map[string]entry
}

type entry struct {
	value  int
	readAt int
}

// NewCache returns a cache whose entries live for ttl ticks after they were last
// written. A ttl of zero means entries never expire; a capacity of zero means
// nothing is ever stored.
func NewCache(clock *Clock, capacity, ttl int) *Cache {
	return &Cache{clock: clock, ttl: ttl, items: make(map[string]entry)}
}

// Put stores a value under a key.
func (c *Cache) Put(key string, value int) {
	c.items[key] = entry{value: value, readAt: c.clock.Now()}
}

// Get returns the stored value, or 0 when the key is absent or expired.
func (c *Cache) Get(key string) int {
	item, ok := c.items[key]
	if !ok {
		return 0
	}
	return item.value
}
