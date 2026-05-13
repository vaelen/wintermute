// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

// Package telnet implements the telnet protocol layer: IAC option negotiation
// (CHARSET, NAWS, TTYPE), session-level read/write with character-set
// transcoding, and a binary-mode toggle used during in-band file transfer.
// See docs/milestones/01-connection-layer.md.
package telnet
