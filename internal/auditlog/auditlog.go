// Package auditlog writes audit events inside the same transaction as the
// business change, filling in the actor and the request identifier from context.
package auditlog

import (
	"context"

	"github.com/vance1852/manjuflow-studio/internal/clock"
	"github.com/vance1852/manjuflow-studio/internal/domain/audit"
	"github.com/vance1852/manjuflow-studio/internal/repository"
	"github.com/vance1852/manjuflow-studio/internal/reqctx"
)

// Recorder appends audit events.
type Recorder struct {
	events repository.AuditRepository
	clock  clock.Clock
}

// New builds a recorder.
func New(events repository.AuditRepository, timeSource clock.Clock) *Recorder {
	return &Recorder{events: events, clock: timeSource}
}

// Record completes the event from context and appends it. The caller keeps
// ownership of the transaction so an audit failure rolls the business change
// back with it.
func (r *Recorder) Record(ctx context.Context, q repository.Querier, event audit.Event) error {
	prepared := event.Clone()
	if principal, ok := reqctx.Principal(ctx); ok {
		if prepared.ActorID == 0 {
			prepared.ActorID = principal.UserID
		}
		if prepared.ActorRole == "" {
			prepared.ActorRole = string(principal.Role)
		}
		if prepared.StudioID == 0 {
			prepared.StudioID = principal.StudioID
		}
	}
	if prepared.ActorRole == "" {
		prepared.ActorRole = "system"
	}
	if prepared.RequestID == "" {
		prepared.RequestID = reqctx.RequestID(ctx)
	}
	if prepared.Result == "" {
		prepared.Result = audit.ResultSuccess
	}
	if prepared.CreatedAt.IsZero() {
		prepared.CreatedAt = r.clock.Now()
	}
	if _, err := r.events.Append(ctx, q, prepared); err != nil {
		return err
	}
	return nil
}

// Success is a small helper for the common committed-change event.
func Success(action, objectType string, objectID int64) audit.Event {
	return audit.Event{
		Action:     action,
		ObjectType: objectType,
		ObjectID:   objectID,
		Result:     audit.ResultSuccess,
	}
}

// Rejected records a business rule that refused a request.
func Rejected(action, objectType string, objectID int64) audit.Event {
	return audit.Event{
		Action:     action,
		ObjectType: objectType,
		ObjectID:   objectID,
		Result:     audit.ResultRejected,
	}
}
