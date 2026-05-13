// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package term

// PETSCII control codes and a partial translation table for the
// (post-Shift-Out) mixed-case mode the engine assumes after connect.
//
// References used at design time: standard PETSCII tables (Mapping
// PETSCII to Unicode wikis, CBM hardware reference). Approximate.

// PETSCIIShiftOut is the byte the engine sends on every connect to put
// PETSCII clients into mixed-case mode. Modern terminals ignore it.
const PETSCIIShiftOut byte = 0x0E

// PETSCII control codes (subset).
const (
	petsciiNUL      byte = 0x00
	petsciiCR       byte = 0x0D
	petsciiShiftOut      = PETSCIIShiftOut
	petsciiHome     byte = 0x13
	petsciiClear    byte = 0x93
	petsciiCursorDn byte = 0x11
	petsciiCursorUp byte = 0x91
	petsciiCursorRt byte = 0x1D
	petsciiCursorLt byte = 0x9D
	petsciiRVSOn    byte = 0x12
	petsciiRVSOff   byte = 0x92
)

// PETSCII color codes.
const (
	petsciiColorBlack     byte = 0x90
	petsciiColorWhite     byte = 0x05
	petsciiColorRed       byte = 0x1C
	petsciiColorCyan      byte = 0x9F
	petsciiColorPurple    byte = 0x9C
	petsciiColorGreen     byte = 0x1E
	petsciiColorBlue      byte = 0x1F
	petsciiColorYellow    byte = 0x9E
	petsciiColorOrange    byte = 0x81
	petsciiColorBrown     byte = 0x95
	petsciiColorLightRed  byte = 0x96
	petsciiColorDarkGray  byte = 0x97
	petsciiColorMidGray   byte = 0x98
	petsciiColorLightGrn  byte = 0x99
	petsciiColorLightBlue byte = 0x9A
	petsciiColorLightGray byte = 0x9B
)

// petsciiFromASCIILetter converts an ASCII letter rune to the PETSCII byte
// that, in mixed-case mode, displays as that letter. Mixed-case mode swaps
// the case interpretation of 0x41-0x5A vs 0x61-0x7A: the byte 0x41 ('A' in
// uppercase mode) displays as lowercase 'a' in mixed-case mode. So to
// display an uppercase letter we send the lowercase-position byte, and vice
// versa.
func petsciiFromASCIILetter(r rune) byte {
	switch {
	case r >= 'A' && r <= 'Z':
		return byte(r) + 0x20 // emit lowercase-position byte
	case r >= 'a' && r <= 'z':
		return byte(r) - 0x20 // emit uppercase-position byte
	}
	return byte(r)
}

// petsciiFromANSIColor returns the PETSCII color control byte that most
// closely matches the given ANSI SGR foreground code. Returns 0 if the code
// has no useful mapping (the caller may then drop it).
func petsciiFromANSIColor(sgr int) byte {
	switch sgr {
	case 0: // reset → return to default white
		return petsciiColorWhite
	case 30:
		return petsciiColorBlack
	case 31:
		return petsciiColorRed
	case 32:
		return petsciiColorGreen
	case 33:
		return petsciiColorYellow
	case 34:
		return petsciiColorBlue
	case 35:
		return petsciiColorPurple
	case 36:
		return petsciiColorCyan
	case 37:
		return petsciiColorWhite
	case 90:
		return petsciiColorDarkGray
	case 91:
		return petsciiColorLightRed
	case 92:
		return petsciiColorLightGrn
	case 93:
		return petsciiColorYellow
	case 94:
		return petsciiColorLightBlue
	case 95:
		return petsciiColorPurple
	case 96:
		return petsciiColorCyan
	case 97:
		return petsciiColorLightGray
	}
	return 0
}
