// Package middleware holds the HTTP cross cutting concerns: request identifiers,
// structured access logs, panic recovery, request deadlines and bearer
// authentication.
package middleware

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
	"github.com/vance1852/manjuflow-studio/internal/domain/identity"
	"github.com/vance1852/manjuflow-studio/internal/logging"
	"github.com/vance1852/manjuflow-studio/internal/reqctx"
)

// HeaderRequestID is the correlation header accepted and echoed by the service.
const HeaderRequestID = "X-Request-Id"

// Authenticator resolves a bearer token into a principal.
type Authenticator interface {
	Authenticate(ctx context.Context, token string) (identity.Principal, error)
}

// ErrorWriter renders a classified error as the unified JSON envelope.
type ErrorWriter func(w http.ResponseWriter, r *http.Request, err error)

// statusRecorder captures the response status for the access log.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(payload []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	written, err := s.ResponseWriter.Write(payload)
	s.bytes += written
	return written, err
}

// RequestID assigns or reuses a correlation identifier and echoes it back.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := reqctx.SanitiseRequestID(r.Header.Get(HeaderRequestID))
		if id == "" {
			id = reqctx.NewRequestID()
		}
		w.Header().Set(HeaderRequestID, id)
		next.ServeHTTP(w, r.WithContext(reqctx.WithRequestID(r.Context(), id)))
	})
}

// AccessLog emits one structured record per request.
func AccessLog(logger *logging.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			recorder := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(recorder, r)
			status := recorder.status
			if status == 0 {
				status = http.StatusOK
			}
			logger.Info("http request",
				"request_id", reqctx.RequestID(r.Context()),
				"method", r.Method,
				"path", r.URL.Path,
				"status", status,
				"bytes", recorder.bytes,
				"duration", time.Since(started))
		})
	}
}

// Recover converts a panic into the unified internal error envelope so one bad
// request cannot take the process down.
func Recover(logger *logging.Logger, write ErrorWriter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if recovered := recover(); recovered != nil {
					logger.Error("panic recovered",
						"request_id", reqctx.RequestID(r.Context()),
						"path", r.URL.Path,
						"panic", recovered)
					write(w, r, apperr.New(apperr.CodeInternal, "unexpected server fault"))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// Timeout bounds the request context so a slow database call cannot hold a
// connection forever.
func Timeout(limit time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if limit <= 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), limit)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Authenticated rejects requests without a usable session and stores the
// principal in context.
func Authenticated(auth Authenticator, write ErrorWriter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, err := BearerToken(r)
			if err != nil {
				write(w, r, err)
				return
			}
			principal, err := auth.Authenticate(r.Context(), token)
			if err != nil {
				write(w, r, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(reqctx.WithPrincipal(r.Context(), principal)))
		})
	}
}

// BearerToken extracts the bearer credential from the Authorization header.
func BearerToken(r *http.Request) (string, error) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header == "" {
		return "", apperr.New(apperr.CodeUnauthenticated, "authorization header is missing")
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") || strings.TrimSpace(parts[1]) == "" {
		return "", apperr.New(apperr.CodeUnauthenticated, "authorization header must use the bearer scheme")
	}
	return strings.TrimSpace(parts[1]), nil
}

// Chain applies middlewares in the given order, outermost first.
func Chain(handler http.Handler, layers ...func(http.Handler) http.Handler) http.Handler {
	for i := len(layers) - 1; i >= 0; i-- {
		handler = layers[i](handler)
	}
	return handler
}
