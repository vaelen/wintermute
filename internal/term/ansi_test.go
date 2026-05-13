// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package term

import "testing"

func TestStripCSI(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"no esc", "hello world", "hello world"},
		{"only DA response", "\x1B[?1;2c", ""},
		{"DA response then user input", "\x1B[?1;2cu", "u"},
		{"trailing DA", "u\x1B[?1;2c", "u"},
		{"multiple sequences", "a\x1B[31mb\x1B[0mc", "abc"},
		{"unterminated CSI drops tail", "ok\x1B[?1;2", "ok"},
		{"CSI inside word", "fo\x1B[1;1Hod", "food"},
		{"colon and digits OK", "a:b;c", "a:b;c"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := StripCSI(c.in)
			if got != c.want {
				t.Errorf("StripCSI(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestParsePromptResponseDAResponsePrefix(t *testing.T) {
	// Reproduces the bug a user hits when their terminal's Device
	// Attributes auto-response is line-buffered alongside their typed
	// input — the first line on the wire ends up as "\x1B[?1;2cu"
	// instead of just "u". The parser must recognize the 'u'.
	cases := []struct {
		in   string
		want Encoding
	}{
		{"\x1B[?1;2cu", EncodingUTF8},
		{"\x1B[?1;2cp", EncodingPETSCII},
		{"\x1B[?62;1;c", EncodingASCII}, // DA response alone — empty input → default
	}
	for _, c := range cases {
		got, ok := ParsePromptResponse(c.in, EncodingASCII)
		if !ok {
			t.Errorf("Parse(%q): ok=false", c.in)
			continue
		}
		if got != c.want {
			t.Errorf("Parse(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
