// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package budget

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/vaelen/wintermute/internal/store"
	"github.com/vaelen/wintermute/internal/world"
)

// Manager holds the live per-NPC NPCBudget for every NPC. Allow and
// Record are concurrency-safe. Persistence is best-effort and
// debounced: Record marks a budget dirty and a separate flush loop
// (RunFlushLoop) writes the dirty set to npc_budgets at an interval.
// A crash can lose up to one flush interval of usage data — fine for a
// usage counter.
type Manager struct {
	db       *store.DB
	defaults Defaults
	mu       sync.Mutex
	budgets  map[world.ObjectID]*NPCBudget
	dirty    map[world.ObjectID]struct{}
}

// NewManager constructs a Manager backed by db with the given default
// per-window limits for newly-seeded NPCs.
func NewManager(db *store.DB, d Defaults) *Manager {
	return &Manager{
		db:       db,
		defaults: d,
		budgets:  map[world.ObjectID]*NPCBudget{},
		dirty:    map[world.ObjectID]struct{}{},
	}
}

// Load reads every npc_budgets row into memory. Missing rows are not
// seeded here; the first Allow for a new NPC creates an in-memory
// budget that gets persisted on the next Flush.
func (m *Manager) Load(ctx context.Context) error {
	rows, err := m.db.Read().QueryContext(ctx,
		`SELECT npc_id,
		        minute_limit, minute_used, minute_started_at,
		        hour_limit,   hour_used,   hour_started_at,
		        day_limit,    day_used,    day_started_at
		   FROM npc_budgets`)
	if err != nil {
		return fmt.Errorf("budget: load: %w", err)
	}
	defer rows.Close()
	m.mu.Lock()
	defer m.mu.Unlock()
	for rows.Next() {
		var (
			id                                    int64
			ml, mUsed, ms, hl, hu, hs, dl, du, ds int64
		)
		if err := rows.Scan(&id, &ml, &mUsed, &ms, &hl, &hu, &hs, &dl, &du, &ds); err != nil {
			return fmt.Errorf("budget: scan: %w", err)
		}
		m.budgets[world.ObjectID(id)] = &NPCBudget{
			Minute: Window{Limit: int(ml), Used: int(mUsed), StartedAt: time.Unix(ms, 0), Duration: time.Minute},
			Hour:   Window{Limit: int(hl), Used: int(hu), StartedAt: time.Unix(hs, 0), Duration: time.Hour},
			Day:    Window{Limit: int(dl), Used: int(du), StartedAt: time.Unix(ds, 0), Duration: 24 * time.Hour},
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("budget: load: %w", err)
	}
	return nil
}

// get returns the existing budget for npcID or seeds a new one.
// Caller must hold m.mu.
func (m *Manager) get(npcID world.ObjectID, now time.Time) *NPCBudget {
	if b, ok := m.budgets[npcID]; ok {
		return b
	}
	b := NewNPCBudget(now, m.defaults)
	m.budgets[npcID] = b
	return b
}

// Allow reports whether npcID can afford estimate tokens right now.
func (m *Manager) Allow(npcID world.ObjectID, estimate int) bool {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.get(npcID, now).Allow(estimate, now)
}

// Record adds tokens to npcID's three windows and marks it dirty.
func (m *Manager) Record(npcID world.ObjectID, usageIn, usageOut int) {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.get(npcID, now).Record(usageIn, usageOut, now)
	m.dirty[npcID] = struct{}{}
}

// Flush writes every dirty budget to npc_budgets and clears the dirty
// set. No-op when nothing is dirty.
func (m *Manager) Flush(ctx context.Context) error {
	m.mu.Lock()
	if len(m.dirty) == 0 {
		m.mu.Unlock()
		return nil
	}
	snap := make(map[world.ObjectID]NPCBudget, len(m.dirty))
	for id := range m.dirty {
		snap[id] = *m.budgets[id]
	}
	m.dirty = map[world.ObjectID]struct{}{}
	m.mu.Unlock()

	return m.db.Write(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx,
			`INSERT INTO npc_budgets
			   (npc_id,
			    minute_limit, minute_used, minute_started_at,
			    hour_limit,   hour_used,   hour_started_at,
			    day_limit,    day_used,    day_started_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(npc_id) DO UPDATE SET
			    minute_limit       = excluded.minute_limit,
			    minute_used        = excluded.minute_used,
			    minute_started_at  = excluded.minute_started_at,
			    hour_limit         = excluded.hour_limit,
			    hour_used          = excluded.hour_used,
			    hour_started_at    = excluded.hour_started_at,
			    day_limit          = excluded.day_limit,
			    day_used           = excluded.day_used,
			    day_started_at     = excluded.day_started_at`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for id, b := range snap {
			if _, err := stmt.ExecContext(ctx,
				int64(id),
				int64(b.Minute.Limit), int64(b.Minute.Used), b.Minute.StartedAt.Unix(),
				int64(b.Hour.Limit), int64(b.Hour.Used), b.Hour.StartedAt.Unix(),
				int64(b.Day.Limit), int64(b.Day.Used), b.Day.StartedAt.Unix()); err != nil {
				return err
			}
		}
		return nil
	})
}

// RunFlushLoop flushes dirty budgets at interval until ctx is done.
// Transient Flush errors are logged and the loop continues — losing
// one interval of usage on an error is acceptable, but abandoning the
// loop (and therefore the final shutdown Flush) is not. On ctx done,
// a final Flush runs with context.Background() so usage is persisted
// even when the parent ctx is already cancelled.
func (m *Manager) RunFlushLoop(ctx context.Context, interval time.Duration, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			if err := m.Flush(ctx); err != nil {
				logger.Warn("budget: periodic flush failed", "err", err)
			}
		case <-ctx.Done():
			return m.Flush(context.Background())
		}
	}
}
