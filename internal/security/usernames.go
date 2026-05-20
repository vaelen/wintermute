// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package security

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vaelen/wintermute/internal/auth"
)

// ReservedAccountCreateKeyword is the username the login flow treats as
// the account-create sentinel. It must never be added to the disallowed
// list — that would lock new-account creation out of the engine.
const ReservedAccountCreateKeyword = "new"

// Sentinel errors returned by the username DAO and the policy layer.
// The disallow / reserved sentinels are *aliases* for the auth package's
// equivalents so a single `errors.Is` check covers both layers.
var (
	// ErrUsernameDisallowed is returned by CheckAvailable when name
	// matches an entry in the disallowed_usernames table.
	ErrUsernameDisallowed = auth.ErrUsernameDisallowed
	// ErrUsernameReserved is returned by CheckAvailable when name
	// matches an entry in username_history (a previously-used name).
	ErrUsernameReserved = auth.ErrUsernameReserved
	// ErrDisallowReservedKeyword is returned by DisallowUsername when
	// the caller tries to add the account-create sentinel.
	ErrDisallowReservedKeyword = errors.New("security: cannot disallow account-create keyword")
	// ErrDisallowExistingAccount is returned by DisallowUsername when
	// the requested name matches a current row in accounts.
	ErrDisallowExistingAccount = errors.New("security: name collides with an existing account")
	// ErrAlreadyDisallowed is returned by DisallowUsername when the name
	// is already in the disallowed_usernames table. Lets the admin UI
	// render a clean "already on the list" message instead of falling
	// through to a raw SQLite UNIQUE-constraint error.
	ErrAlreadyDisallowed = errors.New("security: username already disallowed")
)

// DisallowedUsername is a single row in the disallowed_usernames table.
// Reason is operator metadata; AddedAt is unix seconds.
type DisallowedUsername struct {
	Username string
	Reason   string
	AddedAt  int64
	AddedBy  *int64
}

// disallowUsername inserts a name into disallowed_usernames inside tx.
// Pre-conditions (sentinel keyword, existing-account collision) are
// checked by the caller; this is the raw insert helper. Uses
// INSERT OR IGNORE + RowsAffected so a duplicate add returns a typed
// ErrAlreadyDisallowed instead of a raw modernc.org/sqlite UNIQUE
// constraint error (CLAUDE.md sentinel-error rule).
func disallowUsername(ctx context.Context, tx *sql.Tx, name, reason string, addedBy *int64) error {
	res, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO disallowed_usernames (username, reason, added_at, added_by)
		 VALUES (?, ?, ?, ?)`,
		name, reason, time.Now().Unix(), addedBy,
	)
	if err != nil {
		return fmt.Errorf("security: disallow %q: %w", name, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("security: disallow %q rows-affected: %w", name, err)
	}
	if n == 0 {
		return ErrAlreadyDisallowed
	}
	return nil
}

// allowUsername removes a name from disallowed_usernames inside tx.
// Removing a non-existent row is a no-op (zero rows affected).
func allowUsername(ctx context.Context, tx *sql.Tx, name string) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM disallowed_usernames WHERE username = ? COLLATE NOCASE`,
		name,
	); err != nil {
		return fmt.Errorf("security: allow %q: %w", name, err)
	}
	return nil
}

// listDisallowed returns every row in disallowed_usernames ordered by name.
func listDisallowed(ctx context.Context, db *sql.DB) ([]DisallowedUsername, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT username, reason, added_at, added_by
		   FROM disallowed_usernames
		   ORDER BY username ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("security: list disallowed: %w", err)
	}
	defer rows.Close()
	var out []DisallowedUsername
	for rows.Next() {
		var row DisallowedUsername
		var added sql.NullInt64
		if err := rows.Scan(&row.Username, &row.Reason, &row.AddedAt, &added); err != nil {
			return nil, fmt.Errorf("security: scan disallowed: %w", err)
		}
		if added.Valid {
			v := added.Int64
			row.AddedBy = &v
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// isDisallowed reports whether name is present in disallowed_usernames.
// Case-insensitive (COLLATE NOCASE on the column).
func isDisallowed(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var one int
	err := db.QueryRowContext(ctx,
		`SELECT 1 FROM disallowed_usernames WHERE username = ? COLLATE NOCASE`,
		name,
	).Scan(&one)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return false, fmt.Errorf("security: lookup disallowed %q: %w", name, err)
}

// accountExists reports whether an account row with the given username
// exists. Case-insensitive — mirrors the accounts.username COLLATE NOCASE
// constraint.
func accountExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var one int
	err := db.QueryRowContext(ctx,
		`SELECT 1 FROM accounts WHERE username = ? COLLATE NOCASE`,
		name,
	).Scan(&one)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return false, fmt.Errorf("security: lookup account %q: %w", name, err)
}

// isReservedKeyword reports whether name matches the account-create
// sentinel. Case-insensitive.
func isReservedKeyword(name string) bool {
	return strings.EqualFold(name, ReservedAccountCreateKeyword)
}
