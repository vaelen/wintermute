// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"

	"github.com/vaelen/wintermute/internal/npc"
	"github.com/vaelen/wintermute/internal/npc/loop"
	npcmemory "github.com/vaelen/wintermute/internal/npc/memory"
	scriptlua "github.com/vaelen/wintermute/internal/script/lua"
	"github.com/vaelen/wintermute/internal/world"
	worldcmd "github.com/vaelen/wintermute/internal/world/cmd"
	"github.com/vaelen/wintermute/internal/world/events"
)

// errLuaPoolNotReady is returned if an NPC tool fires before the lua
// pool has been wired into the adapter. In practice this never happens
// because the pool is set before any session is accepted; the guard is
// belt-and-suspenders.
var errLuaPoolNotReady = errors.New("loop: lua pool not yet wired")

// worldBroadcaster adapts *world.World to the loop.Broadcaster
// interface. Conversion between events.ObjectID (an int64 alias) and
// the named world.ObjectID type happens here so the loop package has
// no compile-time dependency on internal/world.
type worldBroadcaster struct{ w *world.World }

func (b worldBroadcaster) NPCSay(npcID events.ObjectID, text string) error {
	return b.w.NPCSay(world.ObjectID(npcID), text)
}

// luaToolsAdapter adapts the *scriptlua.ToolRegistry + *scriptlua.Pool
// pair to the loop.Tools interface. The pool is held via atomic.Pointer
// so it can be set after npc.Load runs (the npc registry, the lua API,
// and the lua pool form a cycle in cmd/wintermute/main.go; this
// indirection breaks it).
type luaToolsAdapter struct {
	reg  *scriptlua.ToolRegistry
	pool atomic.Pointer[scriptlua.Pool]
}

// setPool installs the pool after construction; safe to call once main
// has finished building the lua infrastructure.
func (a *luaToolsAdapter) setPool(p *scriptlua.Pool) {
	a.pool.Store(p)
}

func (a *luaToolsAdapter) Get(name string) loop.ToolMeta {
	if a == nil || a.reg == nil {
		return loop.ToolMeta{}
	}
	e := a.reg.Get(name)
	if e == nil {
		return loop.ToolMeta{}
	}
	return loop.ToolMeta{
		Name:        e.Name,
		Description: e.Description,
		Schema:      e.Schema,
	}
}

func (a *luaToolsAdapter) Invoke(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	pool := a.pool.Load()
	if pool == nil {
		return nil, errLuaPoolNotReady
	}
	return a.reg.Invoke(ctx, pool, name, args)
}

// npcDebugAdapter wraps *npc.Registry to satisfy worldcmd.NPCDebugger,
// translating between the npc package's domain types and the cmd-side
// view types so the cmd package stays free of an npc/loop/budget
// import.
type npcDebugAdapter struct{ r *npc.Registry }

func (a npcDebugAdapter) LookupByName(name string) (worldcmd.NPCInfo, bool) {
	n, ok := a.r.LookupByName(name)
	if !ok {
		return worldcmd.NPCInfo{}, false
	}
	tools := append([]string(nil), n.ToolNames...)
	return worldcmd.NPCInfo{
		ObjectID:  n.ObjectID,
		Name:      n.Name,
		Persona:   n.Persona,
		Model:     n.Model,
		GateModel: n.GateModel,
		ToolNames: tools,
	}, true
}

func (a npcDebugAdapter) LoopSnapshot(npcID world.ObjectID) (worldcmd.NPCSnapshot, bool) {
	snap, ok := a.r.LoopSnapshot(npcID)
	if !ok {
		return worldcmd.NPCSnapshot{}, false
	}
	obs := make([]worldcmd.NPCEvent, 0, len(snap.Observations))
	for _, e := range snap.Observations {
		obs = append(obs, worldcmd.NPCEvent{
			Kind:  string(e.Kind),
			Actor: int64(e.Actor),
			Text:  e.Text,
		})
	}
	return worldcmd.NPCSnapshot{
		NPCName:      snap.NPCName,
		RoomID:       int64(snap.RoomID),
		Observations: obs,
		LastReply:    snap.LastReply,
	}, true
}

func (a npcDebugAdapter) BudgetFor(npcID world.ObjectID) (worldcmd.NPCBudgetWindow, worldcmd.NPCBudgetWindow, worldcmd.NPCBudgetWindow, bool) {
	w1, w2, w3, ok := a.r.BudgetFor(npcID)
	if !ok {
		return worldcmd.NPCBudgetWindow{}, worldcmd.NPCBudgetWindow{}, worldcmd.NPCBudgetWindow{}, false
	}
	return worldcmd.NPCBudgetWindow{Limit: w1.Limit, Used: w1.Used},
		worldcmd.NPCBudgetWindow{Limit: w2.Limit, Used: w2.Used},
		worldcmd.NPCBudgetWindow{Limit: w3.Limit, Used: w3.Used},
		true
}

func (a npcDebugAdapter) GCMemories(ctx context.Context, floor float64) (int64, error) {
	var deleted int64
	err := a.r.DB().Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM npc_memories WHERE salience < ?`, floor)
		if err != nil {
			return err
		}
		deleted, _ = res.RowsAffected()
		return nil
	})
	return deleted, err
}

func (a npcDebugAdapter) SalienceFloor() float64 { return npcmemory.DecayFloor }
