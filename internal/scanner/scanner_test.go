package scanner

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockQueue implements queue.Queuer for testing
type mockQueue struct {
	mu   sync.Mutex
	jobs []struct {
		absPath string
		relPath string
	}
}

func (m *mockQueue) AddJob(absPath, relPath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.jobs = append(m.jobs, struct {
		absPath string
		relPath string
	}{absPath, relPath})
	return nil
}

func TestNewScanner(t *testing.T) {
	// Create a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "scanner-test-*")
	require.NoError(t, err)
	defer func() {
		_ = os.RemoveAll(tempDir)
	}()

	mockQ := &mockQueue{}
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	tests := []struct {
		name         string
		dir          string
		scanInterval time.Duration
		wantErr      bool
	}{
		{
			name:         "valid directory",
			dir:          tempDir,
			scanInterval: time.Second,
			wantErr:      false,
		},
		{
			name:         "non-existent directory",
			dir:          "/non/existent/path",
			scanInterval: time.Second,
			wantErr:      false, // Should not error, just log warning
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scanner := New(tt.dir, mockQ, logger, tt.scanInterval)
			assert.NotNil(t, scanner)
			assert.Equal(t, tt.scanInterval, scanner.scanInterval)
		})
	}
}

func TestScanner_ScanDirectory(t *testing.T) {
	// Create a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "scanner-test-*")
	require.NoError(t, err)
	defer func() {
		_ = os.RemoveAll(tempDir)
	}()

	// Create some test files
	testFiles := []string{
		"test1.nzb",
		"test2.nzb",
		"test3.txt",
		"subdir/test4.nzb",
	}

	for _, f := range testFiles {
		path := filepath.Join(tempDir, f)
		err := os.MkdirAll(filepath.Dir(path), 0755)
		require.NoError(t, err)
		_, err = os.Create(path)
		require.NoError(t, err)
	}

	mockQ := &mockQueue{}
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	scanner := New(tempDir, mockQ, logger, time.Second)

	ctx := context.Background()
	err = scanner.scanDirectory(ctx, tempDir)
	require.NoError(t, err)

	// Should have found 3 .nzb files
	assert.Equal(t, 3, len(mockQ.jobs))

	// Verify the files were found
	foundFiles := make(map[string]bool)
	for _, job := range mockQ.jobs {
		foundFiles[filepath.Base(job.absPath)] = true
	}

	assert.True(t, foundFiles["test1.nzb"])
	assert.True(t, foundFiles["test2.nzb"])
	assert.True(t, foundFiles["test4.nzb"])
	assert.False(t, foundFiles["test3.txt"])
}

func TestScanner_Run(t *testing.T) {
	// Create a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "scanner-test-*")
	require.NoError(t, err)
	defer func() {
		_ = os.RemoveAll(tempDir)
	}()

	mockQ := &mockQueue{}
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	scanner := New(tempDir, mockQ, logger, 100*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	// Create a test file after a short delay
	go func() {
		time.Sleep(50 * time.Millisecond)
		path := filepath.Join(tempDir, "test.nzb")
		_, err := os.Create(path)
		require.NoError(t, err)
	}()

	// Run the scanner
	err = scanner.Run(ctx)
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	// Should have found the file
	assert.GreaterOrEqual(t, len(mockQ.jobs), 1)
	if len(mockQ.jobs) > 0 {
		assert.Equal(t, "test.nzb", filepath.Base(mockQ.jobs[0].absPath))
	}
}

func TestScanner_NestedFolders(t *testing.T) {
	// Create a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "scanner-test-*")
	require.NoError(t, err)
	defer func() {
		_ = os.RemoveAll(tempDir)
	}()

	// Create a complex nested directory structure with NZB files
	testFiles := []string{
		"root.nzb",
		"level1/level1.nzb",
		"level1/level2/level2.nzb",
	}

	// Create the directory structure and files
	for _, f := range testFiles {
		path := filepath.Join(tempDir, f)
		err := os.MkdirAll(filepath.Dir(path), 0755)
		require.NoError(t, err)
		_, err = os.Create(path)
		require.NoError(t, err)
	}

	mockQ := &mockQueue{}
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	scanner := New(tempDir, mockQ, logger, time.Second)

	ctx := context.Background()
	err = scanner.scanDirectory(ctx, tempDir)
	require.NoError(t, err)

	// Should have found all 20 NZB files
	assert.Equal(t, 3, len(mockQ.jobs))

	// Verify all files were found
	foundFiles := make(map[string]bool)
	for _, job := range mockQ.jobs {
		foundFiles[filepath.Base(job.absPath)] = true
	}

	// Check that all expected files were found
	for i := 1; i <= 2; i++ {
		expectedFile := fmt.Sprintf("level%d.nzb", i)
		if i == 1 {
			expectedFile = "root.nzb"
		}
		assert.True(t, foundFiles[expectedFile], "Expected to find %s", expectedFile)
	}
}
