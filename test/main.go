package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// TestConfig defines load test parameters
type TestConfig struct {
	BaseURL        string
	Duration       time.Duration
	Concurrency    int
	ReadRatio      float64 // ratio of read vs write operations (0.7 = 70% reads, 30% writes)
	RampUpTime     time.Duration
	ReportInterval time.Duration
}

// Statistics tracks performance metrics
type Statistics struct {
	TotalRequests      int64
	SuccessfulRequests int64
	FailedRequests     int64
	CreateRequests     int64
	GetRequests        int64
	StartTime          time.Time
	mu                 sync.RWMutex
	RequestTimes       []time.Duration
	ErrorCodes         map[int]int
}

// User represents a user for testing
type User struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Age   int    `json:"age"`
}

// Error response format
type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// generateRandomUser creates a random user for testing
func generateRandomUser() User {
	return User{
		ID:    fmt.Sprintf("user-%d", rand.Intn(1000000)),
		Name:  fmt.Sprintf("User %d", rand.Intn(10000)),
		Email: fmt.Sprintf("user%d@example.com", rand.Intn(100000)),
		Age:   18 + rand.Intn(80),
	}
}

// createUser sends a create user request
func createUser(baseURL string, stats *Statistics) {
	user := generateRandomUser()
	jsonData, err := json.Marshal(user)
	if err != nil {
		atomic.AddInt64(&stats.FailedRequests, 1)
		return
	}

	startTime := time.Now()
	resp, err := http.Post(
		baseURL+"/create",
		"application/json",
		bytes.NewBuffer(jsonData),
	)
	requestTime := time.Since(startTime)

	atomic.AddInt64(&stats.CreateRequests, 1)
	atomic.AddInt64(&stats.TotalRequests, 1)

	if err != nil {
		atomic.AddInt64(&stats.FailedRequests, 1)
		return
	}
	defer resp.Body.Close()

	stats.mu.Lock()
	stats.RequestTimes = append(stats.RequestTimes, requestTime)
	stats.ErrorCodes[resp.StatusCode]++
	stats.mu.Unlock()

	if resp.StatusCode != http.StatusCreated {
		atomic.AddInt64(&stats.FailedRequests, 1)
		return
	}

	atomic.AddInt64(&stats.SuccessfulRequests, 1)
}

// getUser sends a get user request
func getUser(baseURL string, stats *Statistics) {
	// Generate some ID - in a real scenario we'd use IDs we know exist
	userID := fmt.Sprintf("user-%d", rand.Intn(1000000))

	startTime := time.Now()
	resp, err := http.Get(baseURL + "/get/" + userID)
	requestTime := time.Since(startTime)

	atomic.AddInt64(&stats.GetRequests, 1)
	atomic.AddInt64(&stats.TotalRequests, 1)

	if err != nil {
		atomic.AddInt64(&stats.FailedRequests, 1)
		return
	}
	defer resp.Body.Close()

	stats.mu.Lock()
	stats.RequestTimes = append(stats.RequestTimes, requestTime)
	stats.ErrorCodes[resp.StatusCode]++
	stats.mu.Unlock()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		// NotFound is expected sometimes, so we don't count it as a failure
		atomic.AddInt64(&stats.FailedRequests, 1)
		return
	}

	atomic.AddInt64(&stats.SuccessfulRequests, 1)
}

// calculateRequestsPerSecond calculates the current requests per second
func calculateRequestsPerSecond(stats *Statistics) float64 {
	stats.mu.RLock()
	defer stats.mu.RUnlock()

	duration := time.Since(stats.StartTime).Seconds()
	if duration == 0 {
		return 0
	}
	return float64(stats.TotalRequests) / duration
}

// calculatePercentile calculates a percentile from request times
func calculatePercentile(times []time.Duration, percentile float64) time.Duration {
	if len(times) == 0 {
		return 0
	}

	// Create a copy and sort it
	timesCopy := make([]time.Duration, len(times))
	copy(timesCopy, times)

	// Sort times by duration
	for i := 0; i < len(timesCopy); i++ {
		for j := i + 1; j < len(timesCopy); j++ {
			if timesCopy[i] > timesCopy[j] {
				timesCopy[i], timesCopy[j] = timesCopy[j], timesCopy[i]
			}
		}
	}

	index := int(float64(len(timesCopy)) * percentile / 100)
	if index >= len(timesCopy) {
		index = len(timesCopy) - 1
	}
	return timesCopy[index]
}

// reportStatistics prints the current statistics
func reportStatistics(stats *Statistics, final bool) {
	stats.mu.RLock()
	times := stats.RequestTimes
	errorCodes := stats.ErrorCodes
	stats.mu.RUnlock()

	rps := calculateRequestsPerSecond(stats)

	p50 := calculatePercentile(times, 50)
	p95 := calculatePercentile(times, 95)
	p99 := calculatePercentile(times, 99)

	fmt.Println("===== Load Test Statistics =====")
	if final {
		fmt.Println("FINAL REPORT")
	}
	fmt.Printf("Total Requests: %d\n", stats.TotalRequests)
	fmt.Printf("Successful Requests: %d (%.2f%%)\n",
		stats.SuccessfulRequests,
		float64(stats.SuccessfulRequests)/float64(stats.TotalRequests)*100,
	)
	fmt.Printf("Failed Requests: %d (%.2f%%)\n",
		stats.FailedRequests,
		float64(stats.FailedRequests)/float64(stats.TotalRequests)*100,
	)
	fmt.Printf("Create Requests: %d\n", stats.CreateRequests)
	fmt.Printf("Get Requests: %d\n", stats.GetRequests)
	fmt.Printf("Current RPS: %.2f\n", rps)
	fmt.Printf("Response Times: P50=%.2fms, P95=%.2fms, P99=%.2fms\n",
		float64(p50.Microseconds())/1000,
		float64(p95.Microseconds())/1000,
		float64(p99.Microseconds())/1000,
	)

	fmt.Println("Status Code Distribution:")
	for code, count := range errorCodes {
		fmt.Printf("  HTTP %d: %d\n", code, count)
	}
	fmt.Println()
}

// runLoadTest executes the load test with the given configuration
func runLoadTest(config TestConfig) {
	stats := &Statistics{
		StartTime:  time.Now(),
		ErrorCodes: make(map[int]int),
	}

	// Calculate request interval for target RPS
	requestsPerWorker := (1000000 / config.Concurrency) // Target is 1 million RPS
	intervalPerRequest := time.Second / time.Duration(requestsPerWorker)

	fmt.Printf("Starting load test with %d concurrent workers\n", config.Concurrency)
	fmt.Printf("Target: 1,000,000 requests per second\n")
	fmt.Printf("Test duration: %s\n", config.Duration)
	fmt.Printf("Read/Write ratio: %.1f/%.1f\n", config.ReadRatio, 1-config.ReadRatio)

	// Start the report goroutine
	stopReport := make(chan bool)
	var reportWg sync.WaitGroup
	reportWg.Add(1)

	go func() {
		defer reportWg.Done()
		reportTicker := time.NewTicker(config.ReportInterval)
		defer reportTicker.Stop()

		for {
			select {
			case <-reportTicker.C:
				reportStatistics(stats, false)
			case <-stopReport:
				return
			}
		}
	}()

	// Create worker pool
	var wg sync.WaitGroup
	wg.Add(config.Concurrency)

	// Start workers
	for i := 0; i < config.Concurrency; i++ {
		// Stagger startup to avoid thundering herd
		time.Sleep(config.RampUpTime / time.Duration(config.Concurrency))

		go func(workerID int) {
			defer wg.Done()

			ticker := time.NewTicker(intervalPerRequest)
			defer ticker.Stop()

			endTime := time.Now().Add(config.Duration)

			for time.Now().Before(endTime) {
				<-ticker.C

				// Decide whether to do a read or write operation
				if rand.Float64() < config.ReadRatio {
					getUser(config.BaseURL, stats)
				} else {
					createUser(config.BaseURL, stats)
				}
			}
		}(i)
	}

	// Wait for all workers to finish
	wg.Wait()

	// Stop the reporting goroutine
	close(stopReport)
	reportWg.Wait()

	// Final report
	reportStatistics(stats, true)
}

// main is the entry point for the load test
func main() {
	// Seed the random number generator
	rand.Seed(time.Now().UnixNano())

	// Configure the load test
	config := TestConfig{
		BaseURL:        "http://localhost:3000",
		Duration:       30 * time.Second, // How long to run the test
		Concurrency:    100,              // Number of concurrent workers
		ReadRatio:      0.7,              // 70% reads, 30% writes
		RampUpTime:     5 * time.Second,  // Time to gradually ramp up to full load
		ReportInterval: 5 * time.Second,  // How often to print statistics
	}

	fmt.Println("Cache Load Testing Tool")
	fmt.Println("======================")
	fmt.Println("This tool will attempt to push your custom cache to its limits")
	fmt.Printf("Target: 1 million requests/second to %s\n", config.BaseURL)
	fmt.Println()
	fmt.Println("Starting in 3 seconds...")
	time.Sleep(3 * time.Second)

	// Run the load test
	runLoadTest(config)
}
