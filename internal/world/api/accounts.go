// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"database/sql"
	"errors"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/security"
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

// RenameAccount changes accounts.username from currentName to newName.
// renamedBy is the actor's account id (0 means "unknown actor"). Wraps
// auth.Store.Rename so the rename + history insert run in one
// transaction and live sessions are booted on success.
//
// Returns api.Error("not_found") when currentName is unknown,
// "duplicate_slug" when newName is taken, "permission_denied" for
// disallowed names, and "invalid_argument" for everything else
// (reserved-name, syntax errors).
//
// Callers that have a stable account ID in hand should prefer
// RenameAccountByID — the name-based path re-resolves the account
// and has a TOCTOU window when two admins act concurrently.
func (a *API) RenameAccount(ctx context.Context, currentName, newName string, renamedBy int64) error {
	if a.Accts == nil {
		return errorf(CodeInternal, "account store not wired")
	}
	target, err := a.Accts.GetByUsername(ctx, currentName)
	if err != nil {
		if errors.Is(err, auth.ErrAccountNotFound) {
			return notFound("account", currentName)
		}
		return errorf(CodeInternal, "rename lookup: %v", err)
	}
	return a.renameByID(ctx, target.ID, currentName, newName, renamedBy)
}

// RenameAccountByID is the TOCTOU-safe form for callers that already
// hold a stable account ID (e.g. the admin menu). The currentName is
// only used to render not-found errors when the row has been deleted
// since the menu state was constructed; the rename itself is keyed by
// ID end-to-end.
func (a *API) RenameAccountByID(ctx context.Context, accountID int64, newName string, renamedBy int64) error {
	if a.Accts == nil {
		return errorf(CodeInternal, "account store not wired")
	}
	cur, err := a.Accts.GetByID(ctx, accountID)
	if err != nil {
		if errors.Is(err, auth.ErrAccountNotFound) {
			return notFound("account", "")
		}
		return errorf(CodeInternal, "rename lookup: %v", err)
	}
	return a.renameByID(ctx, accountID, cur.Username, newName, renamedBy)
}

// renameByID is the shared rename body that translates auth-layer
// sentinel errors into the API's stable codes. currentName is purely
// for not-found context strings.
func (a *API) renameByID(ctx context.Context, id int64, currentName, newName string, renamedBy int64) error {
	var actor *int64
	if renamedBy > 0 {
		actor = &renamedBy
	}
	switch err := a.Accts.Rename(ctx, id, newName, actor); {
	case err == nil:
		return nil
	case errors.Is(err, auth.ErrUsernameTaken):
		return duplicateSlug(newName)
	case errors.Is(err, auth.ErrUsernameDisallowed),
		errors.Is(err, security.ErrUsernameDisallowed):
		return errorf(CodePermissionDenied, "username disallowed: %s", newName)
	case errors.Is(err, auth.ErrUsernameReserved),
		errors.Is(err, security.ErrUsernameReserved):
		return errorf(CodeInvalidArgument, "username reserved: %s", newName)
	case errors.Is(err, auth.ErrInvalidUsername):
		return errorf(CodeInvalidArgument, "invalid username: %s", newName)
	case errors.Is(err, auth.ErrAccountNotFound):
		return notFound("account", currentName)
	default:
		return errorf(CodeInternal, "rename: %v", err)
	}
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
