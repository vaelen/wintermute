// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package integration

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/security"
	"github.com/vaelen/wintermute/internal/store"
)

// securityRig wires auth.Store and security.Service together exactly
// the way main.go does, so the integration tests can exercise the
// rename + history reservation flow end-to-end without a network.
type securityRig struct {
	t     *testing.T
	db    *store.DB
	auth  *auth.Store
	sec   *security.Service
	boots []bootEvent
}

type bootEvent struct {
	accountID int64
	oldName   string
	newName   string
}

func newSecurityRig(t *testing.T) *securityRig {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "rig.db"), logger)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	a := auth.NewStore(db)
	sec := security.New(db, security.Options{
		AutoDenyEnabled:         true,
		AutoDenyTTL:             5 * time.Minute,
		FailedPasswordThreshold: 5,
		FailedPasswordWindow:    time.Minute,
		EvictionInterval:        time.Minute,
		Logger:                  logger,
	})
	if err := sec.Start(context.Background()); err != nil {
		t.Fatalf("sec.Start: %v", err)
	}
	r := &securityRig{t: t, db: db, auth: a, sec: sec}
	a.SetUsernamePolicy(sec)
	a.SetRenameRecorder(func(ctx context.Context, tx *sql.Tx, oldName string, accountID int64, newName string, renamedBy *int64) error {
		return sec.RecordRename(ctx, tx, oldName, accountID, newName, renamedBy)
	})
	a.SetAfterRename(func(_ context.Context, accountID int64, oldName, newName string) error {
		r.boots = append(r.boots, bootEvent{accountID: accountID, oldName: oldName, newName: newName})
		return nil
	})
	return r
}

func TestRenameReservesOldNameForNewAccountCreation(t *testing.T) {
	r := newSecurityRig(t)
	ctx := context.Background()

	alice, err := r.auth.Create(ctx, "alice", "hunter22", auth.AccessPlayer)
	if err != nil {
		t.Fatalf("Create alice: %v", err)
	}
	if err := r.auth.Rename(ctx, alice.ID, "arwen", nil); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	// The after-rename hook fired with alice → arwen.
	if len(r.boots) != 1 || r.boots[0].oldName != "alice" || r.boots[0].newName != "arwen" {
		t.Errorf("boots = %+v", r.boots)
	}

	// New-account create with the now-reserved old name must fail.
	if _, err := r.auth.Create(ctx, "alice", "hunter22", auth.AccessPlayer); !errors.Is(err, auth.ErrUsernameReserved) {
		t.Errorf("Create reserved-name: want ErrUsernameReserved, got %v", err)
	}

	// Admin releases the row; create now succeeds.
	if err := r.sec.ReleaseUsernameHistory(ctx, "alice"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	newAlice, err := r.auth.Create(ctx, "alice", "hunter22", auth.AccessPlayer)
	if err != nil {
		t.Fatalf("Create after release: %v", err)
	}
	if newAlice.ID == alice.ID {
		t.Errorf("post-release create should produce a distinct account row")
	}
}

func TestRenameBlocksOtherAccountFromReservedName(t *testing.T) {
	r := newSecurityRig(t)
	ctx := context.Background()
	alice, _ := r.auth.Create(ctx, "alice", "hunter22", auth.AccessPlayer)
	bob, _ := r.auth.Create(ctx, "bob", "hunter22", auth.AccessPlayer)
	if err := r.auth.Rename(ctx, alice.ID, "arwen", nil); err != nil {
		t.Fatalf("Rename alice: %v", err)
	}
	// Bob attempts to rename into alice's old name — must fail.
	if err := r.auth.Rename(ctx, bob.ID, "alice", nil); !errors.Is(err, auth.ErrUsernameReserved) {
		t.Errorf("Rename bob→alice: want ErrUsernameReserved, got %v", err)
	}
}

func TestSelfRotationPermitted(t *testing.T) {
	r := newSecurityRig(t)
	ctx := context.Background()
	alice, _ := r.auth.Create(ctx, "alice", "hunter22", auth.AccessPlayer)
	if err := r.auth.Rename(ctx, alice.ID, "arwen", nil); err != nil {
		t.Fatalf("Rename to arwen: %v", err)
	}
	if err := r.auth.Rename(ctx, alice.ID, "alice", nil); err != nil {
		t.Fatalf("Rename back to alice (self-rotation): %v", err)
	}
	hist, err := r.sec.ListUsernameHistory(ctx, alice.ID)
	if err != nil {
		t.Fatalf("ListUsernameHistory: %v", err)
	}
	// alice → arwen and arwen → alice both reserved.
	if len(hist) != 2 {
		t.Errorf("history rows = %d, want 2 (alice + arwen)", len(hist))
	}
}

func TestRenameIntoDisallowedRejects(t *testing.T) {
	r := newSecurityRig(t)
	ctx := context.Background()
	alice, _ := r.auth.Create(ctx, "alice", "hunter22", auth.AccessPlayer)
	// "admin" is in the seeded disallow list.
	if err := r.auth.Rename(ctx, alice.ID, "admin", nil); !errors.Is(err, auth.ErrUsernameDisallowed) {
		t.Errorf("Rename into disallowed: want ErrUsernameDisallowed, got %v", err)
	}
	// alice still has her name and no history row was written.
	cur, err := r.auth.GetByID(ctx, alice.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if cur.Username != "alice" {
		t.Errorf("Username = %q, want alice", cur.Username)
	}
	hist, _ := r.sec.ListUsernameHistory(ctx, alice.ID)
	if len(hist) != 0 {
		t.Errorf("history rows = %d, want 0", len(hist))
	}
}

func TestFailedPasswordThresholdDropsConnection(t *testing.T) {
	r := newSecurityRig(t)
	ctx := context.Background()
	ip := netip.MustParseAddr("203.0.113.99")

	// Five failed attempts on a known account.
	_, _ = r.auth.Create(ctx, "victim", "correctpw", auth.AccessPlayer)
	for i := 0; i < 5; i++ {
		if _, err := r.auth.Login(ctx, "victim", "wrongpw"); !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Fatalf("attempt %d: %v", i, err)
		}
		triggered := r.sec.RecordFailedPassword(ctx, ip)
		if i < 4 && triggered {
			t.Errorf("attempt %d triggered too early", i)
		}
		if i == 4 && !triggered {
			t.Errorf("attempt %d should have triggered the flag", i)
		}
	}
	if !r.sec.IsIPDenied(ip) {
		t.Errorf("IP should be denied after 5 failed attempts in window")
	}
}

func TestUsernameDisallowedSilentDropOnCheckUsername(t *testing.T) {
	r := newSecurityRig(t)
	ctx := context.Background()
	ip := netip.MustParseAddr("198.18.0.7")

	// Seeded "root" — must report ErrUsernameDisallowed and flag the IP.
	err := r.sec.CheckUsername(ctx, ip, "root")
	if !errors.Is(err, security.ErrUsernameDisallowed) {
		t.Errorf("CheckUsername(root): want ErrUsernameDisallowed, got %v", err)
	}
	if !r.sec.IsIPDenied(ip) {
		t.Errorf("IP should be auto-denied after disallowed-username trigger")
	}
}
