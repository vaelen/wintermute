// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package memory

import (
	"sync"
	"time"
)

// Turn is one utterance in a conversation between an NPC and a player.
// Speaker is the player's display name, the literal "<npc>" for the NPC's
// own replies, or "<system>" for synthesised context entries.
type Turn struct {
	Speaker string
	Text    string
	At      time.Time
}

// ShortTerm is a bounded, thread-safe ring buffer of recent turns for a
// single NPC-player conversation. Oldest entries are evicted once cap is
// exceeded. The buffer is wiped on process restart; persistence happens at
// the long-term layer once a conversation ends and the worker summarises
// it.
type ShortTerm struct {
	mu    sync.Mutex
	turns []Turn
	cap   int
}

// NewShortTerm returns a buffer that will retain at most cap most-recent
// turns. cap must be positive; non-positive values default to 1 to avoid
// confusingly silent "no-op buffer" behaviour for callers who pass 0.
func NewShortTerm(cap int) *ShortTerm {
	if cap < 1 {
		cap = 1
	}
	return &ShortTerm{cap: cap}
}

// Append records t. If the buffer is at capacity, the oldest turn is
// dropped.
func (s *ShortTerm) Append(t Turn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.turns) >= s.cap {
		s.turns = append(s.turns[:0], s.turns[len(s.turns)-s.cap+1:]...)
	}
	s.turns = append(s.turns, t)
}

// Recent returns up to n most-recent turns, oldest first. A snapshot copy
// is returned so callers can iterate without holding the mutex.
func (s *ShortTerm) Recent(n int) []Turn {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n <= 0 || len(s.turns) == 0 {
		return nil
	}
	if n > len(s.turns) {
		n = len(s.turns)
	}
	out := make([]Turn, n)
	copy(out, s.turns[len(s.turns)-n:])
	return out
}

// Drain returns every turn in order and empties the buffer. Used when a
// conversation ends and the contents are about to be handed to the
// summarisation worker.
func (s *ShortTerm) Drain() []Turn {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.turns) == 0 {
		return nil
	}
	out := make([]Turn, len(s.turns))
	copy(out, s.turns)
	s.turns = s.turns[:0]
	return out
}

// Len returns the current number of buffered turns.
func (s *ShortTerm) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.turns)
}

// Empty reports whether the buffer holds no turns.
func (s *ShortTerm) Empty() bool {
	return s.Len() == 0
}
