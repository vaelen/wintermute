// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package term

import (
	"bufio"
	"context"
	"net"
	"strings"
	"time"
)

// DetectHints carries information gathered during connection acceptance,
// before the user has been prompted to confirm capabilities. The fields
// are filled in by the network layer (telnet peek, IAC subnegotiation)
// and by ProbeANSI.
type DetectHints struct {
	Telnet      bool
	TermType    string
	NAWSWidth   int
	NAWSHeight  int
	ANSICapable bool
}

// AutoDetect combines the hints into a Capabilities default. Color and
// DEC line drawing fall out of the encoding defaults via ApplyEncodingDefaults.
func AutoDetect(h DetectHints) Capabilities {
	caps := Capabilities{
		Telnet:   h.Telnet,
		TermType: h.TermType,
	}

	if enc, ok := encodingFromTTYPE(h.TermType); ok {
		caps.Encoding = enc
	} else if h.ANSICapable {
		caps.Encoding = EncodingUTF8
	} else {
		caps.Encoding = EncodingASCII
	}

	if h.NAWSWidth > 0 {
		caps.Width = h.NAWSWidth
	}
	if h.NAWSHeight > 0 {
		caps.Height = h.NAWSHeight
	}

	return caps.ApplyEncodingDefaults()
}

// encodingFromTTYPE picks an encoding from a telnet TTYPE string. The
// match is loose substring on a lowercased copy. Returns false if no
// useful hint can be inferred.
func encodingFromTTYPE(ttype string) (Encoding, bool) {
	t := strings.ToLower(ttype)
	switch {
	case t == "":
		return 0, false
	case strings.Contains(t, "petscii"),
		strings.Contains(t, "c64"),
		strings.Contains(t, "c128"),
		strings.Contains(t, "cbm"):
		return EncodingPETSCII, true
	case strings.Contains(t, "cp437"),
		strings.Contains(t, "ibmpc"),
		strings.Contains(t, "ibm-pc"),
		strings.Contains(t, "syncterm"),
		strings.Contains(t, "ansi-bbs"):
		return EncodingCP437, true
	case strings.Contains(t, "ansi"),
		strings.Contains(t, "xterm"),
		strings.Contains(t, "vt100"),
		strings.Contains(t, "vt220"),
		strings.Contains(t, "screen"),
		strings.Contains(t, "tmux"),
		strings.Contains(t, "rxvt"):
		return EncodingUTF8, true
	}
	return 0, false
}

// ProbeANSI sends the Device Attributes query (ESC [ c) and waits up to
// timeout for an ESC [ ? ... c response. Returns true if such a response
// is observed. The bytes that form the response are consumed from br;
// any other peeked bytes remain in the buffer for subsequent reads.
//
// br must be the buffered reader that the session uses for all input;
// otherwise consumed response bytes would be invisible to the next read.
//
// conn is used solely to set a read deadline. The deadline is cleared
// before this function returns.
func ProbeANSI(ctx context.Context, conn net.Conn, br *bufio.Reader, timeout time.Duration) (bool, error) {
	if _, err := conn.Write([]byte("\x1B[c")); err != nil {
		return false, err
	}
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetReadDeadline(deadline); err != nil {
		return false, err
	}
	defer func() {
		_ = conn.SetReadDeadline(time.Time{})
	}()

	// Peek up to 32 bytes. Peek blocks until that many bytes arrive or the
	// underlying read errors (which it will at the deadline). We ignore the
	// error and inspect the partial buffer.
	peeked, _ := br.Peek(32)
	idx := findDAResponseEnd(peeked)
	if idx == -1 {
		return false, nil
	}
	_, _ = br.Discard(idx + 1)
	return true, nil
}

// findDAResponseEnd returns the index of the 'c' that terminates an
// ANSI Device Attributes response (ESC [ ? <digits and semicolons> c)
// within buf, or -1 if no complete response is present. Bytes between
// '?' and 'c' must be digits or ';' for the response to be recognized.
func findDAResponseEnd(buf []byte) int {
	for i := 0; i+2 < len(buf); i++ {
		if buf[i] != 0x1B || buf[i+1] != '[' || buf[i+2] != '?' {
			continue
		}
		for j := i + 3; j < len(buf); j++ {
			b := buf[j]
			if b == 'c' {
				return j
			}
			if !((b >= '0' && b <= '9') || b == ';') {
				break
			}
		}
		return -1
	}
	return -1
}
