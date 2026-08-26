package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/vance1852/manjuflow-studio/internal/clock"
	"github.com/vance1852/manjuflow-studio/internal/logging"
	"github.com/vance1852/manjuflow-studio/internal/middleware"
	"github.com/vance1852/manjuflow-studio/internal/service/authsvc"
	"github.com/vance1852/manjuflow-studio/internal/service/productionsvc"
	"github.com/vance1852/manjuflow-studio/internal/service/promptsvc"
	"github.com/vance1852/manjuflow-studio/internal/service/teachingsvc"
)

// HealthChecker verifies the dependencies used by the readiness probe.
type HealthChecker interface {
	Ping(ctx context.Context) error
	SchemaVersion(ctx context.Context) (int, error)
}

// Server owns the routing table and the handler dependencies.
type Server struct {
	auth          *authsvc.Service
	prompts       *promptsvc.Service
	production    *productionsvc.Service
	teaching      *teachingsvc.Service
	health        HealthChecker
	logger        *logging.Logger
	clock         clock.Clock
	defaultStudio string
	timeout       time.Duration
}

// Options configures the server.
type Options struct {
	Auth           *authsvc.Service
	Prompts        *promptsvc.Service
	Production     *productionsvc.Service
	Teaching       *teachingsvc.Service
	Health         HealthChecker
	Logger         *logging.Logger
	Clock          clock.Clock
	DefaultStudio  string
	RequestTimeout time.Duration
}

// New builds the HTTP server.
func New(opts Options) *Server {
	return &Server{
		auth:          opts.Auth,
		prompts:       opts.Prompts,
		production:    opts.Production,
		teaching:      opts.Teaching,
		health:        opts.Health,
		logger:        opts.Logger,
		clock:         opts.Clock,
		defaultStudio: opts.DefaultStudio,
		timeout:       opts.RequestTimeout,
	}
}

func (s *Server) now() time.Time { return s.clock.Now() }

// Handler assembles the routing table and the middleware chain.
func (s *Server) Handler() http.Handler {
	public := http.NewServeMux()
	public.HandleFunc("GET /healthz", s.handleLive)
	public.HandleFunc("GET /readyz", s.handleReady)
	public.HandleFunc("POST /v1/auth/login", s.handleLogin)

	private := http.NewServeMux()
	private.HandleFunc("POST /v1/auth/logout", s.handleLogout)
	private.HandleFunc("GET /v1/auth/session", s.handleSession)
	private.HandleFunc("POST /v1/members", s.handleCreateMember)

	private.HandleFunc("POST /v1/prompt-templates", s.handleCreateTemplate)
	private.HandleFunc("POST /v1/prompt-templates/{templateID}/versions", s.handleAppendVersion)
	private.HandleFunc("GET /v1/prompt-templates/{templateID}/versions", s.handleListVersions)
	private.HandleFunc("POST /v1/prompt-versions/{versionID}/activate", s.handleActivateVersion)
	private.HandleFunc("POST /v1/prompt-versions/{versionID}/retire", s.handleRetireVersion)
	private.HandleFunc("GET /v1/prompt-versions/{versionID}/references", s.handleVersionReferences)

	private.HandleFunc("POST /v1/series", s.handleCreateSeries)
	private.HandleFunc("GET /v1/series", s.handleListSeries)
	private.HandleFunc("GET /v1/series/{seriesID}", s.handleGetSeries)
	private.HandleFunc("POST /v1/series/{seriesID}/shots", s.handlePlanShots)
	private.HandleFunc("POST /v1/series/{seriesID}/publish", s.handlePublishSeries)

	private.HandleFunc("POST /v1/shots/{shotID}/prompt", s.handleBindPrompt)
	private.HandleFunc("POST /v1/shots/{shotID}/render", s.handleSubmitRender)
	private.HandleFunc("GET /v1/shots/{shotID}/render-jobs", s.handleListRenderJobs)
	private.HandleFunc("POST /v1/shots/{shotID}/review", s.handleReviewShot)
	private.HandleFunc("GET /v1/render-quota", s.handleQuota)

	private.HandleFunc("POST /v1/workshops", s.handleOpenWorkshop)
	private.HandleFunc("GET /v1/workshops", s.handleListWorkshops)
	private.HandleFunc("POST /v1/workshops/{workshopID}/enrollments", s.handleEnroll)
	private.HandleFunc("POST /v1/workshops/{workshopID}/submissions", s.handleSubmitPractice)
	private.HandleFunc("GET /v1/workshops/{workshopID}/submissions", s.handleListSubmissions)
	private.HandleFunc("POST /v1/workshops/{workshopID}/grading", s.handleStartGrading)
	private.HandleFunc("POST /v1/workshops/{workshopID}/close", s.handleCloseWorkshop)
	private.HandleFunc("POST /v1/practice-submissions/{submissionID}/review", s.handleReviewPractice)

	guarded := middleware.Chain(private, middleware.Authenticated(s.auth, WriteError))

	root := http.NewServeMux()
	root.Handle("/v1/", guarded)
	root.Handle("/healthz", public)
	root.Handle("/readyz", public)
	root.Handle("/v1/auth/login", public)
	root.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { notFound(w, r) })

	return middleware.Chain(root,
		middleware.RequestID,
		middleware.AccessLog(s.logger),
		middleware.Recover(s.logger, WriteError),
		middleware.Timeout(s.timeout),
	)
}

func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "alive"})
}

type readyResponse struct {
	Status        string `json:"status"`
	SchemaVersion int    `json:"schema_version"`
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.health.Ping(ctx); err != nil {
		WriteError(w, r, err)
		return
	}
	version, err := s.health.SchemaVersion(ctx)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, readyResponse{Status: "ready", SchemaVersion: version})
}
