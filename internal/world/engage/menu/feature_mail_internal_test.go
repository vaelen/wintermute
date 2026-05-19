// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package menu

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestSplitBodyLines_chunksByRunes verifies that splitBodyLines treats
// `max` as a rune count and never splits a UTF-8 codepoint mid-stream.
// A naive byte slice would produce invalid UTF-8 on input like "♥♥♥♥…".
func TestSplitBodyLines_chunksByRunes(t *testing.T) {
	body := strings.Repeat("♥", 12) // 12 runes, 36 bytes (3 bytes per ♥)
	got := splitBodyLines(body, 5)
	// Every emitted line must be valid UTF-8 and at most 5 runes long.
	totalRunes := 0
	for i, ln := range got {
		if !utf8.ValidString(ln) {
			t.Errorf("line %d is not valid UTF-8: %q", i, ln)
		}
		if r := utf8.RuneCountInString(ln); r > 5 {
			t.Errorf("line %d has %d runes, want ≤ 5: %q", i, r, ln)
		}
		totalRunes += utf8.RuneCountInString(ln)
	}
	if totalRunes != 12 {
		t.Errorf("total runes across lines = %d, want 12", totalRunes)
	}
}

func TestSplitBodyLines_asciiWrapByRune(t *testing.T) {
	body := strings.Repeat("x", 11)
	got := splitBodyLines(body, 5)
	if len(got) != 3 {
		t.Fatalf("got %d lines, want 3: %q", len(got), got)
	}
	if got[0] != "xxxxx" || got[1] != "xxxxx" || got[2] != "x" {
		t.Errorf("lines = %q", got)
	}
}

func TestSplitBodyLines_preservesHardNewlines(t *testing.T) {
	got := splitBodyLines("hello\nworld", 40)
	if len(got) != 2 || got[0] != "hello" || got[1] != "world" {
		t.Errorf("lines = %q", got)
	}
}
