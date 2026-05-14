// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package memory

import (
	"sync"
	"testing"
	"time"
)

func TestShortTermAppendAndRecent(t *testing.T) {
	s := NewShortTerm(20)
	now := time.Unix(1700000000, 0)
	s.Append(Turn{Speaker: "alice", Text: "hello", At: now})
	s.Append(Turn{Speaker: "<npc>", Text: "evening", At: now.Add(time.Second)})
	s.Append(Turn{Speaker: "alice", Text: "what's good", At: now.Add(2 * time.Second)})

	got := s.Recent(2)
	if len(got) != 2 {
		t.Fatalf("Recent(2) length = %d, want 2", len(got))
	}
	if got[0].Text != "evening" {
		t.Errorf("Recent(2)[0].Text = %q, want %q", got[0].Text, "evening")
	}
	if got[1].Text != "what's good" {
		t.Errorf("Recent(2)[1].Text = %q, want %q", got[1].Text, "what's good")
	}
}

func TestShortTermRecentMoreThanAvailable(t *testing.T) {
	s := NewShortTerm(20)
	s.Append(Turn{Speaker: "alice", Text: "only"})

	got := s.Recent(10)
	if len(got) != 1 {
		t.Fatalf("Recent(10) over 1-element buffer = %d entries, want 1", len(got))
	}
}

func TestShortTermDrain(t *testing.T) {
	s := NewShortTerm(20)
	s.Append(Turn{Speaker: "alice", Text: "one"})
	s.Append(Turn{Speaker: "alice", Text: "two"})

	out := s.Drain()
	if len(out) != 2 {
		t.Fatalf("Drain length = %d, want 2", len(out))
	}
	if got := s.Recent(10); len(got) != 0 {
		t.Errorf("after Drain, Recent length = %d, want 0", len(got))
	}
	if got := s.Drain(); len(got) != 0 {
		t.Errorf("second Drain length = %d, want 0 (already empty)", len(got))
	}
}

func TestShortTermDrainReturnsCopy(t *testing.T) {
	s := NewShortTerm(20)
	s.Append(Turn{Speaker: "alice", Text: "original"})

	out := s.Drain()
	out[0].Text = "mutated"
	s.Append(Turn{Speaker: "alice", Text: "fresh"})

	got := s.Recent(10)
	if len(got) != 1 || got[0].Text != "fresh" {
		t.Errorf("after mutating drained slice, buffer should still hold the new append; got %#v", got)
	}
}

func TestShortTermRingBufferOverflow(t *testing.T) {
	const capN = 3
	s := NewShortTerm(capN)
	for i, txt := range []string{"a", "b", "c", "d", "e"} {
		_ = i
		s.Append(Turn{Speaker: "alice", Text: txt})
	}

	got := s.Recent(capN)
	if len(got) != capN {
		t.Fatalf("Recent over full buffer length = %d, want %d", len(got), capN)
	}
	wantTexts := []string{"c", "d", "e"}
	for i := range got {
		if got[i].Text != wantTexts[i] {
			t.Errorf("Recent[%d] = %q, want %q (oldest entries should have been evicted)",
				i, got[i].Text, wantTexts[i])
		}
	}
}

func TestShortTermLenAndEmpty(t *testing.T) {
	s := NewShortTerm(10)
	if !s.Empty() {
		t.Errorf("new ShortTerm should report Empty() == true")
	}
	if got := s.Len(); got != 0 {
		t.Errorf("Len of new buffer = %d, want 0", got)
	}

	s.Append(Turn{Speaker: "alice", Text: "x"})
	if s.Empty() {
		t.Errorf("ShortTerm with one turn should report Empty() == false")
	}
	if got := s.Len(); got != 1 {
		t.Errorf("Len after one Append = %d, want 1", got)
	}
}

func TestShortTermConcurrent(t *testing.T) {
	s := NewShortTerm(1024)
	const goroutines = 8
	const perGoroutine = 64

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				s.Append(Turn{Speaker: "alice", Text: "x"})
			}
		}()
	}
	wg.Wait()

	if got, want := s.Len(), goroutines*perGoroutine; got != want {
		t.Errorf("after concurrent Append, Len = %d, want %d", got, want)
	}
}
