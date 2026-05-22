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
	"github.com/vaelen/wintermute/internal/llm/budget"
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

// Tools is the minimum surface the loop needs to look up and invoke a
// tool by name. The production *lua.ToolRegistry is wrapped by a small
// adapter in main.go that satisfies this interface; keeping it as an
// interface here means internal/npc/loop has no compile-time
// dependency on internal/script/lua.
type Tools interface {
	Get(name string) ToolMeta
	Invoke(ctx context.Context, name string, args map[string]any) (map[string]any, error)
}

// ToolMeta is the subset of a registered tool's metadata the response
// model needs to know about (the fields filled into an llm.ToolDef). A
// zero-value ToolMeta (Name == "") means "no such tool".
type ToolMeta struct {
	Name        string
	Description string
	Schema      map[string]any
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

	// Tools is the per-NPC tool registry surface. Nil suppresses the
	// ToolDefs sent to the response model (no tools advertised); a
	// well-behaved model will then never return a ToolCall, but if it
	// does the call fails with a "registry not configured" error fed
	// back via a RoleTool message.
	Tools Tools

	// ToolNames is the NPC's allow-list: only these tool names are
	// exposed to the response model and accepted from ToolCall replies.
	// An empty list disables tools for this NPC.
	ToolNames []string

	// MaxToolDepth caps the number of nested tool-call rounds before the
	// response is broadcast as-is.
	MaxToolDepth int

	// Budget enforces per-NPC token budgets across minute/hour/day
	// windows. Nil disables budget enforcement (every Allow returns true
	// implicitly). Production wires this in main.go via budget.Manager.
	Budget *budget.Manager

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
