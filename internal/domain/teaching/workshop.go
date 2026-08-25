// Package teaching models the mentoring side of the studio: a published shot is
// opened as a workshop, apprentices enrol, submit practice against the frozen
// prompt version and the director grades every submission before closing.
package teaching

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
)

// WorkshopState is the lifecycle state of one teaching workshop.
type WorkshopState string

const (
	// StateOpen accepts enrolment.
	StateOpen WorkshopState = "open"
	// StateTeaching accepts practice submissions from enrolled apprentices.
	StateTeaching WorkshopState = "teaching"
	// StateGrading stops new submissions and waits for the director review.
	StateGrading WorkshopState = "grading"
	// StateClosed released the prompt version reference.
	StateClosed WorkshopState = "closed"
	// StateCancelled abandons the workshop.
	StateCancelled WorkshopState = "cancelled"
)

var workshopTransitions = map[WorkshopState]map[WorkshopState]bool{
	// An open workshop may go straight to grading when the director stops
	// enrolment before any practice arrived.
	StateOpen:      {StateTeaching: true, StateGrading: true, StateCancelled: true},
	StateTeaching:  {StateGrading: true, StateCancelled: true},
	StateGrading:   {StateClosed: true, StateTeaching: true},
	StateClosed:    {},
	StateCancelled: {},
}

// Workshop is one mentoring session bound to an approved shot.
type Workshop struct {
	ID              int64
	StudioID        int64
	SeriesID        int64
	ShotID          int64
	PromptVersionID int64
	MentorID        int64
	Title           string
	Brief           string
	State           WorkshopState
	Capacity        int
	Enrolled        int
	OpensAt         time.Time
	ClosesAt        time.Time
	Version         int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// EnrollmentState is the lifecycle state of one apprentice enrolment.
type EnrollmentState string

const (
	// Enrolled has a seat but no submission yet.
	Enrolled EnrollmentState = "enrolled"
	// Submitted has practice waiting for review.
	Submitted EnrollmentState = "submitted"
	// Graded received a reviewed result.
	Graded EnrollmentState = "graded"
)

// Enrollment is one apprentice seat in a workshop.
type Enrollment struct {
	ID           int64
	WorkshopID   int64
	ApprenticeID int64
	State        EnrollmentState
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// SubmissionState is the review outcome of one practice submission.
type SubmissionState string

const (
	// Pending awaits the director review.
	Pending SubmissionState = "pending"
	// Accepted passed review.
	Accepted SubmissionState = "accepted"
	// Returned was sent back with a score below the pass mark.
	Returned SubmissionState = "returned"
)

// Submission is one apprentice practice attempt.
type Submission struct {
	ID              int64
	WorkshopID      int64
	EnrollmentID    int64
	PromptVersionID int64
	Body            string
	State           SubmissionState
	Score           int
	Feedback        string
	ReviewedBy      *int64
	ReviewedAt      *time.Time
	Version         int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// PassMark is the lowest accepted practice score.
const PassMark = 60

// CanTransitionTo reports whether the workshop transition is legal.
func (s WorkshopState) CanTransitionTo(next WorkshopState) bool {
	return workshopTransitions[s][next]
}

// ParseWorkshopState validates external workshop state text.
func ParseWorkshopState(raw string) (WorkshopState, error) {
	candidate := WorkshopState(strings.ToLower(strings.TrimSpace(raw)))
	if _, ok := workshopTransitions[candidate]; !ok {
		return "", apperr.New(apperr.CodeInvalidArgument, "unknown workshop state %q", raw).With("field", "state")
	}
	return candidate, nil
}

// Transition applies a workshop state change.
func (w *Workshop) Transition(next WorkshopState, now time.Time) error {
	if w.State == next {
		return apperr.New(apperr.CodeConflict, "workshop %d is already %s", w.ID, next).
			With("workshop_state", string(next))
	}
	if !w.State.CanTransitionTo(next) {
		return apperr.New(apperr.CodeFailedPrecondition, "workshop %d cannot move from %s to %s", w.ID, w.State, next).
			With("from", string(w.State)).
			With("to", string(next))
	}
	w.State = next
	w.UpdatedAt = now
	return nil
}

// ReferencesPromptVersion reports whether the workshop still holds its prompt
// version, which blocks retirement of that version.
func (w *Workshop) ReferencesPromptVersion() bool {
	switch w.State {
	case StateClosed, StateCancelled:
		return false
	default:
		return true
	}
}

// EnsureEnrollable checks the seat, the schedule window and the state.
func (w *Workshop) EnsureEnrollable(now time.Time) error {
	if w.State != StateOpen {
		return apperr.New(apperr.CodeFailedPrecondition, "workshop %d is %s and not open for enrolment", w.ID, w.State).
			With("workshop_state", string(w.State))
	}
	if now.Before(w.OpensAt) {
		return apperr.New(apperr.CodeFailedPrecondition, "workshop %d opens at %s", w.ID, w.OpensAt.Format(time.RFC3339)).
			With("opens_at", w.OpensAt.Format(time.RFC3339))
	}
	if !now.Before(w.ClosesAt) {
		return apperr.New(apperr.CodeFailedPrecondition, "workshop %d closed for enrolment at %s", w.ID, w.ClosesAt.Format(time.RFC3339)).
			With("closes_at", w.ClosesAt.Format(time.RFC3339))
	}
	if w.Enrolled >= w.Capacity {
		return apperr.New(apperr.CodeExhausted, "workshop %d is full at %d seats", w.ID, w.Capacity).
			With("capacity", strconv.Itoa(w.Capacity))
	}
	return nil
}

// EnsureAcceptsSubmission checks that practice may still be submitted.
func (w *Workshop) EnsureAcceptsSubmission(now time.Time) error {
	switch w.State {
	case StateOpen, StateTeaching:
	default:
		return apperr.New(apperr.CodeFailedPrecondition, "workshop %d is %s and no longer accepts practice", w.ID, w.State).
			With("workshop_state", string(w.State))
	}
	if !now.Before(w.ClosesAt) {
		return apperr.New(apperr.CodeFailedPrecondition, "workshop %d passed its submission deadline %s", w.ID, w.ClosesAt.Format(time.RFC3339)).
			With("closes_at", w.ClosesAt.Format(time.RFC3339))
	}
	return nil
}

// EnsureClosable rejects closing while submissions are still pending review.
func (w *Workshop) EnsureClosable(pendingReviews int) error {
	if w.State != StateGrading {
		return apperr.New(apperr.CodeFailedPrecondition, "workshop %d must be grading before it closes, current state %s", w.ID, w.State).
			With("workshop_state", string(w.State))
	}
	if pendingReviews > 0 {
		return apperr.New(apperr.CodeFailedPrecondition, "workshop %d still has %d submissions awaiting review", w.ID, pendingReviews).
			With("pending_reviews", strconv.Itoa(pendingReviews))
	}
	return nil
}

// ValidateSchedule checks the teaching window.
func ValidateSchedule(opensAt, closesAt time.Time) error {
	if opensAt.IsZero() || closesAt.IsZero() {
		return apperr.New(apperr.CodeInvalidArgument, "workshop window must define both bounds").
			With("field", "closes_at")
	}
	if !closesAt.After(opensAt) {
		return apperr.New(apperr.CodeInvalidArgument, "workshop must close after it opens").With("field", "closes_at")
	}
	if closesAt.Sub(opensAt) > 90*24*time.Hour {
		return apperr.New(apperr.CodeInvalidArgument, "workshop window must not exceed 90 days").With("field", "closes_at")
	}
	return nil
}

// ValidateCapacity bounds the seat count.
func ValidateCapacity(capacity int) error {
	if capacity < 1 || capacity > 200 {
		return apperr.New(apperr.CodeInvalidArgument, "workshop capacity must be between 1 and 200").
			With("field", "capacity")
	}
	return nil
}

// ValidateTitle bounds the workshop title.
func ValidateTitle(raw string) (string, error) {
	title := strings.TrimSpace(raw)
	if title == "" {
		return "", apperr.New(apperr.CodeInvalidArgument, "workshop title must not be empty").With("field", "title")
	}
	if utf8.RuneCountInString(title) > 120 {
		return "", apperr.New(apperr.CodeInvalidArgument, "workshop title must not exceed 120 characters").
			With("field", "title")
	}
	return title, nil
}

// ValidatePracticeBody bounds an apprentice submission.
func ValidatePracticeBody(raw string) (string, error) {
	body := strings.TrimSpace(raw)
	if utf8.RuneCountInString(body) < 20 {
		return "", apperr.New(apperr.CodeInvalidArgument, "practice body must be at least 20 characters").
			With("field", "body")
	}
	if utf8.RuneCountInString(body) > 8000 {
		return "", apperr.New(apperr.CodeInvalidArgument, "practice body must not exceed 8000 characters").
			With("field", "body")
	}
	return body, nil
}

// MarkSubmitted moves an enrolment once practice arrives.
func (e *Enrollment) MarkSubmitted(now time.Time) error {
	switch e.State {
	case Enrolled, Submitted, Graded:
		e.State = Submitted
		e.UpdatedAt = now
		return nil
	default:
		return apperr.New(apperr.CodeFailedPrecondition, "enrolment %d is %s", e.ID, e.State).
			With("enrollment_state", string(e.State))
	}
}

// MarkGraded moves an enrolment once its practice was reviewed.
func (e *Enrollment) MarkGraded(now time.Time) error {
	if e.State != Submitted {
		return apperr.New(apperr.CodeFailedPrecondition, "enrolment %d must have a submission before grading, current state %s", e.ID, e.State).
			With("enrollment_state", string(e.State))
	}
	e.State = Graded
	e.UpdatedAt = now
	return nil
}

// Review applies the director decision to a pending submission. The outcome is
// derived from the score so the pass mark stays a single business rule.
func (s *Submission) Review(score int, feedback string, reviewer int64, now time.Time) error {
	if s.State != Pending {
		return apperr.New(apperr.CodeConflict, "submission %d was already reviewed as %s", s.ID, s.State).
			With("submission_state", string(s.State))
	}
	if score < 0 || score > 100 {
		return apperr.New(apperr.CodeInvalidArgument, "score must be between 0 and 100").With("field", "score")
	}
	trimmed := strings.TrimSpace(feedback)
	if utf8.RuneCountInString(trimmed) > 1000 {
		return apperr.New(apperr.CodeInvalidArgument, "feedback must not exceed 1000 characters").
			With("field", "feedback")
	}
	if score < PassMark && trimmed == "" {
		return apperr.New(apperr.CodeInvalidArgument, "a returned submission needs written feedback").
			With("field", "feedback")
	}
	if score >= PassMark {
		s.State = Accepted
	} else {
		s.State = Returned
	}
	s.Score = score
	s.Feedback = trimmed
	reviewedBy := reviewer
	s.ReviewedBy = &reviewedBy
	reviewedAt := now
	s.ReviewedAt = &reviewedAt
	s.UpdatedAt = now
	return nil
}

// Clone isolates pointer fields for repository results.
func (s Submission) Clone() Submission {
	if s.ReviewedBy != nil {
		reviewer := *s.ReviewedBy
		s.ReviewedBy = &reviewer
	}
	if s.ReviewedAt != nil {
		reviewedAt := *s.ReviewedAt
		s.ReviewedAt = &reviewedAt
	}
	return s
}
