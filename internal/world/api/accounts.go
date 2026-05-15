// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"database/sql"

	"github.com/vaelen/wintermute/internal/auth"
)

// SetAccountLevel updates the access level of the named account.
// level must be one of auth.AccessPlayer / AccessBuilder / AccessAdmin.
func (a *API) SetAccountLevel(ctx context.Context, username, level string) error {
	if a.Accts == nil {
		return errorf(CodeInternal, "account store not wired")
	}
	switch auth.AccessLevel(level) {
	case auth.AccessAdmin, auth.AccessBuilder, auth.AccessPlayer:
	default:
		return errorf(CodeInvalidArgument, "unknown access level %q", level)
	}
	if err := a.DB.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE accounts SET access_level = ? WHERE username = ? COLLATE NOCASE`,
			level, username,
		)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return notFound("account", username)
		}
		return nil
	}); err != nil {
		var apiErr *Error
		if asErr(err, &apiErr) {
			return apiErr
		}
		return errorf(CodeInternal, "set account level: %v", err)
	}
	return nil
}

// BootAccount disconnects every active session belonging to the named
// account. Optional msg is sent to each session immediately before
// detach. Returns the number of sessions detached, or zero (no error) if
// the account exists but has no active sessions.
func (a *API) BootAccount(ctx context.Context, username, msg string) (int, error) {
	if a.Accts == nil {
		return 0, errorf(CodeInternal, "account store not wired")
	}
	var accountID int64
	err := a.DB.Read().QueryRowContext(ctx,
		`SELECT id FROM accounts WHERE username = ? COLLATE NOCASE`,
		username,
	).Scan(&accountID)
	if err != nil {
		return 0, notFound("account", username)
	}
	return a.World.BootByAccount(accountID, msg), nil
}

// asErr is a generic-free equivalent of errors.As for *Error, kept
// internal so the rest of the package can use the canonical errors.As
// when needed.
func asErr(err error, target **Error) bool {
	for cur := err; cur != nil; {
		if e, ok := cur.(*Error); ok {
			*target = e
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := cur.(unwrapper)
		if !ok {
			break
		}
		cur = u.Unwrap()
	}
	return false
}
