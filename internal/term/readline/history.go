// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package readline

// History captures one session's in-memory line-editing history.
//
// It is a bounded append-only ring with two cursor semantics layered on
// top: cursor == -1 means "the user is on the live draft they are
// currently typing"; cursor in [0, len) indexes a recalled entry, where
// 0 is the oldest visible entry and len-1 is the newest.
//
// The History is single-goroutine (the session's read goroutine). Its
// methods are not safe to call concurrently.
type History struct {
	entries []string
	cap     int
	cursor  int    // -1 means "on the live draft"
	draft   string // text typed before the user first walked back into history
}

// NewHistory returns a History with the given capacity. cap <= 0 means
// "no history": Add is a no-op and Prev/Next never produce entries.
func NewHistory(cap int) *History {
	return &History{cap: cap, cursor: -1}
}

// Len returns the number of entries currently stored.
func (h *History) Len() int {
	if h == nil {
		return 0
	}
	return len(h.entries)
}

// Add records line as the newest history entry. Empty lines and lines
// identical to the previous entry are dropped. When the ring is full,
// the oldest entry is evicted. Add also clears the browse cursor and
// the saved draft, so the next Prev starts from the bottom.
func (h *History) Add(line string) {
	if h == nil || h.cap <= 0 {
		return
	}
	if line == "" {
		h.Reset()
		return
	}
	if n := len(h.entries); n > 0 && h.entries[n-1] == line {
		h.Reset()
		return
	}
	if len(h.entries) >= h.cap {
		// Drop oldest.
		copy(h.entries, h.entries[1:])
		h.entries = h.entries[:len(h.entries)-1]
	}
	h.entries = append(h.entries, line)
	h.Reset()
}

// Prev moves one step toward older entries and returns the new line. On
// the first Prev call after Reset (i.e. when the user is still on the
// live draft), `current` is captured so a subsequent Next can restore
// it. Returns ("", false) if there is no older entry available.
func (h *History) Prev(current string) (string, bool) {
	if h == nil || len(h.entries) == 0 {
		return "", false
	}
	if h.cursor == -1 {
		h.draft = current
		h.cursor = len(h.entries) - 1
		return h.entries[h.cursor], true
	}
	if h.cursor == 0 {
		return h.entries[0], true
	}
	h.cursor--
	return h.entries[h.cursor], true
}

// Next moves one step toward newer entries. Going off the end of
// history returns to the live draft (the text the user had typed before
// they first pressed Up). Returns ("", false) when already on the draft.
func (h *History) Next() (string, bool) {
	if h == nil || h.cursor == -1 {
		return "", false
	}
	if h.cursor == len(h.entries)-1 {
		// Walking off the newest entry returns to the live draft.
		h.cursor = -1
		d := h.draft
		h.draft = ""
		return d, true
	}
	h.cursor++
	return h.entries[h.cursor], true
}

// Reset clears the browse cursor and the saved draft. After Reset, the
// History considers the user to be on a fresh live draft.
func (h *History) Reset() {
	if h == nil {
		return
	}
	h.cursor = -1
	h.draft = ""
}
