// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/mail"
)

// SendMailFromSystem delivers a message from the configured system handle
// to a local account. Returns the new mail row id.
func (a *API) SendMailFromSystem(ctx context.Context, toUsername, subject, body string) (int64, error) {
	if a.Mail == nil {
		return 0, errorf(CodeInternal, "mail service not configured")
	}
	id, err := a.Mail.SendFromSystem(ctx, toUsername, subject, body)
	if err != nil {
		return 0, translateMailErr("account", toUsername, err)
	}
	return id, nil
}

// BroadcastMailOpts narrows which accounts a broadcast targets.
type BroadcastMailOpts struct {
	// AccessLevel restricts delivery to accounts at that level.
	// Empty string means "every account regardless of level".
	AccessLevel string
}

// BroadcastMail sends the same message from the configured system handle
// to every account whose access level matches opts.AccessLevel (empty
// matches all). Returns the number of mail rows inserted plus, if any
// recipient failed twice in a row, an Error whose Details lists every
// twice-failed username — so one transient SQLite contention doesn't
// shred a server-wide announcement.
//
// Each row gets its own MSGID per the FTS-0009 uniqueness contract.
func (a *API) BroadcastMail(ctx context.Context, subject, body string, opts BroadcastMailOpts) (int, error) {
	if a.Mail == nil {
		return 0, errorf(CodeInternal, "mail service not configured")
	}
	if subject == "" {
		return 0, errorf(CodeInvalidArgument, "subject is empty")
	}
	if body == "" {
		return 0, errorf(CodeInvalidArgument, "body is empty")
	}
	if opts.AccessLevel != "" {
		switch auth.AccessLevel(opts.AccessLevel) {
		case auth.AccessAdmin, auth.AccessBuilder, auth.AccessPlayer:
		default:
			return 0, errorf(CodeInvalidArgument,
				"unknown access level %q", opts.AccessLevel)
		}
	}

	usernames, err := a.listAccountUsernames(ctx, opts.AccessLevel)
	if err != nil {
		return 0, err
	}

	send := func(targets []string) (sent int, failed []string) {
		for _, u := range targets {
			if _, err := a.Mail.SendFromSystem(ctx, u, subject, body); err != nil {
				failed = append(failed, u)
				continue
			}
			sent++
		}
		return sent, failed
	}

	sent, failed := send(usernames)
	if len(failed) == 0 {
		return sent, nil
	}
	// Second pass: retry once. Anything still failing is reported in a
	// single canonical Error so callers learn exactly which accounts
	// didn't receive the message.
	retried, stillFailed := send(failed)
	sent += retried
	if len(stillFailed) > 0 {
		return sent, errorf(CodeInternal,
			"broadcast failed for %d account(s): %s",
			len(stillFailed), strings.Join(stillFailed, ", "))
	}
	return sent, nil
}

// DeleteMailForUser removes a single mail row owned by the named account.
// Admin override: no permission check on the caller is performed at this
// layer (M5 already gated `wintermute.*` to admin tier).
func (a *API) DeleteMailForUser(ctx context.Context, username string, mailID int64) error {
	if a.Mail == nil {
		return errorf(CodeInternal, "mail service not configured")
	}
	id, err := a.accountIDByUsername(ctx, username)
	if err != nil {
		return err
	}
	if err := a.Mail.Delete(ctx, mailID, id); err != nil {
		if errors.Is(err, mail.ErrNotFound) {
			return notFound("mail", fmt.Sprintf("%s:%d", username, mailID))
		}
		return errorf(CodeInternal, "delete: %v", err)
	}
	return nil
}

// UnreadMailCount returns the number of unread mails addressed to the
// named account.
func (a *API) UnreadMailCount(ctx context.Context, username string) (int, error) {
	if a.Mail == nil {
		return 0, errorf(CodeInternal, "mail service not configured")
	}
	id, err := a.accountIDByUsername(ctx, username)
	if err != nil {
		return 0, err
	}
	n, err := a.Mail.UnreadCount(ctx, id)
	if err != nil {
		return 0, errorf(CodeInternal, "unread count: %v", err)
	}
	return n, nil
}

// accountIDByUsername resolves a username to its account id, returning
// a stable not_found Error on miss.
func (a *API) accountIDByUsername(ctx context.Context, username string) (int64, error) {
	var id int64
	err := a.DB.Read().QueryRowContext(ctx,
		`SELECT id FROM accounts WHERE username = ? COLLATE NOCASE`,
		username).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, notFound("account", username)
	}
	if err != nil {
		return 0, errorf(CodeInternal, "lookup account: %v", err)
	}
	return id, nil
}

// listAccountUsernames returns every account's username, optionally
// filtered by access level (empty level matches all).
func (a *API) listAccountUsernames(ctx context.Context, level string) ([]string, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if level == "" {
		rows, err = a.DB.Read().QueryContext(ctx,
			`SELECT username FROM accounts ORDER BY id`)
	} else {
		rows, err = a.DB.Read().QueryContext(ctx,
			`SELECT username FROM accounts WHERE access_level = ? ORDER BY id`, level)
	}
	if err != nil {
		return nil, errorf(CodeInternal, "list accounts: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, errorf(CodeInternal, "scan account: %v", err)
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, errorf(CodeInternal, "list accounts: %v", err)
	}
	return out, nil
}

// translateMailErr maps the mail package's sentinels into the API's
// stable codes. kind/details together populate the not_found details
// string when the error doesn't carry its own.
func translateMailErr(kind, details string, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, mail.ErrRecipientNotFound):
		return notFound(kind, details)
	case errors.Is(err, mail.ErrEmptySubject):
		return errorf(CodeInvalidArgument, "subject is empty")
	case errors.Is(err, mail.ErrEmptyBody):
		return errorf(CodeInvalidArgument, "body is empty")
	case errors.Is(err, mail.ErrParentNotFound):
		return notFound("mail", details)
	case errors.Is(err, mail.ErrNotFound):
		return notFound("mail", details)
	}
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return err
	}
	return errorf(CodeInternal, "%v", err)
}
