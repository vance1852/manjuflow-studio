package teaching

import (
	"strings"
	"testing"
	"time"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
)

func base() time.Time { return time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC) }

func openWorkshop() *Workshop {
	return &Workshop{
		ID:       17,
		State:    StateOpen,
		Capacity: 2,
		Enrolled: 0,
		OpensAt:  base(),
		ClosesAt: base().Add(48 * time.Hour),
		Version:  1,
	}
}

func TestEnrolmentWindowAndSeatLimitAreEnforced(t *testing.T) {
	workshop := openWorkshop()
	if err := workshop.EnsureEnrollable(base()); err != nil {
		t.Fatalf("enrolment at the opening instant was refused: %v", err)
	}
	if err := workshop.EnsureEnrollable(base().Add(-time.Hour)); !apperr.IsCode(err, apperr.CodeFailedPrecondition) {
		t.Fatalf("early enrolment reported %v", apperr.CodeOf(err))
	}
	if err := workshop.EnsureEnrollable(workshop.ClosesAt); !apperr.IsCode(err, apperr.CodeFailedPrecondition) {
		t.Fatalf("enrolment at the deadline reported %v", apperr.CodeOf(err))
	}

	workshop.Enrolled = 2
	err := workshop.EnsureEnrollable(base())
	if !apperr.IsCode(err, apperr.CodeExhausted) {
		t.Fatalf("a full workshop reported %v, want exhausted", apperr.CodeOf(err))
	}
	typed, ok := apperr.As(err)
	if !ok || typed.Details["capacity"] != "2" {
		t.Fatalf("refusal does not report the capacity: %#v", err)
	}

	workshop.Enrolled = 0
	workshop.State = StateGrading
	if err := workshop.EnsureEnrollable(base()); err == nil {
		t.Fatal("a grading workshop accepted enrolment")
	}
}

func TestSubmissionWindowFollowsStateAndDeadline(t *testing.T) {
	workshop := openWorkshop()
	if err := workshop.EnsureAcceptsSubmission(base()); err != nil {
		t.Fatalf("submission during enrolment was refused: %v", err)
	}
	workshop.State = StateTeaching
	if err := workshop.EnsureAcceptsSubmission(base().Add(time.Hour)); err != nil {
		t.Fatalf("submission during teaching was refused: %v", err)
	}
	if err := workshop.EnsureAcceptsSubmission(workshop.ClosesAt.Add(time.Second)); err == nil {
		t.Fatal("a late submission was accepted")
	}
	workshop.State = StateGrading
	if err := workshop.EnsureAcceptsSubmission(base()); err == nil {
		t.Fatal("a grading workshop accepted a submission")
	}
}

func TestWorkshopTransitionsFollowTheTeachingOrder(t *testing.T) {
	workshop := openWorkshop()
	if err := workshop.Transition(StateClosed, base()); err == nil {
		t.Fatal("an open workshop closed without passing through grading")
	}
	if err := workshop.Transition(StateTeaching, base()); err != nil {
		t.Fatalf("teaching transition was refused: %v", err)
	}
	if err := workshop.Transition(StateTeaching, base()); !apperr.IsCode(err, apperr.CodeConflict) {
		t.Fatalf("repeated transition reported %v", apperr.CodeOf(err))
	}
	if err := workshop.Transition(StateGrading, base()); err != nil {
		t.Fatalf("grading transition was refused: %v", err)
	}
	if err := workshop.Transition(StateTeaching, base()); err != nil {
		t.Fatalf("returning to teaching was refused: %v", err)
	}
	if err := workshop.Transition(StateGrading, base()); err != nil {
		t.Fatalf("second grading transition was refused: %v", err)
	}
	if err := workshop.Transition(StateClosed, base()); err != nil {
		t.Fatalf("closing was refused: %v", err)
	}
	if err := workshop.Transition(StateTeaching, base()); err == nil {
		t.Fatal("a closed workshop reopened")
	}
}

func TestClosingRequiresEveryReviewDone(t *testing.T) {
	workshop := openWorkshop()
	workshop.State = StateGrading
	err := workshop.EnsureClosable(3)
	if !apperr.IsCode(err, apperr.CodeFailedPrecondition) {
		t.Fatalf("closing with pending reviews reported %v", apperr.CodeOf(err))
	}
	typed, ok := apperr.As(err)
	if !ok || typed.Details["pending_reviews"] != "3" {
		t.Fatalf("refusal does not report the pending count: %#v", err)
	}
	if err := workshop.EnsureClosable(0); err != nil {
		t.Fatalf("closing a fully reviewed workshop was refused: %v", err)
	}
	workshop.State = StateTeaching
	if err := workshop.EnsureClosable(0); err == nil {
		t.Fatal("a teaching workshop was closable")
	}
}

func TestPromptVersionIsHeldUntilTheWorkshopEnds(t *testing.T) {
	workshop := openWorkshop()
	for _, state := range []WorkshopState{StateOpen, StateTeaching, StateGrading} {
		workshop.State = state
		if !workshop.ReferencesPromptVersion() {
			t.Fatalf("a %s workshop released its prompt version", state)
		}
	}
	for _, state := range []WorkshopState{StateClosed, StateCancelled} {
		workshop.State = state
		if workshop.ReferencesPromptVersion() {
			t.Fatalf("a %s workshop still holds its prompt version", state)
		}
	}
}

func TestScheduleCapacityAndTitleValidation(t *testing.T) {
	if err := ValidateSchedule(time.Time{}, base()); err == nil {
		t.Fatal("a zero opening instant was accepted")
	}
	if err := ValidateSchedule(base(), base()); err == nil {
		t.Fatal("a zero length window was accepted")
	}
	if err := ValidateSchedule(base(), base().Add(200*24*time.Hour)); err == nil {
		t.Fatal("a 200 day window was accepted")
	}
	if err := ValidateSchedule(base(), base().Add(time.Hour)); err != nil {
		t.Fatalf("a one hour window was refused: %v", err)
	}
	if err := ValidateCapacity(0); err == nil {
		t.Fatal("zero seats were accepted")
	}
	if err := ValidateCapacity(201); err == nil {
		t.Fatal("201 seats were accepted")
	}
	if _, err := ValidateTitle("   "); err == nil {
		t.Fatal("a blank title was accepted")
	}
	if _, err := ValidateTitle(strings.Repeat("甲", 121)); err == nil {
		t.Fatal("an oversized title was accepted")
	}
}

func TestPracticeBodyBounds(t *testing.T) {
	if _, err := ValidatePracticeBody("太短了"); err == nil {
		t.Fatal("a short practice body was accepted")
	}
	if _, err := ValidatePracticeBody(strings.Repeat("甲", 8001)); err == nil {
		t.Fatal("an oversized practice body was accepted")
	}
	body, err := ValidatePracticeBody("  这是一段足够长的练习说明，用来描述分镜复刻的过程。  ")
	if err != nil {
		t.Fatalf("a valid practice body was refused: %v", err)
	}
	if strings.HasPrefix(body, " ") {
		t.Fatal("the practice body was not trimmed")
	}
}

func TestReviewDerivesTheOutcomeFromThePassMark(t *testing.T) {
	submission := &Submission{ID: 5, State: Pending}
	if err := submission.Review(-1, "", 9, base()); !apperr.IsCode(err, apperr.CodeInvalidArgument) {
		t.Fatalf("a negative score reported %v", apperr.CodeOf(err))
	}
	if err := submission.Review(101, "", 9, base()); !apperr.IsCode(err, apperr.CodeInvalidArgument) {
		t.Fatalf("a score above 100 reported %v", apperr.CodeOf(err))
	}
	if err := submission.Review(PassMark-1, "", 9, base()); err == nil {
		t.Fatal("a failing score without feedback was accepted")
	}
	if err := submission.Review(PassMark-1, "构图仍然偏散", 9, base()); err != nil {
		t.Fatalf("a documented failing score was refused: %v", err)
	}
	if submission.State != Returned {
		t.Fatalf("submission state is %s, want returned", submission.State)
	}
	if submission.ReviewedBy == nil || *submission.ReviewedBy != 9 {
		t.Fatalf("reviewer is %v", submission.ReviewedBy)
	}
	if submission.ReviewedAt == nil {
		t.Fatal("review instant was not recorded")
	}
	if err := submission.Review(90, "", 9, base()); !apperr.IsCode(err, apperr.CodeConflict) {
		t.Fatalf("a second review reported %v", apperr.CodeOf(err))
	}

	passing := &Submission{ID: 6, State: Pending}
	if err := passing.Review(PassMark, "", 9, base()); err != nil {
		t.Fatalf("a passing score was refused: %v", err)
	}
	if passing.State != Accepted {
		t.Fatalf("passing submission is %s, want accepted", passing.State)
	}
}

func TestReviewRejectsOversizedFeedback(t *testing.T) {
	submission := &Submission{ID: 7, State: Pending}
	if err := submission.Review(80, strings.Repeat("甲", 1001), 9, base()); err == nil {
		t.Fatal("oversized feedback was accepted")
	}
}

func TestEnrolmentAdvancesOnlyAfterASubmission(t *testing.T) {
	enrollment := &Enrollment{ID: 3, State: Enrolled}
	if err := enrollment.MarkGraded(base()); !apperr.IsCode(err, apperr.CodeFailedPrecondition) {
		t.Fatalf("grading before submission reported %v", apperr.CodeOf(err))
	}
	if err := enrollment.MarkSubmitted(base()); err != nil {
		t.Fatalf("submission was refused: %v", err)
	}
	if enrollment.State != Submitted {
		t.Fatalf("enrolment state is %s", enrollment.State)
	}
	if err := enrollment.MarkGraded(base()); err != nil {
		t.Fatalf("grading was refused: %v", err)
	}
	if enrollment.State != Graded {
		t.Fatalf("enrolment state is %s, want graded", enrollment.State)
	}
	if err := enrollment.MarkSubmitted(base()); err != nil {
		t.Fatalf("a graded apprentice cannot submit again: %v", err)
	}
	if enrollment.State != Submitted {
		t.Fatalf("resubmission left the enrolment at %s", enrollment.State)
	}
}

func TestSubmissionCloneIsolatesReviewPointers(t *testing.T) {
	submission := &Submission{ID: 8, State: Pending}
	if err := submission.Review(95, "很好", 11, base()); err != nil {
		t.Fatalf("review was refused: %v", err)
	}
	clone := submission.Clone()
	*clone.ReviewedBy = 999
	*clone.ReviewedAt = base().Add(time.Hour)
	if *submission.ReviewedBy != 11 {
		t.Fatal("clone shares the reviewer pointer")
	}
	if !submission.ReviewedAt.Equal(base()) {
		t.Fatal("clone shares the review instant")
	}
}

func TestWorkshopStateParsing(t *testing.T) {
	if state, err := ParseWorkshopState(" Teaching "); err != nil || state != StateTeaching {
		t.Fatalf("teaching parsed as %q with %v", state, err)
	}
	if _, err := ParseWorkshopState("archived"); !apperr.IsCode(err, apperr.CodeInvalidArgument) {
		t.Fatalf("unknown workshop state reported %v", apperr.CodeOf(err))
	}
}

func TestAnEmptyOpenWorkshopCanBeGradedAndClosed(t *testing.T) {
	workshop := openWorkshop()
	if err := workshop.Transition(StateGrading, base()); err != nil {
		t.Fatalf("stopping enrolment on an empty workshop was refused: %v", err)
	}
	if err := workshop.EnsureClosable(0); err != nil {
		t.Fatalf("an empty graded workshop was not closable: %v", err)
	}
	if err := workshop.Transition(StateClosed, base()); err != nil {
		t.Fatalf("closing was refused: %v", err)
	}
}
