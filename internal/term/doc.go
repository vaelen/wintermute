// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

// Package term owns the engine's terminal capability model and the
// per-encoding I/O translation layer. The engine composes all output as
// UTF-8 + ANSI internally; term downgrades on the way out (and upgrades
// on the way in) for the six supported encodings: UTF-8, CP437,
// ISO-8859-1, MacRoman, PETSCII, and ASCII.
//
// It also owns capability auto-detection (the ANSI probe, TTYPE-hint
// interpretation, default selection) and the pre-login confirmation
// prompt. See docs/milestones/01-connection-layer.md.
package term
