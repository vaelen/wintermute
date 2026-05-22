// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

// Package schedule tests exercise the scheduler with a real *store.DB
// (temp-file) and a real *world.World — there's nothing entangled
// enough in world construction to warrant a stub Locator, and using
// the real type catches integration issues earlier.
package schedule

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
	"github.com/vaelen/wintermute/internal/world/events"
)

type schedTestEnv struct {
	db    *store.DB
	world *world.World
	bus   *events.MemBus
	npcID world.ObjectID
	room  world.RoomID
}

func newSchedEnv(t *testing.T) *schedTestEnv {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	path := filepath.Join(t.TempDir(), "sched.db")
	db, err := store.Open(context.Background(), path, logger)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	w, err := world.Load(context.Background(), db, logger)
	if err != nil {
		t.Fatalf("world.Load: %v", err)
	}
	lobby, err := w.LobbyID()
	if err != nil {
		t.Fatalf("LobbyID: %v", err)
	}
	npcID, err := w.CreateObject(context.Background(), world.ObjectSpec{
		Slug:        "npc/test",
		Name:        "test npc",
		Kind:        world.KindNPC,
		InitialRoom: lobby,
	})
	if err != nil {
		t.Fatalf("CreateObject: %v", err)
	}
	bus := events.NewMemBus()
	t.Cleanup(bus.Close)
	return &schedTestEnv{db: db, world: w, bus: bus, npcID: npcID, room: lobby}
}

// insertGoal inserts a goal row directly so tests can control id /
// fire_at / recurring without going through the api package.
func insertGoal(t *testing.T, db *store.DB, npcID world.ObjectID, fireAt time.Time, goal, recurring string) int64 {
	t.Helper()
	var id int64
	if err := db.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`INSERT INTO npc_goals (npc_id, fire_at, goal, recurring, created_at)
			   VALUES (?, ?, ?, NULLIF(?, ''), ?)`,
			int64(npcID), fireAt.Unix(), goal, recurring, time.Now().Unix())
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	}); err != nil {
		t.Fatalf("insert npc_goal: %v", err)
	}
	return id
}

func goalCount(t *testing.T, db *store.DB, id int64) int {
	t.Helper()
	var n int
	if err := db.Read().QueryRow(
		`SELECT COUNT(*) FROM npc_goals WHERE id = ?`, id,
	).Scan(&n); err != nil {
		t.Fatalf("count goal %d: %v", id, err)
	}
	return n
}

func goalFireAt(t *testing.T, db *store.DB, id int64) int64 {
	t.Helper()
	var n int64
	if err := db.Read().QueryRow(
		`SELECT fire_at FROM npc_goals WHERE id = ?`, id,
	).Scan(&n); err != nil {
		t.Fatalf("read fire_at for %d: %v", id, err)
	}
	return n
}

func TestScheduler_OneShot(t *testing.T) {
	env := newSchedEnv(t)
	ch, cancel := env.bus.Subscribe(int64(env.room))
	defer cancel()

	past := time.Now().Add(-time.Minute)
	id := insertGoal(t, env.db, env.npcID, past, "check the till", "")

	s := New(env.db, env.world, env.bus, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.fireDue(context.Background())

	select {
	case e := <-ch:
		if e.Kind != events.KindSched {
			t.Errorf("Kind = %q, want %q", e.Kind, events.KindSched)
		}
		if e.Actor != int64(env.npcID) {
			t.Errorf("Actor = %d, want %d", e.Actor, int64(env.npcID))
		}
		if e.RoomID != int64(env.room) {
			t.Errorf("RoomID = %d, want %d", e.RoomID, int64(env.room))
		}
		if e.Text != "check the till" {
			t.Errorf("Text = %q, want %q", e.Text, "check the till")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for KindSched event")
	}

	// Drain any extras (we expect none).
	select {
	case e := <-ch:
		t.Fatalf("unexpected extra event: %+v", e)
	case <-time.After(50 * time.Millisecond):
	}

	if got := goalCount(t, env.db, id); got != 0 {
		t.Errorf("npc_goals row count for id %d = %d, want 0 (one-shot should be deleted)", id, got)
	}
}

func TestScheduler_Daily(t *testing.T) {
	env := newSchedEnv(t)
	ch, cancel := env.bus.Subscribe(int64(env.room))
	defer cancel()

	past := time.Now().Add(-time.Minute)
	id := insertGoal(t, env.db, env.npcID, past, "open the bar", "daily")

	s := New(env.db, env.world, env.bus, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.fireDue(context.Background())

	select {
	case e := <-ch:
		if e.Kind != events.KindSched {
			t.Errorf("Kind = %q, want sched", e.Kind)
		}
		if e.Text != "open the bar" {
			t.Errorf("Text = %q", e.Text)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for KindSched event")
	}

	if got := goalCount(t, env.db, id); got != 1 {
		t.Fatalf("daily goal row count = %d, want 1 (recurring should persist)", got)
	}
	wantFireAt := past.Add(24 * time.Hour).Unix()
	if got := goalFireAt(t, env.db, id); got != wantFireAt {
		t.Errorf("fire_at after daily fire = %d, want %d (delta %d sec)",
			got, wantFireAt, got-wantFireAt)
	}
}
