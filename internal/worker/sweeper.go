package worker

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/vance1852/manjuflow-studio/internal/logging"
	"github.com/vance1852/manjuflow-studio/internal/service/authsvc"
	"github.com/vance1852/manjuflow-studio/internal/service/rendersvc"
	"github.com/vance1852/manjuflow-studio/internal/service/teachingsvc"
)

// Sweeper performs the periodic housekeeping that keeps the pipeline moving after
// a restart: retrying jobs are requeued, overdue workshops move to grading and
// expired sessions are pruned.
type Sweeper struct {
	renders  *rendersvc.Service
	teaching *teachingsvc.Service
	auth     *authsvc.Service
	logger   *logging.Logger
	interval time.Duration
	batch    int

	stopped chan struct{}
	once    sync.Once
}

// SweepOptions configures the sweeper.
type SweepOptions struct {
	Renders  *rendersvc.Service
	Teaching *teachingsvc.Service
	Auth     *authsvc.Service
	Logger   *logging.Logger
	Interval time.Duration
	Batch    int
}

// NewSweeper builds the housekeeping worker.
func NewSweeper(opts SweepOptions) *Sweeper {
	interval := opts.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	batch := opts.Batch
	if batch <= 0 {
		batch = 20
	}
	return &Sweeper{
		renders:  opts.Renders,
		teaching: opts.Teaching,
		auth:     opts.Auth,
		logger:   opts.Logger,
		interval: interval,
		batch:    batch,
		stopped:  make(chan struct{}),
	}
}

// Report describes the outcome of one sweep.
type Report struct {
	RequeuedJobs     int
	GradingWorkshops int
	PrunedSessions   int
}

// Run sweeps until the context is cancelled.
func (s *Sweeper) Run(ctx context.Context) {
	defer s.once.Do(func() { close(s.stopped) })
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.logger.Info("sweeper stopped", "reason", ctx.Err())
			return
		case <-ticker.C:
		}
		report, err := s.SweepOnce(ctx)
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			s.logger.Info("sweeper stopped mid-cycle")
			return
		case err != nil:
			s.logger.Warn("sweep failed", "error", err)
			continue
		}
		if report.RequeuedJobs > 0 || report.GradingWorkshops > 0 || report.PrunedSessions > 0 {
			s.logger.Info("sweep completed",
				"requeued_jobs", report.RequeuedJobs,
				"grading_workshops", report.GradingWorkshops,
				"pruned_sessions", report.PrunedSessions)
		}
	}
}

// Wait blocks until Run returned.
func (s *Sweeper) Wait() { <-s.stopped }

// SweepOnce performs a single housekeeping pass.
func (s *Sweeper) SweepOnce(ctx context.Context) (Report, error) {
	var report Report
	requeued, err := s.renders.RequeueDue(ctx, s.batch)
	if err != nil {
		return report, err
	}
	report.RequeuedJobs = requeued

	moved, err := s.teaching.SweepOverdue(ctx, s.batch)
	if err != nil {
		return report, err
	}
	report.GradingWorkshops = moved

	pruned, err := s.auth.PruneExpiredSessions(ctx)
	if err != nil {
		return report, err
	}
	report.PrunedSessions = pruned
	return report, nil
}
