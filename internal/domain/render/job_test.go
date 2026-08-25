package render

import (
	"testing"
	"time"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
)

func anchor() time.Time { return time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC) }

func queuedJob() *Job {
	return &Job{
		ID:            42,
		ShotID:        7,
		State:         StateQueued,
		MaxAttempts:   3,
		NextAttemptAt: anchor(),
		CreatedAt:     anchor(),
		UpdatedAt:     anchor(),
	}
}

func TestLeaseTakesOwnershipAndCountsTheAttempt(t *testing.T) {
	job := queuedJob()
	if err := job.Lease("worker-a", anchor(), 30*time.Second); err != nil {
		t.Fatalf("lease was refused: %v", err)
	}
	if job.State != StateLeased {
		t.Fatalf("job state is %s, want leased", job.State)
	}
	if job.Attempts != 1 {
		t.Fatalf("attempts is %d, want 1", job.Attempts)
	}
	if job.LeaseGeneration != 1 {
		t.Fatalf("lease generation is %d, want 1", job.LeaseGeneration)
	}
	if job.LeaseExpiresAt == nil || !job.LeaseExpiresAt.Equal(anchor().Add(30*time.Second)) {
		t.Fatalf("lease expiry is %v", job.LeaseExpiresAt)
	}
}

func TestLeaseRejectsEmptyOwnerAndNonPositiveTTL(t *testing.T) {
	job := queuedJob()
	if err := job.Lease("  ", anchor(), time.Second); !apperr.IsCode(err, apperr.CodeInvalidArgument) {
		t.Fatalf("empty owner reported %v", apperr.CodeOf(err))
	}
	if err := job.Lease("worker-a", anchor(), 0); !apperr.IsCode(err, apperr.CodeInvalidArgument) {
		t.Fatalf("zero ttl reported %v", apperr.CodeOf(err))
	}
}

func TestLeaseIsRefusedWhileAnotherWorkerHoldsIt(t *testing.T) {
	job := queuedJob()
	if err := job.Lease("worker-a", anchor(), time.Minute); err != nil {
		t.Fatalf("first lease was refused: %v", err)
	}
	err := job.Lease("worker-b", anchor().Add(time.Second), time.Minute)
	if !apperr.IsCode(err, apperr.CodeConflict) {
		t.Fatalf("second lease reported %v, want a conflict", apperr.CodeOf(err))
	}
}

func TestExpiredLeaseIsReclaimedWithANewGeneration(t *testing.T) {
	job := queuedJob()
	if err := job.Lease("worker-a", anchor(), time.Minute); err != nil {
		t.Fatalf("first lease was refused: %v", err)
	}
	later := anchor().Add(2 * time.Minute)
	if !job.Due(later) {
		t.Fatal("an expired lease was not reported as due")
	}
	if err := job.Lease("worker-b", later, time.Minute); err != nil {
		t.Fatalf("reclaim was refused: %v", err)
	}
	if job.LeaseOwner != "worker-b" || job.LeaseGeneration != 2 {
		t.Fatalf("reclaimed job is owned by %s at generation %d", job.LeaseOwner, job.LeaseGeneration)
	}
	if err := job.EnsureLeaseHeld("worker-a", 1); !apperr.IsCode(err, apperr.CodeConflict) {
		t.Fatalf("stale owner check reported %v, want a conflict", apperr.CodeOf(err))
	}
	if err := job.EnsureLeaseHeld("worker-b", 2); err != nil {
		t.Fatalf("current owner was rejected: %v", err)
	}
}

func TestJobIsNotDueBeforeItsBackoffWindow(t *testing.T) {
	job := queuedJob()
	job.State = StateRetrying
	job.NextAttemptAt = anchor().Add(time.Minute)
	if job.Due(anchor()) {
		t.Fatal("a retrying job was due inside its backoff window")
	}
	if err := job.Lease("worker-a", anchor(), time.Minute); !apperr.IsCode(err, apperr.CodeFailedPrecondition) {
		t.Fatalf("early lease reported %v", apperr.CodeOf(err))
	}
	if !job.Due(anchor().Add(2 * time.Minute)) {
		t.Fatal("a retrying job stayed undue after its window")
	}
}

func TestFailSchedulesBackoffUntilTheBudgetIsExhausted(t *testing.T) {
	job := queuedJob()
	base := 2 * time.Second

	if err := job.Lease("worker-a", anchor(), time.Minute); err != nil {
		t.Fatalf("lease was refused: %v", err)
	}
	permanent, err := job.Fail("渲染节点掉线", anchor(), base)
	if err != nil {
		t.Fatalf("first failure was refused: %v", err)
	}
	if permanent {
		t.Fatal("the first failure was reported as permanent")
	}
	if job.State != StateRetrying {
		t.Fatalf("job state is %s, want retrying", job.State)
	}
	if !job.NextAttemptAt.Equal(anchor().Add(base)) {
		t.Fatalf("first backoff window ends at %v", job.NextAttemptAt)
	}
	if job.LeaseOwner != "" || job.LeaseExpiresAt != nil {
		t.Fatal("failure kept the lease")
	}

	second := anchor().Add(base)
	if err := job.Requeue(second); err != nil {
		t.Fatalf("requeue was refused: %v", err)
	}
	if err := job.Lease("worker-a", second, time.Minute); err != nil {
		t.Fatalf("second lease was refused: %v", err)
	}
	permanent, err = job.Fail("渲染节点再次掉线", second, base)
	if err != nil {
		t.Fatalf("second failure was refused: %v", err)
	}
	if permanent {
		t.Fatal("the second failure was reported as permanent")
	}
	if !job.NextAttemptAt.Equal(second.Add(2 * base)) {
		t.Fatalf("second backoff window ends at %v, want doubled base", job.NextAttemptAt)
	}

	third := second.Add(2 * base)
	if err := job.Requeue(third); err != nil {
		t.Fatalf("second requeue was refused: %v", err)
	}
	if err := job.Lease("worker-a", third, time.Minute); err != nil {
		t.Fatalf("third lease was refused: %v", err)
	}
	permanent, err = job.Fail("显存不足", third, base)
	if err != nil {
		t.Fatalf("third failure was refused: %v", err)
	}
	if !permanent {
		t.Fatal("the attempt budget was not exhausted after three attempts")
	}
	if job.State != StateFailedPermanent {
		t.Fatalf("job state is %s, want failed_permanent", job.State)
	}
	if job.FinishedAt == nil {
		t.Fatal("permanent failure recorded no finish instant")
	}
	if !job.State.Terminal() {
		t.Fatal("permanent failure is not terminal")
	}
}

func TestRequeueRefusesJobsOutsideTheRetryState(t *testing.T) {
	job := queuedJob()
	if err := job.Requeue(anchor()); !apperr.IsCode(err, apperr.CodeFailedPrecondition) {
		t.Fatalf("requeueing a queued job reported %v", apperr.CodeOf(err))
	}
	job.State = StateRetrying
	job.NextAttemptAt = anchor().Add(time.Minute)
	if err := job.Requeue(anchor()); !apperr.IsCode(err, apperr.CodeFailedPrecondition) {
		t.Fatalf("early requeue reported %v", apperr.CodeOf(err))
	}
}

func TestSucceedRequiresALeaseAndAnArtifact(t *testing.T) {
	job := queuedJob()
	if err := job.Succeed("manju://a/1", anchor()); !apperr.IsCode(err, apperr.CodeFailedPrecondition) {
		t.Fatalf("unleased success reported %v", apperr.CodeOf(err))
	}
	if err := job.Lease("worker-a", anchor(), time.Minute); err != nil {
		t.Fatalf("lease was refused: %v", err)
	}
	if err := job.Succeed("  ", anchor()); !apperr.IsCode(err, apperr.CodeInvalidArgument) {
		t.Fatalf("empty artifact reported %v", apperr.CodeOf(err))
	}
	if err := job.Succeed("manju://a/1", anchor()); err != nil {
		t.Fatalf("success was refused: %v", err)
	}
	if job.State != StateSucceeded || job.FinishedAt == nil || job.LeaseOwner != "" {
		t.Fatalf("succeeded job is %#v", job)
	}
	if err := job.Lease("worker-b", anchor(), time.Minute); !apperr.IsCode(err, apperr.CodeFailedPrecondition) {
		t.Fatalf("leasing a finished job reported %v", apperr.CodeOf(err))
	}
}

func TestCancelIsRefusedOnFinishedJobs(t *testing.T) {
	job := queuedJob()
	if err := job.Cancel(anchor()); err != nil {
		t.Fatalf("cancelling a queued job was refused: %v", err)
	}
	if job.State != StateCancelled || job.FinishedAt == nil {
		t.Fatalf("cancelled job is %#v", job)
	}
	if err := job.Cancel(anchor()); !apperr.IsCode(err, apperr.CodeConflict) {
		t.Fatalf("double cancel reported %v", apperr.CodeOf(err))
	}
	if job.State.Unfinished() {
		t.Fatal("a cancelled job still counts as unfinished")
	}
}

func TestBackoffDoublesAndIsCapped(t *testing.T) {
	base := time.Second
	if got := Backoff(0, base); got != base {
		t.Fatalf("attempt zero produced %v, want the base delay", got)
	}
	if got := Backoff(1, base); got != base {
		t.Fatalf("attempt one produced %v", got)
	}
	if got := Backoff(2, base); got != 2*base {
		t.Fatalf("attempt two produced %v", got)
	}
	if got := Backoff(4, base); got != 8*base {
		t.Fatalf("attempt four produced %v", got)
	}
	if got := Backoff(20, time.Minute); got != 5*time.Minute {
		t.Fatalf("a long backoff produced %v, want the five minute ceiling", got)
	}
	if got := Backoff(1, 0); got != time.Second {
		t.Fatalf("a zero base produced %v, want the one second fallback", got)
	}
}

func TestJobCloneIsolatesTimestamps(t *testing.T) {
	job := queuedJob()
	if err := job.Lease("worker-a", anchor(), time.Minute); err != nil {
		t.Fatalf("lease was refused: %v", err)
	}
	if err := job.Succeed("manju://a/1", anchor()); err != nil {
		t.Fatalf("success was refused: %v", err)
	}
	clone := job.Clone()
	*clone.FinishedAt = anchor().Add(time.Hour)
	if !job.FinishedAt.Equal(anchor()) {
		t.Fatal("clone shares the finish instant")
	}

	leased := queuedJob()
	if err := leased.Lease("worker-a", anchor(), time.Minute); err != nil {
		t.Fatalf("lease was refused: %v", err)
	}
	leasedClone := leased.Clone()
	*leasedClone.LeaseExpiresAt = anchor().Add(time.Hour)
	if !leased.LeaseExpiresAt.Equal(anchor().Add(time.Minute)) {
		t.Fatal("clone shares the lease expiry")
	}
}

func TestLongFailureReasonIsTruncated(t *testing.T) {
	job := queuedJob()
	if err := job.Lease("worker-a", anchor(), time.Minute); err != nil {
		t.Fatalf("lease was refused: %v", err)
	}
	reason := make([]byte, 900)
	for i := range reason {
		reason[i] = 'x'
	}
	if _, err := job.Fail(string(reason), anchor(), time.Second); err != nil {
		t.Fatalf("failure was refused: %v", err)
	}
	if len(job.LastError) != 500 {
		t.Fatalf("stored reason has %d bytes, want the 500 byte bound", len(job.LastError))
	}
}
