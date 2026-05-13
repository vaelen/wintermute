// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package session

import (
	"context"
	"strconv"
	"strings"

	"github.com/vaelen/wintermute/internal/term"
)

// commandLoop runs the placeholder post-login command loop. Recognized
// commands are minimal in milestone 1: terminal, help, who (stub), look
// (void), and quit. Anything else echoes the "void" placeholder.
func (h *Handler) commandLoop(ctx context.Context, s *Session) {
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
		cmd, rest := splitCmd(line)
		switch strings.ToLower(cmd) {
		case "quit", "logout", "disconnect":
			_ = s.writeString("Goodbye.\r\n")
			return
		case "help", "?":
			h.cmdHelp(s)
		case "terminal":
			h.cmdTerminal(ctx, s, rest)
		case "look":
			_ = s.writeString("You are in the void. Rooms arrive in milestone 2.\r\n")
		default:
			_ = s.writef("Unknown command: %q (try 'help'). The world is empty until milestone 2.\r\n", cmd)
		}
	}
}

func splitCmd(line string) (cmd, rest string) {
	i := strings.IndexAny(line, " \t")
	if i < 0 {
		return line, ""
	}
	return line[:i], strings.TrimSpace(line[i+1:])
}

func (h *Handler) cmdHelp(s *Session) {
	help := strings.Join([]string{
		"Commands available in milestone 1:",
		"  help               — show this list",
		"  terminal           — show current terminal settings",
		"  terminal encoding <utf8|cp437|iso88591|macroman|petscii|ascii>",
		"  terminal width <n>",
		"  terminal height <n>",
		"  terminal color on|off",
		"  terminal lines vt100|native",
		"  look               — placeholder (real rooms arrive in milestone 2)",
		"  quit               — disconnect",
		"",
	}, "\r\n")
	_ = s.writeString(help)
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
	_ = s.writef("Terminal settings:\r\n"+
		"  encoding : %s\r\n"+
		"  size     : %d x %d\r\n"+
		"  color    : %s\r\n"+
		"  lines    : %s\r\n"+
		"  telnet   : %s\r\n",
		c.Encoding, c.Width, c.Height, colorStr, linesStr, telnetStr)
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
// emitted before the switch.
func (h *Handler) reconfigure(ctx context.Context, s *Session, next term.Capabilities) {
	closing := s.enc.Reconfigure(next)
	if len(closing) > 0 {
		_, _ = s.writer().Write(closing)
	}
	if err := h.savePrefs(ctx, s); err != nil {
		s.log.Warn("save prefs failed", "err", err)
	}
}
