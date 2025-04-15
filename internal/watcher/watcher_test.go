package watcher_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/javi11/nzb-repair/internal/watcher"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockQueue is a simple thread-safe mock for the queue.Queue.
type MockQueue struct {
	mu       sync.Mutex
	jobs     map[string]string // Map absolute path to relative path
	addCount int
}

func NewMockQueue() *MockQueue {
	return &MockQueue{
		jobs: make(map[string]string),
	}
}

func (mq *MockQueue) AddJob(absPath, relPath string) error {
	mq.mu.Lock()
	defer mq.mu.Unlock()
	// Simple duplicate check for testing purposes
	if _, exists := mq.jobs[absPath]; exists {
		return fmt.Errorf("duplicate job: %s", absPath) // Simulate queue's duplicate error
	}
	mq.jobs[absPath] = relPath
	mq.addCount++
	return nil
}

func (mq *MockQueue) GetJobCount() int {
	mq.mu.Lock()
	defer mq.mu.Unlock()
	return mq.addCount
}

func (mq *MockQueue) GetJobs() map[string]string {
	mq.mu.Lock()
	defer mq.mu.Unlock()
	// Return a copy to avoid race conditions in assertions
	jobsCopy := make(map[string]string, len(mq.jobs))
	for k, v := range mq.jobs {
		jobsCopy[k] = v
	}
	return jobsCopy
}

// Helper function to create a temporary file
func createTempFile(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	err := os.WriteFile(path, []byte("test"), 0644)
	require.NoError(t, err)
	return path
}

// Helper function to create a temporary directory
func createTempDir(t *testing.T, parentDir, name string) string {
	t.Helper()
	path := filepath.Join(parentDir, name)
	err := os.MkdirAll(path, 0755)
	require.NoError(t, err)
	return path
}

func TestWatcher(t *testing.T) {
	// Setup temporary watch directory
	watchDir, err := os.MkdirTemp("", "watchertest-")
	require.NoError(t, err)
	defer func() {
		_ = os.RemoveAll(watchDir) // Clean up
	}()

	mockQueue := NewMockQueue()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})) // Use Debug for testing
	w := watcher.NewWatcher(watchDir, mockQueue, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // Ensure context is cancelled eventually

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := w.Run(ctx)
		// Expect context cancellation error when test finishes
		if err != nil && err != context.Canceled {
			assert.NoError(t, err, "Watcher Run returned unexpected error")
		}
	}()

	// Give the watcher a moment to start up
	time.Sleep(500 * time.Millisecond) // Adjust if needed

	t.Run("DetectNewNzbFileInRoot", func(t *testing.T) {
		nzbFileName := "test1.nzb"
		expectedRelPath := nzbFileName
		absPath := createTempFile(t, watchDir, nzbFileName)

		// Wait for the event to be processed
		assert.Eventually(t, func() bool {
			return mockQueue.GetJobCount() == 1
		}, 3*time.Second, 100*time.Millisecond, "Watcher did not add job for root NZB file")

		jobs := mockQueue.GetJobs()
		assert.Contains(t, jobs, absPath)
		assert.Equal(t, expectedRelPath, jobs[absPath])
	})

	t.Run("IgnoreNonNzbFile", func(t *testing.T) {
		initialJobCount := mockQueue.GetJobCount()
		_ = createTempFile(t, watchDir, "test2.txt")

		// Wait a bit to ensure no event is processed
		time.Sleep(500 * time.Millisecond)
		assert.Equal(t, initialJobCount, mockQueue.GetJobCount(), "Watcher added job for non-NZB file")
	})

	t.Run("DetectNewNzbFileInNewSubdirectory", func(t *testing.T) {
		subDirName := "subdir1"
		subDirPath := createTempDir(t, watchDir, subDirName)
		// Wait for fsbroker to potentially pick up the dir creation
		time.Sleep(200 * time.Millisecond)

		nzbFileName := "test3.nzb"
		expectedRelPath := filepath.Join(subDirName, nzbFileName)
		absPath := createTempFile(t, subDirPath, nzbFileName)

		initialJobCount := mockQueue.GetJobCount()
		// Wait for the event to be processed
		assert.Eventually(t, func() bool {
			return mockQueue.GetJobCount() == initialJobCount+1
		}, 3*time.Second, 100*time.Millisecond, "Watcher did not add job for NZB file in new subdirectory")

		jobs := mockQueue.GetJobs()
		assert.Contains(t, jobs, absPath)
		assert.Equal(t, expectedRelPath, jobs[absPath])
	})

	t.Run("ScanNewDirectoryWithExistingNzb", func(t *testing.T) {
		scanDirName := "scanDir"
		scanDirPath := filepath.Join(watchDir, scanDirName) // Path before creation

		// Create NZB file *before* creating the directory in the watch path
		tempDirForPreCreate, err := os.MkdirTemp("", "precreate-")
		require.NoError(t, err)
		defer func() {
			_ = os.RemoveAll(tempDirForPreCreate)
		}()

		nzbFileName := "preexisting.nzb"
		expectedRelPath := filepath.Join(scanDirName, nzbFileName)
		preCreatedAbsPath := createTempFile(t, tempDirForPreCreate, nzbFileName)
		expectedAbsPath := filepath.Join(scanDirPath, nzbFileName) // Final expected path

		// Now, move the temp dir containing the NZB file into the watch directory
		initialJobCount := mockQueue.GetJobCount()
		err = os.Rename(tempDirForPreCreate, scanDirPath)
		require.NoError(t, err, "Failed to move directory into watch directory")

		// Wait for the directory creation event and subsequent scan to complete
		assert.Eventually(t, func() bool {
			return mockQueue.GetJobCount() == initialJobCount+1
		}, 5*time.Second, 200*time.Millisecond, "Watcher did not add job for NZB file in scanned directory") // Increased timeout for scan

		jobs := mockQueue.GetJobs()
		// Check the expected *final* absolute path
		assert.Contains(t, jobs, expectedAbsPath, "Job map does not contain expected final absolute path")
		// Check original pre-created path is NOT in jobs (should be the moved path)
		assert.NotContains(t, jobs, preCreatedAbsPath, "Job map contains original pre-created path")
		assert.Equal(t, expectedRelPath, jobs[expectedAbsPath])
	})

	// Test cancellation
	cancel()  // Signal the watcher to stop
	wg.Wait() // Wait for the Run goroutine to exit
	logger.Info("Watcher goroutine finished.")
}
