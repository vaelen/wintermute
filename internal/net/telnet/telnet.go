// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package telnet

// Telnet command bytes (RFC 854).
const (
	cmdSE   byte = 240
	cmdNOP  byte = 241
	cmdAYT  byte = 246
	cmdSB   byte = 250
	cmdWILL byte = 251
	cmdWONT byte = 252
	cmdDO   byte = 253
	cmdDONT byte = 254
	cmdIAC  byte = 255
)

// Option codes for the small subset we care about.
const (
	optEcho     byte = 1
	optSGA      byte = 3  // Suppress Go Ahead (RFC 858)
	optTTYPE    byte = 24 // Terminal Type (RFC 1091)
	optNAWS     byte = 31 // Negotiate About Window Size (RFC 1073)
	optLINEMODE byte = 34 // Linemode (RFC 1184) — we proactively reject
	optCHARSET  byte = 42 // CHARSET (RFC 2066)
)

// Subnegotiation sub-commands.
const (
	ttypeIS    byte = 0
	ttypeSEND  byte = 1
	chrstREQ   byte = 1
	chrstACCPT byte = 2
	chrstREJ   byte = 3
)

// IsIAC reports whether b is the IAC marker.
func IsIAC(b byte) bool { return b == cmdIAC }
