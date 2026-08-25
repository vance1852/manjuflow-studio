// Package authsvc owns login, session lifecycle and studio membership.
package authsvc

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
	"github.com/vance1852/manjuflow-studio/internal/auditlog"
	"github.com/vance1852/manjuflow-studio/internal/clock"
	"github.com/vance1852/manjuflow-studio/internal/domain/audit"
	"github.com/vance1852/manjuflow-studio/internal/domain/identity"
	"github.com/vance1852/manjuflow-studio/internal/repository"
	"github.com/vance1852/manjuflow-studio/internal/reqctx"
	"github.com/vance1852/manjuflow-studio/internal/security"
)

// Service implements the identity use cases.
type Service struct {
	runner   repository.Runner
	studios  repository.StudioRepository
	users    repository.UserRepository
	sessions repository.SessionRepository
	audits   *auditlog.Recorder
	clock    clock.Clock
	ttl      time.Duration
}

// Options configures the service.
type Options struct {
	Runner     repository.Runner
	Studios    repository.StudioRepository
	Users      repository.UserRepository
	Sessions   repository.SessionRepository
	Audits     *auditlog.Recorder
	Clock      clock.Clock
	SessionTTL time.Duration
}

// New builds the auth service.
func New(opts Options) *Service {
	return &Service{
		runner:   opts.Runner,
		studios:  opts.Studios,
		users:    opts.Users,
		sessions: opts.Sessions,
		audits:   opts.Audits,
		clock:    opts.Clock,
		ttl:      opts.SessionTTL,
	}
}

// LoginInput carries the credential attempt.
type LoginInput struct {
	StudioSlug string
	Email      string
	Password   string
}

// LoginResult carries the one-time bearer token.
type LoginResult struct {
	Token     string
	ExpiresAt time.Time
	User      identity.User
}

// Login verifies the credential and opens a revocable session.
func (s *Service) Login(ctx context.Context, input LoginInput) (LoginResult, error) {
	email, err := identity.NormaliseEmail(input.Email)
	if err != nil {
		return LoginResult{}, err
	}
	slug := strings.TrimSpace(input.StudioSlug)
	if slug == "" {
		return LoginResult{}, apperr.New(apperr.CodeInvalidArgument, "studio must be provided").With("field", "studio")
	}
	token, err := security.NewSessionToken()
	if err != nil {
		return LoginResult{}, err
	}
	now := s.clock.Now()
	var result LoginResult
	err = s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		studio, err := s.studios.FindBySlug(ctx, q, slug)
		if err != nil {
			if apperr.IsCode(err, apperr.CodeNotFound) {
				return apperr.New(apperr.CodeUnauthenticated, "credentials do not match")
			}
			return err
		}
		user, err := s.users.FindByEmail(ctx, q, studio.ID, email)
		if err != nil {
			if apperr.IsCode(err, apperr.CodeNotFound) {
				return apperr.New(apperr.CodeUnauthenticated, "credentials do not match")
			}
			return err
		}
		if err := security.VerifyPassword(user.PasswordHash, input.Password); err != nil {
			return err
		}
		if err := user.EnsureActive(); err != nil {
			return err
		}
		session := identity.Session{
			UserID:     user.ID,
			StudioID:   user.StudioID,
			TokenHash:  security.HashToken(token),
			Generation: 1,
			IssuedAt:   now,
			ExpiresAt:  now.Add(s.ttl),
			LastSeenAt: now,
		}
		sessionID, err := s.sessions.Create(ctx, q, session)
		if err != nil {
			return err
		}
		event := auditlog.Success("session.opened", audit.ObjectSession, sessionID).
			WithDetail("email", user.Email).
			WithDetail("role", string(user.Role))
		event.StudioID = user.StudioID
		event.ActorID = user.ID
		event.ActorRole = string(user.Role)
		if err := s.audits.Record(ctx, q, event); err != nil {
			return err
		}
		result = LoginResult{Token: token, ExpiresAt: session.ExpiresAt, User: user}
		return nil
	})
	if err != nil {
		return LoginResult{}, err
	}
	result.User.PasswordHash = ""
	return result, nil
}

// Authenticate resolves a bearer token into a principal. Expired and revoked
// sessions are rejected with a stable unauthenticated code.
func (s *Service) Authenticate(ctx context.Context, token string) (identity.Principal, error) {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return identity.Principal{}, apperr.New(apperr.CodeUnauthenticated, "bearer token is missing")
	}
	now := s.clock.Now()
	var principal identity.Principal
	err := s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		session, err := s.sessions.FindByTokenHash(ctx, q, security.HashToken(trimmed))
		if err != nil {
			if apperr.IsCode(err, apperr.CodeNotFound) {
				return apperr.New(apperr.CodeUnauthenticated, "session is unknown")
			}
			return err
		}
		if err := session.EnsureUsable(now); err != nil {
			return err
		}
		user, err := s.users.FindByID(ctx, q, session.UserID)
		if err != nil {
			return err
		}
		if err := user.EnsureActive(); err != nil {
			return err
		}
		if err := s.sessions.TouchLastSeen(ctx, q, session.ID, now); err != nil {
			return err
		}
		principal = identity.Principal{
			UserID:     user.ID,
			StudioID:   user.StudioID,
			SessionID:  session.ID,
			Generation: session.Generation,
			Role:       user.Role,
			Email:      user.Email,
		}
		return nil
	})
	if err != nil {
		return identity.Principal{}, err
	}
	return principal, nil
}

// Logout revokes the session of the current caller.
func (s *Service) Logout(ctx context.Context) error {
	principal, err := reqctx.RequirePrincipal(ctx)
	if err != nil {
		return err
	}
	now := s.clock.Now()
	return s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		if err := s.sessions.Revoke(ctx, q, principal.SessionID, now); err != nil {
			if apperr.IsCode(err, apperr.CodeNotFound) {
				return apperr.New(apperr.CodeUnauthenticated, "session is no longer active")
			}
			return err
		}
		return s.audits.Record(ctx, q, auditlog.Success("session.revoked", audit.ObjectSession, principal.SessionID))
	})
}

// CreateMemberInput describes a new studio member.
type CreateMemberInput struct {
	Email       string
	DisplayName string
	Password    string
	Role        string
}

// CreateMember lets a director add an apprentice or a co-director.
func (s *Service) CreateMember(ctx context.Context, input CreateMemberInput) (identity.User, error) {
	principal, err := reqctx.RequirePrincipal(ctx)
	if err != nil {
		return identity.User{}, err
	}
	if principal.Role != identity.RoleDirector {
		return identity.User{}, apperr.New(apperr.CodePermissionDenied, "only a director can add studio members").
			With("role", string(principal.Role))
	}
	email, err := identity.NormaliseEmail(input.Email)
	if err != nil {
		return identity.User{}, err
	}
	role, err := identity.ParseRole(input.Role)
	if err != nil {
		return identity.User{}, err
	}
	displayName := strings.TrimSpace(input.DisplayName)
	if displayName == "" || utf8.RuneCountInString(displayName) > 60 {
		return identity.User{}, apperr.New(apperr.CodeInvalidArgument, "display name must be 1 to 60 characters").
			With("field", "display_name")
	}
	hash, err := security.HashPassword(input.Password)
	if err != nil {
		return identity.User{}, err
	}
	user := identity.User{
		StudioID:     principal.StudioID,
		Email:        email,
		DisplayName:  displayName,
		PasswordHash: hash,
		Role:         role,
		Status:       identity.StatusActive,
		CreatedAt:    s.clock.Now(),
	}
	err = s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		id, err := s.users.Create(ctx, q, user)
		if err != nil {
			if apperr.IsCode(err, apperr.CodeConflict) {
				return apperr.New(apperr.CodeConflict, "email %s is already registered in this studio", email).
					With("field", "email")
			}
			return err
		}
		user.ID = id
		return s.audits.Record(ctx, q, auditlog.Success("member.created", "user", id).
			WithDetail("email", email).
			WithDetail("role", string(role)))
	})
	if err != nil {
		return identity.User{}, err
	}
	user.PasswordHash = ""
	return user, nil
}

// CurrentSession describes the caller session for the whoami endpoint.
type CurrentSession struct {
	User         identity.User
	SessionID    int64
	Capabilities []identity.Capability
}

// Describe returns the caller session.
func (s *Service) Describe(ctx context.Context) (CurrentSession, error) {
	principal, err := reqctx.RequirePrincipal(ctx)
	if err != nil {
		return CurrentSession{}, err
	}
	var out CurrentSession
	err = s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		user, err := s.users.FindByID(ctx, q, principal.UserID)
		if err != nil {
			return err
		}
		user.PasswordHash = ""
		out.User = user
		out.SessionID = principal.SessionID
		out.Capabilities = user.Role.Capabilities()
		return nil
	})
	if err != nil {
		return CurrentSession{}, err
	}
	return out, nil
}

// PruneExpiredSessions removes sessions whose expiry has passed. It is called by
// the background sweeper.
func (s *Service) PruneExpiredSessions(ctx context.Context) (int, error) {
	cutoff := s.clock.Now()
	var removed int
	err := s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		count, err := s.sessions.DeleteExpired(ctx, q, cutoff)
		if err != nil {
			return err
		}
		removed = count
		return nil
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}

// EnsureBootstrapInput describes the first-run studio and director seed.
type EnsureBootstrapInput struct {
	StudioSlug          string
	StudioName          string
	DailyRenderCapacity int
	DirectorEmail       string
	DirectorPassword    string
	ApprenticeEmail     string
	ApprenticePassword  string
}

// BootstrapResult reports what the first run created.
type BootstrapResult struct {
	StudioID        int64
	CreatedStudio   bool
	CreatedDirector bool
	CreatedTrainee  bool
}

// EnsureBootstrap creates the studio and its two starting roles when they are
// missing. Running it again on a populated database changes nothing.
func (s *Service) EnsureBootstrap(ctx context.Context, input EnsureBootstrapInput) (BootstrapResult, error) {
	directorEmail, err := identity.NormaliseEmail(input.DirectorEmail)
	if err != nil {
		return BootstrapResult{}, err
	}
	apprenticeEmail, err := identity.NormaliseEmail(input.ApprenticeEmail)
	if err != nil {
		return BootstrapResult{}, err
	}
	directorHash, err := security.HashPassword(input.DirectorPassword)
	if err != nil {
		return BootstrapResult{}, err
	}
	apprenticeHash, err := security.HashPassword(input.ApprenticePassword)
	if err != nil {
		return BootstrapResult{}, err
	}
	now := s.clock.Now()
	var result BootstrapResult
	err = s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		studio, err := s.studios.FindBySlug(ctx, q, input.StudioSlug)
		switch {
		case err == nil:
			result.StudioID = studio.ID
		case apperr.IsCode(err, apperr.CodeNotFound):
			id, createErr := s.studios.Create(ctx, q, input.StudioSlug, input.StudioName, input.DailyRenderCapacity, now)
			if createErr != nil {
				return createErr
			}
			result.StudioID = id
			result.CreatedStudio = true
		default:
			return err
		}

		created, err := s.ensureMember(ctx, q, identity.User{
			StudioID:     result.StudioID,
			Email:        directorEmail,
			DisplayName:  "Studio Director",
			PasswordHash: directorHash,
			Role:         identity.RoleDirector,
			Status:       identity.StatusActive,
			CreatedAt:    now,
		})
		if err != nil {
			return err
		}
		result.CreatedDirector = created

		created, err = s.ensureMember(ctx, q, identity.User{
			StudioID:     result.StudioID,
			Email:        apprenticeEmail,
			DisplayName:  "Studio Apprentice",
			PasswordHash: apprenticeHash,
			Role:         identity.RoleApprentice,
			Status:       identity.StatusActive,
			CreatedAt:    now,
		})
		if err != nil {
			return err
		}
		result.CreatedTrainee = created
		return nil
	})
	if err != nil {
		return BootstrapResult{}, err
	}
	return result, nil
}

func (s *Service) ensureMember(ctx context.Context, q repository.Querier, user identity.User) (bool, error) {
	_, err := s.users.FindByEmail(ctx, q, user.StudioID, user.Email)
	if err == nil {
		return false, nil
	}
	if !apperr.IsCode(err, apperr.CodeNotFound) {
		return false, err
	}
	if _, err := s.users.Create(ctx, q, user); err != nil {
		return false, err
	}
	return true, nil
}
