// Package productionsvc owns the director facing production pipeline: series,
// storyboard shots, prompt binding, render submission, review and publication.
package productionsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
	"github.com/vance1852/manjuflow-studio/internal/auditlog"
	"github.com/vance1852/manjuflow-studio/internal/clock"
	"github.com/vance1852/manjuflow-studio/internal/domain/audit"
	"github.com/vance1852/manjuflow-studio/internal/domain/identity"
	"github.com/vance1852/manjuflow-studio/internal/domain/production"
	"github.com/vance1852/manjuflow-studio/internal/domain/prompt"
	"github.com/vance1852/manjuflow-studio/internal/domain/render"
	"github.com/vance1852/manjuflow-studio/internal/idempotency"
	"github.com/vance1852/manjuflow-studio/internal/repository"
	"github.com/vance1852/manjuflow-studio/internal/reqctx"
)

// SeriesSequence is the business sequence name backing series codes.
const SeriesSequence = "series"

// Service implements the production use cases.
type Service struct {
	runner      repository.Runner
	studios     repository.StudioRepository
	series      repository.SeriesRepository
	shots       repository.ShotRepository
	prompts     repository.PromptRepository
	renders     repository.RenderRepository
	quotas      repository.QuotaRepository
	teaching    repository.TeachingRepository
	sequences   repository.SequenceRepository
	audits      *auditlog.Recorder
	guard       *idempotency.Guard
	clock       clock.Clock
	maxAttempts int
}

// Options configures the service.
type Options struct {
	Runner      repository.Runner
	Studios     repository.StudioRepository
	Series      repository.SeriesRepository
	Shots       repository.ShotRepository
	Prompts     repository.PromptRepository
	Renders     repository.RenderRepository
	Quotas      repository.QuotaRepository
	Teaching    repository.TeachingRepository
	Sequences   repository.SequenceRepository
	Audits      *auditlog.Recorder
	Guard       *idempotency.Guard
	Clock       clock.Clock
	MaxAttempts int
}

// New builds the production service.
func New(opts Options) *Service {
	return &Service{
		runner:      opts.Runner,
		studios:     opts.Studios,
		series:      opts.Series,
		shots:       opts.Shots,
		prompts:     opts.Prompts,
		renders:     opts.Renders,
		quotas:      opts.Quotas,
		teaching:    opts.Teaching,
		sequences:   opts.Sequences,
		audits:      opts.Audits,
		guard:       opts.Guard,
		clock:       opts.Clock,
		maxAttempts: opts.MaxAttempts,
	}
}

// CreateSeriesInput describes a new production unit.
type CreateSeriesInput struct {
	Title      string
	Logline    string
	CodePrefix string
}

// CreateSeries registers a series and issues its gap free business code.
func (s *Service) CreateSeries(ctx context.Context, input CreateSeriesInput) (production.Series, error) {
	principal, err := s.authorize(ctx, identity.CapManageSeries)
	if err != nil {
		return production.Series{}, err
	}
	title, err := production.ValidateTitle(input.Title)
	if err != nil {
		return production.Series{}, err
	}
	logline, err := production.ValidateLogline(input.Logline)
	if err != nil {
		return production.Series{}, err
	}
	prefix, err := production.ValidateCodePrefix(input.CodePrefix)
	if err != nil {
		return production.Series{}, err
	}
	now := s.clock.Now()
	var created production.Series
	err = s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		next, err := s.sequences.Next(ctx, q, principal.StudioID, SeriesSequence)
		if err != nil {
			return err
		}
		series := production.Series{
			StudioID:  principal.StudioID,
			Code:      fmt.Sprintf("%s-%04d", prefix, next),
			Title:     title,
			Logline:   logline,
			State:     production.StateDraft,
			Version:   1,
			CreatedBy: principal.UserID,
			CreatedAt: now,
			UpdatedAt: now,
		}
		id, err := s.series.Create(ctx, q, series)
		if err != nil {
			return err
		}
		series.ID = id
		created = series
		return s.audits.Record(ctx, q, auditlog.Success("series.created", audit.ObjectSeries, id).
			WithDetail("code", series.Code).
			WithDetail("title", series.Title))
	})
	if err != nil {
		return production.Series{}, err
	}
	return created, nil
}

// PlanResult reports the outcome of one planned shot in a batch.
type PlanResult struct {
	Ordinal int
	ShotID  int64
	Created bool
	Error   string
}

// PlanShots creates several storyboard shots in one call. Each draft is applied
// independently: a rejected ordinal does not discard the accepted ones, and the
// caller receives one result per item.
func (s *Service) PlanShots(ctx context.Context, seriesID int64, drafts []production.Draft) ([]PlanResult, error) {
	principal, err := s.authorize(ctx, identity.CapManageSeries)
	if err != nil {
		return nil, err
	}
	if len(drafts) == 0 {
		return nil, apperr.New(apperr.CodeInvalidArgument, "at least one shot must be planned").With("field", "shots")
	}
	if len(drafts) > 50 {
		return nil, apperr.New(apperr.CodeInvalidArgument, "at most 50 shots can be planned per call").With("field", "shots")
	}
	now := s.clock.Now()
	results := make([]PlanResult, 0, len(drafts))
	err = s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		series, err := s.loadSeries(ctx, q, seriesID, principal.StudioID)
		if err != nil {
			return err
		}
		if err := series.EnsureAcceptsPlanning(); err != nil {
			return err
		}
		accepted := 0
		for _, draft := range drafts {
			result := PlanResult{Ordinal: draft.Ordinal}
			if err := production.ValidateDraft(draft); err != nil {
				result.Error = apperr.Message(err)
				results = append(results, result)
				continue
			}
			shot := production.Shot{
				SeriesID:  series.ID,
				Ordinal:   draft.Ordinal,
				Title:     draft.Title,
				Direction: draft.Direction,
				State:     production.ShotDraft,
				Version:   1,
				CreatedAt: now,
				UpdatedAt: now,
			}
			id, err := s.shots.Create(ctx, q, shot)
			if err != nil {
				if apperr.IsCode(err, apperr.CodeConflict) {
					result.Error = fmt.Sprintf("shot %d already exists in this series", draft.Ordinal)
					results = append(results, result)
					continue
				}
				return err
			}
			result.ShotID = id
			result.Created = true
			accepted++
			results = append(results, result)
		}
		if accepted == 0 {
			return apperr.New(apperr.CodeInvalidArgument, "no shot in the batch could be planned").
				With("field", "shots")
		}
		series.UpdatedAt = now
		if err := s.series.Save(ctx, q, series); err != nil {
			return err
		}
		return s.audits.Record(ctx, q, auditlog.Success("series.shots_planned", audit.ObjectSeries, series.ID).
			WithDetail("accepted", strconv.Itoa(accepted)).
			WithDetail("submitted", strconv.Itoa(len(drafts))))
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

// BindPrompt attaches an active prompt version to a shot.
func (s *Service) BindPrompt(ctx context.Context, shotID, promptVersionID int64) (production.Shot, error) {
	principal, err := s.authorize(ctx, identity.CapBindPromptVersion)
	if err != nil {
		return production.Shot{}, err
	}
	now := s.clock.Now()
	var bound production.Shot
	err = s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		shot, series, err := s.loadShot(ctx, q, shotID, principal.StudioID)
		if err != nil {
			return err
		}
		if err := series.EnsureAcceptsPlanning(); err != nil {
			return err
		}
		version, err := s.loadPromptVersion(ctx, q, promptVersionID, principal.StudioID)
		if err != nil {
			return err
		}
		if err := version.EnsureBindable(); err != nil {
			return err
		}
		if err := shot.Bind(version.ID, now); err != nil {
			return err
		}
		if err := s.shots.Save(ctx, q, shot); err != nil {
			return err
		}
		bound = shot
		return s.audits.Record(ctx, q, auditlog.Success("shot.prompt_bound", audit.ObjectShot, shot.ID).
			WithDetail("series_code", series.Code).
			WithDetail("prompt_version_id", strconv.FormatInt(version.ID, 10)).
			WithDetail("ordinal", strconv.Itoa(shot.Ordinal)))
	})
	if err != nil {
		return production.Shot{}, err
	}
	return bound, nil
}

// SubmitRenderResult is the response of a render submission.
type SubmitRenderResult struct {
	JobID    int64  `json:"job_id"`
	ShotID   int64  `json:"shot_id"`
	State    string `json:"state"`
	QuotaDay string `json:"quota_day"`
	Replayed bool   `json:"-"`
}

// SubmitRender queues a render job for a bound shot. The daily studio quota is
// consumed with a conditional update, the shot moves into rendering and the
// series starts shooting, all inside one transaction. Submitting twice with the
// same idempotency key replays the first response instead of burning a slot.
func (s *Service) SubmitRender(ctx context.Context, shotID int64, idempotencyKey string) (SubmitRenderResult, error) {
	principal, err := s.authorize(ctx, identity.CapSubmitRender)
	if err != nil {
		return SubmitRenderResult{}, err
	}
	request := idempotency.Request{
		StudioID: principal.StudioID,
		Method:   "POST",
		Path:     "/v1/shots/render",
		Key:      idempotencyKey,
		Payload:  []string{strconv.FormatInt(shotID, 10)},
	}
	var claim idempotency.Claim
	if err := s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		claimed, err := s.guard.Begin(ctx, q, request)
		if err != nil {
			return err
		}
		claim = claimed
		return nil
	}); err != nil {
		return SubmitRenderResult{}, err
	}
	if claim.Replayed {
		var replayed SubmitRenderResult
		if err := json.Unmarshal([]byte(claim.Response), &replayed); err != nil {
			return SubmitRenderResult{}, apperr.Wrap(err, apperr.CodeInternal, "stored idempotent response is unreadable")
		}
		replayed.Replayed = true
		return replayed, nil
	}

	result, workErr := s.submitRenderWork(ctx, principal, shotID, claim)
	if workErr != nil {
		if releaseErr := s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
			return s.guard.Release(ctx, q, claim)
		}); releaseErr != nil && !apperr.IsCode(releaseErr, apperr.CodeNotFound) {
			return SubmitRenderResult{}, releaseErr
		}
		return SubmitRenderResult{}, workErr
	}
	return result, nil
}

func (s *Service) submitRenderWork(ctx context.Context, principal identity.Principal, shotID int64, claim idempotency.Claim) (SubmitRenderResult, error) {
	now := s.clock.Now()
	day := clock.QuotaDay(now)
	var (
		result    SubmitRenderResult
		rejection audit.Event
	)
	err := s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		shot, series, err := s.loadShot(ctx, q, shotID, principal.StudioID)
		if err != nil {
			return err
		}
		if shot.PromptVersionID == nil {
			return apperr.New(apperr.CodeFailedPrecondition, "shot %d has no prompt version bound", shot.Ordinal).
				With("shot_state", string(shot.State))
		}
		version, err := s.loadPromptVersion(ctx, q, *shot.PromptVersionID, principal.StudioID)
		if err != nil {
			return err
		}
		if err := version.EnsureBindable(); err != nil {
			return err
		}
		studio, err := s.studios.FindByID(ctx, q, principal.StudioID)
		if err != nil {
			return err
		}
		if err := s.quotas.Ensure(ctx, q, studio.ID, day, studio.DailyRenderCapacity); err != nil {
			return err
		}
		consumed, err := s.quotas.TryConsume(ctx, q, studio.ID, day)
		if err != nil {
			return err
		}
		if !consumed {
			rejection = s.rejectionFor(principal,
				auditlog.Rejected("render_job.quota_exhausted", audit.ObjectShot, shot.ID).
					WithDetail("quota_day", day).
					WithDetail("capacity", strconv.Itoa(studio.DailyRenderCapacity)))
			return apperr.New(apperr.CodeExhausted, "daily render quota of %d is exhausted for %s",
				studio.DailyRenderCapacity, day).With("quota_day", day)
		}
		if err := shot.StartRender(now); err != nil {
			return err
		}
		job := render.Job{
			StudioID:        studio.ID,
			SeriesID:        series.ID,
			ShotID:          shot.ID,
			PromptVersionID: version.ID,
			QuotaDay:        day,
			State:           render.StateQueued,
			MaxAttempts:     s.maxAttempts,
			NextAttemptAt:   now,
			RequestedBy:     principal.UserID,
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		jobID, err := s.renders.CreateJob(ctx, q, job)
		if err != nil {
			if apperr.IsCode(err, apperr.CodeConflict) {
				return apperr.New(apperr.CodeConflict, "shot %d already has a render in flight", shot.Ordinal).
					With("shot_id", strconv.FormatInt(shot.ID, 10))
			}
			return err
		}
		if err := s.shots.Save(ctx, q, shot); err != nil {
			return err
		}
		if series.State == production.StateDraft {
			if err := series.Transition(production.StateShooting, now); err != nil {
				return err
			}
			if err := s.series.Save(ctx, q, series); err != nil {
				return err
			}
		}
		result = SubmitRenderResult{JobID: jobID, ShotID: shot.ID, State: string(render.StateQueued), QuotaDay: day}
		if err := s.audits.Record(ctx, q, auditlog.Success("render_job.submitted", audit.ObjectRenderJob, jobID).
			WithDetail("series_code", series.Code).
			WithDetail("ordinal", strconv.Itoa(shot.Ordinal)).
			WithDetail("quota_day", day)); err != nil {
			return err
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return apperr.Wrap(err, apperr.CodeInternal, "cannot encode idempotent response")
		}
		return s.guard.Complete(ctx, q, claim, 202, string(encoded))
	})
	if err != nil {
		s.recordRejection(ctx, rejection)
		return SubmitRenderResult{}, err
	}
	return result, nil
}

// rejectionFor stamps a refusal event with the calling principal.
func (s *Service) rejectionFor(principal identity.Principal, event audit.Event) audit.Event {
	stamped := event.Clone()
	stamped.StudioID = principal.StudioID
	stamped.ActorID = principal.UserID
	stamped.ActorRole = string(principal.Role)
	return stamped
}

// recordRejection persists a refused attempt in its own transaction, because the
// business transaction that hit the rule was rolled back.
func (s *Service) recordRejection(ctx context.Context, event audit.Event) {
	if event.Action == "" {
		return
	}
	_ = s.runner.InTx(context.WithoutCancel(ctx), func(ctx context.Context, q repository.Querier) error {
		return s.audits.Record(ctx, q, event)
	})
}

// ReviewShotInput describes a director review decision.
type ReviewShotInput struct {
	ShotID  int64
	Approve bool
	Reason  string
}

// ReviewShot approves a rendered shot or sends it back for rework. Sending an
// approved shot back is refused while a teaching workshop still uses it, because
// apprentices would lose the reference material mid-course.
func (s *Service) ReviewShot(ctx context.Context, input ReviewShotInput) (production.Shot, error) {
	principal, err := s.authorize(ctx, identity.CapReviewShot)
	if err != nil {
		return production.Shot{}, err
	}
	now := s.clock.Now()
	var (
		reviewed  production.Shot
		rejection audit.Event
	)
	err = s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		shot, series, err := s.loadShot(ctx, q, input.ShotID, principal.StudioID)
		if err != nil {
			return err
		}
		action := "shot.approved"
		if input.Approve {
			if err := shot.Approve(now); err != nil {
				return err
			}
		} else {
			live, err := s.teaching.CountLiveWorkshopsForShot(ctx, q, shot.ID)
			if err != nil {
				return err
			}
			if live > 0 {
				rejection = s.rejectionFor(principal,
					auditlog.Rejected("shot.rework_blocked", audit.ObjectShot, shot.ID).
						WithDetail("live_workshops", strconv.Itoa(live)))
				return apperr.New(apperr.CodeFailedPrecondition,
					"shot %d backs %d live teaching workshops and cannot return to rework", shot.Ordinal, live).
					With("live_workshops", strconv.Itoa(live))
			}
			if err := shot.SendBackToRework(input.Reason, now); err != nil {
				return err
			}
			action = "shot.reworked"
		}
		if err := s.shots.Save(ctx, q, shot); err != nil {
			return err
		}
		siblings, err := s.shots.ListBySeries(ctx, q, series.ID)
		if err != nil {
			return err
		}
		if !input.Approve && series.State == production.StateReviewing {
			if err := series.Transition(production.StateShooting, now); err != nil {
				return err
			}
			if err := s.series.Save(ctx, q, series); err != nil {
				return err
			}
		} else if input.Approve && series.State == production.StateShooting && production.AllRendered(siblings) {
			if err := series.Transition(production.StateReviewing, now); err != nil {
				return err
			}
			if err := s.series.Save(ctx, q, series); err != nil {
				return err
			}
		}
		reviewed = shot
		return s.audits.Record(ctx, q, auditlog.Success(action, audit.ObjectShot, shot.ID).
			WithDetail("series_code", series.Code).
			WithDetail("ordinal", strconv.Itoa(shot.Ordinal)))
	})
	if err != nil {
		s.recordRejection(ctx, rejection)
		return production.Shot{}, err
	}
	return reviewed, nil
}

// PublishSeries exposes a fully approved series to the teaching catalogue.
func (s *Service) PublishSeries(ctx context.Context, seriesID int64) (production.Series, error) {
	principal, err := s.authorize(ctx, identity.CapPublishSeries)
	if err != nil {
		return production.Series{}, err
	}
	now := s.clock.Now()
	var (
		published production.Series
		rejection audit.Event
	)
	err = s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		series, err := s.loadSeries(ctx, q, seriesID, principal.StudioID)
		if err != nil {
			return err
		}
		shots, err := s.shots.ListBySeries(ctx, q, series.ID)
		if err != nil {
			return err
		}
		if err := series.EnsurePublishable(shots); err != nil {
			rejection = s.rejectionFor(principal,
				auditlog.Rejected("series.publish_blocked", audit.ObjectSeries, series.ID).
					WithDetail("state", string(series.State)).
					WithDetail("shots", strconv.Itoa(len(shots))))
			return err
		}
		if err := series.Transition(production.StatePublished, now); err != nil {
			return err
		}
		if err := s.series.Save(ctx, q, series); err != nil {
			return err
		}
		published = series
		return s.audits.Record(ctx, q, auditlog.Success("series.published", audit.ObjectSeries, series.ID).
			WithDetail("code", series.Code).
			WithDetail("shots", strconv.Itoa(len(shots))))
	})
	if err != nil {
		s.recordRejection(ctx, rejection)
		return production.Series{}, err
	}
	return published, nil
}

// SeriesDetail bundles a series with its storyboard.
type SeriesDetail struct {
	Series production.Series
	Shots  []production.Shot
	Counts map[production.ShotState]int
}

// GetSeries loads a series with its shots.
func (s *Service) GetSeries(ctx context.Context, seriesID int64) (SeriesDetail, error) {
	principal, err := s.authorize(ctx, identity.CapViewCatalog)
	if err != nil {
		return SeriesDetail{}, err
	}
	var detail SeriesDetail
	err = s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		series, err := s.loadSeries(ctx, q, seriesID, principal.StudioID)
		if err != nil {
			return err
		}
		shots, err := s.shots.ListBySeries(ctx, q, series.ID)
		if err != nil {
			return err
		}
		detail = SeriesDetail{Series: series, Shots: shots, Counts: production.CountByState(shots)}
		return nil
	})
	if err != nil {
		return SeriesDetail{}, err
	}
	return detail, nil
}

// ListSeries returns one filtered page of series plus the matching total.
func (s *Service) ListSeries(ctx context.Context, filter repository.SeriesFilter, page repository.Page) ([]production.Series, int, error) {
	principal, err := s.authorize(ctx, identity.CapViewCatalog)
	if err != nil {
		return nil, 0, err
	}
	var (
		items []production.Series
		total int
	)
	err = s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		items, total, err = s.series.List(ctx, q, principal.StudioID, filter, page)
		return err
	})
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// ListRenderJobs returns the render history of one shot.
func (s *Service) ListRenderJobs(ctx context.Context, shotID int64) ([]render.Job, error) {
	principal, err := s.authorize(ctx, identity.CapViewCatalog)
	if err != nil {
		return nil, err
	}
	var jobs []render.Job
	err = s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		if _, _, err := s.loadShot(ctx, q, shotID, principal.StudioID); err != nil {
			return err
		}
		found, err := s.renders.ListByShot(ctx, q, shotID)
		if err != nil {
			return err
		}
		jobs = found
		return nil
	})
	if err != nil {
		return nil, err
	}
	return jobs, nil
}

// RemainingQuota reports the unused render allowance of the current day.
func (s *Service) RemainingQuota(ctx context.Context) (repository.Quota, error) {
	principal, err := s.authorize(ctx, identity.CapViewCatalog)
	if err != nil {
		return repository.Quota{}, err
	}
	day := clock.QuotaDay(s.clock.Now())
	var quota repository.Quota
	err = s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		studio, err := s.studios.FindByID(ctx, q, principal.StudioID)
		if err != nil {
			return err
		}
		if err := s.quotas.Ensure(ctx, q, studio.ID, day, studio.DailyRenderCapacity); err != nil {
			return err
		}
		found, err := s.quotas.Get(ctx, q, studio.ID, day)
		if err != nil {
			return err
		}
		quota = found
		return nil
	})
	if err != nil {
		return repository.Quota{}, err
	}
	return quota, nil
}

func (s *Service) loadSeries(ctx context.Context, q repository.Querier, seriesID, studioID int64) (production.Series, error) {
	series, err := s.series.FindByID(ctx, q, seriesID)
	if err != nil {
		return production.Series{}, err
	}
	if series.StudioID != studioID {
		return production.Series{}, apperr.New(apperr.CodeNotFound, "series not found").With("entity", "series")
	}
	return series, nil
}

func (s *Service) loadShot(ctx context.Context, q repository.Querier, shotID, studioID int64) (production.Shot, production.Series, error) {
	shot, err := s.shots.FindByID(ctx, q, shotID)
	if err != nil {
		return production.Shot{}, production.Series{}, err
	}
	series, err := s.loadSeries(ctx, q, shot.SeriesID, studioID)
	if err != nil {
		return production.Shot{}, production.Series{}, err
	}
	return shot, series, nil
}

func (s *Service) loadPromptVersion(ctx context.Context, q repository.Querier, versionID, studioID int64) (prompt.Version, error) {
	version, err := s.prompts.FindVersionByID(ctx, q, versionID)
	if err != nil {
		return prompt.Version{}, err
	}
	template, err := s.prompts.FindTemplateByID(ctx, q, version.TemplateID)
	if err != nil {
		return prompt.Version{}, err
	}
	if template.StudioID != studioID {
		return prompt.Version{}, apperr.New(apperr.CodeNotFound, "prompt version not found").
			With("entity", "prompt version")
	}
	return version, nil
}

func (s *Service) authorize(ctx context.Context, capability identity.Capability) (identity.Principal, error) {
	principal, err := reqctx.RequirePrincipal(ctx)
	if err != nil {
		return identity.Principal{}, err
	}
	if err := principal.Authorize(capability); err != nil {
		return identity.Principal{}, err
	}
	return principal, nil
}
