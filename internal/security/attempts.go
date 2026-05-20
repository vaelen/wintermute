// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package security

import (
	"net/netip"
	"sync"
	"time"
)

// attemptTracker is the per-IP failed-password sliding-window counter.
// Memory is bounded by the configured threshold times the number of
// active IPs — every Record call trims entries older than the window
// before deciding whether to flag.
type attemptTracker struct {
	mu        sync.Mutex
	now       func() time.Time
	threshold int
	window    time.Duration
	hits      map[netip.Addr][]time.Time
}

func newAttemptTracker(threshold int, window time.Duration, now func() time.Time) *attemptTracker {
	if now == nil {
		now = time.Now
	}
	if threshold < 1 {
		threshold = 1
	}
	if window <= 0 {
		window = time.Minute
	}
	return &attemptTracker{
		now:       now,
		threshold: threshold,
		window:    window,
		hits:      make(map[netip.Addr][]time.Time),
	}
}

// record appends a hit for addr at the current instant, trims entries
// older than the window, and reports whether the threshold has been
// crossed *with this hit*. The caller is expected to flag the IP only
// on a true return.
func (t *attemptTracker) record(addr netip.Addr) bool {
	now := t.now()
	cutoff := now.Add(-t.window)
	t.mu.Lock()
	defer t.mu.Unlock()
	hits := t.hits[addr]
	pruned := hits[:0]
	for _, h := range hits {
		if h.After(cutoff) {
			pruned = append(pruned, h)
		}
	}
	pruned = append(pruned, now)
	t.hits[addr] = pruned
	return len(pruned) >= t.threshold
}

// reset drops the per-IP history. Called from Flag so a freshly flagged
// IP starts clean if the deny TTL expires before the next attempt.
func (t *attemptTracker) reset(addr netip.Addr) {
	t.mu.Lock()
	delete(t.hits, addr)
	t.mu.Unlock()
}

// gc drops cold entries — every per-IP slice whose newest hit is older
// than the window. Returns the number of IPs removed.
func (t *attemptTracker) gc() int {
	now := t.now()
	cutoff := now.Add(-t.window)
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for addr, hits := range t.hits {
		if len(hits) == 0 {
			delete(t.hits, addr)
			n++
			continue
		}
		if !hits[len(hits)-1].After(cutoff) {
			delete(t.hits, addr)
			n++
		}
	}
	return n
}
