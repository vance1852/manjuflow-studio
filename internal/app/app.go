// Package app wires configuration, storage, services, HTTP and background
// workers into one runnable application. Tests build the same graph, so the
// production path and the integration path never drift.
package app

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
	"github.com/vance1852/manjuflow-studio/internal/auditlog"
	"github.com/vance1852/manjuflow-studio/internal/clock"
	"github.com/vance1852/manjuflow-studio/internal/config"
	"github.com/vance1852/manjuflow-studio/internal/httpapi"
	"github.com/vance1852/manjuflow-studio/internal/idempotency"
	"github.com/vance1852/manjuflow-studio/internal/logging"
	"github.com/vance1852/manjuflow-studio/internal/repository"
	"github.com/vance1852/manjuflow-studio/internal/repository/sqliterepo"
	"github.com/vance1852/manjuflow-studio/internal/security"
	"github.com/vance1852/manjuflow-studio/internal/service/authsvc"
	"github.com/vance1852/manjuflow-studio/internal/service/productionsvc"
	"github.com/vance1852/manjuflow-studio/internal/service/promptsvc"
	"github.com/vance1852/manjuflow-studio/internal/service/rendersvc"
	"github.com/vance1852/manjuflow-studio/internal/service/teachingsvc"
	"github.com/vance1852/manjuflow-studio/internal/storage/sqlitedb"
	"github.com/vance1852/manjuflow-studio/internal/worker"
)

// Repositories bundles the persistence implementations.
type Repositories struct {
	Studios     repository.StudioRepository
	Users       repository.UserRepository
	Sessions    repository.SessionRepository
	Prompts     repository.PromptRepository
	Series      repository.SeriesRepository
	Shots       repository.ShotRepository
	Renders     repository.RenderRepository
	Quotas      repository.QuotaRepository
	Teaching    repository.TeachingRepository
	Audits      repository.AuditRepository
	Idempotency repository.IdempotencyRepository
	Sequences   repository.SequenceRepository
}

// NewRepositories builds the SQLite implementations.
func NewRepositories() Repositories {
	return Repositories{
		Studios:     sqliterepo.NewStudioStore(),
		Users:       sqliterepo.NewUserStore(),
		Sessions:    sqliterepo.NewSessionStore(),
		Prompts:     sqliterepo.NewPromptStore(),
		Series:      sqliterepo.NewSeriesStore(),
		Shots:       sqliterepo.NewShotStore(),
		Renders:     sqliterepo.NewRenderStore(),
		Quotas:      sqliterepo.NewQuotaStore(),
		Teaching:    sqliterepo.NewTeachingStore(),
		Audits:      sqliterepo.NewAuditStore(),
		Idempotency: sqliterepo.NewIdempotencyStore(),
		Sequences:   sqliterepo.NewSequenceStore(),
	}
}

// Services bundles the use case layer.
type Services struct {
	Auth       *authsvc.Service
	Prompts    *promptsvc.Service
	Production *productionsvc.Service
	Render     *rendersvc.Service
	Teaching   *teachingsvc.Service
}

// App is the assembled application.
type App struct {
	Config       config.Config
	Logger       *logging.Logger
	Clock        clock.Clock
	DB           *sqlitedb.DB
	Repositories Repositories
	Services     Services
	Handler      http.Handler

	renderWorkers []*worker.RenderWorker
	sweeper       *worker.Sweeper
	workers       sync.WaitGroup
	schemaVersion int
}

// Build opens the database, applies migrations, seeds the studio and wires
// everything together.
func Build(ctx context.Context, cfg config.Config, logger *logging.Logger, timeSource clock.Clock) (*App, error) {
	db, err := sqlitedb.Open(ctx, sqlitedb.DefaultOptions(cfg.DatabasePath))
	if err != nil {
		return nil, err
	}
	schemaVersion, err := db.Migrate(ctx)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	repos := NewRepositories()
	recorder := auditlog.New(repos.Audits, timeSource)
	guard := idempotency.New(repos.Idempotency, timeSource)

	auth := authsvc.New(authsvc.Options{
		Runner:     db,
		Studios:    repos.Studios,
		Users:      repos.Users,
		Sessions:   repos.Sessions,
		Audits:     recorder,
		Clock:      timeSource,
		SessionTTL: cfg.SessionTTL,
	})
	prompts := promptsvc.New(promptsvc.Options{
		Runner:   db,
		Prompts:  repos.Prompts,
		Shots:    repos.Shots,
		Renders:  repos.Renders,
		Teaching: repos.Teaching,
		Audits:   recorder,
		Clock:    timeSource,
	})
	production := productionsvc.New(productionsvc.Options{
		Runner:      db,
		Studios:     repos.Studios,
		Series:      repos.Series,
		Shots:       repos.Shots,
		Prompts:     repos.Prompts,
		Renders:     repos.Renders,
		Quotas:      repos.Quotas,
		Teaching:    repos.Teaching,
		Sequences:   repos.Sequences,
		Audits:      recorder,
		Guard:       guard,
		Clock:       timeSource,
		MaxAttempts: cfg.RenderMaxAttempts,
	})
	renders := rendersvc.New(rendersvc.Options{
		Runner:      db,
		Series:      repos.Series,
		Shots:       repos.Shots,
		Prompts:     repos.Prompts,
		Renders:     repos.Renders,
		Quotas:      repos.Quotas,
		Audits:      recorder,
		Clock:       timeSource,
		LeaseTTL:    cfg.RenderLeaseTTL,
		BackoffBase: cfg.RenderBackoffBase,
	})
	teachingService := teachingsvc.New(teachingsvc.Options{
		Runner:   db,
		Series:   repos.Series,
		Shots:    repos.Shots,
		Prompts:  repos.Prompts,
		Teaching: repos.Teaching,
		Audits:   recorder,
		Clock:    timeSource,
	})

	application := &App{
		Config:        cfg,
		Logger:        logger,
		Clock:         timeSource,
		DB:            db,
		Repositories:  repos,
		schemaVersion: schemaVersion,
		Services: Services{
			Auth:       auth,
			Prompts:    prompts,
			Production: production,
			Render:     renders,
			Teaching:   teachingService,
		},
	}

	if err := application.bootstrap(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}

	server := httpapi.New(httpapi.Options{
		Auth:           auth,
		Prompts:        prompts,
		Production:     production,
		Teaching:       teachingService,
		Health:         db,
		Logger:         logger,
		Clock:          timeSource,
		DefaultStudio:  cfg.StudioSlug,
		RequestTimeout: cfg.RequestTimeout,
	})
	application.Handler = server.Handler()
	return application, nil
}

// SchemaVersion reports the applied migration version.
func (a *App) SchemaVersion() int { return a.schemaVersion }

func (a *App) bootstrap(ctx context.Context) error {
	directorPassword, generatedDirector, err := resolvePassword(a.Config.DirectorPassword)
	if err != nil {
		return err
	}
	apprenticePassword, generatedApprentice, err := resolvePassword(a.Config.ApprenticePassword)
	if err != nil {
		return err
	}
	result, err := a.Services.Auth.EnsureBootstrap(ctx, authsvc.EnsureBootstrapInput{
		StudioSlug:          a.Config.StudioSlug,
		StudioName:          a.Config.StudioName,
		DailyRenderCapacity: a.Config.DailyRenderCapacity,
		DirectorEmail:       a.Config.DirectorEmail,
		DirectorPassword:    directorPassword,
		ApprenticeEmail:     a.Config.ApprenticeEmail,
		ApprenticePassword:  apprenticePassword,
	})
	if err != nil {
		return err
	}
	if result.CreatedDirector && generatedDirector {
		a.Logger.Warn("generated a one-time director password; set MANJU_DIRECTOR_PASSWORD to control it",
			"email", a.Config.DirectorEmail, "password", directorPassword)
	}
	if result.CreatedTrainee && generatedApprentice {
		a.Logger.Warn("generated a one-time apprentice password; set MANJU_APPRENTICE_PASSWORD to control it",
			"email", a.Config.ApprenticeEmail, "password", apprenticePassword)
	}
	a.Logger.Info("studio ready",
		"studio_id", result.StudioID,
		"created_studio", result.CreatedStudio,
		"schema_version", a.schemaVersion)
	return nil
}

func resolvePassword(configured string) (string, bool, error) {
	if configured != "" {
		if err := security.ValidatePassword(configured); err != nil {
			return "", false, err
		}
		return configured, false, nil
	}
	generated, err := security.NewSessionToken()
	if err != nil {
		return "", false, err
	}
	return "mj" + generated[:20] + "42", true, nil
}

// StartWorkers launches the render workers and the sweeper.
func (a *App) StartWorkers(ctx context.Context, renderers int) {
	if renderers <= 0 {
		renderers = 1
	}
	for i := 0; i < renderers; i++ {
		instance := worker.NewRenderWorker(worker.RenderOptions{
			Name:     workerName(i),
			Renders:  a.Services.Render,
			Renderer: worker.StoryboardRenderer{},
			Logger:   a.Logger,
			Interval: a.Config.RenderPollInterval,
		})
		a.renderWorkers = append(a.renderWorkers, instance)
		a.workers.Add(1)
		go func() {
			defer a.workers.Done()
			instance.Run(ctx)
		}()
	}
	a.sweeper = worker.NewSweeper(worker.SweepOptions{
		Renders:  a.Services.Render,
		Teaching: a.Services.Teaching,
		Auth:     a.Services.Auth,
		Logger:   a.Logger,
		Interval: a.Config.WorkshopSweepEvery,
	})
	a.workers.Add(1)
	go func() {
		defer a.workers.Done()
		a.sweeper.Run(ctx)
	}()
}

// WaitForWorkers blocks until every background goroutine returned.
func (a *App) WaitForWorkers() { a.workers.Wait() }

// Close releases the database handle.
func (a *App) Close() error { return a.DB.Close() }

// Run serves HTTP until the context is cancelled, then drains connections and
// background workers within the configured grace period.
func (a *App) Run(ctx context.Context, renderers int) error {
	workerCtx, stopWorkers := context.WithCancel(context.WithoutCancel(ctx))
	defer stopWorkers()
	a.StartWorkers(workerCtx, renderers)

	server := &http.Server{
		Addr:              a.Config.HTTPAddr,
		Handler:           a.Handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errs := make(chan error, 1)
	go func() {
		a.Logger.Info("http listening", "addr", a.Config.HTTPAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- apperr.Wrap(err, apperr.CodeInternal, "http server stopped")
			return
		}
		errs <- nil
	}()

	select {
	case err := <-errs:
		stopWorkers()
		a.WaitForWorkers()
		return err
	case <-ctx.Done():
		a.Logger.Info("shutdown requested", "grace", a.Config.ShutdownGrace)
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.Config.ShutdownGrace)
	defer cancel()
	shutdownErr := server.Shutdown(shutdownCtx)
	stopWorkers()
	a.WaitForWorkers()
	if shutdownErr != nil {
		return apperr.Wrap(shutdownErr, apperr.CodeInternal, "graceful shutdown failed")
	}
	return <-errs
}

func workerName(index int) string {
	names := []string{"render-worker-a", "render-worker-b", "render-worker-c", "render-worker-d"}
	if index < len(names) {
		return names[index]
	}
	return "render-worker-extra"
}
