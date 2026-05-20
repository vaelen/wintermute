// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vaelen/wintermute/internal/auth"
	scriptlua "github.com/vaelen/wintermute/internal/script/lua"
	"github.com/vaelen/wintermute/internal/security"
	"github.com/vaelen/wintermute/internal/world"
	worldapi "github.com/vaelen/wintermute/internal/world/api"
)

// AdminBackend bundles every dependency the @-commands need. Holding it
// as an interface lets tests pass a fake without dragging in the entire
// LLM/sqlite chain.
type AdminBackend struct {
	API     *worldapi.API
	Scripts *scriptlua.ScriptStore
	Pool    *scriptlua.Pool
	Tools   *scriptlua.ToolRegistry
}

// requireAdmin returns OutcomeUnknown when the caller lacks admin /
// builder access — the surrounding Dispatch then surfaces "Unknown
// command" so the @-command stays invisible to ordinary players. Returns
// OutcomeContinue when the access check passes (caller proceeds).
func (h *Handler) requireAdmin(forBuilders bool) (Outcome, bool) {
	if h.Presence == nil || h.Presence.Account == nil {
		return OutcomeUnknown, false
	}
	lvl := h.Presence.Account.AccessLevel
	if lvl == auth.AccessAdmin {
		return OutcomeContinue, true
	}
	if forBuilders && lvl == auth.AccessBuilder {
		return OutcomeContinue, true
	}
	return OutcomeUnknown, false
}

// adminBackend pulls the backend off the Handler. Returns nil for tests
// that haven't wired one in; affected @-commands degrade to
// OutcomeUnknown so they stay hidden.
func (h *Handler) adminBackend() *AdminBackend {
	return h.Admin
}

// ---------------------------------------------------------------------------
// @create-room

func (h *Handler) cmdCreateRoom(ctx context.Context, rest string) Outcome {
	if outcome, ok := h.requireAdmin(true); !ok {
		return outcome
	}
	be := h.adminBackend()
	if be == nil {
		return OutcomeUnknown
	}
	slug, rest := splitCmd(rest)
	if slug == "" {
		_ = h.Presence.Write("Usage: @create-room <slug> \"<name>\"\r\n")
		return OutcomeContinue
	}
	name := strings.TrimSpace(stripQuotes(rest))
	if name == "" {
		_ = h.Presence.Write("Usage: @create-room <slug> \"<name>\"\r\n")
		return OutcomeContinue
	}
	if _, err := be.API.CreateRoom(ctx, worldapi.RoomSpec{
		Slug: slug, Name: name,
		OwnerID: h.Presence.Account.ID,
	}); err != nil {
		_ = h.Presence.Write(formatAdminError(err))
		return OutcomeContinue
	}
	_ = h.Presence.Write("Created room \"" + slug + "\".\r\n")
	return OutcomeContinue
}

// ---------------------------------------------------------------------------
// @dig <direction> <room-slug>

func (h *Handler) cmdDig(ctx context.Context, rest string) Outcome {
	if outcome, ok := h.requireAdmin(true); !ok {
		return outcome
	}
	be := h.adminBackend()
	if be == nil {
		return OutcomeUnknown
	}
	dir, rest := splitCmd(rest)
	toSlug := strings.TrimSpace(rest)
	if dir == "" || toSlug == "" {
		_ = h.Presence.Write("Usage: @dig <direction> <room-slug>\r\n")
		return OutcomeContinue
	}
	dir = canonicalDirection(dir)
	loc, err := h.World.LocationOf(h.Presence.PlayerID)
	if err != nil {
		_ = h.Presence.Write("You float in undefined void.\r\n")
		return OutcomeContinue
	}
	room, err := h.World.Room(loc.RoomID)
	if err != nil {
		_ = h.Presence.Write("Your current room is missing.\r\n")
		return OutcomeContinue
	}
	if _, _, err := be.API.Dig(ctx, room.Slug, dir, toSlug); err != nil {
		_ = h.Presence.Write(formatAdminError(err))
		return OutcomeContinue
	}
	_ = h.Presence.Write("Dug " + dir + " to " + toSlug + ".\r\n")
	return OutcomeContinue
}

// ---------------------------------------------------------------------------
// @create-door <slug> <direction> <to-room-slug>

func (h *Handler) cmdCreateDoor(ctx context.Context, rest string) Outcome {
	if outcome, ok := h.requireAdmin(true); !ok {
		return outcome
	}
	be := h.adminBackend()
	if be == nil {
		return OutcomeUnknown
	}
	slug, rest := splitCmd(rest)
	dir, rest := splitCmd(rest)
	toSlug := strings.TrimSpace(rest)
	if slug == "" || dir == "" || toSlug == "" {
		_ = h.Presence.Write("Usage: @create-door <slug> <direction> <to-room-slug>\r\n")
		return OutcomeContinue
	}
	loc, err := h.World.LocationOf(h.Presence.PlayerID)
	if err != nil {
		_ = h.Presence.Write("You float in undefined void.\r\n")
		return OutcomeContinue
	}
	room, err := h.World.Room(loc.RoomID)
	if err != nil {
		_ = h.Presence.Write("Your current room is missing.\r\n")
		return OutcomeContinue
	}
	if _, err := be.API.CreateDoor(ctx, worldapi.DoorSpec{
		Slug: slug, FromRoomSlug: room.Slug,
		Direction: canonicalDirection(dir), ToRoomSlug: toSlug,
		OwnerID: h.Presence.Account.ID,
	}); err != nil {
		_ = h.Presence.Write(formatAdminError(err))
		return OutcomeContinue
	}
	_ = h.Presence.Write("Created door \"" + slug + "\" " + dir + " to " + toSlug + ".\r\n")
	return OutcomeContinue
}

// ---------------------------------------------------------------------------
// @door-msg <slug> leave|arrive "<template>"

func (h *Handler) cmdDoorMsg(ctx context.Context, rest string) Outcome {
	if outcome, ok := h.requireAdmin(true); !ok {
		return outcome
	}
	be := h.adminBackend()
	if be == nil {
		return OutcomeUnknown
	}
	slug, rest := splitCmd(rest)
	kind, rest := splitCmd(rest)
	tmpl := strings.TrimSpace(stripQuotes(rest))
	if slug == "" || (kind != "leave" && kind != "arrive") || tmpl == "" {
		_ = h.Presence.Write("Usage: @door-msg <slug> leave|arrive \"<template>\"\r\n")
		return OutcomeContinue
	}
	var leave, arrive string
	if kind == "leave" {
		leave = tmpl
	} else {
		arrive = tmpl
	}
	if err := be.API.SetDoorMessages(ctx, slug, leave, arrive); err != nil {
		_ = h.Presence.Write(formatAdminError(err))
		return OutcomeContinue
	}
	_ = h.Presence.Write("Updated " + slug + " " + kind + " template.\r\n")
	return OutcomeContinue
}

// ---------------------------------------------------------------------------
// @create-npc <slug> "<name>" "<persona>"

func (h *Handler) cmdCreateNPC(ctx context.Context, rest string) Outcome {
	if outcome, ok := h.requireAdmin(false); !ok {
		return outcome
	}
	be := h.adminBackend()
	if be == nil {
		return OutcomeUnknown
	}
	slug, rest := splitCmd(rest)
	name, rest := extractQuoted(rest)
	persona := strings.TrimSpace(stripQuotes(rest))
	if slug == "" || name == "" || persona == "" {
		_ = h.Presence.Write("Usage: @create-npc <slug> \"<name>\" \"<persona>\"\r\n")
		return OutcomeContinue
	}
	loc, err := h.World.LocationOf(h.Presence.PlayerID)
	if err != nil {
		_ = h.Presence.Write("You float in undefined void.\r\n")
		return OutcomeContinue
	}
	room, err := h.World.Room(loc.RoomID)
	if err != nil {
		_ = h.Presence.Write("Your current room is missing.\r\n")
		return OutcomeContinue
	}
	if _, err := be.API.CreateNPC(ctx, worldapi.NPCSpec{
		Slug: slug, Name: name, Persona: persona,
		RoomSlug: room.Slug, OwnerID: h.Presence.Account.ID,
	}); err != nil {
		_ = h.Presence.Write(formatAdminError(err))
		return OutcomeContinue
	}
	_ = h.Presence.Write("Created NPC \"" + slug + "\" in " + room.Slug + ".\r\n")
	return OutcomeContinue
}

// ---------------------------------------------------------------------------
// @persona <npc-slug> "<new persona>"

func (h *Handler) cmdPersona(ctx context.Context, rest string) Outcome {
	if outcome, ok := h.requireAdmin(false); !ok {
		return outcome
	}
	be := h.adminBackend()
	if be == nil {
		return OutcomeUnknown
	}
	slug, rest := splitCmd(rest)
	persona := strings.TrimSpace(stripQuotes(rest))
	if slug == "" || persona == "" {
		_ = h.Presence.Write("Usage: @persona <npc-slug> \"<new persona>\"\r\n")
		return OutcomeContinue
	}
	if err := be.API.SetNPCPersona(ctx, slug, persona); err != nil {
		_ = h.Presence.Write(formatAdminError(err))
		return OutcomeContinue
	}
	_ = h.Presence.Write("Updated persona for " + slug + ".\r\n")
	return OutcomeContinue
}

// ---------------------------------------------------------------------------
// @script <slug>  — show source

func (h *Handler) cmdScript(ctx context.Context, rest string) Outcome {
	if outcome, ok := h.requireAdmin(false); !ok {
		return outcome
	}
	be := h.adminBackend()
	if be == nil {
		return OutcomeUnknown
	}
	slug := strings.TrimSpace(rest)
	if slug == "" {
		_ = h.Presence.Write("Usage: @script <slug>\r\n")
		return OutcomeContinue
	}
	sc, err := be.Scripts.Get(ctx, slug)
	if err != nil {
		_ = h.Presence.Write("No script with slug \"" + slug + "\".\r\n")
		return OutcomeContinue
	}
	_ = h.Presence.Write("--- " + sc.Slug + " ---\r\n")
	_ = h.Presence.Write(normaliseCRLF(sc.Source))
	if !strings.HasSuffix(sc.Source, "\n") {
		_ = h.Presence.Write("\r\n")
	}
	_ = h.Presence.Write("--- end ---\r\n")
	return OutcomeContinue
}

// ---------------------------------------------------------------------------
// @run <slug>

func (h *Handler) cmdRun(ctx context.Context, rest string) Outcome {
	if outcome, ok := h.requireAdmin(false); !ok {
		return outcome
	}
	be := h.adminBackend()
	if be == nil {
		return OutcomeUnknown
	}
	slug := strings.TrimSpace(rest)
	if slug == "" {
		_ = h.Presence.Write("Usage: @run <slug>\r\n")
		return OutcomeContinue
	}
	sc, err := be.Scripts.Get(ctx, slug)
	if err != nil {
		_ = h.Presence.Write("No script with slug \"" + slug + "\".\r\n")
		return OutcomeContinue
	}
	if err := be.Pool.Run(ctx, sc.Source); err != nil {
		_ = h.Presence.Write("Run failed: " + err.Error() + "\r\n")
		return OutcomeContinue
	}
	_ = h.Presence.Write("Script " + slug + " ran.\r\n")
	return OutcomeContinue
}

// ---------------------------------------------------------------------------
// @tools

func (h *Handler) cmdTools() Outcome {
	if outcome, ok := h.requireAdmin(false); !ok {
		return outcome
	}
	be := h.adminBackend()
	if be == nil {
		return OutcomeUnknown
	}
	names := be.Tools.Names()
	if len(names) == 0 {
		_ = h.Presence.Write("No tools registered.\r\n")
		return OutcomeContinue
	}
	sort.Strings(names)
	_ = h.Presence.Write("Registered tools (" + fmt.Sprint(len(names)) + "):\r\n")
	for _, n := range names {
		_ = h.Presence.Write("  " + n + "\r\n")
	}
	return OutcomeContinue
}

// ---------------------------------------------------------------------------
// @invoke <tool> <json>

func (h *Handler) cmdInvoke(ctx context.Context, rest string) Outcome {
	if outcome, ok := h.requireAdmin(false); !ok {
		return outcome
	}
	be := h.adminBackend()
	if be == nil {
		return OutcomeUnknown
	}
	name, rest := splitCmd(rest)
	jsonStr := strings.TrimSpace(rest)
	if name == "" {
		_ = h.Presence.Write("Usage: @invoke <tool> [json-args]\r\n")
		return OutcomeContinue
	}
	args := map[string]any{}
	if jsonStr != "" {
		if err := json.Unmarshal([]byte(jsonStr), &args); err != nil {
			_ = h.Presence.Write("Invalid JSON: " + err.Error() + "\r\n")
			return OutcomeContinue
		}
	}
	result, err := be.Tools.Invoke(ctx, be.Pool, name, args)
	if err != nil {
		_ = h.Presence.Write("Invoke failed: " + err.Error() + "\r\n")
		return OutcomeContinue
	}
	out, _ := json.Marshal(result)
	_ = h.Presence.Write(string(out) + "\r\n")
	return OutcomeContinue
}

// ---------------------------------------------------------------------------
// @reload-scripts

func (h *Handler) cmdReloadScripts(ctx context.Context) Outcome {
	if outcome, ok := h.requireAdmin(false); !ok {
		return outcome
	}
	be := h.adminBackend()
	if be == nil {
		return OutcomeUnknown
	}
	count := 0
	err := scriptlua.RunInit(ctx, be.Pool, be.Scripts, func(slug string, err error) {
		count++
		if err != nil {
			_ = h.Presence.Write("  " + slug + ": " + err.Error() + "\r\n")
		} else {
			_ = h.Presence.Write("  " + slug + ": ok\r\n")
		}
	})
	if err != nil {
		_ = h.Presence.Write("Reload failed: " + err.Error() + "\r\n")
		return OutcomeContinue
	}
	_ = h.Presence.Write(fmt.Sprintf("Reloaded %d init script(s).\r\n", count))
	return OutcomeContinue
}

// ---------------------------------------------------------------------------
// @boot <player-username>

func (h *Handler) cmdBoot(ctx context.Context, rest string) Outcome {
	if outcome, ok := h.requireAdmin(false); !ok {
		return outcome
	}
	be := h.adminBackend()
	if be == nil {
		return OutcomeUnknown
	}
	parts := strings.SplitN(strings.TrimSpace(rest), " ", 2)
	if len(parts) == 0 || parts[0] == "" {
		_ = h.Presence.Write("Usage: @boot <username> [message]\r\n")
		return OutcomeContinue
	}
	username := parts[0]
	msg := ""
	if len(parts) == 2 {
		msg = strings.TrimSpace(stripQuotes(parts[1]))
		if msg != "" && !strings.HasSuffix(msg, "\r\n") {
			msg += "\r\n"
		}
	}
	n, err := be.API.BootAccount(ctx, username, msg)
	if err != nil {
		_ = h.Presence.Write(formatAdminError(err))
		return OutcomeContinue
	}
	_ = h.Presence.Write(fmt.Sprintf("Disconnected %d session(s) for %s.\r\n", n, username))
	return OutcomeContinue
}

// ---------------------------------------------------------------------------
// @cleanup-files
//
// Output format ("reaped N expired token(s), M orphan blob(s)") is the
// stable contract callers (and tests) match on; do not reword without
// updating the test in admin_test.go.

func (h *Handler) cmdCleanupFiles(ctx context.Context) Outcome {
	if outcome, ok := h.requireAdmin(false); !ok {
		return outcome
	}
	be := h.adminBackend()
	if be == nil {
		return OutcomeUnknown
	}
	stats, err := be.API.CleanupFiles(ctx, 0)
	if err != nil {
		_ = h.Presence.Write(formatAdminError(err))
		return OutcomeContinue
	}
	_ = h.Presence.Write(fmt.Sprintf("reaped %d expired token(s), %d orphan blob(s)\r\n",
		stats.TokensReaped, stats.BlobsReaped))
	return OutcomeContinue
}

// ---------------------------------------------------------------------------
// helpers

// stripQuotes removes a surrounding pair of double quotes, if present.
// Strings without quotes are returned as-is.
func stripQuotes(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// extractQuoted pulls the first double-quoted token off the front of s
// and returns it (without quotes) along with the remainder. If s does
// not begin with a quote, the first whitespace-delimited token is
// returned instead.
func extractQuoted(s string) (string, string) {
	s = strings.TrimLeft(s, " \t")
	if !strings.HasPrefix(s, "\"") {
		return splitCmd(s)
	}
	end := strings.Index(s[1:], "\"")
	if end < 0 {
		// Unbalanced; treat the whole thing as the token.
		return s[1:], ""
	}
	tok := s[1 : 1+end]
	rest := strings.TrimSpace(s[2+end:])
	return tok, rest
}

// formatAdminError renders an admin-side error for the session output.
// Wraps the canonical "wintermute: ..." messages with a CRLF and a
// leading "error:" tag.
func formatAdminError(err error) string {
	if err == nil {
		return ""
	}
	var apiErr *worldapi.Error
	if errors.As(err, &apiErr) {
		return "error: " + apiErr.Error() + "\r\n"
	}
	return "error: " + err.Error() + "\r\n"
}

// normaliseCRLF turns lone LF into CRLF so script source can be written
// raw onto a telnet stream.
func normaliseCRLF(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}

// cmdAtHelp is `@help` — gated to admin/builder so it stays invisible
// to ordinary players (same hiding rule as every other @-command), but
// the output it produces is identical to the plain `help` command. The
// alias exists for discoverability: an admin who has just typed an
// @-command is more likely to guess `@help` than `help`.
func (h *Handler) cmdAtHelp() Outcome {
	if outcome, ok := h.requireAdmin(true); !ok {
		return outcome
	}
	h.cmdHelp()
	return OutcomeContinue
}

// editorState carries the in-progress paste-mode capture for @edit.
// While editor is non-nil on the Handler, every input line goes into
// lines until the user types "." on its own line, at which point the
// joined source is saved to the scripts table and the state is cleared.
type editorState struct {
	slug  string
	lines []string
}

// cmdEdit opens paste-mode for a script slug. The session prompt is
// replaced by a brief instruction; subsequent lines are captured until
// a single-dot terminator. ".abort" cancels without saving.
func (h *Handler) cmdEdit(ctx context.Context, rest string) Outcome {
	if outcome, ok := h.requireAdmin(false); !ok {
		return outcome
	}
	be := h.adminBackend()
	if be == nil {
		return OutcomeUnknown
	}
	slug := strings.TrimSpace(rest)
	if slug == "" {
		_ = h.Presence.Write("Usage: @edit <slug>\r\n")
		return OutcomeContinue
	}
	h.editor = &editorState{slug: slug}
	// If an existing script lives at slug, seed the editor with its
	// source. ".abort" still cancels without saving, so the safety net
	// stays intact.
	if sc, err := be.Scripts.Get(ctx, slug); err == nil {
		h.editor.lines = strings.Split(strings.ReplaceAll(sc.Source, "\r\n", "\n"), "\n")
		// strings.Split on a trailing "\n" produces an empty tail
		// element; drop it so the user does not see an extra blank
		// line on save.
		if n := len(h.editor.lines); n > 0 && h.editor.lines[n-1] == "" {
			h.editor.lines = h.editor.lines[:n-1]
		}
		_ = h.Presence.Write(fmt.Sprintf(
			"Editing %q (%d existing lines). End input with \".\" on its own line; \".abort\" cancels.\r\n",
			slug, len(h.editor.lines)))
	} else {
		_ = h.Presence.Write(fmt.Sprintf(
			"Editing new %q. End input with \".\" on its own line; \".abort\" cancels.\r\n",
			slug))
	}
	return OutcomeContinue
}

// editorLine consumes a single line in paste mode. Returns OutcomeContinue
// to keep the session loop running.
func (h *Handler) editorLine(ctx context.Context, line string) Outcome {
	be := h.adminBackend()
	if be == nil || h.editor == nil {
		h.editor = nil
		return OutcomeContinue
	}
	// Trim only trailing CR so a CRLF stream still produces clean lines.
	line = strings.TrimRight(line, "\r")
	trimmed := strings.TrimSpace(line)
	switch trimmed {
	case ".":
		source := strings.Join(h.editor.lines, "\n")
		slug := h.editor.slug
		h.editor = nil
		ownerID := int64(0)
		if h.Presence.Account != nil {
			ownerID = h.Presence.Account.ID
		}
		if err := be.Scripts.Save(ctx, slug, ownerID, source); err != nil {
			_ = h.Presence.Write("Save failed: " + err.Error() + "\r\n")
			return OutcomeContinue
		}
		_ = h.Presence.Write(fmt.Sprintf("Saved %q (%d lines).\r\n", slug, lineCount(source)))
		return OutcomeContinue
	case ".abort":
		h.editor = nil
		_ = h.Presence.Write("Aborted.\r\n")
		return OutcomeContinue
	}
	h.editor.lines = append(h.editor.lines, line)
	return OutcomeContinue
}

func lineCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// world is imported in admin.go for the worldapi types declared in this
// package; keep this reference alive so go imports does not strip it.
var _ = world.RoomID(0)

// ---------------------------------------------------------------------------
// M6.6 security @-commands.

// cmdUsernameDeny dispatches @username-deny subcommands. admin-only.
func (h *Handler) cmdUsernameDeny(ctx context.Context, rest string) Outcome {
	if outcome, ok := h.requireAdmin(false); !ok {
		return outcome
	}
	be := h.adminBackend()
	if be == nil || be.API == nil || be.API.Security == nil {
		return OutcomeUnknown
	}
	sub, rest := splitCmd(rest)
	switch strings.ToLower(sub) {
	case "add":
		name, rest := splitCmd(rest)
		if name == "" {
			_ = h.Presence.Write("Usage: @username-deny add <name> [reason]\r\n")
			return OutcomeContinue
		}
		reason := strings.TrimSpace(stripQuotes(rest))
		actor := h.actorID()
		var addedBy *int64
		if actor > 0 {
			addedBy = &actor
		}
		if err := be.API.Security.DisallowUsername(ctx, name, reason, addedBy); err != nil {
			_ = h.Presence.Write(formatSecurityErr(err))
			return OutcomeContinue
		}
		h.adminAuditCmd("username_deny_add", name, "reason", reason)
		_ = h.Presence.Write("Added \"" + name + "\" to the disallow list.\r\n")
	case "remove", "rm":
		name := strings.TrimSpace(rest)
		if name == "" {
			_ = h.Presence.Write("Usage: @username-deny remove <name>\r\n")
			return OutcomeContinue
		}
		if err := be.API.Security.AllowUsername(ctx, name); err != nil {
			_ = h.Presence.Write(formatSecurityErr(err))
			return OutcomeContinue
		}
		h.adminAuditCmd("username_deny_remove", name)
		_ = h.Presence.Write("Removed \"" + name + "\" from the disallow list.\r\n")
	case "list", "":
		rows, err := be.API.Security.ListDisallowed(ctx)
		if err != nil {
			_ = h.Presence.Write(formatSecurityErr(err))
			return OutcomeContinue
		}
		if len(rows) == 0 {
			_ = h.Presence.Write("(disallow list is empty)\r\n")
			return OutcomeContinue
		}
		_ = h.Presence.Write(fmt.Sprintf("%-24s  %s\r\n", "name", "reason"))
		for _, r := range rows {
			_ = h.Presence.Write(fmt.Sprintf("%-24s  %s\r\n", r.Username, r.Reason))
		}
	default:
		_ = h.Presence.Write("Usage: @username-deny add|remove|list [args]\r\n")
	}
	return OutcomeContinue
}

// cmdUsernameHistory dispatches @username-history subcommands. admin-only.
func (h *Handler) cmdUsernameHistory(ctx context.Context, rest string) Outcome {
	if outcome, ok := h.requireAdmin(false); !ok {
		return outcome
	}
	be := h.adminBackend()
	if be == nil || be.API == nil || be.API.Security == nil {
		return OutcomeUnknown
	}
	sub, rest := splitCmd(rest)
	switch strings.ToLower(sub) {
	case "list", "":
		target := strings.TrimSpace(rest)
		var accountID int64
		if target != "" && !strings.EqualFold(target, "all") {
			acc, err := be.API.Accts.GetByUsername(ctx, target)
			if err != nil {
				_ = h.Presence.Write("error: account not found: " + target + "\r\n")
				return OutcomeContinue
			}
			accountID = acc.ID
		}
		rows, err := be.API.Security.ListUsernameHistory(ctx, accountID)
		if err != nil {
			_ = h.Presence.Write(formatSecurityErr(err))
			return OutcomeContinue
		}
		if len(rows) == 0 {
			_ = h.Presence.Write("(username history is empty)\r\n")
			return OutcomeContinue
		}
		_ = h.Presence.Write(fmt.Sprintf("%-20s  %-20s  %s\r\n", "old", "renamed_to", "renamed_at"))
		for _, r := range rows {
			when := time.Unix(r.RenamedAt, 0).UTC().Format("2006-01-02 15:04")
			_ = h.Presence.Write(fmt.Sprintf("%-20s  %-20s  %s\r\n", r.OldUsername, r.RenamedTo, when))
		}
	case "release":
		name := strings.TrimSpace(rest)
		if name == "" {
			_ = h.Presence.Write("Usage: @username-history release <name>\r\n")
			return OutcomeContinue
		}
		if err := be.API.Security.ReleaseUsernameHistory(ctx, name); err != nil {
			_ = h.Presence.Write(formatSecurityErr(err))
			return OutcomeContinue
		}
		h.adminAuditCmd("username_history_release", name)
		_ = h.Presence.Write("Released \"" + name + "\" from the username-history reservation.\r\n")
	default:
		_ = h.Presence.Write("Usage: @username-history list [user|all] | release <name>\r\n")
	}
	return OutcomeContinue
}

// cmdIPDeny dispatches @ip-deny subcommands. admin-only.
func (h *Handler) cmdIPDeny(ctx context.Context, rest string) Outcome {
	if outcome, ok := h.requireAdmin(false); !ok {
		return outcome
	}
	be := h.adminBackend()
	if be == nil || be.API == nil || be.API.Security == nil {
		return OutcomeUnknown
	}
	sub, rest := splitCmd(rest)
	switch strings.ToLower(sub) {
	case "add":
		ipStr, rest := splitCmd(rest)
		addr, err := netip.ParseAddr(ipStr)
		if err != nil {
			_ = h.Presence.Write("error: invalid IP: " + ipStr + "\r\n")
			return OutcomeContinue
		}
		kind, rest := splitCmd(rest)
		actor := h.actorID()
		var addedBy *int64
		if actor > 0 {
			addedBy = &actor
		}
		switch strings.ToLower(kind) {
		case "permanent", "perm", "p":
			reason := strings.TrimSpace(stripQuotes(rest))
			if err := be.API.Security.AddPermanentDeny(ctx, addr, addedBy, reason); err != nil {
				_ = h.Presence.Write(formatSecurityErr(err))
				return OutcomeContinue
			}
			h.adminAuditCmd("ip_deny_add", addr.String(), "kind", "permanent", "reason", reason)
			_ = h.Presence.Write("Added permanent deny for " + addr.String() + ".\r\n")
		case "temp", "t", "":
			ttlStr, rest := splitCmd(rest)
			ttl := time.Duration(0)
			if ttlStr != "" {
				n, perr := strconv.Atoi(ttlStr)
				if perr != nil || n < 0 {
					_ = h.Presence.Write("error: invalid ttl seconds: " + ttlStr + "\r\n")
					return OutcomeContinue
				}
				ttl = time.Duration(n) * time.Second
			}
			reason := strings.TrimSpace(stripQuotes(rest))
			if err := be.API.Security.AddTempDeny(ctx, addr, ttl, addedBy, reason); err != nil {
				_ = h.Presence.Write(formatSecurityErr(err))
				return OutcomeContinue
			}
			h.adminAuditCmd("ip_deny_add", addr.String(), "kind", "temp", "ttl", int(ttl.Seconds()), "reason", reason)
			_ = h.Presence.Write("Added temp deny for " + addr.String() + ".\r\n")
		default:
			_ = h.Presence.Write("Usage: @ip-deny add <ip> [permanent|temp <seconds>] [reason]\r\n")
		}
	case "remove", "rm":
		ipStr := strings.TrimSpace(rest)
		addr, err := netip.ParseAddr(ipStr)
		if err != nil {
			_ = h.Presence.Write("error: invalid IP: " + ipStr + "\r\n")
			return OutcomeContinue
		}
		if err := be.API.Security.RemoveDeny(ctx, addr); err != nil {
			_ = h.Presence.Write(formatSecurityErr(err))
			return OutcomeContinue
		}
		h.adminAuditCmd("ip_deny_remove", addr.String())
		_ = h.Presence.Write("Removed deny for " + addr.String() + ".\r\n")
	case "flush-temp", "flush":
		n, err := be.API.Security.FlushTempDenials(ctx)
		if err != nil {
			_ = h.Presence.Write(formatSecurityErr(err))
			return OutcomeContinue
		}
		h.adminAuditCmd("ip_deny_flush_temp", "*", "count", n)
		_ = h.Presence.Write(fmt.Sprintf("Flushed %d temporary deny(s).\r\n", n))
	case "list", "":
		filter := strings.ToLower(strings.TrimSpace(rest))
		rows := be.API.Security.ListDenials()
		if len(rows) == 0 {
			_ = h.Presence.Write("(deny list is empty)\r\n")
			return OutcomeContinue
		}
		_ = h.Presence.Write(fmt.Sprintf("%-6s  %-15s  %-19s  %s\r\n", "kind", "ip", "expires", "reason"))
		for _, r := range rows {
			marker := "[T]"
			when := ""
			if r.Permanent() {
				marker = "[P]"
				when = "—"
			} else {
				when = r.ExpiresAt.Format("2006-01-02 15:04:05")
			}
			if filter == "permanent" && !r.Permanent() {
				continue
			}
			if filter == "temp" && r.Permanent() {
				continue
			}
			_ = h.Presence.Write(fmt.Sprintf("%-6s  %-15s  %-19s  %s\r\n", marker, r.IP.String(), when, r.Reason))
		}
	default:
		_ = h.Presence.Write("Usage: @ip-deny add|remove|list|flush-temp [args]\r\n")
	}
	return OutcomeContinue
}

// cmdRename implements @rename <user> <newname>. admin-only.
func (h *Handler) cmdRename(ctx context.Context, rest string) Outcome {
	if outcome, ok := h.requireAdmin(false); !ok {
		return outcome
	}
	be := h.adminBackend()
	if be == nil || be.API == nil {
		return OutcomeUnknown
	}
	cur, rest := splitCmd(rest)
	newName := strings.TrimSpace(rest)
	if cur == "" || newName == "" {
		_ = h.Presence.Write("Usage: @rename <user> <newname>\r\n")
		return OutcomeContinue
	}
	if err := be.API.RenameAccount(ctx, cur, newName, h.actorID()); err != nil {
		_ = h.Presence.Write(formatAdminError(err))
		return OutcomeContinue
	}
	h.adminAuditCmd("rename_account", cur, "new", newName)
	_ = h.Presence.Write("Renamed " + cur + " → " + newName + ".\r\n")
	return OutcomeContinue
}

// actorID returns the calling admin's account id, or 0 when unknown
// (test paths without a presence). Used to fill the added_by /
// renamed_by columns of audit rows.
func (h *Handler) actorID() int64 {
	if h.Presence == nil || h.Presence.Account == nil {
		return 0
	}
	return h.Presence.Account.ID
}

// adminAuditCmd emits one structured slog line per admin-mutation
// @-command, mirroring adminAudit in the menu package so external
// tooling sees the same event names regardless of entry point.
func (h *Handler) adminAuditCmd(action, target string, args ...any) {
	be := h.adminBackend()
	if be == nil || be.API == nil || be.API.Logger == nil {
		return
	}
	actor := ""
	if h.Presence != nil && h.Presence.Account != nil {
		actor = h.Presence.Account.Username
	}
	fields := []any{"actor", actor, "action", action, "target", target}
	fields = append(fields, args...)
	be.API.Logger.Info("admin_audit", fields...)
}

// formatSecurityErr renders a security.Service error as a one-line
// admin message. Recognises the package's sentinel errors so the user
// sees a stable message rather than the wrapped Go form.
func formatSecurityErr(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, security.ErrUsernameDisallowed):
		return "error: username is disallowed\r\n"
	case errors.Is(err, security.ErrUsernameReserved):
		return "error: username is reserved by username_history\r\n"
	case errors.Is(err, security.ErrDisallowReservedKeyword):
		return "error: cannot disallow the account-create keyword\r\n"
	case errors.Is(err, security.ErrDisallowExistingAccount):
		return "error: name collides with an existing account — rename or delete the account first\r\n"
	}
	return "error: " + err.Error() + "\r\n"
}
