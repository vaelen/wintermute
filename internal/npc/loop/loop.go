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
	"log/slog"
	"time"

	"github.com/vaelen/wintermute/internal/llm"
	"github.com/vaelen/wintermute/internal/npc/memory"
	"github.com/vaelen/wintermute/internal/world/events"
)

// Broadcaster is the minimum World surface the loop needs to publish an
// NPC reply. *world.World satisfies this via a small adapter (see
// cmd/wintermute wiring): the adapter converts events.ObjectID (an
// int64 alias) into the named world.ObjectID type. Tests can swap in a
// stub that captures broadcasts without standing up the full world.
type Broadcaster interface {
	NPCSay(npcID events.ObjectID, text string) error
}

// Loop is one NPC's tick goroutine. Construct with the required fields
// filled in and call Run from a goroutine. Leave Tick nil to use the
// production dispatch path (gate-model decision; response in later M7
// tasks); set Tick explicitly only for tests that want to intercept the
// buffered batch.
type Loop struct {
	RoomID   events.RoomID
	Bus      events.Bus
	Debounce time.Duration
	Tick     func(ctx context.Context, observations []events.Event)

	NPCID   events.ObjectID
	NPCName string
	Persona string

	LLM       llm.LLM
	ChatModel string
	GateModel string

	// Memory is the per-NPC short+long-term memory wrapper. Nil disables
	// memory operations (retrieve, append) — the loop still runs but
	// without context.
	Memory *memory.State

	// World is the broadcast surface the loop uses to publish NPC
	// replies. Nil is permitted only for unit tests that don't need to
	// observe the broadcast end-to-end; production always wires it.
	World Broadcaster

	// MaxToolDepth caps the number of nested tool-call rounds before the
	// response is broadcast as-is. Task 8 uses this; Task 7 just stores
	// it.
	MaxToolDepth int

	Logger *slog.Logger
}

// Run subscribes to the bus for RoomID and buffers events until
// Debounce elapses without a new event, then invokes Tick once with
// the buffered batch. Returns when ctx is cancelled or the
// subscription channel closes.
func (l *Loop) Run(ctx context.Context) {
	if l.Tick == nil {
		l.Tick = l.defaultTick
	}
	if l.Logger == nil {
		l.Logger = slog.Default()
	}
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
