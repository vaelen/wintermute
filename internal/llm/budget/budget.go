// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

// Package budget tracks per-NPC token consumption across fixed
// minute/hour/day windows. A request is Allow'd only if every window
// has room for its estimated cost; Record decrements available room
// after the LLM call returns the actual usage. Windows roll over
// lazily: the next Record or Allow that lands in a new window resets
// Used and advances StartedAt.
package budget

import "time"

// Window is a single fixed time window with a token cap.
type Window struct {
	Limit     int
	Used      int
	StartedAt time.Time
	Duration  time.Duration
}

// Allow reports whether estimate additional tokens fit in the window
// at time now (after lazy rollover).
func (w *Window) Allow(estimate int, now time.Time) bool {
	w.rollover(now)
	return w.Used+estimate <= w.Limit
}

// Record adds tokens to Used (after lazy rollover).
func (w *Window) Record(tokens int, now time.Time) {
	w.rollover(now)
	w.Used += tokens
}

// rollover resets Used and advances StartedAt when the previous
// window has expired (or was never started).
func (w *Window) rollover(now time.Time) {
	if w.StartedAt.IsZero() || now.Sub(w.StartedAt) >= w.Duration {
		w.StartedAt = now
		w.Used = 0
	}
}

// Defaults bundles the three per-window token limits.
type Defaults struct {
	Minute, Hour, Day int
}

// NPCBudget is the three-window per-NPC tracker.
type NPCBudget struct {
	Minute Window
	Hour   Window
	Day    Window
}

// NewNPCBudget returns a fresh budget with all three windows opened at
// now and the given limits.
func NewNPCBudget(now time.Time, d Defaults) *NPCBudget {
	return &NPCBudget{
		Minute: Window{Limit: d.Minute, StartedAt: now, Duration: time.Minute},
		Hour:   Window{Limit: d.Hour, StartedAt: now, Duration: time.Hour},
		Day:    Window{Limit: d.Day, StartedAt: now, Duration: 24 * time.Hour},
	}
}

// Allow requires room in every window. Estimate is the predicted token
// cost (in + out) of the impending LLM call.
func (b *NPCBudget) Allow(estimate int, now time.Time) bool {
	return b.Minute.Allow(estimate, now) &&
		b.Hour.Allow(estimate, now) &&
		b.Day.Allow(estimate, now)
}

// Record charges all three windows by usageIn + usageOut.
func (b *NPCBudget) Record(usageIn, usageOut int, now time.Time) {
	total := usageIn + usageOut
	b.Minute.Record(total, now)
	b.Hour.Record(total, now)
	b.Day.Record(total, now)
}
