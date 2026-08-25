package apptest

import (
	"context"
	"net/http"
	"testing"

	"github.com/vance1852/manjuflow-studio/internal/repository"
)

// TestRetireBlockedByLiveReferenceLeavesAuditTrail proves that a retirement
// refused because the version is still referenced is still recorded in the audit
// trail together with its operator and its reference breakdown. The business
// transaction rolls back, so the rejection must be persisted in its own tx.
func TestRetireBlockedByLiveReferenceLeavesAuditTrail(t *testing.T) {
	h := newHarness(t)
	token := h.directorToken()
	// One bound shot keeps the active version referenced, so retirement is
	// refused on the live-reference precondition.
	board := h.seedStoryboard(token, 1)

	const requestID = "trace-retire-blocked-77"
	blocked := h.call(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/prompt-versions/" + itoa(board.VersionID) + "/retire",
		token:   token,
		headers: map[string]string{"X-Request-Id": requestID},
	})
	if blocked.Status != http.StatusUnprocessableEntity {
		t.Fatalf("retiring a referenced version returned %d: %s", blocked.Status, blocked.Body)
	}
	if blocked.errorCode() != "failed_precondition" {
		t.Fatalf("unexpected error code %q, want failed_precondition", blocked.errorCode())
	}

	studioID := shotStudioID(t, h)
	events, total, err := h.app.Repositories.Audits.List(context.Background(), h.app.DB.Reader(), studioID,
		repository.AuditFilter{Action: "prompt_version.retire_blocked"}, repository.Page{Limit: 5})
	if err != nil {
		t.Fatalf("cannot list audit events: %v", err)
	}
	if total == 0 || len(events) == 0 {
		t.Fatal("the refused retirement left no audit trail")
	}
	event := events[0]
	if event.Result != "rejected" {
		t.Fatalf("rejection event result is %q, want rejected", event.Result)
	}
	if event.ObjectType != "prompt_version" || event.ObjectID != board.VersionID {
		t.Fatalf("rejection event points at %#v, want prompt_version %d",
			event, board.VersionID)
	}
	if event.ActorID == 0 || event.ActorRole != "director" {
		t.Fatalf("rejection event lost the operator: %#v", event)
	}
	if event.RequestID != requestID {
		t.Fatalf("rejection event request id is %q, want %q", event.RequestID, requestID)
	}
	if event.Detail["bound_shots"] != "1" {
		t.Fatalf("rejection event bound_shots is %q, want 1: %#v",
			event.Detail["bound_shots"], event.Detail)
	}
	if _, ok := event.Detail["unfinished_jobs"]; !ok {
		t.Fatalf("rejection event lost the unfinished_jobs breakdown: %#v", event.Detail)
	}
	if _, ok := event.Detail["live_workshops"]; !ok {
		t.Fatalf("rejection event lost the live_workshops breakdown: %#v", event.Detail)
	}

	// Repeating the blocked attempt must leave one trail per refusal, not a
	// single swallowed record.
	second := h.call(requestSpec{
		method: http.MethodPost,
		path:   "/v1/prompt-versions/" + itoa(board.VersionID) + "/retire",
		token:  token,
	})
	if second.Status != http.StatusUnprocessableEntity {
		t.Fatalf("second retirement returned %d, want a refusal", second.Status)
	}
	events, total, err = h.app.Repositories.Audits.List(context.Background(), h.app.DB.Reader(), studioID,
		repository.AuditFilter{Action: "prompt_version.retire_blocked"}, repository.Page{Limit: 5})
	if err != nil {
		t.Fatalf("cannot re-list audit events: %v", err)
	}
	if total != 2 || len(events) != 2 {
		t.Fatalf("after two refusals the trail holds %d events (total %d), want 2",
			len(events), total)
	}

	// The version is unchanged: the refused path must not retire it. The bound
	// shot still references the version, proving retirement never applied.
	refs := h.mustCall(requestSpec{
		method: http.MethodGet,
		path:   "/v1/prompt-versions/" + itoa(board.VersionID) + "/references",
		token:  token,
	}, http.StatusOK)
	if int(refs.Decoded["bound_shots"].(float64)) != 1 {
		t.Fatalf("bound_shots is %v, want the reference intact after the refusal",
			refs.Decoded["bound_shots"])
	}
}

// TestRetireSucceedsAfterReferencesAreReleased proves the happy path still
// retires the version and records the success event, once no references remain.
func TestRetireSucceedsAfterReferencesAreReleased(t *testing.T) {
	h := newHarness(t)
	token := h.directorToken()
	board := h.seedStoryboard(token, 1)

	// Rebind the shot to a fresh active version so the original has no live
	// references and can be retired.
	second := h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/prompt-templates/" + itoa(board.TemplateID) + "/versions",
		token:  token,
		payload: map[string]any{
			"body":     PromptBody("夜市追逐的女主角", "赛博水墨", 4),
			"activate": true,
		},
	}, http.StatusCreated)
	h.mustCall(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/shots/" + itoa(board.ShotIDs[0]) + "/prompt",
		token:   token,
		payload: map[string]any{"prompt_version_id": second.int64Field("id")},
	}, http.StatusOK)

	retired := h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/prompt-versions/" + itoa(board.VersionID) + "/retire",
		token:  token,
	}, http.StatusOK)
	if retired.stringField("status") != "retired" {
		t.Fatalf("version status is %q, want retired", retired.stringField("status"))
	}

	studioID := shotStudioID(t, h)
	events, total, err := h.app.Repositories.Audits.List(context.Background(), h.app.DB.Reader(), studioID,
		repository.AuditFilter{Action: "prompt_version.retired"}, repository.Page{Limit: 5})
	if err != nil {
		t.Fatalf("cannot list audit events: %v", err)
	}
	if total == 0 || len(events) == 0 {
		t.Fatal("successful retirement left no audit trail")
	}
	if events[0].Result != "success" {
		t.Fatalf("success event result is %q, want success", events[0].Result)
	}
}
