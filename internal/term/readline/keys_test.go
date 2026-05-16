// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package readline

import (
	"bufio"
	"io"
	"strings"
	"testing"

	"github.com/vaelen/wintermute/internal/term"
)

func eventsFrom(t *testing.T, s string, enc *term.Encoder) []Event {
	t.Helper()
	br := bufio.NewReader(strings.NewReader(s))
	var out []Event
	for {
		ev, err := readEvent(br, enc)
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("readEvent: %v", err)
		}
		out = append(out, ev)
	}
}

func TestReadEventControlBytes(t *testing.T) {
	cases := []struct {
		in   string
		want Key
	}{
		{"\r", KeyEnter},
		{"\r\n", KeyEnter},
		{"\n", KeyEnter},
		{"\x01", KeyHome},
		{"\x03", KeyInterrupt},
		{"\x04", KeyEOF},
		{"\x05", KeyEnd},
		{"\x08", KeyBackspace},
		{"\x7F", KeyBackspace},
		{"\x0B", KeyKillToEnd},
		{"\x0C", KeyClear},
		{"\x15", KeyKillToStart},
		{"\x17", KeyKillWord},
		// An unmapped control byte should produce KeyUnknown.
		{"\x07", KeyUnknown},
	}
	for _, c := range cases {
		evs := eventsFrom(t, c.in, nil)
		if len(evs) != 1 || evs[0].Key != c.want {
			t.Errorf("readEvent(%q) = %+v, want one event with Key=%v", c.in, evs, c.want)
		}
	}
}

func TestReadEventCSIArrows(t *testing.T) {
	cases := []struct {
		in   string
		want Key
	}{
		{"\x1B[A", KeyUp},
		{"\x1B[B", KeyDown},
		{"\x1B[C", KeyRight},
		{"\x1B[D", KeyLeft},
		{"\x1B[H", KeyHome},
		{"\x1B[F", KeyEnd},
		{"\x1B[3~", KeyDelete},
		{"\x1B[1~", KeyHome},
		{"\x1B[7~", KeyHome},
		{"\x1B[4~", KeyEnd},
		{"\x1B[8~", KeyEnd},
	}
	for _, c := range cases {
		evs := eventsFrom(t, c.in, nil)
		if len(evs) != 1 || evs[0].Key != c.want {
			t.Errorf("readEvent(% X) = %+v, want one event with Key=%v", c.in, evs, c.want)
		}
	}
}

func TestReadEventSS3Arrows(t *testing.T) {
	cases := []struct {
		in   string
		want Key
	}{
		{"\x1BOA", KeyUp},
		{"\x1BOB", KeyDown},
		{"\x1BOC", KeyRight},
		{"\x1BOD", KeyLeft},
		{"\x1BOH", KeyHome},
		{"\x1BOF", KeyEnd},
	}
	for _, c := range cases {
		evs := eventsFrom(t, c.in, nil)
		if len(evs) != 1 || evs[0].Key != c.want {
			t.Errorf("readEvent(% X) = %+v, want %v", c.in, evs, c.want)
		}
	}
}

func TestReadEventUnknownCSIDiscarded(t *testing.T) {
	// A CSI we don't recognize collapses to KeyUnknown, and then any
	// subsequent plain char decodes normally.
	evs := eventsFrom(t, "\x1B[99Zx", nil)
	if len(evs) != 2 {
		t.Fatalf("got %d events, want 2: %+v", len(evs), evs)
	}
	if evs[0].Key != KeyUnknown {
		t.Errorf("first event = %+v, want KeyUnknown", evs[0])
	}
	if evs[1].Key != KeyChar || evs[1].Rune != 'x' {
		t.Errorf("second event = %+v, want KeyChar 'x'", evs[1])
	}
}

func TestReadEventOverflowingCSIDrainsThroughTerminator(t *testing.T) {
	// A CSI parameter string longer than the defensive cap (32 bytes)
	// must still consume the terminating byte before returning
	// KeyUnknown. Otherwise the next readEvent would read the
	// terminator (an ASCII letter like 'A') as a printable KeyChar
	// and inject a spurious character into the user's buffer.
	overflow := strings.Repeat("1;", 40) // 80 bytes of params, then 'A'
	input := "\x1B[" + overflow + "Ax"
	evs := eventsFrom(t, input, nil)
	if len(evs) != 2 {
		t.Fatalf("got %d events, want 2: %+v", len(evs), evs)
	}
	if evs[0].Key != KeyUnknown {
		t.Errorf("first event = %+v, want KeyUnknown", evs[0])
	}
	if evs[1].Key != KeyChar || evs[1].Rune != 'x' {
		t.Errorf("second event = %+v, want KeyChar 'x' (the 'A' terminator must have been drained)", evs[1])
	}
}

func TestReadEventASCIIChars(t *testing.T) {
	evs := eventsFrom(t, "ab!~", nil)
	wantRunes := []rune{'a', 'b', '!', '~'}
	if len(evs) != len(wantRunes) {
		t.Fatalf("len = %d, want %d", len(evs), len(wantRunes))
	}
	for i, w := range wantRunes {
		if evs[i].Key != KeyChar || evs[i].Rune != w {
			t.Errorf("evs[%d] = %+v, want KeyChar %q", i, evs[i], w)
		}
	}
}

func TestReadEventUTF8MultibyteSplitAcrossReads(t *testing.T) {
	// A "slow reader" that returns one byte per Read call simulates the
	// case where a multibyte rune arrives across multiple network reads.
	enc := term.Open(term.Capabilities{Encoding: term.EncodingUTF8})
	r := &oneByteReader{data: []byte("\xC3\xA9x")} // "é" + 'x'
	br := bufio.NewReaderSize(r, 1)
	ev, err := readEvent(br, enc)
	if err != nil {
		t.Fatalf("readEvent: %v", err)
	}
	if ev.Key != KeyChar || ev.Rune != 'é' {
		t.Errorf("first event = %+v, want KeyChar 'é'", ev)
	}
	ev, err = readEvent(br, enc)
	if err != nil {
		t.Fatalf("readEvent: %v", err)
	}
	if ev.Key != KeyChar || ev.Rune != 'x' {
		t.Errorf("second event = %+v, want KeyChar 'x'", ev)
	}
}

func TestReadEventSingleByteEncodingDecodes(t *testing.T) {
	// CP437 0x80 → "Ç" (U+00C7).
	enc := term.Open(term.Capabilities{Encoding: term.EncodingCP437})
	br := bufio.NewReader(strings.NewReader("\x80"))
	ev, err := readEvent(br, enc)
	if err != nil {
		t.Fatalf("readEvent: %v", err)
	}
	if ev.Key != KeyChar || ev.Rune != 'Ç' {
		t.Errorf("event = %+v, want KeyChar 'Ç'", ev)
	}
}

// oneByteReader is a Reader that returns at most one byte per Read.
// Used to simulate input that arrives split across multiple syscalls,
// so we can verify multibyte UTF-8 reassembly.
type oneByteReader struct {
	data []byte
	pos  int
}

func (r *oneByteReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	p[0] = r.data[r.pos]
	r.pos++
	return 1, nil
}
