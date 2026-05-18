// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package lua

import (
	lua "github.com/yuin/gopher-lua"

	"github.com/vaelen/wintermute/internal/files"
	worldapi "github.com/vaelen/wintermute/internal/world/api"
)

// luaFileAreaCreate binds wintermute.file.area.create({...}).
func (a *API) luaFileAreaCreate(L *lua.LState) int {
	tbl := L.CheckTable(1)
	spec := worldapi.FileAreaSpec{
		Slug:          optString(tbl, "slug"),
		Name:          optString(tbl, "name"),
		Description:   optString(tbl, "description"),
		ReadMinLevel:  int(optInt64(tbl, "read_min_level")),
		WriteMinLevel: int(optInt64(tbl, "write_min_level")),
		AdminMinLevel: int(optInt64(tbl, "admin_min_level")),
	}
	return pushError(L, a.Backend.CreateFileArea(a.Ctx, spec))
}

// luaFileAreaDelete binds wintermute.file.area.delete(slug).
func (a *API) luaFileAreaDelete(L *lua.LState) int {
	slug := L.CheckString(1)
	return pushError(L, a.Backend.DeleteFileArea(a.Ctx, slug))
}

// luaFileAreaList binds wintermute.file.area.list() → array of tables.
func (a *API) luaFileAreaList(L *lua.LState) int {
	rows, err := a.Backend.ListFileAreas(a.Ctx)
	if err != nil {
		return pushError(L, err)
	}
	out := L.NewTable()
	for i, r := range rows {
		out.RawSetInt(i+1, pushFileAreaTable(L, r))
	}
	L.Push(out)
	return 1
}

// luaFileList binds wintermute.file.list({owner_username?, area?}).
func (a *API) luaFileList(L *lua.LState) int {
	var opts worldapi.ListFilesOpts
	if L.GetTop() >= 1 {
		tbl := L.CheckTable(1)
		opts.OwnerUsername = optString(tbl, "owner_username")
		opts.Area = optString(tbl, "area")
	}
	rows, err := a.Backend.ListFiles(a.Ctx, opts)
	if err != nil {
		return pushError(L, err)
	}
	out := L.NewTable()
	for i, r := range rows {
		out.RawSetInt(i+1, pushFileTable(L, r))
	}
	L.Push(out)
	return 1
}

// luaFileDelete binds wintermute.file.delete(slug).
func (a *API) luaFileDelete(L *lua.LState) int {
	slug := L.CheckString(1)
	return pushError(L, a.Backend.DeleteFile(a.Ctx, slug))
}

// luaFileSetACLs binds wintermute.file.set_acls(file_slug,
// {account_username, perms}).
func (a *API) luaFileSetACLs(L *lua.LState) int {
	slug := L.CheckString(1)
	opts := L.CheckTable(2)
	username := optString(opts, "account_username")
	perms := int(optInt64(opts, "perms"))
	return pushError(L, a.Backend.SetFileACLs(a.Ctx, slug, username, perms))
}

func pushFileAreaTable(L *lua.LState, a files.Area) *lua.LTable {
	t := L.NewTable()
	L.SetField(t, "slug", lua.LString(a.Slug))
	L.SetField(t, "name", lua.LString(a.Name))
	L.SetField(t, "description", lua.LString(a.Description))
	L.SetField(t, "read_min_level", lua.LNumber(a.ReadMinLevel))
	L.SetField(t, "write_min_level", lua.LNumber(a.WriteMinLevel))
	L.SetField(t, "admin_min_level", lua.LNumber(a.AdminMinLevel))
	return t
}

func pushFileTable(L *lua.LState, f files.File) *lua.LTable {
	t := L.NewTable()
	L.SetField(t, "id", lua.LNumber(f.ID))
	L.SetField(t, "slug", lua.LString(f.Slug))
	L.SetField(t, "hash", lua.LString(f.Hash))
	L.SetField(t, "size", lua.LNumber(f.Size))
	L.SetField(t, "mime", lua.LString(f.MIME))
	L.SetField(t, "owner_id", lua.LNumber(f.OwnerID))
	L.SetField(t, "created_at", lua.LNumber(f.CreatedAt.Unix()))
	L.SetField(t, "description", lua.LString(f.Description))
	L.SetField(t, "area", lua.LString(f.Area))
	return t
}
