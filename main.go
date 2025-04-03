package main

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
)

// User represents a simple data structure to cache
type User struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Email    string    `json:"email"`
	Age      int       `json:"age"`
	CreateAt time.Time `json:"created_at"`
}

// Item represents a cached item with expiration
type Item struct {
	Object     interface{}
	Expiration int64
}

// CacheStats tracks various cache metrics
type CacheStats struct {
	Hits         int64 // Number of cache hits
	Misses       int64 // Number of cache misses
	Sets         int64 // Number of cache sets
	Deletes      int64 // Number of manual deletes
	Evictions    int64 // Number of automatic evictions
	TotalLookups int64 // Total number of Get operations
	ItemCount    int64 // Current number of items in cache
	mu           sync.RWMutex
}

// Cache is a custom cache implementation with expiry
type Cache struct {
	items             sync.Map // Using sync.Map instead of map with mutex
	defaultExpiration time.Duration
	cleanupInterval   time.Duration
	stringBuilderPool sync.Pool
	stop              chan bool
	Stats             *CacheStats
}

// New creates a new cache with default expiration and cleanup interval
func NewCache(defaultExpiration, cleanupInterval time.Duration) *Cache {
	cache := &Cache{
		defaultExpiration: defaultExpiration,
		cleanupInterval:   cleanupInterval,
		stringBuilderPool: sync.Pool{
			New: func() interface{} {
				return new(strings.Builder)
			},
		},
		stop:  make(chan bool),
		Stats: &CacheStats{},
	}

	// Start cleanup routine if interval > 0
	if cleanupInterval > 0 {
		go cache.startGC()
	}

	return cache
}

// startGC starts the garbage collection process
func (c *Cache) startGC() {
	ticker := time.NewTicker(c.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.deleteExpired()
		case <-c.stop:
			return
		}
	}
}

// Stop cache cleanup routine
func (c *Cache) Stop() {
	close(c.stop)
}

// Get retrieves an item from the cache and reports whether it was found
func (c *Cache) Get(key string) (interface{}, bool) {
	// Update total lookup counter
	c.Stats.mu.Lock()
	c.Stats.TotalLookups++
	c.Stats.mu.Unlock()

	// Get the string builder from the pool
	sb := c.stringBuilderPool.Get().(*strings.Builder)
	sb.WriteString(key)
	k := sb.String()
	sb.Reset()
	c.stringBuilderPool.Put(sb)

	itemObj, found := c.items.Load(k)
	if !found {
		// Update miss counter
		c.Stats.mu.Lock()
		c.Stats.Misses++
		c.Stats.mu.Unlock()
		return nil, false
	}

	item := itemObj.(Item)

	// Check if the item has expired
	if item.Expiration > 0 && time.Now().UnixNano() > item.Expiration {
		// Delete the item if it's expired
		c.items.Delete(k)

		// Update eviction counter
		c.Stats.mu.Lock()
		c.Stats.Evictions++
		c.Stats.ItemCount--
		c.Stats.Misses++
		c.Stats.mu.Unlock()

		return nil, false
	}

	// Update hit counter
	c.Stats.mu.Lock()
	c.Stats.Hits++
	c.Stats.mu.Unlock()

	return item.Object, true
}

// Set adds an item to the cache with the specified expiration time
func (c *Cache) Set(key string, value interface{}, duration time.Duration) {
	// Get expiration time
	var expiration int64
	if duration == 0 {
		duration = c.defaultExpiration
	}
	if duration > 0 {
		expiration = time.Now().Add(duration).UnixNano()
	}

	// Get the string builder from the pool
	sb := c.stringBuilderPool.Get().(*strings.Builder)
	sb.WriteString(key)
	k := sb.String()
	sb.Reset()
	c.stringBuilderPool.Put(sb)

	// Check if the key exists before setting
	_, exists := c.items.Load(k)

	// Store the item
	c.items.Store(k, Item{
		Object:     value,
		Expiration: expiration,
	})

	// Update stats
	c.Stats.mu.Lock()
	c.Stats.Sets++
	if !exists {
		c.Stats.ItemCount++
	}
	c.Stats.mu.Unlock()
}

// Delete removes an item from the cache
func (c *Cache) Delete(key string) {
	// Get the string builder from the pool
	sb := c.stringBuilderPool.Get().(*strings.Builder)
	sb.WriteString(key)
	k := sb.String()
	sb.Reset()
	c.stringBuilderPool.Put(sb)

	// Check if key exists before deleting
	_, exists := c.items.Load(k)
	if exists {
		c.items.Delete(k)

		// Update stats
		c.Stats.mu.Lock()
		c.Stats.Deletes++
		c.Stats.ItemCount--
		c.Stats.mu.Unlock()
	}
}

// deleteExpired removes all expired items from the cache
func (c *Cache) deleteExpired() {
	now := time.Now().UnixNano()
	var evictionCount int64

	// With sync.Map we need to iterate over all items
	c.items.Range(func(k, v interface{}) bool {
		item := v.(Item)
		if item.Expiration > 0 && now > item.Expiration {
			c.items.Delete(k)
			evictionCount++
		}
		return true
	})

	// Update stats if any items were evicted
	if evictionCount > 0 {
		c.Stats.mu.Lock()
		c.Stats.Evictions += evictionCount
		c.Stats.ItemCount -= evictionCount
		c.Stats.mu.Unlock()
	}
}

// GetStats returns a snapshot of current cache statistics
func (c *Cache) GetStats() map[string]interface{} {
	c.Stats.mu.RLock()
	defer c.Stats.mu.RUnlock()

	hitRate := float64(0)
	if c.Stats.TotalLookups > 0 {
		hitRate = float64(c.Stats.Hits) / float64(c.Stats.TotalLookups) * 100
	}

	// Count items for more accurate item count with sync.Map
	var itemCount int64
	c.items.Range(func(k, v interface{}) bool {
		itemCount++
		return true
	})

	return map[string]interface{}{
		"hits":          c.Stats.Hits,
		"misses":        c.Stats.Misses,
		"sets":          c.Stats.Sets,
		"deletes":       c.Stats.Deletes,
		"evictions":     c.Stats.Evictions,
		"total_lookups": c.Stats.TotalLookups,
		"item_count":    itemCount, // Use the calculated item count
		"hit_rate":      hitRate,
	}
}

// EstimateSize estimates the memory usage of the cache (approximate)
func (c *Cache) EstimateSize() int64 {
	var count int64
	c.items.Range(func(k, v interface{}) bool {
		count++
		return true
	})

	// This is a very rough approximation - real memory usage depends on the objects stored
	return count * 64 // Assuming 64 bytes per item on average
}

// TestStringBuilder tests the string builder pool performance
func (c *Cache) TestStringBuilder(key string, iterations int) time.Duration {
	start := time.Now()

	for i := 0; i < iterations; i++ {
		// Get the string builder from the pool
		sb := c.stringBuilderPool.Get().(*strings.Builder)
		sb.WriteString(key)
		_ = sb.String()
		sb.Reset()
		c.stringBuilderPool.Put(sb)
	}

	return time.Since(start)
}

// Global cache instance
var dataCache *Cache

func main() {
	// Set the maximum number of CPUs to use to 2
	runtime.GOMAXPROCS(2)

	// Initialize custom cache with default expiration of 5 minutes and cleanup interval of 10 minutes
	dataCache = NewCache(5*time.Minute, 10*time.Minute)

	app := fiber.New()

	// Create route - store data in cache
	app.Post("/create", createHandler)

	// Get route - retrieve data from cache
	app.Get("/get/:id", getHandler)

	// Stats route - get cache statistics
	app.Get("/stats", statsHandler)

	// New endpoint to test string builder pool
	app.Get("/test-string-builder", testStringBuilderHandler)

	// Start server
	fmt.Println("Server starting on port 3000...")
	err := app.Listen(":3000")
	if err != nil {
		panic(err)
	}
}

// createHandler handles storing data in the cache
func createHandler(c *fiber.Ctx) error {
	// Parse the incoming user data
	user := new(User)
	if err := c.BodyParser(user); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Cannot parse JSON",
		})
	}

	// Validate user data
	if user.ID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "User ID is required",
		})
	}

	// Set creation time
	user.CreateAt = time.Now()

	// Get expiration time from query parameter, default to 5 minutes
	expiryParam := c.Query("expiry", "300")
	expiry, err := time.ParseDuration(expiryParam + "s")
	if err != nil {
		expiry = 5 * time.Minute
	}

	// Store the user struct directly in the cache
	dataCache.Set(user.ID, user, expiry)

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"message": "User cached successfully",
		"expiry":  expiry.String(),
		"user":    user,
	})
}

// getHandler retrieves data from the cache
func getHandler(c *fiber.Ctx) error {
	// Get the user ID from URL parameter
	id := c.Params("id")
	if id == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "User ID is required",
		})
	}

	// Try to get the user from cache
	userData, found := dataCache.Get(id)
	if !found {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"message": "User not found in cache",
		})
	}

	// Type assert back to User struct
	user, ok := userData.(*User)
	if !ok {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Cached data is not a valid User",
		})
	}

	return c.JSON(fiber.Map{
		"message": "User retrieved from cache",
		"user":    user,
	})
}

// statsHandler returns cache statistics
func statsHandler(c *fiber.Ctx) error {
	stats := dataCache.GetStats()

	// Add estimated memory usage
	stats["estimated_memory_bytes"] = dataCache.EstimateSize()

	// Add hit rate summary description
	hitRate := stats["hit_rate"].(float64)
	var hitRateSummary string
	switch {
	case hitRate >= 90:
		hitRateSummary = "Excellent - Very efficient cache usage"
	case hitRate >= 70:
		hitRateSummary = "Good - Cache is working well"
	case hitRate >= 50:
		hitRateSummary = "Fair - Consider increasing cache size or TTL"
	default:
		hitRateSummary = "Poor - Cache may be undersized or TTL too short"
	}
	stats["hit_rate_summary"] = hitRateSummary

	return c.JSON(fiber.Map{
		"message": "Cache statistics",
		"stats":   stats,
	})
}

// testStringBuilderHandler tests the string builder pool performance
func testStringBuilderHandler(c *fiber.Ctx) error {
	// Get parameters from query
	key := c.Query("key", "test-key")
	iterationsStr := c.Query("iterations", "100000")

	// Parse iterations
	iterations := 100000
	fmt.Sscanf(iterationsStr, "%d", &iterations)

	// Run the test
	duration := dataCache.TestStringBuilder(key, iterations)

	// Calculate operations per second
	opsPerSecond := float64(iterations) / duration.Seconds()

	return c.JSON(fiber.Map{
		"message": "String builder pool test completed",
		"test_details": fiber.Map{
			"key":                key,
			"iterations":         iterations,
			"duration_ms":        duration.Milliseconds(),
			"duration_ns_per_op": duration.Nanoseconds() / int64(iterations),
			"ops_per_second":     opsPerSecond,
		},
	})
}
