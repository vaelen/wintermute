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

	"github.com/vaelen/wintermute/internal/auth"
	wtelnet "github.com/vaelen/wintermute/internal/net/telnet"
	"github.com/vaelen/wintermute/internal/term"
)

// Session represents one connected user across the engine. It owns the
// underlying connection, the buffered input reader, the telnet wrapper
// (if any), the chosen encoder, and the post-login account.
type Session struct {
	id      string
	conn    net.Conn
	tc      *wtelnet.Conn   // nil if not telnet
	in      *bufio.Reader   // line-oriented input; sourced from tc or raw
	rawBR   *bufio.Reader   // the original buffered reader, used during detection
	enc     *term.Encoder
	account *auth.Account
	log     *slog.Logger
	auth    *auth.Store
}

// newSession constructs a Session given an already-accepted connection
// and the shared dependencies. The encoder and account are nil until the
// pre-login prompt and login flow have completed.
func newSession(conn net.Conn, br *bufio.Reader, a *auth.Store, log *slog.Logger) *Session {
	return &Session{
		id:    conn.RemoteAddr().String(),
		conn:  conn,
		rawBR: br,
		log:   log.With("session", conn.RemoteAddr().String()),
		auth:  a,
	}
}

// reader returns the byte source for the session: either the telnet Conn
// (which strips IAC inline) or the raw bufio.Reader.
func (s *Session) reader() io.Reader {
	if s.tc != nil {
		return s.tc
	}
	return s.rawBR
}

// writer returns the byte sink. The telnet path doubles IAC bytes
// automatically.
func (s *Session) writer() io.Writer {
	if s.tc != nil {
		return s.tc
	}
	return s.conn
}

// setEcho asks the client to suppress its local echo when suppress is true.
// On non-telnet sessions this is a no-op (the user will see their typed
// password on screen).
func (s *Session) setEcho(suppress bool) error {
	if s.tc != nil {
		return s.tc.SetEcho(suppress)
	}
	return nil
}

// writeRaw writes bytes directly to the connection without going through
// the encoder. Used during pre-prompt phases when no encoder exists yet.
func (s *Session) writeRaw(b []byte) error {
	_, err := s.writer().Write(b)
	return err
}

// writeString encodes s through the current Encoder and writes the result.
// Requires that prepareInput has been called and the Encoder is set.
func (s *Session) writeString(text string) error {
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
