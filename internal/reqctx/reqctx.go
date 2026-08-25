// Package reqctx carries the request identifier and the authenticated principal
// through context from the HTTP edge down to repositories and workers.
package reqctx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
	"github.com/vance1852/manjuflow-studio/internal/domain/identity"
)

type requestIDKey struct{}

type principalKey struct{}

// NewRequestID generates a random correlation identifier.
func NewRequestID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "req-unavailable"
	}
	return "req_" + hex.EncodeToString(buf)
}

// SanitiseRequestID keeps caller supplied identifiers printable and bounded.
func SanitiseRequestID(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if len(trimmed) > 64 {
		trimmed = trimmed[:64]
	}
	var sb strings.Builder
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			sb.WriteRune(r)
		case r == '-' || r == '_' || r == '.' || r == ':':
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// WithRequestID stores the correlation identifier.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestID reads the correlation identifier, returning an empty string when the
// context did not pass through the HTTP middleware.
func RequestID(ctx context.Context) string {
	value, _ := ctx.Value(requestIDKey{}).(string)
	return value
}

// WithPrincipal stores the authenticated caller.
func WithPrincipal(ctx context.Context, principal identity.Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, principal)
}

// Principal reads the authenticated caller.
func Principal(ctx context.Context) (identity.Principal, bool) {
	value, ok := ctx.Value(principalKey{}).(identity.Principal)
	return value, ok
}

// RequirePrincipal returns the caller or an unauthenticated error.
func RequirePrincipal(ctx context.Context) (identity.Principal, error) {
	principal, ok := Principal(ctx)
	if !ok {
		return identity.Principal{}, apperr.New(apperr.CodeUnauthenticated, "request is not authenticated")
	}
	return principal, nil
}
