package watcher

import (
	"context"
	"errors" // Added for error checking
	"fmt"
	"io/fs" // Added for filepath.WalkDir
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time" // Added for scan duration logging

	"github.com/helshabini/fsbroker"
	"github.com/javi11/nzb-repair/internal/queue"
)

// Watcher monitors a directory for new .nzb files.
type Watcher struct {
	dir   string
	queue queue.Queuer
	log   *slog.Logger
}

func NewWatcher(dir string, q queue.Queuer, logger *slog.Logger) *Watcher {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		logger.Warn("Failed to get absolute path for watch directory, relative paths might be inconsistent.", "directory", dir, "error", err)
		absDir = dir
	}

	return &Watcher{
		dir:   absDir,
		queue: q,
		log:   logger.With("component", "watcher", "directory", absDir),
	}
}

// Run starts the directory watching process using fsbroker.
// It blocks until the context is canceled.
func (w *Watcher) Run(ctx context.Context) error {
	config := fsbroker.DefaultFSConfig()

	w.log.InfoContext(ctx, "Initializing watcher")
	broker, err := fsbroker.NewFSBroker(config)
	if err != nil {
		w.log.ErrorContext(ctx, "Failed to create watcher", "error", err)
		return fmt.Errorf("failed to create watcher: %w", err)
	}
	defer broker.Stop()

	w.log.InfoContext(ctx, "Adding recursive watch", "directory", w.dir)
	if err := broker.AddRecursiveWatch(w.dir); err != nil {
		w.log.ErrorContext(ctx, "Failed to add recursive watch", "directory", w.dir, "error", err)
		return fmt.Errorf("failed to add recursive watch for %s: %w", w.dir, err)
	}

	w.log.InfoContext(ctx, "Starting watch loop")
	broker.Start()

	for {
		select {
		case <-ctx.Done():
			w.log.InfoContext(ctx, "Stopping watcher due to context cancellation")
			return ctx.Err()
		case event := <-broker.Next():
			w.log.DebugContext(ctx, "Received event from watcher", "path", event.Path, "type", event.Type.String())

			// Handle Create events for both files and directories
			if event.Type == fsbroker.Create {
				fileInfo, err := os.Stat(event.Path)
				if err != nil {
					if !os.IsNotExist(err) {
						w.log.WarnContext(ctx, "Failed to stat path from create event", "path", event.Path, "error", err)
					} else {
						w.log.DebugContext(ctx, "Path from create event disappeared before stat", "path", event.Path)
					}
					continue
				}

				if fileInfo.IsDir() {
					// Directory created: start background scan
					w.log.InfoContext(ctx, "Detected directory creation, scheduling scan", "directory", event.Path)
					// Run scan in a goroutine to avoid blocking the main watch loop
					go w.scanDirectoryForNzb(ctx, event.Path)
				} else {
					// File created: check if it's an NZB file
					if strings.ToLower(filepath.Ext(event.Path)) == ".nzb" {
						w.log.DebugContext(ctx, "Detected NZB file creation", "path", event.Path)
						w.addFileToQueue(ctx, event.Path)
					} else {
						w.log.DebugContext(ctx, "Ignoring non-NZB file creation", "path", event.Path)
					}
				}
			} else {
				// Log other event types for debugging if needed
				w.log.DebugContext(ctx, "Ignoring event type", "path", event.Path, "type", event.Type.String())
			}

		case err := <-broker.Error():
			// Log fsbroker errors but continue running
			// Corrected typo in log message key from "qatcher error" to "watcher error"
			w.log.ErrorContext(ctx, "Watcher error", "error", err)
		}
	}
}

// scanDirectoryForNzb recursively scans a directory for .nzb files and adds them to the queue.
// It respects context cancellation.
func (w *Watcher) scanDirectoryForNzb(ctx context.Context, dirPath string) {
	w.log.InfoContext(ctx, "Scanning directory for NZB files", "directory", dirPath)
	startTime := time.Now() // Optional: track scan duration

	err := filepath.WalkDir(dirPath, func(path string, d fs.DirEntry, walkErr error) error {
		// 1. Check for context cancellation first
		select {
		case <-ctx.Done():
			w.log.InfoContext(ctx, "Directory scan cancelled by context", "directory", dirPath)
			return ctx.Err() // Stop walking
		default:
			// Continue
		}

		// 2. Handle errors during walking (e.g., permission denied)
		if walkErr != nil {
			w.log.WarnContext(ctx, "Error accessing path during scan", "path", path, "error", walkErr)
			// If it's a directory we can't read, skip its contents
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			// For file errors, just continue walking other files
			return nil
		}

		// 3. Process the entry if it's a regular file with .nzb extension
		if !d.IsDir() && strings.ToLower(filepath.Ext(d.Name())) == ".nzb" {
			w.log.DebugContext(ctx, "Found NZB file during scan", "path", path)
			// Use the existing function to handle path processing and queue addition
			w.addFileToQueue(ctx, path)
		}

		return nil // Continue walking
	})

	duration := time.Since(startTime)
	if err != nil && !errors.Is(err, context.Canceled) { // Don't log error if scan was just cancelled
		w.log.ErrorContext(ctx, "Error during directory scan", "directory", dirPath, "duration", duration, "error", err)
	} else {
		w.log.InfoContext(ctx, "Finished scanning directory", "directory", dirPath, "duration", duration)
	}
}

// addFileToQueue handles the logic of validating and adding a file path to the queue.
// It's called by the Run loop or scanDirectoryForNzb.
func (w *Watcher) addFileToQueue(ctx context.Context, filePath string) {
	// Corrected log message to be more accurate
	w.log.InfoContext(ctx, "Adding detected NZB file to queue", "path", filePath)

	// Ensure the path is absolute. fsbroker paths should generally be absolute.
	// filepath.WalkDir also provides absolute paths if the root is absolute.
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		w.log.WarnContext(ctx, "Failed to get absolute path, using original", "path", filePath, "error", err)
		absPath = filePath
	}

	// Calculate relative path based on the absolute watch directory (w.dir)
	relPath, err := filepath.Rel(w.dir, absPath)
	if err != nil {
		w.log.ErrorContext(ctx, "Failed to calculate relative path, using base filename as fallback", "base_dir", w.dir, "file_path", absPath, "error", err)
		relPath = filepath.Base(absPath) // Fallback to base name
	}

	// Add job with both absolute and relative paths
	err = w.queue.AddJob(absPath, relPath)
	if err != nil {
		// Log error generally, as specific ErrDuplicateJob might not be exported/available
		w.log.ErrorContext(ctx, "Failed to add job to queue", "path", absPath, "relative_path", relPath, "error", err)
	} else {
		w.log.InfoContext(ctx, "Successfully added job to queue", "path", absPath, "relative_path", relPath)
	}
}
