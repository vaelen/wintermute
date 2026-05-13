// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package term

import (
	"bytes"
	"testing"
)

func encode(t *testing.T, caps Capabilities, in string) []byte {
	t.Helper()
	enc := Open(caps)
	out, err := enc.EncodeOut([]byte(in))
	if err != nil {
		t.Fatalf("EncodeOut(%q): %v", in, err)
	}
	if fin := enc.FinalizeOut(); len(fin) > 0 {
		out = append(out, fin...)
	}
	return out
}

func TestUTF8PassThrough(t *testing.T) {
	got := encode(t, Capabilities{Encoding: EncodingUTF8, Color: true}, "Hello, 世界\n")
	want := "Hello, 世界\n"
	if string(got) != want {
		t.Errorf("UTF-8 pass-through: got %q, want %q", got, want)
	}
}

func TestCP437BoxDrawing(t *testing.T) {
	got := encode(t, Capabilities{Encoding: EncodingCP437}, "┌─┐")
	want := []byte{0xDA, 0xC4, 0xBF}
	if !bytes.Equal(got, want) {
		t.Errorf("CP437 box: got % X, want % X", got, want)
	}
}

func TestCP437DoubleLine(t *testing.T) {
	got := encode(t, Capabilities{Encoding: EncodingCP437}, "╔═╗")
	want := []byte{0xC9, 0xCD, 0xBB}
	if !bytes.Equal(got, want) {
		t.Errorf("CP437 double-line: got % X, want % X", got, want)
	}
}

func TestISO88591WithDEC(t *testing.T) {
	got := encode(t, Capabilities{Encoding: EncodingISO88591, DECLineDrawing: true}, "┌─┐")
	want := []byte("\x1B(0lqk\x1B(B")
	if !bytes.Equal(got, want) {
		t.Errorf("ISO-8859-1 DEC: got %q, want %q", got, want)
	}
}

func TestISO88591WithoutDEC(t *testing.T) {
	got := encode(t, Capabilities{Encoding: EncodingISO88591, DECLineDrawing: false}, "┌─┐")
	want := []byte("+-+")
	if !bytes.Equal(got, want) {
		t.Errorf("ISO-8859-1 no DEC: got %q, want %q", got, want)
	}
}

func TestDoubleLineDowngradeInLatin1(t *testing.T) {
	// Double-line should normalize to single-line, then go through DEC.
	got := encode(t, Capabilities{Encoding: EncodingISO88591, DECLineDrawing: true}, "╔═╗")
	want := []byte("\x1B(0lqk\x1B(B")
	if !bytes.Equal(got, want) {
		t.Errorf("Latin-1 double-line: got %q, want %q", got, want)
	}
}

func TestDECBatching(t *testing.T) {
	got := encode(t, Capabilities{Encoding: EncodingISO88591, DECLineDrawing: true}, "─────────")
	// One ESC(0, nine 'q', one ESC(B.
	want := []byte("\x1B(0qqqqqqqqq\x1B(B")
	if !bytes.Equal(got, want) {
		t.Errorf("DEC batching: got %q, want %q", got, want)
	}
}

func TestDECTextInterleaved(t *testing.T) {
	// Drawing → text → drawing should produce two bracket pairs.
	got := encode(t, Capabilities{Encoding: EncodingISO88591, DECLineDrawing: true}, "─X─")
	want := []byte("\x1B(0q\x1B(BX\x1B(0q\x1B(B")
	if !bytes.Equal(got, want) {
		t.Errorf("DEC interleave: got %q, want %q", got, want)
	}
}

func TestDECStateAcrossCalls(t *testing.T) {
	enc := Open(Capabilities{Encoding: EncodingISO88591, DECLineDrawing: true})
	out1, _ := enc.EncodeOut([]byte("─"))
	out2, _ := enc.EncodeOut([]byte("─"))
	out3, _ := enc.EncodeOut([]byte("─"))
	fin := enc.FinalizeOut()
	got := append(append(append(out1, out2...), out3...), fin...)
	// Open once, three 'q' bytes split across calls, close once.
	want := []byte("\x1B(0qqq\x1B(B")
	if !bytes.Equal(got, want) {
		t.Errorf("DEC across calls: got %q, want %q", got, want)
	}
}

func TestFinalizeOutClosesGraphics(t *testing.T) {
	enc := Open(Capabilities{Encoding: EncodingISO88591, DECLineDrawing: true})
	out, _ := enc.EncodeOut([]byte("─"))
	if bytes.Contains(out, []byte("\x1B(B")) {
		t.Errorf("EncodeOut should not pre-close graphics; got %q", out)
	}
	fin := enc.FinalizeOut()
	if string(fin) != "\x1B(B" {
		t.Errorf("FinalizeOut: got %q, want ESC(B", fin)
	}
	// Second FinalizeOut should be a no-op.
	if got := enc.FinalizeOut(); len(got) != 0 {
		t.Errorf("FinalizeOut second call: got %q, want empty", got)
	}
}

func TestASCIIStripsANSI(t *testing.T) {
	got := encode(t, Capabilities{Encoding: EncodingASCII}, "\x1B[31mred\x1B[0m text")
	want := []byte("red text")
	if !bytes.Equal(got, want) {
		t.Errorf("ASCII strip ANSI: got %q, want %q", got, want)
	}
}

func TestASCIIBoxDrawing(t *testing.T) {
	got := encode(t, Capabilities{Encoding: EncodingASCII}, "┌─┐\n│ │\n└─┘")
	want := []byte("+-+\n| |\n+-+")
	if !bytes.Equal(got, want) {
		t.Errorf("ASCII box: got %q, want %q", got, want)
	}
}

func TestASCIIShadeBlocksBecomeSpace(t *testing.T) {
	got := encode(t, Capabilities{Encoding: EncodingASCII}, "░▒▓█")
	want := []byte("    ")
	if !bytes.Equal(got, want) {
		t.Errorf("ASCII shade → space: got %q, want %q", got, want)
	}
}

func TestASCIIHighByteBecomesQuestion(t *testing.T) {
	got := encode(t, Capabilities{Encoding: EncodingASCII}, "café")
	want := []byte("caf?")
	if !bytes.Equal(got, want) {
		t.Errorf("ASCII high-byte: got %q, want %q", got, want)
	}
}

func TestPETSCIILetterCaseSwap(t *testing.T) {
	got := encode(t, Capabilities{Encoding: EncodingPETSCII}, "Hello")
	// 'H' (0x48) → 0x68; 'e' (0x65) → 0x45; 'l' (0x6C) → 0x4C; 'o' (0x6F) → 0x4F
	want := []byte{0x68, 0x45, 0x4C, 0x4C, 0x4F}
	if !bytes.Equal(got, want) {
		t.Errorf("PETSCII case-swap: got % X, want % X", got, want)
	}
}

func TestPETSCIIColorFromANSI(t *testing.T) {
	got := encode(t, Capabilities{Encoding: EncodingPETSCII, Color: true}, "\x1B[31m\x1B[0m")
	want := []byte{petsciiColorRed, petsciiColorWhite}
	if !bytes.Equal(got, want) {
		t.Errorf("PETSCII color: got % X, want % X", got, want)
	}
}

func TestPETSCIIColorDisabledDropsSGR(t *testing.T) {
	got := encode(t, Capabilities{Encoding: EncodingPETSCII, Color: false}, "\x1B[31mRED\x1B[0m")
	// SGR dropped; 'R'/'E'/'D' case-swap.
	want := []byte{petsciiFromASCIILetter('R'), petsciiFromASCIILetter('E'), petsciiFromASCIILetter('D')}
	if !bytes.Equal(got, want) {
		t.Errorf("PETSCII color off: got % X, want % X", got, want)
	}
}

func TestPETSCIIBoxDrawing(t *testing.T) {
	got := encode(t, Capabilities{Encoding: EncodingPETSCII}, "─│┌")
	want := []byte{petsciiDrawing['─'], petsciiDrawing['│'], petsciiDrawing['┌']}
	if !bytes.Equal(got, want) {
		t.Errorf("PETSCII box: got % X, want % X", got, want)
	}
}

func TestColorDisabledStripsSGR(t *testing.T) {
	got := encode(t, Capabilities{Encoding: EncodingUTF8, Color: false}, "\x1B[31mred\x1B[0m")
	want := []byte("red")
	if !bytes.Equal(got, want) {
		t.Errorf("color off should strip SGR: got %q, want %q", got, want)
	}
}

func TestColorEnabledPassesSGR(t *testing.T) {
	got := encode(t, Capabilities{Encoding: EncodingUTF8, Color: true}, "\x1B[31mred\x1B[0m")
	want := []byte("\x1B[31mred\x1B[0m")
	if !bytes.Equal(got, want) {
		t.Errorf("color on should pass SGR: got %q, want %q", got, want)
	}
}

func TestDecodeInUTF8(t *testing.T) {
	enc := Open(Capabilities{Encoding: EncodingUTF8})
	got, err := enc.DecodeIn([]byte("Hello"))
	if err != nil || string(got) != "Hello" {
		t.Errorf("DecodeIn UTF-8: got %q, err %v", got, err)
	}
}

func TestDecodeInCP437(t *testing.T) {
	enc := Open(Capabilities{Encoding: EncodingCP437})
	got, err := enc.DecodeIn([]byte{0xC4}) // ─
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "─" {
		t.Errorf("DecodeIn CP437: got %q, want ─", got)
	}
}

func TestDecodeInPETSCIICaseSwap(t *testing.T) {
	enc := Open(Capabilities{Encoding: EncodingPETSCII})
	// Bytes typed on a C64 in mixed-case mode: 0x68 displays as 'H'.
	got, err := enc.DecodeIn([]byte{0x68, 0x45, 0x4C, 0x4C, 0x4F, petsciiCR})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "Hello\n" {
		t.Errorf("DecodeIn PETSCII: got %q, want Hello\\n", got)
	}
}

func TestReconfigureClosesGraphics(t *testing.T) {
	enc := Open(Capabilities{Encoding: EncodingISO88591, DECLineDrawing: true})
	_, _ = enc.EncodeOut([]byte("─"))
	closing := enc.Reconfigure(Capabilities{Encoding: EncodingUTF8})
	if string(closing) != "\x1B(B" {
		t.Errorf("Reconfigure should return closing graphics: got %q", closing)
	}
	// Subsequent output should be UTF-8 pass-through, no leftover state.
	out, _ := enc.EncodeOut([]byte("hi"))
	if string(out) != "hi" {
		t.Errorf("after Reconfigure: got %q, want hi", out)
	}
}

func TestEncodingParsing(t *testing.T) {
	cases := []struct {
		in string
		e  Encoding
		ok bool
	}{
		{"utf8", EncodingUTF8, true},
		{"UTF-8", EncodingUTF8, true},
		{"unicode", EncodingUTF8, true},
		{"cp437", EncodingCP437, true},
		{"latin1", EncodingISO88591, true},
		{"iso-8859-1", EncodingISO88591, true},
		{"macroman", EncodingMacRoman, true},
		{"mac", EncodingMacRoman, true},
		{"petscii", EncodingPETSCII, true},
		{"c64", EncodingPETSCII, true},
		{"ascii", EncodingASCII, true},
		{"klingon", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		got, ok := ParseEncoding(c.in)
		if ok != c.ok {
			t.Errorf("ParseEncoding(%q) ok = %v, want %v", c.in, ok, c.ok)
			continue
		}
		if ok && got != c.e {
			t.Errorf("ParseEncoding(%q) = %v, want %v", c.in, got, c.e)
		}
	}
}
