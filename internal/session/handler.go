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
	"net/netip"
	"strings"
	"time"

	"github.com/vaelen/wintermute/internal/auth"
	wtelnet "github.com/vaelen/wintermute/internal/net/telnet"
	"github.com/vaelen/wintermute/internal/term"
	"github.com/vaelen/wintermute/internal/world"
	worldcmd "github.com/vaelen/wintermute/internal/world/cmd"
	"github.com/vaelen/wintermute/internal/world/engage"
)

// LoginGuard is the M6.6 hook the login flow consults. nil-safe: every
// session created without one falls back to the pre-M6.6 behaviour.
//
// CheckUsername runs after the username prompt; returning a non-nil
// error (specifically auth.ErrUsernameDisallowed) causes login() to
// drop the connection silently via ErrSilentDrop. RecordFailedPassword
// is called on every auth.ErrInvalidCredentials and returns true when
// the per-IP threshold has just been crossed — again, login() drops
// silently in that case.
type LoginGuard interface {
	CheckUsername(ctx context.Context, ip netip.Addr, name string) error
	RecordFailedPassword(ctx context.Context, ip netip.Addr) bool
}

// ErrSilentDrop is returned by login() when M6.6 hardening decides
// the connection should be closed without rendering any further output
// (disallowed username, failed-password threshold crossed). Handle
// recognises it and returns without logging at Info — the security
// service has already emitted the structured "ip flagged" line.
var ErrSilentDrop = errors.New("session: silent drop")

// Handler holds the dependencies a connection handler needs. One Handler
// is created at engine startup and reused for every accepted connection.
type Handler struct {
	Auth   *auth.Store
	World  *world.World
	NPC    worldcmd.NPCReloader
	Admin  *worldcmd.AdminBackend
	Logger *slog.Logger
	MOTD   string

	// EngageRegistry is the process-wide engagement registry. nil-safe
	// for tests; when nil, modal dispatch falls through to the world parser.
	EngageRegistry *engage.Registry

	// EngageBackend is the per-session worldcmd-level engagement plumbing.
	// May be nil; the world cmd handler is nil-safe (T9).
	EngageBackend *worldcmd.EngageBackend

	// Tuning knobs (intentionally exported so tests / config can lower them).
	TelnetDetectTimeout      time.Duration
	NegotiationSettleTimeout time.Duration

	// HistorySize is the per-session line-edit history capacity. 0
	// disables in-line history. Defaults to 100 via DefaultHandler.
	HistorySize int

	// PostMOTD, if set, is invoked once per session after the MOTD is
	// written and before the command loop begins. Its return value is
	// written to the session verbatim. Used in M6 to print the
	// unread-mail count.
	PostMOTD func(ctx context.Context, acc *auth.Account) string

	// LoginGuard, if non-nil, gates the username and failed-password
	// paths against the M6.6 security service. nil disables the
	// hardening hooks (tests, pre-M6.6 milestones).
	LoginGuard LoginGuard
}

// DefaultHandler returns a Handler with sensible defaults wired in.
func DefaultHandler(a *auth.Store, w *world.World, npcReg worldcmd.NPCReloader, log *slog.Logger, motd string) *Handler {
	return &Handler{
		Auth:                     a,
		World:                    w,
		NPC:                      npcReg,
		Logger:                   log,
		MOTD:                     motd,
		TelnetDetectTimeout:      200 * time.Millisecond,
		NegotiationSettleTimeout: 200 * time.Millisecond,
		HistorySize:              100,
	}
}

// Handle drives a single accepted connection through the full lifecycle:
// always-on telnet wrap + initial IAC offers, press-enter banner + ANSI
// probe, capability prompt, login, MOTD, then the placeholder command
// loop.
//
// The connection is always wrapped in a telnet.Conn so any IAC bytes a
// client sends are parsed (and any IAC bytes we emit are doubled).
// telnet vs. raw is determined *after* we observe the client's
// response: tc.Negotiated() reports whether any IAC came back.
func (h *Handler) Handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	s := newSession(conn, h.Auth, h.World, h.Logger, h.HistorySize)
	defer s.finalize()

	br := bufio.NewReader(conn)
	tc, err := wtelnet.Wrap(conn, br, wtelnet.DefaultOptions())
	if err != nil {
		s.log.Warn("telnet wrap failed", "err", err)
		return
	}
	s.tc = tc

	// Give telnet-speaking clients a moment to respond to our initial
	// offers, then drain any IAC sequences that are already buffered. By
	// the time this returns, tc.Negotiated() reflects whether we're talking
	// to a real telnet client or something simpler.
	time.Sleep(h.NegotiationSettleTimeout)
	if err := tc.ProcessBuffered(); err != nil {
		s.log.Debug("process buffered IAC failed", "err", err)
	}

	hints := term.DetectHints{}

	// --- Press-enter banner + ANSI probes --------------------------------
	// Write the welcome banner, a "Detecting terminal type..." line that
	// gives obvious context for any probe garble on dumb terminals,
	// every detection probe, and finally the press-enter prompt — all
	// in one write so the bytes travel together. Cooked-mode terminals
	// line-buffer the probe replies with the user's Enter keystroke, so
	// the readRawUntilNewline below catches both with no timing race.
	banner := []byte("WINTERMUTE\r\n\r\nDetecting terminal type...\r\n")
	if _, err := s.writer().Write(banner); err != nil {
		return
	}
	if _, err := s.writer().Write(term.AllProbes()); err != nil {
		return
	}
	if _, err := s.writer().Write([]byte("PRESS ENTER TO BEGIN.\r\n")); err != nil {
		return
	}
	raw, err := s.readRawUntilNewline()
	if err != nil && len(raw) == 0 {
		s.log.Debug("pre-prompt read failed", "err", err)
		return
	}
	term.ScanProbeReplies(raw, &hints)

	// Telnet status / TTYPE / NAWS hints reflect whatever the conn has
	// learned by now (initial offers + any negotiation that completed
	// before / during the press-enter read). TTYPE / NAWS take
	// precedence over the ANSI-probe equivalents WHEN they have been
	// negotiated — when the conn has no value (the common non-telnet
	// case, or a telnet client that declined the option), the scan
	// result from a Secondary DA / CSI 18 t reply is left intact.
	hints.Telnet = tc.Negotiated()
	st := tc.State()
	if st.TermType != "" {
		hints.TermType = st.TermType
	}
	if st.Width > 0 {
		hints.NAWSWidth = st.Width
	}
	if st.Height > 0 {
		hints.NAWSHeight = st.Height
	}

	// For non-telnet sessions, the engine cannot tell whether the user's
	// terminal is doing local echo. Ask them. Default is No, which matches
	// the most common case (a cooked-mode `nc localhost 2323` with local
	// echo already on at the terminal). Answering Yes makes the server
	// take over echo — useful when the user has explicitly disabled local
	// echo on their end. Telnet sessions skip this step entirely because
	// negotiation already established server-side echo.
	if !hints.Telnet {
		s.enc = term.Open(term.Capabilities{Encoding: term.EncodingASCII})
		if err := s.writeString("ENABLE ECHO (Y/[N]): "); err != nil {
			return
		}
		resp, err := s.readLine()
		if err != nil && resp == "" {
			s.log.Debug("local echo prompt read failed", "err", err)
			return
		}
		resp = strings.TrimSpace(resp)
		if len(resp) > 0 && (resp[0] == 'Y' || resp[0] == 'y') {
			// "Yes, my terminal has local echo off — please echo for me."
			_ = s.setEcho(false)
			_ = s.writeString("\r\n")
		}
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
	// Snapshot the post-detection capabilities for use by the line
	// editor. ANSI is decided once, at connection time; later terminal
	// reconfigures (encoding / width / color) don't touch it.
	s.caps = chosen

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
		switch {
		case errors.Is(err, ErrSilentDrop):
			// The security service has already logged the flag/deny line.
			// Nothing more to say.
		case errors.Is(err, io.EOF):
			// Disconnect during login — uninteresting.
		default:
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

	// --- Forced password change (post-reset redemption) -------------------
	// Runs before MOTD so a redeemed reset cannot bypass the prompt by
	// disconnecting and reconnecting (the reset row is still intact and
	// the same flow re-fires). Prefs and saved-prefs application stay
	// above this so the prompts render in the user's chosen encoding.
	if s.mustChangePassword {
		if err := h.forcePasswordChange(ctx, s); err != nil {
			if !errors.Is(err, io.EOF) {
				s.log.Info("forced password change failed", "err", err)
			}
			return
		}
	}

	// --- MOTD -------------------------------------------------------------
	if h.MOTD != "" {
		_ = s.writeString(h.MOTD + "\r\n")
	}

	// --- Post-MOTD hook (M6: unread-mail count) ---------------------------
	if h.PostMOTD != nil && s.account != nil {
		if msg := h.PostMOTD(ctx, s.account); msg != "" {
			_ = s.writeString(msg)
		}
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
