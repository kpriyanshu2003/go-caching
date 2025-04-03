package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// This program is specifically designed to identify weaknesses in the cache
// implementation by applying extreme stress patterns.

var (
	baseURL      = "http://localhost:3000"
	concurrency  = 500
	testDuration = 30 * time.Second
	reportPeriod = 5 * time.Second
)

// User represents a user for testing
type User struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Age   int    `json:"age"`
	Data  string `json:"data"` // Extra large field for memory pressure
}

// Counters tracks operations
type Counters struct {
	Creates      int64
	Reads        int64
	Failures     int64
	Successes    int64
	TotalLatency int64
	Requests     int64
}

// Pattern represents a specific test pattern designed to break the cache
type Pattern struct {
	Name        string
	Description string
	RunFunc     func(ctx context.Context, wg *sync.WaitGroup, counters *Counters)
}

// generateRandomString generates a string of specified size
func generateRandomString(size int) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	b := make([]byte, size)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

// generateUser creates a user with random data of specified size in KB
func generateUser(sizeKB int) User {
	return User{
		ID:    fmt.Sprintf("user-%d", rand.Int63()),
		Name:  fmt.Sprintf("User %d", rand.Intn(10000)),
		Email: fmt.Sprintf("user%d@example.com", rand.Intn(100000)),
		Age:   18 + rand.Intn(80),
		Data:  generateRandomString(sizeKB * 1024),
	}
}

// createUser creates a user with data of specified size
func createUser(sizeKB int, counters *Counters) string {
	user := generateUser(sizeKB)
	jsonData, err := json.Marshal(user)
	if err != nil {
		atomic.AddInt64(&counters.Failures, 1)
		return ""
	}

	start := time.Now()
	resp, err := http.Post(
		baseURL+"/create",
		"application/json",
		bytes.NewBuffer(jsonData),
	)
	latency := time.Since(start)

	atomic.AddInt64(&counters.Requests, 1)
	atomic.AddInt64(&counters.TotalLatency, latency.Microseconds())
	atomic.AddInt64(&counters.Creates, 1)

	if err != nil || resp.StatusCode != http.StatusCreated {
		atomic.AddInt64(&counters.Failures, 1)
		if resp != nil {
			resp.Body.Close()
		}
		return ""
	}

	atomic.AddInt64(&counters.Successes, 1)
	resp.Body.Close()
	return user.ID
}

// getUser retrieves a user by ID
func getUser(id string, counters *Counters) bool {
	start := time.Now()
	resp, err := http.Get(baseURL + "/get/" + id)
	latency := time.Since(start)

	atomic.AddInt64(&counters.Requests, 1)
	atomic.AddInt64(&counters.TotalLatency, latency.Microseconds())
	atomic.AddInt64(&counters.Reads, 1)

	if err != nil || (resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound) {
		atomic.AddInt64(&counters.Failures, 1)
		if resp != nil {
			resp.Body.Close()
		}
		return false
	}

	atomic.AddInt64(&counters.Successes, 1)
	resp.Body.Close()
	return true
}

// printStats outputs current test statistics
func printStats(counters *Counters, elapsed time.Duration, pattern string) {
	requests := atomic.LoadInt64(&counters.Requests)
	creates := atomic.LoadInt64(&counters.Creates)
	reads := atomic.LoadInt64(&counters.Reads)
	successes := atomic.LoadInt64(&counters.Successes)
	failures := atomic.LoadInt64(&counters.Failures)
	latency := atomic.LoadInt64(&counters.TotalLatency)

	rps := float64(requests) / elapsed.Seconds()
	avgLatency := float64(0)
	if requests > 0 {
		avgLatency = float64(latency) / float64(requests) / 1000 // convert to ms
	}

	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	fmt.Printf("\n=== Pattern: %s ===\n", pattern)
	fmt.Printf("Elapsed: %.1fs\n", elapsed.Seconds())
	fmt.Printf("Requests: %d (%.1f/sec)\n", requests, rps)
	fmt.Printf("Operations: %d creates, %d reads\n", creates, reads)
	fmt.Printf("Results: %d successes (%.1f%%), %d failures (%.1f%%)\n",
		successes, float64(successes)/float64(requests)*100,
		failures, float64(failures)/float64(requests)*100)
	fmt.Printf("Average Latency: %.2f ms\n", avgLatency)
	fmt.Printf("Memory Usage: %.1f MB\n", float64(m.Alloc)/1024/1024)

	// Calculate failure rate
	failureRate := float64(0)
	if requests > 0 {
		failureRate = float64(failures) / float64(requests) * 100
	}
	fmt.Printf("Failure Rate: %.2f%%\n", failureRate)
}

// resetCounters resets all counters to zero
func resetCounters() *Counters {
	return &Counters{}
}

// availablePatterns returns all test patterns
func availablePatterns() []Pattern {
	return []Pattern{
		{
			Name:        "HighWriteVolume",
			Description: "Massive write operations with minimal reads",
			RunFunc:     patternHighWriteVolume,
		},
		{
			Name:        "LargeObjects",
			Description: "Store very large objects in the cache",
			RunFunc:     patternLargeObjects,
		},
		{
			Name:        "ConcurrentHotKey",
			Description: "Many concurrent operations on the same keys",
			RunFunc:     patternConcurrentHotKey,
		},
		{
			Name:        "ExpireFlood",
			Description: "Create items that expire simultaneously",
			RunFunc:     patternExpireFlood,
		},
		{
			Name:        "ReadWriteChurn",
			Description: "Rapid alternating reads and writes",
			RunFunc:     patternReadWriteChurn,
		},
		{
			Name:        "ProgressiveLoadUncached",
			Description: "Progressive load increase with uncached reads until 100% failure",
			RunFunc:     patternProgressiveLoadUncached,
		},
		{
			Name:        "ProgressiveLoadCached",
			Description: "Progressive load increase with cached reads until 100% failure",
			RunFunc:     patternProgressiveLoadCached,
		},
		{
			Name:        "ProgressiveLoadMixed",
			Description: "Progressive load increase with 70% reads/30% writes until 100% failure",
			RunFunc:     patternProgressiveLoadMixed,
		},
	}
}

// patternHighWriteVolume performs continuous rapid writes
func patternHighWriteVolume(ctx context.Context, wg *sync.WaitGroup, counters *Counters) {
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
					createUser(1, counters) // 1KB data
				}
			}
		}()
	}
}

// patternLargeObjects stores very large objects in the cache
func patternLargeObjects(ctx context.Context, wg *sync.WaitGroup, counters *Counters) {
	sizes := []int{50, 100, 250, 500, 1000} // KB sizes

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
					size := sizes[rand.Intn(len(sizes))]
					createUser(size, counters)
					time.Sleep(100 * time.Millisecond) // Small delay due to large payloads
				}
			}
		}()
	}
}

// patternConcurrentHotKey focuses many operations on the same keys
func patternConcurrentHotKey(ctx context.Context, wg *sync.WaitGroup, counters *Counters) {
	// First create a small set of users
	userIDs := make([]string, 10)
	for i := range userIDs {
		userIDs[i] = createUser(1, counters)
	}

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
					// Get random user from our hot set
					if len(userIDs) > 0 {
						id := userIDs[rand.Intn(len(userIDs))]
						getUser(id, counters)
					}
				}
			}
		}()
	}
}

// patternExpireFlood creates many items that expire at the same time
func patternExpireFlood(ctx context.Context, wg *sync.WaitGroup, counters *Counters) {
	// First, create a batch of users with short expiry
	for i := 0; i < 1000; i++ {
		user := generateUser(5)
		jsonData, _ := json.Marshal(user)

		// Use a query parameter to set very short expiry
		req, _ := http.NewRequest("POST", baseURL+"/create?expiry=1", bytes.NewBuffer(jsonData))
		req.Header.Set("Content-Type", "application/json")

		client := &http.Client{}
		resp, err := client.Do(req)

		atomic.AddInt64(&counters.Requests, 1)
		atomic.AddInt64(&counters.Creates, 1)

		if err != nil || resp.StatusCode != http.StatusCreated {
			atomic.AddInt64(&counters.Failures, 1)
		} else {
			atomic.AddInt64(&counters.Successes, 1)
			resp.Body.Close()
		}
	}

	// Wait for a second to let them expire
	time.Sleep(1100 * time.Millisecond)

	// Now flood with gets for those users
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
					id := fmt.Sprintf("user-%d", index*1000+rand.Intn(1000))
					getUser(id, counters)
				}
			}
		}(i)
	}
}

// patternReadWriteChurn rapidly alternates between reads and writes
func patternReadWriteChurn(ctx context.Context, wg *sync.WaitGroup, counters *Counters) {
	var userIDs sync.Map

	// Write workers
	for i := 0; i < concurrency/2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
					id := createUser(10, counters)
					if id != "" {
						userIDs.Store(id, true)
					}
				}
			}
		}()
	}

	// Read workers
	for i := 0; i < concurrency/2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Collector for IDs
			var ids []string

			for {
				select {
				case <-ctx.Done():
					return
				default:
					// Periodically update our ID list from the map
					if rand.Intn(100) < 5 || len(ids) == 0 {
						ids = make([]string, 0)
						userIDs.Range(func(key, value interface{}) bool {
							ids = append(ids, key.(string))
							return len(ids) < 1000 // Limit collection size
						})
					}

					if len(ids) > 0 {
						id := ids[rand.Intn(len(ids))]
						getUser(id, counters)
					}
				}
			}
		}()
	}
}

// patternProgressiveLoadUncached progressively increases load with uncached reads until 100% failure
func patternProgressiveLoadUncached(ctx context.Context, wg *sync.WaitGroup, counters *Counters) {
	// Shared control for workers to monitor failure rate and coordination
	var (
		workers     int64      = 10    // Start with 10 workers
		targetLoad  int64              // Target load shared among all workers
		failureRate float64            // Current failure rate
		mu          sync.Mutex         // Protect shared data
		stopWorkers bool       = false // Flag to stop all workers
		checkPeriod            = 1 * time.Second
	)

	// Background goroutine to monitor failure rate and adjust load
	wg.Add(1)
	go func() {
		defer wg.Done()

		ticker := time.NewTicker(checkPeriod)
		defer ticker.Stop()

		lastFailures := int64(0)
		lastRequests := int64(0)

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				currentFailures := atomic.LoadInt64(&counters.Failures)
				currentRequests := atomic.LoadInt64(&counters.Requests)

				// Calculate new failures and requests in this period
				periodFailures := currentFailures - lastFailures
				periodRequests := currentRequests - lastRequests

				// Calculate failure rate for this period
				currentFailureRate := float64(0)
				if periodRequests > 0 {
					currentFailureRate = float64(periodFailures) / float64(periodRequests) * 100
				}

				// Update tracking variables
				lastFailures = currentFailures
				lastRequests = currentRequests

				mu.Lock()
				failureRate = currentFailureRate

				// If failure rate is below 95%, increase workers
				if failureRate < 95 {
					// Increase by 10% each time
					newWorkers := workers + (workers / 10)
					if newWorkers == workers {
						newWorkers++
					}
					fmt.Printf("Increasing workers from %d to %d (failure rate: %.2f%%)\n",
						workers, newWorkers, failureRate)

					// Atomically add new workers
					atomic.StoreInt64(&targetLoad, newWorkers)
					atomic.StoreInt64(&workers, newWorkers)
				} else if failureRate >= 99 {
					// We've reached our goal of ~100% failure
					fmt.Printf("Target reached! Failure rate: %.2f%% with %d workers\n",
						failureRate, workers)
					stopWorkers = true
				}
				mu.Unlock()

				// If we've reached 100% failure, we can stop the test
				if stopWorkers {
					return
				}
			}
		}
	}()

	// Worker pool manager - continuously adjusts number of active workers
	wg.Add(1)
	go func() {
		defer wg.Done()

		// Set of active worker channels
		activeWorkers := make(map[int]chan bool)
		workerCounter := 0

		// Ticker to check if we need to adjust worker count
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				// Stop all workers
				for _, ch := range activeWorkers {
					close(ch)
				}
				return
			case <-ticker.C:
				mu.Lock()
				target := atomic.LoadInt64(&targetLoad)
				stopped := stopWorkers
				mu.Unlock()

				// If we need to stop all workers
				if stopped {
					for _, ch := range activeWorkers {
						close(ch)
					}
					return
				}

				// Add workers if needed
				currentCount := int64(len(activeWorkers))
				for currentCount < target {
					stopCh := make(chan bool)
					workerID := workerCounter
					workerCounter++

					// Start a new worker
					wg.Add(1)
					go func(id int, stopCh chan bool) {
						defer wg.Done()
						for {
							select {
							case <-stopCh:
								return
							case <-ctx.Done():
								return
							default:
								// Always generate a random ID that won't be in cache
								randomID := fmt.Sprintf("nonexistent-%d-%d", id, rand.Int63())
								getUser(randomID, counters)
							}
						}
					}(workerID, stopCh)

					activeWorkers[workerID] = stopCh
					currentCount++
				}

				// Remove workers if needed (should not happen in this pattern)
				if currentCount > target {
					// Find workers to remove
					toRemove := currentCount - target
					for id, ch := range activeWorkers {
						if toRemove <= 0 {
							break
						}
						close(ch)
						delete(activeWorkers, id)
						toRemove--
					}
				}
			}
		}
	}()
}

// patternProgressiveLoadCached progressively increases load with cached reads until 100% failure
func patternProgressiveLoadCached(ctx context.Context, wg *sync.WaitGroup, counters *Counters) {
	// First, create a fixed set of users to be used for cache hits
	const numCachedUsers = 100
	cachedUserIDs := make([]string, 0, numCachedUsers)

	// Create users with 1-hour expiry
	for i := 0; i < numCachedUsers; i++ {
		user := generateUser(5) // 5KB data size
		jsonData, _ := json.Marshal(user)

		req, _ := http.NewRequest("POST", baseURL+"/create?expiry=3600", bytes.NewBuffer(jsonData))
		req.Header.Set("Content-Type", "application/json")

		client := &http.Client{}
		resp, err := client.Do(req)

		if err == nil && resp.StatusCode == http.StatusCreated {
			cachedUserIDs = append(cachedUserIDs, user.ID)
			resp.Body.Close()
		}
	}

	// Wait to ensure all users are properly cached
	time.Sleep(500 * time.Millisecond)

	// Shared control for workers to monitor failure rate and coordination
	var (
		workers     int64      = 10    // Start with 10 workers
		targetLoad  int64              // Target load shared among all workers
		failureRate float64            // Current failure rate
		mu          sync.Mutex         // Protect shared data
		stopWorkers bool       = false // Flag to stop all workers
		checkPeriod            = 1 * time.Second
	)

	// Background goroutine to monitor failure rate and adjust load
	wg.Add(1)
	go func() {
		defer wg.Done()

		ticker := time.NewTicker(checkPeriod)
		defer ticker.Stop()

		lastFailures := int64(0)
		lastRequests := int64(0)

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				currentFailures := atomic.LoadInt64(&counters.Failures)
				currentRequests := atomic.LoadInt64(&counters.Requests)

				// Calculate new failures and requests in this period
				periodFailures := currentFailures - lastFailures
				periodRequests := currentRequests - lastRequests

				// Calculate failure rate for this period
				currentFailureRate := float64(0)
				if periodRequests > 0 {
					currentFailureRate = float64(periodFailures) / float64(periodRequests) * 100
				}

				// Update tracking variables
				lastFailures = currentFailures
				lastRequests = currentRequests

				mu.Lock()
				failureRate = currentFailureRate

				// If failure rate is below 95%, increase workers
				if failureRate < 95 {
					// Increase faster as this is cache-focused
					newWorkers := workers + (workers / 5) // 20% increase
					if newWorkers == workers {
						newWorkers += 5
					}
					fmt.Printf("Increasing workers from %d to %d (failure rate: %.2f%%)\n",
						workers, newWorkers, failureRate)

					// Atomically add new workers
					atomic.StoreInt64(&targetLoad, newWorkers)
					atomic.StoreInt64(&workers, newWorkers)
				} else if failureRate >= 99 {
					// We've reached our goal of ~100% failure
					fmt.Printf("Target reached! Failure rate: %.2f%% with %d workers\n",
						failureRate, workers)
					stopWorkers = true
				}
				mu.Unlock()

				// If we've reached 100% failure, we can stop the test
				if stopWorkers {
					return
				}
			}
		}
	}()

	// Worker pool manager - continuously adjusts number of active workers
	wg.Add(1)
	go func() {
		defer wg.Done()

		// Set of active worker channels
		activeWorkers := make(map[int]chan bool)
		workerCounter := 0

		// Ticker to check if we need to adjust worker count
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				// Stop all workers
				for _, ch := range activeWorkers {
					close(ch)
				}
				return
			case <-ticker.C:
				mu.Lock()
				target := atomic.LoadInt64(&targetLoad)
				stopped := stopWorkers
				mu.Unlock()

				// If we need to stop all workers
				if stopped {
					for _, ch := range activeWorkers {
						close(ch)
					}
					return
				}

				// Add workers if needed
				currentCount := int64(len(activeWorkers))
				for currentCount < target {
					stopCh := make(chan bool)
					workerID := workerCounter
					workerCounter++

					// Start a new worker
					wg.Add(1)
					go func(id int, stopCh chan bool) {
						defer wg.Done()
						for {
							select {
							case <-stopCh:
								return
							case <-ctx.Done():
								return
							default:
								// Get a cached user ID
								if len(cachedUserIDs) > 0 {
									userID := cachedUserIDs[rand.Intn(len(cachedUserIDs))]
									getUser(userID, counters)
								}
							}
						}
					}(workerID, stopCh)

					activeWorkers[workerID] = stopCh
					currentCount++
				}

				// Remove workers if needed (should not happen in this pattern)
				if currentCount > target {
					// Find workers to remove
					toRemove := currentCount - target
					for id, ch := range activeWorkers {
						if toRemove <= 0 {
							break
						}
						close(ch)
						delete(activeWorkers, id)
						toRemove--
					}
				}
			}
		}
	}()
}

func patternProgressiveLoadMixed(ctx context.Context, wg *sync.WaitGroup, counters *Counters) {
	// Maintain a pool of user IDs for read operations
	var userIDs sync.Map

	// Shared control for workers to monitor failure rate and coordination
	var (
		workers     int64      = 10    // Start with 10 workers
		targetLoad  int64              // Target load shared among all workers
		failureRate float64            // Current failure rate
		mu          sync.Mutex         // Protect shared data
		stopWorkers bool       = false // Flag to stop all workers
		checkPeriod            = 1 * time.Second
	)

	// Background goroutine to monitor failure rate and adjust load
	wg.Add(1)
	go func() {
		defer wg.Done()

		ticker := time.NewTicker(checkPeriod)
		defer ticker.Stop()

		lastFailures := int64(0)
		lastRequests := int64(0)

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				currentFailures := atomic.LoadInt64(&counters.Failures)
				currentRequests := atomic.LoadInt64(&counters.Requests)

				// Calculate new failures and requests in this period
				periodFailures := currentFailures - lastFailures
				periodRequests := currentRequests - lastRequests

				// Calculate failure rate for this period
				currentFailureRate := float64(0)
				if periodRequests > 0 {
					currentFailureRate = float64(periodFailures) / float64(periodRequests) * 100
				}

				// Update tracking variables
				lastFailures = currentFailures
				lastRequests = currentRequests

				mu.Lock()
				failureRate = currentFailureRate

				// If failure rate is below 95%, increase workers
				if failureRate < 95 {
					// Increase by 15% each time
					newWorkers := workers + (workers * 15 / 100)
					if newWorkers == workers {
						newWorkers += 2
					}
					fmt.Printf("Increasing workers from %d to %d (failure rate: %.2f%%)\n",
						workers, newWorkers, failureRate)

					// Atomically add new workers
					atomic.StoreInt64(&targetLoad, newWorkers)
					atomic.StoreInt64(&workers, newWorkers)
				} else if failureRate >= 99 {
					// We've reached our goal of ~100% failure
					fmt.Printf("Target reached! Failure rate: %.2f%% with %d workers\n",
						failureRate, workers)
					stopWorkers = true
				}
				mu.Unlock()

				// If we've reached 100% failure, we can stop the test
				if stopWorkers {
					return
				}
			}
		}
	}()

	// Worker pool manager - continuously adjusts number of active workers
	wg.Add(1)
	go func() {
		defer wg.Done()

		// Set of active worker channels
		activeWorkers := make(map[int]chan bool)
		workerCounter := 0

		// Ticker to check if we need to adjust worker count
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				// Stop all workers
				for _, ch := range activeWorkers {
					close(ch)
				}
				return
			case <-ticker.C:
				mu.Lock()
				target := atomic.LoadInt64(&targetLoad)
				stopped := stopWorkers
				mu.Unlock()

				// If we need to stop all workers
				if stopped {
					for _, ch := range activeWorkers {
						close(ch)
					}
					return
				}

				// Add workers if needed
				currentCount := int64(len(activeWorkers))
				for currentCount < target {
					stopCh := make(chan bool)
					workerID := workerCounter
					workerCounter++

					// Determine if this worker should be a reader or writer
					// 70% readers, 30% writers
					isReader := rand.Intn(100) < 70

					// Start a new worker
					wg.Add(1)
					go func(id int, isReader bool, stopCh chan bool) {
						defer wg.Done()

						// Keep track of IDs we create if we're a writer
						var myCreatedIDs []string

						// Collector for IDs
						var idCache []string

						for {
							select {
							case <-stopCh:
								return
							case <-ctx.Done():
								return
							default:
								if isReader {
									// Reader: Try to get a random ID from the pool
									if rand.Intn(100) < 5 || len(idCache) == 0 {
										// Refresh our ID cache
										idCache = make([]string, 0)
										userIDs.Range(func(key, value interface{}) bool {
											idCache = append(idCache, key.(string))
											return len(idCache) < 1000 // Limit collection size
										})
									}

									if len(idCache) > 0 {
										// Get a random ID from our cache
										id := idCache[rand.Intn(len(idCache))]
										getUser(id, counters)
									} else if len(myCreatedIDs) > 0 {
										// Fall back to our own created IDs if global pool is empty
										id := myCreatedIDs[rand.Intn(len(myCreatedIDs))]
										getUser(id, counters)
									} else {
										// If no IDs are available yet, create one
										id := createUser(5, counters)
										if id != "" {
											userIDs.Store(id, true)
											myCreatedIDs = append(myCreatedIDs, id)
										}
									}
								} else {
									// Writer: Create a new user
									id := createUser(5, counters) // 5KB data
									if id != "" {
										userIDs.Store(id, true)

										// Also track it in our local list
										myCreatedIDs = append(myCreatedIDs, id)
										if len(myCreatedIDs) > 100 {
											// Keep the list from growing too large
											myCreatedIDs = myCreatedIDs[len(myCreatedIDs)-100:]
										}
									}
								}
							}
						}
					}(workerID, isReader, stopCh)

					activeWorkers[workerID] = stopCh
					currentCount++
				}

				// Remove workers if needed (should not happen in this pattern)
				if currentCount > target {
					// Find workers to remove
					toRemove := currentCount - target
					for id, ch := range activeWorkers {
						if toRemove <= 0 {
							break
						}
						close(ch)
						delete(activeWorkers, id)
						toRemove--
					}
				}
			}
		}
	}()
}

func main() {
	// Enable more OS threads
	runtime.GOMAXPROCS(runtime.NumCPU() * 2)

	// Seed the random number generator
	rand.Seed(time.Now().UnixNano())

	patterns := availablePatterns()

	fmt.Println("Cache Breaking Test Tool")
	fmt.Println("======================")
	fmt.Println("This tool attempts to identify weaknesses in your cache by applying various stress patterns")

	// Get the server stats before we start
	resp, err := http.Get(baseURL + "/stats")
	if err == nil {
		fmt.Println("\nInitial server stats:")
		defer resp.Body.Close()
		var result map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&result)
		fmt.Printf("%+v\n", result)
	}

	fmt.Println("\nRunning", len(patterns), "test patterns...")

	// Run each pattern sequentially
	for _, pattern := range patterns {
		fmt.Printf("\n\nRunning pattern: %s - %s\n", pattern.Name, pattern.Description)
		fmt.Printf("Duration: %s, Concurrency: %d\n", testDuration, concurrency)

		// Create a context with timeout for this pattern
		ctx, cancel := context.WithTimeout(context.Background(), testDuration)

		counters := resetCounters()
		var wg sync.WaitGroup

		// Start reporting goroutine
		var reportWg sync.WaitGroup
		reportWg.Add(1)
		go func() {
			defer reportWg.Done()
			start := time.Now()
			ticker := time.NewTicker(reportPeriod)
			defer ticker.Stop()

			for {
				select {
				case <-ticker.C:
					elapsed := time.Since(start)
					printStats(counters, elapsed, pattern.Name)
				case <-ctx.Done():
					elapsed := time.Since(start)
					printStats(counters, elapsed, pattern.Name)
					return
				}
			}
		}()

		// Run the pattern
		pattern.RunFunc(ctx, &wg, counters)

		// Wait for the timeout
		<-ctx.Done()
		cancel()

		// Wait for all workers to finish
		wg.Wait()
		reportWg.Wait()

		// Get the server stats after this pattern
		resp, err := http.Get(baseURL + "/stats")
		if err == nil {
			fmt.Printf("\nServer stats after pattern %s:\n", pattern.Name)
			defer resp.Body.Close()
			var result map[string]interface{}
			json.NewDecoder(resp.Body).Decode(&result)
			fmt.Printf("%+v\n", result)
		}

		// Give the server a moment to recover before the next pattern
		time.Sleep(2 * time.Second)
	}

	fmt.Println("\nAll patterns completed!")
}
