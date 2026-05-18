// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	ftnnetworks "github.com/vaelen/wintermute/internal/ftn/networks"
)

// ListNetworks returns every configured FTN network row in insertion
// order.
func (a *API) ListNetworks(ctx context.Context) ([]ftnnetworks.Network, error) {
	if a.DB == nil {
		return nil, errorf(CodeInternal, "db not configured")
	}
	out, err := ftnnetworks.List(ctx, a.DB)
	if err != nil {
		return nil, errorf(CodeInternal, "list networks: %v", err)
	}
	return out, nil
}

// GetNetwork returns the named network row, or not_found if absent.
func (a *API) GetNetwork(ctx context.Context, slug string) (ftnnetworks.Network, error) {
	if a.DB == nil {
		return ftnnetworks.Network{}, errorf(CodeInternal, "db not configured")
	}
	if slug == "" {
		return ftnnetworks.Network{}, errorf(CodeInvalidArgument, "slug is required")
	}
	n, err := ftnnetworks.Get(ctx, a.DB, slug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ftnnetworks.Network{}, notFound("ftn_network", slug)
		}
		return ftnnetworks.Network{}, errorf(CodeInternal, "%v", err)
	}
	return n, nil
}

// SetDefaultNetwork clears the current is_default = 1 row and sets the
// named network as the new default. Both steps run in a single writer
// transaction so the partial unique index on is_default is never
// violated. Returns not_found if the slug doesn't match a configured
// network.
//
// The list of configured networks remains owned by the TOML config —
// add / delete are deliberately not exposed; changing origaddr after
// MSGIDs have been issued would invalidate the FTS-0009 uniqueness
// contract for previously-issued serials.
func (a *API) SetDefaultNetwork(ctx context.Context, slug string) error {
	if a.DB == nil {
		return errorf(CodeInternal, "db not configured")
	}
	if slug == "" {
		return errorf(CodeInvalidArgument, "slug is required")
	}
	var affected int64
	err := a.DB.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE ftn_networks SET is_default = 0`); err != nil {
			return fmt.Errorf("clear defaults: %w", err)
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE ftn_networks SET is_default = 1 WHERE slug = ?`, slug)
		if err != nil {
			return fmt.Errorf("set default: %w", err)
		}
		affected, _ = res.RowsAffected()
		return nil
	})
	if err != nil {
		return errorf(CodeInternal, "%v", err)
	}
	if affected == 0 {
		return notFound("ftn_network", slug)
	}
	return nil
}
