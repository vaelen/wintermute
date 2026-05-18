// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package lua

import (
	lua "github.com/yuin/gopher-lua"

	"github.com/vaelen/wintermute/internal/boards"
	worldapi "github.com/vaelen/wintermute/internal/world/api"
)

// luaBoardCreate binds wintermute.board.create({...}) → id.
func (a *API) luaBoardCreate(L *lua.LState) int {
	tbl := L.CheckTable(1)
	spec := worldapi.BoardSpec{
		Slug:          optString(tbl, "slug"),
		Name:          optString(tbl, "name"),
		Description:   optString(tbl, "description"),
		NetworkSlug:   optString(tbl, "network"),
		AreaTag:       optString(tbl, "area_tag"),
		ReadMinLevel:  int(optInt64(tbl, "read_min_level")),
		PostMinLevel:  int(optInt64(tbl, "post_min_level")),
		AdminMinLevel: int(optInt64(tbl, "admin_min_level")),
	}
	id, err := a.Backend.CreateBoard(a.Ctx, spec)
	if err != nil {
		return pushError(L, err)
	}
	L.Push(lua.LNumber(id))
	return 1
}

// luaBoardDelete binds wintermute.board.delete(slug).
func (a *API) luaBoardDelete(L *lua.LState) int {
	slug := L.CheckString(1)
	return pushError(L, a.Backend.DeleteBoard(a.Ctx, slug))
}

// luaBoardGet binds wintermute.board.get(slug) → table, or nil if absent.
func (a *API) luaBoardGet(L *lua.LState) int {
	slug := L.CheckString(1)
	b, err := a.Backend.GetBoard(a.Ctx, slug)
	if err != nil {
		if isNotFound(err) {
			L.Push(lua.LNil)
			return 1
		}
		return pushError(L, err)
	}
	L.Push(pushBoardTable(L, b))
	return 1
}

// luaBoardList binds wintermute.board.list() → array of board tables.
func (a *API) luaBoardList(L *lua.LState) int {
	rows, err := a.Backend.ListAllBoards(a.Ctx)
	if err != nil {
		return pushError(L, err)
	}
	out := L.NewTable()
	for i, b := range rows {
		out.RawSetInt(i+1, pushBoardTable(L, b))
	}
	L.Push(out)
	return 1
}

// luaBoardSetACLs binds wintermute.board.set_acls(slug, {read?, post?,
// admin?}). Each field is optional; only supplied fields change.
func (a *API) luaBoardSetACLs(L *lua.LState) int {
	slug := L.CheckString(1)
	opts := L.CheckTable(2)
	var acls worldapi.BoardACLs
	if v := opts.RawGetString("read"); v != lua.LNil {
		if n, ok := v.(lua.LNumber); ok {
			lv := int(n)
			acls.ReadMinLevel = &lv
		}
	}
	if v := opts.RawGetString("post"); v != lua.LNil {
		if n, ok := v.(lua.LNumber); ok {
			lv := int(n)
			acls.PostMinLevel = &lv
		}
	}
	if v := opts.RawGetString("admin"); v != lua.LNil {
		if n, ok := v.(lua.LNumber); ok {
			lv := int(n)
			acls.AdminMinLevel = &lv
		}
	}
	return pushError(L, a.Backend.SetBoardACLs(a.Ctx, slug, acls))
}

// pushBoardTable converts a boards.Board into the Lua table shape used
// by wintermute.board.get / list.
func pushBoardTable(L *lua.LState, b boards.Board) *lua.LTable {
	t := L.NewTable()
	L.SetField(t, "id", lua.LNumber(b.ID))
	L.SetField(t, "network_id", lua.LNumber(b.NetworkID))
	L.SetField(t, "slug", lua.LString(b.Slug))
	L.SetField(t, "name", lua.LString(b.Name))
	L.SetField(t, "description", lua.LString(b.Description))
	if b.AreaTag.Valid {
		L.SetField(t, "area_tag", lua.LString(b.AreaTag.String))
	}
	L.SetField(t, "read_min_level", lua.LNumber(b.ReadMinLevel))
	L.SetField(t, "post_min_level", lua.LNumber(b.PostMinLevel))
	L.SetField(t, "admin_min_level", lua.LNumber(b.AdminMinLevel))
	return t
}
