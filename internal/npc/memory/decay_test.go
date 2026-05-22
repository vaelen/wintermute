// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package memory

import (
	"context"
	"math"
	"testing"

	"github.com/vaelen/wintermute/internal/store"
	"github.com/vaelen/wintermute/internal/world"
)

func TestDecayOnce_AppliesFactor(t *testing.T) {
	db := newTestDB(t)
	st := NewSQLStore(db)
	ctx := context.Background()
	npcID := seedNPCID(t, db)

	id, err := st.Insert(ctx, Memory{
		NPCID:     npcID,
		Summary:   "decay-target",
		Embedding: []float32{1, 0, 0},
		Salience:  1.0,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if err := DecayOnce(ctx, db); err != nil {
		t.Fatalf("DecayOnce: %v", err)
	}

	got := readSalienceByID(t, db, id)
	if math.Abs(got-DecayFactor) > 1e-9 {
		t.Fatalf("after 1 decay salience=%v want %v", got, DecayFactor)
	}

	for i := 0; i < 10; i++ {
		if err := DecayOnce(ctx, db); err != nil {
			t.Fatalf("DecayOnce iter %d: %v", i, err)
		}
	}
	got = readSalienceByID(t, db, id)
	want := math.Pow(DecayFactor, 11)
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("after 11 decays salience=%v want %v", got, want)
	}
}

func TestDecayOnce_TouchesEveryRow(t *testing.T) {
	db := newTestDB(t)
	st := NewSQLStore(db)
	ctx := context.Background()
	npcA := seedNPCID(t, db)
	npcB := insertSecondNPC(t, db)

	rows := []struct {
		npc      world.ObjectID
		salience float64
	}{
		{npcA, 1.0},
		{npcA, 0.5},
		{npcB, 2.0},
		{npcB, 0.2},
	}
	ids := make([]int64, len(rows))
	for i, r := range rows {
		id, err := st.Insert(ctx, Memory{
			NPCID:     r.npc,
			Summary:   "m",
			Embedding: []float32{1, 0},
			Salience:  r.salience,
		})
		if err != nil {
			t.Fatalf("Insert: %v", err)
		}
		ids[i] = id
	}

	before := make([]float64, len(ids))
	for i, id := range ids {
		before[i] = readSalienceByID(t, db, id)
	}

	if err := DecayOnce(ctx, db); err != nil {
		t.Fatalf("DecayOnce: %v", err)
	}

	for i, id := range ids {
		got := readSalienceByID(t, db, id)
		want := before[i] * DecayFactor
		if math.Abs(got-want) > 1e-9 {
			t.Fatalf("row %d (id=%d): salience=%v want %v", i, id, got, want)
		}
	}
}

func TestDecayOnce_EmptyTableIsNoop(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if err := DecayOnce(ctx, db); err != nil {
		t.Fatalf("DecayOnce on empty table: %v", err)
	}
}

func TestDecayFloor_ReachedAfterSaneNumberOfTicks(t *testing.T) {
	// Documents the relationship between DecayFactor and DecayFloor:
	// a memory inserted at salience 1.0 must reach DecayFloor in a
	// reasonable number of daily ticks. The @gc-memories command
	// (Task 14) is the one that deletes; this test only pins the math
	// so future tuning of either constant trips here first.
	if DecayFactor <= 0 || DecayFactor >= 1 {
		t.Fatalf("DecayFactor %v must be in (0,1)", DecayFactor)
	}
	if DecayFloor <= 0 || DecayFloor >= 1 {
		t.Fatalf("DecayFloor %v must be in (0,1)", DecayFloor)
	}
	n := 0
	s := 1.0
	for s >= DecayFloor {
		s *= DecayFactor
		n++
		if n > 10000 {
			t.Fatalf("decay never reaches floor; factor=%v floor=%v", DecayFactor, DecayFloor)
		}
	}
	if n < 20 || n > 200 {
		t.Fatalf("decays-to-floor=%d outside sanity band [20,200] for factor=%v floor=%v",
			n, DecayFactor, DecayFloor)
	}
}

// readSalienceByID returns the salience column for the given memory row.
func readSalienceByID(t *testing.T, db *store.DB, id int64) float64 {
	t.Helper()
	var got float64
	if err := db.Read().QueryRow(
		`SELECT salience FROM npc_memories WHERE id = ?`, id,
	).Scan(&got); err != nil {
		t.Fatalf("read salience id=%d: %v", id, err)
	}
	return got
}
