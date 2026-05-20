// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package auth

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// stubPolicy is a UsernamePolicy used by the rename tests. It returns
// the configured error verbatim from CheckAvailable.
type stubPolicy struct {
	err func(name string, forAccount int64) error
}

func (s *stubPolicy) CheckAvailable(_ context.Context, name string, forAccount int64) error {
	if s.err == nil {
		return nil
	}
	return s.err(name, forAccount)
}

func TestRenameHappyPath(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	acc, err := s.Create(ctx, "alice", "hunter22", AccessPlayer)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Recorder counts invocations so we can verify it runs inside the
	// rename transaction.
	var recorderCalls int
	s.SetRenameRecorder(func(ctx context.Context, _ *sql.Tx, oldName string, accountID int64, newName string, _ *int64) error {
		recorderCalls++
		if oldName != "alice" || newName != "arwen" || accountID != acc.ID {
			t.Errorf("recorder args: old=%s new=%s id=%d", oldName, newName, accountID)
		}
		return nil
	})
	var afterCalls int
	s.SetAfterRename(func(_ context.Context, id int64, oldName, newName string) error {
		afterCalls++
		if id != acc.ID || oldName != "alice" || newName != "arwen" {
			t.Errorf("after-rename args: id=%d old=%s new=%s", id, oldName, newName)
		}
		return nil
	})
	if err := s.Rename(ctx, acc.ID, "arwen", nil); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if recorderCalls != 1 {
		t.Errorf("recorderCalls = %d, want 1", recorderCalls)
	}
	if afterCalls != 1 {
		t.Errorf("afterCalls = %d, want 1", afterCalls)
	}
	cur, err := s.GetByID(ctx, acc.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if cur.Username != "arwen" {
		t.Errorf("Username = %q, want arwen", cur.Username)
	}
}

func TestRenameCollision(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	a, _ := s.Create(ctx, "alice", "hunter22", AccessPlayer)
	_, _ = s.Create(ctx, "bob", "hunter22", AccessPlayer)
	if err := s.Rename(ctx, a.ID, "bob", nil); !errors.Is(err, ErrUsernameTaken) {
		t.Errorf("Rename collision: want ErrUsernameTaken, got %v", err)
	}
}

func TestRenameInvalidUsername(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	a, _ := s.Create(ctx, "alice", "hunter22", AccessPlayer)
	if err := s.Rename(ctx, a.ID, "!", nil); !errors.Is(err, ErrInvalidUsername) {
		t.Errorf("Rename invalid: want ErrInvalidUsername, got %v", err)
	}
}

func TestRenamePolicyDisallowed(t *testing.T) {
	s := newStore(t)
	s.SetUsernamePolicy(&stubPolicy{
		err: func(name string, _ int64) error {
			if name == "admin" {
				return ErrUsernameDisallowed
			}
			return nil
		},
	})
	ctx := context.Background()
	a, _ := s.Create(ctx, "alice", "hunter22", AccessPlayer)
	if err := s.Rename(ctx, a.ID, "admin", nil); !errors.Is(err, ErrUsernameDisallowed) {
		t.Errorf("Rename disallowed: want ErrUsernameDisallowed, got %v", err)
	}
}

func TestRenamePolicyReserved(t *testing.T) {
	s := newStore(t)
	s.SetUsernamePolicy(&stubPolicy{
		err: func(name string, forAccount int64) error {
			if name == "reserved" && forAccount != 42 {
				return ErrUsernameReserved
			}
			return nil
		},
	})
	ctx := context.Background()
	a, _ := s.Create(ctx, "alice", "hunter22", AccessPlayer)
	if err := s.Rename(ctx, a.ID, "reserved", nil); !errors.Is(err, ErrUsernameReserved) {
		t.Errorf("Rename reserved: want ErrUsernameReserved, got %v", err)
	}
}

func TestCreateRejectsDisallowedFromPolicy(t *testing.T) {
	s := newStore(t)
	s.SetUsernamePolicy(&stubPolicy{
		err: func(name string, _ int64) error {
			if name == "evilbot" {
				return ErrUsernameDisallowed
			}
			return nil
		},
	})
	ctx := context.Background()
	_, err := s.Create(ctx, "evilbot", "hunter22", AccessPlayer)
	if !errors.Is(err, ErrUsernameDisallowed) {
		t.Errorf("Create disallowed: want ErrUsernameDisallowed, got %v", err)
	}
}
