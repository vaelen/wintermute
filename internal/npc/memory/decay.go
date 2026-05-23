// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package memory

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/vaelen/wintermute/internal/store"
)

// DecayFactor is the multiplicative drop applied to every memory's
// salience per daily tick. 0.95 means a memory loses ~5% per day;
// combined with the salience bump on retrieval, frequently referenced
// memories stay high while ignored ones drift toward DecayFloor.
const DecayFactor = 0.95

// DecayFloor is the threshold below which a memory becomes eligible
// for deletion via @gc-memories. The decay job itself never deletes;
// it only multiplies salience by DecayFactor.
const DecayFloor = 0.1

// DecayJob runs DecayOnce on a periodic ticker until ctx is done.
// Interval defaults to 24h; Wait is the delay before the first tick
// and defaults to Interval.
type DecayJob struct {
	DB       *store.DB
	Logger   *slog.Logger
	Interval time.Duration
	Wait     time.Duration
}

// Run is the goroutine entry point. Returns when ctx is done.
func (j *DecayJob) Run(ctx context.Context) {
	logger := j.Logger
	if logger == nil {
		logger = slog.Default()
	}
	interval := j.Interval
	if interval == 0 {
		interval = 24 * time.Hour
	}
	wait := j.Wait
	if wait == 0 {
		wait = interval
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := DecayOnce(ctx, j.DB); err != nil {
				logger.Warn("memory decay: tick failed", "err", err)
			}
			t.Reset(interval)
		}
	}
}

// DecayOnce multiplies every npc_memories row's salience by DecayFactor
// in a single SQL statement, routed through the writer goroutine.
func DecayOnce(ctx context.Context, db *store.DB) error {
	err := db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE npc_memories SET salience = salience * ?`,
			DecayFactor,
		)
		return err
	})
	if err != nil {
		return fmt.Errorf("memory: decay: %w", err)
	}
	return nil
}
