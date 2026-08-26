// Package production models the comic drama production pipeline: a series holds
// ordered shots, each shot binds one prompt version and passes through render
// and review before the series can be published.
package production

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
)

// SeriesState is the lifecycle state of one comic drama series.
type SeriesState string

const (
	// StateDraft accepts shot planning and prompt binding.
	StateDraft SeriesState = "draft"
	// StateShooting means at least one shot has entered rendering.
	StateShooting SeriesState = "shooting"
	// StateReviewing means every shot finished rendering and awaits review.
	StateReviewing SeriesState = "reviewing"
	// StatePublished exposes the series to the teaching catalogue.
	StatePublished SeriesState = "published"
	// StateArchived retires a published series.
	StateArchived SeriesState = "archived"
	// StateCancelled abandons a series before publication.
	StateCancelled SeriesState = "cancelled"
)

var seriesTransitions = map[SeriesState]map[SeriesState]bool{
	StateDraft:     {StateShooting: true, StateCancelled: true},
	StateShooting:  {StateReviewing: true, StateDraft: true, StateCancelled: true},
	StateReviewing: {StatePublished: true, StateShooting: true, StateCancelled: true},
	StatePublished: {StateArchived: true},
	StateArchived:  {},
	StateCancelled: {},
}

// Series is one comic drama production unit.
type Series struct {
	ID          int64
	StudioID    int64
	Code        string
	Title       string
	Logline     string
	State       SeriesState
	Version     int
	CreatedBy   int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
	PublishedAt *time.Time
}

// ParseSeriesState validates external state text.
func ParseSeriesState(raw string) (SeriesState, error) {
	candidate := SeriesState(strings.ToLower(strings.TrimSpace(raw)))
	if _, ok := seriesTransitions[candidate]; !ok {
		return "", apperr.New(apperr.CodeInvalidArgument, "unknown series state %q", raw).With("field", "state")
	}
	return candidate, nil
}

// CanTransitionTo reports whether the transition is part of the state machine.
func (s SeriesState) CanTransitionTo(next SeriesState) bool {
	return seriesTransitions[s][next]
}

// Terminal reports whether no further transition is possible.
func (s SeriesState) Terminal() bool { return len(seriesTransitions[s]) == 0 }

// Transition applies a state change, rejecting illegal moves.
func (s *Series) Transition(next SeriesState, now time.Time) error {
	if s.State == next {
		return apperr.New(apperr.CodeConflict, "series %s is already %s", s.Code, next).
			With("series_code", s.Code).
			With("state", string(next))
	}
	if !s.State.CanTransitionTo(next) {
		return apperr.New(apperr.CodeFailedPrecondition, "series %s cannot move from %s to %s", s.Code, s.State, next).
			With("series_code", s.Code).
			With("from", string(s.State)).
			With("to", string(next))
	}
	s.State = next
	s.UpdatedAt = now
	if next == StatePublished {
		published := now
		s.PublishedAt = &published
	}
	return nil
}

// EnsureAcceptsPlanning rejects shot planning once the series left draft or
// rework.
func (s *Series) EnsureAcceptsPlanning() error {
	switch s.State {
	case StateDraft, StateShooting:
		return nil
	default:
		return apperr.New(apperr.CodeFailedPrecondition, "series %s no longer accepts shot planning while %s", s.Code, s.State).
			With("series_code", s.Code).
			With("state", string(s.State))
	}
}

// EnsurePublishable verifies the cross entity precondition: a series is only
// publishable when it owns at least one shot and every shot is approved with a
// prompt version still attached.
func (s *Series) EnsurePublishable(shots []Shot) error {
	if s.State != StateReviewing {
		return apperr.New(apperr.CodeFailedPrecondition, "series %s must be under review before publishing, current state %s", s.Code, s.State).
			With("series_code", s.Code).
			With("state", string(s.State))
	}
	if len(shots) == 0 {
		return apperr.New(apperr.CodeFailedPrecondition, "series %s has no shots to publish", s.Code).
			With("series_code", s.Code)
	}
	for _, shot := range shots {
		if shot.State != ShotApproved {
			return apperr.New(apperr.CodeFailedPrecondition,
				"shot %d of series %s is %s and blocks publication", shot.Ordinal, s.Code, shot.State).
				With("series_code", s.Code).
				With("shot_state", string(shot.State))
		}
		if shot.PromptVersionID == nil {
			return apperr.New(apperr.CodeFailedPrecondition,
				"shot %d of series %s lost its prompt version binding", shot.Ordinal, s.Code).
				With("series_code", s.Code)
		}
	}
	return nil
}

// ValidateCodePrefix bounds the caller supplied series code prefix.
func ValidateCodePrefix(raw string) (string, error) {
	prefix := strings.ToUpper(strings.TrimSpace(raw))
	if prefix == "" {
		prefix = "MJ"
	}
	if len(prefix) < 2 || len(prefix) > 8 {
		return "", apperr.New(apperr.CodeInvalidArgument, "series code prefix must be 2 to 8 characters").
			With("field", "code_prefix")
	}
	for _, r := range prefix {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return "", apperr.New(apperr.CodeInvalidArgument, "series code prefix accepts upper case letters and digits only").
				With("field", "code_prefix")
		}
	}
	return prefix, nil
}

// ValidateTitle bounds a series title.
func ValidateTitle(raw string) (string, error) {
	title := strings.TrimSpace(raw)
	if title == "" {
		return "", apperr.New(apperr.CodeInvalidArgument, "series title must not be empty").With("field", "title")
	}
	if utf8.RuneCountInString(title) > 120 {
		return "", apperr.New(apperr.CodeInvalidArgument, "series title must not exceed 120 characters").With("field", "title")
	}
	return title, nil
}

// ValidateLogline bounds the one line synopsis.
func ValidateLogline(raw string) (string, error) {
	logline := strings.TrimSpace(raw)
	if utf8.RuneCountInString(logline) > 400 {
		return "", apperr.New(apperr.CodeInvalidArgument, "logline must not exceed 400 characters").With("field", "logline")
	}
	return logline, nil
}

// Clone isolates pointer fields for repository results.
func (s Series) Clone() Series {
	if s.PublishedAt != nil {
		published := *s.PublishedAt
		s.PublishedAt = &published
	}
	return s
}
