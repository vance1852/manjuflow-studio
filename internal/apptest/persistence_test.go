package apptest

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
	"github.com/vance1852/manjuflow-studio/internal/domain/production"
	"github.com/vance1852/manjuflow-studio/internal/repository"
	"github.com/vance1852/manjuflow-studio/internal/storage/sqlitedb"
	"github.com/vance1852/manjuflow-studio/migrations"
)

func TestMigrationsAreIdempotentAndRecordEveryStep(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, sqlitedb.DefaultOptions(filepath.Join(dir, "schema.sqlite")))
	if err != nil {
		t.Fatalf("cannot open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	first, err := db.Migrate(ctx)
	if err != nil {
		t.Fatalf("first migration failed: %v", err)
	}
	latest, err := migrations.LatestVersion()
	if err != nil {
		t.Fatalf("cannot read embedded migrations: %v", err)
	}
	if first != latest {
		t.Fatalf("first migration reached version %d, want %d", first, latest)
	}

	second, err := db.Migrate(ctx)
	if err != nil {
		t.Fatalf("second migration failed: %v", err)
	}
	if second != first {
		t.Fatalf("repeated migration moved the version from %d to %d", first, second)
	}

	steps, err := db.AppliedSteps(ctx)
	if err != nil {
		t.Fatalf("cannot read the ledger: %v", err)
	}
	if len(steps) != latest {
		t.Fatalf("ledger holds %d rows, want %d", len(steps), latest)
	}
	for index, step := range steps {
		if step.Version != index+1 {
			t.Fatalf("ledger row %d records version %d", index, step.Version)
		}
		if step.Checksum == "" || step.AppliedAt.IsZero() {
			t.Fatalf("ledger row %d is incomplete: %#v", index, step)
		}
	}
}

func TestUnknownRecordedMigrationBlocksStartup(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	path := filepath.Join(dir, "diverged.sqlite")
	db, err := sqlitedb.Open(ctx, sqlitedb.DefaultOptions(path))
	if err != nil {
		t.Fatalf("cannot open database: %v", err)
	}
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatalf("initial migration failed: %v", err)
	}
	if _, err := db.Handle().ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (99, 'from_the_future', 'deadbeef', ?)`,
		time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("cannot insert the foreign ledger row: %v", err)
	}

	_, err = db.Migrate(ctx)
	if err == nil {
		t.Fatal("startup continued with an unknown migration recorded")
	}
	if !apperr.IsCode(err, apperr.CodeFailedPrecondition) {
		t.Fatalf("unknown migration reported %v, want a failed precondition", err)
	}
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("cannot close database: %v", closeErr)
	}
}

func TestStateSurvivesAProcessRestart(t *testing.T) {
	h := newHarness(t)
	token := h.directorToken()
	board := h.seedStoryboard(token, 2)

	renderAndApprove(t, h, token, board, 0, true)
	if reply := h.submitRender(token, board.ShotIDs[1], "restart-take"); reply.Status != http.StatusAccepted {
		t.Fatalf("submission returned %d", reply.Status)
	}
	quotaBefore := h.mustCall(requestSpec{method: http.MethodGet, path: "/v1/render-quota", token: token}, http.StatusOK)

	restarted := h.reopen()
	restartedToken := restarted.directorToken()

	detail := restarted.mustCall(requestSpec{
		method: http.MethodGet,
		path:   "/v1/series/" + itoa(board.SeriesID),
		token:  restartedToken,
	}, http.StatusOK)
	shots := detail.Decoded["shots"].([]any)
	if len(shots) != 2 {
		t.Fatalf("restarted instance sees %d shots, want 2", len(shots))
	}
	states := map[string]string{}
	for _, item := range shots {
		shot := item.(map[string]any)
		state, _ := shot["state"].(string)
		artifact, _ := shot["artifact_ref"].(string)
		states[state] = artifact
	}
	if _, ok := states["approved"]; !ok {
		t.Fatalf("approved shot was lost across the restart: %#v", states)
	}
	if _, ok := states["rendering"]; !ok {
		t.Fatalf("in-flight shot was lost across the restart: %#v", states)
	}

	quotaAfter := restarted.mustCall(requestSpec{method: http.MethodGet, path: "/v1/render-quota", token: restartedToken}, http.StatusOK)
	if quotaAfter.Decoded["used"] != quotaBefore.Decoded["used"] {
		t.Fatalf("quota ledger changed across the restart: %v then %v",
			quotaBefore.Decoded["used"], quotaAfter.Decoded["used"])
	}

	replay := restarted.submitRender(restartedToken, board.ShotIDs[1], "restart-take")
	if replay.Status != http.StatusAccepted || replay.Header.Get("Idempotent-Replay") != "true" {
		t.Fatalf("idempotency record did not survive the restart: status %d header %q",
			replay.Status, replay.Header.Get("Idempotent-Replay"))
	}

	if processed := restarted.processRenders(NewScriptedRenderer(Succeed("manju://after-restart/take")), 3); processed != 1 {
		t.Fatalf("restarted worker processed %d jobs, want the queued one", processed)
	}
}

func TestBootstrapDoesNotDuplicateTheStudioOnRestart(t *testing.T) {
	h := newHarness(t)
	first := h.mustCall(requestSpec{method: http.MethodGet, path: "/v1/auth/session", token: h.directorToken()}, http.StatusOK)
	firstUser := first.Decoded["user"].(map[string]any)

	restarted := h.reopen()
	second := restarted.mustCall(requestSpec{
		method: http.MethodGet,
		path:   "/v1/auth/session",
		token:  restarted.directorToken(),
	}, http.StatusOK)
	secondUser := second.Decoded["user"].(map[string]any)

	if firstUser["id"] != secondUser["id"] {
		t.Fatalf("bootstrap created a second director: %v then %v", firstUser["id"], secondUser["id"])
	}
}

func TestRepositoryResultsAreIsolatedFromCallerMutation(t *testing.T) {
	h := newHarness(t)
	token := h.directorToken()
	board := h.seedStoryboard(token, 2)

	ctx := context.Background()
	reader := h.app.DB.Reader()
	shots, err := h.app.Repositories.Shots.ListBySeries(ctx, reader, board.SeriesID)
	if err != nil {
		t.Fatalf("cannot list shots: %v", err)
	}
	if len(shots) != 2 {
		t.Fatalf("repository returned %d shots, want 2", len(shots))
	}
	if shots[0].PromptVersionID == nil {
		t.Fatal("bound shot lost its prompt version pointer")
	}

	original := *shots[0].PromptVersionID
	*shots[0].PromptVersionID = 999999
	shots[0].State = production.ShotApproved
	shots[0].Title = "被调用方改写的标题"

	reread, err := h.app.Repositories.Shots.ListBySeries(ctx, reader, board.SeriesID)
	if err != nil {
		t.Fatalf("cannot re-read shots: %v", err)
	}
	if reread[0].Title == "被调用方改写的标题" {
		t.Fatal("caller mutation leaked into stored shot titles")
	}
	if reread[0].State == production.ShotApproved {
		t.Fatal("caller mutation leaked into stored shot state")
	}
	if *reread[0].PromptVersionID != original {
		t.Fatalf("caller mutation leaked through the shared pointer: %d", *reread[0].PromptVersionID)
	}

	events, _, err := h.app.Repositories.Audits.List(ctx, reader, shotStudioID(t, h), repository.AuditFilter{
		ObjectType: "series",
	}, repository.Page{Limit: 10})
	if err != nil {
		t.Fatalf("cannot list audit events: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("no series audit event was recorded")
	}
	events[0].Detail["code"] = "被调用方改写的编码"
	rereadEvents, _, err := h.app.Repositories.Audits.List(ctx, reader, shotStudioID(t, h), repository.AuditFilter{
		ObjectType: "series",
	}, repository.Page{Limit: 10})
	if err != nil {
		t.Fatalf("cannot re-read audit events: %v", err)
	}
	if rereadEvents[0].Detail["code"] == "被调用方改写的编码" {
		t.Fatal("caller mutation leaked into the stored audit detail")
	}
}

func TestAuditTrailLinksActorObjectAndRequest(t *testing.T) {
	h := newHarness(t)
	token := h.directorToken()

	h.mustCall(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/series",
		token:   token,
		payload: map[string]string{"title": "审计剧集", "code_prefix": "MJ"},
		headers: map[string]string{"X-Request-Id": "trace-audit-77"},
	}, http.StatusCreated)

	ctx := context.Background()
	events, total, err := h.app.Repositories.Audits.List(ctx, h.app.DB.Reader(), shotStudioID(t, h), repository.AuditFilter{
		Action: "series.created",
	}, repository.Page{Limit: 10, SortBy: "created_at"})
	if err != nil {
		t.Fatalf("cannot list audit events: %v", err)
	}
	if total == 0 {
		t.Fatal("series creation produced no audit event")
	}
	found := false
	for _, event := range events {
		if event.RequestID == "trace-audit-77" {
			found = true
			if event.ActorID == 0 || event.ActorRole != "director" {
				t.Fatalf("audit event has no usable actor: %#v", event)
			}
			if event.ObjectType != "series" || event.ObjectID == 0 {
				t.Fatalf("audit event has no object binding: %#v", event)
			}
			if event.Detail["code"] == "" {
				t.Fatalf("audit event lost its business detail: %#v", event.Detail)
			}
		}
	}
	if !found {
		t.Fatal("audit trail does not carry the request identifier")
	}
}

func TestRejectedBusinessRuleIsAudited(t *testing.T) {
	h := newHarness(t)
	token := h.directorToken()
	board := h.seedStoryboard(token, 1)

	blocked := h.call(requestSpec{
		method: http.MethodPost,
		path:   "/v1/series/" + itoa(board.SeriesID) + "/publish",
		token:  token,
	})
	if blocked.Status != http.StatusUnprocessableEntity {
		t.Fatalf("publishing a draft series returned %d", blocked.Status)
	}

	events, _, err := h.app.Repositories.Audits.List(context.Background(), h.app.DB.Reader(), shotStudioID(t, h),
		repository.AuditFilter{Action: "series.publish_blocked"}, repository.Page{Limit: 5})
	if err != nil {
		t.Fatalf("cannot list audit events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("%d rejection events were recorded, want 1", len(events))
	}
	if events[0].Result != "rejected" {
		t.Fatalf("rejection event result is %q", events[0].Result)
	}
	if events[0].Detail["state"] != "draft" {
		t.Fatalf("rejection event detail is %#v", events[0].Detail)
	}
}

func TestDatabaseFileIsCreatedWithItsDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "data")
	ctx := context.Background()
	path := filepath.Join(dir, "manjuflow.sqlite")
	db, err := sqlitedb.Open(ctx, sqlitedb.DefaultOptions(path))
	if err != nil {
		t.Fatalf("cannot open database in a missing directory: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatalf("migration failed: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database file was not created: %v", err)
	}
	if err := db.Ping(ctx); err != nil {
		t.Fatalf("database is not reachable: %v", err)
	}
	version, err := db.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("cannot read schema version: %v", err)
	}
	if version == 0 {
		t.Fatal("schema version is zero after migrating")
	}
}

func TestCancelledContextIsReportedAsCancelledNotInternal(t *testing.T) {
	h := newHarness(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := h.app.DB.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		t.Fatal("transaction body ran with a cancelled context")
		return nil
	})
	if err == nil {
		t.Fatal("cancelled context produced no error")
	}
	if !apperr.IsCode(err, apperr.CodeCanceled) {
		t.Fatalf("cancelled context mapped to %v, want the cancelled code", apperr.CodeOf(err))
	}
}

// shotStudioID resolves the bootstrap studio identifier.
func shotStudioID(t *testing.T, h *harness) int64 {
	t.Helper()
	studio, err := h.app.Repositories.Studios.FindBySlug(context.Background(), h.app.DB.Reader(), studioSlug)
	if err != nil {
		t.Fatalf("cannot load studio: %v", err)
	}
	return studio.ID
}
