// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"database/sql"
	"testing"
)

func TestScriptsSchema(t *testing.T) {
	d := tempDB(t)

	var name string
	if err := d.Read().QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='scripts'`,
	).Scan(&name); err != nil {
		t.Fatalf("scripts table missing: %v", err)
	}

	var indexName string
	if err := d.Read().QueryRow(
		`SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='scripts' AND name='idx_scripts_owner'`,
	).Scan(&indexName); err != nil {
		t.Errorf("idx_scripts_owner index missing: %v", err)
	}
}

func TestScriptsInsertRequiresOwner(t *testing.T) {
	d := tempDB(t)
	ctx := context.Background()

	// Create an account to own the script.
	var accountID int64
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO accounts(username, password_hash, access_level, created_at)
			 VALUES (?, ?, 'admin', strftime('%s','now'))`,
			"admin", "fakehash",
		)
		if err != nil {
			return err
		}
		accountID, err = res.LastInsertId()
		return err
	}); err != nil {
		t.Fatalf("create account: %v", err)
	}

	if err := d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO scripts(slug, owner_id, source, updated_at)
			 VALUES (?, ?, ?, strftime('%s','now'))`,
			"init.0001-test", accountID, "wintermute.system.log('hi')",
		)
		return err
	}); err != nil {
		t.Fatalf("insert script: %v", err)
	}

	var got string
	if err := d.Read().QueryRow(
		`SELECT source FROM scripts WHERE slug='init.0001-test'`,
	).Scan(&got); err != nil {
		t.Fatalf("read script: %v", err)
	}
	if got != "wintermute.system.log('hi')" {
		t.Errorf("source = %q", got)
	}
}
