// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package term

import (
	"testing"
)

func TestAutoDetectDefaults(t *testing.T) {
	cases := []struct {
		name        string
		hints       DetectHints
		wantEnc     Encoding
		wantWidth   int
		wantColor   bool
		wantDEC     bool
		wantTelnet  bool
	}{
		{
			name:      "nothing detected → ASCII",
			hints:     DetectHints{},
			wantEnc:   EncodingASCII,
			wantWidth: 40,
			wantColor: false,
			wantDEC:   false,
		},
		{
			name:      "ANSI capable → UTF-8",
			hints:     DetectHints{ANSICapable: true},
			wantEnc:   EncodingUTF8,
			wantWidth: 80,
			wantColor: true,
			wantDEC:   false,
		},
		{
			name:      "TTYPE xterm → UTF-8",
			hints:     DetectHints{TermType: "xterm-256color"},
			wantEnc:   EncodingUTF8,
			wantWidth: 80,
			wantColor: true,
			wantDEC:   false,
		},
		{
			name:      "TTYPE c64 → PETSCII",
			hints:     DetectHints{TermType: "c64"},
			wantEnc:   EncodingPETSCII,
			wantWidth: 40,
			wantColor: true,
			wantDEC:   false,
		},
		{
			name:      "TTYPE syncterm → CP437",
			hints:     DetectHints{TermType: "syncterm"},
			wantEnc:   EncodingCP437,
			wantWidth: 80,
			wantColor: true,
			wantDEC:   false,
		},
		{
			name:       "Telnet + NAWS overrides default size",
			hints:      DetectHints{Telnet: true, NAWSWidth: 132, NAWSHeight: 50, ANSICapable: true},
			wantEnc:    EncodingUTF8,
			wantWidth:  132,
			wantColor:  true,
			wantDEC:    false,
			wantTelnet: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := AutoDetect(c.hints)
			if got.Encoding != c.wantEnc {
				t.Errorf("Encoding = %v, want %v", got.Encoding, c.wantEnc)
			}
			if got.Width != c.wantWidth {
				t.Errorf("Width = %d, want %d", got.Width, c.wantWidth)
			}
			if got.Color != c.wantColor {
				t.Errorf("Color = %v, want %v", got.Color, c.wantColor)
			}
			if got.DECLineDrawing != c.wantDEC {
				t.Errorf("DECLineDrawing = %v, want %v", got.DECLineDrawing, c.wantDEC)
			}
			if got.Telnet != c.wantTelnet {
				t.Errorf("Telnet = %v, want %v", got.Telnet, c.wantTelnet)
			}
		})
	}
}

func TestEncodingDefaultsForLatin1AndMacRoman(t *testing.T) {
	// These specifically default DEC line drawing ON.
	got := AutoDetect(DetectHints{TermType: "vt100"})
	got.Encoding = EncodingISO88591
	got = got.ApplyEncodingDefaults()
	if !got.DECLineDrawing {
		t.Errorf("ISO-8859-1 DEC default = false, want true")
	}
	if !got.Color {
		t.Errorf("ISO-8859-1 Color default = false, want true")
	}

	got2 := Capabilities{Encoding: EncodingMacRoman}.ApplyEncodingDefaults()
	if !got2.DECLineDrawing {
		t.Errorf("MacRoman DEC default = false, want true")
	}
}

func TestFindDAResponseEnd(t *testing.T) {
	cases := []struct {
		buf  string
		want int
	}{
		{"\x1B[?62;c", 6},
		{"prefix\x1B[?1;2;6c", 14},
		{"no response here", -1},
		{"\x1B[?incomplete", -1}, // non-digit char after ? breaks the match
		{"\x1B[c", -1},           // missing '?'
		{"", -1},
	}
	for _, c := range cases {
		got := findDAResponseEnd([]byte(c.buf))
		if got != c.want {
			t.Errorf("findDAResponseEnd(%q) = %d, want %d", c.buf, got, c.want)
		}
	}
}

func TestRenderPromptDefaultMarker(t *testing.T) {
	out := RenderPromptOnly(EncodingUTF8)
	if !contains(out, "U - Unicode [modern, default]") {
		t.Errorf("expected Unicode to be marked default; got: %s", out)
	}
	out = RenderPromptOnly(EncodingPETSCII)
	if !contains(out, "P - PETSCII [default]") {
		t.Errorf("expected PETSCII to be marked default; got: %s", out)
	}
	if contains(out, "Unicode [modern, default]") {
		t.Errorf("Unicode should not have default marker when PETSCII is default; got: %s", out)
	}
}

func TestParsePromptResponse(t *testing.T) {
	def := EncodingUTF8
	cases := []struct {
		in   string
		want Encoding
		ok   bool
	}{
		{"", def, true},
		{"\n", def, true},
		{"u", EncodingUTF8, true},
		{"U", EncodingUTF8, true},
		{"d", EncodingCP437, true},
		{"M", EncodingMacRoman, true},
		{"L", EncodingISO88591, true},
		{"P", EncodingPETSCII, true},
		{"A", EncodingASCII, true},
		{"X", def, false},
		{"unicode", EncodingUTF8, true}, // first letter wins
	}
	for _, c := range cases {
		got, ok := ParsePromptResponse(c.in, def)
		if got != c.want || ok != c.ok {
			t.Errorf("Parse(%q) = (%v, %v), want (%v, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
