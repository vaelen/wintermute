// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

// Package loop drives the per-NPC observation-and-act cycle: each NPC
// has one Loop goroutine that subscribes to its room's events.Bus,
// buffers observations until a debounce window expires, and calls
// Tick to decide whether (and how) to act on the batch.
//
// Subsequent M7 tasks fill in the gate-model and response-model flow
// inside Tick; the skeleton in this file is concerned only with
// subscription lifecycle and observation buffering.
package loop

import (
	"context"
	"time"

	"github.com/vaelen/wintermute/internal/world/events"
)

// Loop is one NPC's tick goroutine. Construct with the required fields
// filled in and call Run from a goroutine.
type Loop struct {
	RoomID   events.RoomID
	Bus      events.Bus
	Debounce time.Duration
	Tick     func(ctx context.Context, observations []events.Event)
}

// Run subscribes to the bus for RoomID and buffers events until
// Debounce elapses without a new event, then invokes Tick once with
// the buffered batch. Returns when ctx is cancelled or the
// subscription channel closes.
func (l *Loop) Run(ctx context.Context) {
	sub, cancel := l.Bus.Subscribe(l.RoomID)
	defer cancel()

	var (
		obs       []events.Event
		debounceT *time.Timer
	)
	armDebounce := func() {
		if debounceT == nil {
			debounceT = time.NewTimer(l.Debounce)
			return
		}
		if !debounceT.Stop() {
			select {
			case <-debounceT.C:
			default:
			}
		}
		debounceT.Reset(l.Debounce)
	}
	debounceC := func() <-chan time.Time {
		if debounceT == nil {
			return nil
		}
		return debounceT.C
	}

	for {
		select {
		case e, ok := <-sub:
			if !ok {
				return
			}
			obs = append(obs, e)
			armDebounce()
		case <-debounceC():
			if len(obs) > 0 {
				l.Tick(ctx, obs)
				obs = nil
			}
		case <-ctx.Done():
			return
		}
	}
}
