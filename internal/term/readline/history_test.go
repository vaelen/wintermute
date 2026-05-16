// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package readline

import "testing"

func TestHistoryEmpty(t *testing.T) {
	h := NewHistory(10)
	if _, ok := h.Prev(""); ok {
		t.Errorf("Prev on empty history should return ok=false")
	}
	if _, ok := h.Next(); ok {
		t.Errorf("Next on empty history should return ok=false")
	}
	if h.Len() != 0 {
		t.Errorf("Len = %d, want 0", h.Len())
	}
}

func TestHistoryAddAndPrev(t *testing.T) {
	h := NewHistory(10)
	h.Add("look")
	h.Add("north")
	h.Add("inventory")

	got, ok := h.Prev("draft")
	if !ok || got != "inventory" {
		t.Errorf("first Prev = (%q, %v), want (\"inventory\", true)", got, ok)
	}
	got, ok = h.Prev("ignored")
	if !ok || got != "north" {
		t.Errorf("second Prev = (%q, %v), want (\"north\", true)", got, ok)
	}
	got, ok = h.Prev("ignored")
	if !ok || got != "look" {
		t.Errorf("third Prev = (%q, %v), want (\"look\", true)", got, ok)
	}
	// One step past the oldest stays clamped at the oldest entry.
	got, ok = h.Prev("ignored")
	if !ok || got != "look" {
		t.Errorf("fourth Prev = (%q, %v), want (\"look\", true)", got, ok)
	}
}

func TestHistoryNextRestoresDraft(t *testing.T) {
	h := NewHistory(10)
	h.Add("a")
	h.Add("b")

	if got, _ := h.Prev("typed-but-not-sent"); got != "b" {
		t.Fatalf("Prev = %q", got)
	}
	if got, _ := h.Prev("ignored"); got != "a" {
		t.Fatalf("Prev = %q", got)
	}
	// Walk forward.
	if got, _ := h.Next(); got != "b" {
		t.Fatalf("Next = %q, want b", got)
	}
	got, ok := h.Next()
	if !ok || got != "typed-but-not-sent" {
		t.Errorf("walking off newest = (%q, %v), want (typed-but-not-sent, true)", got, ok)
	}
	// Now on draft; further Next does nothing.
	if _, ok := h.Next(); ok {
		t.Errorf("Next on draft should return ok=false")
	}
}

func TestHistoryDedupConsecutive(t *testing.T) {
	h := NewHistory(10)
	h.Add("same")
	h.Add("same")
	h.Add("same")
	if h.Len() != 1 {
		t.Errorf("Len after consecutive dups = %d, want 1", h.Len())
	}
	// Non-consecutive duplicate is fine.
	h.Add("other")
	h.Add("same")
	if h.Len() != 3 {
		t.Errorf("Len after non-consecutive dup = %d, want 3", h.Len())
	}
}

func TestHistoryDropsEmptyLines(t *testing.T) {
	h := NewHistory(10)
	h.Add("")
	h.Add("real")
	h.Add("")
	if h.Len() != 1 {
		t.Errorf("Len = %d, want 1", h.Len())
	}
}

func TestHistoryCapacityWrap(t *testing.T) {
	h := NewHistory(3)
	h.Add("a")
	h.Add("b")
	h.Add("c")
	h.Add("d") // should evict "a"
	if h.Len() != 3 {
		t.Fatalf("Len = %d, want 3", h.Len())
	}
	// Newest first via Prev.
	got, _ := h.Prev("")
	if got != "d" {
		t.Errorf("newest = %q, want d", got)
	}
	got, _ = h.Prev("")
	if got != "c" {
		t.Errorf("next = %q, want c", got)
	}
	got, _ = h.Prev("")
	if got != "b" {
		t.Errorf("oldest = %q, want b (a was evicted)", got)
	}
}

func TestHistoryAddResetsCursor(t *testing.T) {
	h := NewHistory(10)
	h.Add("a")
	h.Add("b")
	if got, _ := h.Prev("draft"); got != "b" {
		t.Fatalf("Prev = %q", got)
	}
	// Submitting a new entry while browsing must reset cursor + draft.
	h.Add("c")
	if h.cursor != -1 || h.draft != "" {
		t.Errorf("Add did not reset browse state: cursor=%d draft=%q", h.cursor, h.draft)
	}
	got, _ := h.Prev("new-draft")
	if got != "c" {
		t.Errorf("after Add, first Prev = %q, want c", got)
	}
}

func TestHistoryResetClears(t *testing.T) {
	h := NewHistory(10)
	h.Add("a")
	if got, _ := h.Prev("draft"); got != "a" {
		t.Fatalf("Prev = %q", got)
	}
	h.Reset()
	if h.cursor != -1 || h.draft != "" {
		t.Errorf("Reset did not clear browse state: cursor=%d draft=%q", h.cursor, h.draft)
	}
}

func TestHistoryDisabledWhenCapZero(t *testing.T) {
	h := NewHistory(0)
	h.Add("x")
	if h.Len() != 0 {
		t.Errorf("Len with cap=0 = %d, want 0", h.Len())
	}
	if _, ok := h.Prev(""); ok {
		t.Errorf("Prev with cap=0 should return ok=false")
	}
}

func TestHistoryNilSafe(t *testing.T) {
	var h *History
	h.Add("nope")
	if _, ok := h.Prev(""); ok {
		t.Errorf("Prev on nil should return ok=false")
	}
	if _, ok := h.Next(); ok {
		t.Errorf("Next on nil should return ok=false")
	}
	if h.Len() != 0 {
		t.Errorf("Len on nil = %d, want 0", h.Len())
	}
	h.Reset()
}
