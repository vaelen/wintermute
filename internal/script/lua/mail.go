// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package lua

import (
	lua "github.com/yuin/gopher-lua"

	worldapi "github.com/vaelen/wintermute/internal/world/api"
)

// luaMailSendFromSystem binds wintermute.mail.send_from_system(to,
// subject, body, opts?) → id. opts is currently reserved for future use.
func (a *API) luaMailSendFromSystem(L *lua.LState) int {
	to := L.CheckString(1)
	subject := L.CheckString(2)
	body := L.CheckString(3)
	// 4th argument is opts; reserved for future expansion (read-receipt
	// flags, scheduled-send, etc.). Accept and ignore.
	if L.GetTop() >= 4 {
		_ = L.CheckTable(4)
	}
	id, err := a.Backend.SendMailFromSystem(a.Ctx, to, subject, body)
	if err != nil {
		return pushError(L, err)
	}
	L.Push(lua.LNumber(id))
	return 1
}

// luaMailBroadcast binds wintermute.mail.broadcast(subject, body, opts?)
// → count. opts.access_level filters delivery.
func (a *API) luaMailBroadcast(L *lua.LState) int {
	subject := L.CheckString(1)
	body := L.CheckString(2)
	var opts worldapi.BroadcastMailOpts
	if L.GetTop() >= 3 {
		tbl := L.CheckTable(3)
		opts.AccessLevel = optString(tbl, "access_level")
	}
	n, err := a.Backend.BroadcastMail(a.Ctx, subject, body, opts)
	if err != nil {
		return pushError(L, err)
	}
	L.Push(lua.LNumber(n))
	return 1
}

// luaMailDeleteForUser binds wintermute.mail.delete_for_user(username,
// mail_id).
func (a *API) luaMailDeleteForUser(L *lua.LState) int {
	username := L.CheckString(1)
	id := L.CheckInt64(2)
	return pushError(L, a.Backend.DeleteMailForUser(a.Ctx, username, id))
}

// luaMailUnreadCount binds wintermute.mail.unread_count(username) → int.
func (a *API) luaMailUnreadCount(L *lua.LState) int {
	username := L.CheckString(1)
	n, err := a.Backend.UnreadMailCount(a.Ctx, username)
	if err != nil {
		return pushError(L, err)
	}
	L.Push(lua.LNumber(n))
	return 1
}
