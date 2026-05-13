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
}

// DefaultHandler returns a Handler with sensible defaults wired in.
func DefaultHandler(a *auth.Store, log *slog.Logger, motd string) *Handler {
	return &Handler{
		Auth:                     a,
		Logger:                   log,
		MOTD:                     motd,
		TelnetDetectTimeout:      200 * time.Millisecond,
		NegotiationSettleTimeout: 200 * time.Millisecond,
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
	}

	// --- Press-enter banner + ANSI probe ---------------------------------
	// Display a tiny banner and an ANSI Device Attributes query in one
	// write. Cooked-mode terminals line-buffer their auto-response with
	// the user's Enter keystroke, so when we read the next line both
	// arrive together — no timing race, no probe timeout to tune.
	pressEnter := []byte("WINTERMUTE\r\n\r\nPRESS ENTER TO BEGIN.\r\n")
	if _, err := s.writer().Write(pressEnter); err != nil {
		return
	}
	if _, err := s.writer().Write(term.ANSIProbe); err != nil {
		return
	}
	raw, err := s.readRawUntilNewline()
	if err != nil && len(raw) == 0 {
		s.log.Debug("pre-prompt read failed", "err", err)
		return
	}
	if term.HasDAResponse(raw) {
		hints.ANSICapable = true
	}

	defaults := term.AutoDetect(hints)

	// --- Confirmation prompt ----------------------------------------------
	// The prompt encoder is always ASCII regardless of auto-detect, because
	// the prompt is uppercase-ASCII-only by construction so that it renders
	// natively on a PETSCII client in its default (uppercase / graphics)
	// mode — no Shift Out is needed yet.
	s.enc = term.Open(term.Capabilities{Encoding: term.EncodingASCII})

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
			if err := s.writeString("PLEASE CHOOSE ONE OF U, D, M, L, P, A (OR PRESS ENTER FOR THE DEFAULT): "); err != nil {
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

	// Transition into PETSCII: emit Shift Out so the C64 switches to
	// mixed-case mode before any further text reaches it. Other encodings
	// never see this byte from the engine.
	if chosen.Encoding == term.EncodingPETSCII {
		_, _ = s.writer().Write([]byte{term.PETSCIIShiftOut})
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
