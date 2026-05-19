// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package session

import (
	"bufio"
	"time"

	"github.com/vaelen/wintermute/internal/term"
)

// detectDrainTimeout bounds how long runDetect waits for probe replies
// when it is NOT bracketed by a press-enter read. The value balances
// "long enough for a slow link to respond" against "short enough not to
// feel laggy" in the command-loop `terminal detect` path.
const detectDrainTimeout = 250 * time.Millisecond

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
	_ = s.writeString("Detecting terminal type...\r\n")
	_, _ = s.writer().Write(term.AllProbes())
	if s.tc != nil && s.tc.Negotiated() {
		s.tc.RequestTTYPE()
	}
	raw := s.drainRaw(detectDrainTimeout)
	hints := term.DetectHints{}
	term.ScanProbeReplies(raw, &hints)
	h.mergeTelnetState(s, &hints)
	return hints
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
