// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package cmd

import (
	"context"
	"errors"
	"strings"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/world"
	"github.com/vaelen/wintermute/internal/world/engage"
	"github.com/vaelen/wintermute/internal/world/render"
)

// NPCReloader is the minimal interface the cmd package needs from the
// NPC registry. Keeping it as an interface avoids importing the npc
// package (and the LLM/sqlite chain) into the world cmd package.
type NPCReloader interface {
	Reload(ctx context.Context) error
}

// EngageBackend bundles the engage dependencies the world cmd handler
// needs to dispatch engage verbs and open engagements. One instance is
// shared across all sessions.
type EngageBackend struct {
	Registry *engage.Registry
	Hosts    *engage.HostCache
	OpenFn   func(host *engage.Host, presence *world.Presence, sb engage.SessionBinding) error
}

// Handler binds a Presence to a World. Each session creates one Handler
// after login and routes every input line through Dispatch.
type Handler struct {
	World    *world.World
	Presence *world.Presence
	// NPC is the npc registry, used by admin commands. May be nil
	// (e.g. in tests where NPC reactivity isn't exercised).
	NPC NPCReloader
	// Admin bundles the dependencies the admin @-commands need
	// (world API, scripts table access, Lua pool, tool registry).
	// May be nil; affected commands then return OutcomeUnknown so
	// they stay invisible.
	Admin *AdminBackend

	// Engage is the engagement integration. May be nil in tests where
	// engagement isn't exercised. The OpenFn is what actually constructs
	// the right handler (terminal vs npc) when an engage verb succeeds.
	Engage *EngageBackend

	// SB is the session-side engagement binding. Optional: when nil,
	// OpenFn (in main.go) falls back to non-session-aware Open. Tests
	// without engage wiring leave this nil.
	SB engage.SessionBinding

	// editor, when non-nil, captures every subsequent input line as
	// script source until a "." terminator closes paste mode. See
	// cmdEdit and editorLine in admin.go.
	editor *editorState
}

// Outcome reports a special command result that the session loop must act
// on. Quit ends the session normally; OutcomeUnknown means the dispatcher
// didn't recognize the command; OutcomeDetached means the world has
// unregistered this presence and the session must exit.
type Outcome int

// Outcome values.
const (
	OutcomeContinue Outcome = iota
	OutcomeQuit
	OutcomeUnknown
	OutcomeDetached
)

// InPasteMode reports whether the handler is currently capturing input
// lines into the @edit paste-mode buffer. Session-level callers use
// this to skip the in-line line editor while paste mode is active —
// arrow keys must reach the script source as literal bytes (or be
// dropped), not be interpreted as history recall.
func (h *Handler) InPasteMode() bool {
	return h != nil && h.editor != nil
}

// Dispatch parses a single input line and runs the corresponding command.
// It returns OutcomeUnknown when the command name is not recognized — the
// caller decides whether to print an error or fall through to a different
// command set (e.g. the milestone-1 `terminal` command).
//
// Errors from the world layer are translated into user-facing messages by
// this function; only programmer errors (failed Write to the session)
// bubble up.
func (h *Handler) Dispatch(ctx context.Context, line string) Outcome {
	if h.Presence.IsDetached() {
		return OutcomeDetached
	}
	// @edit paste-mode: lines after `@edit <slug>` are captured here
	// until a line containing only "." closes the editor and writes
	// the script. Trimming happens inside editorLine so an empty
	// content line ("") still becomes part of the script source.
	if h.editor != nil {
		return h.editorLine(ctx, line)
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return OutcomeContinue
	}

	cmd, rest := splitCmd(line)
	var outcome Outcome
	switch strings.ToLower(cmd) {
	case "quit", "logout", "disconnect":
		_ = h.Presence.Write("Goodbye.\r\n")
		return OutcomeQuit

	case "look", "l":
		outcome = h.cmdLook(rest)
	case "n", "north", "s", "south", "e", "east", "w", "west",
		"u", "up", "d", "down", "in", "out":
		outcome = h.cmdMove(ctx, canonicalDirection(cmd))
	case "go":
		dir := strings.ToLower(strings.TrimSpace(rest))
		if dir == "" {
			_ = h.Presence.Write("Go where?\r\n")
			outcome = OutcomeContinue
			break
		}
		outcome = h.cmdMove(ctx, canonicalDirection(dir))
	case "say", "'":
		outcome = h.cmdSay(rest)
	case "emote", ":":
		outcome = h.cmdEmote(rest)
	case "who":
		h.cmdWho()
		outcome = OutcomeContinue
	case "inventory", "i", "inv":
		h.cmdInventory()
		outcome = OutcomeContinue
	case "get", "take":
		outcome = h.cmdTake(ctx, rest)
	case "drop":
		outcome = h.cmdDrop(ctx, rest)
	case "help", "?":
		h.cmdHelp()
		outcome = OutcomeContinue
	case "@npcreload":
		outcome = h.cmdNPCReload(ctx)
	case "@create-room":
		outcome = h.cmdCreateRoom(ctx, rest)
	case "@dig":
		outcome = h.cmdDig(ctx, rest)
	case "@create-door":
		outcome = h.cmdCreateDoor(ctx, rest)
	case "@door-msg":
		outcome = h.cmdDoorMsg(ctx, rest)
	case "@create-npc":
		outcome = h.cmdCreateNPC(ctx, rest)
	case "@persona":
		outcome = h.cmdPersona(ctx, rest)
	case "@script":
		outcome = h.cmdScript(ctx, rest)
	case "@run":
		outcome = h.cmdRun(ctx, rest)
	case "@tools":
		outcome = h.cmdTools()
	case "@invoke":
		outcome = h.cmdInvoke(ctx, rest)
	case "@reload-scripts":
		outcome = h.cmdReloadScripts(ctx)
	case "@boot":
		outcome = h.cmdBoot(ctx, rest)
	case "@edit":
		outcome = h.cmdEdit(ctx, rest)
	case "@help":
		outcome = h.cmdAtHelp()
	default:
		if out := h.tryEngage(line); out != OutcomeUnknown {
			return out
		}
		return OutcomeUnknown
	}
	// Admin-only commands return OutcomeUnknown when the caller lacks
	// access; surface that to the session loop so it falls through to
	// the unknown-command path and the command stays invisible to
	// non-admins.
	if outcome == OutcomeUnknown {
		return OutcomeUnknown
	}
	if outcome == OutcomeDetached {
		return OutcomeDetached
	}
	if h.Presence.IsDetached() {
		return OutcomeDetached
	}
	return OutcomeContinue
}

func (h *Handler) cmdLook(target string) Outcome {
	if target == "" {
		h.showRoom()
		return OutcomeContinue
	}
	obj, err := h.World.FindVisible(h.Presence.PlayerID, target)
	if err != nil {
		var amb *world.AmbiguousMatchError
		if errors.As(err, &amb) {
			_ = h.Presence.Write(didYouMean(amb.Candidates))
		} else {
			_ = h.Presence.Write("You see nothing like that here.\r\n")
		}
		return OutcomeContinue
	}
	_ = h.Presence.Write(render.ObjectLong(obj))
	return OutcomeContinue
}

func (h *Handler) cmdMove(ctx context.Context, dir string) Outcome {
	if dir == "" {
		_ = h.Presence.Write("Go where?\r\n")
		return OutcomeContinue
	}
	if _, err := h.World.Move(ctx, h.Presence, dir); err != nil {
		if errors.Is(err, world.ErrStalePresence) {
			return OutcomeDetached
		}
		_ = h.Presence.Write("You can't go that way.\r\n")
		return OutcomeContinue
	}
	h.showRoom()
	return OutcomeContinue
}

func (h *Handler) cmdSay(rest string) Outcome {
	if strings.TrimSpace(rest) == "" {
		_ = h.Presence.Write("Say what?\r\n")
		return OutcomeContinue
	}
	if err := h.World.Say(h.Presence, rest); err != nil {
		if errors.Is(err, world.ErrStalePresence) {
			return OutcomeDetached
		}
		_ = h.Presence.Write("You try to speak, but the world is silent.\r\n")
	}
	return OutcomeContinue
}

func (h *Handler) cmdEmote(rest string) Outcome {
	if strings.TrimSpace(rest) == "" {
		_ = h.Presence.Write("Emote what?\r\n")
		return OutcomeContinue
	}
	if err := h.World.Emote(h.Presence, rest); err != nil {
		if errors.Is(err, world.ErrStalePresence) {
			return OutcomeDetached
		}
	}
	return OutcomeContinue
}

func (h *Handler) cmdWho() {
	names := h.World.WhoOnline()
	// Filter out self for a friendlier reading.
	self := h.selfName()
	if self != "" {
		filtered := names[:0]
		for _, n := range names {
			if n != self {
				filtered = append(filtered, n)
			}
		}
		names = filtered
	}
	_ = h.Presence.Write(render.Who(names))
}

func (h *Handler) cmdInventory() {
	items := h.World.Inventory(h.Presence.PlayerID)
	_ = h.Presence.Write(render.Inventory(items))
}

func (h *Handler) cmdTake(ctx context.Context, target string) Outcome {
	if strings.TrimSpace(target) == "" {
		_ = h.Presence.Write("Take what?\r\n")
		return OutcomeContinue
	}
	obj, err := h.World.Take(ctx, h.Presence, target)
	if err != nil {
		var amb *world.AmbiguousMatchError
		switch {
		case errors.Is(err, world.ErrStalePresence):
			return OutcomeDetached
		case errors.As(err, &amb):
			_ = h.Presence.Write(didYouMean(amb.Candidates))
		case errors.Is(err, world.ErrNotTakeable):
			_ = h.Presence.Write("You can't take that.\r\n")
		default:
			_ = h.Presence.Write("You don't see that here.\r\n")
		}
		return OutcomeContinue
	}
	_ = h.Presence.Write("You pick up " + obj.Name + ".\r\n")
	return OutcomeContinue
}

func (h *Handler) cmdDrop(ctx context.Context, target string) Outcome {
	if strings.TrimSpace(target) == "" {
		_ = h.Presence.Write("Drop what?\r\n")
		return OutcomeContinue
	}
	obj, err := h.World.Drop(ctx, h.Presence, target)
	if err != nil {
		var amb *world.AmbiguousMatchError
		switch {
		case errors.Is(err, world.ErrStalePresence):
			return OutcomeDetached
		case errors.As(err, &amb):
			_ = h.Presence.Write(didYouMean(amb.Candidates))
		default:
			_ = h.Presence.Write("You aren't carrying that.\r\n")
		}
		return OutcomeContinue
	}
	_ = h.Presence.Write("You drop " + obj.Name + ".\r\n")
	return OutcomeContinue
}

// cmdNPCReload re-reads npc_config from the database. Admin-only; hidden
// from non-admins by returning OutcomeUnknown so they see the same
// "Unknown command" reply as for any other unrecognised input.
func (h *Handler) cmdNPCReload(ctx context.Context) Outcome {
	if h.NPC == nil {
		return OutcomeUnknown
	}
	if h.Presence == nil || h.Presence.Account == nil ||
		h.Presence.Account.AccessLevel != auth.AccessAdmin {
		return OutcomeUnknown
	}
	if err := h.NPC.Reload(ctx); err != nil {
		_ = h.Presence.Write("NPC reload failed: " + err.Error() + "\r\n")
		return OutcomeContinue
	}
	_ = h.Presence.Write("NPC registry reloaded.\r\n")
	return OutcomeContinue
}

func (h *Handler) cmdHelp() {
	lines := []string{
		"Commands available:",
		"",
		"Movement and looking:",
		"  look [object]      — describe the room or an object (alias: l)",
		"  n s e w u d in out — move in that direction (also: north, ..., 'go <dir>')",
		"",
		"Communication:",
		"  say <text>         — speak to your room (alias: '<text>)",
		"  emote <text>       — describe what you're doing (alias: :<text>)",
		"",
		"Objects:",
		"  get <item>         — pick something up (alias: take)",
		"  drop <item>        — drop something you're carrying",
		"  inventory          — list what you're carrying (aliases: i, inv)",
		"",
		"Other:",
		"  who                — list other awake players",
		"  motd               — re-display the message of the day",
		"  terminal           — show or change terminal settings",
		"    terminal encoding <utf8|cp437|iso88591|macroman|petscii|ascii>",
		"    terminal width <n> | height <n>",
		"    terminal color on|off | lines vt100|native | echo on|off",
		"  quit               — disconnect (aliases: logout, disconnect)",
		"",
	}
	lines = append(lines, h.adminHelpLines()...)
	_ = h.Presence.Write(strings.Join(lines, "\r\n"))
}

// adminHelpLines returns the access-gated portion of the help output:
// nothing for ordinary players, the build subset for builders, and the
// full @-command surface for admins. Returned slice is empty when the
// caller has no elevated access so callers can append unconditionally.
func (h *Handler) adminHelpLines() []string {
	if h.Presence == nil || h.Presence.Account == nil {
		return nil
	}
	lvl := h.Presence.Account.AccessLevel
	if lvl != auth.AccessAdmin && lvl != auth.AccessBuilder {
		return nil
	}
	out := []string{
		"World building (admin or builder):",
		"  @create-room <slug> \"<name>\"           — create a new room",
		"  @dig <direction> <room-slug>           — create a door pair from here",
		"  @create-door <slug> <dir> <to-room>    — single one-way door from here",
		"  @door-msg <slug> leave|arrive \"<tmpl>\" — set door broadcast template",
		"",
	}
	if lvl != auth.AccessAdmin {
		return out
	}
	out = append(out,
		"NPCs (admin):",
		"  @create-npc <slug> \"<name>\" \"<persona>\" — spawn an NPC here",
		"  @persona <npc-slug> \"<persona>\"         — replace an NPC's persona",
		"  @npcreload                              — re-read npc_config from disk",
		"",
		"Scripts and tools (admin):",
		"  @script <slug>                          — show a stored script's source",
		"  @edit <slug>                            — open paste-mode editor",
		"                                            (end with \".\"; cancel with \".abort\")",
		"  @run <slug>                             — execute a stored script",
		"  @tools                                  — list registered tools",
		"  @invoke <tool> [json-args]              — manually invoke a tool",
		"  @reload-scripts                         — re-run every init.* script",
		"",
		"Sessions (admin):",
		"  @boot <username> [message]              — force-disconnect a user",
		"",
		"Help:",
		"  @help                                   — same as help, listed for discoverability",
		"",
	)
	return out
}

// tryEngage attempts to match line as an engage command — either the
// universal "engage [with] <target>" or a host-specific engage verb for
// an object in the player's room or inventory. Returns OutcomeContinue
// on a match (regardless of whether the engagement actually opened —
// errors are reported to the player and the input is considered
// consumed). Returns OutcomeUnknown if no verb matched.
func (h *Handler) tryEngage(line string) Outcome {
	if h.Engage == nil || h.Engage.OpenFn == nil || h.Engage.Hosts == nil {
		return OutcomeUnknown
	}
	loc, err := h.World.LocationOf(h.Presence.PlayerID)
	if err != nil {
		return OutcomeUnknown
	}
	candidates := h.Engage.Hosts.FilterRoomAndInventory(
		loc.RoomID, h.Presence.PlayerID,
		func(id world.ObjectID) (world.Location, bool) {
			l, err := h.World.LocationOf(id)
			return l, err == nil
		})

	if target, ok := engage.MatchUniversalEngageVerb(line); ok {
		return h.openByTarget(candidates, target)
	}
	if host, _, ok := engage.MatchEngageVerb(line, candidates); ok {
		return h.openMatchedHost(host)
	}
	return OutcomeUnknown
}

// openByTarget is the universal-verb path: resolve target via the
// world's name matcher, then open the engagement for that object if
// it's an engageable candidate.
func (h *Handler) openByTarget(candidates []*engage.Host, target string) Outcome {
	obj, err := h.World.FindVisible(h.Presence.PlayerID, target)
	if err != nil {
		var amb *world.AmbiguousMatchError
		if errors.As(err, &amb) {
			_ = h.Presence.Write(didYouMean(amb.Candidates))
		} else {
			_ = h.Presence.Write("Engage with what?\r\n")
		}
		return OutcomeContinue
	}
	for _, host := range candidates {
		if host.ObjectID == obj.ID {
			return h.openMatchedHost(host)
		}
	}
	_ = h.Presence.Write("That isn't something you can engage with.\r\n")
	return OutcomeContinue
}

// openMatchedHost calls into the OpenFn and renders any error.
func (h *Handler) openMatchedHost(host *engage.Host) Outcome {
	if err := h.Engage.OpenFn(host, h.Presence, h.SB); err != nil {
		obj, _ := h.World.Object(host.ObjectID)
		name := "it"
		if obj.Name != "" {
			name = obj.Name
		}
		switch {
		case errors.Is(err, engage.ErrHostBusy):
			_ = h.Presence.Write(name + " is occupied.\r\n")
		case errors.Is(err, engage.ErrAlreadyEngaged):
			_ = h.Presence.Write("You're already engaged.\r\n")
		default:
			_ = h.Presence.Write("Engagement failed: " + err.Error() + "\r\n")
		}
	}
	return OutcomeContinue
}

// showRoom renders the player's current room. Called after `look` with no
// arg, after `move`, and on initial login.
func (h *Handler) showRoom() {
	loc, err := h.World.LocationOf(h.Presence.PlayerID)
	if err != nil {
		_ = h.Presence.Write("You float in an undefined void.\r\n")
		return
	}
	room, err := h.World.Room(loc.RoomID)
	if err != nil {
		_ = h.Presence.Write("You float in an undefined void.\r\n")
		return
	}
	view := render.RoomView{
		Room:    room,
		Self:    h.Presence.PlayerID,
		Players: h.World.PlayersInRoom(loc.RoomID),
		NPCs:    h.World.NPCsInRoom(loc.RoomID),
		Items:   h.World.ItemsInRoom(loc.RoomID),
	}
	_ = h.Presence.Write(render.Room(view))
}

// ShowRoom is the exported form of showRoom for use by the session layer
// just after attach.
func (h *Handler) ShowRoom() { h.showRoom() }

func (h *Handler) selfName() string {
	obj, err := h.World.Object(h.Presence.PlayerID)
	if err != nil {
		return ""
	}
	return obj.Name
}

func splitCmd(line string) (cmd, rest string) {
	// Single-character prefix shortcuts: ' for say, : for emote.
	if len(line) > 0 && (line[0] == '\'' || line[0] == ':') {
		return string(line[0]), strings.TrimSpace(line[1:])
	}
	i := strings.IndexAny(line, " \t")
	if i < 0 {
		return line, ""
	}
	return line[:i], strings.TrimSpace(line[i+1:])
}

// canonicalDirection collapses long-form direction words ("north", "up")
// to the single-letter codes stored in the exits table.
func canonicalDirection(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "n", "north":
		return "n"
	case "s", "south":
		return "s"
	case "e", "east":
		return "e"
	case "w", "west":
		return "w"
	case "u", "up":
		return "u"
	case "d", "down":
		return "d"
	case "in":
		return "in"
	case "out":
		return "out"
	default:
		return strings.ToLower(strings.TrimSpace(s))
	}
}

// didYouMean renders a disambiguation prompt for a list of candidate
// object names. Returns a complete line ending with CRLF.
func didYouMean(candidates []string) string {
	switch len(candidates) {
	case 0:
		return "Which one?\r\n"
	case 1:
		return "Did you mean " + candidates[0] + "?\r\n"
	case 2:
		return "Did you mean " + candidates[0] + " or " + candidates[1] + "?\r\n"
	default:
		head := strings.Join(candidates[:len(candidates)-1], ", ")
		return "Did you mean " + head + ", or " + candidates[len(candidates)-1] + "?\r\n"
	}
}
