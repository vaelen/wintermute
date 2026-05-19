// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package session

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/vaelen/wintermute/internal/term"
	"github.com/vaelen/wintermute/internal/world"
	worldcmd "github.com/vaelen/wintermute/internal/world/cmd"
	"github.com/vaelen/wintermute/internal/world/engage"
)

// defaultMetaCommands are world commands that remain available while a
// session is engaged. Movement commands are included so that wh.Dispatch
// (which calls cmdMove → closeEngagementIfAny) handles auto-disengage
// before the move, matching the design in docs/milestones/05.7.
var defaultMetaCommands = []string{
	"look", "l", "who", "help", "?",
	// movement directions — auto-disengage happens inside cmdMove
	"n", "north", "s", "south", "e", "east", "w", "west",
	"u", "up", "d", "down", "in", "out", "go",
	// terminal capability tuning works mid-engagement so a player can
	// resize / re-encode without disengaging. Width changes trigger a
	// live re-render via the engage.Resizer hook (see reconfigure).
	"terminal",
}

// menuMetaCommands is the subset of defaultMetaCommands that still apply
// inside a menu_terminal engagement. Menu hosts are modal: their state
// machine owns every keystroke (numbered selectors, B for Back, Q for
// Quit, plus per-screen letters like N for Next page and U for "promote
// to builder"). Routing single-letter movement directions to the world
// parser there would close the engagement out from under the user.
// Universal world commands (look/who/help/terminal) stay reachable so
// the player can still glance at the world or resize their terminal
// mid-menu.
var menuMetaCommands = []string{
	"look", "l", "who", "help", "?", "terminal",
}

// commandLoop runs the post-login input loop. The line is first offered to
// the world command handler (look/move/say/...); anything the world
// doesn't recognize is dispatched here as a session-level command
// (terminal, motd, help).
//
// The exit reason determines how the world broadcasts the player's
// departure: an explicit `quit` produces "X goes to sleep." while a
// dropped socket or force-detach produces "X fell asleep.". Defaulting
// to dropped means a panic or unhandled error path is still surfaced as
// a link drop, never a clean quit.
func (h *Handler) commandLoop(ctx context.Context, s *Session) {
	wh := h.attachToWorld(ctx, s)
	if wh == nil {
		return
	}
	reason := world.DisconnectDropped
	defer func() { h.detachFromWorld(s, reason) }()

	wh.ShowRoom()

	for {
		if s.Engagement() == nil {
			if err := s.writeString("> "); err != nil {
				return
			}
		}
		// Paste mode (@edit) captures every subsequent line as script
		// source. Falling back to the simple loop keeps arrow keys and
		// other CSI bytes out of the recall path so the user can't
		// accidentally yank a previous command into the script.
		var line string
		var err error
		if wh.InPasteMode() {
			line, err = s.readLine()
		} else {
			line, err = s.readLineEditing()
		}
		if err != nil && line == "" {
			return
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !wh.InPasteMode() {
			s.history.Add(line)
		}

		// Modal dispatch: if engaged, free input goes to the engagement
		// handler; the meta-command whitelist falls through to the world
		// parser; disengage verbs close the engagement.
		if eng := s.Engagement(); eng != nil {
			// If the registry no longer knows about this engagement, it
			// was force-closed (e.g. host destroyed). Clear our pointer
			// and let the world parser handle this line.
			if h.EngageRegistry != nil && h.EngageRegistry.HostEngagement(eng.Host.ObjectID) != eng {
				s.SetEngagement(nil)
			} else {
				if engage.MatchDisengageVerb(line, eng.Host) {
					if h.EngageRegistry != nil {
						engage.CloseForSession(h.EngageRegistry, s, engage.CloseVoluntary)
					}
					continue
				}
				if !engagementMetaCommand(line, eng) {
					if p := participantFor(s, eng); p != nil {
						eng.Handler.Handle(p, line)
					}
					continue
				}
				// fall through to wh.Dispatch
			}
		}

		switch wh.Dispatch(ctx, line) {
		case worldcmd.OutcomeQuit:
			reason = world.DisconnectQuit
			return
		case worldcmd.OutcomeDetached:
			// The world has unregistered our presence — typically because
			// a newer login force-detached this session. Exit silently;
			// the prompt would just be confusing at this point.
			return
		case worldcmd.OutcomeContinue:
			continue
		case worldcmd.OutcomeUnknown:
			// fall through to session-level commands
		}

		cmd, rest := splitCmd(line)
		switch strings.ToLower(cmd) {
		case "terminal":
			h.cmdTerminal(ctx, s, rest)
		case "motd":
			h.cmdMOTD(s)
		default:
			_ = s.writef("Unknown command: %q (try 'help').\r\n", cmd)
		}
	}
}

// attachToWorld builds a Presence for the session and registers it with
// the world. Returns the world cmd Handler, or nil if attach failed (in
// which case an error has already been written to the session).
//
// If an older session is still attached for this account, it is force-
// detached first. The world marks the old Presence stale, so any in-
// flight commands from that old session will be rejected (ErrStalePresence)
// and its command loop will exit on the next iteration.
func (h *Handler) attachToWorld(ctx context.Context, s *Session) *worldcmd.Handler {
	if h.World == nil || s.account == nil {
		_ = s.writeString("The world is unavailable. Please try again later.\r\n")
		return nil
	}
	playerID, err := h.World.PlayerByAccount(s.account.ID)
	if err != nil {
		// Lazy bootstrap: account exists but body doesn't (e.g. account
		// predates this migration). Create one now.
		playerID, err = h.World.CreatePlayer(ctx, s.account)
		if err != nil {
			s.log.Error("create player object", "err", err)
			_ = s.writeString("The world refuses to acknowledge you. (Could not create your body.)\r\n")
			return nil
		}
	}
	s.playerID = playerID

	pres := &world.Presence{
		PlayerID:   playerID,
		Account:    s.account,
		Write:      s.writeString,
		Log:        s.log,
		SessionID:  s.id,
		TermWidth:  s.caps.Width,
		TermHeight: s.caps.Height,
		TermType:   s.caps.TermType,
	}
	s.presence = pres
	if _, err := h.World.Attach(pres); err != nil {
		if err == world.ErrAlreadyAttached {
			h.World.Detach(playerID, world.DisconnectDropped)
			if _, err = h.World.Attach(pres); err != nil {
				s.log.Error("re-attach failed", "err", err)
				_ = s.writeString("You are already logged in elsewhere.\r\n")
				return nil
			}
		} else {
			s.log.Error("world attach failed", "err", err)
			_ = s.writeString("The world refuses to acknowledge you.\r\n")
			return nil
		}
	}
	return &worldcmd.Handler{World: h.World, Presence: pres, NPC: h.NPC, Admin: h.Admin, Engage: h.EngageBackend, SB: s}
}

func (h *Handler) detachFromWorld(s *Session, reason world.DisconnectReason) {
	if h.World == nil || s.playerID == 0 {
		return
	}
	// Close any active engagement first so the OnClose handler runs
	// before the player is removed from the room — otherwise the
	// exit broadcast would have no audience.
	if h.EngageRegistry != nil {
		engage.CloseForSession(h.EngageRegistry, s, engage.CloseDisconnect)
	}
	h.World.Detach(s.playerID, reason)
}

// engagementMetaCommand reports whether the line's first token should
// route through the world parser instead of the engagement handler.
// Universal meta-commands plus the host's disengage verbs qualify.
// Menu terminals use a tighter list — see menuMetaCommands.
func engagementMetaCommand(line string, eng *engage.Engagement) bool {
	if engage.MatchDisengageVerb(line, eng.Host) {
		return true
	}
	cmd, _ := splitCmd(line)
	low := strings.ToLower(cmd)
	if eng.Host.Kind == engage.KindMenuTerminal {
		return slices.Contains(menuMetaCommands, low)
	}
	return slices.Contains(defaultMetaCommands, low)
}

// participantFor returns the participant entry for s within eng. Returns
// nil if s is not in eng.Participants (which shouldn't happen during
// normal flow, since Open always adds the opening session as a participant).
func participantFor(s *Session, eng *engage.Engagement) *engage.Participant {
	for _, p := range eng.Participants {
		if p.SessionID == s.id {
			return p
		}
	}
	return nil
}

func splitCmd(line string) (cmd, rest string) {
	i := strings.IndexAny(line, " \t")
	if i < 0 {
		return line, ""
	}
	return line[:i], strings.TrimSpace(line[i+1:])
}

func (h *Handler) cmdMOTD(s *Session) {
	if h.MOTD == "" {
		_ = s.writeString("No message of the day.\r\n")
		return
	}
	_ = s.writeString(h.MOTD + "\r\n")
}

func (h *Handler) cmdTerminal(ctx context.Context, s *Session, args string) {
	if args == "" {
		h.printTerminalStatus(s)
		return
	}
	sub, rest := splitCmd(args)
	switch strings.ToLower(sub) {
	case "encoding":
		h.terminalSetEncoding(ctx, s, rest)
	case "width":
		h.terminalSetSize(ctx, s, rest, true)
	case "height":
		h.terminalSetSize(ctx, s, rest, false)
	case "color", "colour":
		h.terminalSetColor(ctx, s, rest)
	case "lines":
		h.terminalSetLines(ctx, s, rest)
	case "echo":
		h.terminalSetEcho(s, rest)
	case "detect":
		h.terminalDetect(ctx, s)
	default:
		_ = s.writef("Unknown 'terminal' subcommand: %q. Try 'help'.\r\n", sub)
	}
}

func (h *Handler) printTerminalStatus(s *Session) {
	c := s.enc.Capabilities()
	colorStr := "off"
	if c.Color {
		colorStr = "on"
	}
	linesStr := "native"
	if c.DECLineDrawing {
		linesStr = "vt100"
	}
	telnetStr := "no"
	if c.Telnet {
		telnetStr = "yes"
	}
	echoStr := "off"
	if s.echoOn() {
		echoStr = "on"
	}
	termTypeStr := c.TermType
	if termTypeStr == "" {
		termTypeStr = "(not reported)"
	}
	_ = s.writef("Terminal settings:\r\n"+
		"  encoding : %s\r\n"+
		"  size     : %d x %d\r\n"+
		"  type     : %s\r\n"+
		"  color    : %s\r\n"+
		"  lines    : %s\r\n"+
		"  echo     : %s\r\n"+
		"  telnet   : %s\r\n",
		c.Encoding, c.Width, c.Height, termTypeStr,
		colorStr, linesStr, echoStr, telnetStr)
}

func (h *Handler) terminalSetEncoding(ctx context.Context, s *Session, arg string) {
	enc, ok := term.ParseEncoding(strings.TrimSpace(arg))
	if !ok {
		_ = s.writef("Unknown encoding %q. Choices: utf8, cp437, iso88591, macroman, petscii, ascii.\r\n", arg)
		return
	}
	next := s.enc.Capabilities()
	next.Encoding = enc
	h.reconfigure(ctx, s, next)
	_ = s.writef("Encoding set to %s.\r\n", enc)
}

func (h *Handler) terminalSetSize(ctx context.Context, s *Session, arg string, isWidth bool) {
	n, err := strconv.Atoi(strings.TrimSpace(arg))
	if err != nil || n <= 0 || n > 1000 {
		_ = s.writeString("Size must be a positive integer.\r\n")
		return
	}
	next := s.enc.Capabilities()
	if isWidth {
		next.Width = n
	} else {
		next.Height = n
	}
	h.reconfigure(ctx, s, next)
	axis := "width"
	if !isWidth {
		axis = "height"
	}
	_ = s.writef("Terminal %s set to %d.\r\n", axis, n)
}

func (h *Handler) terminalSetColor(ctx context.Context, s *Session, arg string) {
	on, ok := parseOnOff(arg)
	if !ok {
		_ = s.writeString("Usage: terminal color on|off\r\n")
		return
	}
	next := s.enc.Capabilities()
	next.Color = on
	h.reconfigure(ctx, s, next)
	state := "off"
	if on {
		state = "on"
	}
	_ = s.writef("Color: %s.\r\n", state)
}

// terminalDetect re-runs terminal capability detection. For telnet
// sessions it asks the client to re-report TTYPE (NAWS is push-based
// so c.State() already reflects the latest value) and reconfigures
// the encoder + Presence + active engagement once the reply arrives.
//
// For non-telnet sessions, ANSI-based detection (Secondary DA,
// XTVERSION, CSI 18 t, cursor-position fallback for size) is planned
// in milestone M6.5; until then the command prints a "coming soon"
// placeholder rather than silently doing nothing.
func (h *Handler) terminalDetect(ctx context.Context, s *Session) {
	if !s.caps.Telnet {
		_ = s.writeString(
			"Terminal detection over plain TCP is not yet supported. " +
				"Coming soon — see docs/milestones/06.5.\r\n")
		return
	}
	_ = s.writeString("Detecting terminal...\r\n")
	s.tc.RequestTTYPE()
	// Wait for the TTYPE reply. The conn parser updates state.TermType
	// inline as bytes arrive; one second gives a comfortable margin
	// for slow links and busy CI runners while still feeling
	// interactive for the local case.
	time.Sleep(1 * time.Second)
	st := s.tc.State()

	next := s.enc.Capabilities()
	next.TermType = st.TermType
	if st.Width > 0 {
		next.Width = st.Width
	}
	if st.Height > 0 {
		next.Height = st.Height
	}
	h.reconfigure(ctx, s, next)
	_ = s.writef("Detection complete: type=%q size=%dx%d.\r\n",
		st.TermType, st.Width, st.Height)
}

func (h *Handler) terminalSetEcho(s *Session, arg string) {
	on, ok := parseOnOff(arg)
	if !ok {
		_ = s.writeString("Usage: terminal echo on|off\r\n")
		return
	}
	// SetEcho takes a "suppress" bool: true means do NOT echo. The
	// user-facing "on/off" inverts that.
	if err := s.setEcho(!on); err != nil {
		_ = s.writef("Failed to change echo state: %v\r\n", err)
		return
	}
	state := "off"
	if on {
		state = "on"
	}
	_ = s.writef("Server echo: %s.\r\n", state)
}

func (h *Handler) terminalSetLines(ctx context.Context, s *Session, arg string) {
	a := strings.ToLower(strings.TrimSpace(arg))
	var dec bool
	switch a {
	case "vt100", "dec", "on":
		dec = true
	case "native", "off":
		dec = false
	default:
		_ = s.writeString("Usage: terminal lines vt100|native\r\n")
		return
	}
	next := s.enc.Capabilities()
	next.DECLineDrawing = dec
	h.reconfigure(ctx, s, next)
	state := "native"
	if dec {
		state = "vt100"
	}
	_ = s.writef("Line drawing: %s.\r\n", state)
}

func parseOnOff(s string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "yes", "1", "true":
		return true, true
	case "off", "no", "0", "false":
		return false, true
	}
	return false, false
}

// reconfigure switches the encoder to next and persists the change to
// the account. Encoder mutation and the closing/Shift-Out byte writes
// are serialized with any concurrent broadcasts via the session's write
// mutex (see Session.reconfigureEncoder).
func (h *Handler) reconfigure(ctx context.Context, s *Session, next term.Capabilities) {
	s.reconfigureEncoder(next)
	// Propagate the new dimensions to the world-side Presence and to
	// any active engagement that implements engage.Resizer. Without
	// this, `terminal width N` would land on the session caps but
	// neither the current nor any subsequent engagement in the same
	// session would observe the change (Presence is otherwise built
	// once at attach time).
	if s.presence != nil {
		s.presence.TermWidth = next.Width
		s.presence.TermHeight = next.Height
		s.presence.TermType = next.TermType
	}
	if eng := s.Engagement(); eng != nil {
		if r, ok := eng.Handler.(engage.Resizer); ok {
			r.Resize(next.Width, next.Height)
		}
	}
	if err := h.savePrefs(ctx, s); err != nil {
		s.log.Warn("save prefs failed", "err", err)
	}
}
