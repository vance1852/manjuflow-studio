package identity

import (
	"strings"
	"testing"
	"time"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
)

func instant() time.Time { return time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC) }

func TestRoleCapabilitiesSeparateDirectorAndApprentice(t *testing.T) {
	directorOnly := []Capability{
		CapManagePromptAssets, CapManageSeries, CapBindPromptVersion,
		CapSubmitRender, CapReviewShot, CapPublishSeries, CapOpenWorkshop, CapGradePractice,
	}
	for _, capability := range directorOnly {
		if !RoleDirector.Allows(capability) {
			t.Fatalf("the director cannot %s", capability)
		}
		if RoleApprentice.Allows(capability) {
			t.Fatalf("the apprentice can %s", capability)
		}
	}
	apprenticeOnly := []Capability{CapEnrollWorkshop, CapSubmitPractice}
	for _, capability := range apprenticeOnly {
		if !RoleApprentice.Allows(capability) {
			t.Fatalf("the apprentice cannot %s", capability)
		}
		if RoleDirector.Allows(capability) {
			t.Fatalf("the director can %s", capability)
		}
	}
	if !RoleDirector.Allows(CapViewCatalog) || !RoleApprentice.Allows(CapViewCatalog) {
		t.Fatal("catalogue access is not shared")
	}
}

func TestCapabilitiesSnapshotIsIsolated(t *testing.T) {
	first := RoleDirector.Capabilities()
	if len(first) == 0 {
		t.Fatal("the director has no capability")
	}
	first[0] = "tampered"
	second := RoleDirector.Capabilities()
	for _, capability := range second {
		if capability == "tampered" {
			t.Fatal("the capability snapshot is shared")
		}
	}
}

func TestRoleParsing(t *testing.T) {
	if role, err := ParseRole(" Director "); err != nil || role != RoleDirector {
		t.Fatalf("director parsed as %q with %v", role, err)
	}
	if role, err := ParseRole("APPRENTICE"); err != nil || role != RoleApprentice {
		t.Fatalf("apprentice parsed as %q with %v", role, err)
	}
	if _, err := ParseRole("producer"); !apperr.IsCode(err, apperr.CodeInvalidArgument) {
		t.Fatalf("unknown role reported %v", apperr.CodeOf(err))
	}
}

func TestEmailNormalisation(t *testing.T) {
	email, err := NormaliseEmail("  Director@Manjuflow.TEST ")
	if err != nil {
		t.Fatalf("a valid address was refused: %v", err)
	}
	if email != "director@manjuflow.test" {
		t.Fatalf("address normalised to %q", email)
	}
	for _, candidate := range []string{"", "no-at-sign", "@leading", "trailing@", strings.Repeat("a", 250) + "@example.test"} {
		if _, err := NormaliseEmail(candidate); err == nil {
			t.Fatalf("address %q was accepted", candidate)
		}
	}
}

func TestSuspendedAccountCannotAct(t *testing.T) {
	user := &User{Email: "director@manjuflow.test", Role: RoleDirector, Status: StatusSuspended}
	if err := user.EnsureActive(); !apperr.IsCode(err, apperr.CodePermissionDenied) {
		t.Fatalf("suspended account reported %v", apperr.CodeOf(err))
	}
	if err := user.Authorize(CapManageSeries); !apperr.IsCode(err, apperr.CodePermissionDenied) {
		t.Fatalf("suspended authorisation reported %v", apperr.CodeOf(err))
	}
	user.Status = StatusActive
	if err := user.Authorize(CapManageSeries); err != nil {
		t.Fatalf("an active director was refused: %v", err)
	}
	if err := user.Authorize(CapSubmitPractice); !apperr.IsCode(err, apperr.CodePermissionDenied) {
		t.Fatalf("director practice submission reported %v", apperr.CodeOf(err))
	}
}

func TestSessionUsabilityCoversRevocationAndExpiry(t *testing.T) {
	session := &Session{IssuedAt: instant(), ExpiresAt: instant().Add(time.Hour)}
	if err := session.EnsureUsable(instant()); err != nil {
		t.Fatalf("a fresh session was refused: %v", err)
	}
	if err := session.EnsureUsable(session.ExpiresAt); !apperr.IsCode(err, apperr.CodeUnauthenticated) {
		t.Fatalf("session at its expiry reported %v", apperr.CodeOf(err))
	}
	typed, ok := apperr.As(session.EnsureUsable(session.ExpiresAt))
	if !ok || typed.Details["reason"] != "expired" {
		t.Fatalf("expiry refusal does not report the reason: %#v", typed)
	}

	revoked := instant().Add(time.Minute)
	session.RevokedAt = &revoked
	err := session.EnsureUsable(instant().Add(2 * time.Minute))
	if !apperr.IsCode(err, apperr.CodeUnauthenticated) {
		t.Fatalf("revoked session reported %v", apperr.CodeOf(err))
	}
	typed, ok = apperr.As(err)
	if !ok || typed.Details["reason"] != "revoked" {
		t.Fatalf("revocation refusal does not report the reason: %#v", typed)
	}
}

func TestPrincipalAuthorisationMirrorsTheRole(t *testing.T) {
	director := Principal{Role: RoleDirector}
	if err := director.Authorize(CapPublishSeries); err != nil {
		t.Fatalf("director publication was refused: %v", err)
	}
	apprentice := Principal{Role: RoleApprentice}
	err := apprentice.Authorize(CapPublishSeries)
	if !apperr.IsCode(err, apperr.CodePermissionDenied) {
		t.Fatalf("apprentice publication reported %v", apperr.CodeOf(err))
	}
	typed, ok := apperr.As(err)
	if !ok || typed.Details["capability"] != string(CapPublishSeries) {
		t.Fatalf("refusal does not name the capability: %#v", typed)
	}
}
