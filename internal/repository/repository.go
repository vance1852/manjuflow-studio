// Package repository declares the persistence contracts used by services. The
// concrete SQL implementation lives in internal/repository/sqliterepo, so no
// service ever builds a statement itself.
package repository

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
	"github.com/vance1852/manjuflow-studio/internal/domain/audit"
	"github.com/vance1852/manjuflow-studio/internal/domain/identity"
	"github.com/vance1852/manjuflow-studio/internal/domain/production"
	"github.com/vance1852/manjuflow-studio/internal/domain/prompt"
	"github.com/vance1852/manjuflow-studio/internal/domain/render"
	"github.com/vance1852/manjuflow-studio/internal/domain/teaching"
)

// Querier is the subset of database/sql shared by *sql.DB and *sql.Tx. Passing
// it explicitly keeps every repository call bound to the caller transaction.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Runner exposes the transaction boundary. InTx runs fn inside one immediate
// write transaction and rolls back on any error or panic.
type Runner interface {
	InTx(ctx context.Context, fn func(ctx context.Context, q Querier) error) error
	Reader() Querier
}

// Page describes a normalised pagination and ordering request.
type Page struct {
	Limit  int
	Offset int
	SortBy string
	Desc   bool
}

// DefaultLimit and MaxLimit bound list endpoints.
const (
	DefaultLimit = 20
	MaxLimit     = 100
)

// NormalisePage validates a caller supplied page against the allowed sort keys.
// allowed maps the public sort name to its SQL column.
func NormalisePage(page Page, allowed map[string]string, fallback string) (Page, error) {
	out := page
	if out.Limit == 0 {
		out.Limit = DefaultLimit
	}
	if out.Limit < 1 || out.Limit > MaxLimit {
		return Page{}, apperr.New(apperr.CodeInvalidArgument, "limit must be between 1 and %d", MaxLimit).
			With("field", "limit")
	}
	if out.Offset < 0 {
		return Page{}, apperr.New(apperr.CodeInvalidArgument, "offset must not be negative").With("field", "offset")
	}
	key := strings.TrimSpace(strings.ToLower(out.SortBy))
	if key == "" {
		key = fallback
	}
	if _, ok := allowed[key]; !ok {
		return Page{}, apperr.New(apperr.CodeInvalidArgument, "sort key %q is not supported", out.SortBy).
			With("field", "sort_by")
	}
	out.SortBy = key
	return out, nil
}

// Column resolves the SQL column for a normalised sort key.
func Column(allowed map[string]string, key string) string { return allowed[key] }

// SeriesFilter narrows a series listing.
type SeriesFilter struct {
	States      []production.SeriesState
	TitleLike   string
	CreatedFrom *time.Time
	CreatedTo   *time.Time
}

// CountPredicate returns the filter used when counting matches. Counting only
// needs the indexed state predicate, so the text and time range narrowing is
// left to the page query.
func (f SeriesFilter) CountPredicate() SeriesFilter {
	return SeriesFilter{States: f.States}
}

// WorkshopFilter narrows a workshop listing.
type WorkshopFilter struct {
	States   []teaching.WorkshopState
	SeriesID int64
	MentorID int64
}

// AuditFilter narrows an audit listing.
type AuditFilter struct {
	ObjectType string
	ObjectID   int64
	ActorID    int64
	Action     string
}

// Quota is the persisted daily render allowance of one studio.
type Quota struct {
	StudioID int64
	Day      string
	Capacity int
	Used     int
}

// Remaining reports the unused allowance.
func (q Quota) Remaining() int {
	if q.Used >= q.Capacity {
		return 0
	}
	return q.Capacity - q.Used
}

// IdempotencyState is the lifecycle of a stored idempotency record.
type IdempotencyState string

const (
	// IdempotencyInProgress marks a first request that has not finished.
	IdempotencyInProgress IdempotencyState = "in_progress"
	// IdempotencyCompleted stores the replayable response.
	IdempotencyCompleted IdempotencyState = "completed"
	// IdempotencyFailed lets the caller retry with the same key.
	IdempotencyFailed IdempotencyState = "failed"
)

// IdempotencyRecord is the persisted first-writer-wins marker for one mutating
// request.
type IdempotencyRecord struct {
	ID          int64
	StudioID    int64
	Method      string
	Path        string
	Key         string
	Fingerprint string
	State       IdempotencyState
	StatusCode  int
	Response    string
	CreatedAt   time.Time
	CompletedAt *time.Time
}

// StudioRepository stores the tenant row.
type StudioRepository interface {
	Create(ctx context.Context, q Querier, slug, name string, dailyRenderCapacity int, now time.Time) (int64, error)
	FindBySlug(ctx context.Context, q Querier, slug string) (Studio, error)
	FindByID(ctx context.Context, q Querier, id int64) (Studio, error)
}

// Studio is the tenant record.
type Studio struct {
	ID                  int64
	Slug                string
	Name                string
	DailyRenderCapacity int
	CreatedAt           time.Time
}

// UserRepository stores studio members.
type UserRepository interface {
	Create(ctx context.Context, q Querier, user identity.User) (int64, error)
	FindByEmail(ctx context.Context, q Querier, studioID int64, email string) (identity.User, error)
	FindByID(ctx context.Context, q Querier, id int64) (identity.User, error)
	CountByRole(ctx context.Context, q Querier, studioID int64, role identity.Role) (int, error)
}

// SessionRepository stores revocable sessions.
type SessionRepository interface {
	Create(ctx context.Context, q Querier, session identity.Session) (int64, error)
	FindByTokenHash(ctx context.Context, q Querier, tokenHash string) (identity.Session, error)
	Revoke(ctx context.Context, q Querier, sessionID int64, at time.Time) error
	RevokeAllForUser(ctx context.Context, q Querier, userID int64, at time.Time) (int, error)
	TouchLastSeen(ctx context.Context, q Querier, sessionID int64, at time.Time) error
	DeleteExpired(ctx context.Context, q Querier, before time.Time) (int, error)
}

// PromptRepository stores prompt templates and their immutable versions.
type PromptRepository interface {
	CreateTemplate(ctx context.Context, q Querier, template prompt.Template) (int64, error)
	FindTemplateByID(ctx context.Context, q Querier, id int64) (prompt.Template, error)
	FindTemplateBySlug(ctx context.Context, q Querier, studioID int64, slug string) (prompt.Template, error)
	CreateVersion(ctx context.Context, q Querier, version prompt.Version) (int64, error)
	FindVersionByID(ctx context.Context, q Querier, id int64) (prompt.Version, error)
	FindVersion(ctx context.Context, q Querier, templateID int64, number int) (prompt.Version, error)
	SaveVersionStatus(ctx context.Context, q Querier, version prompt.Version) error
	AdvanceTemplateHead(ctx context.Context, q Querier, templateID int64, head int, now time.Time) error
	ListVersions(ctx context.Context, q Querier, templateID int64, page Page) ([]prompt.Version, int, error)
}

// SeriesRepository stores production series with optimistic locking.
type SeriesRepository interface {
	Create(ctx context.Context, q Querier, series production.Series) (int64, error)
	FindByID(ctx context.Context, q Querier, id int64) (production.Series, error)
	FindByCode(ctx context.Context, q Querier, studioID int64, code string) (production.Series, error)
	Save(ctx context.Context, q Querier, series production.Series) error
	List(ctx context.Context, q Querier, studioID int64, filter SeriesFilter, page Page) ([]production.Series, int, error)
}

// ShotRepository stores storyboard shots with optimistic locking.
type ShotRepository interface {
	Create(ctx context.Context, q Querier, shot production.Shot) (int64, error)
	FindByID(ctx context.Context, q Querier, id int64) (production.Shot, error)
	FindByOrdinal(ctx context.Context, q Querier, seriesID int64, ordinal int) (production.Shot, error)
	Save(ctx context.Context, q Querier, shot production.Shot) error
	ListBySeries(ctx context.Context, q Querier, seriesID int64) ([]production.Shot, error)
	CountReferencingPromptVersion(ctx context.Context, q Querier, promptVersionID int64) (int, error)
}

// RenderRepository stores render jobs and their leases.
type RenderRepository interface {
	CreateJob(ctx context.Context, q Querier, job render.Job) (int64, error)
	FindJobByID(ctx context.Context, q Querier, id int64) (render.Job, error)
	FindActiveJobForShot(ctx context.Context, q Querier, shotID int64) (render.Job, error)
	Save(ctx context.Context, q Querier, job render.Job, expectedGeneration int) error
	ClaimDueJob(ctx context.Context, q Querier, owner string, now time.Time, leaseTTL time.Duration) (render.Job, error)
	RequeueDue(ctx context.Context, q Querier, now time.Time, limit int) (int, error)
	ListByShot(ctx context.Context, q Querier, shotID int64) ([]render.Job, error)
	CountUnfinishedForPromptVersion(ctx context.Context, q Querier, promptVersionID int64) (int, error)
}

// QuotaRepository stores the daily render allowance ledger.
type QuotaRepository interface {
	Ensure(ctx context.Context, q Querier, studioID int64, day string, capacity int) error
	TryConsume(ctx context.Context, q Querier, studioID int64, day string) (bool, error)
	Release(ctx context.Context, q Querier, studioID int64, day string) error
	Get(ctx context.Context, q Querier, studioID int64, day string) (Quota, error)
}

// TeachingRepository stores workshops, enrolments and practice submissions.
type TeachingRepository interface {
	CreateWorkshop(ctx context.Context, q Querier, workshop teaching.Workshop) (int64, error)
	FindWorkshopByID(ctx context.Context, q Querier, id int64) (teaching.Workshop, error)
	SaveWorkshop(ctx context.Context, q Querier, workshop teaching.Workshop) error
	ReserveSeat(ctx context.Context, q Querier, workshopID int64, now time.Time) (bool, error)
	ListWorkshops(ctx context.Context, q Querier, studioID int64, filter WorkshopFilter, page Page) ([]teaching.Workshop, int, error)
	CountLiveWorkshopsForPromptVersion(ctx context.Context, q Querier, promptVersionID int64) (int, error)
	CountLiveWorkshopsForShot(ctx context.Context, q Querier, shotID int64) (int, error)

	ListOverdueWorkshops(ctx context.Context, q Querier, now time.Time, limit int) ([]teaching.Workshop, error)

	CreateEnrollment(ctx context.Context, q Querier, enrollment teaching.Enrollment) (int64, error)
	FindEnrollment(ctx context.Context, q Querier, workshopID, apprenticeID int64) (teaching.Enrollment, error)
	FindEnrollmentByID(ctx context.Context, q Querier, id int64) (teaching.Enrollment, error)
	SaveEnrollment(ctx context.Context, q Querier, enrollment teaching.Enrollment) error

	CreateSubmission(ctx context.Context, q Querier, submission teaching.Submission) (int64, error)
	FindSubmissionByID(ctx context.Context, q Querier, id int64) (teaching.Submission, error)
	SaveSubmission(ctx context.Context, q Querier, submission teaching.Submission) error
	ListSubmissions(ctx context.Context, q Querier, workshopID int64, page Page) ([]teaching.Submission, int, error)
	CountPendingSubmissions(ctx context.Context, q Querier, workshopID int64) (int, error)
}

// AuditRepository appends and reads the audit trail.
type AuditRepository interface {
	Append(ctx context.Context, q Querier, event audit.Event) (int64, error)
	List(ctx context.Context, q Querier, studioID int64, filter AuditFilter, page Page) ([]audit.Event, int, error)
}

// IdempotencyRepository stores first-writer-wins request markers.
type IdempotencyRepository interface {
	Begin(ctx context.Context, q Querier, record IdempotencyRecord) (IdempotencyRecord, bool, error)
	Complete(ctx context.Context, q Querier, id int64, statusCode int, response string, at time.Time) error
	Fail(ctx context.Context, q Querier, id int64, at time.Time) error
	Reopen(ctx context.Context, q Querier, id int64, fingerprint string, at time.Time) error
	Get(ctx context.Context, q Querier, studioID int64, method, path, key string) (IdempotencyRecord, error)
}

// SequenceRepository issues gap free business numbers.
type SequenceRepository interface {
	Next(ctx context.Context, q Querier, studioID int64, name string) (int64, error)
}

// NotFound builds the canonical not-found error for a repository lookup.
func NotFound(entity string, key any) error {
	return apperr.New(apperr.CodeNotFound, "%s not found", entity).With("entity", entity).
		With("key", strings.TrimSpace(sprint(key)))
}

func sprint(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	default:
		return "unspecified"
	}
}
