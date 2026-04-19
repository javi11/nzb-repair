package queue

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAddJob_DoesNotRequeueCompletedJob(t *testing.T) {
	q, err := NewQueue(":memory:")
	require.NoError(t, err)

	// Add a job and mark it completed
	require.NoError(t, q.AddJob("/watch/foo.nzb", "foo.nzb"))
	job, err := q.GetNextJob()
	require.NoError(t, err)
	require.NoError(t, q.UpdateJobStatus(job.ID, StatusCompleted, ""))

	// Scanner finds the same file again — should NOT re-queue
	require.NoError(t, q.AddJob("/watch/foo.nzb", "foo.nzb"))

	// Verify no pending job exists
	_, err = q.GetNextJob()
	assert.ErrorIs(t, err, sql.ErrNoRows, "completed job must not be re-queued")
}

func TestAddJob_RequeuesFailedJob(t *testing.T) {
	q, err := NewQueue(":memory:")
	require.NoError(t, err)

	require.NoError(t, q.AddJob("/watch/bar.nzb", "bar.nzb"))
	job, err := q.GetNextJob()
	require.NoError(t, err)
	require.NoError(t, q.UpdateJobStatus(job.ID, StatusFailed, "some error"))

	// Failed job SHOULD be re-queued
	require.NoError(t, q.AddJob("/watch/bar.nzb", "bar.nzb"))

	job2, err := q.GetNextJob()
	require.NoError(t, err)
	assert.Equal(t, "/watch/bar.nzb", job2.FilePath)
}

func TestAddJob_IgnoresPendingJob(t *testing.T) {
	q, err := NewQueue(":memory:")
	require.NoError(t, err)

	require.NoError(t, q.AddJob("/watch/baz.nzb", "baz.nzb"))
	// Add again without processing — should be a no-op
	require.NoError(t, q.AddJob("/watch/baz.nzb", "baz.nzb"))

	job, err := q.GetNextJob()
	require.NoError(t, err)
	assert.Equal(t, "/watch/baz.nzb", job.FilePath)

	// Only one job in queue
	_, err = q.GetNextJob()
	assert.ErrorIs(t, err, sql.ErrNoRows)
}
