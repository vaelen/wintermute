// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package term

import "strings"

// Encoding identifies one of the six wire encodings the engine supports.
type Encoding int

// Supported encodings.
const (
	EncodingUTF8 Encoding = iota
	EncodingCP437
	EncodingISO88591
	EncodingMacRoman
	EncodingPETSCII
	EncodingASCII
)

// String returns the canonical short name used in config / database / wire
// protocol contexts. Stable; do not change values.
func (e Encoding) String() string {
	switch e {
	case EncodingUTF8:
		return "utf8"
	case EncodingCP437:
		return "cp437"
	case EncodingISO88591:
		return "iso88591"
	case EncodingMacRoman:
		return "macroman"
	case EncodingPETSCII:
		return "petscii"
	case EncodingASCII:
		return "ascii"
	}
	return "utf8"
}

// ParseEncoding maps a short name (case-insensitive) to an Encoding.
// Returns false if the name is not recognized.
func ParseEncoding(s string) (Encoding, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "utf8", "utf-8", "unicode":
		return EncodingUTF8, true
	case "cp437", "dos":
		return EncodingCP437, true
	case "iso88591", "iso-8859-1", "latin1", "latin-1":
		return EncodingISO88591, true
	case "macroman", "mac-roman", "mac":
		return EncodingMacRoman, true
	case "petscii", "c64":
		return EncodingPETSCII, true
	case "ascii":
		return EncodingASCII, true
	}
	return 0, false
}

// Capabilities is the per-session set of independent terminal-capability
// axes. Each axis can be set or changed independently of the others.
type Capabilities struct {
	// Telnet indicates whether IAC option negotiation is active.
	Telnet bool
	// Encoding is the wire character encoding.
	Encoding Encoding
	// Width / Height are the screen dimensions in cells.
	Width, Height int
	// Color enables color output (ANSI SGR or encoding-native equivalents).
	Color bool
	// DECLineDrawing enables substitution of UTF-8 single-line box-drawing
	// characters with VT100 DEC Special Graphics. Independent of encoding.
	DECLineDrawing bool
	// TermType is the raw TTYPE value reported by the client, if any.
	// Informational; the engine does not branch on it past auto-detect.
	TermType string
}

// DefaultCapabilities returns a conservative capability set suitable as the
// initial value before any detection has happened.
func DefaultCapabilities() Capabilities {
	return Capabilities{
		Telnet:         false,
		Encoding:       EncodingASCII,
		Width:          80,
		Height:         24,
		Color:          false,
		DECLineDrawing: false,
	}
}

// ApplyEncodingDefaults overlays the encoding-driven defaults for Width,
// Color, and DECLineDrawing on top of c. Existing non-zero / non-false
// fields are preserved. Returns the adjusted capabilities.
//
// Encoding-driven defaults:
//   - Width:          80, except PETSCII / ASCII default to 40.
//   - Color:          on for every encoding except ASCII.
//   - DECLineDrawing: on for ISO-8859-1 and MacRoman; off otherwise.
func (c Capabilities) ApplyEncodingDefaults() Capabilities {
	if c.Width == 0 {
		switch c.Encoding {
		case EncodingPETSCII, EncodingASCII:
			c.Width = 40
		default:
			c.Width = 80
		}
	}
	if c.Height == 0 {
		c.Height = 24
	}
	// Color and DECLineDrawing default by encoding when the user has
	// not explicitly chosen. We use a separate Resolve helper to layer
	// these in the right order from saved prefs and live auto-detect.
	c.Color = encodingDefaultColor(c.Encoding)
	c.DECLineDrawing = encodingDefaultDEC(c.Encoding)
	return c
}

func encodingDefaultColor(e Encoding) bool {
	return e != EncodingASCII
}

func encodingDefaultDEC(e Encoding) bool {
	return e == EncodingISO88591 || e == EncodingMacRoman
}
