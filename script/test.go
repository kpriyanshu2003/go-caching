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

// Config for the load test
type Config struct {
	BaseURL          string
	TestDuration     time.Duration
	NumCreateWorkers int
	NumGetWorkers    int
	CreateRatePerSec int
	GetRatePerSec    int
	Timeout          time.Duration
}

// Stats to track during the test
type Stats struct {
	TotalRequests      int64
	SuccessfulRequests int64
	FailedRequests     int64
	CreateRequests     int64
	GetRequests        int64
	Errors             []string
	ErrorMutex         sync.Mutex
}

// User represents the data structure to cache
type User struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Email    string    `json:"email"`
	Age      int       `json:"age"`
	CreateAt time.Time `json:"created_at,omitempty"`
}

// Response from the API
type Response struct {
	Message string `json:"message"`
	User    *User  `json:"user,omitempty"`
	Expiry  string `json:"expiry,omitempty"`
	Error   string `json:"error,omitempty"`
}

func main() {
	rand.Seed(time.Now().UnixNano())

	// Configure the test
	config := Config{
		BaseURL:          "http://localhost:3000",
		TestDuration:     30 * time.Minute, // Run for 30 minutes or until failure
		NumCreateWorkers: 120,
		NumGetWorkers:    240,
		CreateRatePerSec: 10000,  // Create 100 records per second
		GetRatePerSec:    500000, // Get 500 records per second
		Timeout:          5 * time.Second,
	}

	// Create a client with a timeout
	client := &http.Client{
		Timeout: config.Timeout,
	}

	// Initialize stats
	stats := Stats{}

	// Create a channel to store user IDs for GET requests
	userIDs := make(chan string, 100000)

	// Create stop signal channels
	stopCreate := make(chan struct{})
	stopGet := make(chan struct{})
	stopStats := make(chan struct{})

	fmt.Printf("Starting load test against %s\n", config.BaseURL)
	fmt.Printf("Test will run for %v or until API fails\n", config.TestDuration)
	fmt.Printf("Creating %d records/sec with %d workers\n", config.CreateRatePerSec, config.NumCreateWorkers)
	fmt.Printf("Getting %d records/sec with %d workers\n", config.GetRatePerSec, config.NumGetWorkers)

	// Start the stats reporter
	go reportStats(&stats, stopStats)

	// Start the test timer
	testStart := time.Now()
	testEnd := testStart.Add(config.TestDuration)

	// Start create worker goroutines
	var createWg sync.WaitGroup
	createInterval := time.Second / time.Duration(config.CreateRatePerSec)
	createTicker := time.NewTicker(createInterval)

	createWg.Add(config.NumCreateWorkers)
	for i := 0; i < config.NumCreateWorkers; i++ {
		go func(workerID int) {
			defer createWg.Done()
			createWorker(client, &config, &stats, userIDs, stopCreate, createTicker.C, workerID)
		}(i)
	}

	// Start get worker goroutines after a short delay to allow some creates to happen
	time.Sleep(5 * time.Second)

	var getWg sync.WaitGroup
	getInterval := time.Second / time.Duration(config.GetRatePerSec)
	getTicker := time.NewTicker(getInterval)

	getWg.Add(config.NumGetWorkers)
	for i := 0; i < config.NumGetWorkers; i++ {
		go func(workerID int) {
			defer getWg.Done()
			getWorker(client, &config, &stats, userIDs, stopGet, getTicker.C, workerID)
		}(i)
	}

	// Monitor for test completion or failure
	apiFailure := false
	testComplete := false

	// Check for errors and test time limit in the main loop
	for !apiFailure && !testComplete {
		time.Sleep(1 * time.Second)

		// Check if test duration has been reached
		if time.Now().After(testEnd) {
			fmt.Println("\nTest duration reached, stopping test")
			testComplete = true
		}

		// Check if there are too many errors (>50% failure rate after some requests)
		if atomic.LoadInt64(&stats.TotalRequests) > 1000 &&
			float64(atomic.LoadInt64(&stats.FailedRequests))/float64(atomic.LoadInt64(&stats.TotalRequests)) > 0.5 {
			fmt.Println("\nAPI failure detected (high error rate), stopping test")
			apiFailure = true
		}
	}

	// Stop all workers
	fmt.Println("Shutting down workers...")
	close(stopCreate)
	close(stopGet)

	// Wait for workers to finish
	createWg.Wait()
	getWg.Wait()

	// Final stats report
	close(stopStats)
	time.Sleep(100 * time.Millisecond) // Give the stats reporter a moment to print final stats

	// Calculate test duration
	testDuration := time.Since(testStart)

	fmt.Printf("\n=== Test Summary ===\n")
	fmt.Printf("Test duration: %v\n", testDuration)
	fmt.Printf("Total requests: %d\n", atomic.LoadInt64(&stats.TotalRequests))
	fmt.Printf("Successful requests: %d\n", atomic.LoadInt64(&stats.SuccessfulRequests))
	fmt.Printf("Failed requests: %d\n", atomic.LoadInt64(&stats.FailedRequests))
	fmt.Printf("Create requests: %d\n", atomic.LoadInt64(&stats.CreateRequests))
	fmt.Printf("Get requests: %d\n", atomic.LoadInt64(&stats.GetRequests))

	if apiFailure {
		fmt.Println("Test result: API FAILED")
	} else {
		fmt.Println("Test result: API SURVIVED the test duration")
	}

	// Report the last 10 errors
	stats.ErrorMutex.Lock()
	fmt.Println("\n=== Last Errors ===")
	errorCount := len(stats.Errors)
	startIdx := 0
	if errorCount > 10 {
		startIdx = errorCount - 10
	}
	for i := startIdx; i < errorCount; i++ {
		fmt.Println(stats.Errors[i])
	}
	stats.ErrorMutex.Unlock()
}

// Create worker sends POST requests to create cache entries
func createWorker(client *http.Client, config *Config, stats *Stats, userIDs chan string, stop chan struct{}, ticker <-chan time.Time, workerID int) {
	for {
		select {
		case <-stop:
			return
		case <-ticker:
			// Generate a random user
			user := generateUser()

			// Marshal the user to JSON
			userJSON, err := json.Marshal(user)
			if err != nil {
				logError(stats, fmt.Sprintf("Error marshaling user: %v", err))
				continue
			}

			// Create the request with 1hr expiry
			url := fmt.Sprintf("%s/create?expiry=3600", config.BaseURL)
			req, err := http.NewRequest("POST", url, bytes.NewBuffer(userJSON))
			if err != nil {
				logError(stats, fmt.Sprintf("Error creating request: %v", err))
				continue
			}
			req.Header.Set("Content-Type", "application/json")

			// Send the request
			atomic.AddInt64(&stats.TotalRequests, 1)
			atomic.AddInt64(&stats.CreateRequests, 1)

			resp, err := client.Do(req)
			if err != nil {
				atomic.AddInt64(&stats.FailedRequests, 1)
				logError(stats, fmt.Sprintf("Create request failed: %v", err))
				continue
			}

			// Process the response
			if resp.StatusCode != http.StatusCreated {
				atomic.AddInt64(&stats.FailedRequests, 1)
				errorMsg := fmt.Sprintf("Create request returned status %d", resp.StatusCode)
				logError(stats, errorMsg)
				resp.Body.Close()
				continue
			}

			// Parse the response
			var response Response
			if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
				atomic.AddInt64(&stats.FailedRequests, 1)
				logError(stats, fmt.Sprintf("Error decoding response: %v", err))
				resp.Body.Close()
				continue
			}
			resp.Body.Close()

			// Send the user ID to the channel for GET requests
			select {
			case userIDs <- user.ID:
				// User ID sent to channel for GET requests
			default:
				// Channel is full, discard this ID
			}

			atomic.AddInt64(&stats.SuccessfulRequests, 1)
		}
	}
}

// Get worker sends GET requests to retrieve cache entries
func getWorker(client *http.Client, config *Config, stats *Stats, userIDs chan string, stop chan struct{}, ticker <-chan time.Time, workerID int) {
	for {
		select {
		case <-stop:
			return
		case <-ticker:
			// Try to get a user ID from the channel
			var userID string
			select {
			case id := <-userIDs:
				userID = id
			default:
				// No user IDs available, generate a random one (will likely 404)
				userID = fmt.Sprintf("user-%d", rand.Intn(1000000))
			}

			// Create the request
			url := fmt.Sprintf("%s/get/%s", config.BaseURL, userID)
			req, err := http.NewRequest("GET", url, nil)
			if err != nil {
				logError(stats, fmt.Sprintf("Error creating request: %v", err))
				continue
			}

			// Send the request
			atomic.AddInt64(&stats.TotalRequests, 1)
			atomic.AddInt64(&stats.GetRequests, 1)

			resp, err := client.Do(req)
			if err != nil {
				atomic.AddInt64(&stats.FailedRequests, 1)
				logError(stats, fmt.Sprintf("Get request failed: %v", err))
				continue
			}

			// Process the response
			if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
				atomic.AddInt64(&stats.FailedRequests, 1)
				errorMsg := fmt.Sprintf("Get request returned status %d", resp.StatusCode)
				logError(stats, errorMsg)
				resp.Body.Close()
				continue
			}

			// Parse the response
			var response Response
			if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
				atomic.AddInt64(&stats.FailedRequests, 1)
				logError(stats, fmt.Sprintf("Error decoding response: %v", err))
				resp.Body.Close()
				continue
			}
			resp.Body.Close()

			// Put the user ID back in the channel for reuse if it was found
			if resp.StatusCode == http.StatusOK {
				select {
				case userIDs <- userID:
					// User ID sent back to channel for future GET requests
				default:
					// Channel is full, discard this ID
				}
			}

			atomic.AddInt64(&stats.SuccessfulRequests, 1)
		}
	}
}

// Generate a random user
func generateUser() User {
	id := fmt.Sprintf("user-%d", rand.Intn(1000000))
	names := []string{"Alice", "Bob", "Charlie", "Dave", "Eve", "Frank", "Grace", "Heidi", "Ivan", "Judy"}
	domains := []string{"example.com", "test.org", "demo.net", "mail.com", "cache.io"}

	return User{
		ID:    id,
		Name:  names[rand.Intn(len(names))] + " " + fmt.Sprintf("User-%d", rand.Intn(10000)),
		Email: fmt.Sprintf("%s.%d@%s", names[rand.Intn(len(names))], rand.Intn(10000), domains[rand.Intn(len(domains))]),
		Age:   rand.Intn(80) + 18,
	}
}

// Log an error
func logError(stats *Stats, message string) {
	stats.ErrorMutex.Lock()
	defer stats.ErrorMutex.Unlock()

	// Add timestamp to error message
	timestamp := time.Now().Format("15:04:05.000")
	errorMsg := fmt.Sprintf("[%s] %s", timestamp, message)

	// Keep only the last 100 errors
	if len(stats.Errors) >= 100 {
		stats.Errors = append(stats.Errors[1:], errorMsg)
	} else {
		stats.Errors = append(stats.Errors, errorMsg)
	}
}

// Report stats periodically
func reportStats(stats *Stats, stop chan struct{}) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	lastTotal := int64(0)
	lastTime := time.Now()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			now := time.Now()
			currentTotal := atomic.LoadInt64(&stats.TotalRequests)
			duration := now.Sub(lastTime)
			requestRate := float64(currentTotal-lastTotal) / duration.Seconds()

			fmt.Printf("\rRequests: %d | Success: %d | Failed: %d | Rate: %.2f req/s | Create: %d | Get: %d",
				currentTotal,
				atomic.LoadInt64(&stats.SuccessfulRequests),
				atomic.LoadInt64(&stats.FailedRequests),
				requestRate,
				atomic.LoadInt64(&stats.CreateRequests),
				atomic.LoadInt64(&stats.GetRequests))

			lastTotal = currentTotal
			lastTime = now
		}
	}
}
