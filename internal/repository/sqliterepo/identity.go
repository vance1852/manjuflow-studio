package sqliterepo

import (
	"context"
	"database/sql"
	"time"

	"github.com/vance1852/manjuflow-studio/internal/domain/identity"
	"github.com/vance1852/manjuflow-studio/internal/repository"
)

// StudioStore implements repository.StudioRepository.
type StudioStore struct{}

// NewStudioStore builds the studio repository.
func NewStudioStore() StudioStore { return StudioStore{} }

// Create inserts a studio row.
func (StudioStore) Create(ctx context.Context, q repository.Querier, slug, name string, capacity int, now time.Time) (int64, error) {
	return insert(ctx, q, "cannot create studio",
		`INSERT INTO studios (slug, name, daily_render_capacity, created_at) VALUES (?, ?, ?, ?)`,
		slug, name, capacity, encodeTime(now))
}

// FindBySlug loads a studio by its slug.
func (StudioStore) FindBySlug(ctx context.Context, q repository.Querier, slug string) (repository.Studio, error) {
	row := q.QueryRowContext(ctx,
		`SELECT id, slug, name, daily_render_capacity, created_at FROM studios WHERE slug = ?`, slug)
	return scanStudio(row, "studio", slug)
}

// FindByID loads a studio by identifier.
func (StudioStore) FindByID(ctx context.Context, q repository.Querier, id int64) (repository.Studio, error) {
	row := q.QueryRowContext(ctx,
		`SELECT id, slug, name, daily_render_capacity, created_at FROM studios WHERE id = ?`, id)
	return scanStudio(row, "studio", id)
}

func scanStudio(row *sql.Row, entity string, key any) (repository.Studio, error) {
	var (
		studio    repository.Studio
		createdAt string
	)
	if err := row.Scan(&studio.ID, &studio.Slug, &studio.Name, &studio.DailyRenderCapacity, &createdAt); err != nil {
		if noRows(err) {
			return repository.Studio{}, repository.NotFound(entity, key)
		}
		return repository.Studio{}, wrap(err, "cannot read studio")
	}
	parsed, err := decodeTime(createdAt)
	if err != nil {
		return repository.Studio{}, err
	}
	studio.CreatedAt = parsed
	return studio, nil
}

// UserStore implements repository.UserRepository.
type UserStore struct{}

// NewUserStore builds the user repository.
func NewUserStore() UserStore { return UserStore{} }

// Create inserts a studio member.
func (UserStore) Create(ctx context.Context, q repository.Querier, user identity.User) (int64, error) {
	return insert(ctx, q, "cannot create user",
		`INSERT INTO users (studio_id, email, display_name, password_hash, role, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		user.StudioID, user.Email, user.DisplayName, user.PasswordHash,
		string(user.Role), string(user.Status), encodeTime(user.CreatedAt))
}

const userColumns = `id, studio_id, email, display_name, password_hash, role, status, created_at`

// FindByEmail loads a member by studio scoped email.
func (UserStore) FindByEmail(ctx context.Context, q repository.Querier, studioID int64, email string) (identity.User, error) {
	row := q.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE studio_id = ? AND email = ?`, studioID, email)
	return scanUser(row, email)
}

// FindByID loads a member by identifier.
func (UserStore) FindByID(ctx context.Context, q repository.Querier, id int64) (identity.User, error) {
	row := q.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id)
	return scanUser(row, id)
}

// CountByRole counts active members of one role.
func (UserStore) CountByRole(ctx context.Context, q repository.Querier, studioID int64, role identity.Role) (int, error) {
	return countRows(ctx, q, "cannot count users",
		`SELECT COUNT(*) FROM users WHERE studio_id = ? AND role = ? AND status = 'active'`,
		studioID, string(role))
}

func scanUser(row *sql.Row, key any) (identity.User, error) {
	var (
		user      identity.User
		role      string
		status    string
		createdAt string
	)
	err := row.Scan(&user.ID, &user.StudioID, &user.Email, &user.DisplayName, &user.PasswordHash,
		&role, &status, &createdAt)
	if err != nil {
		if noRows(err) {
			return identity.User{}, repository.NotFound("user", key)
		}
		return identity.User{}, wrap(err, "cannot read user")
	}
	parsed, err := decodeTime(createdAt)
	if err != nil {
		return identity.User{}, err
	}
	user.Role = identity.Role(role)
	user.Status = identity.Status(status)
	user.CreatedAt = parsed
	return user, nil
}

// SessionStore implements repository.SessionRepository.
type SessionStore struct{}

// NewSessionStore builds the session repository.
func NewSessionStore() SessionStore { return SessionStore{} }

// Create stores a new session digest.
func (SessionStore) Create(ctx context.Context, q repository.Querier, session identity.Session) (int64, error) {
	return insert(ctx, q, "cannot create session",
		`INSERT INTO sessions (user_id, studio_id, token_hash, generation, issued_at, expires_at, revoked_at, last_seen_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		session.UserID, session.StudioID, session.TokenHash, session.Generation,
		encodeTime(session.IssuedAt), encodeTime(session.ExpiresAt),
		encodeNullableTime(session.RevokedAt), encodeTime(session.LastSeenAt))
}

// FindByTokenHash loads a session by its stored digest.
func (SessionStore) FindByTokenHash(ctx context.Context, q repository.Querier, tokenHash string) (identity.Session, error) {
	row := q.QueryRowContext(ctx,
		`SELECT id, user_id, studio_id, token_hash, generation, issued_at, expires_at, revoked_at, last_seen_at
		 FROM sessions WHERE token_hash = ?`, tokenHash)
	var (
		session   identity.Session
		issuedAt  string
		expiresAt string
		revokedAt sql.NullString
		lastSeen  string
	)
	err := row.Scan(&session.ID, &session.UserID, &session.StudioID, &session.TokenHash, &session.Generation,
		&issuedAt, &expiresAt, &revokedAt, &lastSeen)
	if err != nil {
		if noRows(err) {
			return identity.Session{}, repository.NotFound("session", "token")
		}
		return identity.Session{}, wrap(err, "cannot read session")
	}
	if session.IssuedAt, err = decodeTime(issuedAt); err != nil {
		return identity.Session{}, err
	}
	if session.ExpiresAt, err = decodeTime(expiresAt); err != nil {
		return identity.Session{}, err
	}
	if session.LastSeenAt, err = decodeTime(lastSeen); err != nil {
		return identity.Session{}, err
	}
	revoked, err := decodeNullableTime(revokedAt)
	if err != nil {
		return identity.Session{}, err
	}
	session.RevokedAt = revoked
	return session, nil
}

// Revoke marks one session as revoked. Revoking an already revoked session keeps
// the first revocation instant.
func (SessionStore) Revoke(ctx context.Context, q repository.Querier, sessionID int64, at time.Time) error {
	affected, err := execExpectingRow(ctx, q, "cannot revoke session",
		`UPDATE sessions SET revoked_at = ?, generation = generation + 1
		 WHERE id = ? AND revoked_at IS NULL`, encodeTime(at), sessionID)
	if err != nil {
		return err
	}
	if affected == 0 {
		return repository.NotFound("active session", sessionID)
	}
	return nil
}

// RevokeAllForUser revokes every live session of a member.
func (SessionStore) RevokeAllForUser(ctx context.Context, q repository.Querier, userID int64, at time.Time) (int, error) {
	affected, err := execExpectingRow(ctx, q, "cannot revoke sessions",
		`UPDATE sessions SET revoked_at = ?, generation = generation + 1
		 WHERE user_id = ? AND revoked_at IS NULL`, encodeTime(at), userID)
	if err != nil {
		return 0, err
	}
	return int(affected), nil
}

// TouchLastSeen records session activity.
func (SessionStore) TouchLastSeen(ctx context.Context, q repository.Querier, sessionID int64, at time.Time) error {
	_, err := execExpectingRow(ctx, q, "cannot update session activity",
		`UPDATE sessions SET last_seen_at = ? WHERE id = ?`, encodeTime(at), sessionID)
	return err
}

// DeleteExpired removes sessions that expired before the given instant.
func (SessionStore) DeleteExpired(ctx context.Context, q repository.Querier, before time.Time) (int, error) {
	affected, err := execExpectingRow(ctx, q, "cannot prune sessions",
		`DELETE FROM sessions WHERE expires_at < ?`, encodeTime(before))
	if err != nil {
		return 0, err
	}
	return int(affected), nil
}
