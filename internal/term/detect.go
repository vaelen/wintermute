// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package term

import "strings"

// DetectHints carries information gathered during connection acceptance,
// before the user has been prompted to confirm capabilities. The fields
// are filled in by the network layer (telnet peek, IAC subnegotiation)
// and by scanning the press-enter response for terminal auto-responses
// (ANSI Device Attributes, primarily).
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
		ANSI:     h.ANSICapable,
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

// ANSIProbe is the byte sequence the engine sends to elicit a Device
// Attributes response from an ANSI-capable terminal. The terminal
// auto-replies with ESC [ ? <digits and semicolons> c if it understands
// the query. Non-ANSI terminals send nothing.
//
// The engine sends ANSIProbe alongside the "press enter to begin"
// prompt: cooked-mode terminals line-buffer the response together with
// the user's Enter keystroke, so by the time the server reads the
// resulting line the response (if any) is right there at the start.
var ANSIProbe = []byte("\x1B[c")

// HasDAResponse reports whether buf contains a complete ANSI Device
// Attributes response.
func HasDAResponse(buf []byte) bool {
	return findDAResponseEnd(buf) != -1
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
		// This ESC[? prefix didn't lead to a valid response; keep scanning
		// in case a real one sits further along in the buffer.
	}
	return -1
}
