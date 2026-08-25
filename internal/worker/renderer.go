// Package worker runs the background render pipeline and the periodic sweeper.
// Nothing here talks to an external service: rendering is a deterministic local
// transformation of the frozen prompt body.
package worker

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
	"github.com/vance1852/manjuflow-studio/internal/logging"
	"github.com/vance1852/manjuflow-studio/internal/service/rendersvc"
)

// Request is the work handed to a renderer.
type Request struct {
	SeriesCode string
	Ordinal    int
	ShotTitle  string
	PromptBody string
	Attempt    int
}

// Renderer turns a prompt into an artifact reference.
type Renderer interface {
	Render(ctx context.Context, request Request) (string, error)
}

// StoryboardRenderer is the production renderer. It validates the prompt shape
// once more and derives a stable artifact reference, which keeps the pipeline
// reproducible without any network dependency.
type StoryboardRenderer struct{}

// Render produces the artifact reference for one attempt.
func (StoryboardRenderer) Render(ctx context.Context, request Request) (string, error) {
	if err := apperr.FromContext(ctx); err != nil {
		return "", err
	}
	if strings.TrimSpace(request.PromptBody) == "" {
		return "", apperr.New(apperr.CodeFailedPrecondition, "prompt body is empty")
	}
	panels := strings.Count(request.PromptBody, "[panel]")
	if panels > 24 {
		return "", apperr.New(apperr.CodeInvalidArgument, "prompt declares %d panels, the renderer accepts 24", panels)
	}
	return "manju://" + request.SeriesCode + "/shot-" + strconv.Itoa(request.Ordinal) +
		"/take-" + strconv.Itoa(request.Attempt), nil
}

// RenderWorker polls for due jobs and publishes their results.
type RenderWorker struct {
	name     string
	renders  *rendersvc.Service
	renderer Renderer
	logger   *logging.Logger
	interval time.Duration

	stopped chan struct{}
	once    sync.Once
}

// RenderOptions configures the worker.
type RenderOptions struct {
	Name     string
	Renders  *rendersvc.Service
	Renderer Renderer
	Logger   *logging.Logger
	Interval time.Duration
}

// NewRenderWorker builds a render worker.
func NewRenderWorker(opts RenderOptions) *RenderWorker {
	interval := opts.Interval
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		name = "render-worker"
	}
	return &RenderWorker{
		name:     name,
		renders:  opts.Renders,
		renderer: opts.Renderer,
		logger:   opts.Logger,
		interval: interval,
		stopped:  make(chan struct{}),
	}
}

// Name reports the lease owner identity of this worker.
func (w *RenderWorker) Name() string { return w.name }

// Run polls until the context is cancelled. It always closes its stopped channel
// so a shutdown can wait for a clean exit.
func (w *RenderWorker) Run(ctx context.Context) {
	defer w.once.Do(func() { close(w.stopped) })
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			w.logger.Info("render worker stopped", "worker", w.name, "reason", ctx.Err())
			return
		case <-timer.C:
		}
		worked, err := w.ProcessOne(ctx)
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			w.logger.Info("render worker stopped mid-cycle", "worker", w.name)
			return
		case err != nil:
			w.logger.Warn("render cycle failed", "worker", w.name, "error", err)
		}
		delay := w.interval
		if worked {
			delay = time.Millisecond
		}
		timer.Reset(delay)
	}
}

// Wait blocks until Run returned.
func (w *RenderWorker) Wait() { <-w.stopped }

// ProcessOne leases at most one job and publishes its outcome. It reports whether
// work was found.
func (w *RenderWorker) ProcessOne(ctx context.Context) (bool, error) {
	assignment, err := w.renders.Claim(ctx, w.name)
	if err != nil {
		if errors.Is(err, rendersvc.ErrNoWork) || apperr.IsCode(err, apperr.CodeNotFound) {
			return false, nil
		}
		return false, err
	}
	artifact, renderErr := w.renderer.Render(ctx, Request{
		SeriesCode: assignment.SeriesCode,
		Ordinal:    assignment.Ordinal,
		ShotTitle:  assignment.ShotTitle,
		PromptBody: assignment.PromptBody,
		Attempt:    assignment.Job.Attempts,
	})
	if renderErr != nil {
		permanent, failErr := w.renders.Fail(ctx, assignment, w.name, renderErr)
		if failErr != nil {
			return true, failErr
		}
		w.logger.Warn("render attempt failed",
			"worker", w.name,
			"job_id", assignment.Job.ID,
			"attempt", assignment.Job.Attempts,
			"permanent", permanent,
			"error", renderErr)
		return true, nil
	}
	if err := w.renders.Complete(ctx, assignment, w.name, artifact); err != nil {
		return true, err
	}
	w.logger.Info("render attempt succeeded",
		"worker", w.name,
		"job_id", assignment.Job.ID,
		"artifact_ref", artifact)
	return true, nil
}
