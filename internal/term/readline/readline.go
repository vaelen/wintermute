// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package readline

import (
	"bufio"
	"errors"
	"fmt"
	"io"

	"github.com/vaelen/wintermute/internal/term"
)

// ErrInterrupt is returned by ReadLine when the user presses Ctrl-C.
// The current draft is discarded; the caller is expected to start over
// from a fresh prompt.
var ErrInterrupt = errors.New("readline: interrupt")

// ReadLine reads one edited line of input. The caller is expected to
// have already written the prompt to w; ReadLine begins recording from
// what it assumes is the cursor's position immediately after the
// prompt. On Enter the finished line is returned (without the newline)
// and the terminal cursor is left on a fresh line. On Ctrl-C the line
// is canceled, a literal "^C\r\n" is emitted, and ErrInterrupt is
// returned. On Ctrl-D over an empty buffer, io.EOF is returned.
//
// hist may be nil, in which case Up / Down do nothing.
func ReadLine(in *bufio.Reader, w io.Writer, caps term.Capabilities, enc *term.Encoder, hist *History) (string, error) {
	ed := &editor{in: in, w: w, caps: caps, enc: enc, hist: hist}
	return ed.run()
}

// editor holds the state of one in-progress line edit.
//
// runes / cursor are the logical content. prevCursor tracks where the
// physical terminal cursor sits (in columns from the start of the edit
// area) so we know how many CSI movement bytes to emit when we want to
// reposition; prevLen records the rune length of the most-recently
// drawn line so we can size the erase-to-end operation correctly.
type editor struct {
	in   *bufio.Reader
	w    io.Writer
	caps term.Capabilities
	enc  *term.Encoder
	hist *History

	runes      []rune
	cursor     int
	prevCursor int
	prevLen    int
}

func (e *editor) run() (string, error) {
	for {
		ev, err := readEvent(e.in, e.enc)
		if err != nil {
			return "", err
		}
		switch ev.Key {
		case KeyEnter:
			// Park the cursor at end-of-line before the newline so any
			// post-cursor characters are not skipped over by CRLF.
			if err := e.moveTo(len(e.runes)); err != nil {
				return "", err
			}
			if _, err := io.WriteString(e.w, "\r\n"); err != nil {
				return "", err
			}
			line := string(e.runes)
			if e.hist != nil {
				e.hist.Reset()
			}
			return line, nil

		case KeyInterrupt:
			if err := e.moveTo(len(e.runes)); err != nil {
				return "", err
			}
			if _, err := io.WriteString(e.w, "^C\r\n"); err != nil {
				return "", err
			}
			if e.hist != nil {
				e.hist.Reset()
			}
			return "", ErrInterrupt

		case KeyEOF:
			if len(e.runes) == 0 {
				return "", io.EOF
			}
			// Ignore Ctrl-D mid-line for tier-1.

		case KeyChar:
			e.runes = insertRune(e.runes, e.cursor, ev.Rune)
			e.cursor++
			if err := e.redraw(); err != nil {
				return "", err
			}

		case KeyBackspace:
			if e.cursor > 0 {
				e.runes = append(e.runes[:e.cursor-1], e.runes[e.cursor:]...)
				e.cursor--
				if err := e.redraw(); err != nil {
					return "", err
				}
			}

		case KeyDelete:
			if e.cursor < len(e.runes) {
				e.runes = append(e.runes[:e.cursor], e.runes[e.cursor+1:]...)
				if err := e.redraw(); err != nil {
					return "", err
				}
			}

		case KeyLeft:
			if e.cursor > 0 {
				if err := e.moveTo(e.cursor - 1); err != nil {
					return "", err
				}
			}

		case KeyRight:
			if e.cursor < len(e.runes) {
				if err := e.moveTo(e.cursor + 1); err != nil {
					return "", err
				}
			}

		case KeyHome:
			if err := e.moveTo(0); err != nil {
				return "", err
			}

		case KeyEnd:
			if err := e.moveTo(len(e.runes)); err != nil {
				return "", err
			}

		case KeyUp:
			if e.hist == nil {
				continue
			}
			if line, ok := e.hist.Prev(string(e.runes)); ok {
				e.replace(line)
				if err := e.redraw(); err != nil {
					return "", err
				}
			}

		case KeyDown:
			if e.hist == nil {
				continue
			}
			if line, ok := e.hist.Next(); ok {
				e.replace(line)
				if err := e.redraw(); err != nil {
					return "", err
				}
			}

		case KeyKillToEnd:
			if e.cursor < len(e.runes) {
				e.runes = e.runes[:e.cursor]
				if err := e.redraw(); err != nil {
					return "", err
				}
			}

		case KeyKillToStart:
			if e.cursor > 0 {
				e.runes = append([]rune{}, e.runes[e.cursor:]...)
				e.cursor = 0
				if err := e.redraw(); err != nil {
					return "", err
				}
			}

		case KeyKillWord:
			if e.killWordLeft() {
				if err := e.redraw(); err != nil {
					return "", err
				}
			}

		case KeyClear:
			// Best-effort redraw: rewrite the line in place. We don't
			// know the prompt, so we can't fully clear-and-repaint.
			if err := e.redraw(); err != nil {
				return "", err
			}

		case KeyUnknown:
			// Drop silently.
		}
	}
}

// replace swaps the entire buffer for line and parks the cursor at
// end-of-line. Used by history navigation.
func (e *editor) replace(line string) {
	e.runes = []rune(line)
	e.cursor = len(e.runes)
}

// killWordLeft deletes from the cursor leftward across one run of
// whitespace and the run of non-whitespace before it. Returns true if
// anything was removed.
func (e *editor) killWordLeft() bool {
	if e.cursor == 0 {
		return false
	}
	i := e.cursor
	for i > 0 && isSpace(e.runes[i-1]) {
		i--
	}
	for i > 0 && !isSpace(e.runes[i-1]) {
		i--
	}
	if i == e.cursor {
		return false
	}
	e.runes = append(e.runes[:i], e.runes[e.cursor:]...)
	e.cursor = i
	return true
}

// moveTo repositions the physical cursor to the column corresponding to
// the logical cursor index `to`, emitting the minimum CSI movement
// needed. Both e.cursor and e.prevCursor are updated.
func (e *editor) moveTo(to int) error {
	delta := to - e.prevCursor
	if delta > 0 {
		if _, err := fmt.Fprintf(e.w, "\x1B[%dC", delta); err != nil {
			return err
		}
	} else if delta < 0 {
		if _, err := fmt.Fprintf(e.w, "\x1B[%dD", -delta); err != nil {
			return err
		}
	}
	e.cursor = to
	e.prevCursor = to
	return nil
}

// redraw repaints the entire edit area. The sequence is: move back to
// the start of the edit area, emit the current line through the
// encoder, erase any leftover characters from a previously-longer
// line, then position the cursor at the logical insertion point.
func (e *editor) redraw() error {
	if e.prevCursor > 0 {
		if _, err := fmt.Fprintf(e.w, "\x1B[%dD", e.prevCursor); err != nil {
			return err
		}
	}
	if err := e.emitRunes(e.runes); err != nil {
		return err
	}
	if _, err := io.WriteString(e.w, "\x1B[K"); err != nil {
		return err
	}
	back := len(e.runes) - e.cursor
	if back > 0 {
		if _, err := fmt.Fprintf(e.w, "\x1B[%dD", back); err != nil {
			return err
		}
	}
	e.prevCursor = e.cursor
	e.prevLen = len(e.runes)
	return nil
}

// emitRunes writes runes through the encoder, or directly when no
// encoder is configured.
func (e *editor) emitRunes(runes []rune) error {
	if len(runes) == 0 {
		return nil
	}
	if e.enc == nil {
		_, err := io.WriteString(e.w, string(runes))
		return err
	}
	out, err := e.enc.EncodeOut([]byte(string(runes)))
	if err != nil {
		return err
	}
	_, err = e.w.Write(out)
	return err
}

func insertRune(runes []rune, at int, r rune) []rune {
	runes = append(runes, 0)
	copy(runes[at+1:], runes[at:])
	runes[at] = r
	return runes
}

func isSpace(r rune) bool {
	return r == ' ' || r == '\t'
}
