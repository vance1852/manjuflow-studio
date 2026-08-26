// Package idempotency implements first-writer-wins request keys. The key is
// scoped by studio, HTTP method and path so the same client key on two different
// endpoints stays independent.
package idempotency

import (
	"context"
	"strings"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
	"github.com/vance1852/manjuflow-studio/internal/clock"
	"github.com/vance1852/manjuflow-studio/internal/repository"
	"github.com/vance1852/manjuflow-studio/internal/security"
)

// MaxKeyLength bounds a caller supplied key.
const MaxKeyLength = 120

// Request describes the mutating call being guarded.
type Request struct {
	StudioID int64
	Method   string
	Path     string
	Key      string
	Payload  []string
}

// Claim is the outcome of guarding a request.
type Claim struct {
	// Guarded is false when the caller supplied no key.
	Guarded bool
	// Replayed is true when a completed response must be returned as is.
	Replayed bool
	RecordID int64
	Status   int
	Response string
}

// Guard coordinates the stored records.
type Guard struct {
	records repository.IdempotencyRepository
	clock   clock.Clock
}

// New builds a guard.
func New(records repository.IdempotencyRepository, timeSource clock.Clock) *Guard {
	return &Guard{records: records, clock: timeSource}
}

// ValidateKey bounds the caller supplied key.
func ValidateKey(raw string) (string, error) {
	key := strings.TrimSpace(raw)
	if key == "" {
		return "", nil
	}
	if len(key) > MaxKeyLength {
		return "", apperr.New(apperr.CodeInvalidArgument, "idempotency key must not exceed %d characters", MaxKeyLength).
			With("field", "Idempotency-Key")
	}
	for _, r := range key {
		if r <= 32 || r == 127 {
			return "", apperr.New(apperr.CodeInvalidArgument, "idempotency key must not contain control characters").
				With("field", "Idempotency-Key")
		}
	}
	return key, nil
}

// Begin claims the key. A completed record replays, an in-progress record is a
// conflict, a failed record with the same payload is reopened for the retry and
// a different payload under the same key is rejected.
func (g *Guard) Begin(ctx context.Context, q repository.Querier, request Request) (Claim, error) {
	key, err := ValidateKey(request.Key)
	if err != nil {
		return Claim{}, err
	}
	if key == "" {
		return Claim{Guarded: false}, nil
	}
	fingerprint := security.Fingerprint(append([]string{request.Method, request.Path}, request.Payload...)...)
	now := g.clock.Now()
	record, created, err := g.records.Begin(ctx, q, repository.IdempotencyRecord{
		StudioID:    request.StudioID,
		Method:      request.Method,
		Path:        request.Path,
		Key:         key,
		Fingerprint: fingerprint,
		CreatedAt:   now,
	})
	if err != nil {
		return Claim{}, err
	}
	if created {
		return Claim{Guarded: true, RecordID: record.ID}, nil
	}
	if record.Fingerprint != fingerprint {
		return Claim{}, apperr.New(apperr.CodeConflict,
			"idempotency key %q was already used with a different payload", key).
			With("field", "Idempotency-Key")
	}
	switch record.State {
	case repository.IdempotencyCompleted:
		return Claim{Guarded: true, Replayed: true, RecordID: record.ID, Status: record.StatusCode, Response: record.Response}, nil
	case repository.IdempotencyInProgress:
		return Claim{}, apperr.New(apperr.CodeConflict,
			"idempotency key %q is still being processed", key).
			With("field", "Idempotency-Key")
	default:
		if err := g.records.Reopen(ctx, q, record.ID, fingerprint, now); err != nil {
			return Claim{}, err
		}
		return Claim{Guarded: true, RecordID: record.ID}, nil
	}
}

// Finish records the outcome of a guarded request so a later call with the same
// key replays the stored response instead of running the work twice. It is only
// for requests that produced a durable side effect: a finished render job, a
// stored prompt version and so on.
func (g *Guard) Finish(ctx context.Context, q repository.Querier, claim Claim, status int, response string) error {
	if !claim.Guarded || claim.Replayed || claim.RecordID == 0 {
		return nil
	}
	return g.records.Complete(ctx, q, claim.RecordID, status, response, g.clock.Now())
}

// Fail releases the key of a request whose business work rolled back, so a later
// call with the same key reopens it and runs the work again instead of replaying
// the refusal. Call it when the guarded request was rejected before it produced
// any durable side effect, for example because the daily render quota was
// exhausted. A request that was actually accepted must still call Finish so the
// same key keeps replaying the original response.
func (g *Guard) Fail(ctx context.Context, q repository.Querier, claim Claim) error {
	if !claim.Guarded || claim.Replayed || claim.RecordID == 0 {
		return nil
	}
	return g.records.Fail(ctx, q, claim.RecordID, g.clock.Now())
}
