// Package rendersvc owns the worker facing half of the render pipeline: leasing
// due jobs, publishing results under a fencing token and releasing the shot and
// the quota when a render fails for good.
package rendersvc

import (
	"context"
	"strconv"
	"time"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
	"github.com/vance1852/manjuflow-studio/internal/auditlog"
	"github.com/vance1852/manjuflow-studio/internal/clock"
	"github.com/vance1852/manjuflow-studio/internal/domain/audit"
	"github.com/vance1852/manjuflow-studio/internal/domain/production"
	"github.com/vance1852/manjuflow-studio/internal/domain/prompt"
	"github.com/vance1852/manjuflow-studio/internal/domain/render"
	"github.com/vance1852/manjuflow-studio/internal/repository"
)

// Service implements the render job lifecycle.
type Service struct {
	runner      repository.Runner
	series      repository.SeriesRepository
	shots       repository.ShotRepository
	prompts     repository.PromptRepository
	renders     repository.RenderRepository
	quotas      repository.QuotaRepository
	audits      *auditlog.Recorder
	clock       clock.Clock
	leaseTTL    time.Duration
	backoffBase time.Duration
}

// Options configures the service.
type Options struct {
	Runner      repository.Runner
	Series      repository.SeriesRepository
	Shots       repository.ShotRepository
	Prompts     repository.PromptRepository
	Renders     repository.RenderRepository
	Quotas      repository.QuotaRepository
	Audits      *auditlog.Recorder
	Clock       clock.Clock
	LeaseTTL    time.Duration
	BackoffBase time.Duration
}

// New builds the render service.
func New(opts Options) *Service {
	return &Service{
		runner:      opts.Runner,
		series:      opts.Series,
		shots:       opts.Shots,
		prompts:     opts.Prompts,
		renders:     opts.Renders,
		quotas:      opts.Quotas,
		audits:      opts.Audits,
		clock:       opts.Clock,
		leaseTTL:    opts.LeaseTTL,
		backoffBase: opts.BackoffBase,
	}
}

// Assignment is a leased job together with the frozen prompt body the worker
// must render.
type Assignment struct {
	Job        render.Job
	Generation int
	PromptBody string
	Checksum   string
	SeriesCode string
	Ordinal    int
	ShotTitle  string
}

// ErrNoWork reports an empty queue.
var ErrNoWork = apperr.New(apperr.CodeNotFound, "no render job is due")

// Claim leases the next due job. Expired leases are reclaimed with a bumped
// generation so a stalled worker cannot publish a late result.
func (s *Service) Claim(ctx context.Context, owner string) (Assignment, error) {
	now := s.clock.Now()
	var assignment Assignment
	err := s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		job, err := s.renders.ClaimDueJob(ctx, q, owner, now, s.leaseTTL)
		if err != nil {
			if apperr.IsCode(err, apperr.CodeNotFound) {
				return ErrNoWork
			}
			return err
		}
		version, err := s.prompts.FindVersionByID(ctx, q, job.PromptVersionID)
		if err != nil {
			return err
		}
		if err := version.VerifyIntegrity(); err != nil {
			return err
		}
		shot, err := s.shots.FindByID(ctx, q, job.ShotID)
		if err != nil {
			return err
		}
		series, err := s.series.FindByID(ctx, q, job.SeriesID)
		if err != nil {
			return err
		}
		assignment = Assignment{
			Job:        job,
			Generation: job.LeaseGeneration,
			PromptBody: version.Body,
			Checksum:   version.Checksum,
			SeriesCode: series.Code,
			Ordinal:    shot.Ordinal,
			ShotTitle:  shot.Title,
		}
		return s.audits.Record(ctx, q, systemEvent(job.StudioID,
			auditlog.Success("render_job.leased", audit.ObjectRenderJob, job.ID).
				WithDetail("owner", owner).
				WithDetail("attempt", strconv.Itoa(job.Attempts)).
				WithDetail("generation", strconv.Itoa(job.LeaseGeneration))))
	})
	if err != nil {
		return Assignment{}, err
	}
	return assignment, nil
}

// Complete publishes a successful render: the job finishes, the shot stores the
// artifact and the series moves into review once every shot is rendered.
func (s *Service) Complete(ctx context.Context, assignment Assignment, owner, artifactRef string) error {
	now := s.clock.Now()
	return s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		job, err := s.renders.FindJobByID(ctx, q, assignment.Job.ID)
		if err != nil {
			return err
		}
		if err := job.EnsureLeaseHeld(owner, assignment.Generation); err != nil {
			return err
		}
		if err := job.Succeed(artifactRef, now); err != nil {
			return err
		}
		if err := s.renders.Save(ctx, q, job, assignment.Generation); err != nil {
			return err
		}
		shot, err := s.shots.FindByID(ctx, q, job.ShotID)
		if err != nil {
			return err
		}
		if err := shot.CompleteRender(artifactRef, now); err != nil {
			return err
		}
		if err := s.shots.Save(ctx, q, shot); err != nil {
			return err
		}
		siblings, err := s.shots.ListBySeries(ctx, q, job.SeriesID)
		if err != nil {
			return err
		}
		series, err := s.series.FindByID(ctx, q, job.SeriesID)
		if err != nil {
			return err
		}
		if series.State == production.StateShooting && production.AllRendered(siblings) {
			if err := series.Transition(production.StateReviewing, now); err != nil {
				return err
			}
			if err := s.series.Save(ctx, q, series); err != nil {
				return err
			}
		}
		return s.audits.Record(ctx, q, systemEvent(job.StudioID,
			auditlog.Success("render_job.succeeded", audit.ObjectRenderJob, job.ID).
				WithDetail("artifact_ref", artifactRef).
				WithDetail("attempt", strconv.Itoa(job.Attempts))))
	})
}

// Fail records a render failure. Retries stay queued behind an exponential
// backoff; the final attempt releases both the shot and the consumed quota slot
// so the director can plan a new take.
func (s *Service) Fail(ctx context.Context, assignment Assignment, owner string, cause error) (bool, error) {
	now := s.clock.Now()
	reason := apperr.Message(cause)
	if reason == "" {
		reason = "render failed"
	}
	permanent := false
	err := s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		job, err := s.renders.FindJobByID(ctx, q, assignment.Job.ID)
		if err != nil {
			return err
		}
		if err := job.EnsureLeaseHeld(owner, assignment.Generation); err != nil {
			return err
		}
		exhausted, err := job.Fail(reason, now, s.backoffBase)
		if err != nil {
			return err
		}
		permanent = exhausted
		if err := s.renders.Save(ctx, q, job, assignment.Generation); err != nil {
			return err
		}
		if !exhausted {
			return s.audits.Record(ctx, q, systemEvent(job.StudioID,
				auditlog.Success("render_job.retry_scheduled", audit.ObjectRenderJob, job.ID).
					WithDetail("attempt", strconv.Itoa(job.Attempts)).
					WithDetail("next_attempt_at", job.NextAttemptAt.Format(time.RFC3339)).
					WithDetail("reason", reason)))
		}
		shot, err := s.shots.FindByID(ctx, q, job.ShotID)
		if err != nil {
			return err
		}
		if err := shot.ReleaseRender(now); err != nil {
			return err
		}
		if err := s.shots.Save(ctx, q, shot); err != nil {
			return err
		}
		if err := s.quotas.Release(ctx, q, job.StudioID, job.ReleaseDay(now)); err != nil {
			return err
		}
		return s.audits.Record(ctx, q, systemEvent(job.StudioID,
			auditlog.Success("render_job.failed_permanent", audit.ObjectRenderJob, job.ID).
				WithDetail("attempts", strconv.Itoa(job.Attempts)).
				WithDetail("reason", reason)))
	})
	if err != nil {
		return false, err
	}
	return permanent, nil
}

// RequeueDue promotes retrying jobs whose backoff window elapsed. The sweeper
// calls it so a restarted process picks work up again without operator action.
func (s *Service) RequeueDue(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 20
	}
	now := s.clock.Now()
	promoted := 0
	err := s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		count, err := s.renders.RequeueDue(ctx, q, now, limit)
		if err != nil {
			return err
		}
		promoted = count
		return nil
	})
	if err != nil {
		return 0, err
	}
	return promoted, nil
}

// CancelJob withdraws an unfinished job and releases its resources.
func (s *Service) CancelJob(ctx context.Context, jobID int64, reason string) error {
	now := s.clock.Now()
	return s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		job, err := s.renders.FindJobByID(ctx, q, jobID)
		if err != nil {
			return err
		}
		generation := job.LeaseGeneration
		if err := job.Cancel(now); err != nil {
			return err
		}
		if err := s.renders.Save(ctx, q, job, generation); err != nil {
			return err
		}
		shot, err := s.shots.FindByID(ctx, q, job.ShotID)
		if err != nil {
			return err
		}
		if shot.State == production.ShotRendering {
			if err := shot.ReleaseRender(now); err != nil {
				return err
			}
			if err := s.shots.Save(ctx, q, shot); err != nil {
				return err
			}
		}
		if err := s.quotas.Release(ctx, q, job.StudioID, job.ReleaseDay(now)); err != nil {
			return err
		}
		return s.audits.Record(ctx, q, systemEvent(job.StudioID,
			auditlog.Success("render_job.cancelled", audit.ObjectRenderJob, job.ID).
				WithDetail("reason", reason)))
	})
}

// ArtifactRef derives the deterministic artifact identifier of one attempt.
func ArtifactRef(assignment Assignment) string {
	return "manju://" + assignment.SeriesCode + "/shot-" + strconv.Itoa(assignment.Ordinal) + "/" +
		prompt.Checksum(assignment.PromptBody + "|" + strconv.Itoa(assignment.Job.Attempts))[:16]
}

func systemEvent(studioID int64, event audit.Event) audit.Event {
	prepared := event.Clone()
	prepared.StudioID = studioID
	if prepared.ActorRole == "" {
		prepared.ActorRole = "render_worker"
	}
	return prepared
}
