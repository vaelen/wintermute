// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package session

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/term"
)

// login runs the username/password flow. On success the session has
// s.account populated and the account's last_login_at is updated. On
// failure (bad credentials, disconnect, etc.) login returns an error.
//
// The user can type "new" at the username prompt to enter the account
// creation flow. The first account created on a fresh server is granted
// admin access; subsequent accounts default to player.
func (h *Handler) login(ctx context.Context, s *Session) error {
	for {
		_ = s.writeString("Username (or 'new' to create an account): ")
		username, err := s.readLine()
		if err != nil && username == "" {
			return err
		}
		username = strings.TrimSpace(username)
		if username == "" {
			continue
		}
		if strings.EqualFold(username, "new") {
			if err := h.createAccount(ctx, s); err != nil {
				if errors.Is(err, io.EOF) {
					return err
				}
				_ = s.writef("Account creation failed: %v\r\n", err)
				continue
			}
			continue // back to login prompt; user can now log in
		}

		if h.LoginGuard != nil {
			if err := h.LoginGuard.CheckUsername(ctx, s.remoteIP(), username); err != nil {
				if errors.Is(err, auth.ErrUsernameDisallowed) {
					return ErrSilentDrop
				}
				return err
			}
		}

		_ = s.writeString("Password: ")
		prevEcho := s.echoOn()
		_ = s.setEcho(true)
		password, err := s.readLine()
		_ = s.setEcho(!prevEcho) // restore to whatever it was, don't force-on
		if err != nil && password == "" {
			return err
		}
		// Newline after password (we suppressed echo, so the client never saw one).
		_ = s.writeString("\r\n")

		res, err := s.auth.Login(ctx, username, password)
		if err != nil {
			if errors.Is(err, auth.ErrInvalidCredentials) {
				if h.LoginGuard != nil &&
					h.LoginGuard.RecordFailedPassword(ctx, s.remoteIP()) {
					return ErrSilentDrop
				}
				_ = s.writeString("Invalid username or password.\r\n")
				continue
			}
			return err
		}
		if err := s.auth.Touch(ctx, res.Account.ID); err != nil {
			s.log.Warn("touch failed", "err", err)
		}
		s.account = res.Account
		s.mustChangePassword = res.MustChangePassword
		s.log = s.log.With("user", res.Account.Username)
		_ = s.writef("\r\nWelcome, %s.\r\n", res.Account.Username)
		return nil
	}
}

// createAccount runs the account creation sub-flow. The first account
// created on the server is granted admin; subsequent ones default to player.
func (h *Handler) createAccount(ctx context.Context, s *Session) error {
	_ = s.writeString("Choose a username: ")
	username, err := s.readLine()
	if err != nil && username == "" {
		return err
	}
	username = strings.TrimSpace(username)

	_ = s.writeString("Choose a password: ")
	prevEcho := s.echoOn()
	_ = s.setEcho(true)
	password, err := s.readLine()
	_ = s.setEcho(!prevEcho)
	if err != nil && password == "" {
		return err
	}
	_ = s.writeString("\r\n")

	// Determine bootstrap admin status.
	level := auth.AccessPlayer
	if count, err := s.auth.Count(ctx); err == nil && count == 0 {
		level = auth.AccessAdmin
	}
	if _, err := s.auth.Create(ctx, username, password, level); err != nil {
		return err
	}
	_ = s.writef("Account %q created.\r\n", username)
	return nil
}

// forcePasswordChange runs the minimal sub-flow that gates a session
// whose login succeeded via a reset token. The user is prompted twice
// for a new password (length-validated, matching-required); on success
// auth.ChangePassword updates the row and clears the reset_hash /
// reset_expires_at columns. Disconnect or repeated failure returns an
// error so Handle can close the session — the reset row stays intact
// so the user can retry next login until expiry.
func (h *Handler) forcePasswordChange(ctx context.Context, s *Session) error {
	if s.account == nil {
		return errors.New("session: forcePasswordChange: no account")
	}
	_ = s.writeString("\r\nA password reset is outstanding for this account.\r\n")
	_ = s.writeString("Please choose a new password before continuing.\r\n\r\n")
	for {
		first, err := h.promptPassword(s, "New password: ")
		if err != nil {
			return err
		}
		if len(first) < auth.MinPasswordLen {
			_ = s.writef("Password must be at least %d characters.\r\n\r\n", auth.MinPasswordLen)
			continue
		}
		second, err := h.promptPassword(s, "Confirm new password: ")
		if err != nil {
			return err
		}
		if first != second {
			_ = s.writeString("Passwords do not match. Try again.\r\n\r\n")
			continue
		}
		if err := s.auth.ChangePassword(ctx, s.account.ID, first); err != nil {
			if errors.Is(err, auth.ErrPasswordTooShort) {
				_ = s.writef("Password must be at least %d characters.\r\n\r\n", auth.MinPasswordLen)
				continue
			}
			return err
		}
		s.mustChangePassword = false
		s.log.Info("password_reset_redeemed", "actor", s.account.Username)
		_ = s.writeString("\r\nPassword updated. Continuing.\r\n")
		return nil
	}
}

// promptPassword writes the prompt, suppresses client echo, reads a
// line, and restores the prior echo state. Empty input is returned as
// "" so the caller's length check fires the same way as for a short
// password. A read error with no buffered data is propagated.
func (h *Handler) promptPassword(s *Session, prompt string) (string, error) {
	_ = s.writeString(prompt)
	prevEcho := s.echoOn()
	_ = s.setEcho(true)
	line, err := s.readLine()
	_ = s.setEcho(!prevEcho)
	_ = s.writeString("\r\n")
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// savePrefs writes the session's current Capabilities back to the
// account's terminal_* columns. Called after login so the saved prefs
// reflect the user's most recent confirmed selection.
func (h *Handler) savePrefs(ctx context.Context, s *Session) error {
	if s.account == nil || s.enc == nil {
		return nil
	}
	c := s.enc.Capabilities()
	enc := c.Encoding.String()
	width := c.Width
	height := c.Height
	color := c.Color
	dec := c.DECLineDrawing
	prefs := auth.TerminalPrefs{
		Encoding: &enc,
		Width:    &width,
		Height:   &height,
		Color:    &color,
		DECLines: &dec,
	}
	return s.auth.SaveTerminalPrefs(ctx, s.account.ID, prefs)
}

// applyAccountPrefsIfDiffer overlays saved per-account preferences on top
// of the user's connect-time choice. If the saved encoding differs from
// the chosen one, the encoder is rebuilt; otherwise only color / width /
// height / DEC are individually applied.
func (h *Handler) applyAccountPrefsIfDiffer(s *Session) {
	if s.account == nil || s.enc == nil {
		return
	}
	cur := s.enc.Capabilities()
	next := cur
	a := s.account

	if a.TerminalEncoding != nil {
		if e, ok := term.ParseEncoding(*a.TerminalEncoding); ok {
			next.Encoding = e
		}
	}
	if a.TerminalWidth != nil {
		next.Width = *a.TerminalWidth
	}
	if a.TerminalHeight != nil {
		next.Height = *a.TerminalHeight
	}
	if a.TerminalColor != nil {
		next.Color = *a.TerminalColor
	}
	if a.TerminalDECLines != nil {
		next.DECLineDrawing = *a.TerminalDECLines
	}
	if next == cur {
		return
	}
	closing := s.enc.Reconfigure(next)
	if len(closing) > 0 {
		_, _ = s.writer().Write(closing)
	}
	// If the saved prefs flip the session into PETSCII and the pre-login
	// choice wasn't PETSCII, the C64 still needs the Shift Out before
	// mixed-case content arrives.
	if next.Encoding == term.EncodingPETSCII && cur.Encoding != term.EncodingPETSCII {
		_, _ = s.writer().Write([]byte{term.PETSCIIShiftOut})
	}
}
