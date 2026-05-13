// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package session

import (
	"context"
	"strconv"
	"strings"

	"github.com/vaelen/wintermute/internal/term"
	"github.com/vaelen/wintermute/internal/world"
	worldcmd "github.com/vaelen/wintermute/internal/world/cmd"
)

// commandLoop runs the post-login input loop. The line is first offered to
// the world command handler (look/move/say/...); anything the world
// doesn't recognize is dispatched here as a session-level command
// (terminal, motd, help).
func (h *Handler) commandLoop(ctx context.Context, s *Session) {
	wh := h.attachToWorld(s)
	if wh == nil {
		return
	}
	defer h.detachFromWorld(s)

	wh.ShowRoom()

	for {
		if err := s.writeString("> "); err != nil {
			return
		}
		line, err := s.readLine()
		if err != nil && line == "" {
			return
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		switch wh.Dispatch(line) {
		case worldcmd.OutcomeQuit:
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
func (h *Handler) attachToWorld(s *Session) *worldcmd.Handler {
	if h.World == nil || s.account == nil {
		_ = s.writeString("The world is unavailable. Please try again later.\r\n")
		return nil
	}
	playerID, err := h.World.PlayerByAccount(s.account.ID)
	if err != nil {
		// Lazy bootstrap: account exists but body doesn't (e.g. account
		// predates this migration). Create one now.
		playerID, err = h.World.CreatePlayer(context.Background(), s.account)
		if err != nil {
			s.log.Error("create player object", "err", err)
			_ = s.writeString("The world refuses to acknowledge you. (Could not create your body.)\r\n")
			return nil
		}
	}
	s.playerID = playerID

	pres := &world.Presence{
		PlayerID: playerID,
		Account:  s.account,
		Write:    s.writeString,
		Log:      s.log,
	}
	if _, err := h.World.Attach(pres); err != nil {
		if err == world.ErrAlreadyAttached {
			// A stale session is still attached. Force a detach and retry.
			h.World.Detach(playerID)
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
	return &worldcmd.Handler{World: h.World, Presence: pres}
}

func (h *Handler) detachFromWorld(s *Session) {
	if h.World == nil || s.playerID == 0 {
		return
	}
	h.World.Detach(s.playerID)
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
	_ = s.writef("Terminal settings:\r\n"+
		"  encoding : %s\r\n"+
		"  size     : %d x %d\r\n"+
		"  color    : %s\r\n"+
		"  lines    : %s\r\n"+
		"  echo     : %s\r\n"+
		"  telnet   : %s\r\n",
		c.Encoding, c.Width, c.Height, colorStr, linesStr, echoStr, telnetStr)
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
// the account. Closing graphics-mode bytes from the old encoder are
// emitted before the switch; if the new encoding is PETSCII and the old
// one wasn't, a Shift Out is emitted afterward so the C64 enters
// mixed-case mode.
func (h *Handler) reconfigure(ctx context.Context, s *Session, next term.Capabilities) {
	prev := s.enc.Capabilities()
	closing := s.enc.Reconfigure(next)
	if len(closing) > 0 {
		_, _ = s.writer().Write(closing)
	}
	if next.Encoding == term.EncodingPETSCII && prev.Encoding != term.EncodingPETSCII {
		_, _ = s.writer().Write([]byte{term.PETSCIIShiftOut})
	}
	if err := h.savePrefs(ctx, s); err != nil {
		s.log.Warn("save prefs failed", "err", err)
	}
}
