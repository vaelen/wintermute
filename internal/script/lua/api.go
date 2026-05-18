// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package lua

import (
	"context"
	"errors"

	lua "github.com/yuin/gopher-lua"

	"github.com/vaelen/wintermute/internal/world"
	worldapi "github.com/vaelen/wintermute/internal/world/api"
	"github.com/vaelen/wintermute/internal/world/engage"
)

// API binds an admin-tier set of operations onto a Lua state as the
// `wintermute` global table. The package-level Pool keeps warm VMs with
// the table pre-loaded.
//
// One API value is shared across all VMs in a pool. It holds the
// (process-lifetime) context used for DB writes from inside a Lua call;
// in the M5 admin path that ctx is context.Background derived. M8 will
// introduce a per-call request ctx for player scripts.
type API struct {
	Backend *worldapi.API
	Tools   *ToolRegistry
	Ctx     context.Context
}

// NewAPI constructs an API. ctx is the parent context used by all DB
// operations invoked from Lua; pass context.Background for the admin
// path.
func NewAPI(backend *worldapi.API, tools *ToolRegistry, ctx context.Context) *API {
	if ctx == nil {
		ctx = context.Background()
	}
	if tools == nil {
		tools = NewToolRegistry()
	}
	return &API{Backend: backend, Tools: tools, Ctx: ctx}
}

// Bind installs the wintermute global on L. Called by the pool's VM
// constructor.
func (a *API) Bind(L *lua.LState) {
	root := L.NewTable()

	room := L.NewTable()
	L.SetField(room, "create", L.NewFunction(a.luaRoomCreate))
	L.SetField(room, "ensure", L.NewFunction(a.luaRoomEnsure))
	L.SetField(room, "find", L.NewFunction(a.luaRoomFind))
	L.SetField(room, "set_description", L.NewFunction(a.luaRoomSetDescription))
	L.SetField(room, "delete", L.NewFunction(a.luaRoomDelete))
	L.SetField(root, "room", room)

	door := L.NewTable()
	L.SetField(door, "create", L.NewFunction(a.luaDoorCreate))
	L.SetField(door, "ensure", L.NewFunction(a.luaDoorEnsure))
	L.SetField(door, "find", L.NewFunction(a.luaDoorFind))
	L.SetField(door, "set_messages", L.NewFunction(a.luaDoorSetMessages))
	L.SetField(door, "delete", L.NewFunction(a.luaDoorDelete))
	L.SetField(door, "dig", L.NewFunction(a.luaDoorDig))
	L.SetField(root, "door", door)

	obj := L.NewTable()
	L.SetField(obj, "create", L.NewFunction(a.luaObjectCreate))
	L.SetField(obj, "ensure", L.NewFunction(a.luaObjectEnsure))
	L.SetField(obj, "find", L.NewFunction(a.luaObjectFind))
	L.SetField(obj, "move", L.NewFunction(a.luaObjectMove))
	L.SetField(obj, "delete", L.NewFunction(a.luaObjectDelete))
	L.SetField(obj, "set_engage", L.NewFunction(a.luaObjectSetEngage))
	L.SetField(obj, "clear_engage", L.NewFunction(a.luaObjectClearEngage))
	L.SetField(root, "object", obj)

	npc := L.NewTable()
	L.SetField(npc, "create", L.NewFunction(a.luaNPCCreate))
	L.SetField(npc, "ensure", L.NewFunction(a.luaNPCEnsure))
	L.SetField(npc, "set_persona", L.NewFunction(a.luaNPCSetPersona))
	L.SetField(root, "npc", npc)

	account := L.NewTable()
	L.SetField(account, "set_level", L.NewFunction(a.luaAccountSetLevel))
	L.SetField(account, "boot", L.NewFunction(a.luaAccountBoot))
	L.SetField(root, "account", account)

	sys := L.NewTable()
	L.SetField(sys, "broadcast", L.NewFunction(a.luaSystemBroadcast))
	L.SetField(sys, "motd", L.NewFunction(a.luaSystemMotd))
	L.SetField(sys, "log", L.NewFunction(a.luaSystemLog))
	L.SetField(root, "system", sys)

	tool := L.NewTable()
	L.SetField(tool, "register", L.NewFunction(a.luaToolRegister))
	L.SetField(tool, "unregister", L.NewFunction(a.luaToolUnregister))
	L.SetField(tool, "list", L.NewFunction(a.luaToolList))
	L.SetField(root, "tool", tool)

	board := L.NewTable()
	L.SetField(board, "create", L.NewFunction(a.luaBoardCreate))
	L.SetField(board, "delete", L.NewFunction(a.luaBoardDelete))
	L.SetField(board, "get", L.NewFunction(a.luaBoardGet))
	L.SetField(board, "list", L.NewFunction(a.luaBoardList))
	L.SetField(board, "set_acls", L.NewFunction(a.luaBoardSetACLs))
	L.SetField(root, "board", board)

	mail := L.NewTable()
	L.SetField(mail, "send_from_system", L.NewFunction(a.luaMailSendFromSystem))
	L.SetField(mail, "broadcast", L.NewFunction(a.luaMailBroadcast))
	L.SetField(mail, "delete_for_user", L.NewFunction(a.luaMailDeleteForUser))
	L.SetField(mail, "unread_count", L.NewFunction(a.luaMailUnreadCount))
	L.SetField(root, "mail", mail)

	file := L.NewTable()
	fileArea := L.NewTable()
	L.SetField(fileArea, "create", L.NewFunction(a.luaFileAreaCreate))
	L.SetField(fileArea, "delete", L.NewFunction(a.luaFileAreaDelete))
	L.SetField(fileArea, "list", L.NewFunction(a.luaFileAreaList))
	L.SetField(file, "area", fileArea)
	L.SetField(file, "list", L.NewFunction(a.luaFileList))
	L.SetField(file, "delete", L.NewFunction(a.luaFileDelete))
	L.SetField(file, "set_acls", L.NewFunction(a.luaFileSetACLs))
	L.SetField(root, "file", file)

	ftn := L.NewTable()
	ftnNetwork := L.NewTable()
	L.SetField(ftnNetwork, "list", L.NewFunction(a.luaFTNNetworkList))
	L.SetField(ftnNetwork, "get", L.NewFunction(a.luaFTNNetworkGet))
	L.SetField(ftnNetwork, "set_default", L.NewFunction(a.luaFTNNetworkSetDefault))
	L.SetField(ftn, "network", ftnNetwork)
	L.SetField(root, "ftn", ftn)

	L.SetGlobal("wintermute", root)
}

// Reset re-binds the wintermute table on L to a fresh, known shape.
// Called by Pool.Put so a misbehaving script cannot poison subsequent
// runs.
func (a *API) Reset(L *lua.LState) {
	L.SetGlobal("wintermute", lua.LNil)
	a.Bind(L)
}

// ---------------------------------------------------------------------------
// helpers

// optString reads field `key` from tbl as a string. Empty if missing.
func optString(tbl *lua.LTable, key string) string {
	v := tbl.RawGetString(key)
	if v == lua.LNil {
		return ""
	}
	if s, ok := v.(lua.LString); ok {
		return string(s)
	}
	return v.String()
}

// optInt64 reads field `key` from tbl as an int64. Returns 0 if missing
// or not a number.
func optInt64(tbl *lua.LTable, key string) int64 {
	v := tbl.RawGetString(key)
	if n, ok := v.(lua.LNumber); ok {
		return int64(n)
	}
	return 0
}

// handleSlug accepts a string or a table with a .slug field, and returns
// the slug. The empty string indicates "no usable handle".
func handleSlug(v lua.LValue) string {
	switch x := v.(type) {
	case lua.LString:
		return string(x)
	case *lua.LTable:
		if s, ok := x.RawGetString("slug").(lua.LString); ok {
			return string(s)
		}
	}
	return ""
}

// pushError translates a Go-side error into either a raw Lua error
// (which the caller surfaces with lua_error) or pushes nil + message
// pair. We pick the lua_error style here because Lua admin scripts
// generally want to fail-fast; calling code (`pcall`) can still catch
// the error if needed. The thrown message is the canonical
// "wintermute: <code>: <details>" string.
func pushError(L *lua.LState, err error) int {
	if err == nil {
		return 0
	}
	L.RaiseError("%s", err.Error())
	return 0
}

// pushRoomHandle pushes a Lua table representing a room.
func pushRoomHandle(L *lua.LState, id int64, slug, name string) lua.LValue {
	t := L.NewTable()
	L.SetField(t, "kind", lua.LString("room"))
	L.SetField(t, "id", lua.LNumber(id))
	L.SetField(t, "slug", lua.LString(slug))
	L.SetField(t, "name", lua.LString(name))
	return t
}

// pushDoorHandle pushes a Lua table representing a door.
func pushDoorHandle(L *lua.LState, id int64, slug, name, dir string, from, to int64) lua.LValue {
	t := L.NewTable()
	L.SetField(t, "kind", lua.LString("door"))
	L.SetField(t, "id", lua.LNumber(id))
	L.SetField(t, "slug", lua.LString(slug))
	L.SetField(t, "name", lua.LString(name))
	L.SetField(t, "direction", lua.LString(dir))
	L.SetField(t, "from_room", lua.LNumber(from))
	L.SetField(t, "to_room", lua.LNumber(to))
	return t
}

// pushObjectHandle pushes a Lua table representing a non-door, non-NPC
// object.
func pushObjectHandle(L *lua.LState, id int64, slug, name, kind string) lua.LValue {
	t := L.NewTable()
	L.SetField(t, "kind", lua.LString(kind))
	L.SetField(t, "id", lua.LNumber(id))
	L.SetField(t, "slug", lua.LString(slug))
	L.SetField(t, "name", lua.LString(name))
	return t
}

// ---------------------------------------------------------------------------
// room bindings

func (a *API) luaRoomCreate(L *lua.LState) int {
	tbl := L.CheckTable(1)
	spec := worldapi.RoomSpec{
		Slug:        optString(tbl, "slug"),
		Name:        optString(tbl, "name"),
		Description: optString(tbl, "description"),
		OwnerID:     optInt64(tbl, "owner_id"),
	}
	id, err := a.Backend.CreateRoom(a.Ctx, spec)
	if err != nil {
		return pushError(L, err)
	}
	L.Push(pushRoomHandle(L, int64(id), spec.Slug, spec.Name))
	return 1
}

func (a *API) luaRoomEnsure(L *lua.LState) int {
	tbl := L.CheckTable(1)
	spec := worldapi.RoomSpec{
		Slug:        optString(tbl, "slug"),
		Name:        optString(tbl, "name"),
		Description: optString(tbl, "description"),
		OwnerID:     optInt64(tbl, "owner_id"),
	}
	id, created, err := a.Backend.EnsureRoom(a.Ctx, spec)
	if err != nil {
		return pushError(L, err)
	}
	L.Push(pushRoomHandle(L, int64(id), spec.Slug, spec.Name))
	L.Push(lua.LBool(created))
	return 2
}

func (a *API) luaRoomFind(L *lua.LState) int {
	slug := L.CheckString(1)
	r, err := a.Backend.FindRoom(slug)
	if err != nil {
		if isNotFound(err) {
			L.Push(lua.LNil)
			return 1
		}
		return pushError(L, err)
	}
	L.Push(pushRoomHandle(L, int64(r.ID), r.Slug, r.Name))
	return 1
}

func (a *API) luaRoomSetDescription(L *lua.LState) int {
	slug := handleSlug(L.Get(1))
	desc := L.CheckString(2)
	if slug == "" {
		return pushError(L, goErrorf("invalid_argument", "room.set_description: handle missing slug"))
	}
	return pushError(L, a.Backend.SetRoomDescription(a.Ctx, slug, desc))
}

func (a *API) luaRoomDelete(L *lua.LState) int {
	slug := handleSlug(L.Get(1))
	if slug == "" {
		return pushError(L, goErrorf("invalid_argument", "room.delete: handle missing slug"))
	}
	return pushError(L, a.Backend.DeleteRoom(a.Ctx, slug))
}

// ---------------------------------------------------------------------------
// door bindings

func (a *API) luaDoorCreate(L *lua.LState) int {
	tbl := L.CheckTable(1)
	spec := worldapi.DoorSpec{
		Slug:         optString(tbl, "slug"),
		Name:         optString(tbl, "name"),
		FromRoomSlug: handleSlug(tbl.RawGetString("from")),
		Direction:    optString(tbl, "direction"),
		ToRoomSlug:   handleSlug(tbl.RawGetString("to")),
		LeaveMsg:     optString(tbl, "leave_msg"),
		ArriveMsg:    optString(tbl, "arrive_msg"),
	}
	id, err := a.Backend.CreateDoor(a.Ctx, spec)
	if err != nil {
		return pushError(L, err)
	}
	d, err := a.Backend.FindDoor(spec.Slug)
	if err != nil {
		return pushError(L, err)
	}
	L.Push(pushDoorHandle(L, int64(id), d.Slug, d.Name, d.Direction,
		int64(d.FromRoom), int64(d.ToRoom)))
	return 1
}

func (a *API) luaDoorEnsure(L *lua.LState) int {
	tbl := L.CheckTable(1)
	spec := worldapi.DoorSpec{
		Slug:         optString(tbl, "slug"),
		Name:         optString(tbl, "name"),
		FromRoomSlug: handleSlug(tbl.RawGetString("from")),
		Direction:    optString(tbl, "direction"),
		ToRoomSlug:   handleSlug(tbl.RawGetString("to")),
		LeaveMsg:     optString(tbl, "leave_msg"),
		ArriveMsg:    optString(tbl, "arrive_msg"),
	}
	id, created, err := a.Backend.EnsureDoor(a.Ctx, spec)
	if err != nil {
		return pushError(L, err)
	}
	d, err := a.Backend.FindDoor(spec.Slug)
	if err != nil {
		return pushError(L, err)
	}
	L.Push(pushDoorHandle(L, int64(id), d.Slug, d.Name, d.Direction,
		int64(d.FromRoom), int64(d.ToRoom)))
	L.Push(lua.LBool(created))
	return 2
}

func (a *API) luaDoorFind(L *lua.LState) int {
	slug := L.CheckString(1)
	d, err := a.Backend.FindDoor(slug)
	if err != nil {
		if isNotFound(err) {
			L.Push(lua.LNil)
			return 1
		}
		return pushError(L, err)
	}
	L.Push(pushDoorHandle(L, int64(d.ID), d.Slug, d.Name, d.Direction,
		int64(d.FromRoom), int64(d.ToRoom)))
	return 1
}

func (a *API) luaDoorSetMessages(L *lua.LState) int {
	slug := handleSlug(L.Get(1))
	tbl := L.CheckTable(2)
	if slug == "" {
		return pushError(L, goErrorf("invalid_argument", "door.set_messages: handle missing slug"))
	}
	return pushError(L, a.Backend.SetDoorMessages(a.Ctx, slug,
		optString(tbl, "leave_msg"), optString(tbl, "arrive_msg")))
}

func (a *API) luaDoorDelete(L *lua.LState) int {
	slug := handleSlug(L.Get(1))
	if slug == "" {
		return pushError(L, goErrorf("invalid_argument", "door.delete: handle missing slug"))
	}
	return pushError(L, a.Backend.DeleteDoor(a.Ctx, slug))
}

func (a *API) luaDoorDig(L *lua.LState) int {
	from := handleSlug(L.Get(1))
	dir := L.CheckString(2)
	to := handleSlug(L.Get(3))
	if from == "" || to == "" {
		return pushError(L, goErrorf("invalid_argument", "door.dig: from and to room slugs required"))
	}
	_, _, err := a.Backend.Dig(a.Ctx, from, dir, to)
	return pushError(L, err)
}

// ---------------------------------------------------------------------------
// object bindings

func (a *API) luaObjectCreate(L *lua.LState) int {
	tbl := L.CheckTable(1)
	spec := worldapi.ObjectSpec{
		Slug:      optString(tbl, "slug"),
		Name:      optString(tbl, "name"),
		ShortDesc: optString(tbl, "short_desc"),
		LongDesc:  optString(tbl, "long_desc"),
		RoomSlug:  handleSlug(tbl.RawGetString("room")),
		OwnerID:   optInt64(tbl, "owner_id"),
	}
	if k := optString(tbl, "kind"); k != "" {
		spec.Kind = mapObjectKind(k)
	}
	id, err := a.Backend.CreateObject(a.Ctx, spec)
	if err != nil {
		return pushError(L, err)
	}
	L.Push(pushObjectHandle(L, int64(id), spec.Slug, spec.Name, string(spec.Kind)))
	return 1
}

func (a *API) luaObjectEnsure(L *lua.LState) int {
	tbl := L.CheckTable(1)
	spec := worldapi.ObjectSpec{
		Slug:      optString(tbl, "slug"),
		Name:      optString(tbl, "name"),
		ShortDesc: optString(tbl, "short_desc"),
		LongDesc:  optString(tbl, "long_desc"),
		RoomSlug:  handleSlug(tbl.RawGetString("room")),
	}
	if k := optString(tbl, "kind"); k != "" {
		spec.Kind = mapObjectKind(k)
	}
	id, created, err := a.Backend.EnsureObject(a.Ctx, spec)
	if err != nil {
		return pushError(L, err)
	}
	L.Push(pushObjectHandle(L, int64(id), spec.Slug, spec.Name, string(spec.Kind)))
	L.Push(lua.LBool(created))
	return 2
}

func (a *API) luaObjectFind(L *lua.LState) int {
	slug := L.CheckString(1)
	o, err := a.Backend.FindObject(slug)
	if err != nil {
		if isNotFound(err) {
			L.Push(lua.LNil)
			return 1
		}
		return pushError(L, err)
	}
	L.Push(pushObjectHandle(L, int64(o.ID), o.Slug, o.Name, string(o.Kind)))
	return 1
}

func (a *API) luaObjectMove(L *lua.LState) int {
	objSlug := handleSlug(L.Get(1))
	roomSlug := handleSlug(L.Get(2))
	if objSlug == "" || roomSlug == "" {
		return pushError(L, goErrorf("invalid_argument", "object.move: handle missing slug"))
	}
	return pushError(L, a.Backend.MoveObject(a.Ctx, objSlug, roomSlug))
}

func (a *API) luaObjectDelete(L *lua.LState) int {
	slug := handleSlug(L.Get(1))
	if slug == "" {
		return pushError(L, goErrorf("invalid_argument", "object.delete: handle missing slug"))
	}
	return pushError(L, a.Backend.DeleteObject(a.Ctx, slug))
}

// luaObjectSetEngage binds wintermute.object.set_engage(slug, opts).
// opts is a Lua table with: kind (required), engage_verbs (array),
// disengage_verbs (array), enter_msg, present_msg, exit_msg, prompt.
func (a *API) luaObjectSetEngage(L *lua.LState) int {
	slug := L.CheckString(1)
	opts := L.CheckTable(2)

	kind := optString(opts, "kind")
	if kind == "" {
		return pushError(L, goErrorf("invalid_argument", "object.set_engage: missing 'kind'"))
	}
	setOpts := worldapi.SetEngageOpts{
		Kind:           kind,
		EngageVerbs:    luaTableStringArray(opts, "engage_verbs"),
		DisengageVerbs: luaTableStringArray(opts, "disengage_verbs"),
		EnterMsg:       optString(opts, "enter_msg"),
		PresentMsg:     optString(opts, "present_msg"),
		ExitMsg:        optString(opts, "exit_msg"),
		Prompt:         optString(opts, "prompt"),
		Menu:           luaMenuEntries(opts, "menu"),
	}
	if err := a.Backend.SetEngage(a.Ctx, slug, setOpts); err != nil {
		return pushError(L, err)
	}
	L.Push(lua.LBool(true))
	return 1
}

// luaObjectClearEngage binds wintermute.object.clear_engage(slug).
func (a *API) luaObjectClearEngage(L *lua.LState) int {
	slug := L.CheckString(1)
	if err := a.Backend.ClearEngage(a.Ctx, slug); err != nil {
		return pushError(L, err)
	}
	L.Push(lua.LBool(true))
	return 1
}

// luaMenuEntries reads an array-of-tables field from a Lua opts table
// and converts it to []engage.MenuEntry. Each row must be a table with
// string fields `feature` and (for files) `area`. Unknown / malformed
// rows are skipped here; api.SetEngage performs full validation.
func luaMenuEntries(t *lua.LTable, key string) []engage.MenuEntry {
	v := t.RawGetString(key)
	tbl, ok := v.(*lua.LTable)
	if !ok {
		return nil
	}
	var out []engage.MenuEntry
	tbl.ForEach(func(_, vv lua.LValue) {
		row, ok := vv.(*lua.LTable)
		if !ok {
			return
		}
		feature := optString(row, "feature")
		if feature == "" {
			return
		}
		out = append(out, engage.MenuEntry{
			Feature: feature,
			Area:    optString(row, "area"),
		})
	})
	return out
}

// luaTableStringArray reads an array field from a Lua table as []string.
func luaTableStringArray(t *lua.LTable, key string) []string {
	v := t.RawGetString(key)
	tbl, ok := v.(*lua.LTable)
	if !ok {
		return nil
	}
	var out []string
	tbl.ForEach(func(_, vv lua.LValue) {
		if s, ok := vv.(lua.LString); ok {
			out = append(out, string(s))
		}
	})
	return out
}

// ---------------------------------------------------------------------------
// npc bindings

func (a *API) luaNPCCreate(L *lua.LState) int {
	tbl := L.CheckTable(1)
	spec := npcSpecFromTable(tbl)
	id, err := a.Backend.CreateNPC(a.Ctx, spec)
	if err != nil {
		return pushError(L, err)
	}
	L.Push(pushObjectHandle(L, int64(id), spec.Slug, spec.Name, "npc"))
	return 1
}

func (a *API) luaNPCEnsure(L *lua.LState) int {
	tbl := L.CheckTable(1)
	spec := npcSpecFromTable(tbl)
	id, created, err := a.Backend.EnsureNPC(a.Ctx, spec)
	if err != nil {
		return pushError(L, err)
	}
	L.Push(pushObjectHandle(L, int64(id), spec.Slug, spec.Name, "npc"))
	L.Push(lua.LBool(created))
	return 2
}

func (a *API) luaNPCSetPersona(L *lua.LState) int {
	slug := handleSlug(L.Get(1))
	persona := L.CheckString(2)
	if slug == "" {
		return pushError(L, goErrorf("invalid_argument", "npc.set_persona: handle missing slug"))
	}
	return pushError(L, a.Backend.SetNPCPersona(a.Ctx, slug, persona))
}

func npcSpecFromTable(tbl *lua.LTable) worldapi.NPCSpec {
	return worldapi.NPCSpec{
		Slug:        optString(tbl, "slug"),
		Name:        optString(tbl, "name"),
		ShortDesc:   optString(tbl, "short_desc"),
		LongDesc:    optString(tbl, "long_desc"),
		RoomSlug:    handleSlug(tbl.RawGetString("room")),
		OwnerID:     optInt64(tbl, "owner_id"),
		Persona:     optString(tbl, "persona"),
		Backend:     optString(tbl, "backend"),
		BackendOpts: optString(tbl, "backend_opts"),
		ChatModel:   optString(tbl, "model"),
		GateModel:   optString(tbl, "gate_model"),
		MaxContext:  int(optInt64(tbl, "max_context")),
	}
}

// ---------------------------------------------------------------------------
// account bindings

func (a *API) luaAccountSetLevel(L *lua.LState) int {
	username := L.CheckString(1)
	level := L.CheckString(2)
	return pushError(L, a.Backend.SetAccountLevel(a.Ctx, username, level))
}

func (a *API) luaAccountBoot(L *lua.LState) int {
	username := L.CheckString(1)
	msg := L.OptString(2, "")
	n, err := a.Backend.BootAccount(a.Ctx, username, msg)
	if err != nil {
		return pushError(L, err)
	}
	L.Push(lua.LNumber(n))
	return 1
}

// ---------------------------------------------------------------------------
// system bindings

func (a *API) luaSystemBroadcast(L *lua.LState) int {
	msg := L.CheckString(1)
	a.Backend.Broadcast(msg)
	return 0
}

func (a *API) luaSystemMotd(L *lua.LState) int {
	msg := L.CheckString(1)
	return pushError(L, a.Backend.SetMOTD(a.Ctx, msg))
}

func (a *API) luaSystemLog(L *lua.LState) int {
	msg := L.CheckString(1)
	a.Backend.Log(msg)
	return 0
}

// ---------------------------------------------------------------------------
// helpers

// mapObjectKind translates a Lua-supplied kind string into world.Kind.
// Unknown kinds are returned unchanged — the api layer rejects them
// with invalid_argument so the user gets the correct error message.
func mapObjectKind(k string) world.Kind {
	return world.Kind(k)
}

// isNotFound reports whether err is the api package's not_found error.
func isNotFound(err error) bool {
	var e *worldapi.Error
	if errors.As(err, &e) {
		return e.Code == worldapi.CodeNotFound
	}
	return false
}
