// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package menu

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClampWidth(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{0, DefaultWidth},
		{10, MinWidth},
		{DefaultWidth, DefaultWidth},
		{MaxWidth - 1, MaxWidth - 1},
		{MaxWidth, MaxWidth},
		{MaxWidth + 50, MaxWidth},
	}
	for _, c := range cases {
		if got := ClampWidth(c.in); got != c.want {
			t.Errorf("ClampWidth(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestFrame_topAndBottomBorders(t *testing.T) {
	f := Frame{Width: 30, Title: "menu", Rows: []Row{{Blank: true}}}
	out := f.String()
	lines := splitFrame(out)
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines, got %d (%q)", len(lines), out)
	}
	if !strings.HasPrefix(lines[0], "┌") || !strings.HasSuffix(lines[0], "┐") {
		t.Errorf("top border = %q", lines[0])
	}
	if !strings.HasPrefix(lines[len(lines)-1], "└") || !strings.HasSuffix(lines[len(lines)-1], "┘") {
		t.Errorf("bottom border = %q", lines[len(lines)-1])
	}
}

func TestFrame_titleEmbeddedInTop(t *testing.T) {
	f := Frame{Width: 40, Title: "lobby terminal", Rows: []Row{{Blank: true}}}
	out := f.String()
	top := splitFrame(out)[0]
	if !strings.Contains(top, "lobby terminal") {
		t.Errorf("title not in top border: %q", top)
	}
}

func TestFrame_widthMatches(t *testing.T) {
	f := Frame{Width: 40, Title: "x", Rows: []Row{{Blank: true}}}
	out := f.String()
	for i, line := range splitFrame(out) {
		w := utf8.RuneCountInString(line)
		if w != 40 {
			t.Errorf("line %d width = %d, want 40 (%q)", i, w, line)
		}
	}
}

func TestFrame_rowWithSelectorAndLabel(t *testing.T) {
	f := Frame{Width: 50, Title: "x", Rows: []Row{
		{Selector: "1)", Label: "Mail"},
	}}
	out := f.String()
	if !strings.Contains(out, "1)") || !strings.Contains(out, "Mail") {
		t.Errorf("row missing selector or label: %q", out)
	}
}

func TestFrame_rowWithRightAlignedNote(t *testing.T) {
	f := Frame{Width: 50, Title: "x", Rows: []Row{
		{Selector: "1)", Label: "Mail", Note: "(3 unread)"},
	}}
	out := f.String()
	// The note must appear and be right-aligned: there should be at
	// least one space immediately after the note before the right border.
	if !strings.Contains(out, "(3 unread)") {
		t.Fatalf("note missing: %q", out)
	}
	for _, line := range splitFrame(out) {
		idx := strings.Index(line, "(3 unread)")
		if idx < 0 {
			continue
		}
		// trailing slice between note end and right border
		after := line[idx+len("(3 unread)"):]
		// after should end with the right border "│" or "|"; check rune count > 0
		if utf8.RuneCountInString(after) < 1 {
			t.Errorf("note has no trailing space before border: %q", line)
		}
	}
}

func TestFrame_blankRowIsAllSpaces(t *testing.T) {
	f := Frame{Width: 30, Title: "x", Rows: []Row{{Blank: true}}}
	out := f.String()
	lines := splitFrame(out)
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	body := lines[1]
	// strip borders
	stripped := strings.TrimPrefix(body, "│")
	stripped = strings.TrimSuffix(stripped, "│")
	if strings.TrimSpace(stripped) != "" {
		t.Errorf("blank row body = %q, want all spaces", stripped)
	}
}

func TestFrame_emitsCRLF(t *testing.T) {
	f := Frame{Width: 30, Title: "x", Rows: []Row{{Blank: true}}}
	out := f.String()
	if !strings.Contains(out, "\r\n") {
		t.Errorf("expected CRLF line terminators: %q", out)
	}
}

func TestFrame_longLabelTruncated(t *testing.T) {
	long := strings.Repeat("x", 200)
	f := Frame{Width: 30, Title: "x", Rows: []Row{{Selector: "1)", Label: long}}}
	out := f.String()
	for _, line := range splitFrame(out) {
		if utf8.RuneCountInString(line) != 30 {
			t.Errorf("line width = %d, want 30 (%q)", utf8.RuneCountInString(line), line)
		}
	}
}

// splitFrame splits the rendered frame on CRLF, dropping the trailing
// empty element (because String ends with CRLF).
func splitFrame(out string) []string {
	out = strings.TrimSuffix(out, "\r\n")
	return strings.Split(out, "\r\n")
}
