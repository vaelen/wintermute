// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

// Package networks manages the ftn_networks table: configured FTN
// networks this server is a member of, the auto-created `local`
// network, and lookups by slug or domain. Reads go direct via
// store.Read(); mutations go through the store's single-writer
// goroutine.
package networks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/vaelen/wintermute/internal/config"
	"github.com/vaelen/wintermute/internal/store"
)

// Network is a row of ftn_networks.
type Network struct {
	ID        int64
	Slug      string
	Name      string
	Domain    string
	OurAddr   string
	IsDefault bool
}

// OrigAddr returns the full FSC-0089 domain-form address used in
// FTS-0009 MSGID lines for messages we originate on this network.
func (n Network) OrigAddr() string {
	return n.OurAddr + "@" + n.Domain
}

const (
	localSlug   = "local"
	localName   = "Local"
	localDomain = "local"
	localAddr   = "255:255/255.0"
)

// Bootstrap reconciles the ftn_networks table with the operator's
// declared [[ftn.network]] blocks: upsert each declared network, then
// ensure a `local` network exists (auto-creating it if absent) and
// that exactly one network is marked default. If no declared network
// is default, `local` becomes the default.
//
// Bootstrap is idempotent — safe to call on every startup.
func Bootstrap(ctx context.Context, db *store.DB, declared []config.FTNNetwork, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}

	hasLocal := false
	explicitDefault := ""
	for _, n := range declared {
		if n.Slug == localSlug {
			hasLocal = true
		}
		if n.Default {
			explicitDefault = n.Slug
		}
	}

	return db.Write(ctx, func(tx *sql.Tx) error {
		// Clear all defaults first so we can apply a fresh one without
		// tripping the partial unique index.
		if _, err := tx.ExecContext(ctx, `UPDATE ftn_networks SET is_default = 0`); err != nil {
			return fmt.Errorf("clear defaults: %w", err)
		}

		for _, n := range declared {
			if err := upsert(ctx, tx, n.Slug, n.Name, n.Domain, n.Addr); err != nil {
				return err
			}
		}

		if !hasLocal {
			if err := ensureLocal(ctx, tx); err != nil {
				return err
			}
		}

		defaultSlug := explicitDefault
		if defaultSlug == "" {
			defaultSlug = localSlug
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE ftn_networks SET is_default = 1 WHERE slug = ?`, defaultSlug)
		if err != nil {
			return fmt.Errorf("set default %s: %w", defaultSlug, err)
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return fmt.Errorf("default network %q not present after bootstrap", defaultSlug)
		}
		logger.Info("ftn networks bootstrapped",
			"declared", len(declared), "default", defaultSlug, "auto_local", !hasLocal)
		return nil
	})
}

func upsert(ctx context.Context, tx *sql.Tx, slug, name, domain, addr string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO ftn_networks(slug, name, domain, our_addr, is_default)
		VALUES (?, ?, ?, ?, 0)
		ON CONFLICT(slug) DO UPDATE SET
			name     = excluded.name,
			domain   = excluded.domain,
			our_addr = excluded.our_addr
	`, slug, name, domain, addr)
	if err != nil {
		return fmt.Errorf("upsert %s: %w", slug, err)
	}
	return nil
}

func ensureLocal(ctx context.Context, tx *sql.Tx) error {
	var exists int
	err := tx.QueryRowContext(ctx,
		`SELECT 1 FROM ftn_networks WHERE slug = ?`, localSlug).Scan(&exists)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check local: %w", err)
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO ftn_networks(slug, name, domain, our_addr, is_default)
		 VALUES (?, ?, ?, ?, 0)`,
		localSlug, localName, localDomain, localAddr)
	if err != nil {
		return fmt.Errorf("create local: %w", err)
	}
	return nil
}

// List returns every network row, ordered by id (insertion order).
func List(ctx context.Context, db *store.DB) ([]Network, error) {
	rows, err := db.Read().QueryContext(ctx,
		`SELECT id, slug, name, domain, our_addr, is_default
		 FROM ftn_networks ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Network
	for rows.Next() {
		var n Network
		var d int
		if err := rows.Scan(&n.ID, &n.Slug, &n.Name, &n.Domain, &n.OurAddr, &d); err != nil {
			return nil, err
		}
		n.IsDefault = d == 1
		out = append(out, n)
	}
	return out, rows.Err()
}

// Get returns the network with the given slug.
func Get(ctx context.Context, db *store.DB, slug string) (Network, error) {
	var n Network
	var d int
	err := db.Read().QueryRowContext(ctx,
		`SELECT id, slug, name, domain, our_addr, is_default
		 FROM ftn_networks WHERE slug = ?`, slug).
		Scan(&n.ID, &n.Slug, &n.Name, &n.Domain, &n.OurAddr, &d)
	if err != nil {
		return Network{}, fmt.Errorf("ftn networks: get %q: %w", slug, err)
	}
	n.IsDefault = d == 1
	return n, nil
}

// SetDefault marks the network with the given slug as the default,
// clearing the flag on every other row. The clear+set happens in a
// single transaction so there is never a moment with two defaults.
// Returns an error if no network matches slug.
func SetDefault(ctx context.Context, db *store.DB, slug string) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		var id int64
		if err := tx.QueryRowContext(ctx,
			`SELECT id FROM ftn_networks WHERE slug = ?`, slug).Scan(&id); err != nil {
			return fmt.Errorf("ftn networks: SetDefault %q: %w", slug, err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE ftn_networks SET is_default = 0`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE ftn_networks SET is_default = 1 WHERE id = ?`, id)
		return err
	})
}

// Default returns the network row flagged is_default = 1.
func Default(ctx context.Context, db *store.DB) (Network, error) {
	var n Network
	var d int
	err := db.Read().QueryRowContext(ctx,
		`SELECT id, slug, name, domain, our_addr, is_default
		 FROM ftn_networks WHERE is_default = 1`).
		Scan(&n.ID, &n.Slug, &n.Name, &n.Domain, &n.OurAddr, &d)
	if err != nil {
		return Network{}, fmt.Errorf("ftn networks: default: %w", err)
	}
	n.IsDefault = d == 1
	return n, nil
}
