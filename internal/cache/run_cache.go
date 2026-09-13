package cache

import (
	"fmt"
	"hash/fnv"
	"sync"
	"time"
)

// entry holds a cached value with its expiry time.
type entry struct {
	value     string
	expiresAt int64
}

// RunCache is an in-memory, TTL-aware cache for agent run IDs.
type RunCache struct {
	mu      sync.RWMutex
	entries map[string]*entry
	order   []string
	max     int
	ttl     time.Duration
	salt    string
}

// Config configures the RunCache.
type Config struct {
	MaxEntries int
	TTL        time.Duration
	Salt       string
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		MaxEntries: 512,
		TTL:        30 * time.Minute,
		Salt:       "cmux-use-platform-key",
	}
}

// NewRunCache creates a new RunCache.
func NewRunCache(cfg Config) *RunCache {
	if cfg.MaxEntries <= 0 {
		cfg.MaxEntries = 512
	}
	if cfg.TTL <= 0 {
		cfg.TTL = 30 * time.Minute
	}
	return &RunCache{
		entries: make(map[string]*entry),
		max:     cfg.MaxEntries,
		ttl:     cfg.TTL,
		salt:    cfg.Salt,
	}
}

// Get retrieves a cached value.
func (c *RunCache) Get(key string) (string, bool) {
	c.mu.RLock()
	e, ok := c.entries[key]
	if !ok {
		c.mu.RUnlock()
		return "", false
	}

	now := time.Now().UnixMilli()
	if e.expiresAt > 0 && now > e.expiresAt {
		c.mu.RUnlock()
		c.mu.Lock()
		delete(c.entries, key)
		c.removeOrderLocked(key)
		c.mu.Unlock()
		return "", false
	}
	c.mu.RUnlock()
	return e.value, true
}

// Set stores a value with TTL.
func (c *RunCache) Set(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.entries[key]; ok {
		c.entries[key] = &entry{
			value:     value,
			expiresAt: time.Now().Add(c.ttl).UnixMilli(),
		}
		return
	}

	if len(c.entries) >= c.max {
		for _, oldKey := range c.order {
			if _, ok := c.entries[oldKey]; ok {
				delete(c.entries, oldKey)
				c.removeOrderLocked(oldKey)
				break
			}
		}
	}

	c.entries[key] = &entry{
		value:     value,
		expiresAt: time.Now().Add(c.ttl).UnixMilli(),
	}
	c.order = append(c.order, key)
}

// Delete removes a key from the cache.
func (c *RunCache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
	c.removeOrderLocked(key)
}

// Len returns the current number of cache entries.
func (c *RunCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

// HashFNV1a returns a salted FNV-1a hash of the input.
func (c *RunCache) HashFNV1a(value string) string {
	input := c.salt + ":" + value
	h := fnv.New32a()
	h.Write([]byte(input))
	return fmt.Sprintf("%x", h.Sum32())
}

// BuildAgentRunCacheKey builds the cache key for an agent run ID.
func BuildAgentRunCacheKey(hashedKey, agentID, clientIdentity string) string {
	return hashedKey + "\x00" + agentID + "\x00" + clientIdentity
}

func (c *RunCache) removeOrderLocked(key string) {
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			return
		}
	}
}
