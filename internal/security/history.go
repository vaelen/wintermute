// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package security

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// UsernameHistoryRow is a single row in username_history.
type UsernameHistoryRow struct {
	OldUsername string
	AccountID   *int64
	RenamedAt   int64
	RenamedTo   string
	RenamedBy   *int64
}

// recordHistory inserts (or refreshes) a username_history row inside tx.
// Uses ON CONFLICT(old_username) DO UPDATE so a name that has been
// rotated through the same account more than once collapses to one row
// with the most recent rename timestamp / target / actor.
func recordHistory(ctx context.Context, tx *sql.Tx, oldName string, accountID int64, newName string, renamedBy *int64) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO username_history (old_username, account_id, renamed_at, renamed_to, renamed_by)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(old_username) DO UPDATE SET
		     account_id = excluded.account_id,
		     renamed_at = excluded.renamed_at,
		     renamed_to = excluded.renamed_to,
		     renamed_by = excluded.renamed_by`,
		oldName, accountID, time.Now().Unix(), newName, renamedBy,
	)
	if err != nil {
		return fmt.Errorf("security: record history %q: %w", oldName, err)
	}
	return nil
}

// historyOwner returns the account_id stored against name, or 0 if no
// row exists. The boolean reports whether a row was found at all.
func historyOwner(ctx context.Context, db *sql.DB, name string) (int64, bool, error) {
	var owner sql.NullInt64
	err := db.QueryRowContext(ctx,
		`SELECT account_id FROM username_history WHERE old_username = ? COLLATE NOCASE`,
		name,
	).Scan(&owner)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("security: history owner %q: %w", name, err)
	}
	if !owner.Valid {
		return 0, true, nil
	}
	return owner.Int64, true, nil
}

// isReserved reports whether name is reserved by username_history for
// any account other than forAccount. forAccount = 0 means "any reservation
// counts" — the new-account-create path.
func isReserved(ctx context.Context, db *sql.DB, name string, forAccount int64) (bool, error) {
	owner, present, err := historyOwner(ctx, db, name)
	if err != nil {
		return false, err
	}
	if !present {
		return false, nil
	}
	if forAccount > 0 && owner == forAccount {
		return false, nil
	}
	return true, nil
}

// listHistory returns username_history rows for the given account. If
// accountID == 0, every row is returned.
func listHistory(ctx context.Context, db *sql.DB, accountID int64) ([]UsernameHistoryRow, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if accountID > 0 {
		rows, err = db.QueryContext(ctx,
			`SELECT old_username, account_id, renamed_at, renamed_to, renamed_by
			   FROM username_history
			   WHERE account_id = ?
			   ORDER BY renamed_at DESC`,
			accountID,
		)
	} else {
		rows, err = db.QueryContext(ctx,
			`SELECT old_username, account_id, renamed_at, renamed_to, renamed_by
			   FROM username_history
			   ORDER BY renamed_at DESC`,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("security: list history: %w", err)
	}
	defer rows.Close()
	var out []UsernameHistoryRow
	for rows.Next() {
		var (
			row    UsernameHistoryRow
			accID  sql.NullInt64
			byID   sql.NullInt64
		)
		if err := rows.Scan(&row.OldUsername, &accID, &row.RenamedAt, &row.RenamedTo, &byID); err != nil {
			return nil, fmt.Errorf("security: scan history: %w", err)
		}
		if accID.Valid {
			v := accID.Int64
			row.AccountID = &v
		}
		if byID.Valid {
			v := byID.Int64
			row.RenamedBy = &v
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// releaseHistory removes a username_history row, freeing the name for
// re-use. Removing a non-existent row is a no-op.
func releaseHistory(ctx context.Context, tx *sql.Tx, name string) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM username_history WHERE old_username = ? COLLATE NOCASE`,
		name,
	); err != nil {
		return fmt.Errorf("security: release history %q: %w", name, err)
	}
	return nil
}
