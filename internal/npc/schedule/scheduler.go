// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

// Package schedule fires npc_goals as KindSched events into the
// per-room events.Bus when their fire_at timestamp arrives. Recurring
// goals ("daily", "hourly") are rescheduled; all other recurrence
// values fire once and are deleted.
package schedule

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/vaelen/wintermute/internal/store"
	"github.com/vaelen/wintermute/internal/world"
	"github.com/vaelen/wintermute/internal/world/events"
)

// Locator is the minimum world surface the scheduler needs to resolve
// an NPC's current room. *world.World satisfies it.
type Locator interface {
	LocationOf(world.ObjectID) (world.Location, error)
}

// Scheduler reads npc_goals on a periodic cadence and dispatches due
// goals as KindSched events. One goroutine per server.
type Scheduler struct {
	db     *store.DB
	loc    Locator
	bus    events.Bus
	logger *slog.Logger
	// Tick is the polling cadence. Defaults to 30s when zero.
	Tick time.Duration
}

// New constructs a Scheduler. logger may be nil; the default slog
// logger is used.
func New(db *store.DB, loc Locator, bus events.Bus, logger *slog.Logger) *Scheduler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{db: db, loc: loc, bus: bus, logger: logger}
}

// Run polls for due goals on every tick. Cadence defaults to 30s; the
// scheduler is best-effort with minute-resolution accuracy. Returns
// when ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	cadence := s.Tick
	if cadence == 0 {
		cadence = 30 * time.Second
	}
	t := time.NewTicker(cadence)
	defer t.Stop()
	// Fire immediately on start so a freshly-restored DB does not wait
	// a full tick to drain backlog.
	s.fireDue(ctx)
	for {
		select {
		case <-t.C:
			s.fireDue(ctx)
		case <-ctx.Done():
			return
		}
	}
}

type goal struct {
	id        int64
	npcID     world.ObjectID
	fireAt    time.Time
	goal      string
	recurring string
}

// fireDue is exported package-internally for tests; it scans for goals
// whose fire_at has elapsed and dispatches each.
func (s *Scheduler) fireDue(ctx context.Context) {
	now := time.Now().Unix()
	rows, err := s.db.Read().QueryContext(ctx,
		`SELECT id, npc_id, fire_at, goal, COALESCE(recurring, '')
		   FROM npc_goals
		  WHERE fire_at <= ?
		  ORDER BY fire_at ASC`, now)
	if err != nil {
		s.logger.Warn("scheduler: query due goals", "err", err)
		return
	}
	var due []goal
	for rows.Next() {
		var g goal
		var npcID, fireAt int64
		if err := rows.Scan(&g.id, &npcID, &fireAt, &g.goal, &g.recurring); err != nil {
			s.logger.Warn("scheduler: scan goal", "err", err)
			continue
		}
		g.npcID = world.ObjectID(npcID)
		g.fireAt = time.Unix(fireAt, 0)
		due = append(due, g)
	}
	if err := rows.Err(); err != nil {
		s.logger.Warn("scheduler: iterate goals", "err", err)
	}
	rows.Close()
	for _, g := range due {
		s.fire(ctx, g)
	}
}

func (s *Scheduler) fire(ctx context.Context, g goal) {
	loc, err := s.loc.LocationOf(g.npcID)
	if err != nil || loc.RoomID == 0 {
		s.logger.Warn("scheduler: NPC has no room, dropping goal",
			"goal_id", g.id, "npc_id", int64(g.npcID), "err", err)
		s.delete(ctx, g.id)
		return
	}
	s.bus.Publish(events.Event{
		Kind:   events.KindSched,
		RoomID: int64(loc.RoomID),
		Actor:  int64(g.npcID),
		Text:   g.goal,
		At:     time.Now(),
	})
	if next, ok := nextRecurrence(g.fireAt, g.recurring); ok {
		s.reschedule(ctx, g.id, next)
		return
	}
	s.delete(ctx, g.id)
}

// nextRecurrence advances fire_at by the recurrence interval. Returns
// (zero, false) for one-shot goals or unknown recurrence strings.
func nextRecurrence(prev time.Time, recurring string) (time.Time, bool) {
	switch recurring {
	case "daily":
		return prev.Add(24 * time.Hour), true
	case "hourly":
		return prev.Add(time.Hour), true
	}
	return time.Time{}, false
}

func (s *Scheduler) reschedule(ctx context.Context, id int64, next time.Time) {
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE npc_goals SET fire_at = ? WHERE id = ?`,
			next.Unix(), id)
		return err
	}); err != nil {
		s.logger.Warn("scheduler: reschedule", "goal_id", id, "err", err)
	}
}

func (s *Scheduler) delete(ctx context.Context, id int64) {
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`DELETE FROM npc_goals WHERE id = ?`, id)
		return err
	}); err != nil {
		s.logger.Warn("scheduler: delete", "goal_id", id, "err", err)
	}
}
