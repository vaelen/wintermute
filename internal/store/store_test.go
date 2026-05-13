// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

func tempDB(t *testing.T) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	d, err := Open(context.Background(), path, logger)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestOpenAppliesMigrations(t *testing.T) {
	d := tempDB(t)
	var n int
	if err := d.Read().QueryRow(`SELECT COUNT(*) FROM schema_version`).Scan(&n); err != nil {
		t.Fatalf("query schema_version: %v", err)
	}
	if n < 1 {
		t.Errorf("schema_version row count = %d, want at least 1", n)
	}
	if _, err := d.Read().Exec(`SELECT 1 FROM accounts`); err != nil {
		t.Errorf("accounts table should exist after migrate: %v", err)
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for i := 0; i < 3; i++ {
		d, err := Open(context.Background(), path, logger)
		if err != nil {
			t.Fatalf("Open #%d: %v", i, err)
		}
		if err := d.Close(); err != nil {
			t.Fatalf("Close #%d: %v", i, err)
		}
	}
}

func TestWriteSerializes(t *testing.T) {
	d := tempDB(t)
	ctx := context.Background()
	// Insert via writer.
	err := d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO accounts(username, password_hash, access_level, created_at)
			 VALUES (?, ?, ?, strftime('%s','now'))`,
			"alice", "fakehash", "player",
		)
		return err
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	// Read back.
	var got string
	if err := d.Read().QueryRow(`SELECT username FROM accounts WHERE username = 'alice'`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != "alice" {
		t.Errorf("got %q, want alice", got)
	}
}

func TestWriteRollbackOnError(t *testing.T) {
	d := tempDB(t)
	ctx := context.Background()
	want := "deliberate failure"
	err := d.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(
			`INSERT INTO accounts(username, password_hash, access_level, created_at)
			 VALUES (?, ?, ?, strftime('%s','now'))`,
			"bob", "h", "player",
		); err != nil {
			return err
		}
		return errString(want)
	})
	if err == nil || err.Error() != want {
		t.Fatalf("Write err = %v, want %q", err, want)
	}
	var n int
	if err := d.Read().QueryRow(`SELECT COUNT(*) FROM accounts WHERE username = 'bob'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("bob should have been rolled back; got %d rows", n)
	}
}

type errString string

func (e errString) Error() string { return string(e) }
