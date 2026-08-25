// Package identity models studio members, their capabilities and revocable
// server side sessions.
package identity

import (
	"strings"
	"time"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
)

// Role is a business role inside one studio.
type Role string

const (
	// RoleDirector is the solo director who owns prompt assets, drives the
	// production pipeline and grades apprentice practice.
	RoleDirector Role = "director"
	// RoleApprentice is a学员 who can read published material, enrol in a
	// workshop and submit practice for review.
	RoleApprentice Role = "apprentice"
)

// Capability is a fine grained permission checked by services.
type Capability string

const (
	CapManagePromptAssets Capability = "prompt.manage"
	CapManageSeries       Capability = "series.manage"
	CapBindPromptVersion  Capability = "shot.bind_prompt"
	CapSubmitRender       Capability = "render.submit"
	CapReviewShot         Capability = "shot.review"
	CapPublishSeries      Capability = "series.publish"
	CapOpenWorkshop       Capability = "workshop.open"
	CapGradePractice      Capability = "practice.grade"
	CapEnrollWorkshop     Capability = "workshop.enroll"
	CapSubmitPractice     Capability = "practice.submit"
	CapViewCatalog        Capability = "catalog.view"
)

var roleCapabilities = map[Role]map[Capability]bool{
	RoleDirector: {
		CapManagePromptAssets: true,
		CapManageSeries:       true,
		CapBindPromptVersion:  true,
		CapSubmitRender:       true,
		CapReviewShot:         true,
		CapPublishSeries:      true,
		CapOpenWorkshop:       true,
		CapGradePractice:      true,
		CapViewCatalog:        true,
	},
	RoleApprentice: {
		CapEnrollWorkshop: true,
		CapSubmitPractice: true,
		CapViewCatalog:    true,
	},
}

// ParseRole validates external role text.
func ParseRole(raw string) (Role, error) {
	switch Role(strings.ToLower(strings.TrimSpace(raw))) {
	case RoleDirector:
		return RoleDirector, nil
	case RoleApprentice:
		return RoleApprentice, nil
	default:
		return "", apperr.New(apperr.CodeInvalidArgument, "unknown role %q", raw).With("field", "role")
	}
}

// Allows reports whether the role carries the capability.
func (r Role) Allows(capability Capability) bool {
	return roleCapabilities[r][capability]
}

// Capabilities returns an isolated snapshot of the role capabilities.
func (r Role) Capabilities() []Capability {
	granted := roleCapabilities[r]
	out := make([]Capability, 0, len(granted))
	for capability := range granted {
		out = append(out, capability)
	}
	return out
}

// Status is the account lifecycle state.
type Status string

const (
	StatusActive    Status = "active"
	StatusSuspended Status = "suspended"
)

// User is a studio member.
type User struct {
	ID           int64
	StudioID     int64
	Email        string
	DisplayName  string
	PasswordHash string
	Role         Role
	Status       Status
	CreatedAt    time.Time
}

// NormaliseEmail lower cases and trims an address.
func NormaliseEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	if email == "" || !strings.Contains(email, "@") || strings.HasPrefix(email, "@") || strings.HasSuffix(email, "@") {
		return "", apperr.New(apperr.CodeInvalidArgument, "email must be a valid address").With("field", "email")
	}
	if len(email) > 254 {
		return "", apperr.New(apperr.CodeInvalidArgument, "email is longer than 254 characters").With("field", "email")
	}
	return email, nil
}

// EnsureActive rejects suspended accounts.
func (u *User) EnsureActive() error {
	if u.Status != StatusActive {
		return apperr.New(apperr.CodePermissionDenied, "account is %s", u.Status).With("user_id", u.Email)
	}
	return nil
}

// Authorize checks both account status and role capability.
func (u *User) Authorize(capability Capability) error {
	if err := u.EnsureActive(); err != nil {
		return err
	}
	if !u.Role.Allows(capability) {
		return apperr.New(apperr.CodePermissionDenied, "role %s cannot perform %s", u.Role, capability).
			With("capability", string(capability)).
			With("role", string(u.Role))
	}
	return nil
}

// Session is a revocable server side session. Only the token digest is stored;
// the opaque bearer token is returned to the client exactly once.
type Session struct {
	ID         int64
	UserID     int64
	StudioID   int64
	TokenHash  string
	Generation int
	IssuedAt   time.Time
	ExpiresAt  time.Time
	RevokedAt  *time.Time
	LastSeenAt time.Time
}

// EnsureUsable rejects revoked and expired sessions.
func (s *Session) EnsureUsable(now time.Time) error {
	if s.RevokedAt != nil {
		return apperr.New(apperr.CodeUnauthenticated, "session was revoked").With("reason", "revoked")
	}
	if !now.Before(s.ExpiresAt) {
		return apperr.New(apperr.CodeUnauthenticated, "session expired").With("reason", "expired")
	}
	return nil
}

// Principal is the authenticated caller derived from a session.
type Principal struct {
	UserID     int64
	StudioID   int64
	SessionID  int64
	Generation int
	Role       Role
	Email      string
}

// Authorize checks the principal role against a capability.
func (p Principal) Authorize(capability Capability) error {
	if !p.Role.Allows(capability) {
		return apperr.New(apperr.CodePermissionDenied, "role %s cannot perform %s", p.Role, capability).
			With("capability", string(capability)).
			With("role", string(p.Role))
	}
	return nil
}
