// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package term

// findCSITerminator returns the index of the byte that ends the ANSI CSI
// sequence beginning at start (which must point at ESC). Returns -1 if the
// buffer ends before the sequence terminates.
//
// CSI format: ESC '[' (parameter bytes 0x30–0x3F)* (intermediate 0x20–0x2F)*
//             final byte 0x40–0x7E.
//
// We accept any final-byte in 0x40–0x7E as the terminator. Non-CSI escape
// sequences (e.g. ESC '(' x) are handled separately by isLeadingDesignator.
func findCSITerminator(p []byte, start int) int {
	if start >= len(p) || p[start] != 0x1B {
		return -1
	}
	if start+1 >= len(p) || p[start+1] != '[' {
		return -1
	}
	for i := start + 2; i < len(p); i++ {
		b := p[i]
		if b >= 0x40 && b <= 0x7E {
			return i
		}
	}
	return -1
}

// findDesignatorEnd returns the index of the byte that ends a character-set
// designation sequence (ESC ( x, ESC ) x, etc.) starting at start. Such
// sequences are always exactly 3 bytes long; this is a small helper for
// clarity rather than parsing.
func findDesignatorEnd(p []byte, start int) int {
	if start+2 < len(p) {
		return start + 2
	}
	return -1
}

// looksLikeCSI returns true if p, starting at i, looks like the beginning
// of a CSI escape sequence (ESC '[').
func looksLikeCSI(p []byte, i int) bool {
	return i+1 < len(p) && p[i] == 0x1B && p[i+1] == '['
}

// looksLikeDesignator returns true if p, starting at i, looks like a
// character-set designation introducer (ESC '(' or ESC ')').
func looksLikeDesignator(p []byte, i int) bool {
	return i+1 < len(p) && p[i] == 0x1B && (p[i+1] == '(' || p[i+1] == ')')
}

// ---------------------------------------------------------------------------
// ANSI SGR parsing (for PETSCII color translation)

// sgrParams extracts the numeric parameters of a CSI sequence ending in 'm'.
// Returns nil if the sequence is not an SGR (i.e. final byte is not 'm') or
// if the sequence is malformed.
//
// Examples:
//
//	"\x1b[0m"       -> [0]
//	"\x1b[1;31m"    -> [1, 31]
//	"\x1b[m"        -> [0]
func sgrParams(seq []byte) []int {
	if len(seq) < 3 || seq[len(seq)-1] != 'm' {
		return nil
	}
	// Strip the leading ESC '[' and trailing 'm'.
	body := seq[2 : len(seq)-1]
	if len(body) == 0 {
		return []int{0}
	}
	var out []int
	var n int
	var hasDigit bool
	for _, b := range body {
		if b >= '0' && b <= '9' {
			n = n*10 + int(b-'0')
			hasDigit = true
		} else if b == ';' {
			if hasDigit {
				out = append(out, n)
			} else {
				out = append(out, 0)
			}
			n = 0
			hasDigit = false
		} else {
			// Ignore other parameter / intermediate bytes for now.
			hasDigit = false
		}
	}
	if hasDigit {
		out = append(out, n)
	} else if len(out) == 0 {
		out = append(out, 0)
	}
	return out
}
