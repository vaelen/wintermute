// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

// Package readline provides an in-line editor for sessions whose
// terminal advertises ANSI CSI support. It owns nothing connection-
// related: it takes a *bufio.Reader for input, an io.Writer for
// output, a *term.Encoder for rune-to-wire conversion, and a
// per-session History ring buffer.
//
// Tier-1 scope (milestone 5.5):
//
//   - Cursor: Left, Right, Home, End (CSI and Ctrl-A / Ctrl-E).
//   - Edit: insert at cursor, Backspace, Delete, Ctrl-W (kill word left),
//     Ctrl-U (kill to start), Ctrl-K (kill to end).
//   - History: Up and Down arrows browse a per-session ring buffer; the
//     in-progress draft is preserved across browsing.
//   - Control: Ctrl-C cancels the current line (returns ErrInterrupt),
//     Ctrl-D on an empty buffer returns io.EOF, Ctrl-L redraws.
//
// Tier-1 explicitly does not handle: multi-line wrapping, wide-character
// or combining-mark cell accounting, persisted history, tab completion,
// vi mode, reverse-incremental search. PETSCII sessions fall back to the
// plain readline loop owned by the session package.
package readline
