// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package memory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/vaelen/wintermute/internal/llm"
	"github.com/vaelen/wintermute/internal/world"
)

const (
	defaultShortTermCap = 20
	defaultIdleTimeout  = 5 * time.Minute
)

// StateConfig parameterises State. NPCID, LLM, Store, and Worker are
// required.
type StateConfig struct {
	NPCID        world.ObjectID
	LLM          llm.LLM
	Store        Store
	Worker       *Worker
	ShortTermCap int           // size of each per-player ring buffer
	IdleTimeout  time.Duration // per-(NPC, player) idle fallback
	Logger       *slog.Logger
}

// State bundles every per-NPC memory primitive: per-player short-term
// buffers, the summarisation worker, the long-term store, and the
// per-(NPC, player) idle timers that trigger a fallback drain when a
// conversation goes quiet without an explicit end event. One State per
// NPC; lives for the NPC's lifetime.
type State struct {
	cfg StateConfig

	mu       sync.Mutex
	convs    map[world.ObjectID]*conversation
	stopOnce sync.Once
	stopped  bool
}

// conversation is the per-(NPC, player) bundle: the rolling short-term
// buffer plus an idle timer that fires when the player goes quiet.
type conversation struct {
	buf  *ShortTerm
	idle *time.Timer
}

// NewState constructs a State from cfg, applying default values for any
// fields the caller left zero.
func NewState(cfg StateConfig) *State {
	if cfg.ShortTermCap <= 0 {
		cfg.ShortTermCap = defaultShortTermCap
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = defaultIdleTimeout
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &State{
		cfg:   cfg,
		convs: map[world.ObjectID]*conversation{},
	}
}

// Append records a turn in player's conversation buffer and resets the
// per-(NPC, player) idle timer. Safe to call from multiple goroutines.
func (s *State) Append(playerID world.ObjectID, t Turn) {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	c := s.convOrInitLocked(playerID)
	c.buf.Append(t)
	s.resetIdleLocked(playerID, c)
	s.mu.Unlock()
}

// Recent returns up to n most-recent turns from the player's
// conversation buffer.
func (s *State) Recent(playerID world.ObjectID, n int) []Turn {
	s.mu.Lock()
	c, ok := s.convs[playerID]
	s.mu.Unlock()
	if !ok {
		return nil
	}
	return c.buf.Recent(n)
}

// EndConversation drains the player's short-term buffer and submits it
// to the worker for summarisation. The buffer is left empty; a fresh
// conversation can begin with the next Append.
func (s *State) EndConversation(playerID world.ObjectID) {
	s.mu.Lock()
	c, ok := s.convs[playerID]
	if !ok {
		s.mu.Unlock()
		return
	}
	if c.idle != nil {
		c.idle.Stop()
		c.idle = nil
	}
	turns := c.buf.Drain()
	delete(s.convs, playerID)
	s.mu.Unlock()
	if len(turns) == 0 || s.cfg.Worker == nil {
		return
	}
	s.cfg.Worker.Submit(SummaryJob{PlayerID: playerID, Turns: turns})
}

// Retrieve embeds the recent short-term buffer for the player and asks
// the store for up to k memories above threshold (cosine similarity in
// [-1, 1]). A retrieval with no short-term context (no recent turns)
// returns nothing rather than embedding an empty query.
func (s *State) Retrieve(ctx context.Context, playerID world.ObjectID, k int, threshold float64) ([]Memory, error) {
	if s.cfg.LLM == nil || s.cfg.Store == nil {
		return nil, nil
	}
	recent := s.Recent(playerID, 5)
	if len(recent) == 0 {
		return nil, nil
	}
	var b strings.Builder
	for _, t := range recent {
		fmt.Fprintf(&b, "%s: %s\n", t.Speaker, t.Text)
	}
	vec, err := s.cfg.LLM.Embed(ctx, b.String())
	if err != nil {
		return nil, fmt.Errorf("memory state: embed query: %w", err)
	}
	mems, err := s.cfg.Store.Search(ctx, s.cfg.NPCID, vec, k, threshold)
	if err != nil {
		return nil, fmt.Errorf("memory state: search: %w", err)
	}
	return mems, nil
}

// BumpSalience increments the salience counter for every retrieved
// memory. Errors are logged and otherwise ignored; salience drift is a
// non-load-bearing signal.
func (s *State) BumpSalience(ctx context.Context, mems []Memory) {
	if s.cfg.Store == nil {
		return
	}
	for _, m := range mems {
		if err := s.cfg.Store.BumpSalience(ctx, m.ID); err != nil {
			s.cfg.Logger.Warn("memory state: bump salience",
				"npc", s.cfg.NPCID, "memory_id", m.ID, "err", err)
		}
	}
}

// Stop drains every active conversation through the worker and waits for
// the worker to finish processing them. Idle timers are cancelled. After
// Stop returns, further Append/EndConversation calls become no-ops.
func (s *State) Stop(ctx context.Context) error {
	var stopErr error
	s.stopOnce.Do(func() {
		s.mu.Lock()
		s.stopped = true
		drains := make([]SummaryJob, 0, len(s.convs))
		for playerID, c := range s.convs {
			if c.idle != nil {
				c.idle.Stop()
			}
			turns := c.buf.Drain()
			if len(turns) > 0 {
				drains = append(drains, SummaryJob{PlayerID: playerID, Turns: turns})
			}
		}
		s.convs = map[world.ObjectID]*conversation{}
		s.mu.Unlock()
		if s.cfg.Worker != nil {
			for _, j := range drains {
				s.cfg.Worker.Submit(j)
			}
			stopErr = s.cfg.Worker.Stop(ctx)
		}
	})
	return stopErr
}

func (s *State) convOrInitLocked(playerID world.ObjectID) *conversation {
	c, ok := s.convs[playerID]
	if ok {
		return c
	}
	c = &conversation{buf: NewShortTerm(s.cfg.ShortTermCap)}
	s.convs[playerID] = c
	return c
}

// resetIdleLocked (re)arms the per-(NPC, player) idle timer. Caller must
// hold s.mu. The timer fires in a fresh goroutine and re-enters EndConversation,
// which locks s.mu — so we deliberately do NOT call EndConversation directly.
func (s *State) resetIdleLocked(playerID world.ObjectID, c *conversation) {
	if c.idle != nil {
		c.idle.Stop()
	}
	c.idle = time.AfterFunc(s.cfg.IdleTimeout, func() {
		s.EndConversation(playerID)
	})
}

// ErrStateStopped is returned (or wrapped) when an operation is invoked
// after Stop has run. Currently unused externally; reserved for future
// callers that need to distinguish stopped vs. transient errors.
var ErrStateStopped = errors.New("memory state: stopped")
