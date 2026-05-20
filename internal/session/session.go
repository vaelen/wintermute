// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package session

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync"

	"github.com/vaelen/wintermute/internal/auth"
	wtelnet "github.com/vaelen/wintermute/internal/net/telnet"
	"github.com/vaelen/wintermute/internal/term"
	"github.com/vaelen/wintermute/internal/term/readline"
	"github.com/vaelen/wintermute/internal/world"
	"github.com/vaelen/wintermute/internal/world/engage"
)

// Session represents one connected user across the engine. It owns the
// underlying connection, the telnet wrapper (always non-nil after
// handler setup), the buffered line reader, the chosen encoder, and the
// post-login account.
//
// Every connection is wrapped in a telnet.Conn at handler setup time so
// IAC parsing and IAC-doubled writes are always in effect. Whether the
// remote actually speaks telnet is reported by s.tc.Negotiated().
type Session struct {
	id      string
	conn    net.Conn
	tc      *wtelnet.Conn
	in      *bufio.Reader
	enc     *term.Encoder
	account *auth.Account
	log     *slog.Logger
	auth    *auth.Store
	world   *world.World

	// mustChangePassword is set by login() when the credential that
	// matched was a reset token rather than the stored password. When
	// true, Handle gates the post-login flow on forcePasswordChange
	// before any normal interaction starts.
	mustChangePassword bool

	// writeMu serializes writes to the connection. The world layer may
	// invoke our writeString callback from a goroutine that is not the
	// session goroutine (e.g. when another player broadcasts into the
	// room), so the encoder state must not be touched without the lock.
	writeMu sync.Mutex

	// playerID is the world object id of this session's player body, set
	// after Attach completes.
	playerID world.ObjectID

	// caps is the post-auto-detect capability snapshot for this session.
	// Used by readLineEditing to decide whether to engage the in-line
	// editor. The ANSI bit lives here rather than on the Encoder because
	// it is decided once at connection time and never reconfigured.
	caps term.Capabilities

	// history is the per-session in-memory ring buffer used by the
	// command-loop line editor. nil disables in-line history.
	history *readline.History

	// engagement is the current modal engagement, or nil. Reads from
	// other goroutines (e.g. the world's pre-delete hook) must take
	// engMu.
	engagement *engage.Engagement
	engMu      sync.Mutex

	// presence is the world-side handle for this session, set after
	// attachToWorld. Used by reconfigure() to propagate live capability
	// changes (terminal width/height) without having to plumb the
	// pointer through worldcmd. Mutated only from the session goroutine.
	presence *world.Presence
}

// newSession constructs a Session given an already-accepted connection
// and the shared dependencies. The handler is expected to attach s.tc
// before any I/O happens. historySize sizes the in-memory line-edit
// history ring; 0 disables history.
func newSession(conn net.Conn, a *auth.Store, w *world.World, log *slog.Logger, historySize int) *Session {
	return &Session{
		id:      conn.RemoteAddr().String(),
		conn:    conn,
		log:     log.With("session", conn.RemoteAddr().String()),
		auth:    a,
		world:   w,
		history: readline.NewHistory(historySize),
	}
}

// remoteIP returns the connection's remote address parsed as a netip.Addr.
// Returns the zero value when the conn.RemoteAddr() form is not a TCP
// address (test harnesses, unix-socket transports). Used by the M6.6
// hardening hooks; nil-safe handling of the zero value is required at
// every call site.
func (s *Session) remoteIP() netip.Addr {
	tcp, ok := s.conn.RemoteAddr().(*net.TCPAddr)
	if !ok {
		return netip.Addr{}
	}
	addr, ok := netip.AddrFromSlice(tcp.IP)
	if !ok {
		return netip.Addr{}
	}
	return addr.Unmap()
}

// reader returns the session's byte source — always the telnet Conn,
// which strips IAC sequences inline.
func (s *Session) reader() io.Reader { return s.tc }

// writer returns the byte sink — always the telnet Conn, which doubles
// IAC bytes in the outbound data.
func (s *Session) writer() io.Writer { return s.tc }

// setEcho toggles whether the server echoes received data bytes back to
// the client. suppress=true suppresses echo (password entry);
// suppress=false enables echo. Works independently of whether the
// remote has been observed speaking telnet.
func (s *Session) setEcho(suppress bool) error {
	return s.tc.SetEcho(suppress)
}

// echoOn reports whether the server is currently echoing.
func (s *Session) echoOn() bool {
	return s.tc.EchoEnabled()
}

// writeRaw writes bytes directly to the connection without going through
// the encoder. Used during pre-prompt phases when no encoder exists yet.
func (s *Session) writeRaw(b []byte) error {
	_, err := s.writer().Write(b)
	return err
}

// writeString encodes s through the current Encoder and writes the result.
// Requires that prepareInput has been called and the Encoder is set.
//
// Safe to call from multiple goroutines; calls are serialized by writeMu
// so the encoder's internal state and the connection write are atomic.
func (s *Session) writeString(text string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.enc == nil {
		return errors.New("session: encoder not initialized")
	}
	out, err := s.enc.EncodeOut([]byte(text))
	if err != nil {
		return err
	}
	_, err = s.writer().Write(out)
	return err
}

// reconfigureEncoder switches the encoder to next while holding writeMu,
// so the encoder mutation and any closing/Shift-Out byte writes can't
// interleave with a concurrent broadcast going through writeString.
// Returns the previous capabilities so the caller can decide what to log.
func (s *Session) reconfigureEncoder(next term.Capabilities) term.Capabilities {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	prev := s.enc.Capabilities()
	closing := s.enc.Reconfigure(next)
	if len(closing) > 0 {
		_, _ = s.writer().Write(closing)
	}
	if next.Encoding == term.EncodingPETSCII && prev.Encoding != term.EncodingPETSCII {
		_, _ = s.writer().Write([]byte{term.PETSCIIShiftOut})
	}
	return prev
}

// writef is a Printf-style helper around writeString.
func (s *Session) writef(format string, args ...any) error {
	return s.writeString(fmt.Sprintf(format, args...))
}

// readLine reads one line of input, handling byte-at-a-time char-mode
// input including in-line BS/DEL editing. Returns when CR, LF, or CRLF
// arrives. The accumulated bytes are decoded through the current
// Encoder and ANSI CSI sequences are stripped before returning.
//
// The CSI strip protects against terminal auto-responses — Device
// Attributes, cursor position reports — that some terminals
// line-buffer alongside the user's typed input. Without it the first
// line on connect can be `\x1B[?1;2cu` instead of just `u`.
func (s *Session) readLine() (string, error) {
	if s.in == nil {
		s.in = bufio.NewReader(s.reader())
	}
	var line []byte
	for {
		b, err := s.in.ReadByte()
		if err != nil {
			if len(line) > 0 {
				return s.finishLine(line), nil
			}
			return "", err
		}
		switch b {
		case '\n':
			return s.finishLine(line), nil
		case '\r':
			if next, _ := s.in.Peek(1); len(next) > 0 && next[0] == '\n' {
				_, _ = s.in.ReadByte()
			}
			return s.finishLine(line), nil
		case 0x08, 0x7F: // backspace / delete
			if len(line) > 0 {
				line = line[:len(line)-1]
			}
		default:
			if b < 0x20 {
				continue // ignore other control chars in line input
			}
			line = append(line, b)
		}
	}
}

// readLineEditing reads one edited line. On an ANSI-capable terminal
// (and only when the encoding can carry CSI bytes and server-side echo
// is on) this engages the in-line editor; otherwise it falls back to
// the plain readLine loop so password entry, paste mode, PETSCII
// sessions, and pre-login phases keep their existing byte-for-byte
// semantics. ErrInterrupt is translated into an empty line; the caller
// is expected to print a fresh prompt and continue.
//
// While the editor is active the telnet wrapper's per-byte auto-echo
// is suppressed: the editor's own redraw is the *only* source of
// visible output, so leaving auto-echo on would double every keystroke
// and desync the editor's cursor model from the screen (the "TTTTT..."
// pattern). Echo state is restored to whatever it was before the call
// the moment ReadLine returns.
func (s *Session) readLineEditing() (string, error) {
	if !s.shouldUseEditor() {
		return s.readLine()
	}
	if s.in == nil {
		s.in = bufio.NewReader(s.reader())
	}
	// shouldUseEditor already required echoOn() == true, so prevEcho
	// is unconditionally true here. setEcho(true) suppresses telnet's
	// per-byte echo for the duration of the editor frame; the deferred
	// restore puts us back into server-echo mode for the command loop
	// outside the editor.
	prevEcho := s.echoOn()
	_ = s.setEcho(true)
	defer func() { _ = s.setEcho(!prevEcho) }()
	// Pass &writeMu so the editor's per-frame emits are serialized
	// with world broadcasts going through writeString. Without it
	// the two goroutines would race on enc.inGraphics and interleave
	// bytes on the wire.
	line, err := readline.ReadLine(s.in, s.writer(), s.enc, s.history, &s.writeMu)
	if err != nil {
		if errors.Is(err, readline.ErrInterrupt) {
			return "", nil
		}
		return line, err
	}
	return line, nil
}

// shouldUseEditor reports whether the in-line editor is appropriate for
// the current session state. The editor requires an ANSI terminal, a
// configured encoder, server-side echo on (so we know our writes will
// actually appear on screen), and a non-PETSCII encoding (PETSCII has
// its own cursor movement bytes incompatible with ANSI CSI).
func (s *Session) shouldUseEditor() bool {
	if s.enc == nil {
		return false
	}
	if !s.caps.ANSI {
		return false
	}
	if !s.echoOn() {
		return false
	}
	if s.enc.Capabilities().Encoding == term.EncodingPETSCII {
		return false
	}
	return true
}

func (s *Session) finishLine(line []byte) string {
	decoded := string(line)
	if s.enc != nil {
		if out, err := s.enc.DecodeIn(line); err == nil {
			decoded = string(out)
		}
	}
	return term.StripCSI(decoded)
}

// readRawUntilNewline reads bytes from the session input until either
// '\r' or '\n' arrives, then returns the bytes preceding the terminator.
// CRLF is consumed atomically. No encoder decode or CSI stripping is
// applied — the caller gets the raw byte stream so it can scan for
// terminal auto-responses.
//
// Used during the pre-prompt capability detection phase, where the user's
// Enter keystroke flushes their terminal's stdin buffer (potentially
// including any auto-responses like the ANSI Device Attributes reply).
func (s *Session) readRawUntilNewline() ([]byte, error) {
	if s.in == nil {
		s.in = bufio.NewReader(s.reader())
	}
	var buf []byte
	for {
		b, err := s.in.ReadByte()
		if err != nil {
			return buf, err
		}
		if b == '\n' {
			return buf, nil
		}
		if b == '\r' {
			// Eat an optional immediately-following '\n' so the CRLF pair
			// counts as one line terminator.
			if next, _ := s.in.Peek(1); len(next) > 0 && next[0] == '\n' {
				_, _ = s.in.ReadByte()
			}
			return buf, nil
		}
		if b == 0x08 || b == 0x7F {
			if len(buf) > 0 {
				buf = buf[:len(buf)-1]
			}
			continue
		}
		buf = append(buf, b)
		if len(buf) > 4096 {
			return buf, nil // defensive cap
		}
	}
}

// finalize flushes any encoder state. Always safe to call.
func (s *Session) finalize() {
	if s.enc != nil {
		if tail := s.enc.FinalizeOut(); len(tail) > 0 {
			_, _ = s.writer().Write(tail)
		}
	}
}

// close shuts the session down, flushing any encoder state.
func (s *Session) close() {
	s.finalize()
	_ = s.conn.Close()
}

// Engagement returns the session's current engagement, or nil. Safe to
// call from any goroutine.
func (s *Session) Engagement() *engage.Engagement {
	s.engMu.Lock()
	defer s.engMu.Unlock()
	return s.engagement
}

// SetEngagement sets the session's current engagement. Called by the
// engage helpers; not used directly by handlers.
func (s *Session) SetEngagement(e *engage.Engagement) {
	s.engMu.Lock()
	s.engagement = e
	s.engMu.Unlock()
}
