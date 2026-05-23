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
	loops map[events.ObjectID]*loopEntry

	wg sync.WaitGroup
}

// loopEntry pairs a live Loop with the cancel func that stops its
// goroutine. The Loop pointer is kept so the admin @npc-debug command
// can take a Snapshot off the live loop.
type loopEntry struct {
	loop   *Loop
	cancel context.CancelFunc
}

// NewManager constructs a Manager whose per-loop ctx derives from parent.
func NewManager(parent context.Context) *Manager {
	return &Manager{
		parent: parent,
		loops:  map[events.ObjectID]*loopEntry{},
	}
}

// Add starts l on a goroutine derived from the manager's parent ctx.
// If an existing loop is already registered for the same NPCID, its
// ctx is cancelled first.
//
// wg.Add must happen INSIDE the mutex: Stop also holds the mutex while
// draining the map, so once Stop sees the new entry it is guaranteed to
// also see the incremented wg counter when it calls wg.Wait. If we did
// the Add after Unlock, a Stop racing between Unlock and wg.Add(1)
// could observe a zero counter and return before the new goroutine
// even started, orphaning it past shutdown.
func (m *Manager) Add(l *Loop) {
	m.mu.Lock()
	if prev, ok := m.loops[l.NPCID]; ok {
		prev.cancel()
	}
	ctx, cancel := context.WithCancel(m.parent)
	m.loops[l.NPCID] = &loopEntry{loop: l, cancel: cancel}
	m.wg.Add(1)
	m.mu.Unlock()

	go func() {
		defer m.wg.Done()
		l.Run(ctx)
	}()
}

// Remove stops the loop for npcID. No-op if absent.
func (m *Manager) Remove(npcID events.ObjectID) {
	m.mu.Lock()
	entry, ok := m.loops[npcID]
	delete(m.loops, npcID)
	m.mu.Unlock()
	if ok {
		entry.cancel()
	}
}

// Get returns the live Loop for npcID, or nil if absent. Used by the
// admin @npc-debug command to take a Snapshot.
func (m *Manager) Get(npcID events.ObjectID) *Loop {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.loops[npcID]; ok {
		return e.loop
	}
	return nil
}

// Stop cancels every loop and waits for all goroutines to return.
func (m *Manager) Stop() {
	m.mu.Lock()
	for id, entry := range m.loops {
		entry.cancel()
		delete(m.loops, id)
	}
	m.mu.Unlock()
	m.wg.Wait()
}
