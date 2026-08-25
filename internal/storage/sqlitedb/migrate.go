package sqlitedb

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
	"github.com/vance1852/manjuflow-studio/migrations"
)

const ledgerDDL = `CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    checksum TEXT NOT NULL,
    applied_at TEXT NOT NULL
)`

// AppliedStep is one row of the migration ledger.
type AppliedStep struct {
	Version   int
	Name      string
	Checksum  string
	AppliedAt time.Time
}

// Migrate applies every embedded migration that is not recorded yet. Running it
// twice is a no-op. A recorded version whose script changed, or a recorded
// version that no longer exists, blocks startup instead of silently diverging.
func (d *DB) Migrate(ctx context.Context) (int, error) {
	steps, err := migrations.Load()
	if err != nil {
		return 0, apperr.Wrap(err, apperr.CodeInternal, "cannot load embedded migrations")
	}
	if _, err := d.handle.ExecContext(ctx, ledgerDDL); err != nil {
		return 0, classify(err, "cannot create migration ledger")
	}
	applied, err := d.AppliedSteps(ctx)
	if err != nil {
		return 0, err
	}
	known := make(map[int]AppliedStep, len(applied))
	for _, step := range applied {
		known[step.Version] = step
	}
	embedded := make(map[int]bool, len(steps))
	for _, step := range steps {
		embedded[step.Version] = true
	}
	for _, recorded := range applied {
		if !embedded[recorded.Version] {
			return 0, apperr.New(apperr.CodeFailedPrecondition,
				"database contains unknown migration %d (%s); refusing to continue",
				recorded.Version, recorded.Name)
		}
	}

	current := 0
	for _, step := range steps {
		if recorded, ok := known[step.Version]; ok {
			if recorded.Checksum != step.Checksum {
				return 0, apperr.New(apperr.CodeFailedPrecondition,
					"migration %d (%s) changed after it was applied; refusing to continue",
					step.Version, step.Name)
			}
			current = step.Version
			continue
		}
		if err := d.applyStep(ctx, step); err != nil {
			return 0, err
		}
		current = step.Version
	}
	return current, nil
}

func (d *DB) applyStep(ctx context.Context, step migrations.Step) error {
	conn, err := d.handle.Conn(ctx)
	if err != nil {
		return classify(err, "cannot acquire migration connection")
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return classify(err, "cannot begin migration transaction")
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		}
	}()

	if _, err := conn.ExecContext(ctx, step.Script); err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "migration %d (%s) failed", step.Version, step.Name)
	}
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)",
		step.Version, step.Name, step.Checksum, time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "cannot record migration %d", step.Version)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return classify(err, "cannot commit migration transaction")
	}
	committed = true
	return nil
}

// AppliedSteps reads the ledger in ascending order.
func (d *DB) AppliedSteps(ctx context.Context) ([]AppliedStep, error) {
	rows, err := d.handle.QueryContext(ctx,
		"SELECT version, name, checksum, applied_at FROM schema_migrations ORDER BY version")
	if err != nil {
		if isMissingTable(err) {
			return nil, nil
		}
		return nil, classify(err, "cannot read migration ledger")
	}
	defer func() { _ = rows.Close() }()

	var out []AppliedStep
	for rows.Next() {
		var (
			step      AppliedStep
			appliedAt string
		)
		if err := rows.Scan(&step.Version, &step.Name, &step.Checksum, &appliedAt); err != nil {
			return nil, classify(err, "cannot scan migration ledger")
		}
		parsed, parseErr := time.Parse(time.RFC3339Nano, appliedAt)
		if parseErr != nil {
			return nil, apperr.Wrap(parseErr, apperr.CodeInternal, "migration ledger holds an invalid timestamp")
		}
		step.AppliedAt = parsed
		out = append(out, step)
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err, "cannot iterate migration ledger")
	}
	return out, nil
}

// SchemaVersion reports the highest applied migration version.
func (d *DB) SchemaVersion(ctx context.Context) (int, error) {
	var version sql.NullInt64
	err := d.handle.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&version)
	if err != nil {
		if isMissingTable(err) {
			return 0, nil
		}
		return 0, classify(err, "cannot read schema version")
	}
	if !version.Valid {
		return 0, nil
	}
	return int(version.Int64), nil
}

func isMissingTable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, sql.ErrNoRows) {
		return true
	}
	return strings.Contains(err.Error(), "no such table")
}
