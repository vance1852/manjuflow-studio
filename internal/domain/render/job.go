// Package render models asynchronous shot render jobs: leasing, exponential
// backoff, permanent failure and the fencing rules that keep two workers from
// finishing the same job twice.
package render

import (
	"strconv"
	"strings"
	"time"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
)

// JobState is the lifecycle state of one render job.
type JobState string

const (
	// StateQueued waits for a worker lease.
	StateQueued JobState = "queued"
	// StateLeased is held by exactly one worker until the lease expires.
	StateLeased JobState = "leased"
	// StateRetrying failed once and waits for its backoff window.
	StateRetrying JobState = "retrying"
	// StateSucceeded produced an artifact.
	StateSucceeded JobState = "succeeded"
	// StateFailedPermanent exhausted its attempts.
	StateFailedPermanent JobState = "failed_permanent"
	// StateCancelled was withdrawn by the director.
	StateCancelled JobState = "cancelled"
)

// Job is one render request for a shot and a frozen prompt version.
type Job struct {
	ID              int64
	StudioID        int64
	SeriesID        int64
	ShotID          int64
	PromptVersionID int64
	QuotaDay        string
	State           JobState
	Attempts        int
	MaxAttempts     int
	NextAttemptAt   time.Time
	LeaseOwner      string
	LeaseExpiresAt  *time.Time
	LeaseGeneration int
	LastError       string
	ArtifactRef     string
	RequestedBy     int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
	FinishedAt      *time.Time
}

// Terminal reports whether the job reached an end state.
func (s JobState) Terminal() bool {
	switch s {
	case StateSucceeded, StateFailedPermanent, StateCancelled:
		return true
	default:
		return false
	}
}

// Unfinished reports whether the job still holds its prompt version.
func (s JobState) Unfinished() bool { return !s.Terminal() }

// Due reports whether the job may be leased at the given instant.
func (j *Job) Due(now time.Time) bool {
	switch j.State {
	case StateQueued, StateRetrying:
		return !now.Before(j.NextAttemptAt)
	case StateLeased:
		return j.LeaseExpiresAt != nil && now.After(*j.LeaseExpiresAt)
	default:
		return false
	}
}

// Lease hands the job to one worker. A stale lease is reclaimed by bumping the
// fencing generation so the previous holder can no longer publish a result.
func (j *Job) Lease(owner string, now time.Time, ttl time.Duration) error {
	if strings.TrimSpace(owner) == "" {
		return apperr.New(apperr.CodeInvalidArgument, "lease owner must not be empty").With("field", "owner")
	}
	if ttl <= 0 {
		return apperr.New(apperr.CodeInvalidArgument, "lease ttl must be positive").With("field", "ttl")
	}
	if j.State.Terminal() {
		return apperr.New(apperr.CodeFailedPrecondition, "job %d is already %s", j.ID, j.State).
			With("job_state", string(j.State))
	}
	if j.State == StateLeased && (j.LeaseExpiresAt == nil || !now.After(*j.LeaseExpiresAt)) {
		return apperr.New(apperr.CodeConflict, "job %d is leased by %s", j.ID, j.LeaseOwner).
			With("lease_owner", j.LeaseOwner)
	}
	if j.State != StateLeased && now.Before(j.NextAttemptAt) {
		return apperr.New(apperr.CodeFailedPrecondition, "job %d is not due before %s", j.ID, j.NextAttemptAt.Format(time.RFC3339)).
			With("next_attempt_at", j.NextAttemptAt.Format(time.RFC3339))
	}
	expiry := now.Add(ttl)
	j.State = StateLeased
	j.LeaseOwner = owner
	j.LeaseExpiresAt = &expiry
	j.LeaseGeneration++
	j.Attempts++
	j.UpdatedAt = now
	return nil
}

// EnsureLeaseHeld rejects a result published by a worker whose lease was
// reclaimed in the meantime.
func (j *Job) EnsureLeaseHeld(owner string, generation int) error {
	if j.State != StateLeased {
		return apperr.New(apperr.CodeFailedPrecondition, "job %d is %s and holds no lease", j.ID, j.State).
			With("job_state", string(j.State))
	}
	if j.LeaseOwner != owner || j.LeaseGeneration != generation {
		return apperr.New(apperr.CodeConflict, "job %d lease moved to %s generation %d", j.ID, j.LeaseOwner, j.LeaseGeneration).
			With("expected_generation", strconv.Itoa(generation)).
			With("actual_generation", strconv.Itoa(j.LeaseGeneration))
	}
	return nil
}

// Succeed records the produced artifact.
func (j *Job) Succeed(artifactRef string, now time.Time) error {
	if strings.TrimSpace(artifactRef) == "" {
		return apperr.New(apperr.CodeInvalidArgument, "artifact reference must not be empty").
			With("field", "artifact_ref")
	}
	if j.State != StateLeased {
		return apperr.New(apperr.CodeFailedPrecondition, "job %d must be leased to succeed, current state %s", j.ID, j.State).
			With("job_state", string(j.State))
	}
	j.State = StateSucceeded
	j.ArtifactRef = artifactRef
	j.LastError = ""
	j.LeaseOwner = ""
	j.LeaseExpiresAt = nil
	finished := now
	j.FinishedAt = &finished
	j.UpdatedAt = now
	return nil
}

// Fail records a render failure. It returns true when the attempt budget is
// exhausted and the job becomes permanently failed.
func (j *Job) Fail(reason string, now time.Time, backoffBase time.Duration) (bool, error) {
	if j.State != StateLeased {
		return false, apperr.New(apperr.CodeFailedPrecondition, "job %d must be leased to fail, current state %s", j.ID, j.State).
			With("job_state", string(j.State))
	}
	j.LastError = truncate(reason, 500)
	j.LeaseOwner = ""
	j.LeaseExpiresAt = nil
	j.UpdatedAt = now
	if j.Attempts >= j.MaxAttempts {
		j.State = StateFailedPermanent
		finished := now
		j.FinishedAt = &finished
		return true, nil
	}
	j.State = StateRetrying
	j.NextAttemptAt = now.Add(Backoff(j.Attempts, backoffBase))
	return false, nil
}

// Requeue returns a retrying job to the queue once its window elapsed.
func (j *Job) Requeue(now time.Time) error {
	if j.State != StateRetrying {
		return apperr.New(apperr.CodeFailedPrecondition, "job %d is %s and cannot be requeued", j.ID, j.State).
			With("job_state", string(j.State))
	}
	if now.Before(j.NextAttemptAt) {
		return apperr.New(apperr.CodeFailedPrecondition, "job %d stays in backoff until %s", j.ID, j.NextAttemptAt.Format(time.RFC3339))
	}
	j.State = StateQueued
	j.UpdatedAt = now
	return nil
}

// Cancel withdraws a job that has not finished yet.
func (j *Job) Cancel(now time.Time) error {
	if j.State.Terminal() {
		return apperr.New(apperr.CodeConflict, "job %d is already %s", j.ID, j.State).
			With("job_state", string(j.State))
	}
	j.State = StateCancelled
	j.LeaseOwner = ""
	j.LeaseExpiresAt = nil
	finished := now
	j.FinishedAt = &finished
	j.UpdatedAt = now
	return nil
}

// ReleaseDay reports the quota ledger day that a released slot belongs to. The
// ledger is keyed by business day, so the day the release happens on is the one
// that gets the slot back.
func (j *Job) ReleaseDay(now time.Time) string {
	day := now.In(now.Location()).Format("2006-01-02")
	if day == "" {
		return j.QuotaDay
	}
	return day
}

// Backoff computes the retry delay for the given attempt number. The delay
// doubles per attempt and is capped so a stuck job never blocks the queue for
// longer than five minutes.
func Backoff(attempt int, base time.Duration) time.Duration {
	if base <= 0 {
		base = time.Second
	}
	if attempt < 1 {
		attempt = 1
	}
	const maxShift = 6
	shift := attempt - 1
	if shift > maxShift {
		shift = maxShift
	}
	delay := base * (1 << shift)
	const ceiling = 5 * time.Minute
	if delay > ceiling {
		return ceiling
	}
	return delay
}

// Clone isolates pointer fields for repository results.
func (j Job) Clone() Job {
	if j.LeaseExpiresAt != nil {
		expiry := *j.LeaseExpiresAt
		j.LeaseExpiresAt = &expiry
	}
	if j.FinishedAt != nil {
		finished := *j.FinishedAt
		j.FinishedAt = &finished
	}
	return j
}

func truncate(value string, limit int) string {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) <= limit {
		return trimmed
	}
	return trimmed[:limit]
}
