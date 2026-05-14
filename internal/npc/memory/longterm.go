// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package memory

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/vaelen/wintermute/internal/store"
	"github.com/vaelen/wintermute/internal/store/vec"
	"github.com/vaelen/wintermute/internal/world"
)

// Memory is one summarised conversation between an NPC and a player,
// plus the embedding that makes it findable on future turns.
type Memory struct {
	ID        int64
	NPCID     world.ObjectID
	Summary   string
	Embedding []float32
	CreatedAt time.Time
	Salience  float64
}

// Store is the long-term memory persistence interface. Production code
// uses NewSQLStore; tests can substitute fakes when convenient.
type Store interface {
	// Insert persists m and returns its assigned id.
	Insert(ctx context.Context, m Memory) (int64, error)

	// Search returns up to k memories for the given NPC, ordered by
	// descending cosine similarity to queryVec. Memories with similarity
	// below threshold are excluded. threshold is in [-1, 1]; pass 0 (or
	// a negative value) to include everything.
	Search(ctx context.Context, npcID world.ObjectID, queryVec []float32, k int, threshold float64) ([]Memory, error)

	// BumpSalience increments the salience counter for the memory by 1.
	// Best-effort; callers should log errors and continue.
	BumpSalience(ctx context.Context, id int64) error
}

// SQLStore is the SQLite-backed implementation of Store. Search uses a
// brute-force in-process cosine similarity scan because the pure-Go
// modernc.org/sqlite driver this project uses cannot load the native
// sqlite-vec extension. The Encode/Decode format in internal/store/vec
// matches what sqlite-vec expects on disk, so a future migration to a
// native scan requires no data rewrite.
type SQLStore struct {
	db *store.DB
}

// NewSQLStore wraps db as a Store.
func NewSQLStore(db *store.DB) *SQLStore { return &SQLStore{db: db} }

// Insert persists m through the writer goroutine. CreatedAt defaults to
// time.Now if zero so callers don't need to set it for every row.
func (s *SQLStore) Insert(ctx context.Context, m Memory) (int64, error) {
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	if m.Salience == 0 {
		m.Salience = 1.0
	}
	blob := vec.Encode(m.Embedding)
	var id int64
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO npc_memories(npc_id, summary, embedding, created_at, salience)
			 VALUES (?, ?, ?, ?, ?)`,
			int64(m.NPCID), m.Summary, blob, m.CreatedAt.Unix(), m.Salience,
		)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("memory: insert: %w", err)
	}
	return id, nil
}

// Search scans every memory row for npcID, computes cosine similarity in
// Go, and returns the top-k above threshold ordered by descending
// similarity. Salience breaks ties (higher first), then memory id
// descending (newer first).
func (s *SQLStore) Search(ctx context.Context, npcID world.ObjectID, queryVec []float32, k int, threshold float64) ([]Memory, error) {
	if k <= 0 || len(queryVec) == 0 {
		return nil, nil
	}
	rows, err := s.db.Read().QueryContext(ctx,
		`SELECT id, npc_id, summary, embedding, created_at, salience
		   FROM npc_memories
		  WHERE npc_id = ?`,
		int64(npcID),
	)
	if err != nil {
		return nil, fmt.Errorf("memory: search query: %w", err)
	}
	defer rows.Close()

	type scored struct {
		m   Memory
		sim float32
	}
	var hits []scored
	for rows.Next() {
		var (
			id        int64
			npcIDRow  int64
			summary   string
			blob      []byte
			createdAt int64
			salience  float64
		)
		if err := rows.Scan(&id, &npcIDRow, &summary, &blob, &createdAt, &salience); err != nil {
			return nil, fmt.Errorf("memory: scan: %w", err)
		}
		emb, err := vec.Decode(blob)
		if err != nil {
			return nil, fmt.Errorf("memory: decode embedding for id %d: %w", id, err)
		}
		sim := vec.CosineSimilarity(queryVec, emb)
		if float64(sim) < threshold {
			continue
		}
		hits = append(hits, scored{
			m: Memory{
				ID:        id,
				NPCID:     world.ObjectID(npcIDRow),
				Summary:   summary,
				Embedding: emb,
				CreatedAt: time.Unix(createdAt, 0),
				Salience:  salience,
			},
			sim: sim,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("memory: search rows: %w", err)
	}

	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].sim != hits[j].sim {
			return hits[i].sim > hits[j].sim
		}
		if hits[i].m.Salience != hits[j].m.Salience {
			return hits[i].m.Salience > hits[j].m.Salience
		}
		return hits[i].m.ID > hits[j].m.ID
	})

	if len(hits) > k {
		hits = hits[:k]
	}
	out := make([]Memory, len(hits))
	for i, h := range hits {
		out[i] = h.m
	}
	return out, nil
}

// BumpSalience adds 1 to the row's salience counter.
func (s *SQLStore) BumpSalience(ctx context.Context, id int64) error {
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE npc_memories SET salience = salience + 1 WHERE id = ?`,
			id,
		)
		return err
	})
	if err != nil {
		return fmt.Errorf("memory: bump salience: %w", err)
	}
	return nil
}
