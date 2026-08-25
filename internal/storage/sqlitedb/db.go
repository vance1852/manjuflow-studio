// Package sqlitedb opens the SQLite database, applies migrations and provides the
// transaction runner used by every service.
package sqlitedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
	"github.com/vance1852/manjuflow-studio/internal/repository"
)

// DB wraps the pooled handle and exposes the transaction boundary.
type DB struct {
	handle *sql.DB
	path   string
}

// Options configures the connection.
type Options struct {
	Path            string
	BusyTimeout     time.Duration
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

// DefaultOptions returns the production connection settings. SQLite serialises
// writers, so the pool stays small and the busy timeout absorbs short waits.
func DefaultOptions(path string) Options {
	return Options{
		Path:            path,
		BusyTimeout:     5 * time.Second,
		MaxOpenConns:    8,
		MaxIdleConns:    4,
		ConnMaxLifetime: time.Hour,
	}
}

// Open connects to SQLite, enables the pragmas the business rules rely on and
// verifies the connection.
func Open(ctx context.Context, opts Options) (*DB, error) {
	if strings.TrimSpace(opts.Path) == "" {
		return nil, apperr.New(apperr.CodeInvalidArgument, "database path must not be empty")
	}
	if opts.Path != ":memory:" {
		if dir := filepath.Dir(opts.Path); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, apperr.Wrap(err, apperr.CodeInternal, "cannot create database directory")
			}
		}
	}
	busyMillis := int(opts.BusyTimeout / time.Millisecond)
	if busyMillis <= 0 {
		busyMillis = 5000
	}
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(%d)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)",
		opts.Path, busyMillis)
	handle, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "cannot open sqlite database")
	}
	if opts.MaxOpenConns > 0 {
		handle.SetMaxOpenConns(opts.MaxOpenConns)
	}
	if opts.MaxIdleConns > 0 {
		handle.SetMaxIdleConns(opts.MaxIdleConns)
	}
	if opts.ConnMaxLifetime > 0 {
		handle.SetConnMaxLifetime(opts.ConnMaxLifetime)
	}
	if err := handle.PingContext(ctx); err != nil {
		_ = handle.Close()
		return nil, apperr.Wrap(err, apperr.CodeInternal, "cannot reach sqlite database")
	}
	return &DB{handle: handle, path: opts.Path}, nil
}

// Handle exposes the pooled connection for health checks.
func (d *DB) Handle() *sql.DB { return d.handle }

// Path reports the database file location.
func (d *DB) Path() string { return d.path }

// Close releases the pool.
func (d *DB) Close() error { return d.handle.Close() }

// Reader returns a non transactional querier for read only paths.
func (d *DB) Reader() repository.Querier { return d.handle }

// Ping verifies connectivity for the readiness probe.
func (d *DB) Ping(ctx context.Context) error {
	if err := d.handle.PingContext(ctx); err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "database is not reachable")
	}
	return nil
}

// InTx runs fn inside one immediate write transaction. The transaction rolls
// back on any error and on panic, and the context is honoured throughout.
func (d *DB) InTx(ctx context.Context, fn func(ctx context.Context, q repository.Querier) error) error {
	if err := apperr.FromContext(ctx); err != nil {
		return err
	}
	conn, err := d.handle.Conn(ctx)
	if err != nil {
		return classify(err, "cannot acquire database connection")
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return classify(err, "cannot begin transaction")
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		}
	}()

	if err := fn(ctx, connQuerier{conn: conn}); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return classify(err, "cannot commit transaction")
	}
	committed = true
	return nil
}

type connQuerier struct {
	conn *sql.Conn
}

func (c connQuerier) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return c.conn.ExecContext(ctx, query, args...)
}

func (c connQuerier) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return c.conn.QueryContext(ctx, query, args...)
}

func (c connQuerier) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return c.conn.QueryRowContext(ctx, query, args...)
}

func classify(err error, message string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return apperr.Wrap(err, apperr.CodeCanceled, "%s", message)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return apperr.Wrap(err, apperr.CodeDeadlineExceeded, "%s", message)
	}
	return apperr.Wrap(err, apperr.CodeInternal, "%s", message)
}
