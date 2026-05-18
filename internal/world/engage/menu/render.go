// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

// Package menu renders a line-drawn engagement menu and runs the input
// state machine that backs the KindMenuTerminal handler. The renderer
// emits UTF-8 box-drawing characters unconditionally — the M1 charset
// conversion layer handles any downstream translation for non-UTF-8
// clients.
package menu

import (
	"strings"
	"unicode/utf8"
)

// Width bounds. NAWS-reported widths are clamped into [MinWidth, MaxWidth]
// before rendering; DefaultWidth is used when no width is available.
const (
	MinWidth     = 30
	MaxWidth     = 80
	DefaultWidth = 60
)

// ClearScreen is the ANSI sequence sent before each frame to wipe the
// terminal and home the cursor. Tests render Frames in isolation and do
// not see this prefix; the Handler prepends it when writing to a session.
const ClearScreen = "\x1b[2J\x1b[H"

// ClampWidth applies the MinWidth/MaxWidth bounds. A non-positive input
// yields DefaultWidth.
func ClampWidth(w int) int {
	if w <= 0 {
		return DefaultWidth
	}
	if w < MinWidth {
		return MinWidth
	}
	if w > MaxWidth {
		return MaxWidth
	}
	return w
}

// Frame is a single menu screen ready to render. Width is the total line
// length including the left and right borders. Title is centred in the
// top border. Rows are emitted between the top and bottom borders.
type Frame struct {
	Width int
	Title string
	Rows  []Row
}

// Row is one body line. Blank suppresses content (pad with spaces). For
// non-blank rows, Selector and Label are joined at the left ("  1)  Mail"),
// and Note is right-aligned within the same line ("(3 unread)   ").
type Row struct {
	Selector string
	Label    string
	Note     string
	Blank    bool
}

// String renders the frame to a multi-line CRLF-terminated string.
func (f Frame) String() string {
	w := ClampWidth(f.Width)
	var b strings.Builder
	b.WriteString(topBorder(w, f.Title))
	b.WriteString("\r\n")
	for _, r := range f.Rows {
		b.WriteString(renderRow(w, r))
		b.WriteString("\r\n")
	}
	b.WriteString(bottomBorder(w))
	b.WriteString("\r\n")
	return b.String()
}

// topBorder renders "┌────── title ──────┐", padded to total width w.
// The title is centred and surrounded by single spaces when non-empty.
func topBorder(w int, title string) string {
	inner := w - 2 // chars between corners
	if title == "" {
		return "┌" + strings.Repeat("─", inner) + "┐"
	}
	padded := " " + title + " "
	padLen := utf8.RuneCountInString(padded)
	if padLen >= inner {
		// Title too long; truncate to fit, no surrounding spaces.
		return "┌" + truncateRunes(title, inner) + "┐"
	}
	leftBars := (inner - padLen) / 2
	rightBars := inner - padLen - leftBars
	return "┌" +
		strings.Repeat("─", leftBars) +
		padded +
		strings.Repeat("─", rightBars) +
		"┐"
}

// bottomBorder renders "└──────┘".
func bottomBorder(w int) string {
	return "└" + strings.Repeat("─", w-2) + "┘"
}

// renderRow renders one interior line "│ ...content... │" padded to w.
func renderRow(w int, r Row) string {
	inner := w - 2
	if r.Blank {
		return "│" + strings.Repeat(" ", inner) + "│"
	}
	// Left side: leading indent + selector + spaces + label.
	left := "  "
	if r.Selector != "" {
		left += r.Selector + "  "
	}
	left += r.Label
	leftLen := utf8.RuneCountInString(left)
	right := r.Note
	rightLen := utf8.RuneCountInString(right)
	// Minimum gap of 2 between left and right (only enforced when note exists).
	minGap := 0
	if right != "" {
		minGap = 2
	}
	// Always leave a 2-space margin on the right edge before the border.
	rightMargin := 2
	available := inner - leftLen - rightLen - minGap - rightMargin
	if available < 0 {
		// Truncate label to make room.
		overflow := -available
		labelKeep := utf8.RuneCountInString(r.Label) - overflow
		if labelKeep < 0 {
			labelKeep = 0
		}
		newLabel := truncateRunes(r.Label, labelKeep)
		left = "  "
		if r.Selector != "" {
			left += r.Selector + "  "
		}
		left += newLabel
		leftLen = utf8.RuneCountInString(left)
		available = inner - leftLen - rightLen - minGap - rightMargin
		if available < 0 {
			available = 0
		}
	}
	mid := strings.Repeat(" ", minGap+available)
	return "│" + left + mid + right + strings.Repeat(" ", rightMargin) + "│"
}

// truncateRunes returns the first n runes of s. If s already has ≤ n
// runes it is returned unchanged.
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}
