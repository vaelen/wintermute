// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package readline

import (
	"bufio"
	"unicode/utf8"

	"github.com/vaelen/wintermute/internal/term"
)

// Key identifies one logical input event. KeyChar carries a decoded rune
// in Event.Rune; every other Key is a discrete editing or control action.
type Key int

// Keys recognized by the tier-1 editor.
const (
	KeyUnknown Key = iota
	KeyChar
	KeyEnter
	KeyBackspace
	KeyDelete
	KeyLeft
	KeyRight
	KeyHome
	KeyEnd
	KeyUp
	KeyDown
	KeyKillToEnd   // Ctrl-K
	KeyKillToStart // Ctrl-U
	KeyKillWord    // Ctrl-W
	KeyInterrupt   // Ctrl-C
	KeyEOF         // Ctrl-D
	KeyClear       // Ctrl-L (redraw)
)

// Event is the result of one readEvent call.
type Event struct {
	Key  Key
	Rune rune // valid only when Key == KeyChar
}

// readEvent consumes one logical input event from in. CSI sequences
// (cursor arrows, Home/End/Delete) are parsed inline; control bytes are
// mapped to their corresponding Key; printable bytes decode through enc
// into a rune. enc may be nil, in which case input is treated as ASCII.
//
// For UTF-8 sessions a multibyte rune that arrives split across reads
// is reassembled here; for single-byte encodings every input byte yields
// exactly one Event.
func readEvent(in *bufio.Reader, enc *term.Encoder) (Event, error) {
	b, err := in.ReadByte()
	if err != nil {
		return Event{}, err
	}
	switch b {
	case '\r':
		// Treat CR / CRLF / LF identically as Enter.
		if next, _ := in.Peek(1); len(next) > 0 && next[0] == '\n' {
			_, _ = in.ReadByte()
		}
		return Event{Key: KeyEnter}, nil
	case '\n':
		return Event{Key: KeyEnter}, nil
	case 0x01:
		return Event{Key: KeyHome}, nil
	case 0x03:
		return Event{Key: KeyInterrupt}, nil
	case 0x04:
		return Event{Key: KeyEOF}, nil
	case 0x05:
		return Event{Key: KeyEnd}, nil
	case 0x08, 0x7F:
		return Event{Key: KeyBackspace}, nil
	case 0x0B:
		return Event{Key: KeyKillToEnd}, nil
	case 0x0C:
		return Event{Key: KeyClear}, nil
	case 0x15:
		return Event{Key: KeyKillToStart}, nil
	case 0x17:
		return Event{Key: KeyKillWord}, nil
	case 0x1B:
		return readEscape(in)
	}
	if b < 0x20 {
		return Event{Key: KeyUnknown}, nil
	}
	if b < 0x80 {
		return Event{Key: KeyChar, Rune: rune(b)}, nil
	}
	return decodeHighByte(b, in, enc)
}

// readEscape consumes the bytes following an ESC. Recognized prefixes:
// ESC [  → CSI sequence; ESC O → SS3-style cursor key. Anything else
// becomes KeyUnknown so the editor can discard it.
func readEscape(in *bufio.Reader) (Event, error) {
	b, err := in.ReadByte()
	if err != nil {
		return Event{}, err
	}
	switch b {
	case '[':
		return readCSI(in)
	case 'O':
		b2, err := in.ReadByte()
		if err != nil {
			return Event{}, err
		}
		switch b2 {
		case 'A':
			return Event{Key: KeyUp}, nil
		case 'B':
			return Event{Key: KeyDown}, nil
		case 'C':
			return Event{Key: KeyRight}, nil
		case 'D':
			return Event{Key: KeyLeft}, nil
		case 'H':
			return Event{Key: KeyHome}, nil
		case 'F':
			return Event{Key: KeyEnd}, nil
		}
		return Event{Key: KeyUnknown}, nil
	}
	return Event{Key: KeyUnknown}, nil
}

// readCSI reads the parameter bytes (digits, semicolons) plus the
// terminating byte (0x40–0x7E) of an ANSI CSI sequence and classifies
// it into a Key. Sequences we don't recognize collapse to KeyUnknown.
func readCSI(in *bufio.Reader) (Event, error) {
	var params []byte
	for {
		b, err := in.ReadByte()
		if err != nil {
			return Event{}, err
		}
		if b >= 0x40 && b <= 0x7E {
			return classifyCSI(params, b), nil
		}
		params = append(params, b)
		// Defensive cap. Real CSI sequences for cursor / function keys
		// are short; anything longer is almost certainly noise.
		if len(params) > 32 {
			return Event{Key: KeyUnknown}, nil
		}
	}
}

func classifyCSI(params []byte, final byte) Event {
	switch final {
	case 'A':
		return Event{Key: KeyUp}
	case 'B':
		return Event{Key: KeyDown}
	case 'C':
		return Event{Key: KeyRight}
	case 'D':
		return Event{Key: KeyLeft}
	case 'H':
		return Event{Key: KeyHome}
	case 'F':
		return Event{Key: KeyEnd}
	case '~':
		// VT-style function-key sequences. Only the editing keys we
		// care about are handled.
		switch string(params) {
		case "1", "7":
			return Event{Key: KeyHome}
		case "3":
			return Event{Key: KeyDelete}
		case "4", "8":
			return Event{Key: KeyEnd}
		}
	}
	return Event{Key: KeyUnknown}
}

// decodeHighByte assembles a non-ASCII rune from in. For UTF-8 sessions
// the rune's expected length is read from the leading byte and the
// remaining continuation bytes are pulled from in; for single-byte
// encodings the byte is decoded through enc directly.
func decodeHighByte(first byte, in *bufio.Reader, enc *term.Encoder) (Event, error) {
	encoding := term.EncodingASCII
	if enc != nil {
		encoding = enc.Capabilities().Encoding
	}
	if encoding == term.EncodingUTF8 {
		n := utf8RuneLen(first)
		if n <= 0 {
			return Event{Key: KeyUnknown}, nil
		}
		buf := make([]byte, n)
		buf[0] = first
		for i := 1; i < n; i++ {
			b, err := in.ReadByte()
			if err != nil {
				return Event{}, err
			}
			buf[i] = b
		}
		r, size := utf8.DecodeRune(buf)
		if r == utf8.RuneError && size <= 1 {
			return Event{Key: KeyUnknown}, nil
		}
		return Event{Key: KeyChar, Rune: r}, nil
	}
	if enc == nil {
		// No encoder: high bytes are not meaningful, drop them.
		return Event{Key: KeyUnknown}, nil
	}
	out, err := enc.DecodeIn([]byte{first})
	if err != nil || len(out) == 0 {
		return Event{Key: KeyUnknown}, nil
	}
	r, _ := utf8.DecodeRune(out)
	if r == utf8.RuneError {
		return Event{Key: KeyUnknown}, nil
	}
	return Event{Key: KeyChar, Rune: r}, nil
}

func utf8RuneLen(b byte) int {
	switch {
	case b < 0x80:
		return 1
	case b&0xE0 == 0xC0:
		return 2
	case b&0xF0 == 0xE0:
		return 3
	case b&0xF8 == 0xF0:
		return 4
	}
	return -1
}
