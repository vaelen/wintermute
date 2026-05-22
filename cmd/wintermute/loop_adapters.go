// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/vaelen/wintermute/internal/npc/loop"
	scriptlua "github.com/vaelen/wintermute/internal/script/lua"
	"github.com/vaelen/wintermute/internal/world"
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
