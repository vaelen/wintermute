// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package session

import (
	"bufio"
	"time"

	"github.com/vaelen/wintermute/internal/term"
)

// detectDrainTimeout bounds how long runDetect waits for probe replies
// when it is NOT bracketed by a press-enter read. Sized for the worst
// real-world client: a 2400 bps dialup terminal (≈ 300 bytes/sec) on a
// re-issued TTYPE round-trip — see CLAUDE.md. ANSI probes alone would
// be comfortable in 250 ms locally, but the TTYPE re-request matters
// more (it's the slowest reply we wait for) and matches the prior
// M6.3.1 telnet-only `terminal detect` budget. The command is
// human-triggered so 1 s remains responsive.
const detectDrainTimeout = 1 * time.Second

// runDetect emits the "Detecting terminal type..." banner, sends every
// ANSI probe in term.AllProbes(), optionally re-requests TTYPE on
// telnet-negotiated sessions, drains incoming bytes for a short window,
// and returns hints populated from the drained replies plus the current
// telnet state.
//
// Used by the `terminal detect` command, where the drain timeout IS the
// wait window. The login flow does NOT call runDetect — it writes the
// banner+probes inline so the same I/O can also carry the "PRESS ENTER
// TO BEGIN" prompt and use the user's Enter keystroke as the wait
// signal. See Handle for the login path.
func (h *Handler) runDetect(s *Session) term.DetectHints {
	s.writeDetectionProbes()
	raw := s.drainRaw(detectDrainTimeout)
	hints := term.DetectHints{}
	term.ScanProbeReplies(raw, &hints)
	h.mergeTelnetState(s, &hints)
	return hints
}

// writeDetectionProbes emits the "Detecting terminal type..." banner,
// every byte of term.AllProbes(), and a telnet TTYPE re-request (when
// the conn has negotiated) as one atomic sequence under writeMu. The
// lock matters post-attach: without it a concurrent room broadcast
// going through writeString could land between the banner and the
// probe bytes, putting unrelated text in front of the user just before
// the detection window opens. The telnet conn's own write mutex
// protects single Write calls from byte-level interleaving but does
// not serialize across multiple Writes, which is what writeMu adds.
func (s *Session) writeDetectionProbes() {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.enc != nil {
		if out, err := s.enc.EncodeOut([]byte("Detecting terminal type...\r\n")); err == nil {
			_, _ = s.writer().Write(out)
		}
	}
	_, _ = s.writer().Write(term.AllProbes())
	if s.tc != nil && s.tc.Negotiated() {
		s.tc.RequestTTYPE()
	}
}

// mergeTelnetState folds the telnet conn's current state into hints.
// TTYPE and NAWS are pushed from the conn parser as IAC subnegotiations
// complete; whatever's there at call time is the latest value. Existing
// non-empty / non-zero hint fields are preserved so a freshly-arrived
// ANSI probe reply doesn't overwrite a previously-seen TTYPE.
func (h *Handler) mergeTelnetState(s *Session, hints *term.DetectHints) {
	if s.tc == nil {
		return
	}
	hints.Telnet = s.tc.Negotiated()
	st := s.tc.State()
	if hints.TermType == "" && st.TermType != "" {
		hints.TermType = st.TermType
	}
	if hints.NAWSWidth == 0 && st.Width > 0 {
		hints.NAWSWidth = st.Width
	}
	if hints.NAWSHeight == 0 && st.Height > 0 {
		hints.NAWSHeight = st.Height
	}
}

// drainRaw reads any bytes available from the session for up to dur,
// returning them as one slice. A read that exhausts dur returns the
// bytes accumulated so far with no error; the connection's read
// deadline is cleared before return so subsequent reads block normally.
//
// Used by runDetect to catch probe replies that arrive after the writes
// complete. Bytes are read through the same bufio.Reader the rest of
// the session uses, so any deadline-bounded leftovers stay in the
// reader for the next caller.
func (s *Session) drainRaw(dur time.Duration) []byte {
	if s.in == nil {
		s.in = bufio.NewReader(s.reader())
	}
	_ = s.conn.SetReadDeadline(time.Now().Add(dur))
	defer func() { _ = s.conn.SetReadDeadline(time.Time{}) }()
	var buf []byte
	for {
		b, err := s.in.ReadByte()
		if err != nil {
			// Both timeout and EOF are valid termination signals for
			// a best-effort drain; return whatever's been collected.
			return buf
		}
		buf = append(buf, b)
		if len(buf) > 4096 {
			return buf
		}
	}
}
