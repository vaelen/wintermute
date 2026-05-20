// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package security

import (
	"net/netip"
	"sync"
	"time"
)

// denyKind tags a cache entry as either temporary (expires_at populated)
// or permanent (no expiry).
type denyKind int

const (
	denyTemp denyKind = iota
	denyPermanent
)

// denyEntry is the in-memory representation of a row in ip_denials.
// expiresAt is the unix-second deadline for temporary rows; permanent
// rows have expiresAt = 0 and kind = denyPermanent.
type denyEntry struct {
	kind      denyKind
	expiresAt int64
	reason    string
	automatic bool
}

// ipCache mirrors ip_denials in memory for cheap accept-time lookups.
// Reads take RLock; mutations take Lock. Lazy expiry on every IsDenied
// call protects correctness between ticker-driven janitor sweeps.
type ipCache struct {
	mu  sync.RWMutex
	now func() time.Time
	m   map[netip.Addr]denyEntry
}

func newIPCache(now func() time.Time) *ipCache {
	if now == nil {
		now = time.Now
	}
	return &ipCache{
		now: now,
		m:   make(map[netip.Addr]denyEntry),
	}
}

// set inserts or replaces the entry for addr. Caller must hold an
// external write barrier (the Service mutex) — the cache's own lock
// only protects internal consistency.
func (c *ipCache) set(addr netip.Addr, e denyEntry) {
	c.mu.Lock()
	c.m[addr] = e
	c.mu.Unlock()
}

// remove drops the entry for addr (if any).
func (c *ipCache) remove(addr netip.Addr) {
	c.mu.Lock()
	delete(c.m, addr)
	c.mu.Unlock()
}

// flushTemp drops every temporary entry. Returns the count removed.
func (c *ipCache) flushTemp() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for addr, e := range c.m {
		if e.kind == denyTemp {
			delete(c.m, addr)
			n++
		}
	}
	return n
}

// snapshot returns a stable list of all entries paired with their
// addresses. Used by ListDenials.
func (c *ipCache) snapshot() []ipCacheItem {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]ipCacheItem, 0, len(c.m))
	for addr, e := range c.m {
		out = append(out, ipCacheItem{addr: addr, entry: e})
	}
	return out
}

// isDenied reports whether addr is currently in the deny list. Lazy
// expiry: a temporary entry whose deadline has passed is removed inline
// and reported as not-denied.
func (c *ipCache) isDenied(addr netip.Addr) bool {
	c.mu.RLock()
	e, ok := c.m[addr]
	c.mu.RUnlock()
	if !ok {
		return false
	}
	if e.kind == denyPermanent {
		return true
	}
	if c.now().Unix() < e.expiresAt {
		return true
	}
	c.mu.Lock()
	if cur, still := c.m[addr]; still && cur.kind == denyTemp && c.now().Unix() >= cur.expiresAt {
		delete(c.m, addr)
	}
	c.mu.Unlock()
	return false
}

// expiredAddrs returns every temporary address whose deadline has passed
// at the supplied wall clock instant.
func (c *ipCache) expiredAddrs(at time.Time) []netip.Addr {
	c.mu.RLock()
	defer c.mu.RUnlock()
	deadline := at.Unix()
	var out []netip.Addr
	for addr, e := range c.m {
		if e.kind == denyTemp && deadline >= e.expiresAt {
			out = append(out, addr)
		}
	}
	return out
}

// ipCacheItem is the snapshot payload used by the list-and-render path.
type ipCacheItem struct {
	addr  netip.Addr
	entry denyEntry
}
