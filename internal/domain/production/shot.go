package production

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
)

// ShotState is the lifecycle state of one storyboard shot.
type ShotState string

const (
	// ShotDraft is planned but has no prompt version bound yet.
	ShotDraft ShotState = "draft"
	// ShotBound carries an active prompt version and may be rendered.
	ShotBound ShotState = "bound"
	// ShotRendering has an in-flight render job holding the shot.
	ShotRendering ShotState = "rendering"
	// ShotRendered holds a render artifact awaiting the director review.
	ShotRendered ShotState = "rendered"
	// ShotApproved passed review and counts towards publication.
	ShotApproved ShotState = "approved"
	// ShotRework was sent back by the director and needs a new prompt revision.
	ShotRework ShotState = "rework"
)

var shotTransitions = map[ShotState]map[ShotState]bool{
	ShotDraft:     {ShotBound: true},
	ShotBound:     {ShotRendering: true, ShotDraft: true},
	ShotRendering: {ShotRendered: true, ShotBound: true},
	ShotRendered:  {ShotApproved: true, ShotRework: true},
	ShotApproved:  {ShotRework: true},
	ShotRework:    {ShotBound: true},
}

// Shot is one storyboard unit of a series.
type Shot struct {
	ID              int64
	SeriesID        int64
	Ordinal         int
	Title           string
	Direction       string
	State           ShotState
	PromptVersionID *int64
	ArtifactRef     string
	ReworkReason    string
	Version         int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Draft is the caller supplied shot plan used by the batch planning endpoint.
type Draft struct {
	Ordinal   int
	Title     string
	Direction string
}

// ParseShotState validates external shot state text.
func ParseShotState(raw string) (ShotState, error) {
	candidate := ShotState(strings.ToLower(strings.TrimSpace(raw)))
	if _, ok := shotTransitions[candidate]; !ok {
		return "", apperr.New(apperr.CodeInvalidArgument, "unknown shot state %q", raw).With("field", "state")
	}
	return candidate, nil
}

// ValidateDraft checks one planned shot.
func ValidateDraft(draft Draft) error {
	if draft.Ordinal < 1 || draft.Ordinal > 999 {
		return apperr.New(apperr.CodeInvalidArgument, "shot ordinal must be between 1 and 999").
			With("field", "ordinal")
	}
	title := strings.TrimSpace(draft.Title)
	if title == "" {
		return apperr.New(apperr.CodeInvalidArgument, "shot title must not be empty").With("field", "title")
	}
	if utf8.RuneCountInString(title) > 120 {
		return apperr.New(apperr.CodeInvalidArgument, "shot title must not exceed 120 characters").With("field", "title")
	}
	if utf8.RuneCountInString(strings.TrimSpace(draft.Direction)) > 600 {
		return apperr.New(apperr.CodeInvalidArgument, "shot direction must not exceed 600 characters").
			With("field", "direction")
	}
	return nil
}

func (s *Shot) transition(next ShotState, now time.Time) error {
	if !shotTransitions[s.State][next] {
		return apperr.New(apperr.CodeFailedPrecondition, "shot %d cannot move from %s to %s", s.Ordinal, s.State, next).
			With("shot_ordinal", strconv.Itoa(s.Ordinal)).
			With("from", string(s.State)).
			With("to", string(next))
	}
	s.State = next
	s.UpdatedAt = now
	return nil
}

// Bind attaches an active prompt version. Rebinding a bound shot is allowed
// while it is not rendering, which is how the director iterates on wording.
func (s *Shot) Bind(promptVersionID int64, now time.Time) error {
	switch s.State {
	case ShotDraft, ShotRework:
		if err := s.transition(ShotBound, now); err != nil {
			return err
		}
	case ShotBound:
		s.UpdatedAt = now
	default:
		return apperr.New(apperr.CodeFailedPrecondition, "shot %d cannot rebind a prompt while %s", s.Ordinal, s.State).
			With("from", string(s.State))
	}
	bound := promptVersionID
	s.PromptVersionID = &bound
	s.ReworkReason = ""
	return nil
}

// StartRender moves a bound shot into rendering.
func (s *Shot) StartRender(now time.Time) error {
	if s.PromptVersionID == nil {
		return apperr.New(apperr.CodeFailedPrecondition, "shot %d has no prompt version bound", s.Ordinal)
	}
	return s.transition(ShotRendering, now)
}

// CompleteRender stores the produced artifact reference.
func (s *Shot) CompleteRender(artifactRef string, now time.Time) error {
	if strings.TrimSpace(artifactRef) == "" {
		return apperr.New(apperr.CodeInvalidArgument, "artifact reference must not be empty").
			With("field", "artifact_ref")
	}
	if err := s.transition(ShotRendered, now); err != nil {
		return err
	}
	s.ArtifactRef = artifactRef
	return nil
}

// ReleaseRender returns a shot to the bound state after a failed render so the
// director can retry without replanning the storyboard.
func (s *Shot) ReleaseRender(now time.Time) error {
	return s.transition(ShotBound, now)
}

// Approve accepts a rendered shot.
func (s *Shot) Approve(now time.Time) error {
	return s.transition(ShotApproved, now)
}

// SendBackToRework rejects a rendered or approved shot with a reason.
func (s *Shot) SendBackToRework(reason string, now time.Time) error {
	trimmed := strings.TrimSpace(reason)
	if trimmed == "" {
		return apperr.New(apperr.CodeInvalidArgument, "rework reason must not be empty").With("field", "reason")
	}
	if utf8.RuneCountInString(trimmed) > 400 {
		return apperr.New(apperr.CodeInvalidArgument, "rework reason must not exceed 400 characters").
			With("field", "reason")
	}
	if err := s.transition(ShotRework, now); err != nil {
		return err
	}
	s.ReworkReason = trimmed
	s.ArtifactRef = ""
	return nil
}

// EnsureTeachable rejects shots that cannot back a teaching workshop.
func (s *Shot) EnsureTeachable() error {
	if s.State != ShotApproved {
		return apperr.New(apperr.CodeFailedPrecondition, "shot %d must be approved before teaching, current state %s", s.Ordinal, s.State).
			With("shot_state", string(s.State))
	}
	if s.PromptVersionID == nil {
		return apperr.New(apperr.CodeFailedPrecondition, "shot %d has no prompt version to teach", s.Ordinal)
	}
	return nil
}

// Clone isolates pointer fields for repository results.
func (s Shot) Clone() Shot {
	if s.PromptVersionID != nil {
		bound := *s.PromptVersionID
		s.PromptVersionID = &bound
	}
	return s
}

// CountByState groups shots for progress reporting. The returned map is freshly
// allocated on every call.
func CountByState(shots []Shot) map[ShotState]int {
	counts := make(map[ShotState]int, len(shotTransitions))
	for _, shot := range shots {
		counts[shot.State]++
	}
	return counts
}

// AllRendered reports whether every shot finished rendering, which drives the
// automatic move of the series into review.
func AllRendered(shots []Shot) bool {
	if len(shots) == 0 {
		return false
	}
	for _, shot := range shots {
		switch shot.State {
		case ShotRendered, ShotApproved:
		default:
			return false
		}
	}
	return true
}
