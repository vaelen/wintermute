// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package security

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"time"
)

// IPDenial is one row in ip_denials as returned by ListDenials.
type IPDenial struct {
	IP        netip.Addr
	ExpiresAt *time.Time
	AddedAt   time.Time
	AddedBy   *int64
	Reason    string
	Automatic bool
}

// Kind reports whether the denial is temporary or permanent.
func (d IPDenial) Permanent() bool { return d.ExpiresAt == nil }

// upsertIPDenial inserts or replaces an ip_denials row inside tx.
// expiresAt = nil means permanent.
func upsertIPDenial(ctx context.Context, tx *sql.Tx, addr netip.Addr, expiresAt *int64, addedBy *int64, reason string, automatic bool) error {
	auto := 0
	if automatic {
		auto = 1
	}
	var exp any
	if expiresAt != nil {
		exp = *expiresAt
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO ip_denials (ip, expires_at, added_at, added_by, reason, automatic)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(ip) DO UPDATE SET
		     expires_at = excluded.expires_at,
		     added_at   = excluded.added_at,
		     added_by   = excluded.added_by,
		     reason     = excluded.reason,
		     automatic  = excluded.automatic`,
		addr.String(), exp, time.Now().Unix(), addedBy, reason, auto,
	); err != nil {
		return fmt.Errorf("security: upsert ip_denials: %w", err)
	}
	return nil
}

// deleteIPDenial removes the row for addr.
func deleteIPDenial(ctx context.Context, tx *sql.Tx, addr netip.Addr) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM ip_denials WHERE ip = ?`,
		addr.String(),
	); err != nil {
		return fmt.Errorf("security: delete ip_denials: %w", err)
	}
	return nil
}

// flushTempIPDenials removes every temporary row (expires_at IS NOT NULL).
// Returns the number of rows deleted.
func flushTempIPDenials(ctx context.Context, tx *sql.Tx) (int64, error) {
	res, err := tx.ExecContext(ctx,
		`DELETE FROM ip_denials WHERE expires_at IS NOT NULL`,
	)
	if err != nil {
		return 0, fmt.Errorf("security: flush temp ip_denials: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("security: flush temp rows-affected: %w", err)
	}
	return n, nil
}

// hydrateIPDenials loads every row in ip_denials. Expired temporary rows
// are silently skipped (caller is expected to also purge them via
// purgeExpiredIPDenials).
func hydrateIPDenials(ctx context.Context, db *sql.DB, now time.Time) ([]IPDenial, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT ip, expires_at, added_at, added_by, reason, automatic
		   FROM ip_denials`,
	)
	if err != nil {
		return nil, fmt.Errorf("security: hydrate ip_denials: %w", err)
	}
	defer rows.Close()
	deadline := now.Unix()
	var out []IPDenial
	for rows.Next() {
		var (
			ipStr   string
			expires sql.NullInt64
			added   int64
			by      sql.NullInt64
			reason  string
			auto    int64
		)
		if err := rows.Scan(&ipStr, &expires, &added, &by, &reason, &auto); err != nil {
			return nil, fmt.Errorf("security: scan ip_denials: %w", err)
		}
		addr, err := netip.ParseAddr(ipStr)
		if err != nil {
			// Skip malformed rows defensively; admins can prune them.
			continue
		}
		row := IPDenial{
			IP:        addr,
			AddedAt:   time.Unix(added, 0).UTC(),
			Reason:    reason,
			Automatic: auto != 0,
		}
		if expires.Valid {
			if deadline >= expires.Int64 {
				continue
			}
			t := time.Unix(expires.Int64, 0).UTC()
			row.ExpiresAt = &t
		}
		if by.Valid {
			v := by.Int64
			row.AddedBy = &v
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("security: iterate ip_denials: %w", err)
	}
	return out, nil
}

// purgeExpiredIPDenials deletes every temporary row whose expires_at is
// at or before now. Returns the rows removed.
func purgeExpiredIPDenials(ctx context.Context, tx *sql.Tx, now time.Time) ([]netip.Addr, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT ip FROM ip_denials WHERE expires_at IS NOT NULL AND expires_at <= ?`,
		now.Unix(),
	)
	if err != nil {
		return nil, fmt.Errorf("security: scan expired ip_denials: %w", err)
	}
	var expired []netip.Addr
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			rows.Close()
			return nil, fmt.Errorf("security: scan expired row: %w", err)
		}
		if addr, perr := netip.ParseAddr(s); perr == nil {
			expired = append(expired, addr)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("security: iterate expired rows: %w", err)
	}
	rows.Close()
	if len(expired) == 0 {
		return nil, nil
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM ip_denials WHERE expires_at IS NOT NULL AND expires_at <= ?`,
		now.Unix(),
	); err != nil {
		return nil, fmt.Errorf("security: delete expired ip_denials: %w", err)
	}
	return expired, nil
}

// ErrIPNotFound is returned by RemoveDeny when the address is unknown.
// Exported so admin renderers can errors.Is it and print a clean line.
var ErrIPNotFound = errors.New("security: ip not in deny list")
