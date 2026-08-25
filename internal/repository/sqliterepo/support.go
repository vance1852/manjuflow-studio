package sqliterepo

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
	"github.com/vance1852/manjuflow-studio/internal/domain/audit"
	"github.com/vance1852/manjuflow-studio/internal/repository"
)

// AuditStore implements repository.AuditRepository.
type AuditStore struct{}

// NewAuditStore builds the audit repository.
func NewAuditStore() AuditStore { return AuditStore{} }

// Append writes one audit row inside the caller transaction.
func (AuditStore) Append(ctx context.Context, q repository.Querier, event audit.Event) (int64, error) {
	if err := event.Validate(); err != nil {
		return 0, err
	}
	detail, err := event.DetailJSON()
	if err != nil {
		return 0, err
	}
	return insert(ctx, q, "cannot append audit event",
		`INSERT INTO audit_events (studio_id, actor_id, actor_role, action, object_type, object_id, result,
		 request_id, detail, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.StudioID, event.ActorID, event.ActorRole, event.Action, event.ObjectType, event.ObjectID,
		string(event.Result), event.RequestID, detail, encodeTime(event.CreatedAt))
}

var auditSortColumns = map[string]string{
	"created_at": "created_at",
	"action":     "action",
}

// AuditSortColumns exposes the allowed sort keys for audit listings.
func AuditSortColumns() map[string]string { return copyColumns(auditSortColumns) }

// List returns one page of audit events plus the matching total.
func (AuditStore) List(ctx context.Context, q repository.Querier, studioID int64, filter repository.AuditFilter, page repository.Page) ([]audit.Event, int, error) {
	normalised, err := repository.NormalisePage(page, auditSortColumns, "created_at")
	if err != nil {
		return nil, 0, err
	}
	where := []string{"studio_id = ?"}
	args := []any{studioID}
	if trimmed := strings.TrimSpace(filter.ObjectType); trimmed != "" {
		where = append(where, "object_type = ?")
		args = append(args, trimmed)
	}
	if filter.ObjectID > 0 {
		where = append(where, "object_id = ?")
		args = append(args, filter.ObjectID)
	}
	if filter.ActorID > 0 {
		where = append(where, "actor_id = ?")
		args = append(args, filter.ActorID)
	}
	if trimmed := strings.TrimSpace(filter.Action); trimmed != "" {
		where = append(where, "action = ?")
		args = append(args, trimmed)
	}
	clause := " WHERE " + strings.Join(where, " AND ")

	total, err := countRows(ctx, q, "cannot count audit events", `SELECT COUNT(*) FROM audit_events`+clause, args...)
	if err != nil {
		return nil, 0, err
	}
	statement := `SELECT id, studio_id, actor_id, actor_role, action, object_type, object_id, result, request_id,
		detail, created_at FROM audit_events` + clause +
		orderClause(repository.Column(auditSortColumns, normalised.SortBy), normalised.Desc) +
		` LIMIT ? OFFSET ?`
	pageArgs := append(append([]any{}, args...), normalised.Limit, normalised.Offset)
	rows, err := q.QueryContext(ctx, statement, pageArgs...)
	if err != nil {
		return nil, 0, wrap(err, "cannot list audit events")
	}
	defer func() { _ = rows.Close() }()

	out := make([]audit.Event, 0, normalised.Limit)
	for rows.Next() {
		var (
			event     audit.Event
			result    string
			detail    string
			createdAt string
		)
		if err := rows.Scan(&event.ID, &event.StudioID, &event.ActorID, &event.ActorRole, &event.Action,
			&event.ObjectType, &event.ObjectID, &result, &event.RequestID, &detail, &createdAt); err != nil {
			return nil, 0, wrap(err, "cannot scan audit event")
		}
		parsedDetail, err := audit.ParseDetail(detail)
		if err != nil {
			return nil, 0, err
		}
		parsedCreated, err := decodeTime(createdAt)
		if err != nil {
			return nil, 0, err
		}
		event.Result = audit.Result(result)
		event.Detail = parsedDetail
		event.CreatedAt = parsedCreated
		out = append(out, event.Clone())
	}
	if err := rows.Err(); err != nil {
		return nil, 0, wrap(err, "cannot iterate audit events")
	}
	return out, total, nil
}

// IdempotencyStore implements repository.IdempotencyRepository.
type IdempotencyStore struct{}

// NewIdempotencyStore builds the idempotency repository.
func NewIdempotencyStore() IdempotencyStore { return IdempotencyStore{} }

const idempotencyColumns = `id, studio_id, method, path, key, fingerprint, state, status_code, response, created_at, completed_at`

// Begin claims the key for the caller. When the unique constraint rejects the
// insert, the stored record is returned so the service can replay or refuse.
func (s IdempotencyStore) Begin(ctx context.Context, q repository.Querier, record repository.IdempotencyRecord) (repository.IdempotencyRecord, bool, error) {
	id, err := insert(ctx, q, "cannot claim idempotency key",
		`INSERT INTO idempotency_records (studio_id, method, path, key, fingerprint, state, status_code, response, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, 0, '', ?)`,
		record.StudioID, record.Method, record.Path, record.Key, record.Fingerprint,
		string(repository.IdempotencyInProgress), encodeTime(record.CreatedAt))
	if err == nil {
		record.ID = id
		record.State = repository.IdempotencyInProgress
		return record, true, nil
	}
	if !apperr.IsCode(err, apperr.CodeConflict) {
		return repository.IdempotencyRecord{}, false, err
	}
	existing, getErr := s.Get(ctx, q, record.StudioID, record.Method, record.Path, record.Key)
	if getErr != nil {
		return repository.IdempotencyRecord{}, false, getErr
	}
	return existing, false, nil
}

// Complete stores the replayable response of a finished request.
func (IdempotencyStore) Complete(ctx context.Context, q repository.Querier, id int64, statusCode int, response string, at time.Time) error {
	affected, err := execExpectingRow(ctx, q, "cannot complete idempotency record",
		`UPDATE idempotency_records SET state = ?, status_code = ?, response = ?, completed_at = ?
		 WHERE id = ? AND state = ?`,
		string(repository.IdempotencyCompleted), statusCode, response, encodeTime(at), id,
		string(repository.IdempotencyInProgress))
	if err != nil {
		return err
	}
	if affected == 0 {
		return repository.NotFound("in-progress idempotency record", id)
	}
	return nil
}

// Fail releases the key so the caller can retry the same request.
func (IdempotencyStore) Fail(ctx context.Context, q repository.Querier, id int64, at time.Time) error {
	_, err := execExpectingRow(ctx, q, "cannot release idempotency record",
		`UPDATE idempotency_records SET state = ?, completed_at = ? WHERE id = ? AND state = ?`,
		string(repository.IdempotencyFailed), encodeTime(at), id, string(repository.IdempotencyInProgress))
	return err
}

// Reopen lets a caller retry a request whose first attempt failed. The stored
// fingerprint must still match so a different payload cannot reuse the key.
func (IdempotencyStore) Reopen(ctx context.Context, q repository.Querier, id int64, fingerprint string, at time.Time) error {
	affected, err := execExpectingRow(ctx, q, "cannot reopen idempotency record",
		`UPDATE idempotency_records SET state = ?, status_code = 0, response = '', completed_at = NULL, created_at = ?
		 WHERE id = ? AND state = ? AND fingerprint = ?`,
		string(repository.IdempotencyInProgress), encodeTime(at), id,
		string(repository.IdempotencyFailed), fingerprint)
	if err != nil {
		return err
	}
	if affected == 0 {
		return repository.NotFound("failed idempotency record", id)
	}
	return nil
}

// Get loads one record by its natural key.
func (IdempotencyStore) Get(ctx context.Context, q repository.Querier, studioID int64, method, path, key string) (repository.IdempotencyRecord, error) {
	var (
		record      repository.IdempotencyRecord
		state       string
		createdAt   string
		completedAt sql.NullString
	)
	err := q.QueryRowContext(ctx,
		`SELECT `+idempotencyColumns+` FROM idempotency_records
		 WHERE studio_id = ? AND method = ? AND path = ? AND key = ?`, studioID, method, path, key).
		Scan(&record.ID, &record.StudioID, &record.Method, &record.Path, &record.Key, &record.Fingerprint,
			&state, &record.StatusCode, &record.Response, &createdAt, &completedAt)
	if err != nil {
		if noRows(err) {
			return repository.IdempotencyRecord{}, repository.NotFound("idempotency record", key)
		}
		return repository.IdempotencyRecord{}, wrap(err, "cannot read idempotency record")
	}
	if record.CreatedAt, err = decodeTime(createdAt); err != nil {
		return repository.IdempotencyRecord{}, err
	}
	completed, err := decodeNullableTime(completedAt)
	if err != nil {
		return repository.IdempotencyRecord{}, err
	}
	record.State = repository.IdempotencyState(state)
	record.CompletedAt = completed
	return record, nil
}

// SequenceStore implements repository.SequenceRepository.
type SequenceStore struct{}

// NewSequenceStore builds the business sequence repository.
func NewSequenceStore() SequenceStore { return SequenceStore{} }

// Next increments and returns a business counter in one statement so two
// concurrent callers can never observe the same number.
func (SequenceStore) Next(ctx context.Context, q repository.Querier, studioID int64, name string) (int64, error) {
	var value int64
	err := q.QueryRowContext(ctx,
		`INSERT INTO business_sequences (studio_id, name, value) VALUES (?, ?, 1)
		 ON CONFLICT (studio_id, name) DO UPDATE SET value = value + 1
		 RETURNING value`, studioID, name).Scan(&value)
	if err != nil {
		return 0, wrap(err, "cannot advance business sequence")
	}
	return value, nil
}
