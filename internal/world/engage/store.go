// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package engage

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/vaelen/wintermute/internal/store"
	"github.com/vaelen/wintermute/internal/world"
)

// LoadHosts reads every row of object_engage, resolves kind-defaults,
// and returns the resulting hosts. Order is unspecified.
func LoadHosts(ctx context.Context, db *store.DB) ([]*Host, error) {
	rows, err := db.Read().QueryContext(ctx, `
		SELECT object_id, kind, engage_verbs, disengage_verbs,
		       COALESCE(enter_msg, ''), COALESCE(present_msg, ''),
		       COALESCE(exit_msg, ''), COALESCE(prompt, ''),
		       policy
		  FROM object_engage`)
	if err != nil {
		return nil, fmt.Errorf("engage: load hosts: %w", err)
	}
	defer rows.Close()

	var out []*Host
	for rows.Next() {
		var (
			id     int64
			kind   string
			ev, dv string
			em, pm string
			xm, pr string
			policy string
		)
		if err := rows.Scan(&id, &kind, &ev, &dv, &em, &pm, &xm, &pr, &policy); err != nil {
			return nil, fmt.Errorf("engage: scan host: %w", err)
		}
		h := &Host{ObjectID: world.ObjectID(id), Kind: kind}
		if err := decodeStrings(ev, &h.EngageVerbs); err != nil {
			return nil, fmt.Errorf("engage: bad engage_verbs for %d: %w", id, err)
		}
		if err := decodeStrings(dv, &h.DisengageVerbs); err != nil {
			return nil, fmt.Errorf("engage: bad disengage_verbs for %d: %w", id, err)
		}
		h.EnterMsg = em
		h.PresentMsg = pm
		h.ExitMsg = xm
		h.Prompt = pr
		p, menu, derr := decodePolicyJSON(policy)
		if derr != nil {
			return nil, fmt.Errorf("engage: bad policy for %d: %w", id, derr)
		}
		h.Policy = p
		h.Menu = menu
		ApplyKindDefaults(h)
		out = append(out, h)
	}
	return out, rows.Err()
}

// HostCache is an in-memory copy of every engage host, indexed by
// ObjectID. Loaded once at startup; admin Lua's set_engage helper
// (Task 20) updates the cache atomically alongside the DB write.
type HostCache struct {
	mu   sync.RWMutex
	byID map[world.ObjectID]*Host
}

// NewHostCache returns an empty cache.
func NewHostCache() *HostCache {
	return &HostCache{byID: make(map[world.ObjectID]*Host)}
}

// Load replaces the cache contents with the result of LoadHosts.
func (c *HostCache) Load(ctx context.Context, db *store.DB) error {
	hosts, err := LoadHosts(ctx, db)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.byID = make(map[world.ObjectID]*Host, len(hosts))
	for _, h := range hosts {
		c.byID[h.ObjectID] = h
	}
	c.mu.Unlock()
	return nil
}

// Get returns the host for id, or nil.
func (c *HostCache) Get(id world.ObjectID) *Host {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.byID[id]
}

// Put inserts or replaces a host. Used by admin Lua.
func (c *HostCache) Put(h *Host) {
	c.mu.Lock()
	c.byID[h.ObjectID] = h
	c.mu.Unlock()
}

// Delete removes a host by ID. Used by admin Lua's clear_engage.
func (c *HostCache) Delete(id world.ObjectID) {
	c.mu.Lock()
	delete(c.byID, id)
	c.mu.Unlock()
}

// FilterRoomAndInventory returns hosts located in roomID or held by
// playerID. The locOf callback resolves each host's current location;
// hosts whose location lookup returns ok=false are skipped (orphans).
func (c *HostCache) FilterRoomAndInventory(
	roomID world.RoomID,
	playerID world.ObjectID,
	locOf func(world.ObjectID) (world.Location, bool),
) []*Host {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []*Host
	for _, h := range c.byID {
		loc, ok := locOf(h.ObjectID)
		if !ok {
			continue
		}
		if loc.RoomID == roomID || loc.HolderID == playerID {
			out = append(out, h)
		}
	}
	return out
}

func decodeStrings(s string, dst *[]string) error {
	if s == "" || s == "[]" {
		return nil
	}
	return json.Unmarshal([]byte(s), dst)
}

// kindDefaults is the table of per-kind fallbacks.
var kindDefaults = map[string]Host{
	KindNPC: {
		EngageVerbs:    []string{"talk to", "address"},
		DisengageVerbs: []string{"leave", "goodbye"},
		EnterMsg:       "{{player}} turns to {{host}}.",
		PresentMsg:     "talking with {{host}}",
		ExitMsg:        "{{player}} turns away from {{host}}.",
	},
	KindTerminal: {
		EngageVerbs:    []string{"use", "sit at"},
		DisengageVerbs: []string{"stand up", "step away"},
		EnterMsg:       "{{player}} sits down at {{host}}.",
		PresentMsg:     "at {{host}}",
		ExitMsg:        "{{player}} steps away from {{host}}.",
		Prompt:         "terminal> ",
	},
	KindMenuTerminal: {
		EngageVerbs:    []string{"use", "sit at"},
		DisengageVerbs: []string{"stand up", "step away"},
		EnterMsg:       "{{player}} sits down at {{host}}.",
		PresentMsg:     "at {{host}}",
		ExitMsg:        "{{player}} steps away from {{host}}.",
		// Prompt intentionally empty: the menu draws its own footer
		// ("Select: ") inside the rendered frame.
	},
}

// ApplyKindDefaults fills h's empty fields from the kind-default table.
// Called by LoadHosts; exported so admin Lua's set_engage helper can
// apply defaults when callers pass nil for individual fields.
func ApplyKindDefaults(h *Host) {
	d, ok := kindDefaults[h.Kind]
	if !ok {
		return
	}
	if len(h.EngageVerbs) == 0 {
		h.EngageVerbs = append([]string(nil), d.EngageVerbs...)
	}
	if len(h.DisengageVerbs) == 0 {
		h.DisengageVerbs = append([]string(nil), d.DisengageVerbs...)
	}
	if h.EnterMsg == "" {
		h.EnterMsg = d.EnterMsg
	}
	if h.PresentMsg == "" {
		h.PresentMsg = d.PresentMsg
	}
	if h.ExitMsg == "" {
		h.ExitMsg = d.ExitMsg
	}
	if h.Prompt == "" {
		h.Prompt = d.Prompt
	}
}
