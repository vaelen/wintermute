// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package auth

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestWordlist_CountAndUniqueness(t *testing.T) {
	words := loadWordlist()
	if got := len(words); got != 7776 {
		t.Fatalf("len(wordlist) = %d, want 7776", got)
	}
	seen := make(map[string]struct{}, len(words))
	for i, w := range words {
		if w == "" {
			t.Errorf("word %d is empty", i)
		}
		if strings.ToLower(w) != w {
			t.Errorf("word %d %q not all-lowercase", i, w)
		}
		if _, dup := seen[w]; dup {
			t.Errorf("duplicate word %q at index %d", w, i)
		}
		seen[w] = struct{}{}
	}
}

func TestGenerateResetToken_Format(t *testing.T) {
	re := regexp.MustCompile(`^[a-z]+(-[a-z]+){3}$`)
	for i := 0; i < 64; i++ {
		tok, err := generateResetToken()
		if err != nil {
			t.Fatalf("generateResetToken: %v", err)
		}
		if !re.MatchString(tok) {
			t.Errorf("token %q does not match expected format", tok)
		}
		parts := strings.Split(tok, "-")
		if len(parts) != ResetTokenWords {
			t.Errorf("token %q split = %d parts, want %d", tok, len(parts), ResetTokenWords)
		}
	}
}

func TestGenerateResetToken_DrawsFromList(t *testing.T) {
	words := loadWordlist()
	set := make(map[string]struct{}, len(words))
	for _, w := range words {
		set[w] = struct{}{}
	}
	tok, err := generateResetToken()
	if err != nil {
		t.Fatalf("generateResetToken: %v", err)
	}
	for _, w := range strings.Split(tok, "-") {
		if _, ok := set[w]; !ok {
			t.Errorf("token word %q is not from the wordlist", w)
		}
	}
}

func TestNormalizeResetToken(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"correct-horse-battery-staple", "correct-horse-battery-staple"},
		{"Correct-Horse-Battery-Staple", "correct-horse-battery-staple"},
		{"CORRECT HORSE BATTERY STAPLE", "correct-horse-battery-staple"},
		{"correct_horse_battery_staple", "correct-horse-battery-staple"},
		{"  correct horse battery staple  ", "correct-horse-battery-staple"},
		{"correct--horse  battery-staple", "correct-horse-battery-staple"},
		{"correct\thorse\tbattery\tstaple", "correct-horse-battery-staple"},
		{"correct-horse-battery-staple-", "correct-horse-battery-staple"},
		{"", ""},
		{"   ", ""},
	}
	for _, c := range cases {
		if got := normalizeResetToken(c.in); got != c.want {
			t.Errorf("normalizeResetToken(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestIssueReset_RoundTrip(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	acc, err := s.Create(ctx, "alice", "hunter2", AccessPlayer)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	tok, err := s.IssueReset(ctx, acc.ID, time.Hour)
	if err != nil {
		t.Fatalf("IssueReset: %v", err)
	}
	if tok == "" {
		t.Fatal("IssueReset returned empty plaintext")
	}

	// Login with reset token should succeed and signal must-change.
	res, err := s.Login(ctx, "alice", tok)
	if err != nil {
		t.Fatalf("Login with reset token: %v", err)
	}
	if !res.MustChangePassword {
		t.Errorf("MustChangePassword = false, want true")
	}
	if res.Account.ID != acc.ID {
		t.Errorf("Login account id = %d, want %d", res.Account.ID, acc.ID)
	}

	// Login with normalised variations of the same token still works.
	mixed := strings.ToUpper(strings.ReplaceAll(tok, "-", " "))
	res2, err := s.Login(ctx, "alice", mixed)
	if err != nil {
		t.Fatalf("Login with normalised token %q: %v", mixed, err)
	}
	if !res2.MustChangePassword {
		t.Errorf("MustChangePassword (normalised) = false, want true")
	}

	// Original password still works (reset is additive, not replacing).
	res3, err := s.Login(ctx, "alice", "hunter2")
	if err != nil {
		t.Fatalf("Login with original password: %v", err)
	}
	if res3.MustChangePassword {
		t.Errorf("MustChangePassword = true on password login")
	}
}

func TestIssueReset_ExpiredTokenRejected(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	acc, err := s.Create(ctx, "alice", "hunter2", AccessPlayer)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	tok, err := s.IssueReset(ctx, acc.ID, time.Hour)
	if err != nil {
		t.Fatalf("IssueReset: %v", err)
	}
	// Backdate the expiry without recomputing the hash.
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx,
			`UPDATE accounts SET reset_expires_at = ? WHERE id = ?`,
			time.Now().Add(-time.Minute).Unix(), acc.ID,
		)
		return e
	}); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	if _, err := s.Login(ctx, "alice", tok); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("Login with expired token err = %v, want ErrInvalidCredentials", err)
	}
}

func TestIssueReset_ReissueInvalidatesPrior(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	acc, err := s.Create(ctx, "alice", "hunter2", AccessPlayer)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	first, err := s.IssueReset(ctx, acc.ID, time.Hour)
	if err != nil {
		t.Fatalf("IssueReset first: %v", err)
	}
	second, err := s.IssueReset(ctx, acc.ID, time.Hour)
	if err != nil {
		t.Fatalf("IssueReset second: %v", err)
	}
	if first == second {
		t.Fatalf("second issue produced identical token %q — randomness suspect", first)
	}
	if _, err := s.Login(ctx, "alice", first); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("Login with superseded token err = %v, want ErrInvalidCredentials", err)
	}
	if _, err := s.Login(ctx, "alice", second); err != nil {
		t.Errorf("Login with reissued token: %v", err)
	}
}

func TestIssueReset_UnknownAccount(t *testing.T) {
	s := newStore(t)
	if _, err := s.IssueReset(context.Background(), 999999, time.Hour); !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("IssueReset unknown account err = %v, want ErrAccountNotFound", err)
	}
}

func TestIssueReset_RejectsNonPositiveTTL(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	acc, _ := s.Create(ctx, "alice", "hunter2", AccessPlayer)
	if _, err := s.IssueReset(ctx, acc.ID, 0); err == nil {
		t.Errorf("expected error for zero ttl")
	}
	if _, err := s.IssueReset(ctx, acc.ID, -time.Minute); err == nil {
		t.Errorf("expected error for negative ttl")
	}
}

func TestChangePassword_ClearsResetAndUpdatesHash(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	acc, err := s.Create(ctx, "alice", "hunter2", AccessPlayer)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	tok, err := s.IssueReset(ctx, acc.ID, time.Hour)
	if err != nil {
		t.Fatalf("IssueReset: %v", err)
	}
	if err := s.ChangePassword(ctx, acc.ID, "fresh-passphrase"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	// Old reset token no longer works.
	if _, err := s.Login(ctx, "alice", tok); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("Login with consumed reset token err = %v, want ErrInvalidCredentials", err)
	}
	// Old password no longer works.
	if _, err := s.Login(ctx, "alice", "hunter2"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("Login with old password err = %v, want ErrInvalidCredentials", err)
	}
	// New password works without must-change.
	res, err := s.Login(ctx, "alice", "fresh-passphrase")
	if err != nil {
		t.Fatalf("Login with new password: %v", err)
	}
	if res.MustChangePassword {
		t.Errorf("MustChangePassword = true after change")
	}
	// Reset row cleared.
	var rh sql.NullString
	var re sql.NullInt64
	if err := s.db.Read().QueryRowContext(ctx,
		`SELECT reset_hash, reset_expires_at FROM accounts WHERE id = ?`, acc.ID,
	).Scan(&rh, &re); err != nil {
		t.Fatalf("scan reset cols: %v", err)
	}
	if rh.Valid || re.Valid {
		t.Errorf("reset cols not cleared after ChangePassword: hash.valid=%v expires.valid=%v", rh.Valid, re.Valid)
	}
}

func TestChangePassword_RejectsShortPassword(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	acc, _ := s.Create(ctx, "alice", "hunter2", AccessPlayer)
	if err := s.ChangePassword(ctx, acc.ID, "abc"); !errors.Is(err, ErrPasswordTooShort) {
		t.Errorf("ChangePassword short err = %v, want ErrPasswordTooShort", err)
	}
}

func TestChangePassword_UnknownAccount(t *testing.T) {
	s := newStore(t)
	if err := s.ChangePassword(context.Background(), 999999, "long-enough"); !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("ChangePassword unknown err = %v, want ErrAccountNotFound", err)
	}
}
