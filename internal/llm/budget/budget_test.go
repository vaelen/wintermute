// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package budget

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/vaelen/wintermute/internal/store"
	"github.com/vaelen/wintermute/internal/world"
)

func TestWindow_AllowAndRecord(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	w := Window{Limit: 100, Used: 0, StartedAt: now, Duration: time.Minute}
	if !w.Allow(50, now.Add(5*time.Second)) {
		t.Fatal("should allow 50 under 100")
	}
	w.Record(50, now.Add(5*time.Second))
	if w.Used != 50 {
		t.Fatalf("Used=%d want 50", w.Used)
	}
	if w.Allow(60, now.Add(10*time.Second)) {
		t.Fatal("should deny 60 (would exceed 100)")
	}
}

func TestWindow_RolloverResetsUsed(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	w := Window{Limit: 100, Used: 90, StartedAt: now, Duration: time.Minute}
	w.Record(5, now.Add(61*time.Second))
	if w.Used != 5 {
		t.Fatalf("Used after rollover=%d want 5", w.Used)
	}
	if !w.StartedAt.After(now) {
		t.Fatal("StartedAt should advance on rollover")
	}
}

func TestWindow_ZeroStartedAtRollsOver(t *testing.T) {
	// A freshly-defaulted Window (StartedAt zero) should accept its
	// first Record without spuriously charging the previous epoch.
	w := Window{Limit: 100, Duration: time.Minute}
	now := time.Unix(1_700_000_000, 0)
	w.Record(7, now)
	if w.Used != 7 {
		t.Fatalf("Used=%d want 7", w.Used)
	}
	if !w.StartedAt.Equal(now) {
		t.Fatalf("StartedAt=%v want %v", w.StartedAt, now)
	}
}

func TestNPCBudget_AllowRequiresAllWindows(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	b := NewNPCBudget(now, Defaults{Minute: 100, Hour: 1000, Day: 10000})
	b.Minute.Used = 95
	if b.Allow(10, now) {
		t.Fatal("minute window exhausted but Allow returned true")
	}
	b.Minute.Used = 0
	b.Hour.Used = 995
	if b.Allow(10, now) {
		t.Fatal("hour window exhausted but Allow returned true")
	}
}

func TestNPCBudget_RecordChargesAllWindows(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	b := NewNPCBudget(now, Defaults{Minute: 100, Hour: 1000, Day: 10000})
	b.Record(7, 3, now)
	if b.Minute.Used != 10 || b.Hour.Used != 10 || b.Day.Used != 10 {
		t.Fatalf("got minute=%d hour=%d day=%d, want all 10",
			b.Minute.Used, b.Hour.Used, b.Day.Used)
	}
}

// tempStore opens an isolated SQLite DB with migrations applied. Caller
// is responsible for closing via t.Cleanup, which Open arranges here.
func tempStore(t *testing.T) *store.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "budget.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := store.Open(context.Background(), path, logger)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// insertNPCObject inserts a row in objects so the npc_budgets foreign
// key has something to point at, and returns its ID.
func insertNPCObject(t *testing.T, db *store.DB, slug string) world.ObjectID {
	t.Helper()
	ctx := context.Background()
	var id int64
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO objects(slug, name, short_desc, long_desc, kind) VALUES (?, ?, '', '', 'npc')`,
			slug, slug)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	}); err != nil {
		t.Fatalf("insert NPC object: %v", err)
	}
	return world.ObjectID(id)
}

func TestManager_PersistAndLoad(t *testing.T) {
	ctx := context.Background()
	db := tempStore(t)
	npcID := insertNPCObject(t, db, "npc/budget-test")

	defaults := Defaults{Minute: 100, Hour: 1000, Day: 10000}

	m := NewManager(db, defaults)
	if err := m.Load(ctx); err != nil {
		t.Fatalf("Load (empty): %v", err)
	}
	m.Record(npcID, 7, 3)
	if err := m.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	m2 := NewManager(db, defaults)
	if err := m2.Load(ctx); err != nil {
		t.Fatalf("Load (after flush): %v", err)
	}
	m2.mu.Lock()
	b, ok := m2.budgets[npcID]
	m2.mu.Unlock()
	if !ok {
		t.Fatalf("budget for npc %d not loaded", npcID)
	}
	if b.Minute.Used != 10 || b.Hour.Used != 10 || b.Day.Used != 10 {
		t.Fatalf("loaded usage minute=%d hour=%d day=%d, want 10/10/10",
			b.Minute.Used, b.Hour.Used, b.Day.Used)
	}
	if b.Minute.Limit != defaults.Minute || b.Hour.Limit != defaults.Hour || b.Day.Limit != defaults.Day {
		t.Fatalf("loaded limits minute=%d hour=%d day=%d, want %v",
			b.Minute.Limit, b.Hour.Limit, b.Day.Limit, defaults)
	}
}

func TestManager_FlushNoopWhenClean(t *testing.T) {
	ctx := context.Background()
	db := tempStore(t)
	m := NewManager(db, Defaults{Minute: 100, Hour: 1000, Day: 10000})
	if err := m.Flush(ctx); err != nil {
		t.Fatalf("Flush on empty: %v", err)
	}
}

func TestManager_AllowSeedsBudget(t *testing.T) {
	db := tempStore(t)
	npcID := insertNPCObject(t, db, "npc/allow-seed")
	m := NewManager(db, Defaults{Minute: 100, Hour: 1000, Day: 10000})
	if !m.Allow(npcID, 50) {
		t.Fatal("expected fresh NPC to be Allow'd at estimate=50")
	}
	m.Record(npcID, 60, 30)
	if m.Allow(npcID, 20) {
		t.Fatal("minute window should be exhausted after 90 used + 20 estimate")
	}
}
