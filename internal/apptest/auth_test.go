package apptest

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestLoginIssuesUsableSessionAndEchoesRequestID(t *testing.T) {
	h := newHarness(t)

	reply := h.mustCall(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/auth/login",
		payload: map[string]string{"studio": studioSlug, "email": directorEmail, "password": directorPassword},
		headers: map[string]string{"X-Request-Id": "trace-login-01"},
	}, http.StatusOK)

	if got := reply.Header.Get("X-Request-Id"); got != "trace-login-01" {
		t.Fatalf("request id was not echoed, got %q", got)
	}
	token := reply.stringField("token")
	if token == "" {
		t.Fatal("login returned an empty token")
	}
	if _, exists := reply.Decoded["password_hash"]; exists {
		t.Fatal("login response leaked the password digest")
	}

	session := h.mustCall(requestSpec{method: http.MethodGet, path: "/v1/auth/session", token: token}, http.StatusOK)
	user, ok := session.Decoded["user"].(map[string]any)
	if !ok {
		t.Fatalf("session response is malformed: %s", session.Body)
	}
	if user["role"] != "director" {
		t.Fatalf("session reports role %v, want director", user["role"])
	}
	capabilities, ok := session.Decoded["capabilities"].([]any)
	if !ok || len(capabilities) == 0 {
		t.Fatalf("session response carries no capabilities: %s", session.Body)
	}
}

func TestLoginRejectsWrongPasswordWithoutRevealingTheAccount(t *testing.T) {
	h := newHarness(t)

	wrongPassword := h.call(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/auth/login",
		payload: map[string]string{"studio": studioSlug, "email": directorEmail, "password": "not-the-password-1"},
	})
	unknownAccount := h.call(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/auth/login",
		payload: map[string]string{"studio": studioSlug, "email": "ghost@manjuflow.test", "password": directorPassword},
	})

	if wrongPassword.Status != http.StatusUnauthorized || unknownAccount.Status != http.StatusUnauthorized {
		t.Fatalf("expected 401 for both attempts, got %d and %d", wrongPassword.Status, unknownAccount.Status)
	}
	if wrongPassword.errorCode() != "unauthenticated" || unknownAccount.errorCode() != "unauthenticated" {
		t.Fatalf("expected unauthenticated codes, got %q and %q", wrongPassword.errorCode(), unknownAccount.errorCode())
	}
	if wrongPassword.requestID() == "" {
		t.Fatal("error envelope carries no request id")
	}
}

func TestLogoutRevokesTheSessionImmediately(t *testing.T) {
	h := newHarness(t)
	token := h.directorToken()

	h.mustCall(requestSpec{method: http.MethodPost, path: "/v1/auth/logout", token: token}, http.StatusOK)

	afterLogout := h.call(requestSpec{method: http.MethodGet, path: "/v1/auth/session", token: token})
	if afterLogout.Status != http.StatusUnauthorized {
		t.Fatalf("revoked session still works, got %d", afterLogout.Status)
	}
	repeated := h.call(requestSpec{method: http.MethodPost, path: "/v1/auth/logout", token: token})
	if repeated.Status != http.StatusUnauthorized {
		t.Fatalf("second logout returned %d, want 401", repeated.Status)
	}
}

func TestExpiredSessionIsRejectedAndPrunedBySweep(t *testing.T) {
	h := newHarness(t)
	token := h.directorToken()

	h.clock.Advance(h.cfg.SessionTTL + time.Minute)

	expired := h.call(requestSpec{method: http.MethodGet, path: "/v1/auth/session", token: token})
	if expired.Status != http.StatusUnauthorized {
		t.Fatalf("expired session returned %d, want 401", expired.Status)
	}
	details, _ := expired.Decoded["error"].(map[string]any)
	detail, _ := details["details"].(map[string]any)
	if detail["reason"] != "expired" {
		t.Fatalf("expected an expired reason, got %#v", detail)
	}

	pruned, err := h.app.Services.Auth.PruneExpiredSessions(context.Background())
	if err != nil {
		t.Fatalf("cannot prune sessions: %v", err)
	}
	if pruned == 0 {
		t.Fatal("sweep pruned no expired session")
	}
}

func TestApprenticeCannotDriveTheProductionPipeline(t *testing.T) {
	h := newHarness(t)
	apprentice := h.apprenticeToken()

	attempt := h.call(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/series",
		token:   apprentice,
		payload: map[string]string{"title": "越权剧集", "code_prefix": "XX"},
	})
	if attempt.Status != http.StatusForbidden {
		t.Fatalf("apprentice created a series, status %d", attempt.Status)
	}
	if attempt.errorCode() != "permission_denied" {
		t.Fatalf("expected permission_denied, got %q", attempt.errorCode())
	}

	promptAttempt := h.call(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/prompt-templates",
		token:   apprentice,
		payload: map[string]string{"slug": "sneaky", "title": "越权模板"},
	})
	if promptAttempt.Status != http.StatusForbidden {
		t.Fatalf("apprentice created a prompt template, status %d", promptAttempt.Status)
	}
}

func TestDirectorAddsApprenticeAndTheNewMemberCanSignIn(t *testing.T) {
	h := newHarness(t)
	token := h.directorToken()

	created := h.mustCall(requestSpec{
		method: http.MethodPost,
		path:   "/v1/members",
		token:  token,
		payload: map[string]string{
			"email":        "second-hand@manjuflow.test",
			"display_name": "副手",
			"password":     "second-hand-2026",
			"role":         "apprentice",
		},
	}, http.StatusCreated)
	if created.stringField("role") != "apprentice" {
		t.Fatalf("created member has role %q", created.stringField("role"))
	}

	duplicate := h.call(requestSpec{
		method: http.MethodPost,
		path:   "/v1/members",
		token:  token,
		payload: map[string]string{
			"email":        "second-hand@manjuflow.test",
			"display_name": "副手",
			"password":     "second-hand-2026",
			"role":         "apprentice",
		},
	})
	if duplicate.Status != http.StatusConflict {
		t.Fatalf("duplicate member returned %d, want 409", duplicate.Status)
	}

	newToken := h.login("second-hand@manjuflow.test", "second-hand-2026")
	if newToken == "" {
		t.Fatal("new member cannot sign in")
	}
}

func TestWeakMemberPasswordIsRejected(t *testing.T) {
	h := newHarness(t)
	token := h.directorToken()

	reply := h.call(requestSpec{
		method: http.MethodPost,
		path:   "/v1/members",
		token:  token,
		payload: map[string]string{
			"email":        "weak@manjuflow.test",
			"display_name": "弱口令",
			"password":     "short1",
			"role":         "apprentice",
		},
	})
	if reply.Status != http.StatusBadRequest {
		t.Fatalf("weak password accepted with status %d", reply.Status)
	}
	if reply.errorCode() != "invalid_argument" {
		t.Fatalf("expected invalid_argument, got %q", reply.errorCode())
	}
}

func TestUnauthenticatedAndMalformedRequestsUseTheSharedEnvelope(t *testing.T) {
	h := newHarness(t)

	missingHeader := h.call(requestSpec{method: http.MethodGet, path: "/v1/series"})
	if missingHeader.Status != http.StatusUnauthorized {
		t.Fatalf("missing credentials returned %d", missingHeader.Status)
	}
	if missingHeader.errorCode() != "unauthenticated" {
		t.Fatalf("unexpected code %q", missingHeader.errorCode())
	}

	badScheme := h.call(requestSpec{
		method:  http.MethodGet,
		path:    "/v1/series",
		headers: map[string]string{"Authorization": "Basic abc"},
	})
	if badScheme.Status != http.StatusUnauthorized {
		t.Fatalf("basic auth returned %d", badScheme.Status)
	}

	token := h.directorToken()
	unknownField := h.call(requestSpec{
		method:  http.MethodPost,
		path:    "/v1/series",
		token:   token,
		payload: map[string]any{"title": "带未知字段", "unexpected": true},
	})
	if unknownField.Status != http.StatusBadRequest {
		t.Fatalf("unknown field accepted with status %d", unknownField.Status)
	}

	unknownRoute := h.call(requestSpec{method: http.MethodGet, path: "/v1/nothing-here", token: token})
	if unknownRoute.Status != http.StatusNotFound {
		t.Fatalf("unknown route returned %d", unknownRoute.Status)
	}
}

func TestHealthAndReadinessAreOpenAndReportSchema(t *testing.T) {
	h := newHarness(t)

	live := h.mustCall(requestSpec{method: http.MethodGet, path: "/healthz"}, http.StatusOK)
	if live.stringField("status") != "alive" {
		t.Fatalf("liveness reported %q", live.stringField("status"))
	}
	ready := h.mustCall(requestSpec{method: http.MethodGet, path: "/readyz"}, http.StatusOK)
	if ready.stringField("status") != "ready" {
		t.Fatalf("readiness reported %q", ready.stringField("status"))
	}
	if int(ready.Decoded["schema_version"].(float64)) != h.app.SchemaVersion() {
		t.Fatalf("readiness schema version %v does not match %d",
			ready.Decoded["schema_version"], h.app.SchemaVersion())
	}
}
