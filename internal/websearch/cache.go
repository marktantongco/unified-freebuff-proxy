package websearch

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// cacheEntry holds a cached search response with expiry.
type cacheEntry struct {
	Response Response  `json:"response"`
	Expires  time.Time `json:"expires"`
}

// Cache is an in-memory TTL cache for search results, with optional
// file-backed persistence (24h max age, best-effort).
type Cache struct {
	mu       sync.RWMutex
	items    map[string]cacheEntry
	ttl      time.Duration
	filePath string // directory for file cache; empty = no persistence
}

// NewCache creates a search cache with the given TTL. If filePath is non-empty,
// it also loads/saves cache entries to SHA256-named JSON files under that dir.
func NewCache(ttl time.Duration, filePath string) *Cache {
	c := &Cache{
		items:    make(map[string]cacheEntry),
		ttl:      ttl,
		filePath: filePath,
	}
	go c.evictLoop()
	return c
}

// Get retrieves a cached response. Returns zero Response and false if missing
// or expired.
func (c *Cache) Get(key string) (Response, bool) {
	c.mu.RLock()
	e, ok := c.items[key]
	c.mu.RUnlock()
	if !ok || time.Now().After(e.Expires) {
		return Response{}, false
	}
	return e.Response, true
}

// Set stores a response in the cache. If filePath is set, also writes to disk.
func (c *Cache) Set(key string, resp Response) {
	now := time.Now()
	entry := cacheEntry{
		Response: resp,
		Expires:  now.Add(c.ttl),
	}
	c.mu.Lock()
	c.items[key] = entry
	c.mu.Unlock()

	if c.filePath != "" {
		c.saveToFile(key, entry)
	}
}

// evictLoop purges expired in-memory entries every 5 minutes.
func (c *Cache) evictLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		c.mu.Lock()
		now := time.Now()
		for k, v := range c.items {
			if now.After(v.Expires) {
				delete(c.items, k)
			}
		}
		c.mu.Unlock()
	}
}

// saveToFile writes a cache entry to a JSON file named by SHA256(key)[:16].
func (c *Cache) saveToFile(key string, entry cacheEntry) {
	if c.filePath == "" {
		return
	}
	// Best-effort: ignore all errors.
	_ = os.MkdirAll(c.filePath, 0o755)
	h := sha256.Sum256([]byte(key))
	filename := fmt.Sprintf("%x.json", h[:8])
	path := filepath.Join(c.filePath, filename)

	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}

// loadFromFile attempts to load a cache entry from disk. Returns zero
// Response and false if file missing, corrupt, or expired.
func (c *Cache) loadFromFile(key string) (Response, bool) {
	if c.filePath == "" {
		return Response{}, false
	}
	h := sha256.Sum256([]byte(key))
	filename := fmt.Sprintf("%x.json", h[:8])
	path := filepath.Join(c.filePath, filename)

	data, err := os.ReadFile(path)
	if err != nil {
		return Response{}, false
	}
	var entry cacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		// Corrupt file: delete it and return miss.
		_ = os.Remove(path)
		return Response{}, false
	}
	if time.Now().After(entry.Expires) {
		_ = os.Remove(path)
		return Response{}, false
	}
	// Populate in-memory cache for future hits.
	c.mu.Lock()
	c.items[key] = entry
	c.mu.Unlock()
	return entry.Response, true
}

// GetOrLoad retrieves from memory, then falls back to file cache.
func (c *Cache) GetOrLoad(key string) (Response, bool) {
	if r, ok := c.Get(key); ok {
		return r, true
	}
	return c.loadFromFile(key)
}

// CacheDir returns the default cache directory (~/.cache/freebuff/search).
func CacheDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".cache", "freebuff", "search")
}
