// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package term

import (
	"testing"
)

func TestScanProbeReplies_PrimaryDA(t *testing.T) {
	cases := []string{
		"\x1B[?1;2c",
		"\x1B[?62;c",
		"prefix\x1B[?6c suffix",
	}
	for _, c := range cases {
		var h DetectHints
		ScanProbeReplies([]byte(c), &h)
		if !h.ANSICapable {
			t.Errorf("ScanProbeReplies(%q): ANSICapable = false, want true", c)
		}
	}
}

func TestScanProbeReplies_SecondaryDA(t *testing.T) {
	cases := []struct {
		in       string
		wantType int
	}{
		{"\x1B[>0;115;0c", 0},
		{"\x1B[>1;10;0c", 1},
		{"\x1B[>41;384;0c", 41},
		{"prefix\x1B[>82;20710;0c", 82},
		{"\x1B[>84;0;0c", 84},
		{"\x1B[>c", 0}, // empty params
	}
	for _, c := range cases {
		var h DetectHints
		ScanProbeReplies([]byte(c.in), &h)
		if h.SecondaryDAType != c.wantType {
			t.Errorf("ScanProbeReplies(%q): SecondaryDAType = %d, want %d",
				c.in, h.SecondaryDAType, c.wantType)
		}
		if !h.ANSICapable {
			t.Errorf("ScanProbeReplies(%q): ANSICapable = false, want true", c.in)
		}
	}
}

func TestScanProbeReplies_XTVersion(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"\x1BP>|xterm(384)\x1B\\", "xterm(384)"},
		{"\x1BP>|kitty 0.35.2\x07", "kitty 0.35.2"},
		{"\x1BP>|WezTerm 20240814-114907-3b2eba32\x1B\\", "WezTerm 20240814-114907-3b2eba32"},
		{"\x1BP>|tmux 3.4\x1B\\", "tmux 3.4"},
		{"\x1BP>| iTerm2 3.5.0\x1B\\", "iTerm2 3.5.0"}, // leading space stripped
	}
	for _, c := range cases {
		var h DetectHints
		ScanProbeReplies([]byte(c.in), &h)
		if h.XTVersion != c.want {
			t.Errorf("ScanProbeReplies(%q): XTVersion = %q, want %q",
				c.in, h.XTVersion, c.want)
		}
	}
}

func TestScanProbeReplies_WindowSize(t *testing.T) {
	cases := []struct {
		in           string
		wantR, wantC int
	}{
		{"\x1B[8;24;80t", 24, 80},
		{"\x1B[8;30;100t", 30, 100},
		{"prefix\x1B[8;50;132t suffix", 50, 132},
	}
	for _, c := range cases {
		var h DetectHints
		ScanProbeReplies([]byte(c.in), &h)
		if h.WinRows != c.wantR || h.WinCols != c.wantC {
			t.Errorf("ScanProbeReplies(%q): WinRows=%d WinCols=%d, want %d/%d",
				c.in, h.WinRows, h.WinCols, c.wantR, c.wantC)
		}
	}
}

func TestScanProbeReplies_CursorPositionReport(t *testing.T) {
	cases := []struct {
		in           string
		wantR, wantC int
	}{
		{"\x1B[24;80R", 24, 80},
		{"prefix\x1B[50;132R", 50, 132},
		{"\x1B[1;1R", 1, 1},
	}
	for _, c := range cases {
		var h DetectHints
		ScanProbeReplies([]byte(c.in), &h)
		if h.CursorRows != c.wantR || h.CursorCols != c.wantC {
			t.Errorf("ScanProbeReplies(%q): CursorRows=%d CursorCols=%d, want %d/%d",
				c.in, h.CursorRows, h.CursorCols, c.wantR, c.wantC)
		}
	}
}

func TestScanProbeReplies_AllAtOnce(t *testing.T) {
	// A real cooked-mode terminal's response to AllProbes might arrive
	// interleaved like this. ScanProbeReplies must extract every field
	// in one pass.
	buf := []byte("\x1B[?62;1;2c" +
		"\x1B[>41;384;0c" +
		"\x1BP>|xterm(384)\x1B\\" +
		"\x1B[8;30;100t" +
		"\x1B[30;100R" +
		"\r\n")
	var h DetectHints
	ScanProbeReplies(buf, &h)
	if !h.ANSICapable {
		t.Errorf("ANSICapable not set")
	}
	if h.SecondaryDAType != 41 {
		t.Errorf("SecondaryDAType = %d, want 41", h.SecondaryDAType)
	}
	if h.XTVersion != "xterm(384)" {
		t.Errorf("XTVersion = %q, want xterm(384)", h.XTVersion)
	}
	if h.WinRows != 30 || h.WinCols != 100 {
		t.Errorf("WinSize = %dx%d, want 30x100", h.WinRows, h.WinCols)
	}
	if h.CursorRows != 30 || h.CursorCols != 100 {
		t.Errorf("CursorPos = %dx%d, want 30x100", h.CursorRows, h.CursorCols)
	}
}

func TestScanProbeReplies_FirstWins(t *testing.T) {
	// A second call merging into the same hints must NOT overwrite
	// already-populated fields. This matters at login, where the press-
	// enter read may capture replies that a previous short-drain read
	// already captured.
	h := DetectHints{
		SecondaryDAType: 41,
		XTVersion:       "xterm(384)",
		WinRows:         30, WinCols: 100,
		CursorRows: 30, CursorCols: 100,
	}
	ScanProbeReplies([]byte(
		"\x1B[>1;100;0c"+
			"\x1BP>|kitty 0.99\x1B\\"+
			"\x1B[8;50;132t"+
			"\x1B[50;132R",
	), &h)
	if h.SecondaryDAType != 41 {
		t.Errorf("SecondaryDAType overwritten: got %d", h.SecondaryDAType)
	}
	if h.XTVersion != "xterm(384)" {
		t.Errorf("XTVersion overwritten: got %q", h.XTVersion)
	}
	if h.WinRows != 30 || h.WinCols != 100 {
		t.Errorf("WinSize overwritten: got %dx%d", h.WinRows, h.WinCols)
	}
	if h.CursorRows != 30 || h.CursorCols != 100 {
		t.Errorf("CursorPos overwritten: got %dx%d", h.CursorRows, h.CursorCols)
	}
}

func TestScanProbeReplies_HandlesGarbage(t *testing.T) {
	// Stray ESC, unterminated CSI, mixed user text — must not panic
	// and must extract what it can.
	cases := []string{
		"",
		"\x1B",
		"\x1B[",
		"\x1B[incomplete",
		"plain text only",
		"\x1B(B" + "\x1B[24;80R", // designator + valid CPR
	}
	for _, c := range cases {
		var h DetectHints
		ScanProbeReplies([]byte(c), &h)
	}
}

func TestResolveTermType_Precedence(t *testing.T) {
	cases := []struct {
		name string
		h    DetectHints
		want string
	}{
		{"TTYPE wins over XTVersion and SecondaryDA",
			DetectHints{TermType: "vt100", XTVersion: "xterm(384)", SecondaryDAType: 41},
			"vt100"},
		{"XTVersion wins over SecondaryDA",
			DetectHints{XTVersion: "xterm(384)", SecondaryDAType: 1},
			"xterm"},
		{"SecondaryDA used when nothing else",
			DetectHints{SecondaryDAType: 41},
			"xterm"},
		{"All empty → empty",
			DetectHints{},
			""},
		{"XTVersion unknown family → raw string",
			DetectHints{XTVersion: "some-future-terminal 1.0"},
			"some-future-terminal 1.0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.h.ResolveTermType()
			if got != c.want {
				t.Errorf("ResolveTermType = %q, want %q", got, c.want)
			}
		})
	}
}

func TestResolveSize_Precedence(t *testing.T) {
	cases := []struct {
		name         string
		h            DetectHints
		wantW, wantH int
	}{
		{"NAWS wins over CSI 18 t and CPR",
			DetectHints{NAWSWidth: 132, NAWSHeight: 50, WinCols: 80, WinRows: 24, CursorCols: 100, CursorRows: 30},
			132, 50},
		{"CSI 18 t wins over CPR",
			DetectHints{WinCols: 80, WinRows: 24, CursorCols: 100, CursorRows: 30},
			80, 24},
		{"CPR used when nothing else",
			DetectHints{CursorCols: 100, CursorRows: 30},
			100, 30},
		{"All empty → zeros",
			DetectHints{},
			0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.h.ResolveWidth(); got != c.wantW {
				t.Errorf("ResolveWidth = %d, want %d", got, c.wantW)
			}
			if got := c.h.ResolveHeight(); got != c.wantH {
				t.Errorf("ResolveHeight = %d, want %d", got, c.wantH)
			}
		})
	}
}

func TestAutoDetect_UsesAllSources(t *testing.T) {
	// A non-telnet client with no TTYPE/NAWS but ANSI probes for
	// xterm-family + 100x30 size should produce the right caps.
	h := DetectHints{
		ANSICapable:     true,
		SecondaryDAType: 41,
		XTVersion:       "xterm(384)",
		WinRows:         30, WinCols: 100,
	}
	got := AutoDetect(h)
	if got.TermType != "xterm" {
		t.Errorf("TermType = %q, want xterm", got.TermType)
	}
	if got.Encoding != EncodingUTF8 {
		t.Errorf("Encoding = %v, want UTF-8", got.Encoding)
	}
	if got.Width != 100 || got.Height != 30 {
		t.Errorf("size = %dx%d, want 100x30", got.Width, got.Height)
	}
	if !got.ANSI {
		t.Errorf("ANSI = false, want true")
	}
}

func TestAutoDetect_TTYPEWinsOverProbes(t *testing.T) {
	// A telnet client with TTYPE=vt100 but Secondary DA replies with
	// type 41 (xterm) — TTYPE wins. This is the acceptance criterion
	// from the milestone doc.
	h := DetectHints{
		Telnet:          true,
		TermType:        "vt100",
		SecondaryDAType: 41,
		ANSICapable:     true,
	}
	got := AutoDetect(h)
	if got.TermType != "vt100" {
		t.Errorf("TermType = %q, want vt100 (TTYPE > Secondary DA)", got.TermType)
	}
}

func TestAutoDetectDefaults(t *testing.T) {
	cases := []struct {
		name       string
		hints      DetectHints
		wantEnc    Encoding
		wantWidth  int
		wantColor  bool
		wantDEC    bool
		wantTelnet bool
		wantANSI   bool
	}{
		{
			name:      "nothing detected → ASCII",
			hints:     DetectHints{},
			wantEnc:   EncodingASCII,
			wantWidth: 40,
			wantColor: false,
			wantDEC:   false,
			wantANSI:  false,
		},
		{
			name:      "ANSI capable → UTF-8",
			hints:     DetectHints{ANSICapable: true},
			wantEnc:   EncodingUTF8,
			wantWidth: 80,
			wantColor: true,
			wantDEC:   false,
			wantANSI:  true,
		},
		{
			name:      "TTYPE xterm → UTF-8",
			hints:     DetectHints{TermType: "xterm-256color"},
			wantEnc:   EncodingUTF8,
			wantWidth: 80,
			wantColor: true,
			wantDEC:   false,
			wantANSI:  false,
		},
		{
			name:      "TTYPE c64 → PETSCII",
			hints:     DetectHints{TermType: "c64"},
			wantEnc:   EncodingPETSCII,
			wantWidth: 40,
			wantColor: true,
			wantDEC:   false,
			wantANSI:  false,
		},
		{
			name:      "TTYPE syncterm → CP437",
			hints:     DetectHints{TermType: "syncterm"},
			wantEnc:   EncodingCP437,
			wantWidth: 80,
			wantColor: true,
			wantDEC:   false,
			wantANSI:  false,
		},
		{
			name:       "Telnet + NAWS overrides default size",
			hints:      DetectHints{Telnet: true, NAWSWidth: 132, NAWSHeight: 50, ANSICapable: true},
			wantEnc:    EncodingUTF8,
			wantWidth:  132,
			wantColor:  true,
			wantDEC:    false,
			wantTelnet: true,
			wantANSI:   true,
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
			if got.ANSI != c.wantANSI {
				t.Errorf("ANSI = %v, want %v", got.ANSI, c.wantANSI)
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
		// A malformed ESC[? prefix should not prevent recognition of a
		// valid DA response that appears later in the buffer.
		{"\x1B[?X\x1B[?1;2c", 10},
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
	if !contains(out, "U - UNICODE [MODERN, DEFAULT]") {
		t.Errorf("expected UNICODE to be marked default; got: %s", out)
	}
	out = RenderPromptOnly(EncodingPETSCII)
	if !contains(out, "P - PETSCII [DEFAULT]") {
		t.Errorf("expected PETSCII to be marked default; got: %s", out)
	}
	if contains(out, "UNICODE [MODERN, DEFAULT]") {
		t.Errorf("UNICODE should not have default marker when PETSCII is default; got: %s", out)
	}
}

func TestRenderPromptIsUppercaseASCIIOnly(t *testing.T) {
	// The prompt has to be safe for a PETSCII client in its default
	// (uppercase / graphics) mode, where lowercase ASCII positions
	// (0x61-0x7A) render as graphics rather than letters. Verify the
	// prompt contains no lowercase letters.
	out := RenderPrompt(EncodingUTF8)
	for _, b := range []byte(out) {
		if b >= 'a' && b <= 'z' {
			t.Errorf("RenderPrompt contains lowercase byte 0x%02X in: %s", b, out)
			return
		}
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
		// Each of the six options must be accepted in BOTH cases.
		{"u", EncodingUTF8, true}, {"U", EncodingUTF8, true},
		{"d", EncodingCP437, true}, {"D", EncodingCP437, true},
		{"m", EncodingMacRoman, true}, {"M", EncodingMacRoman, true},
		{"l", EncodingISO88591, true}, {"L", EncodingISO88591, true},
		{"p", EncodingPETSCII, true}, {"P", EncodingPETSCII, true},
		{"a", EncodingASCII, true}, {"A", EncodingASCII, true},
		// Trailing CR / whitespace are stripped.
		{"u\r", EncodingUTF8, true},
		{" u ", EncodingUTF8, true},
		// First-letter rule still applies for longer inputs.
		{"unicode", EncodingUTF8, true},
		{"PETSCII", EncodingPETSCII, true},
		{"ascii", EncodingASCII, true},
		// Garbage falls back to default with ok=false so the caller re-prompts.
		{"X", def, false},
		{"123", def, false},
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
