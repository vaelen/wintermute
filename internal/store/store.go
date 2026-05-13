// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"sort"
	"strconv"

	_ "modernc.org/sqlite" // SQLite driver registration
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DB wraps an *sql.DB and serializes all writes through a single goroutine.
// Reads can be performed directly via Read().
type DB struct {
	db     *sql.DB
	write  chan writeRequest
	done   chan struct{}
	logger *slog.Logger
}

type writeRequest struct {
	ctx   context.Context
	fn    func(*sql.Tx) error
	reply chan error
}

// Open opens the SQLite database at path, applies any pending migrations,
// and starts the writer goroutine. The returned DB must be closed when no
// longer needed.
func Open(ctx context.Context, path string, logger *slog.Logger) (*DB, error) {
	if logger == nil {
		logger = slog.Default()
	}
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)",
		path,
	)
	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if err := sdb.PingContext(ctx); err != nil {
		_ = sdb.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	if err := migrate(ctx, sdb, logger); err != nil {
		_ = sdb.Close()
		return nil, err
	}
	d := &DB{
		db:     sdb,
		write:  make(chan writeRequest, 64),
		done:   make(chan struct{}),
		logger: logger,
	}
	go d.writer()
	return d, nil
}

// Read returns the underlying *sql.DB for read-only queries.
// Callers must not invoke write operations through this handle; use Write.
func (d *DB) Read() *sql.DB {
	return d.db
}

// Write submits fn to the single writer goroutine inside a transaction.
// fn must not start its own transaction. If fn returns an error the
// transaction is rolled back; otherwise it is committed.
func (d *DB) Write(ctx context.Context, fn func(*sql.Tx) error) error {
	reply := make(chan error, 1)
	req := writeRequest{ctx: ctx, fn: fn, reply: reply}
	select {
	case d.write <- req:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		// Best-effort cancel: the writer goroutine will see the context
		// cancelled and bail out of BeginTx; we just return early here.
		return ctx.Err()
	}
}

// Close shuts down the writer goroutine and closes the database.
func (d *DB) Close() error {
	close(d.write)
	<-d.done
	return d.db.Close()
}

func (d *DB) writer() {
	defer close(d.done)
	for req := range d.write {
		req.reply <- d.runWrite(req.ctx, req.fn)
	}
}

func (d *DB) runWrite(ctx context.Context, fn func(*sql.Tx) error) (rerr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer func() {
		if rerr != nil {
			_ = tx.Rollback()
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// ---------------------------------------------------------------------------
// migrations

var migrationFilenameRE = regexp.MustCompile(`^(\d+)_.+\.sql$`)

func migrate(ctx context.Context, db *sql.DB, logger *slog.Logger) error {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("store: read migrations: %w", err)
	}
	type mig struct {
		version int
		name    string
	}
	var migs []mig
	for _, e := range entries {
		m := migrationFilenameRE.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		v, err := strconv.Atoi(m[1])
		if err != nil {
			return fmt.Errorf("store: bad migration version %s: %w", e.Name(), err)
		}
		migs = append(migs, mig{version: v, name: e.Name()})
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].version < migs[j].version })

	applied, err := loadAppliedVersions(ctx, db)
	if err != nil {
		return err
	}
	for _, m := range migs {
		if applied[m.version] {
			continue
		}
		body, err := fs.ReadFile(migrationsFS, "migrations/"+m.name)
		if err != nil {
			return fmt.Errorf("store: read %s: %w", m.name, err)
		}
		logger.Info("applying migration", "version", m.version, "name", m.name)
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("store: begin %s: %w", m.name, err)
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: migration %s: %w", m.name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_version(version, applied_at) VALUES (?, strftime('%s','now'))`,
			m.version,
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: record %s: %w", m.name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: commit %s: %w", m.name, err)
		}
	}
	return nil
}

func loadAppliedVersions(ctx context.Context, db *sql.DB) (map[int]bool, error) {
	applied := map[int]bool{}
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_version`)
	if err != nil {
		// Likely the table does not exist yet (first ever boot). The
		// schema_version migration will create it.
		return applied, nil
	}
	defer rows.Close()
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	return applied, rows.Err()
}
