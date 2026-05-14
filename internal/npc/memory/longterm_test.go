// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package memory

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

func newTestDB(t *testing.T) *store.DB {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	path := filepath.Join(t.TempDir(), "memory.db")
	db, err := store.Open(context.Background(), path, logger)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// seedNPCID returns the object id of the seeded bartender. The bartender
// is inserted as part of migration 0005, so every fresh DB has one.
func seedNPCID(t *testing.T, db *store.DB) world.ObjectID {
	t.Helper()
	var id int64
	if err := db.Read().QueryRow(
		`SELECT id FROM objects WHERE slug='npc/bartender'`,
	).Scan(&id); err != nil {
		t.Fatalf("look up bartender id: %v", err)
	}
	return world.ObjectID(id)
}

func TestLongTermInsertAndReadBack(t *testing.T) {
	db := newTestDB(t)
	st := NewSQLStore(db)
	ctx := context.Background()
	npcID := seedNPCID(t, db)

	m := Memory{
		NPCID:     npcID,
		Summary:   "alice asked about the Sprawl gate",
		Embedding: []float32{1, 0, 0, 0},
		CreatedAt: time.Unix(1700000000, 0),
		Salience:  1.0,
	}
	id, err := st.Insert(ctx, m)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if id <= 0 {
		t.Errorf("Insert id = %d, want positive", id)
	}

	got, err := st.Search(ctx, npcID, []float32{1, 0, 0, 0}, 5, 0.5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Search returned %d memories, want 1", len(got))
	}
	if got[0].Summary != m.Summary {
		t.Errorf("Search summary = %q, want %q", got[0].Summary, m.Summary)
	}
	if len(got[0].Embedding) != len(m.Embedding) {
		t.Errorf("Search embedding length = %d, want %d", len(got[0].Embedding), len(m.Embedding))
	}
}

func TestLongTermSearchOrderingAndThreshold(t *testing.T) {
	db := newTestDB(t)
	st := NewSQLStore(db)
	ctx := context.Background()
	npcID := seedNPCID(t, db)

	// Three memories with embeddings that share progressively less
	// direction with the query [1,0,0].
	mems := []Memory{
		{NPCID: npcID, Summary: "near", Embedding: []float32{1, 0, 0}, CreatedAt: time.Unix(1, 0), Salience: 1},
		{NPCID: npcID, Summary: "mid", Embedding: []float32{0.7, 0.7, 0}, CreatedAt: time.Unix(2, 0), Salience: 1},
		{NPCID: npcID, Summary: "far", Embedding: []float32{0, 1, 0}, CreatedAt: time.Unix(3, 0), Salience: 1},
	}
	for _, m := range mems {
		if _, err := st.Insert(ctx, m); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	got, err := st.Search(ctx, npcID, []float32{1, 0, 0}, 5, 0.5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	// "far" has cosine 0 with the query, below threshold 0.5; should be excluded.
	if len(got) != 2 {
		t.Fatalf("Search returned %d memories above threshold 0.5, want 2 (got summaries: %v)",
			len(got), summaries(got))
	}
	if got[0].Summary != "near" || got[1].Summary != "mid" {
		t.Errorf("Search ordering = [%q, %q], want [near, mid]", got[0].Summary, got[1].Summary)
	}
}

func TestLongTermSearchHonoursK(t *testing.T) {
	db := newTestDB(t)
	st := NewSQLStore(db)
	ctx := context.Background()
	npcID := seedNPCID(t, db)

	for i := 0; i < 5; i++ {
		if _, err := st.Insert(ctx, Memory{
			NPCID:     npcID,
			Summary:   "m",
			Embedding: []float32{1, 0, 0},
			CreatedAt: time.Unix(int64(i+1), 0),
			Salience:  1,
		}); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	got, err := st.Search(ctx, npcID, []float32{1, 0, 0}, 3, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("Search with k=3 returned %d memories, want 3", len(got))
	}
}

func TestLongTermSearchScopedToNPC(t *testing.T) {
	db := newTestDB(t)
	st := NewSQLStore(db)
	ctx := context.Background()
	npcA := seedNPCID(t, db)
	// Create a second NPC via the world layer so the foreign key into
	// objects is satisfied. The fixture only ships one NPC.
	npcB := insertSecondNPC(t, db)

	if _, err := st.Insert(ctx, Memory{NPCID: npcA, Summary: "A", Embedding: []float32{1, 0}, Salience: 1}); err != nil {
		t.Fatalf("Insert A: %v", err)
	}
	if _, err := st.Insert(ctx, Memory{NPCID: npcB, Summary: "B", Embedding: []float32{1, 0}, Salience: 1}); err != nil {
		t.Fatalf("Insert B: %v", err)
	}

	got, err := st.Search(ctx, npcA, []float32{1, 0}, 10, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 || got[0].Summary != "A" {
		t.Errorf("Search returned %v, want exactly memory A (NPC scoping leak)", summaries(got))
	}
}

func TestLongTermBumpSalience(t *testing.T) {
	db := newTestDB(t)
	st := NewSQLStore(db)
	ctx := context.Background()
	npcID := seedNPCID(t, db)

	id, err := st.Insert(ctx, Memory{
		NPCID:     npcID,
		Summary:   "x",
		Embedding: []float32{1, 0},
		Salience:  1.0,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if err := st.BumpSalience(ctx, id); err != nil {
		t.Fatalf("BumpSalience: %v", err)
	}
	if err := st.BumpSalience(ctx, id); err != nil {
		t.Fatalf("BumpSalience (2nd): %v", err)
	}

	var got float64
	if err := db.Read().QueryRow(
		`SELECT salience FROM npc_memories WHERE id = ?`, id,
	).Scan(&got); err != nil {
		t.Fatalf("read back salience: %v", err)
	}
	if got != 3.0 {
		t.Errorf("salience after two bumps = %v, want 3.0", got)
	}
}

func TestLongTermSearchEmptyVector(t *testing.T) {
	db := newTestDB(t)
	st := NewSQLStore(db)
	ctx := context.Background()
	npcID := seedNPCID(t, db)

	got, err := st.Search(ctx, npcID, []float32{1, 0}, 5, 0)
	if err != nil {
		t.Fatalf("Search on empty table: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Search on empty table returned %d memories, want 0", len(got))
	}
}

func summaries(ms []Memory) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Summary
	}
	return out
}

func insertSecondNPC(t *testing.T, db *store.DB) world.ObjectID {
	t.Helper()
	ctx := context.Background()
	var newID int64
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO objects(slug, name, short_desc, long_desc, kind) VALUES (?, ?, '', '', 'npc')`,
			"npc/test", "the test npc")
		if err != nil {
			return err
		}
		newID, err = res.LastInsertId()
		return err
	}); err != nil {
		t.Fatalf("insert second NPC: %v", err)
	}
	return world.ObjectID(newID)
}
