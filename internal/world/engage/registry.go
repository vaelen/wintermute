// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package engage

import (
	"errors"
	"sync"

	"github.com/vaelen/wintermute/internal/world"
)

// Errors returned by Registry.Open.
var (
	ErrHostBusy       = errors.New("engage: host already engaged")
	ErrAlreadyEngaged = errors.New("engage: participant already engaged elsewhere")
)

// Registry is the process-wide table of live engagements. Lookups are
// O(1) by host or participant. Concurrency: a single sync.RWMutex
// guards both maps; Open/Close take the write lock, lookups the read lock.
type Registry struct {
	mu     sync.RWMutex
	byHost map[world.ObjectID]*Engagement
	byPart map[string]*Engagement // keyed by Participant.SessionID
}

// NewRegistry returns an empty Registry ready for use.
func NewRegistry() *Registry {
	return &Registry{
		byHost: make(map[world.ObjectID]*Engagement),
		byPart: make(map[string]*Engagement),
	}
}

// Open creates and indexes a new engagement. Returns ErrHostBusy if the
// host already has an open engagement; ErrAlreadyEngaged if the
// participant is already in another engagement. On success, the handler's
// OnOpen is invoked synchronously before Open returns.
func (r *Registry) Open(host *Host, handler Handler, p *Participant) (*Engagement, error) {
	r.mu.Lock()
	if _, busy := r.byHost[host.ObjectID]; busy {
		r.mu.Unlock()
		return nil, ErrHostBusy
	}
	if _, busy := r.byPart[p.SessionID]; busy {
		r.mu.Unlock()
		return nil, ErrAlreadyEngaged
	}
	eng := &Engagement{
		Host:         host,
		Handler:      handler,
		Participants: []*Participant{p},
	}
	r.byHost[host.ObjectID] = eng
	r.byPart[p.SessionID] = eng
	r.mu.Unlock()

	handler.OnOpen(p)
	return eng, nil
}

// Close removes the engagement from the registry and invokes the
// handler's OnClose for every participant. Safe to call twice — the
// second call is a no-op (the engagement is no longer indexed).
func (r *Registry) Close(eng *Engagement, reason CloseReason) {
	if eng == nil {
		return
	}
	r.mu.Lock()
	if r.byHost[eng.Host.ObjectID] != eng {
		// Already closed (or never registered with this registry).
		r.mu.Unlock()
		return
	}
	delete(r.byHost, eng.Host.ObjectID)
	for _, p := range eng.Participants {
		delete(r.byPart, p.SessionID)
	}
	r.mu.Unlock()

	for _, p := range eng.Participants {
		eng.Handler.OnClose(p, reason)
	}
}

// HostEngagement returns the live engagement for the given host, or nil.
func (r *Registry) HostEngagement(id world.ObjectID) *Engagement {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byHost[id]
}

// ParticipantEngagement returns the live engagement for the given session,
// or nil.
func (r *Registry) ParticipantEngagement(sessionID string) *Engagement {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byPart[sessionID]
}

// ParticipantEngagementByPlayer returns the engagement whose participant
// list contains a participant with the given PlayerID, or nil. M5.7 has
// capacity 1 per host so the result is unambiguous.
func (r *Registry) ParticipantEngagementByPlayer(pid world.ObjectID) *Engagement {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, e := range r.byHost {
		for _, p := range e.Participants {
			if p.PlayerID == pid {
				return e
			}
		}
	}
	return nil
}
