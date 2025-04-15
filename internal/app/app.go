package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/javi11/nntppool"
	"github.com/javi11/nzb-repair/internal/config"
	"github.com/javi11/nzb-repair/internal/queue"
	"github.com/javi11/nzb-repair/internal/repairnzb"
	"github.com/javi11/nzb-repair/internal/watcher"
	"github.com/javi11/nzb-repair/pkg/par2exedownloader"
	"golang.org/x/sync/errgroup"
)

const (
	defaultPar2Exe          = "./par2cmd"
	defaultWatcherOutputDir = "./repaired"
	defaultWorkerInterval   = 5 * time.Second
)

// RunSingleRepair executes the repair process for a single NZB file.
func RunSingleRepair(ctx context.Context, cfg config.Config, nzbFile string, outputFileOrDir string, tmpDir string, verbose bool) error {
	logger := setupLogging(verbose)

	absTmpDir, err := prepareTmpDir(ctx, tmpDir, logger)
	if err != nil {
		return fmt.Errorf("failed to prepare temporary directory: %w", err)
	}

	// Ensure par2 executable exists and get its path
	par2ExePath, err := ensurePar2Executable(ctx, cfg, logger)
	if err != nil {
		return fmt.Errorf("failed to ensure par2 executable: %w", err)
	}
	// Create the par2 executor
	par2Executor := &repairnzb.Par2CmdExecutor{ExePath: par2ExePath}

	uploadPool, downloadPool, err := createPools(cfg)
	if err != nil {
		return err // Error already contains context
	}
	// Ensure pools are closed properly
	defer func() {
		logger.DebugContext(ctx, "Closing download pool")
		downloadPool.Quit()
		// TODO: Add uploadPool.Quit() when fixed in nntppool
		// logger.DebugContext(ctx, "Closing upload pool")
		// uploadPool.Quit()
	}()

	outputFile, err := getSingleOutputFilePath(nzbFile, outputFileOrDir)
	if err != nil {
		return fmt.Errorf("failed to determine output file path: %w", err)
	}
	logger.InfoContext(ctx, "Starting repair", "input", nzbFile, "output", outputFile, "temp", absTmpDir)

	err = repairnzb.RepairNzb(
		ctx,
		cfg,
		downloadPool,
		uploadPool,
		par2Executor, // Pass the executor instance
		nzbFile,
		outputFile,
		absTmpDir,
	)
	if err != nil {
		logger.ErrorContext(ctx, "Repair failed", "input", nzbFile, "error", err)
		return fmt.Errorf("repair process failed for %q: %w", nzbFile, err)
	}

	logger.InfoContext(ctx, "Repair successful", "input", nzbFile, "output", outputFile)
	return nil
}

// RunWatcher starts the directory watcher and the repair worker goroutines.
func RunWatcher(ctx context.Context, cfg config.Config, watchDir string, dbPath string, outputBaseDirFlag string, tmpDir string, verbose bool) error {
	logger := setupLogging(verbose)

	logger.InfoContext(ctx, "Initializing database...", "path", dbPath)
	dbQueue, err := queue.NewQueue(dbPath)
	if err != nil {
		return fmt.Errorf("failed to initialize queue: %w", err)
	}

	defer func() {
		logger.InfoContext(ctx, "Closing database queue")
		if cErr := dbQueue.Close(); cErr != nil {
			logger.ErrorContext(ctx, "Error closing database queue", "error", cErr)
		}
	}()

	// Cleanup interrupted jobs from previous runs
	logger.InfoContext(ctx, "Cleaning up any jobs marked as 'processing' from previous runs")
	cleanedCount, err := dbQueue.CleanupProcessingJobs()
	if err != nil {
		logger.ErrorContext(ctx, "Failed to cleanup processing jobs, continuing...", "error", err)
	} else {
		logger.InfoContext(ctx, "Cleaned up processing jobs", "count", cleanedCount)
	}

	absTmpDir, err := prepareTmpDir(ctx, tmpDir, logger)
	if err != nil {
		return fmt.Errorf("failed to prepare temporary directory: %w", err)
	}

	// Note: Tmp dir is prepared once at the start for the watcher.
	// Determine and prepare the base output directory.
	outputBaseDir := outputBaseDirFlag
	if outputBaseDir == "" {
		outputBaseDir = defaultWatcherOutputDir
		logger.InfoContext(ctx, "No output directory specified (-o), using default", "path", outputBaseDir)
	}
	// Ensure the base output directory exists and get its absolute path.
	if err := os.MkdirAll(outputBaseDir, 0750); err != nil {
		return fmt.Errorf("failed to create base output directory %q: %w", outputBaseDir, err)
	}

	outputBaseDir, err = filepath.Abs(outputBaseDir)
	if err != nil {
		return fmt.Errorf("failed to get absolute path for output directory %q: %w", outputBaseDir, err)
	}

	logger.InfoContext(ctx, "Using output directory", "path", outputBaseDir)

	// Ensure par2 executable exists and get its path
	par2ExePath, err := ensurePar2Executable(ctx, cfg, logger)
	if err != nil {
		return fmt.Errorf("failed to ensure par2 executable: %w", err)
	}
	// Create the par2 executor
	par2Executor := &repairnzb.Par2CmdExecutor{ExePath: par2ExePath}

	uploadPool, downloadPool, err := createPools(cfg)
	if err != nil {
		return err
	}

	defer func() {
		logger.DebugContext(ctx, "Closing download pool")
		downloadPool.Quit()
		// TODO: Add uploadPool.Quit() when fixed in nntppool
		// logger.DebugContext(ctx, "Closing upload pool")
		// uploadPool.Quit()
	}()

	fileWatcher := watcher.NewWatcher(watchDir, dbQueue, logger)
	eg, gCtx := errgroup.WithContext(ctx)

	// Goroutine for the directory watcher
	eg.Go(func() error {
		logger.InfoContext(gCtx, "Starting directory watcher...", "directory", watchDir)
		err := fileWatcher.Run(gCtx)
		if err != nil && !errors.Is(err, context.Canceled) {
			logger.ErrorContext(gCtx, "Directory watcher failed", "error", err)
			return fmt.Errorf("directory watcher error: %w", err) // Return error to errgroup
		}
		logger.InfoContext(gCtx, "Directory watcher stopped")
		return nil
	})

	// Goroutine for the repair worker
	eg.Go(func() error {
		logger.InfoContext(gCtx, "Starting repair worker...")
		workerTicker := time.NewTicker(defaultWorkerInterval)
		defer workerTicker.Stop()

		for {
			select {
			case <-gCtx.Done():
				logger.InfoContext(gCtx, "Repair worker stopping due to context cancellation.")

				return gCtx.Err()
			case <-workerTicker.C:
				job, err := dbQueue.GetNextJob()
				if err != nil {
					if errors.Is(err, sql.ErrNoRows) {
						continue // No jobs available, wait for next tick
					}

					logger.ErrorContext(gCtx, "Failed to get next job from queue", "error", err)
					time.Sleep(defaultWorkerInterval) // Add a small delay before retrying

					continue
				}

				logger.InfoContext(gCtx, "Processing job", "job_id", job.ID, "filepath", job.FilePath, "relative_path", job.RelativePath)

				// Calculate output path and handle potential errors
				outputFilePath, pathErr := calculateJobOutputPath(outputBaseDir, job, logger, gCtx, dbQueue)
				if pathErr != nil {
					// Error already logged and status updated in calculateJobOutputPath
					continue // Skip this job
				}

				// Run the actual repair process
				repairStartTime := time.Now()
				logger.InfoContext(gCtx, "Starting repair for job", "job_id", job.ID, "input", job.FilePath, "output", outputFilePath)

				tmpDir := filepath.Join(absTmpDir, filepath.Base(job.FilePath))
				repairErr := repairnzb.RepairNzb(
					gCtx,
					cfg,
					downloadPool,
					uploadPool,
					par2Executor,   // Pass the same executor instance
					job.FilePath,   // Absolute path from the watcher/queue
					outputFilePath, // Calculated absolute output path
					tmpDir,         // Shared absolute temp dir path
				)
				repairDuration := time.Since(repairStartTime)

				// Update job status based on repair outcome
				if repairErr != nil {
					errMsg := repairErr.Error()
					logger.ErrorContext(gCtx, "Failed to repair NZB", "job_id", job.ID, "filepath", job.FilePath, "duration", repairDuration, "error", repairErr)
					if uerr := dbQueue.UpdateJobStatus(job.ID, queue.StatusFailed, errMsg); uerr != nil {
						// Log the update error, but the primary error is the repair failure
						logger.ErrorContext(gCtx, "Failed to update job status to failed", "job_id", job.ID, "update_error", uerr)
					}
				} else {
					logger.InfoContext(gCtx, "Successfully repaired NZB", "job_id", job.ID, "input_filepath", job.FilePath, "output_filepath", outputFilePath, "duration", repairDuration)
					if uerr := dbQueue.UpdateJobStatus(job.ID, queue.StatusCompleted, ""); uerr != nil {
						logger.ErrorContext(gCtx, "Failed to update job status to completed", "job_id", job.ID, "update_error", uerr)
					}
				}
			}
		}
	})

	logger.InfoContext(ctx, "Watcher and worker started. Waiting for jobs or termination signal (Ctrl+C)...")
	// Wait for either goroutine to exit (or context cancellation)
	if err := eg.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		logger.ErrorContext(ctx, "Application exited with error", "error", err)
		return err // Return the actual error from the errgroup
	}

	logger.InfoContext(ctx, "Application shut down gracefully.")
	return nil
}

// setupLogging configures the global logger based on the verbosity level.
func setupLogging(verbose bool) *slog.Logger {
	var level slog.Level
	if verbose {
		level = slog.LevelDebug
	} else {
		level = slog.LevelInfo
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)
	return logger
}

// prepareTmpDir ensures the temporary directory exists, is clean, and returns its absolute path.
func prepareTmpDir(ctx context.Context, tmpDir string, logger *slog.Logger) (string, error) {
	absTmpDir, err := filepath.Abs(tmpDir)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path for temporary directory %q: %w", tmpDir, err)
	}

	logger.DebugContext(ctx, "Cleaning up and preparing temporary directory...", "path", absTmpDir)
	// Attempt to remove existing contents first. Log error but continue.
	if err := os.RemoveAll(absTmpDir); err != nil {
		logger.WarnContext(ctx, "Failed to remove existing temporary directory contents, attempting to continue", "path", absTmpDir, "error", err)
	}

	// Create the directory structure.
	if err := os.MkdirAll(absTmpDir, 0750); err != nil {
		return "", fmt.Errorf("failed to create temporary directory %q: %w", absTmpDir, err)
	}

	return absTmpDir, nil
}

// ensurePar2Executable checks if a par2 executable is configured, downloads one if necessary,
// and returns the final path to the executable.
func ensurePar2Executable(ctx context.Context, cfg config.Config, logger *slog.Logger) (string, error) {
	if cfg.Par2Exe != "" {
		logger.DebugContext(ctx, "Using configured Par2 executable", "path", cfg.Par2Exe)
		// Verify it exists?
		if _, err := os.Stat(cfg.Par2Exe); err == nil {
			return cfg.Par2Exe, nil
		} else {
			logger.WarnContext(ctx, "Configured Par2 executable not found, proceeding to check default/download", "path", cfg.Par2Exe, "error", err)
			// Fall through to check default/download
		}
	}

	// Check default path
	if _, err := os.Stat(defaultPar2Exe); err == nil {
		logger.InfoContext(ctx, "Par2 executable found in default path, using it.", "path", defaultPar2Exe)
		// Update the config in memory if we found it here? Might not be necessary if only path is returned.
		// cfg.Par2Exe = defaultPar2Exe // Avoid modifying cfg directly here, just return the path
		return defaultPar2Exe, nil
	} else if !os.IsNotExist(err) {
		// Log unexpected error checking default path, but proceed to download
		logger.WarnContext(ctx, "Unexpected error checking for par2 executable at default path", "path", defaultPar2Exe, "error", err)
	}

	// Download if not configured and not found in default path
	logger.InfoContext(ctx, "No par2 executable configured or found, downloading animetosho/par2cmdline-turbo...")
	execPath, err := par2exedownloader.DownloadPar2Cmd()
	if err != nil {
		return "", fmt.Errorf("failed to download par2cmd: %w", err)
	}
	logger.InfoContext(ctx, "Downloaded Par2 executable", "path", execPath)
	// Update the config in memory? Again, maybe just return the path.
	// cfg.Par2Exe = execPath
	return execPath, nil
}

// createPools initializes and returns the NNTP connection pools.
func createPools(cfg config.Config) (uploadPool, downloadPool nntppool.UsenetConnectionPool, err error) {
	uploadPool, err = nntppool.NewConnectionPool(nntppool.Config{Providers: cfg.UploadProviders})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create upload pool: %w", err)
	}

	downloadPool, err = nntppool.NewConnectionPool(nntppool.Config{Providers: cfg.DownloadProviders})
	if err != nil {
		// Make sure to quit the already created uploadPool if downloadPool fails
		if uploadPool != nil {
			uploadPool.Quit()
		}
		return nil, nil, fmt.Errorf("failed to create download pool: %w", err)
	}

	return uploadPool, downloadPool, nil
}

// getSingleOutputFilePath determines the output path for a single file repair.
// If outputFileOrDir is empty, it defaults to appending "_repaired" to the input filename.
// If outputFileOrDir is a directory, it places the repaired file inside it.
// If outputFileOrDir is a file path, it uses that path.
func getSingleOutputFilePath(inputFile string, outputFileOrDir string) (string, error) {
	if outputFileOrDir == "" {
		ext := filepath.Ext(inputFile)
		return fmt.Sprintf("%s_repaired%s", strings.TrimSuffix(inputFile, ext), ext), nil
	}

	// Check if the output path exists
	info, err := os.Stat(outputFileOrDir)
	if err != nil {
		if os.IsNotExist(err) {
			// Doesn't exist. Check if parent directory exists.
			parentDir := filepath.Dir(outputFileOrDir)
			_, parentErr := os.Stat(parentDir)
			if os.IsNotExist(parentErr) {
				// Parent dir doesn't exist either, return error.
				return "", fmt.Errorf("output directory %q does not exist", parentDir)
			} else if parentErr != nil {
				// Other error stating parent directory.
				return "", fmt.Errorf("failed to stat output directory %q: %w", parentDir, parentErr)
			}
			// Parent exists, assume outputFileOrDir is the intended full file path.
			return outputFileOrDir, nil
		}
		// Other error stating the output path itself.
		return "", fmt.Errorf("failed to stat output path %q: %w", outputFileOrDir, err)
	}

	// Output path exists.
	if info.IsDir() {
		// It's a directory, join with the base name of the input file.
		base := filepath.Base(inputFile)
		return filepath.Join(outputFileOrDir, base), nil
	}

	// It exists and is a file, use it directly.
	return outputFileOrDir, nil
}

// calculateJobOutputPath determines the final path for a repaired file within the watcher's output directory.
// It ensures the relative path is safe and creates necessary subdirectories.
func calculateJobOutputPath(outputBaseDir string, job *queue.Job, logger *slog.Logger, gCtx context.Context, dbQueue *queue.Queue) (string, error) {
	// Clean the relative path to prevent path traversal issues (e.g., ../../..)
	cleanRelativePath := filepath.Clean(job.RelativePath)
	if strings.HasPrefix(cleanRelativePath, "..") || cleanRelativePath == "." || cleanRelativePath == "" || filepath.IsAbs(cleanRelativePath) {
		errMsg := fmt.Sprintf("invalid relative path calculated: %q", job.RelativePath)
		logger.ErrorContext(gCtx, errMsg, "job_id", job.ID)
		if uerr := dbQueue.UpdateJobStatus(job.ID, queue.StatusFailed, errMsg); uerr != nil {
			logger.ErrorContext(gCtx, "Failed to update job status to failed after invalid relative path error", "job_id", job.ID, "update_error", uerr)
		}

		return "", errors.New(errMsg)
	}
	outputFilePath := filepath.Join(outputBaseDir, cleanRelativePath)

	// Ensure the subdirectory structure exists within the output directory
	outputSubDir := filepath.Dir(outputFilePath)
	if err := os.MkdirAll(outputSubDir, 0750); err != nil {
		errMsg := fmt.Sprintf("failed to create output subdirectory %q: %v", outputSubDir, err)
		logger.ErrorContext(gCtx, errMsg, "job_id", job.ID)
		if uerr := dbQueue.UpdateJobStatus(job.ID, queue.StatusFailed, errMsg); uerr != nil {
			logger.ErrorContext(gCtx, "Failed to update job status to failed after output subdir error", "job_id", job.ID, "update_error", uerr)
		}

		return "", errors.New(errMsg)
	}

	return outputFilePath, nil
}
