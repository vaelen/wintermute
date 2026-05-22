// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package loop

import (
	"context"
	"sync"

	"github.com/vaelen/wintermute/internal/world/events"
)

// Manager owns the per-NPC Loop goroutines started by the npc.Registry.
// Add starts a new loop (replacing any prior one with the same npcID).
// Stop cancels every loop and waits for them to exit; safe to call
// once at shutdown.
type Manager struct {
	parent context.Context

	mu    sync.Mutex
	loops map[events.ObjectID]context.CancelFunc

	wg sync.WaitGroup
}

// NewManager constructs a Manager whose per-loop ctx derives from parent.
func NewManager(parent context.Context) *Manager {
	return &Manager{
		parent: parent,
		loops:  map[events.ObjectID]context.CancelFunc{},
	}
}

// Add starts l on a goroutine derived from the manager's parent ctx.
// If an existing loop is already registered for the same NPCID, its
// ctx is cancelled first.
func (m *Manager) Add(l *Loop) {
	m.mu.Lock()
	if cancel, ok := m.loops[l.NPCID]; ok {
		cancel()
	}
	ctx, cancel := context.WithCancel(m.parent)
	m.loops[l.NPCID] = cancel
	m.mu.Unlock()

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		l.Run(ctx)
	}()
}

// Remove stops the loop for npcID. No-op if absent.
func (m *Manager) Remove(npcID events.ObjectID) {
	m.mu.Lock()
	cancel, ok := m.loops[npcID]
	delete(m.loops, npcID)
	m.mu.Unlock()
	if ok {
		cancel()
	}
}

// Stop cancels every loop and waits for all goroutines to return.
func (m *Manager) Stop() {
	m.mu.Lock()
	for id, cancel := range m.loops {
		cancel()
		delete(m.loops, id)
	}
	m.mu.Unlock()
	m.wg.Wait()
}
