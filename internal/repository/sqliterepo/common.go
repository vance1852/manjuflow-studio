// Package sqliterepo implements the repository contracts on top of SQLite. All
// statements live here so services never assemble SQL themselves.
package sqliterepo

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
	"github.com/vance1852/manjuflow-studio/internal/clock"
	"github.com/vance1852/manjuflow-studio/internal/repository"
)

var businessLocation = clock.MustBusinessLocation()

// encodeTime stores instants as UTC RFC3339 with nanosecond precision so text
// ordering matches chronological ordering.
func encodeTime(at time.Time) string {
	return at.UTC().Format(time.RFC3339Nano)
}

func encodeNullableTime(at *time.Time) any {
	if at == nil {
		return nil
	}
	return encodeTime(*at)
}

// decodeTime restores an instant into the business time zone.
func decodeTime(raw string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, apperr.Wrap(err, apperr.CodeInternal, "stored timestamp %q is malformed", raw)
	}
	return parsed.In(businessLocation), nil
}

func decodeNullableTime(raw sql.NullString) (*time.Time, error) {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil, nil
	}
	parsed, err := decodeTime(raw.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func decodeNullableInt64(raw sql.NullInt64) *int64 {
	if !raw.Valid {
		return nil
	}
	value := raw.Int64
	return &value
}

// wrap classifies a driver error. Unique constraint violations become conflicts
// so callers can react to a lost race without string matching.
func wrap(err error, message string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return apperr.Wrap(err, apperr.CodeCanceled, "%s", message)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return apperr.Wrap(err, apperr.CodeDeadlineExceeded, "%s", message)
	}
	if IsUniqueViolation(err) {
		return apperr.Wrap(err, apperr.CodeConflict, "%s", message)
	}
	if isForeignKeyViolation(err) {
		return apperr.Wrap(err, apperr.CodeFailedPrecondition, "%s", message)
	}
	return apperr.Wrap(err, apperr.CodeInternal, "%s", message)
}

// IsUniqueViolation reports whether err is a SQLite uniqueness failure.
func IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	text := err.Error()
	return strings.Contains(text, "UNIQUE constraint failed") || strings.Contains(text, "constraint failed: UNIQUE")
}

func isForeignKeyViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "FOREIGN KEY constraint failed")
}

func insert(ctx context.Context, q repository.Querier, message, statement string, args ...any) (int64, error) {
	result, err := q.ExecContext(ctx, statement, args...)
	if err != nil {
		return 0, wrap(err, message)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, wrap(err, message)
	}
	return id, nil
}

func execExpectingRow(ctx context.Context, q repository.Querier, message, statement string, args ...any) (int64, error) {
	result, err := q.ExecContext(ctx, statement, args...)
	if err != nil {
		return 0, wrap(err, message)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, wrap(err, message)
	}
	return affected, nil
}

func countRows(ctx context.Context, q repository.Querier, message, statement string, args ...any) (int, error) {
	var total int
	if err := q.QueryRowContext(ctx, statement, args...).Scan(&total); err != nil {
		return 0, wrap(err, message)
	}
	return total, nil
}

func noRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// staleWrite is returned when an optimistic update matched no row.
func staleWrite(entity string, id int64, version int) error {
	return apperr.New(apperr.CodeConflict,
		"%s %d was modified concurrently; expected version %d", entity, id, version).
		With("entity", entity).
		With("expected_version", itoa(version))
}

func itoa(value int) string { return strconv.Itoa(value) }

// orderClause renders a validated ORDER BY fragment. The column always comes
// from a whitelist map, never from caller text.
func orderClause(column string, desc bool) string {
	direction := " ASC"
	if desc {
		direction = " DESC"
	}
	return " ORDER BY " + column + direction + ", id" + direction
}
