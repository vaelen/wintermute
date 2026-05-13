// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package session

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"time"

	"github.com/vaelen/wintermute/internal/auth"
	wtelnet "github.com/vaelen/wintermute/internal/net/telnet"
	"github.com/vaelen/wintermute/internal/term"
)

// Handler holds the dependencies a connection handler needs. One Handler
// is created at engine startup and reused for every accepted connection.
type Handler struct {
	Auth   *auth.Store
	Logger *slog.Logger
	MOTD   string

	// Tuning knobs (intentionally exported so tests / config can lower them).
	TelnetDetectTimeout      time.Duration
	NegotiationSettleTimeout time.Duration
	ANSIProbeTimeout         time.Duration
}

// DefaultHandler returns a Handler with sensible defaults wired in.
func DefaultHandler(a *auth.Store, log *slog.Logger, motd string) *Handler {
	return &Handler{
		Auth:                     a,
		Logger:                   log,
		MOTD:                     motd,
		TelnetDetectTimeout:      200 * time.Millisecond,
		NegotiationSettleTimeout: 200 * time.Millisecond,
		ANSIProbeTimeout:         300 * time.Millisecond,
	}
}

// Handle drives a single accepted connection through the full lifecycle:
// telnet detection, optional IAC negotiation, ANSI probe, capability
// prompt, login, MOTD, then the placeholder command loop.
func (h *Handler) Handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	s := newSession(conn, br, h.Auth, h.Logger)
	defer s.finalize()

	// --- Capability detection ---------------------------------------------
	isTelnet, err := wtelnet.Detect(ctx, conn, br, h.TelnetDetectTimeout)
	if err != nil {
		s.log.Debug("telnet detect failed", "err", err)
	}
	hints := term.DetectHints{Telnet: isTelnet}

	if isTelnet {
		tc, terr := wtelnet.Wrap(conn, br, wtelnet.DefaultOptions())
		if terr != nil {
			s.log.Warn("telnet wrap failed", "err", terr)
			return
		}
		s.tc = tc
		// Give the client a moment to send its negotiation responses, then
		// drain whatever IAC bytes are already in the buffer.
		time.Sleep(h.NegotiationSettleTimeout)
		if err := tc.ProcessBuffered(); err != nil {
			s.log.Debug("process buffered IAC failed", "err", err)
		}
		st := tc.State()
		hints.TermType = st.TermType
		hints.NAWSWidth = st.Width
		hints.NAWSHeight = st.Height
	} else {
		// Non-telnet path: probe for ANSI capability. We can only do this
		// safely when there are no concurrent IAC bytes mixed into the stream.
		ansi, err := term.ProbeANSI(ctx, conn, br, h.ANSIProbeTimeout)
		if err != nil {
			s.log.Debug("ANSI probe failed", "err", err)
		}
		hints.ANSICapable = ansi
	}

	defaults := term.AutoDetect(hints)

	// --- Send Shift Out unconditionally -----------------------------------
	// PETSCII clients enter mixed-case mode; modern terminals ignore the byte
	// (or treat it as the moribund NRCS shift, which is a no-op in practice).
	if err := s.writeRaw([]byte{term.PETSCIIShiftOut}); err != nil {
		s.log.Debug("write shift out failed", "err", err)
		return
	}

	// --- Confirmation prompt ----------------------------------------------
	// Use a temporary encoder seeded with the auto-detected capabilities so
	// the prompt itself is rendered correctly even before the user confirms.
	tempEnc := term.Open(defaults)
	s.enc = tempEnc

	if err := s.writeString(term.RenderPrompt(defaults.Encoding)); err != nil {
		s.log.Debug("write prompt failed", "err", err)
		return
	}

	chosen := defaults
	for {
		line, err := s.readLine()
		if err != nil && line == "" {
			s.log.Debug("prompt read failed", "err", err)
			return
		}
		enc, ok := term.ParsePromptResponse(line, defaults.Encoding)
		if !ok {
			if err := s.writeString("Please choose one of U, D, M, L, P, A (or press Enter for the default): "); err != nil {
				return
			}
			continue
		}
		chosen.Encoding = enc
		break
	}
	// Re-derive width / color / DEC defaults from the chosen encoding,
	// preserving any NAWS-supplied size.
	chosen = applyChosen(chosen)
	closing := s.enc.Reconfigure(chosen)
	if len(closing) > 0 {
		_, _ = s.writer().Write(closing)
	}

	// Send a blank line to separate the prompt from the login UI.
	if err := s.writeString("\r\n"); err != nil {
		return
	}

	// --- Login ------------------------------------------------------------
	if err := h.login(ctx, s); err != nil {
		if !errors.Is(err, io.EOF) {
			s.log.Info("login failed", "err", err)
		}
		return
	}

	// If the user has saved preferences from a prior session that differ
	// from what they just picked, apply those silently before the MOTD.
	h.applyAccountPrefsIfDiffer(s)

	// Persist whatever Capabilities we end up with as this account's
	// terminal preferences for next time.
	if err := h.savePrefs(ctx, s); err != nil {
		s.log.Warn("save prefs failed", "err", err)
	}

	// --- MOTD -------------------------------------------------------------
	if h.MOTD != "" {
		_ = s.writeString(h.MOTD + "\r\n")
	}

	// --- Placeholder command loop ----------------------------------------
	h.commandLoop(ctx, s)
}

// applyChosen overlays encoding-driven defaults onto cap without trampling
// a NAWS-supplied size.
func applyChosen(c term.Capabilities) term.Capabilities {
	width, height := c.Width, c.Height
	c.Width = 0
	c.Height = 0
	c = c.ApplyEncodingDefaults()
	if width > 0 {
		c.Width = width
	}
	if height > 0 {
		c.Height = height
	}
	return c
}
