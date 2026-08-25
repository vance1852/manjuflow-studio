package sqliterepo

import (
	"context"
	"database/sql"
	"time"

	"github.com/vance1852/manjuflow-studio/internal/domain/render"
	"github.com/vance1852/manjuflow-studio/internal/repository"
)

// RenderStore implements repository.RenderRepository.
type RenderStore struct{}

// NewRenderStore builds the render job repository.
func NewRenderStore() RenderStore { return RenderStore{} }

const jobColumns = `id, studio_id, series_id, shot_id, prompt_version_id, quota_day, state, attempts, max_attempts,
	next_attempt_at, lease_owner, lease_expires_at, lease_generation, last_error, artifact_ref, requested_by,
	created_at, updated_at, finished_at`

// CreateJob inserts a render job. The partial unique index on active jobs makes
// a second live job for the same shot a conflict instead of a duplicate render.
func (RenderStore) CreateJob(ctx context.Context, q repository.Querier, job render.Job) (int64, error) {
	return insert(ctx, q, "cannot create render job",
		`INSERT INTO render_jobs (studio_id, series_id, shot_id, prompt_version_id, quota_day, state, attempts,
		 max_attempts, next_attempt_at, lease_owner, lease_expires_at, lease_generation, last_error, artifact_ref,
		 requested_by, created_at, updated_at, finished_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.StudioID, job.SeriesID, job.ShotID, job.PromptVersionID, job.QuotaDay, string(job.State),
		job.Attempts, job.MaxAttempts, encodeTime(job.NextAttemptAt), job.LeaseOwner,
		encodeNullableTime(job.LeaseExpiresAt), job.LeaseGeneration, job.LastError, job.ArtifactRef,
		job.RequestedBy, encodeTime(job.CreatedAt), encodeTime(job.UpdatedAt), encodeNullableTime(job.FinishedAt))
}

// FindJobByID loads a job by identifier.
func (RenderStore) FindJobByID(ctx context.Context, q repository.Querier, id int64) (render.Job, error) {
	row := q.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM render_jobs WHERE id = ?`, id)
	return scanJob(row, id)
}

// FindActiveJobForShot loads the unfinished job of a shot when one exists.
func (RenderStore) FindActiveJobForShot(ctx context.Context, q repository.Querier, shotID int64) (render.Job, error) {
	row := q.QueryRowContext(ctx,
		`SELECT `+jobColumns+` FROM render_jobs
		 WHERE shot_id = ? AND state IN ('queued', 'leased', 'retrying')`, shotID)
	return scanJob(row, shotID)
}

// Save persists a job using the lease generation as the fencing token. A worker
// whose lease was reclaimed cannot publish its stale result.
func (RenderStore) Save(ctx context.Context, q repository.Querier, job render.Job, expectedGeneration int) error {
	affected, err := execExpectingRow(ctx, q, "cannot update render job",
		`UPDATE render_jobs SET state = ?, attempts = ?, next_attempt_at = ?, lease_owner = ?, lease_expires_at = ?,
		 lease_generation = ?, last_error = ?, artifact_ref = ?, updated_at = ?, finished_at = ?
		 WHERE id = ? AND lease_generation = ?`,
		string(job.State), job.Attempts, encodeTime(job.NextAttemptAt), job.LeaseOwner,
		encodeNullableTime(job.LeaseExpiresAt), job.LeaseGeneration, job.LastError, job.ArtifactRef,
		encodeTime(job.UpdatedAt), encodeNullableTime(job.FinishedAt), job.ID, expectedGeneration)
	if err != nil {
		return err
	}
	if affected == 0 {
		return staleWrite("render job", job.ID, expectedGeneration)
	}
	return nil
}

// ClaimDueJob leases the oldest due job in one conditional statement so two
// workers cannot hold the same job. Expired leases are reclaimed with a bumped
// generation.
func (RenderStore) ClaimDueJob(ctx context.Context, q repository.Querier, owner string, now time.Time, leaseTTL time.Duration) (render.Job, error) {
	nowText := encodeTime(now)
	expiryText := encodeTime(now.Add(leaseTTL))
	row := q.QueryRowContext(ctx,
		`UPDATE render_jobs SET state = 'leased', lease_owner = ?, lease_expires_at = ?,
		 lease_generation = lease_generation + 1, attempts = attempts + 1, updated_at = ?
		 WHERE id = (
		     SELECT id FROM render_jobs
		     WHERE (state IN ('queued', 'retrying') AND next_attempt_at <= ?)
		        OR (state = 'leased' AND lease_expires_at IS NOT NULL AND lease_expires_at < ?)
		     ORDER BY next_attempt_at ASC, id ASC
		     LIMIT 1
		 )
		 RETURNING `+jobColumns,
		owner, expiryText, nowText, nowText, nowText)
	job, err := scanJob(row, owner)
	if err != nil {
		return render.Job{}, err
	}
	return job, nil
}

// RequeueDue promotes retrying jobs whose backoff window elapsed.
func (RenderStore) RequeueDue(ctx context.Context, q repository.Querier, now time.Time, limit int) (int, error) {
	nowText := encodeTime(now)
	affected, err := execExpectingRow(ctx, q, "cannot requeue render jobs",
		`UPDATE render_jobs SET state = 'queued', updated_at = ?
		 WHERE id IN (
		     SELECT id FROM render_jobs
		     WHERE state = 'retrying' AND next_attempt_at <= ?
		     ORDER BY next_attempt_at ASC, id ASC
		     LIMIT ?
		 )`, nowText, nowText, limit)
	if err != nil {
		return 0, err
	}
	return int(affected), nil
}

// ListByShot returns every job of a shot, newest first.
func (RenderStore) ListByShot(ctx context.Context, q repository.Querier, shotID int64) ([]render.Job, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+jobColumns+` FROM render_jobs WHERE shot_id = ? ORDER BY id DESC`, shotID)
	if err != nil {
		return nil, wrap(err, "cannot list render jobs")
	}
	defer func() { _ = rows.Close() }()

	var out []render.Job
	for rows.Next() {
		job, err := scanJobRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, job)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap(err, "cannot iterate render jobs")
	}
	return out, nil
}

// CountUnfinishedForPromptVersion counts jobs that still depend on a version.
func (RenderStore) CountUnfinishedForPromptVersion(ctx context.Context, q repository.Querier, promptVersionID int64) (int, error) {
	return countRows(ctx, q, "cannot count render jobs",
		`SELECT COUNT(*) FROM render_jobs WHERE prompt_version_id = ? AND state IN ('queued', 'leased', 'retrying')`,
		promptVersionID)
}

func scanJob(row *sql.Row, key any) (render.Job, error) {
	var (
		job        render.Job
		state      string
		nextAt     string
		leaseUntil sql.NullString
		createdAt  string
		updatedAt  string
		finishedAt sql.NullString
	)
	err := row.Scan(&job.ID, &job.StudioID, &job.SeriesID, &job.ShotID, &job.PromptVersionID, &job.QuotaDay,
		&state, &job.Attempts, &job.MaxAttempts, &nextAt, &job.LeaseOwner, &leaseUntil, &job.LeaseGeneration,
		&job.LastError, &job.ArtifactRef, &job.RequestedBy, &createdAt, &updatedAt, &finishedAt)
	if err != nil {
		if noRows(err) {
			return render.Job{}, repository.NotFound("render job", key)
		}
		return render.Job{}, wrap(err, "cannot read render job")
	}
	return hydrateJob(job, state, nextAt, leaseUntil, createdAt, updatedAt, finishedAt)
}

func scanJobRows(rows *sql.Rows) (render.Job, error) {
	var (
		job        render.Job
		state      string
		nextAt     string
		leaseUntil sql.NullString
		createdAt  string
		updatedAt  string
		finishedAt sql.NullString
	)
	err := rows.Scan(&job.ID, &job.StudioID, &job.SeriesID, &job.ShotID, &job.PromptVersionID, &job.QuotaDay,
		&state, &job.Attempts, &job.MaxAttempts, &nextAt, &job.LeaseOwner, &leaseUntil, &job.LeaseGeneration,
		&job.LastError, &job.ArtifactRef, &job.RequestedBy, &createdAt, &updatedAt, &finishedAt)
	if err != nil {
		return render.Job{}, wrap(err, "cannot scan render job")
	}
	return hydrateJob(job, state, nextAt, leaseUntil, createdAt, updatedAt, finishedAt)
}

func hydrateJob(job render.Job, state, nextAt string, leaseUntil sql.NullString, createdAt, updatedAt string, finishedAt sql.NullString) (render.Job, error) {
	var err error
	if job.NextAttemptAt, err = decodeTime(nextAt); err != nil {
		return render.Job{}, err
	}
	if job.CreatedAt, err = decodeTime(createdAt); err != nil {
		return render.Job{}, err
	}
	if job.UpdatedAt, err = decodeTime(updatedAt); err != nil {
		return render.Job{}, err
	}
	if job.LeaseExpiresAt, err = decodeNullableTime(leaseUntil); err != nil {
		return render.Job{}, err
	}
	if job.FinishedAt, err = decodeNullableTime(finishedAt); err != nil {
		return render.Job{}, err
	}
	job.State = render.JobState(state)
	return job.Clone(), nil
}

// QuotaStore implements repository.QuotaRepository.
type QuotaStore struct{}

// NewQuotaStore builds the daily render quota repository.
func NewQuotaStore() QuotaStore { return QuotaStore{} }

// Ensure creates the ledger row for a business day when it is missing.
func (QuotaStore) Ensure(ctx context.Context, q repository.Querier, studioID int64, day string, capacity int) error {
	_, err := execExpectingRow(ctx, q, "cannot prepare render quota",
		`INSERT INTO render_quotas (studio_id, quota_day, capacity, used, updated_at)
		 VALUES (?, ?, ?, 0, ?)
		 ON CONFLICT (studio_id, quota_day) DO UPDATE SET capacity = excluded.capacity`,
		studioID, day, capacity, encodeTime(time.Now()))
	return err
}

// TryConsume takes one slot with a conditional update. It returns false when the
// day is exhausted, which keeps two concurrent submissions from oversubscribing.
func (QuotaStore) TryConsume(ctx context.Context, q repository.Querier, studioID int64, day string) (bool, error) {
	affected, err := execExpectingRow(ctx, q, "cannot consume render quota",
		`UPDATE render_quotas SET used = used + 1, updated_at = ?
		 WHERE studio_id = ? AND quota_day = ? AND used < capacity`,
		encodeTime(time.Now()), studioID, day)
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// Release returns one slot to the ledger without dropping below zero.
func (QuotaStore) Release(ctx context.Context, q repository.Querier, studioID int64, day string) error {
	_, err := execExpectingRow(ctx, q, "cannot release render quota",
		`UPDATE render_quotas SET used = used - 1, updated_at = ?
		 WHERE studio_id = ? AND quota_day = ? AND used > 0`,
		encodeTime(time.Now()), studioID, day)
	return err
}

// Get reads the ledger row for one business day.
func (QuotaStore) Get(ctx context.Context, q repository.Querier, studioID int64, day string) (repository.Quota, error) {
	var quota repository.Quota
	err := q.QueryRowContext(ctx,
		`SELECT studio_id, quota_day, capacity, used FROM render_quotas WHERE studio_id = ? AND quota_day = ?`,
		studioID, day).Scan(&quota.StudioID, &quota.Day, &quota.Capacity, &quota.Used)
	if err != nil {
		if noRows(err) {
			return repository.Quota{}, repository.NotFound("render quota", day)
		}
		return repository.Quota{}, wrap(err, "cannot read render quota")
	}
	return quota, nil
}
