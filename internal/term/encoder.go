// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package term

import (
	"bytes"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// Encoder converts engine-internal UTF-8 + ANSI bytes into the wire
// encoding chosen for the session, and decodes incoming wire bytes back
// to UTF-8.
//
// Encoders are stateful: in particular, they remember whether the wire
// is currently in DEC Special Graphics mode so adjacent line-drawing
// characters share one ESC ( 0 / ESC ( B bracket pair rather than
// receiving one each.
type Encoder struct {
	caps       Capabilities
	inGraphics bool
}

// Open returns an Encoder configured for the given Capabilities.
func Open(caps Capabilities) *Encoder {
	return &Encoder{caps: caps}
}

// Capabilities returns the current Capabilities the encoder is using.
func (e *Encoder) Capabilities() Capabilities { return e.caps }

// Reconfigure replaces the encoder's capabilities. Any pending DEC graphics
// state is closed before the switch so the new encoder starts in a known
// state. The closing bytes are returned and must be written to the wire
// before any subsequent EncodeOut output.
func (e *Encoder) Reconfigure(caps Capabilities) []byte {
	closing := e.FinalizeOut()
	e.caps = caps
	return closing
}

// FinalizeOut flushes any pending wire state. Currently this emits the
// closing ESC ( B if the encoder left the terminal in DEC graphics mode.
func (e *Encoder) FinalizeOut() []byte {
	if e.inGraphics {
		e.inGraphics = false
		return []byte("\x1B(B")
	}
	return nil
}

// EncodeOut translates a UTF-8 + ANSI buffer into wire-encoded bytes.
// DEC line-drawing state persists across calls so back-to-back writes
// containing line characters do not re-bracket each character.
func (e *Encoder) EncodeOut(p []byte) ([]byte, error) {
	var out bytes.Buffer
	i := 0
	for i < len(p) {
		// ANSI CSI?
		if looksLikeCSI(p, i) {
			end := findCSITerminator(p, i)
			if end == -1 {
				// Incomplete CSI; output whatever's left as bytes. The
				// caller typically buffers complete sequences, so this is
				// best-effort.
				e.exitGraphics(&out)
				out.Write(p[i:])
				return out.Bytes(), nil
			}
			e.exitGraphics(&out)
			e.emitCSI(&out, p[i:end+1])
			i = end + 1
			continue
		}
		// Designators (ESC ( x / ESC ) x) — strip on ASCII, pass-through
		// otherwise. The engine itself doesn't generate these, but a
		// passthrough is harmless for terminals that understand them.
		if looksLikeDesignator(p, i) {
			end := findDesignatorEnd(p, i)
			if end == -1 {
				out.Write(p[i:])
				return out.Bytes(), nil
			}
			if e.caps.Encoding != EncodingASCII && e.caps.Encoding != EncodingPETSCII {
				out.Write(p[i : end+1])
			}
			i = end + 1
			continue
		}
		// Regular rune.
		r, size := utf8.DecodeRune(p[i:])
		if r == utf8.RuneError && size == 1 {
			// Invalid UTF-8 byte; emit '?' (or drop). Skip.
			e.exitGraphics(&out)
			out.WriteByte('?')
			i++
			continue
		}
		e.emitRune(&out, r)
		i += size
	}
	return out.Bytes(), nil
}

// DecodeIn translates wire bytes from the client back into UTF-8 for the
// engine. ANSI sequences (rarely sent by clients) and DEC graphics state
// are not tracked here — the input side is assumed to be plain text in
// the chosen encoding.
func (e *Encoder) DecodeIn(p []byte) ([]byte, error) {
	switch e.caps.Encoding {
	case EncodingUTF8, EncodingASCII:
		// UTF-8 / 7-bit ASCII map identically into UTF-8 strings.
		return append([]byte(nil), p...), nil
	case EncodingCP437:
		return charmap.CodePage437.NewDecoder().Bytes(p)
	case EncodingISO88591:
		return charmap.ISO8859_1.NewDecoder().Bytes(p)
	case EncodingMacRoman:
		return charmap.Macintosh.NewDecoder().Bytes(p)
	case EncodingPETSCII:
		return petsciiDecode(p), nil
	}
	return append([]byte(nil), p...), nil
}

// ---------------------------------------------------------------------------
// emission helpers

func (e *Encoder) emitRune(out *bytes.Buffer, r rune) {
	switch classify(r) {
	case drawSingleLine:
		e.emitDrawing(out, r)
	case drawDoubleLine:
		single, ok := doubleToSingle[r]
		if !ok {
			single = r
		}
		if e.caps.Encoding == EncodingUTF8 || e.caps.Encoding == EncodingCP437 {
			// Keep the double-line form for native-capable encodings.
			e.exitGraphics(out)
			e.emitCharNonDrawing(out, r)
		} else {
			e.emitDrawing(out, single)
		}
	case drawSemiGraphics:
		e.exitGraphics(out)
		e.emitSemiGraphics(out, r)
	default:
		e.exitGraphics(out)
		e.emitCharNonDrawing(out, r)
	}
}

func (e *Encoder) emitDrawing(out *bytes.Buffer, r rune) {
	// DEC path applies only to encodings that can carry escape sequences
	// AND where DEC is enabled. PETSCII / ASCII never use DEC.
	canDEC := e.caps.DECLineDrawing &&
		e.caps.Encoding != EncodingPETSCII &&
		e.caps.Encoding != EncodingASCII
	if canDEC {
		if !e.inGraphics {
			out.WriteString("\x1B(0")
			e.inGraphics = true
		}
		if b, ok := decMap[r]; ok {
			out.WriteByte(b)
			return
		}
		// No DEC equivalent (rare). Fall through to native handling.
		e.exitGraphics(out)
	}
	switch e.caps.Encoding {
	case EncodingUTF8:
		out.WriteRune(r)
	case EncodingCP437:
		if b, ok := cp437Drawing[r]; ok {
			out.WriteByte(b)
		} else {
			out.WriteByte(' ')
		}
	case EncodingISO88591, EncodingMacRoman, EncodingASCII:
		out.WriteByte(asciiBoxApprox(r))
	case EncodingPETSCII:
		if b, ok := petsciiDrawing[r]; ok {
			out.WriteByte(b)
		} else {
			out.WriteByte(' ')
		}
	}
}

func (e *Encoder) emitSemiGraphics(out *bytes.Buffer, r rune) {
	switch e.caps.Encoding {
	case EncodingUTF8:
		out.WriteRune(r)
	case EncodingCP437:
		if b, ok := cp437Drawing[r]; ok {
			out.WriteByte(b)
		} else {
			out.WriteByte(' ')
		}
	case EncodingPETSCII:
		if b, ok := petsciiDrawing[r]; ok {
			out.WriteByte(b)
		} else {
			out.WriteByte(' ')
		}
	case EncodingISO88591, EncodingMacRoman, EncodingASCII:
		out.WriteByte(' ')
	}
}

func (e *Encoder) emitCharNonDrawing(out *bytes.Buffer, r rune) {
	switch e.caps.Encoding {
	case EncodingUTF8:
		if r == utf8.RuneError {
			out.WriteByte('?')
			return
		}
		out.WriteRune(r)
	case EncodingCP437:
		writeCharmap(out, charmap.CodePage437, r)
	case EncodingISO88591:
		writeCharmap(out, charmap.ISO8859_1, r)
	case EncodingMacRoman:
		writeCharmap(out, charmap.Macintosh, r)
	case EncodingPETSCII:
		writePETSCII(out, r)
	case EncodingASCII:
		writeASCII(out, r)
	}
}

func (e *Encoder) emitCSI(out *bytes.Buffer, seq []byte) {
	switch e.caps.Encoding {
	case EncodingASCII:
		// Strip entirely.
		return
	case EncodingPETSCII:
		// Translate SGR colors to PETSCII color bytes. Drop everything else.
		params := sgrParams(seq)
		if params == nil {
			return
		}
		if !e.caps.Color {
			return
		}
		for _, p := range params {
			if b := petsciiFromANSIColor(p); b != 0 {
				out.WriteByte(b)
			}
		}
	default:
		// UTF-8 / CP437 / Latin-1 / MacRoman: pass-through.
		if !e.caps.Color {
			// If color is disabled but the sequence is an SGR, drop it.
			if len(seq) >= 1 && seq[len(seq)-1] == 'm' {
				return
			}
		}
		out.Write(seq)
	}
}

func (e *Encoder) exitGraphics(out *bytes.Buffer) {
	if e.inGraphics {
		out.WriteString("\x1B(B")
		e.inGraphics = false
	}
}

// ---------------------------------------------------------------------------
// per-encoding character writers

// writeCharmap writes r encoded via the given charmap. Characters that
// cannot be represented become '?'. (Drawing characters never reach this
// path; they go through emitDrawing / emitSemiGraphics.)
func writeCharmap(out *bytes.Buffer, cm *charmap.Charmap, r rune) {
	// Charmap.Encoder returns U+FFFD for unmappable chars by default.
	b, ok := cm.EncodeRune(r)
	if !ok {
		out.WriteByte('?')
		return
	}
	out.WriteByte(b)
}

// writeASCII writes r as 7-bit ASCII. Non-ASCII non-drawing characters
// emit '?'; the caller handles ANSI stripping separately.
func writeASCII(out *bytes.Buffer, r rune) {
	if r < 0x80 && r >= 0x20 {
		out.WriteByte(byte(r))
		return
	}
	switch r {
	case '\n', '\r', '\t', 0x08:
		out.WriteByte(byte(r))
	default:
		out.WriteByte('?')
	}
}

// writePETSCII writes r in PETSCII mixed-case mode. Letters case-swap,
// digits and standard ASCII punctuation map identity, CR/LF translate to
// PETSCII CR (0x0D).
func writePETSCII(out *bytes.Buffer, r rune) {
	switch {
	case r >= 'A' && r <= 'Z':
		out.WriteByte(byte(r) + 0x20)
	case r >= 'a' && r <= 'z':
		out.WriteByte(byte(r) - 0x20)
	case r >= 0x20 && r <= 0x40:
		// Digits, space, common punctuation — identical positions.
		out.WriteByte(byte(r))
	case r == '\n':
		out.WriteByte(petsciiCR)
	case r == '\r':
		out.WriteByte(petsciiCR)
	case r == '\t':
		out.WriteByte(' ')
	case r == '[' || r == ']':
		out.WriteByte(byte(r))
	default:
		// Anything else gets '?'.
		out.WriteByte('?')
	}
}

// petsciiDecode performs a best-effort decode of PETSCII input back to
// UTF-8. The engine reads typed input from the client; we mirror the
// case-swap from writePETSCII.
func petsciiDecode(p []byte) []byte {
	out := make([]byte, 0, len(p))
	for _, b := range p {
		switch {
		case b >= 'A' && b <= 'Z':
			out = append(out, b+0x20)
		case b >= 'a' && b <= 'z':
			out = append(out, b-0x20)
		case b >= 0x20 && b <= 0x7E:
			out = append(out, b)
		case b == petsciiCR:
			out = append(out, '\n')
		case b == 0x14: // PETSCII DEL/backspace
			out = append(out, 0x08)
		}
		// Anything else (control codes, graphics) is dropped on input.
	}
	return out
}
