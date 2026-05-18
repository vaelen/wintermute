// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package lua

import (
	lua "github.com/yuin/gopher-lua"

	ftnnetworks "github.com/vaelen/wintermute/internal/ftn/networks"
)

// luaFTNNetworkList binds wintermute.ftn.network.list() → array of
// network tables.
func (a *API) luaFTNNetworkList(L *lua.LState) int {
	rows, err := a.Backend.ListNetworks(a.Ctx)
	if err != nil {
		return pushError(L, err)
	}
	out := L.NewTable()
	for i, n := range rows {
		out.RawSetInt(i+1, pushNetworkTable(L, n))
	}
	L.Push(out)
	return 1
}

// luaFTNNetworkGet binds wintermute.ftn.network.get(slug) → table, or
// nil if absent.
func (a *API) luaFTNNetworkGet(L *lua.LState) int {
	slug := L.CheckString(1)
	n, err := a.Backend.GetNetwork(a.Ctx, slug)
	if err != nil {
		if isNotFound(err) {
			L.Push(lua.LNil)
			return 1
		}
		return pushError(L, err)
	}
	L.Push(pushNetworkTable(L, n))
	return 1
}

// luaFTNNetworkSetDefault binds wintermute.ftn.network.set_default(slug).
func (a *API) luaFTNNetworkSetDefault(L *lua.LState) int {
	slug := L.CheckString(1)
	return pushError(L, a.Backend.SetDefaultNetwork(a.Ctx, slug))
}

// pushNetworkTable converts an ftnnetworks.Network into the Lua table
// shape used by wintermute.ftn.network.get / list.
func pushNetworkTable(L *lua.LState, n ftnnetworks.Network) *lua.LTable {
	t := L.NewTable()
	L.SetField(t, "id", lua.LNumber(n.ID))
	L.SetField(t, "slug", lua.LString(n.Slug))
	L.SetField(t, "name", lua.LString(n.Name))
	L.SetField(t, "domain", lua.LString(n.Domain))
	L.SetField(t, "our_addr", lua.LString(n.OurAddr))
	L.SetField(t, "is_default", lua.LBool(n.IsDefault))
	return t
}
