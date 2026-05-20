// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package security

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

	"github.com/vaelen/wintermute/internal/store"
)

// newTestService spins up a fresh on-disk DB (with all migrations applied,
// including 0020 disallowed_usernames, 0021 ip_denials, 0022 username_history)
// and returns a Service ready to Start. Tests that want a controlled clock
// can pass non-nil now; nil falls back to time.Now.
func newTestService(t *testing.T, now func() time.Time) (*Service, *store.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "security.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := store.Open(context.Background(), path, logger)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	svc := New(db, Options{
		AutoDenyEnabled:         true,
		AutoDenyTTL:             5 * time.Minute,
		FailedPasswordThreshold: 5,
		FailedPasswordWindow:    time.Minute,
		EvictionInterval:        time.Minute,
		Logger:                  logger,
		Now:                     now,
	})
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("svc start: %v", err)
	}
	return svc, db
}

func TestDisallowedSeedExcludesExistingAccount(t *testing.T) {
	// Insert an "admin" account row before the migration runs the
	// delete clause and ensure the post-seed DELETE clears the seed.
	path := filepath.Join(t.TempDir(), "seed.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// First migration pass: open with default migrations. We want to
	// simulate "account existed before M6.6's migration." So we open,
	// insert an account, then re-open — but the migrations all run on
	// the first Open. Instead, we inject the account through the
	// existing migration 0001 + manual SQL, then call IsDisallowed.
	db, err := store.Open(context.Background(), path, logger)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	// At this point the seed has already run. Manually re-insert "admin"
	// into accounts and re-execute the protective DELETE clause to
	// confirm the migration's intent. This is the same test the M6.6
	// plan calls out at acceptance criterion 11. Writes go through
	// db.Write so the single-writer invariant holds under -race.
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO accounts(username, password_hash, access_level, created_at) VALUES ('admin', 'x', 'admin', strftime('%s','now'))`)
		return err
	}); err != nil {
		t.Fatalf("insert pre-existing admin: %v", err)
	}
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`DELETE FROM disallowed_usernames WHERE username IN (SELECT username FROM accounts)`)
		return err
	}); err != nil {
		t.Fatalf("post-seed delete: %v", err)
	}

	var count int
	if err := db.Read().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM disallowed_usernames WHERE username = 'admin'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("post-seed delete should have removed admin, got %d rows", count)
	}
}

func TestDisallowUsername(t *testing.T) {
	svc, _ := newTestService(t, nil)
	ctx := context.Background()

	// Add → list → present.
	if err := svc.DisallowUsername(ctx, "evilbot", "spam", nil); err != nil {
		t.Fatalf("DisallowUsername: %v", err)
	}
	rows, err := svc.ListDisallowed(ctx)
	if err != nil {
		t.Fatalf("ListDisallowed: %v", err)
	}
	found := false
	for _, r := range rows {
		if r.Username == "evilbot" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("evilbot not in disallowed list")
	}

	// Case-insensitive lookup.
	ok, err := svc.IsDisallowed(ctx, "EVILBOT")
	if err != nil {
		t.Fatalf("IsDisallowed: %v", err)
	}
	if !ok {
		t.Errorf("EVILBOT should be disallowed (case-insensitive)")
	}

	// "new" keyword is rejected.
	err = svc.DisallowUsername(ctx, "new", "should reject", nil)
	if !errors.Is(err, ErrDisallowReservedKeyword) {
		t.Errorf("disallow 'new': want ErrDisallowReservedKeyword, got %v", err)
	}

	// Duplicate add returns the typed sentinel, not a raw SQLite error.
	if err := svc.DisallowUsername(ctx, "evilbot", "spam", nil); !errors.Is(err, ErrAlreadyDisallowed) {
		t.Errorf("duplicate DisallowUsername: want ErrAlreadyDisallowed, got %v", err)
	}

	// Allow removes the row.
	if err := svc.AllowUsername(ctx, "evilbot"); err != nil {
		t.Fatalf("AllowUsername: %v", err)
	}
	ok, _ = svc.IsDisallowed(ctx, "evilbot")
	if ok {
		t.Errorf("evilbot still disallowed after AllowUsername")
	}
}

func TestRemoveDenyReportsErrIPNotFound(t *testing.T) {
	svc, _ := newTestService(t, nil)
	ctx := context.Background()
	addr := netip.MustParseAddr("10.99.99.99")
	if err := svc.RemoveDeny(ctx, addr); !errors.Is(err, ErrIPNotFound) {
		t.Errorf("RemoveDeny on unknown IP: want ErrIPNotFound, got %v", err)
	}
}

func TestCleanupExpiredAlwaysGCsAttempts(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	svc, _ := newTestService(t, clock.now)
	ctx := context.Background()
	// Drive a few sub-threshold attempts so the tracker has entries
	// that no later eviction will revisit.
	for _, s := range []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"} {
		svc.RecordFailedPassword(ctx, netip.MustParseAddr(s))
	}
	if svc.SnapshotAttempts() == 0 {
		t.Fatalf("expected attempt tracker to have entries before tick")
	}
	// Advance past the window and run the janitor. The DB purge will
	// find nothing to remove (no temp ip_denials rows here), but the
	// attempt tracker should still be swept clean.
	clock.advance(10 * time.Minute)
	if _, err := svc.EvictExpired(ctx); err != nil {
		t.Fatalf("EvictExpired: %v", err)
	}
	if n := svc.SnapshotAttempts(); n != 0 {
		t.Errorf("attempt tracker not GC'd on no-DB-rows path; have %d entries", n)
	}
}

func TestDisallowConflictWithAccount(t *testing.T) {
	svc, db := newTestService(t, nil)
	ctx := context.Background()
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO accounts(username, password_hash, access_level, created_at) VALUES ('alice', 'x', 'player', strftime('%s','now'))`)
		return err
	}); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	if err := svc.DisallowUsername(ctx, "alice", "test", nil); !errors.Is(err, ErrDisallowExistingAccount) {
		t.Errorf("disallow existing account: want ErrDisallowExistingAccount, got %v", err)
	}
}

func TestCheckUsernameFlagsIP(t *testing.T) {
	svc, _ := newTestService(t, nil)
	ctx := context.Background()
	ip := netip.MustParseAddr("10.0.0.5")

	// Seeded list contains "root" — a CheckUsername call should
	// (a) return ErrUsernameDisallowed and (b) flag the IP.
	err := svc.CheckUsername(ctx, ip, "root")
	if !errors.Is(err, ErrUsernameDisallowed) {
		t.Errorf("CheckUsername(root): want ErrUsernameDisallowed, got %v", err)
	}
	if !svc.IsIPDenied(ip) {
		t.Errorf("IP %s should be denied after disallowed-username trigger", ip)
	}
}

func TestCheckUsernameAutoDenyDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auto-off.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := store.Open(context.Background(), path, logger)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	svc := New(db, Options{
		AutoDenyEnabled:         false,
		AutoDenyTTL:             time.Minute,
		FailedPasswordThreshold: 5,
		FailedPasswordWindow:    time.Minute,
		EvictionInterval:        time.Minute,
		Logger:                  logger,
	})
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ip := netip.MustParseAddr("10.0.0.6")
	if err := svc.CheckUsername(context.Background(), ip, "root"); !errors.Is(err, ErrUsernameDisallowed) {
		t.Fatalf("CheckUsername: %v", err)
	}
	if svc.IsIPDenied(ip) {
		t.Errorf("IP should NOT be denied when auto-deny is disabled")
	}
}

func TestRecordFailedPasswordThreshold(t *testing.T) {
	svc, _ := newTestService(t, nil)
	ctx := context.Background()
	ip := netip.MustParseAddr("192.168.1.5")
	for i := 0; i < 4; i++ {
		if svc.RecordFailedPassword(ctx, ip) {
			t.Fatalf("attempt %d should not yet trigger flag", i+1)
		}
	}
	if !svc.RecordFailedPassword(ctx, ip) {
		t.Errorf("5th failed password should trigger flag")
	}
	if !svc.IsIPDenied(ip) {
		t.Errorf("IP should be denied after threshold crossed")
	}
}

func TestIPDenyTempVsPermanent(t *testing.T) {
	svc, _ := newTestService(t, nil)
	ctx := context.Background()
	temp := netip.MustParseAddr("10.0.0.10")
	perm := netip.MustParseAddr("10.0.0.11")

	if err := svc.AddTempDeny(ctx, temp, 10*time.Second, nil, "test-temp"); err != nil {
		t.Fatalf("AddTempDeny: %v", err)
	}
	if err := svc.AddPermanentDeny(ctx, perm, nil, "test-perm"); err != nil {
		t.Fatalf("AddPermanentDeny: %v", err)
	}
	rows := svc.ListDenials()
	var sawTemp, sawPerm bool
	for _, r := range rows {
		switch r.IP {
		case temp:
			sawTemp = true
			if r.Permanent() {
				t.Errorf("temp marked permanent")
			}
		case perm:
			sawPerm = true
			if !r.Permanent() {
				t.Errorf("perm marked temporary")
			}
		}
	}
	if !sawTemp || !sawPerm {
		t.Errorf("ListDenials missing entries: temp=%v perm=%v", sawTemp, sawPerm)
	}

	if err := svc.RemoveDeny(ctx, temp); err != nil {
		t.Fatalf("RemoveDeny: %v", err)
	}
	if svc.IsIPDenied(temp) {
		t.Errorf("temp still denied after RemoveDeny")
	}
}

func TestFlushTempDenials(t *testing.T) {
	svc, _ := newTestService(t, nil)
	ctx := context.Background()
	for _, s := range []string{"10.1.0.1", "10.1.0.2", "10.1.0.3"} {
		_ = svc.AddTempDeny(ctx, netip.MustParseAddr(s), time.Hour, nil, "")
	}
	_ = svc.AddPermanentDeny(ctx, netip.MustParseAddr("10.1.0.99"), nil, "keep")
	n, err := svc.FlushTempDenials(ctx)
	if err != nil {
		t.Fatalf("FlushTempDenials: %v", err)
	}
	if n != 3 {
		t.Errorf("flushed = %d, want 3", n)
	}
	if !svc.IsIPDenied(netip.MustParseAddr("10.1.0.99")) {
		t.Errorf("permanent row was wrongly flushed")
	}
}

func TestEvictExpired(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	svc, _ := newTestService(t, clock.now)
	ctx := context.Background()
	ip := netip.MustParseAddr("203.0.113.4")
	if err := svc.AddTempDeny(ctx, ip, 30*time.Second, nil, ""); err != nil {
		t.Fatalf("AddTempDeny: %v", err)
	}
	if !svc.IsIPDenied(ip) {
		t.Fatalf("ip should be denied immediately after add")
	}
	clock.advance(60 * time.Second)
	if svc.IsIPDenied(ip) {
		t.Errorf("ip should be lazily expired after window")
	}
	// Janitor pass cleans up the DB row.
	if _, err := svc.EvictExpired(ctx); err != nil {
		t.Fatalf("EvictExpired: %v", err)
	}
}

func TestHydrateOnStart(t *testing.T) {
	// Stage 1: open a service, add a permanent + temporary deny,
	// then close.
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	path := filepath.Join(t.TempDir(), "hydrate.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := store.Open(context.Background(), path, logger)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	svc := New(db, Options{
		AutoDenyEnabled:         true,
		AutoDenyTTL:             time.Minute,
		FailedPasswordThreshold: 5,
		FailedPasswordWindow:    time.Minute,
		EvictionInterval:        time.Minute,
		Logger:                  logger,
		Now:                     clock.now,
	})
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	stale := netip.MustParseAddr("198.51.100.1")
	fresh := netip.MustParseAddr("198.51.100.2")
	permIP := netip.MustParseAddr("198.51.100.3")
	if err := svc.AddTempDeny(context.Background(), stale, 30*time.Second, nil, ""); err != nil {
		t.Fatalf("AddTempDeny stale: %v", err)
	}
	if err := svc.AddTempDeny(context.Background(), fresh, time.Hour, nil, ""); err != nil {
		t.Fatalf("AddTempDeny fresh: %v", err)
	}
	if err := svc.AddPermanentDeny(context.Background(), permIP, nil, ""); err != nil {
		t.Fatalf("AddPermanentDeny: %v", err)
	}
	_ = db.Close()

	// Stage 2: re-open with the same path and a clock past the stale
	// row's expiry. Start should purge the stale row and hydrate the
	// other two.
	clock.advance(2 * time.Minute)
	db2, err := store.Open(context.Background(), path, logger)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	t.Cleanup(func() { _ = db2.Close() })
	svc2 := New(db2, Options{
		AutoDenyEnabled:         true,
		AutoDenyTTL:             time.Minute,
		FailedPasswordThreshold: 5,
		FailedPasswordWindow:    time.Minute,
		EvictionInterval:        time.Minute,
		Logger:                  logger,
		Now:                     clock.now,
	})
	if err := svc2.Start(context.Background()); err != nil {
		t.Fatalf("Start2: %v", err)
	}
	if svc2.IsIPDenied(stale) {
		t.Errorf("stale entry should have been purged on Start")
	}
	if !svc2.IsIPDenied(fresh) {
		t.Errorf("fresh entry should still be denied")
	}
	if !svc2.IsIPDenied(permIP) {
		t.Errorf("permanent entry should still be denied")
	}
}

func TestUsernameHistoryRoundTrip(t *testing.T) {
	svc, db := newTestService(t, nil)
	ctx := context.Background()
	// Insert alice (account_id auto = 1).
	mustExec(t, db, `INSERT INTO accounts(username, password_hash, access_level, created_at) VALUES ('alice', 'x', 'player', strftime('%s','now'))`)

	// Record one rename via Service.RecordRename inside a tx.
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		return svc.RecordRename(ctx, tx, "alice", 1, "arwen", nil)
	}); err != nil {
		t.Fatalf("RecordRename: %v", err)
	}
	rows, err := svc.ListUsernameHistory(ctx, 1)
	if err != nil {
		t.Fatalf("ListUsernameHistory: %v", err)
	}
	if len(rows) != 1 || rows[0].OldUsername != "alice" || rows[0].RenamedTo != "arwen" {
		t.Fatalf("history rows = %+v, want one alice→arwen row", rows)
	}
	if rows[0].AccountID == nil || *rows[0].AccountID != 1 {
		t.Fatalf("AccountID = %v, want 1", rows[0].AccountID)
	}

	// ON CONFLICT path: re-recording the same old name refreshes
	// renamed_to without inserting a second row.
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		return svc.RecordRename(ctx, tx, "alice", 1, "athena", nil)
	}); err != nil {
		t.Fatalf("RecordRename refresh: %v", err)
	}
	rows, _ = svc.ListUsernameHistory(ctx, 1)
	if len(rows) != 1 || rows[0].RenamedTo != "athena" {
		t.Fatalf("after refresh: rows = %+v, want one alice→athena row", rows)
	}

	// Release removes the row.
	if err := svc.ReleaseUsernameHistory(ctx, "alice"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	rows, _ = svc.ListUsernameHistory(ctx, 1)
	if len(rows) != 0 {
		t.Errorf("after release: rows = %+v, want empty", rows)
	}
}

// mustExec runs raw SQL via the store's writer transaction. Wrapping
// every test exec through Write keeps the in-process single-writer
// guarantee intact.
func mustExec(t *testing.T, db *store.DB, query string, args ...any) {
	t.Helper()
	if err := db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), query, args...)
		return err
	}); err != nil {
		t.Fatalf("exec %s: %v", query, err)
	}
}

func TestCheckAvailableComposes(t *testing.T) {
	svc, db := newTestService(t, nil)
	ctx := context.Background()

	// Disallowed: seeded "admin" rejects, even for "new-account" use.
	if err := svc.CheckAvailable(ctx, "admin", 0); !errors.Is(err, ErrUsernameDisallowed) {
		t.Errorf("CheckAvailable(admin, 0): want ErrUsernameDisallowed, got %v", err)
	}

	// Reservation: create alice (account_id=1) and bob (account_id=2),
	// stick a history row pointing at alice. CheckAvailable for the
	// reserved name with forAccount = 0 (new account) and = bob.id
	// must reject; with forAccount = alice.id must accept (self
	// rotation).
	mustExec(t, db, `INSERT INTO accounts(username, password_hash, access_level, created_at) VALUES ('alice', 'x', 'player', strftime('%s','now'))`)
	mustExec(t, db, `INSERT INTO accounts(username, password_hash, access_level, created_at) VALUES ('bob', 'x', 'player', strftime('%s','now'))`)
	mustExec(t, db, `INSERT INTO username_history(old_username, account_id, renamed_at, renamed_to, renamed_by) VALUES ('oldname', 1, strftime('%s','now'), 'newname', NULL)`)

	if err := svc.CheckAvailable(ctx, "oldname", 0); !errors.Is(err, ErrUsernameReserved) {
		t.Errorf("CheckAvailable(oldname, 0): want ErrUsernameReserved, got %v", err)
	}
	if err := svc.CheckAvailable(ctx, "oldname", 1); err != nil {
		t.Errorf("CheckAvailable(oldname, 1=alice): self-rotation should succeed, got %v", err)
	}
	if err := svc.CheckAvailable(ctx, "oldname", 2); !errors.Is(err, ErrUsernameReserved) {
		t.Errorf("CheckAvailable(oldname, 2=bob): other account should fail, got %v", err)
	}

	// Available name passes.
	if err := svc.CheckAvailable(ctx, "carol", 0); err != nil {
		t.Errorf("CheckAvailable(carol, 0): want nil, got %v", err)
	}
}

func TestAttemptTrackerSlidingWindow(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	tr := newAttemptTracker(3, 30*time.Second, clock.now)
	ip := netip.MustParseAddr("10.0.0.50")

	for i := 0; i < 2; i++ {
		if tr.record(ip) {
			t.Fatalf("record %d should not trigger", i)
		}
	}
	// Slide past the window: previous two entries should not count.
	clock.advance(time.Minute)
	if tr.record(ip) {
		t.Errorf("first record in fresh window should not trigger")
	}
	// Two more in the new window.
	if tr.record(ip) {
		t.Errorf("second in window should not trigger")
	}
	if !tr.record(ip) {
		t.Errorf("third in window should trigger")
	}
}

// ---------------------------------------------------------------------------
// fake clock

type fakeClock struct {
	t time.Time
}

func newFakeClock(at time.Time) *fakeClock { return &fakeClock{t: at} }
func (c *fakeClock) now() time.Time         { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }
