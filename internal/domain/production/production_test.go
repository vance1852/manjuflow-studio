package production

import (
	"testing"
	"time"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
)

func now() time.Time { return time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC) }

func TestSeriesStateMachineAcceptsOnlyPlannedTransitions(t *testing.T) {
	cases := []struct {
		from    SeriesState
		to      SeriesState
		allowed bool
	}{
		{StateDraft, StateShooting, true},
		{StateDraft, StatePublished, false},
		{StateShooting, StateReviewing, true},
		{StateReviewing, StatePublished, true},
		{StateReviewing, StateShooting, true},
		{StatePublished, StateArchived, true},
		{StatePublished, StateDraft, false},
		{StateArchived, StatePublished, false},
		{StateCancelled, StateDraft, false},
	}
	for _, testCase := range cases {
		series := &Series{Code: "MJ-0001", State: testCase.from}
		err := series.Transition(testCase.to, now())
		if testCase.allowed && err != nil {
			t.Fatalf("%s to %s was rejected: %v", testCase.from, testCase.to, err)
		}
		if !testCase.allowed && err == nil {
			t.Fatalf("%s to %s was accepted", testCase.from, testCase.to)
		}
		if !testCase.allowed && !apperr.IsCode(err, apperr.CodeFailedPrecondition) {
			t.Fatalf("%s to %s reported %v", testCase.from, testCase.to, apperr.CodeOf(err))
		}
	}
}

func TestRepeatingTheCurrentSeriesStateIsAConflict(t *testing.T) {
	series := &Series{Code: "MJ-0002", State: StateShooting}
	err := series.Transition(StateShooting, now())
	if !apperr.IsCode(err, apperr.CodeConflict) {
		t.Fatalf("repeated transition reported %v, want a conflict", apperr.CodeOf(err))
	}
}

func TestPublishingStampsThePublicationInstant(t *testing.T) {
	series := &Series{Code: "MJ-0003", State: StateReviewing}
	if err := series.Transition(StatePublished, now()); err != nil {
		t.Fatalf("publication was rejected: %v", err)
	}
	if series.PublishedAt == nil || !series.PublishedAt.Equal(now()) {
		t.Fatalf("publication instant is %v", series.PublishedAt)
	}
	if !series.State.Terminal() && series.State != StatePublished {
		t.Fatalf("unexpected state %s", series.State)
	}
}

func TestPublishRequiresApprovedShotsWithBoundPrompts(t *testing.T) {
	versionID := int64(7)
	series := &Series{Code: "MJ-0004", State: StateReviewing}

	if err := series.EnsurePublishable(nil); err == nil {
		t.Fatal("a series without shots was publishable")
	}
	pending := []Shot{{Ordinal: 1, State: ShotRendered, PromptVersionID: &versionID}}
	if err := series.EnsurePublishable(pending); err == nil {
		t.Fatal("a series with an unreviewed shot was publishable")
	}
	unbound := []Shot{{Ordinal: 1, State: ShotApproved}}
	if err := series.EnsurePublishable(unbound); err == nil {
		t.Fatal("a series with an unbound approved shot was publishable")
	}
	ready := []Shot{{Ordinal: 1, State: ShotApproved, PromptVersionID: &versionID}}
	if err := series.EnsurePublishable(ready); err != nil {
		t.Fatalf("a fully approved series was refused: %v", err)
	}

	draft := &Series{Code: "MJ-0005", State: StateDraft}
	if err := draft.EnsurePublishable(ready); err == nil {
		t.Fatal("a draft series was publishable")
	}
}

func TestPlanningIsRefusedOnceTheSeriesLeftProduction(t *testing.T) {
	for _, state := range []SeriesState{StateDraft, StateShooting} {
		series := &Series{Code: "MJ-0006", State: state}
		if err := series.EnsureAcceptsPlanning(); err != nil {
			t.Fatalf("planning was refused while %s: %v", state, err)
		}
	}
	for _, state := range []SeriesState{StateReviewing, StatePublished, StateArchived, StateCancelled} {
		series := &Series{Code: "MJ-0007", State: state}
		if err := series.EnsureAcceptsPlanning(); err == nil {
			t.Fatalf("planning was accepted while %s", state)
		}
	}
}

func TestShotLifecycleFollowsTheRenderAndReviewOrder(t *testing.T) {
	shot := &Shot{Ordinal: 1, State: ShotDraft, Version: 1}

	if err := shot.StartRender(now()); err == nil {
		t.Fatal("an unbound shot started rendering")
	}
	if err := shot.Bind(11, now()); err != nil {
		t.Fatalf("binding was refused: %v", err)
	}
	if shot.State != ShotBound || shot.PromptVersionID == nil || *shot.PromptVersionID != 11 {
		t.Fatalf("shot after binding is %#v", shot)
	}
	if err := shot.Bind(12, now()); err != nil {
		t.Fatalf("rebinding a bound shot was refused: %v", err)
	}
	if err := shot.StartRender(now()); err != nil {
		t.Fatalf("render start was refused: %v", err)
	}
	if err := shot.Bind(13, now()); err == nil {
		t.Fatal("prompt was rebound while rendering")
	}
	if err := shot.CompleteRender("", now()); err == nil {
		t.Fatal("an empty artifact reference was accepted")
	}
	if err := shot.CompleteRender("manju://a/1", now()); err != nil {
		t.Fatalf("render completion was refused: %v", err)
	}
	if err := shot.Approve(now()); err != nil {
		t.Fatalf("approval was refused: %v", err)
	}
	if err := shot.Approve(now()); err == nil {
		t.Fatal("double approval was accepted")
	}
	if err := shot.SendBackToRework("光影不稳", now()); err != nil {
		t.Fatalf("rework was refused: %v", err)
	}
	if shot.ArtifactRef != "" {
		t.Fatal("rework kept the stale artifact reference")
	}
	if shot.ReworkReason != "光影不稳" {
		t.Fatalf("rework reason is %q", shot.ReworkReason)
	}
	if err := shot.Bind(14, now()); err != nil {
		t.Fatalf("rebinding after rework was refused: %v", err)
	}
	if shot.ReworkReason != "" {
		t.Fatal("rebinding kept the previous rework reason")
	}
}

func TestReworkNeedsAReasonWithinBounds(t *testing.T) {
	shot := &Shot{Ordinal: 2, State: ShotRendered}
	if err := shot.SendBackToRework("   ", now()); err == nil {
		t.Fatal("an empty reason was accepted")
	}
	long := make([]rune, 401)
	for i := range long {
		long[i] = '甲'
	}
	if err := shot.SendBackToRework(string(long), now()); err == nil {
		t.Fatal("an oversized reason was accepted")
	}
}

func TestReleaseRenderReturnsAShotToBound(t *testing.T) {
	versionID := int64(3)
	shot := &Shot{Ordinal: 1, State: ShotRendering, PromptVersionID: &versionID}
	if err := shot.ReleaseRender(now()); err != nil {
		t.Fatalf("release was refused: %v", err)
	}
	if shot.State != ShotBound {
		t.Fatalf("released shot is %s, want bound", shot.State)
	}
	if err := shot.ReleaseRender(now()); err == nil {
		t.Fatal("release was accepted twice")
	}
}

func TestTeachableShotsMustBeApprovedAndBound(t *testing.T) {
	versionID := int64(5)
	approved := &Shot{Ordinal: 1, State: ShotApproved, PromptVersionID: &versionID}
	if err := approved.EnsureTeachable(); err != nil {
		t.Fatalf("an approved bound shot was refused: %v", err)
	}
	rendered := &Shot{Ordinal: 2, State: ShotRendered, PromptVersionID: &versionID}
	if err := rendered.EnsureTeachable(); err == nil {
		t.Fatal("a rendered shot was teachable")
	}
	unbound := &Shot{Ordinal: 3, State: ShotApproved}
	if err := unbound.EnsureTeachable(); err == nil {
		t.Fatal("an unbound shot was teachable")
	}
}

func TestDraftValidationBoundsOrdinalTitleAndDirection(t *testing.T) {
	if err := ValidateDraft(Draft{Ordinal: 0, Title: "开场"}); err == nil {
		t.Fatal("ordinal zero was accepted")
	}
	if err := ValidateDraft(Draft{Ordinal: 1000, Title: "开场"}); err == nil {
		t.Fatal("ordinal 1000 was accepted")
	}
	if err := ValidateDraft(Draft{Ordinal: 1, Title: "  "}); err == nil {
		t.Fatal("a blank title was accepted")
	}
	direction := make([]rune, 601)
	for i := range direction {
		direction[i] = '乙'
	}
	if err := ValidateDraft(Draft{Ordinal: 1, Title: "开场", Direction: string(direction)}); err == nil {
		t.Fatal("an oversized direction was accepted")
	}
	if err := ValidateDraft(Draft{Ordinal: 1, Title: "开场", Direction: "缓慢推镜"}); err != nil {
		t.Fatalf("a valid draft was refused: %v", err)
	}
}

func TestAllRenderedAndCountByStateDescribeTheStoryboard(t *testing.T) {
	if AllRendered(nil) {
		t.Fatal("an empty storyboard reported every shot rendered")
	}
	shots := []Shot{
		{Ordinal: 1, State: ShotRendered},
		{Ordinal: 2, State: ShotApproved},
	}
	if !AllRendered(shots) {
		t.Fatal("rendered and approved shots were not recognised")
	}
	shots = append(shots, Shot{Ordinal: 3, State: ShotBound})
	if AllRendered(shots) {
		t.Fatal("a bound shot was counted as rendered")
	}
	counts := CountByState(shots)
	if counts[ShotRendered] != 1 || counts[ShotApproved] != 1 || counts[ShotBound] != 1 {
		t.Fatalf("state counts are %#v", counts)
	}
	counts[ShotBound] = 99
	fresh := CountByState(shots)
	if fresh[ShotBound] != 1 {
		t.Fatal("count map is shared between calls")
	}
}

func TestCloneIsolatesPointerFields(t *testing.T) {
	published := now()
	versionID := int64(9)
	series := Series{Code: "MJ-0008", PublishedAt: &published}
	clonedSeries := series.Clone()
	*clonedSeries.PublishedAt = published.Add(time.Hour)
	if !series.PublishedAt.Equal(published) {
		t.Fatal("series clone shares the publication pointer")
	}

	shot := Shot{Ordinal: 1, PromptVersionID: &versionID}
	clonedShot := shot.Clone()
	*clonedShot.PromptVersionID = 100
	if *shot.PromptVersionID != versionID {
		t.Fatal("shot clone shares the prompt version pointer")
	}
}

func TestParsersRejectUnknownStates(t *testing.T) {
	if _, err := ParseSeriesState("shipping"); !apperr.IsCode(err, apperr.CodeInvalidArgument) {
		t.Fatalf("unknown series state reported %v", apperr.CodeOf(err))
	}
	if state, err := ParseSeriesState(" Published "); err != nil || state != StatePublished {
		t.Fatalf("published was parsed as %q with %v", state, err)
	}
	if _, err := ParseShotState("finished"); !apperr.IsCode(err, apperr.CodeInvalidArgument) {
		t.Fatalf("unknown shot state reported %v", apperr.CodeOf(err))
	}
	if state, err := ParseShotState("REWORK"); err != nil || state != ShotRework {
		t.Fatalf("rework was parsed as %q with %v", state, err)
	}
}

func TestTitleLoglineAndCodePrefixValidation(t *testing.T) {
	if _, err := ValidateTitle("  "); err == nil {
		t.Fatal("a blank series title was accepted")
	}
	if _, err := ValidateCodePrefix("m"); err == nil {
		t.Fatal("a one character prefix was accepted")
	}
	if _, err := ValidateCodePrefix("MJ_01"); err == nil {
		t.Fatal("a prefix with an underscore was accepted")
	}
	prefix, err := ValidateCodePrefix("")
	if err != nil || prefix != "MJ" {
		t.Fatalf("the default prefix is %q with %v", prefix, err)
	}
	if _, err := ValidateLogline(string(make([]rune, 401))); err == nil {
		t.Fatal("an oversized logline was accepted")
	}
}
