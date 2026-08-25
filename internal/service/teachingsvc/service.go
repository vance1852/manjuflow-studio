// Package teachingsvc owns the mentoring flow: opening a workshop on a published
// shot, apprentice enrolment, practice submission, review and closing.
package teachingsvc

import (
	"context"
	"strconv"
	"time"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
	"github.com/vance1852/manjuflow-studio/internal/auditlog"
	"github.com/vance1852/manjuflow-studio/internal/clock"
	"github.com/vance1852/manjuflow-studio/internal/domain/audit"
	"github.com/vance1852/manjuflow-studio/internal/domain/identity"
	"github.com/vance1852/manjuflow-studio/internal/domain/production"
	"github.com/vance1852/manjuflow-studio/internal/domain/teaching"
	"github.com/vance1852/manjuflow-studio/internal/repository"
	"github.com/vance1852/manjuflow-studio/internal/reqctx"
)

// Service implements the teaching use cases.
type Service struct {
	runner   repository.Runner
	series   repository.SeriesRepository
	shots    repository.ShotRepository
	prompts  repository.PromptRepository
	teaching repository.TeachingRepository
	audits   *auditlog.Recorder
	clock    clock.Clock
}

// Options configures the service.
type Options struct {
	Runner   repository.Runner
	Series   repository.SeriesRepository
	Shots    repository.ShotRepository
	Prompts  repository.PromptRepository
	Teaching repository.TeachingRepository
	Audits   *auditlog.Recorder
	Clock    clock.Clock
}

// New builds the teaching service.
func New(opts Options) *Service {
	return &Service{
		runner:   opts.Runner,
		series:   opts.Series,
		shots:    opts.Shots,
		prompts:  opts.Prompts,
		teaching: opts.Teaching,
		audits:   opts.Audits,
		clock:    opts.Clock,
	}
}

// OpenWorkshopInput describes a new mentoring session.
type OpenWorkshopInput struct {
	ShotID   int64
	Title    string
	Brief    string
	Capacity int
	OpensAt  time.Time
	ClosesAt time.Time
}

// OpenWorkshop turns an approved shot of a published series into a workshop. The
// prompt version bound to that shot is frozen for the whole course.
func (s *Service) OpenWorkshop(ctx context.Context, input OpenWorkshopInput) (teaching.Workshop, error) {
	principal, err := s.authorize(ctx, identity.CapOpenWorkshop)
	if err != nil {
		return teaching.Workshop{}, err
	}
	title, err := teaching.ValidateTitle(input.Title)
	if err != nil {
		return teaching.Workshop{}, err
	}
	if err := teaching.ValidateCapacity(input.Capacity); err != nil {
		return teaching.Workshop{}, err
	}
	if err := teaching.ValidateSchedule(input.OpensAt, input.ClosesAt); err != nil {
		return teaching.Workshop{}, err
	}
	now := s.clock.Now()
	var created teaching.Workshop
	err = s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		shot, err := s.shots.FindByID(ctx, q, input.ShotID)
		if err != nil {
			return err
		}
		series, err := s.series.FindByID(ctx, q, shot.SeriesID)
		if err != nil {
			return err
		}
		if series.StudioID != principal.StudioID {
			return apperr.New(apperr.CodeNotFound, "shot not found").With("entity", "shot")
		}
		if series.State != production.StatePublished {
			return apperr.New(apperr.CodeFailedPrecondition,
				"series %s must be published before teaching, current state %s", series.Code, series.State).
				With("series_code", series.Code).
				With("state", string(series.State))
		}
		if err := shot.EnsureTeachable(); err != nil {
			return err
		}
		version, err := s.prompts.FindVersionByID(ctx, q, *shot.PromptVersionID)
		if err != nil {
			return err
		}
		workshop := teaching.Workshop{
			StudioID:        series.StudioID,
			SeriesID:        series.ID,
			ShotID:          shot.ID,
			PromptVersionID: version.ID,
			MentorID:        principal.UserID,
			Title:           title,
			Brief:           input.Brief,
			State:           teaching.StateOpen,
			Capacity:        input.Capacity,
			OpensAt:         input.OpensAt,
			ClosesAt:        input.ClosesAt,
			Version:         1,
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		id, err := s.teaching.CreateWorkshop(ctx, q, workshop)
		if err != nil {
			return err
		}
		workshop.ID = id
		created = workshop
		return s.audits.Record(ctx, q, auditlog.Success("workshop.opened", audit.ObjectWorkshop, id).
			WithDetail("series_code", series.Code).
			WithDetail("ordinal", strconv.Itoa(shot.Ordinal)).
			WithDetail("capacity", strconv.Itoa(input.Capacity)))
	})
	if err != nil {
		return teaching.Workshop{}, err
	}
	return created, nil
}

// Enroll takes one seat for the calling apprentice. The seat counter is moved
// with a conditional update, so a full workshop rejects the extra request even
// when several apprentices arrive at the same moment.
func (s *Service) Enroll(ctx context.Context, workshopID int64) (teaching.Enrollment, error) {
	principal, err := s.authorize(ctx, identity.CapEnrollWorkshop)
	if err != nil {
		return teaching.Enrollment{}, err
	}
	now := s.clock.Now()
	var (
		enrollment teaching.Enrollment
		rejection  audit.Event
	)
	err = s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		workshop, err := s.loadWorkshop(ctx, q, workshopID, principal.StudioID)
		if err != nil {
			return err
		}
		if err := workshop.EnsureEnrollable(now); err != nil {
			return err
		}
		reserved, err := s.teaching.ReserveSeat(ctx, q, workshop.ID, now)
		if err != nil {
			return err
		}
		if !reserved {
			rejection = s.rejectionFor(principal,
				auditlog.Rejected("workshop.enrollment_rejected", audit.ObjectWorkshop, workshop.ID).
					WithDetail("capacity", strconv.Itoa(workshop.Capacity)).
					WithDetail("enrolled", strconv.Itoa(workshop.Enrolled)))
			return apperr.New(apperr.CodeExhausted, "workshop %d has no free seat", workshop.ID).
				With("capacity", strconv.Itoa(workshop.Capacity))
		}
		candidate := teaching.Enrollment{
			WorkshopID:   workshop.ID,
			ApprenticeID: principal.UserID,
			State:        teaching.Enrolled,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		id, err := s.teaching.CreateEnrollment(ctx, q, candidate)
		if err != nil {
			if apperr.IsCode(err, apperr.CodeConflict) {
				return apperr.New(apperr.CodeConflict, "you are already enrolled in workshop %d", workshop.ID).
					With("workshop_id", strconv.FormatInt(workshop.ID, 10))
			}
			return err
		}
		candidate.ID = id
		enrollment = candidate
		return s.audits.Record(ctx, q, auditlog.Success("workshop.enrolled", audit.ObjectEnrollment, id).
			WithDetail("workshop_id", strconv.FormatInt(workshop.ID, 10)))
	})
	if err != nil {
		s.recordRejection(ctx, rejection)
		return teaching.Enrollment{}, err
	}
	return enrollment, nil
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
// transaction that hit the teaching rule was rolled back.
func (s *Service) recordRejection(ctx context.Context, event audit.Event) {
	if event.Action == "" {
		return
	}
	_ = s.runner.InTx(context.WithoutCancel(ctx), func(ctx context.Context, q repository.Querier) error {
		return s.audits.Record(ctx, q, event)
	})
}

// SubmitPracticeInput describes an apprentice attempt.
type SubmitPracticeInput struct {
	WorkshopID int64
	Body       string
}

// SubmitPractice records a practice attempt against the frozen prompt version and
// moves the workshop into teaching on the first submission.
func (s *Service) SubmitPractice(ctx context.Context, input SubmitPracticeInput) (teaching.Submission, error) {
	principal, err := s.authorize(ctx, identity.CapSubmitPractice)
	if err != nil {
		return teaching.Submission{}, err
	}
	body, err := teaching.ValidatePracticeBody(input.Body)
	if err != nil {
		return teaching.Submission{}, err
	}
	now := s.clock.Now()
	var created teaching.Submission
	err = s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		workshop, err := s.loadWorkshop(ctx, q, input.WorkshopID, principal.StudioID)
		if err != nil {
			return err
		}
		if err := workshop.EnsureAcceptsSubmission(now); err != nil {
			return err
		}
		enrollment, err := s.teaching.FindEnrollment(ctx, q, workshop.ID, principal.UserID)
		if err != nil {
			if apperr.IsCode(err, apperr.CodeNotFound) {
				return apperr.New(apperr.CodeFailedPrecondition, "enrol in workshop %d before submitting practice", workshop.ID).
					With("workshop_id", strconv.FormatInt(workshop.ID, 10))
			}
			return err
		}
		submission := teaching.Submission{
			WorkshopID:      workshop.ID,
			EnrollmentID:    enrollment.ID,
			PromptVersionID: workshop.PromptVersionID,
			Body:            body,
			State:           teaching.Pending,
			Version:         1,
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		id, err := s.teaching.CreateSubmission(ctx, q, submission)
		if err != nil {
			return err
		}
		submission.ID = id
		if err := enrollment.MarkSubmitted(now); err != nil {
			return err
		}
		if err := s.teaching.SaveEnrollment(ctx, q, enrollment); err != nil {
			return err
		}
		if workshop.State == teaching.StateOpen {
			if err := workshop.Transition(teaching.StateTeaching, now); err != nil {
				return err
			}
			if err := s.teaching.SaveWorkshop(ctx, q, workshop); err != nil {
				return err
			}
		}
		created = submission
		return s.audits.Record(ctx, q, auditlog.Success("practice.submitted", audit.ObjectSubmission, id).
			WithDetail("workshop_id", strconv.FormatInt(workshop.ID, 10)))
	})
	if err != nil {
		return teaching.Submission{}, err
	}
	return created, nil
}

// ReviewPracticeInput describes the director decision.
type ReviewPracticeInput struct {
	SubmissionID int64
	Score        int
	Feedback     string
}

// ReviewPractice grades one submission and advances the enrolment.
func (s *Service) ReviewPractice(ctx context.Context, input ReviewPracticeInput) (teaching.Submission, error) {
	principal, err := s.authorize(ctx, identity.CapGradePractice)
	if err != nil {
		return teaching.Submission{}, err
	}
	now := s.clock.Now()
	var reviewed teaching.Submission
	err = s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		submission, err := s.teaching.FindSubmissionByID(ctx, q, input.SubmissionID)
		if err != nil {
			return err
		}
		workshop, err := s.loadWorkshop(ctx, q, submission.WorkshopID, principal.StudioID)
		if err != nil {
			return err
		}
		if err := submission.Review(input.Score, input.Feedback, principal.UserID, now); err != nil {
			return err
		}
		if err := s.teaching.SaveSubmission(ctx, q, submission); err != nil {
			return err
		}
		enrollment, err := s.teaching.FindEnrollmentByID(ctx, q, submission.EnrollmentID)
		if err != nil {
			return err
		}
		if err := enrollment.MarkGraded(now); err != nil {
			return err
		}
		if err := s.teaching.SaveEnrollment(ctx, q, enrollment); err != nil {
			return err
		}
		reviewed = submission
		return s.audits.Record(ctx, q, auditlog.Success("practice.reviewed", audit.ObjectSubmission, submission.ID).
			WithDetail("workshop_id", strconv.FormatInt(workshop.ID, 10)).
			WithDetail("score", strconv.Itoa(input.Score)).
			WithDetail("outcome", string(submission.State)))
	})
	if err != nil {
		return teaching.Submission{}, err
	}
	return reviewed, nil
}

// StartGrading closes the submission window of a workshop.
func (s *Service) StartGrading(ctx context.Context, workshopID int64) (teaching.Workshop, error) {
	return s.transitionWorkshop(ctx, workshopID, teaching.StateGrading, "workshop.grading_started")
}

// CloseWorkshop finishes a workshop once every submission was reviewed, which
// releases the prompt version for retirement.
func (s *Service) CloseWorkshop(ctx context.Context, workshopID int64) (teaching.Workshop, error) {
	principal, err := s.authorize(ctx, identity.CapGradePractice)
	if err != nil {
		return teaching.Workshop{}, err
	}
	now := s.clock.Now()
	var (
		closed    teaching.Workshop
		rejection audit.Event
	)
	err = s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		workshop, err := s.loadWorkshop(ctx, q, workshopID, principal.StudioID)
		if err != nil {
			return err
		}
		pending, err := s.teaching.CountPendingSubmissions(ctx, q, workshop.ID)
		if err != nil {
			return err
		}
		if err := workshop.EnsureClosable(pending); err != nil {
			rejection = s.rejectionFor(principal,
				auditlog.Rejected("workshop.close_blocked", audit.ObjectWorkshop, workshop.ID).
					WithDetail("pending_reviews", strconv.Itoa(pending)).
					WithDetail("state", string(workshop.State)))
			return err
		}
		if err := workshop.Transition(teaching.StateClosed, now); err != nil {
			return err
		}
		if err := s.teaching.SaveWorkshop(ctx, q, workshop); err != nil {
			return err
		}
		closed = workshop
		return s.audits.Record(ctx, q, auditlog.Success("workshop.closed", audit.ObjectWorkshop, workshop.ID).
			WithDetail("enrolled", strconv.Itoa(workshop.Enrolled)))
	})
	if err != nil {
		s.recordRejection(ctx, rejection)
		return teaching.Workshop{}, err
	}
	return closed, nil
}

// ListWorkshops returns one filtered page of workshops.
func (s *Service) ListWorkshops(ctx context.Context, filter repository.WorkshopFilter, page repository.Page) ([]teaching.Workshop, int, error) {
	principal, err := s.authorize(ctx, identity.CapViewCatalog)
	if err != nil {
		return nil, 0, err
	}
	var (
		items []teaching.Workshop
		total int
	)
	err = s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		items, total, err = s.teaching.ListWorkshops(ctx, q, principal.StudioID, filter, page)
		return err
	})
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// ListSubmissions returns one page of practice submissions of a workshop.
func (s *Service) ListSubmissions(ctx context.Context, workshopID int64, page repository.Page) ([]teaching.Submission, int, error) {
	principal, err := s.authorize(ctx, identity.CapViewCatalog)
	if err != nil {
		return nil, 0, err
	}
	var (
		items []teaching.Submission
		total int
	)
	err = s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		workshop, err := s.loadWorkshop(ctx, q, workshopID, principal.StudioID)
		if err != nil {
			return err
		}
		items, total, err = s.teaching.ListSubmissions(ctx, q, workshop.ID, page)
		return err
	})
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// SweepOverdue moves workshops whose deadline passed into grading so the
// director sees them without manual bookkeeping.
func (s *Service) SweepOverdue(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 20
	}
	now := s.clock.Now()
	moved := 0
	err := s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		candidates, err := s.teaching.ListOverdueWorkshops(ctx, q, now, limit)
		if err != nil {
			return err
		}
		for _, workshop := range candidates {
			if err := workshop.Transition(teaching.StateGrading, now); err != nil {
				continue
			}
			if err := s.teaching.SaveWorkshop(ctx, q, workshop); err != nil {
				return err
			}
			moved++
			event := auditlog.Success("workshop.grading_started", audit.ObjectWorkshop, workshop.ID).
				WithDetail("reason", "deadline_passed")
			event.StudioID = workshop.StudioID
			event.ActorRole = "workshop_sweeper"
			if err := s.audits.Record(ctx, q, event); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return moved, nil
}

func (s *Service) transitionWorkshop(ctx context.Context, workshopID int64, next teaching.WorkshopState, action string) (teaching.Workshop, error) {
	principal, err := s.authorize(ctx, identity.CapOpenWorkshop)
	if err != nil {
		return teaching.Workshop{}, err
	}
	now := s.clock.Now()
	var updated teaching.Workshop
	err = s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		workshop, err := s.loadWorkshop(ctx, q, workshopID, principal.StudioID)
		if err != nil {
			return err
		}
		if err := workshop.Transition(next, now); err != nil {
			return err
		}
		if err := s.teaching.SaveWorkshop(ctx, q, workshop); err != nil {
			return err
		}
		updated = workshop
		return s.audits.Record(ctx, q, auditlog.Success(action, audit.ObjectWorkshop, workshop.ID).
			WithDetail("state", string(next)))
	})
	if err != nil {
		return teaching.Workshop{}, err
	}
	return updated, nil
}

func (s *Service) loadWorkshop(ctx context.Context, q repository.Querier, workshopID, studioID int64) (teaching.Workshop, error) {
	workshop, err := s.teaching.FindWorkshopByID(ctx, q, workshopID)
	if err != nil {
		return teaching.Workshop{}, err
	}
	if workshop.StudioID != studioID {
		return teaching.Workshop{}, apperr.New(apperr.CodeNotFound, "workshop not found").With("entity", "workshop")
	}
	return workshop, nil
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
