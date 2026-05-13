// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package term

// This file holds the drawing-character tables that the encoder uses to
// translate UTF-8 internal representation into per-encoding wire bytes.
//
// Three categories are tracked:
//   - Single-line box-drawing  (U+2500–U+257F, single-stroke set)
//   - Double-line box-drawing  (subset of U+2550–U+256C mirroring CP437)
//   - Shade / block / semi-graphics (U+2580–U+259F)
//
// Categorization helpers and per-encoding mappings live here. ENCODER state
// (DEC graphics in/out tracking) is on the Encoder itself, not here.

// classify returns the drawing-character category for r, or
// drawCategoryNone if r is not a drawing character.
type drawCategory int

const (
	drawNone drawCategory = iota
	drawSingleLine
	drawDoubleLine
	drawSemiGraphics
)

func classify(r rune) drawCategory {
	switch {
	case isSingleLineBox(r):
		return drawSingleLine
	case isDoubleLineBox(r):
		return drawDoubleLine
	case isSemiGraphics(r):
		return drawSemiGraphics
	}
	return drawNone
}

// isSingleLineBox reports whether r is one of the single-stroke
// box-drawing characters with a DEC Special Graphics counterpart.
func isSingleLineBox(r rune) bool {
	_, ok := decMap[r]
	return ok
}

// isDoubleLineBox reports whether r is one of the CP437-mirrored
// double-line box-drawing characters.
func isDoubleLineBox(r rune) bool {
	_, ok := doubleToSingle[r]
	return ok
}

// isSemiGraphics reports whether r is in the shade / block / semi-graphics
// range used by CP437 (U+2580–U+259F).
func isSemiGraphics(r rune) bool {
	return r >= 0x2580 && r <= 0x259F
}

// ---------------------------------------------------------------------------
// DEC Special Graphics mapping
//
// The byte values are the 7-bit codes the terminal interprets *while* G0
// is designated to the DEC Special Graphics set (i.e. between ESC ( 0 and
// ESC ( B). The set is a VT100 standard; only the subset that has a
// corresponding Unicode box-drawing character is included here.

var decMap = map[rune]byte{
	'─': 'q', // horizontal line
	'│': 'x', // vertical line
	'┌': 'l', // upper-left corner
	'┐': 'k', // upper-right corner
	'└': 'm', // lower-left corner
	'┘': 'j', // lower-right corner
	'├': 't', // tee, line to the right
	'┤': 'u', // tee, line to the left
	'┬': 'w', // tee, line down
	'┴': 'v', // tee, line up
	'┼': 'n', // cross
	'═': 'q', // double-line forms (after downgrade)
	'║': 'x',
}

// ---------------------------------------------------------------------------
// Double-line → single-line normalization

var doubleToSingle = map[rune]rune{
	'═': '─', '║': '│',
	'╔': '┌', '╗': '┐', '╚': '└', '╝': '┘',
	'╠': '├', '╣': '┤', '╦': '┬', '╩': '┴', '╬': '┼',
	// Mixed single/double variants — degrade to the all-single form.
	'╒': '┌', '╓': '┌', '╕': '┐', '╖': '┐',
	'╘': '└', '╙': '└', '╛': '┘', '╜': '┘',
	'╞': '├', '╟': '├', '╡': '┤', '╢': '┤',
	'╤': '┬', '╥': '┬', '╧': '┴', '╨': '┴',
	'╪': '┼', '╫': '┼',
}

// ---------------------------------------------------------------------------
// CP437 native mapping
//
// CP437 has a native repertoire for both single- and double-line box drawing
// and for the U+2580–U+259F semi-graphics range. We map UTF-8 directly to
// the high-byte CP437 codepoints. Anything not in this table falls back to
// space (for drawing chars) or '?' (for everything else).

var cp437Drawing = map[rune]byte{
	// single-line box
	'─': 0xC4, '│': 0xB3,
	'┌': 0xDA, '┐': 0xBF, '└': 0xC0, '┘': 0xD9,
	'├': 0xC3, '┤': 0xB4, '┬': 0xC2, '┴': 0xC1, '┼': 0xC5,
	// double-line box
	'═': 0xCD, '║': 0xBA,
	'╔': 0xC9, '╗': 0xBB, '╚': 0xC8, '╝': 0xBC,
	'╠': 0xCC, '╣': 0xB9, '╦': 0xCB, '╩': 0xCA, '╬': 0xCE,
	// mixed-strength corners
	'╒': 0xD5, '╓': 0xD6, '╕': 0xB8, '╖': 0xB7,
	'╘': 0xD4, '╙': 0xD3, '╛': 0xBE, '╜': 0xBD,
	'╞': 0xC6, '╟': 0xC7, '╡': 0xB5, '╢': 0xB6,
	'╤': 0xD1, '╥': 0xD2, '╧': 0xCF, '╨': 0xD0,
	'╪': 0xD8, '╫': 0xD7,
	// shade / block / semi-graphics
	'▀': 0xDF, '▄': 0xDC, '█': 0xDB, '▌': 0xDD, '▐': 0xDE,
	'░': 0xB0, '▒': 0xB1, '▓': 0xB2,
}

// ---------------------------------------------------------------------------
// PETSCII drawing mapping (mixed-case mode, after Shift Out)
//
// These values are the byte codes that render as the expected glyphs on a
// C64 currently in mixed-case (lowercase/uppercase) mode. Coverage is
// approximate; characters not in the table fall back to space per the
// design's "drawing → space" rule.

var petsciiDrawing = map[rune]byte{
	'─': 0xC0,
	'│': 0xDD,
	'┌': 0xB0,
	'┐': 0xAE,
	'└': 0xAD,
	'┘': 0xBD,
	'├': 0xAB,
	'┤': 0xB3,
	'┬': 0xB2,
	'┴': 0xB1,
	'┼': 0xDB,
	'█': 0xA0, // reverse space = solid block
	'▌': 0xA1,
	'▐': 0xA7,
	'▀': 0xA2,
	'▄': 0xA4,
	'░': 0xA6,
	'▒': 0xA6, // approximation; PETSCII lacks medium-shade
	'▓': 0xA6,
}

// ---------------------------------------------------------------------------
// ASCII approximation
//
// When DEC line drawing is off (or the encoding is ASCII), single-line box
// chars degrade to '-' / '|' / '+'. Double-line chars are normalized first
// so they take the same path. Shade / semi-graphics fall back to space.

func asciiBoxApprox(r rune) byte {
	switch r {
	case '─':
		return '-'
	case '│':
		return '|'
	}
	// Any cross / corner / tee → '+'
	if _, ok := decMap[r]; ok {
		return '+'
	}
	return ' '
}
