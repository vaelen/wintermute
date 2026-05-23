// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package events

import (
	"testing"
	"time"
)

func TestMemBus_PublishToSubscribers(t *testing.T) {
	b := NewMemBus()
	defer b.Close()
	ch, cancel := b.Subscribe(RoomID(1))
	defer cancel()
	b.Publish(Event{Kind: KindSay, RoomID: 1, Text: "hello"})
	select {
	case e := <-ch:
		if e.Text != "hello" || e.Kind != KindSay {
			t.Fatalf("unexpected event: %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no event received")
	}
}

func TestMemBus_PerRoomIsolation(t *testing.T) {
	b := NewMemBus()
	defer b.Close()
	ch1, c1 := b.Subscribe(RoomID(1))
	defer c1()
	ch2, c2 := b.Subscribe(RoomID(2))
	defer c2()
	b.Publish(Event{Kind: KindSay, RoomID: 1, Text: "r1"})
	select {
	case <-ch2:
		t.Fatal("room 2 received room 1 event")
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case e := <-ch1:
		if e.Text != "r1" {
			t.Fatalf("expected r1, got %q", e.Text)
		}
	default:
		t.Fatal("room 1 did not receive event")
	}
}

func TestMemBus_OverflowDropsOldest(t *testing.T) {
	b := NewMemBus()
	defer b.Close()
	ch, cancel := b.Subscribe(RoomID(1))
	defer cancel()

	// Publish more events than the buffer can hold, tagging each
	// with a monotonically increasing sequence number in Extra.
	total := defaultSubBuffer * 2
	for i := 0; i < total; i++ {
		b.Publish(Event{
			Kind:   KindSay,
			RoomID: 1,
			Extra:  map[string]any{"seq": i},
		})
	}

	// Drain whatever survived.
	var seqs []int
loop:
	for {
		select {
		case e := <-ch:
			seqs = append(seqs, e.Extra["seq"].(int))
		default:
			break loop
		}
	}

	if len(seqs) == 0 {
		t.Fatal("no events survived")
	}
	if len(seqs) > defaultSubBuffer+1 {
		t.Fatalf("more events than buffer can hold: %d > %d",
			len(seqs), defaultSubBuffer+1)
	}
	// Drop-oldest invariant: the surviving events should be a suffix
	// of the published range. The minimum sequence number among
	// survivors must be at least (total - defaultSubBuffer - 1):
	// anything older was overwritten.
	minSeq := seqs[0]
	for _, s := range seqs {
		if s < minSeq {
			minSeq = s
		}
	}
	if minSeq < total-defaultSubBuffer-1 {
		t.Fatalf("oldest surviving seq=%d but expected ≥ %d (drop-oldest violated)",
			minSeq, total-defaultSubBuffer-1)
	}
	// And the surviving seq numbers should be strictly increasing
	// when read in channel order (FIFO).
	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] {
			t.Fatalf("seqs out of order at index %d: %v", i, seqs)
		}
	}
}

func TestMemBus_CancelRemovesSubscriber(t *testing.T) {
	b := NewMemBus()
	defer b.Close()
	ch, cancel := b.Subscribe(RoomID(1))
	cancel()
	if _, ok := <-ch; ok {
		t.Fatal("channel should be closed after cancel")
	}
}

func TestMemBus_CloseClosesAllSubscribers(t *testing.T) {
	b := NewMemBus()
	ch, cancel := b.Subscribe(RoomID(1))
	defer cancel()
	b.Close()
	if _, ok := <-ch; ok {
		t.Fatal("channel should be closed after Close")
	}
	// Publish after Close is a no-op (no panic).
	b.Publish(Event{Kind: KindSay, RoomID: 1, Text: "after close"})
}
