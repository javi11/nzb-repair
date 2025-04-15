package queue

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3" // Import the sqlite3 driver
)

type JobStatus string

const (
	StatusPending    JobStatus = "pending"
	StatusProcessing JobStatus = "processing"
	StatusCompleted  JobStatus = "completed"
	StatusFailed     JobStatus = "failed"
)

// ErrDuplicateJob can be used by mock implementations.
// Note: The actual Queue implementation handles duplicates internally
// and doesn't currently return a specific exported error type for this.
var ErrDuplicateJob = errors.New("job already exists or is being processed")

type Job struct {
	ID           int64
	FilePath     string
	RelativePath string
	Status       JobStatus
	ErrorMsg     sql.NullString
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Queuer defines the interface for adding jobs, primarily used for dependency injection.
type Queuer interface {
	// AddJob adds a new job to the queue. Implementations should handle
	// path normalization and duplicate checks as needed.
	AddJob(absPath, relPath string) error
	// Potentially add other methods needed by consumers like Watcher later
}

// Ensure Queue implements Queuer
var _ Queuer = (*Queue)(nil)

type Queue struct {
	db *sql.DB
}

// NewQueue initializes the SQLite database and creates/updates the jobs table.
func NewQueue(dbPath string) (*Queue, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Create the jobs table if it doesn't exist
	query := `
	CREATE TABLE IF NOT EXISTS jobs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		filepath TEXT NOT NULL UNIQUE,
		relative_path TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'pending',
		error_msg TEXT,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);
	`
	_, err = db.Exec(query)
	if err != nil {
		// Close DB if table creation fails
		_ = db.Close()
		return nil, fmt.Errorf("failed to create jobs table: %w", err)
	}

	// Attempt to add the relative_path column if it doesn't exist (migration for older dbs)
	// This avoids errors if the table already exists without the column.
	alterQuery := `ALTER TABLE jobs ADD COLUMN relative_path TEXT NOT NULL DEFAULT ''`
	_, err = db.Exec(alterQuery)
	if err != nil {
		// Ignore error if the column already exists
		if !strings.Contains(err.Error(), "duplicate column name") {
			// Log other alteration errors but don't fail initialization
			slog.Warn("failed to add relative_path column (might already exist)", "error", err)
		}
	}

	// Add indexes
	indexQueries := []string{
		`CREATE INDEX IF NOT EXISTS idx_jobs_status_created_at ON jobs (status, created_at);`,
		// No need to index relative_path unless we plan to query by it frequently
		// `CREATE INDEX IF NOT EXISTS idx_jobs_relative_path ON jobs (relative_path);`,
	}
	for _, iq := range indexQueries {
		_, err = db.Exec(iq)
		if err != nil {
			// Log index creation errors but don't fail initialization
			slog.Warn("failed to create index", "query", iq, "error", err)
		}
	}

	return &Queue{db: db}, nil
}

// AddJob adds a new NZB file path (absolute and relative) to the queue with pending status.
// It ignores duplicates based on the absolute filepath unless the existing job is failed,
// in which case it resets the status to pending and updates the relative path.
func (q *Queue) AddJob(filePath string, relativePath string) error {
	tx, err := q.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback() // Rollback if anything fails
	}()

	var currentStatus JobStatus
	var jobID int64 // We don't strictly need the ID here, but scanning into it avoids error if row exists
	// Select based on absolute filepath
	selectQuery := `SELECT id, status FROM jobs WHERE filepath = ?`
	err = tx.QueryRow(selectQuery, filePath).Scan(&jobID, &currentStatus)

	now := time.Now()

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Job doesn't exist, insert as pending with relative path
			insertQuery := `INSERT INTO jobs (filepath, relative_path, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`
			_, err = tx.Exec(insertQuery, filePath, relativePath, StatusPending, now, now)
			if err != nil {
				return fmt.Errorf("failed to insert new job: %w", err)
			}
		} else {
			// Other error during select
			return fmt.Errorf("failed to check for existing job: %w", err)
		}
	} else {
		// Job exists
		if currentStatus == StatusFailed || currentStatus == StatusCompleted {
			// Job failed or completed, reset to pending and update relative path just in case
			updateQuery := `UPDATE jobs SET status = ?, error_msg = NULL, updated_at = ?, relative_path = ? WHERE filepath = ?`
			_, err = tx.Exec(updateQuery, StatusPending, now, relativePath, filePath)
			if err != nil {
				return fmt.Errorf("failed to reset existing job to pending: %w", err)
			}
			slog.Debug("Resetting existing job to pending", "filepath", filePath, "relative_path", relativePath)
		} else {
			// Job exists with status pending or processing - ignore
			slog.Debug("Ignoring add job request for existing non-failed/non-completed job", "filepath", filePath, "status", currentStatus)
			// No action needed, transaction will be committed harmlessly if update wasn't needed.
		}
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction for AddJob: %w", err)
	}

	return nil
}

// GetNextJob retrieves the oldest pending job, marks it as processing, and returns it.
// Returns sql.ErrNoRows if no pending jobs are available.
func (q *Queue) GetNextJob() (*Job, error) {
	tx, err := q.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback() // Rollback if anything fails
	}()

	// Select the oldest pending job, including relative_path
	selectQuery := `SELECT id, filepath, relative_path, status, error_msg, created_at, updated_at FROM jobs WHERE status = ? ORDER BY created_at ASC LIMIT 1`
	row := tx.QueryRow(selectQuery, StatusPending)

	job := &Job{}
	// Scan relative_path into the job struct
	err = row.Scan(&job.ID, &job.FilePath, &job.RelativePath, &job.Status, &job.ErrorMsg, &job.CreatedAt, &job.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows // Specific error for no pending jobs
		}
		// Potential error if relative_path column doesn't exist yet (if ALTER TABLE failed silently)
		// Log the specific scan error for debugging
		slog.Error("Failed to scan job row", "error", err)
		return nil, fmt.Errorf("failed to scan job row: %w", err)
	}

	// Update the job status to processing
	updateQuery := `UPDATE jobs SET status = ?, updated_at = ? WHERE id = ?`
	_, err = tx.Exec(updateQuery, StatusProcessing, time.Now(), job.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to update job status to processing: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	job.Status = StatusProcessing // Update status in the returned struct
	return job, nil
}

// UpdateJobStatus updates the status and optionally the error message for a given job ID.
func (q *Queue) UpdateJobStatus(jobID int64, status JobStatus, errorMsg string) error {
	var errMsg sql.NullString
	if errorMsg != "" {
		errMsg = sql.NullString{String: errorMsg, Valid: true}
	}

	query := `UPDATE jobs SET status = ?, error_msg = ?, updated_at = ? WHERE id = ?`
	_, err := q.db.Exec(query, status, errMsg, time.Now(), jobID)
	if err != nil {
		return fmt.Errorf("failed to update job status: %w", err)
	}
	return nil
}

// Close closes the database connection.
func (q *Queue) Close() error {
	if q.db != nil {
		return q.db.Close()
	}
	return nil
}

// CleanupProcessingJobs finds all jobs marked as processing and sets their status to failed.
// This is typically called on application startup to handle jobs interrupted by a previous crash.
func (q *Queue) CleanupProcessingJobs() (int64, error) {
	query := `UPDATE jobs SET status = ?, updated_at = ? WHERE status = ?`
	now := time.Now()
	result, err := q.db.Exec(query, StatusPending, now, StatusProcessing)
	if err != nil {
		return 0, fmt.Errorf("failed to update processing jobs to failed: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		// Log the error but don't fail the operation if we can't get rows affected
		slog.Warn("failed to get rows affected after cleaning up processing jobs", "error", err)
		return 0, nil // Return 0 rows affected, but no error for the main operation
	}

	if rowsAffected > 0 {
		slog.Info("Cleaned up interrupted jobs", "count", rowsAffected)
	}

	return rowsAffected, nil
}
