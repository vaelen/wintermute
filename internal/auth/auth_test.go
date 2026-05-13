// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/vaelen/wintermute/internal/store"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := store.Open(context.Background(), path, logger)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewStore(db)
}

func TestHashRoundTrip(t *testing.T) {
	hash, err := hashPassword("hunter2")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	ok, err := verifyPassword(hash, "hunter2")
	if err != nil {
		t.Fatalf("verifyPassword: %v", err)
	}
	if !ok {
		t.Errorf("verifyPassword false for correct password")
	}
	ok, err = verifyPassword(hash, "wrong")
	if err != nil {
		t.Fatalf("verifyPassword wrong: %v", err)
	}
	if ok {
		t.Errorf("verifyPassword true for wrong password")
	}
}

func TestVerifyMalformedHash(t *testing.T) {
	if _, err := verifyPassword("not-an-argon2-hash", "x"); err == nil {
		t.Errorf("expected error for malformed hash")
	}
}

func TestCreateAndLogin(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	acc, err := s.Create(ctx, "alice", "hunter2", AccessPlayer)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if acc.Username != "alice" {
		t.Errorf("Username = %q, want alice", acc.Username)
	}
	if acc.AccessLevel != AccessPlayer {
		t.Errorf("AccessLevel = %q, want player", acc.AccessLevel)
	}
	if acc.LastLoginAt != nil {
		t.Errorf("LastLoginAt should be nil for new account, got %v", acc.LastLoginAt)
	}

	got, err := s.Login(ctx, "alice", "hunter2")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if got.ID != acc.ID {
		t.Errorf("Login ID = %d, want %d", got.ID, acc.ID)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.Create(ctx, "alice", "hunter2", AccessPlayer); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.Login(ctx, "alice", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("Login wrong password err = %v, want ErrInvalidCredentials", err)
	}
}

func TestLoginUnknownUsername(t *testing.T) {
	s := newStore(t)
	if _, err := s.Login(context.Background(), "nobody", "x"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("Login unknown user err = %v, want ErrInvalidCredentials", err)
	}
}

func TestCreateDuplicateRejected(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.Create(ctx, "alice", "hunter2", AccessPlayer); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.Create(ctx, "alice", "another", AccessPlayer); !errors.Is(err, ErrUsernameTaken) {
		t.Errorf("Create duplicate err = %v, want ErrUsernameTaken", err)
	}
}

func TestCreatePolicies(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.Create(ctx, "a", "hunter2", AccessPlayer); !errors.Is(err, ErrInvalidUsername) {
		t.Errorf("short username err = %v, want ErrInvalidUsername", err)
	}
	if _, err := s.Create(ctx, "alice!", "hunter2", AccessPlayer); !errors.Is(err, ErrInvalidUsername) {
		t.Errorf("bad-char username err = %v, want ErrInvalidUsername", err)
	}
	if _, err := s.Create(ctx, "alice", "abc", AccessPlayer); !errors.Is(err, ErrPasswordTooShort) {
		t.Errorf("short password err = %v, want ErrPasswordTooShort", err)
	}
}

func TestTouch(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	acc, err := s.Create(ctx, "alice", "hunter2", AccessPlayer)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Touch(ctx, acc.ID); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	got, err := s.GetByID(ctx, acc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastLoginAt == nil {
		t.Errorf("LastLoginAt should be set after Touch")
	}
}

func TestSaveTerminalPrefs(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	acc, err := s.Create(ctx, "alice", "hunter2", AccessPlayer)
	if err != nil {
		t.Fatal(err)
	}
	enc := "petscii"
	w, h := 40, 24
	color := true
	dec := false
	prefs := TerminalPrefs{
		Encoding: &enc,
		Width:    &w,
		Height:   &h,
		Color:    &color,
		DECLines: &dec,
	}
	if err := s.SaveTerminalPrefs(ctx, acc.ID, prefs); err != nil {
		t.Fatalf("SaveTerminalPrefs: %v", err)
	}
	got, err := s.GetByID(ctx, acc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TerminalEncoding == nil || *got.TerminalEncoding != "petscii" {
		t.Errorf("TerminalEncoding = %v, want petscii", got.TerminalEncoding)
	}
	if got.TerminalWidth == nil || *got.TerminalWidth != 40 {
		t.Errorf("TerminalWidth = %v, want 40", got.TerminalWidth)
	}
	if got.TerminalColor == nil || !*got.TerminalColor {
		t.Errorf("TerminalColor = %v, want true", got.TerminalColor)
	}
	if got.TerminalDECLines == nil || *got.TerminalDECLines {
		t.Errorf("TerminalDECLines = %v, want false", got.TerminalDECLines)
	}
}

func TestSaveTerminalPrefsClearsToNull(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	acc, err := s.Create(ctx, "alice", "hunter2", AccessPlayer)
	if err != nil {
		t.Fatal(err)
	}
	enc := "utf8"
	if err := s.SaveTerminalPrefs(ctx, acc.ID, TerminalPrefs{Encoding: &enc}); err != nil {
		t.Fatal(err)
	}
	// Now clear it.
	if err := s.SaveTerminalPrefs(ctx, acc.ID, TerminalPrefs{}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetByID(ctx, acc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TerminalEncoding != nil {
		t.Errorf("TerminalEncoding = %v, want nil after clear", *got.TerminalEncoding)
	}
}

func TestCount(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	n, err := s.Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("Count = %d, want 0", n)
	}
	if _, err := s.Create(ctx, "alice", "hunter2", AccessAdmin); err != nil {
		t.Fatal(err)
	}
	n, err = s.Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("Count = %d, want 1", n)
	}
}

func TestValidUsername(t *testing.T) {
	cases := []struct {
		u  string
		ok bool
	}{
		{"alice", true},
		{"a", false},
		{"Alice-1", true},
		{"a.b_c", true},
		{"with space", false},
		{"😀", false},
		{"", false},
	}
	for _, c := range cases {
		if got := validUsername(c.u); got != c.ok {
			t.Errorf("validUsername(%q) = %v, want %v", c.u, got, c.ok)
		}
	}
}
