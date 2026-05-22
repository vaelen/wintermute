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
	for i := 0; i < defaultSubBuffer+10; i++ {
		b.Publish(Event{Kind: KindSay, RoomID: 1, Text: "x"})
	}
	count := 0
loop:
	for {
		select {
		case <-ch:
			count++
		default:
			break loop
		}
	}
	if count == 0 || count > defaultSubBuffer+1 {
		t.Fatalf("received %d events; want between 1 and %d", count, defaultSubBuffer+1)
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
