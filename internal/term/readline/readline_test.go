// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package readline

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/vaelen/wintermute/internal/term"
)

func runReadLine(t *testing.T, input string, hist *History) (string, []byte, error) {
	t.Helper()
	caps := term.Capabilities{Encoding: term.EncodingUTF8, ANSI: true}
	enc := term.Open(caps)
	br := bufio.NewReader(strings.NewReader(input))
	var w bytes.Buffer
	got, err := ReadLine(br, &w, caps, enc, hist)
	return got, w.Bytes(), err
}

func TestReadLinePlainEnter(t *testing.T) {
	got, out, err := runReadLine(t, "hello\r\n", nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != "hello" {
		t.Errorf("got = %q, want %q", got, "hello")
	}
	// On Enter, the editor should emit a trailing CRLF.
	if !bytes.HasSuffix(out, []byte("\r\n")) {
		t.Errorf("output should end with CRLF, got: % X", out)
	}
}

func TestReadLineLeftArrowInsert(t *testing.T) {
	// Type "abcd", Left, Left, "X", Enter — expect "abXcd".
	input := "abcd\x1B[D\x1B[DX\r\n"
	got, out, err := runReadLine(t, input, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != "abXcd" {
		t.Errorf("got = %q, want abXcd", got)
	}
	// At some point during the editing the wire must contain "abXcd"
	// (the redraw after inserting 'X').
	if !bytes.Contains(out, []byte("abXcd")) {
		t.Errorf("expected 'abXcd' redraw in output:\n% X", out)
	}
}

func TestReadLineHomeEnd(t *testing.T) {
	// Type "abc", Home, "X" → "Xabc"; End, "Y", Enter → "XabcY".
	input := "abc\x1B[HX\x1B[FY\r\n"
	got, _, err := runReadLine(t, input, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != "XabcY" {
		t.Errorf("got = %q, want XabcY", got)
	}
}

func TestReadLineBackspaceAndDelete(t *testing.T) {
	// Type "abcd", Backspace → "abc"; Left, Delete → "ab"; Enter.
	input := "abcd\x08\x1B[D\x1B[3~\r\n"
	got, _, err := runReadLine(t, input, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != "ab" {
		t.Errorf("got = %q, want ab", got)
	}
}

func TestReadLineCtrlKKillToEnd(t *testing.T) {
	// Type "abcdef", Home, Right, Right, Ctrl-K → "ab"; Enter.
	input := "abcdef\x1B[H\x1B[C\x1B[C\x0B\r\n"
	got, _, err := runReadLine(t, input, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != "ab" {
		t.Errorf("got = %q, want ab", got)
	}
}

func TestReadLineCtrlUKillToStart(t *testing.T) {
	// Type "abcdef", Home, Right, Right, Ctrl-U → "cdef"; Enter.
	input := "abcdef\x1B[H\x1B[C\x1B[C\x15\r\n"
	got, _, err := runReadLine(t, input, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != "cdef" {
		t.Errorf("got = %q, want cdef", got)
	}
}

func TestReadLineCtrlWKillWord(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"the quick brown fox\x17\r\n", "the quick brown "},  // delete "fox"
		{"the quick brown \x17\r\n", "the quick "},           // trailing space + word
		{"single\x17\r\n", ""},                                // delete entire word
		{"\x17\r\n", ""},                                      // nothing to delete
	}
	for _, c := range cases {
		got, _, err := runReadLine(t, c.in, nil)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if got != c.want {
			t.Errorf("input %q got %q, want %q", c.in, got, c.want)
		}
	}
}

func TestReadLineCtrlCInterrupt(t *testing.T) {
	got, out, err := runReadLine(t, "abc\x03", nil)
	if !errors.Is(err, ErrInterrupt) {
		t.Errorf("err = %v, want ErrInterrupt", err)
	}
	if got != "" {
		t.Errorf("got = %q, want empty on interrupt", got)
	}
	if !bytes.Contains(out, []byte("^C\r\n")) {
		t.Errorf("output should contain ^C\\r\\n, got: % X", out)
	}
}

func TestReadLineCtrlDEmptyEOF(t *testing.T) {
	_, _, err := runReadLine(t, "\x04", nil)
	if !errors.Is(err, io.EOF) {
		t.Errorf("err = %v, want io.EOF", err)
	}
}

func TestReadLineCtrlDNonEmptyIgnored(t *testing.T) {
	// Ctrl-D over a non-empty buffer is ignored, then Enter submits.
	got, _, err := runReadLine(t, "abc\x04\r\n", nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != "abc" {
		t.Errorf("got = %q, want abc", got)
	}
}

func TestReadLineHistoryUpDown(t *testing.T) {
	h := NewHistory(10)
	h.Add("look")
	h.Add("north")

	// Pressing Up twice walks to "look"; Down walks back to "north";
	// Down again returns to the empty draft.
	input := "\x1B[A\x1B[A\x1B[B\x1B[B\r\n"
	got, _, err := runReadLine(t, input, h)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != "" {
		t.Errorf("after Up Up Down Down on empty draft, got %q, want empty", got)
	}
}

func TestReadLineHistoryPreservesDraft(t *testing.T) {
	h := NewHistory(10)
	h.Add("look")

	// Type "wip", press Up (recalls "look"), press Down (restores
	// "wip"), then Enter.
	input := "wip\x1B[A\x1B[B\r\n"
	got, _, err := runReadLine(t, input, h)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != "wip" {
		t.Errorf("got = %q, want wip", got)
	}
}

func TestReadLineEmitsCursorMovement(t *testing.T) {
	// After typing "abcd" and pressing Left, the wire must contain
	// "\x1B[1D" (the left-arrow movement).
	_, out, err := runReadLine(t, "abcd\x1B[D\r\n", nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !bytes.Contains(out, []byte("\x1B[1D")) {
		t.Errorf("expected left-cursor CSI in wire bytes: % X", out)
	}
}

func TestReadLineEraseToEndOnShrink(t *testing.T) {
	// Type "abcde", Backspace — the redraw must emit \x1B[K so the
	// trailing 'e' on screen is cleared.
	_, out, err := runReadLine(t, "abcde\x08\r\n", nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !bytes.Contains(out, []byte("\x1B[K")) {
		t.Errorf("expected erase-to-end CSI in wire bytes: % X", out)
	}
}

func TestReadLineHistoryAddNotCalledByEditor(t *testing.T) {
	// The editor itself must not auto-add submitted lines to history;
	// that is the caller's responsibility. After Enter, Len stays 0.
	h := NewHistory(10)
	if _, _, err := runReadLine(t, "hello\r\n", h); err != nil {
		t.Fatalf("err = %v", err)
	}
	if h.Len() != 0 {
		t.Errorf("History.Len = %d, want 0 (editor does not Add)", h.Len())
	}
}
