package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"mediavault/internal/models"

	"github.com/google/uuid"
)

type JobRepo struct {
	db *DB
}

func NewJobRepo(db *DB) *JobRepo {
	return &JobRepo{db: db}
}

// CreateOrGetJob creates a job or returns the existing active job if dedupe_key already exists.
func (r *JobRepo) CreateOrGetJob(ctx context.Context, j *models.Job) (*models.Job, bool, error) {
	now := models.UTCNow()
	if j.ID == "" {
		j.ID = "job_" + uuid.New().String()
	}
	if j.State == "" {
		j.State = "queued"
	}
	if j.Generation <= 0 {
		j.Generation = 1
	}
	if j.CreatedAt == "" {
		j.CreatedAt = now
	}
	j.UpdatedAt = now
	if j.ParamsJSON == "" {
		j.ParamsJSON = "{}"
	}
	if j.ResultJSON == "" {
		j.ResultJSON = "{}"
	}

	var existing models.Job
	var created bool

	err := r.db.ExecWrite(ctx, func(tx *sql.Tx) error {
		// Check for existing active job matching (kind, dedupe_key)
		row := tx.QueryRowContext(ctx, `
			SELECT id, kind, dedupe_key, resource_key, binding_id, state, generation,
			       lease_owner, lease_until, attempts, next_run_at, deadline_at,
			       remote_id, owned_root_id, params_json, result_json, last_error,
			       started_at, completed_at, created_at, updated_at
			FROM jobs
			WHERE kind = ? AND dedupe_key = ? AND state IN ('queued', 'running', 'reconcile', 'retry_wait')
			LIMIT 1
		`, j.Kind, j.DedupeKey)

		err := row.Scan(
			&existing.ID, &existing.Kind, &existing.DedupeKey, &existing.ResourceKey, &existing.BindingID,
			&existing.State, &existing.Generation, &existing.LeaseOwner, &existing.LeaseUntil,
			&existing.Attempts, &existing.NextRunAt, &existing.DeadlineAt, &existing.RemoteID,
			&existing.OwnedRootID, &existing.ParamsJSON, &existing.ResultJSON, &existing.LastError,
			&existing.StartedAt, &existing.CompletedAt, &existing.CreatedAt, &existing.UpdatedAt,
		)

		if err == nil {
			// Found active job
			created = false
			return nil
		}

		if err != sql.ErrNoRows {
			return err
		}

		// Insert new job
		_, err = tx.ExecContext(ctx, `
			INSERT INTO jobs (
				id, kind, dedupe_key, resource_key, binding_id, state, generation,
				lease_owner, lease_until, attempts, next_run_at, deadline_at,
				remote_id, owned_root_id, params_json, result_json, last_error,
				started_at, completed_at, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			j.ID, j.Kind, j.DedupeKey, j.ResourceKey, j.BindingID, j.State, j.Generation,
			j.LeaseOwner, j.LeaseUntil, j.Attempts, j.NextRunAt, j.DeadlineAt,
			j.RemoteID, j.OwnedRootID, j.ParamsJSON, j.ResultJSON, j.LastError,
			j.StartedAt, j.CompletedAt, j.CreatedAt, j.UpdatedAt,
		)
		if err != nil {
			return err
		}
		created = true
		existing = *j
		return nil
	})

	if err != nil {
		return nil, false, fmt.Errorf("create or get job: %w", err)
	}

	return &existing, created, nil
}

// ClaimNextJob claims the next due job of specified kind with a lease.
func (r *JobRepo) ClaimNextJob(ctx context.Context, kind, leaseOwner string, leaseDuration time.Duration, now string) (*models.Job, error) {
	leaseUntil := time.Now().UTC().Add(leaseDuration).Format(time.RFC3339)

	var claimed models.Job
	var found bool

	err := r.db.ExecWrite(ctx, func(tx *sql.Tx) error {
		// Find due job
		row := tx.QueryRowContext(ctx, `
			SELECT id, kind, dedupe_key, resource_key, binding_id, state, generation,
			       lease_owner, lease_until, attempts, next_run_at, deadline_at,
			       remote_id, owned_root_id, params_json, result_json, last_error,
			       started_at, completed_at, created_at, updated_at
			FROM jobs
			WHERE kind = ? AND state IN ('queued', 'retry_wait')
			  AND (next_run_at IS NULL OR next_run_at <= ?)
			ORDER BY created_at ASC
			LIMIT 1
		`, kind, now)

		err := row.Scan(
			&claimed.ID, &claimed.Kind, &claimed.DedupeKey, &claimed.ResourceKey, &claimed.BindingID,
			&claimed.State, &claimed.Generation, &claimed.LeaseOwner, &claimed.LeaseUntil,
			&claimed.Attempts, &claimed.NextRunAt, &claimed.DeadlineAt, &claimed.RemoteID,
			&claimed.OwnedRootID, &claimed.ParamsJSON, &claimed.ResultJSON, &claimed.LastError,
			&claimed.StartedAt, &claimed.CompletedAt, &claimed.CreatedAt, &claimed.UpdatedAt,
		)

		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}

		// Update job to running and increment generation
		newGen := claimed.Generation + 1
		res, err := tx.ExecContext(ctx, `
			UPDATE jobs
			SET state = 'running', lease_owner = ?, lease_until = ?, generation = ?,
			    attempts = attempts + 1, started_at = COALESCE(started_at, ?), updated_at = ?
			WHERE id = ? AND generation = ?
		`, leaseOwner, leaseUntil, newGen, now, now, claimed.ID, claimed.Generation)
		if err != nil {
			return err
		}
		rows, _ := res.RowsAffected()
		if rows == 0 {
			// Concurrent claim race
			return nil
		}

		claimed.State = "running"
		claimed.LeaseOwner = &leaseOwner
		claimed.LeaseUntil = &leaseUntil
		claimed.Generation = newGen
		claimed.Attempts++
		claimed.UpdatedAt = now
		found = true
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("claim next job: %w", err)
	}

	if !found {
		return nil, nil // No jobs due
	}
	return &claimed, nil
}

// ClaimJobByID attempts to claim a specific job by ID.
func (r *JobRepo) ClaimJobByID(ctx context.Context, jobID, leaseOwner string, leaseDuration time.Duration) (*models.Job, error) {
	now := models.UTCNow()
	leaseUntil := time.Now().UTC().Add(leaseDuration).Format(time.RFC3339)

	var claimed models.Job
	var found bool

	err := r.db.ExecWrite(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `
			SELECT id, kind, dedupe_key, resource_key, binding_id, state, generation,
			       lease_owner, lease_until, attempts, next_run_at, deadline_at,
			       remote_id, owned_root_id, params_json, result_json, last_error,
			       started_at, completed_at, created_at, updated_at
			FROM jobs
			WHERE id = ? AND state IN ('queued', 'retry_wait')
		`, jobID)

		err := row.Scan(
			&claimed.ID, &claimed.Kind, &claimed.DedupeKey, &claimed.ResourceKey, &claimed.BindingID,
			&claimed.State, &claimed.Generation, &claimed.LeaseOwner, &claimed.LeaseUntil,
			&claimed.Attempts, &claimed.NextRunAt, &claimed.DeadlineAt, &claimed.RemoteID,
			&claimed.OwnedRootID, &claimed.ParamsJSON, &claimed.ResultJSON, &claimed.LastError,
			&claimed.StartedAt, &claimed.CompletedAt, &claimed.CreatedAt, &claimed.UpdatedAt,
		)
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}

		newGen := claimed.Generation + 1
		res, err := tx.ExecContext(ctx, `
			UPDATE jobs
			SET state = 'running', lease_owner = ?, lease_until = ?, generation = ?,
			    attempts = attempts + 1, started_at = COALESCE(started_at, ?), updated_at = ?
			WHERE id = ? AND generation = ?
		`, leaseOwner, leaseUntil, newGen, now, now, claimed.ID, claimed.Generation)
		if err != nil {
			return err
		}
		rows, _ := res.RowsAffected()
		if rows == 0 {
			return nil
		}

		claimed.State = "running"
		claimed.LeaseOwner = &leaseOwner
		claimed.LeaseUntil = &leaseUntil
		claimed.Generation = newGen
		claimed.Attempts++
		claimed.UpdatedAt = now
		found = true
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("claim job by id: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("job %s could not be claimed (not in queued/retry_wait or race)", jobID)
	}
	return &claimed, nil
}

// RenewLease extends lease expiration for a running job with matching generation and leaseOwner.
func (r *JobRepo) RenewLease(ctx context.Context, jobID, leaseOwner string, generation int, leaseDuration time.Duration) error {
	now := models.UTCNow()
	leaseUntil := time.Now().UTC().Add(leaseDuration).Format(time.RFC3339)

	return r.db.ExecWrite(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE jobs
			SET lease_until = ?, updated_at = ?
			WHERE id = ? AND lease_owner = ? AND generation = ? AND state = 'running'
		`, leaseUntil, now, jobID, leaseOwner, generation)
		if err != nil {
			return err
		}
		rows, _ := res.RowsAffected()
		if rows == 0 {
			return fmt.Errorf("job lease renewal failed (generation or owner mismatch or job not running)")
		}
		return nil
	})
}

// FinishJob marks a job completed with succeeded, failed, or cancelled state.
func (r *JobRepo) FinishJob(ctx context.Context, jobID, state, resultJSON string, lastError *string) error {
	now := models.UTCNow()
	return r.db.ExecWrite(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE jobs
			SET state = ?, result_json = ?, last_error = ?, completed_at = ?,
			    lease_owner = NULL, lease_until = NULL, updated_at = ?
			WHERE id = ?
		`, state, resultJSON, lastError, now, now, jobID)
		return err
	})
}

// SetJobReconcile marks an in-flight job whose remote status is unknown to reconcile state.
func (r *JobRepo) SetJobReconcile(ctx context.Context, jobID, remoteID, errReason string) error {
	now := models.UTCNow()
	return r.db.ExecWrite(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE jobs
			SET state = 'reconcile', remote_id = CASE WHEN ? != '' THEN ? ELSE remote_id END,
			    last_error = ?, lease_owner = NULL, lease_until = NULL, updated_at = ?
			WHERE id = ?
		`, remoteID, remoteID, errReason, now, jobID)
		return err
	})
}

// GetJobByID retrieves a job by its ID.
func (r *JobRepo) GetJobByID(ctx context.Context, id string) (*models.Job, error) {
	var j models.Job
	err := r.db.ExecRead(ctx, func(database *sql.DB) error {
		row := database.QueryRowContext(ctx, `
			SELECT id, kind, dedupe_key, resource_key, binding_id, state, generation,
			       lease_owner, lease_until, attempts, next_run_at, deadline_at,
			       remote_id, owned_root_id, params_json, result_json, last_error,
			       started_at, completed_at, created_at, updated_at
			FROM jobs WHERE id = ?
		`, id)
		return row.Scan(
			&j.ID, &j.Kind, &j.DedupeKey, &j.ResourceKey, &j.BindingID, &j.State, &j.Generation,
			&j.LeaseOwner, &j.LeaseUntil, &j.Attempts, &j.NextRunAt, &j.DeadlineAt,
			&j.RemoteID, &j.OwnedRootID, &j.ParamsJSON, &j.ResultJSON, &j.LastError,
			&j.StartedAt, &j.CompletedAt, &j.CreatedAt, &j.UpdatedAt,
		)
	})
	if err != nil {
		return nil, err
	}
	return &j, nil
}
