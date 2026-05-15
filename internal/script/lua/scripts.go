// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package lua

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/vaelen/wintermute/internal/store"
)

// Script is a row from the scripts table.
type Script struct {
	ID        int64
	Slug      string
	OwnerID   int64
	Source    string
	UpdatedAt int64
}

// ScriptStore wraps the scripts table with the load/save/list operations
// used by the @edit, @run, @script, and @reload-scripts admin commands
// and by the boot-time init runner.
type ScriptStore struct {
	db *store.DB
}

// NewScriptStore constructs a ScriptStore.
func NewScriptStore(db *store.DB) *ScriptStore {
	return &ScriptStore{db: db}
}

// Get reads a single script by slug.
func (s *ScriptStore) Get(ctx context.Context, slug string) (Script, error) {
	var sc Script
	err := s.db.Read().QueryRowContext(ctx,
		`SELECT id, slug, owner_id, source, updated_at
		   FROM scripts WHERE slug = ?`, slug,
	).Scan(&sc.ID, &sc.Slug, &sc.OwnerID, &sc.Source, &sc.UpdatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return Script{}, fmt.Errorf("script: not_found: %s", slug)
		}
		return Script{}, err
	}
	return sc, nil
}

// Save upserts a script by slug. The owner_id is taken from spec and
// recorded on first save; subsequent saves keep the original owner so
// audit trails survive edits by other admins.
func (s *ScriptStore) Save(ctx context.Context, slug string, ownerID int64, source string) error {
	if slug == "" {
		return fmt.Errorf("script: slug required")
	}
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO scripts(slug, owner_id, source, updated_at)
			   VALUES (?, ?, ?, strftime('%s','now'))
			   ON CONFLICT(slug) DO UPDATE
			      SET source     = excluded.source,
			          updated_at = excluded.updated_at`,
			slug, ownerID, source,
		)
		return err
	})
}

// Delete removes a script by slug.
func (s *ScriptStore) Delete(ctx context.Context, slug string) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM scripts WHERE slug = ?`, slug)
		return err
	})
}

// List returns every stored script ordered by slug.
func (s *ScriptStore) List(ctx context.Context) ([]Script, error) {
	rows, err := s.db.Read().QueryContext(ctx,
		`SELECT id, slug, owner_id, source, updated_at
		   FROM scripts ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Script
	for rows.Next() {
		var sc Script
		if err := rows.Scan(&sc.ID, &sc.Slug, &sc.OwnerID, &sc.Source, &sc.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// ListInit returns every script with a slug starting with "init." in
// slug order. The boot path runs these in sequence after world load.
func (s *ScriptStore) ListInit(ctx context.Context) ([]Script, error) {
	rows, err := s.db.Read().QueryContext(ctx,
		`SELECT id, slug, owner_id, source, updated_at
		   FROM scripts
		  WHERE slug LIKE 'init.%'
		  ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Script
	for rows.Next() {
		var sc Script
		if err := rows.Scan(&sc.ID, &sc.Slug, &sc.OwnerID, &sc.Source, &sc.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// Run executes a Lua source string against the pool's VMs and returns
// any error the script raised. Errors are wrapped via luaError so
// callers see the standard Source=Lua marker.
func (p *Pool) Run(ctx context.Context, source string) error {
	L := p.Get()
	defer p.Put(L)
	// Honour ctx cancellation for long-running scripts.
	L.SetContext(ctx)
	defer L.RemoveContext()
	if err := L.DoString(source); err != nil {
		return luaError(err)
	}
	return nil
}

// RunInit executes every init.* script in slug order against the pool.
// Errors abort the run and are returned immediately; subsequent
// init scripts will only run on the next boot or after an
// @reload-scripts that succeeds end-to-end. Successful runs are logged
// via the logger callback.
func RunInit(ctx context.Context, pool *Pool, scripts *ScriptStore, logf func(slug string, err error)) error {
	rows, err := scripts.ListInit(ctx)
	if err != nil {
		return fmt.Errorf("script: list init: %w", err)
	}
	for _, sc := range rows {
		if !strings.HasPrefix(sc.Slug, "init.") {
			continue
		}
		err := pool.Run(ctx, sc.Source)
		if logf != nil {
			logf(sc.Slug, err)
		}
		if err != nil {
			return fmt.Errorf("script %s: %w", sc.Slug, err)
		}
	}
	return nil
}
