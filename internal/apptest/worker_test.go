package apptest

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
	"github.com/vance1852/manjuflow-studio/internal/config"
	"github.com/vance1852/manjuflow-studio/internal/logging"
	"github.com/vance1852/manjuflow-studio/internal/service/rendersvc"
	"github.com/vance1852/manjuflow-studio/internal/worker"
)

func TestSuccessfulRenderStoresTheArtifactAndMovesSeriesIntoReview(t *testing.T) {
	h := newHarness(t)
	token := h.directorToken()
	board := h.seedStoryboard(token, 2)

	for _, shotID := range board.ShotIDs {
		if reply := h.submitRender(token, shotID, ""); reply.Status != http.StatusAccepted {
			t.Fatalf("submission returned %d: %s", reply.Status, reply.Body)
		}
	}
	renderer := NewScriptedRenderer(Succeed("manju://night-market/shot-1"), Succeed("manju://night-market/shot-2"))
	if processed := h.processRenders(renderer, 6); processed != 2 {
		t.Fatalf("worker processed %d jobs, want 2", processed)
	}
	if state := h.shotState(token, board, 1); state != "rendered" {
		t.Fatalf("shot 1 is %q, want rendered", state)
	}
	if state := h.seriesState(token, board.SeriesID); state != "reviewing" {
		t.Fatalf("series is %q, want reviewing once every shot rendered", state)
	}

	jobs := h.mustCall(requestSpec{
		method: http.MethodGet,
		path:   "/v1/shots/" + itoa(board.ShotIDs[0]) + "/render-jobs",
		token:  token,
	}, http.StatusOK)
	items := jobs.Decoded["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("shot has %d render jobs, want 1", len(items))
	}
	job := items[0].(map[string]any)
	if job["state"] != "succeeded" {
		t.Fatalf("job state is %v, want succeeded", job["state"])
	}
	if job["artifact_ref"] == "" {
		t.Fatal("succeeded job stores no artifact reference")
	}
	if int(job["attempts"].(float64)) != 1 {
		t.Fatalf("job used %v attempts, want 1", job["attempts"])
	}
}

func TestFailedRenderBacksOffAndSucceedsOnTheSecondAttempt(t *testing.T) {
	h := newHarness(t)
	token := h.directorToken()
	board := h.seedStoryboard(token, 1)

	if reply := h.submitRender(token, board.ShotIDs[0], ""); reply.Status != http.StatusAccepted {
		t.Fatalf("submission returned %d", reply.Status)
	}
	renderer := NewScriptedRenderer(Fail("渲染节点掉线"), Succeed("manju://retry/ok"))

	if processed := h.processRenders(renderer, 1); processed != 1 {
		t.Fatal("worker did not lease the queued job")
	}
	jobs := h.mustCall(requestSpec{
		method: http.MethodGet,
		path:   "/v1/shots/" + itoa(board.ShotIDs[0]) + "/render-jobs",
		token:  token,
	}, http.StatusOK)
	job := jobs.Decoded["items"].([]any)[0].(map[string]any)
	if job["state"] != "retrying" {
		t.Fatalf("job state after failure is %v, want retrying", job["state"])
	}
	if job["last_error"] == "" {
		t.Fatal("retrying job stores no failure reason")
	}
	if state := h.shotState(token, board, 1); state != "rendering" {
		t.Fatalf("shot left rendering while a retry is pending: %q", state)
	}

	if processed := h.processRenders(renderer, 1); processed != 0 {
		t.Fatal("job was leased again before its backoff window elapsed")
	}

	h.clock.Advance(h.cfg.RenderBackoffBase * 4)
	requeued, err := h.app.Services.Render.RequeueDue(context.Background(), 10)
	if err != nil {
		t.Fatalf("cannot requeue due jobs: %v", err)
	}
	if requeued != 1 {
		t.Fatalf("requeued %d jobs, want 1", requeued)
	}
	if processed := h.processRenders(renderer, 2); processed != 1 {
		t.Fatal("worker did not pick the requeued job up")
	}
	if state := h.shotState(token, board, 1); state != "rendered" {
		t.Fatalf("shot is %q, want rendered after the successful retry", state)
	}
	if renderer.Calls() != 2 {
		t.Fatalf("renderer ran %d times, want 2", renderer.Calls())
	}
}

func TestExhaustedRenderReleasesTheShotAndTheQuotaSlot(t *testing.T) {
	h := newHarnessWith(t, func(cfg *config.Config) { cfg.RenderMaxAttempts = 2 })
	token := h.directorToken()
	board := h.seedStoryboard(token, 1)

	if reply := h.submitRender(token, board.ShotIDs[0], ""); reply.Status != http.StatusAccepted {
		t.Fatalf("submission returned %d", reply.Status)
	}
	renderer := NewScriptedRenderer(Fail("显存不足"), Fail("显存不足"))

	h.processRenders(renderer, 1)
	h.clock.Advance(h.cfg.RenderBackoffBase * 8)
	if _, err := h.app.Services.Render.RequeueDue(context.Background(), 10); err != nil {
		t.Fatalf("cannot requeue due jobs: %v", err)
	}
	h.processRenders(renderer, 1)

	jobs := h.mustCall(requestSpec{
		method: http.MethodGet,
		path:   "/v1/shots/" + itoa(board.ShotIDs[0]) + "/render-jobs",
		token:  token,
	}, http.StatusOK)
	job := jobs.Decoded["items"].([]any)[0].(map[string]any)
	if job["state"] != "failed_permanent" {
		t.Fatalf("job state is %v, want failed_permanent", job["state"])
	}
	if state := h.shotState(token, board, 1); state != "bound" {
		t.Fatalf("shot is %q, want bound so the director can plan a new take", state)
	}
	quota := h.mustCall(requestSpec{method: http.MethodGet, path: "/v1/render-quota", token: token}, http.StatusOK)
	if int(quota.Decoded["used"].(float64)) != 0 {
		t.Fatalf("permanent failure kept the quota slot: used=%v", quota.Decoded["used"])
	}

	if reply := h.submitRender(token, board.ShotIDs[0], ""); reply.Status != http.StatusAccepted {
		t.Fatalf("resubmission after permanent failure returned %d: %s", reply.Status, reply.Body)
	}
}

func TestReclaimedLeaseRejectsTheStaleWorkerResult(t *testing.T) {
	h := newHarnessWith(t, func(cfg *config.Config) { cfg.RenderLeaseTTL = 2 * time.Second })
	token := h.directorToken()
	board := h.seedStoryboard(token, 1)

	if reply := h.submitRender(token, board.ShotIDs[0], ""); reply.Status != http.StatusAccepted {
		t.Fatalf("submission returned %d", reply.Status)
	}
	ctx := context.Background()
	stalled, err := h.app.Services.Render.Claim(ctx, "worker-stalled")
	if err != nil {
		t.Fatalf("first worker cannot claim: %v", err)
	}

	h.clock.Advance(h.cfg.RenderLeaseTTL + time.Second)

	fresh, err := h.app.Services.Render.Claim(ctx, "worker-fresh")
	if err != nil {
		t.Fatalf("second worker cannot reclaim the expired lease: %v", err)
	}
	if fresh.Generation <= stalled.Generation {
		t.Fatalf("reclaim did not advance the fencing generation: %d then %d", stalled.Generation, fresh.Generation)
	}

	staleErr := h.app.Services.Render.Complete(ctx, stalled, "worker-stalled", "manju://stale/take")
	if staleErr == nil {
		t.Fatal("stale worker published its result after losing the lease")
	}
	if !apperr.IsCode(staleErr, apperr.CodeConflict) {
		t.Fatalf("stale publication failed with %v, want a conflict", staleErr)
	}

	if err := h.app.Services.Render.Complete(ctx, fresh, "worker-fresh", "manju://fresh/take"); err != nil {
		t.Fatalf("fresh worker cannot publish: %v", err)
	}
	if state := h.shotState(token, board, 1); state != "rendered" {
		t.Fatalf("shot is %q, want rendered", state)
	}
}

func TestClaimReportsAnEmptyQueue(t *testing.T) {
	h := newHarness(t)

	_, err := h.app.Services.Render.Claim(context.Background(), "idle-worker")
	if !errors.Is(err, rendersvc.ErrNoWork) && !apperr.IsCode(err, apperr.CodeNotFound) {
		t.Fatalf("empty queue reported %v, want the no-work signal", err)
	}
}

func TestWorkerStopsOnContextCancellationWithoutLosingTheJob(t *testing.T) {
	h := newHarness(t)
	token := h.directorToken()
	board := h.seedStoryboard(token, 1)

	if reply := h.submitRender(token, board.ShotIDs[0], ""); reply.Status != http.StatusAccepted {
		t.Fatalf("submission returned %d", reply.Status)
	}

	instance := worker.NewRenderWorker(worker.RenderOptions{
		Name:     "cancellable-worker",
		Renders:  h.app.Services.Render,
		Renderer: blockingRenderer{started: make(chan struct{}, 1)},
		Logger:   logging.New(io.Discard, logging.LevelError),
		Interval: time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		instance.Run(ctx)
		close(done)
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not stop after cancellation")
	}
	instance.Wait()

	jobs := h.mustCall(requestSpec{
		method: http.MethodGet,
		path:   "/v1/shots/" + itoa(board.ShotIDs[0]) + "/render-jobs",
		token:  token,
	}, http.StatusOK)
	job := jobs.Decoded["items"].([]any)[0].(map[string]any)
	if job["state"] == "succeeded" {
		t.Fatal("cancelled render reported success")
	}
}

func TestProductionRendererRejectsAnOversizedStoryboard(t *testing.T) {
	renderer := worker.StoryboardRenderer{}

	if _, err := renderer.Render(context.Background(), worker.Request{
		SeriesCode: "MJ-0001",
		Ordinal:    1,
		PromptBody: PromptBody("主角", "水墨", 40),
		Attempt:    1,
	}); err == nil {
		t.Fatal("renderer accepted 40 panels")
	}

	artifact, err := renderer.Render(context.Background(), worker.Request{
		SeriesCode: "MJ-0001",
		Ordinal:    2,
		PromptBody: PromptBody("主角", "水墨", 6),
		Attempt:    3,
	})
	if err != nil {
		t.Fatalf("renderer rejected a valid storyboard: %v", err)
	}
	if artifact != "manju://MJ-0001/shot-2/take-3" {
		t.Fatalf("artifact reference %q is not derived from the request", artifact)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := renderer.Render(cancelled, worker.Request{PromptBody: PromptBody("a", "b", 1)}); err == nil {
		t.Fatal("renderer ignored a cancelled context")
	}
}

func TestSweepPromotesRetriesAndOverdueWorkshops(t *testing.T) {
	h := newHarness(t)
	token := h.directorToken()
	board := h.seedStoryboard(token, 1)

	if reply := h.submitRender(token, board.ShotIDs[0], ""); reply.Status != http.StatusAccepted {
		t.Fatalf("submission returned %d", reply.Status)
	}
	renderer := NewScriptedRenderer(Fail("节点抖动"))
	h.processRenders(renderer, 1)

	sweeper := worker.NewSweeper(worker.SweepOptions{
		Renders:  h.app.Services.Render,
		Teaching: h.app.Services.Teaching,
		Auth:     h.app.Services.Auth,
		Logger:   logging.New(io.Discard, logging.LevelError),
		Interval: time.Second,
	})

	report, err := sweeper.SweepOnce(context.Background())
	if err != nil {
		t.Fatalf("sweep failed: %v", err)
	}
	if report.RequeuedJobs != 0 {
		t.Fatalf("sweep requeued %d jobs before the backoff window", report.RequeuedJobs)
	}

	h.clock.Advance(h.cfg.RenderBackoffBase * 4)
	report, err = sweeper.SweepOnce(context.Background())
	if err != nil {
		t.Fatalf("second sweep failed: %v", err)
	}
	if report.RequeuedJobs != 1 {
		t.Fatalf("sweep requeued %d jobs, want 1", report.RequeuedJobs)
	}
}

// blockingRenderer blocks until the context is cancelled, which lets the suite
// observe a shutdown in the middle of a render.
type blockingRenderer struct {
	started chan struct{}
}

func (b blockingRenderer) Render(ctx context.Context, request worker.Request) (string, error) {
	select {
	case b.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return "", apperr.Wrap(ctx.Err(), apperr.CodeCanceled, "render interrupted")
}
