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
	"sync"
	"time"

	"github.com/vaelen/wintermute/internal/llm"
	"github.com/vaelen/wintermute/internal/llm/budget"
	"github.com/vaelen/wintermute/internal/npc/memory"
	"github.com/vaelen/wintermute/internal/world/events"
)

// Snapshot is a defensive copy of the loop's recent observations and
// last broadcast reply, used by the @npc-debug admin command to
// inspect the live state of an NPC without touching its goroutine.
type Snapshot struct {
	NPCName      string
	RoomID       events.RoomID
	Observations []events.Event
	LastReply    string
}

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

	// Backend is the name of the LLM backend (e.g. "ollama", "fake").
	// Included in structured log fields per project convention.
	Backend string

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

	// Engage is consulted at observation time. When the NPC is currently
	// engaged with someone other than the event's Actor, say events are
	// dropped (the brush-off line is emitted by the npc registry's
	// SayObserver elsewhere). Nil means no filtering.
	Engage EngageLookup

	Logger *slog.Logger

	// snapMu guards recentObs and lastReply, populated from the tick
	// goroutine and read by the admin @npc-debug command via
	// Snapshot(). The mutex is intentionally separate from the loop's
	// own state because it is touched off-goroutine.
	snapMu    sync.Mutex
	recentObs []events.Event
	lastReply string
}

// Snapshot returns a defensive copy of the loop's recent observations
// and last broadcast reply. Safe to call from outside the loop
// goroutine.
func (l *Loop) Snapshot() Snapshot {
	l.snapMu.Lock()
	defer l.snapMu.Unlock()
	return Snapshot{
		NPCName:      l.NPCName,
		RoomID:       l.RoomID,
		Observations: append([]events.Event(nil), l.recentObs...),
		LastReply:    l.lastReply,
	}
}

// recordObservationsForSnapshot copies obs into the loop's
// recent-observations ring (last 16). Called from defaultTick after the
// budget check so the @npc-debug snapshot reflects what was actually
// processed.
func (l *Loop) recordObservationsForSnapshot(obs []events.Event) {
	l.snapMu.Lock()
	defer l.snapMu.Unlock()
	l.recentObs = append([]events.Event(nil), obs...)
	if len(l.recentObs) > 16 {
		l.recentObs = l.recentObs[len(l.recentObs)-16:]
	}
}

// recordLastReplyForSnapshot stores the most recent broadcast reply.
// Called from respond after the World.NPCSay call returns.
func (l *Loop) recordLastReplyForSnapshot(reply string) {
	l.snapMu.Lock()
	defer l.snapMu.Unlock()
	l.lastReply = reply
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
			if l.shouldDropEvent(e) {
				continue
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

// shouldDropEvent reports whether the loop should ignore e. Two cases
// drop:
//   - NPC-originated speech: KindSay / KindEmote whose Actor is any
//     NPC (including self). The world publishes a KindSay for the
//     NPC's own broadcast so admin scripts can observe it; without
//     the self-filter the loop would echo on its own output. Beyond
//     that, cross-NPC reactions are explicitly out of M7 scope: two
//     NPCs in the same room would otherwise cascade through unbounded
//     response calls. Tagging NPC speech via Extra["actor_kind"]="npc"
//     in world.NPCSay lets every NPC loop drop it cheaply.
//   - engagement filter: when the NPC is currently engaged with someone
//     other than the event's Actor, say events are dropped (the
//     brush-off line is emitted by the npc registry's SayObserver
//     elsewhere). Non-verbal events (arrive/depart/take/drop/etc.) are
//     not filtered: the NPC should still be aware of room state
//     changes during an engagement.
func (l *Loop) shouldDropEvent(e events.Event) bool {
	if e.Kind == events.KindSay || e.Kind == events.KindEmote {
		if l.NPCID != 0 && e.Actor == l.NPCID {
			return true
		}
		if kind, _ := e.Extra["actor_kind"].(string); kind == "npc" {
			return true
		}
	}
	if l.Engage == nil {
		return false
	}
	if e.Kind != events.KindSay && e.Kind != events.KindEmote {
		return false
	}
	participants, engaged := l.Engage.EngagedWith(l.NPCID)
	if !engaged {
		return false
	}
	for _, p := range participants {
		if p == e.Actor {
			return false
		}
	}
	return true
}
