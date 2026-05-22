// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package events

import "sync"

const defaultSubBuffer = 32

// MemBus is the in-process Bus. Each subscriber gets a buffered
// channel; when a slow subscriber's channel is full, Publish drops
// the oldest event from that subscriber and enqueues the new one.
// Publish is non-blocking and safe to call from any goroutine,
// including from inside world.mu critical sections.
type MemBus struct {
	mu     sync.Mutex
	subs   map[RoomID]map[int64]chan Event
	nextID int64
	closed bool
}

func NewMemBus() *MemBus {
	return &MemBus{subs: map[RoomID]map[int64]chan Event{}}
}

func (b *MemBus) Publish(e Event) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	room := b.subs[e.RoomID]
	chans := make([]chan Event, 0, len(room))
	for _, ch := range room {
		chans = append(chans, ch)
	}
	b.mu.Unlock()
	for _, ch := range chans {
		select {
		case ch <- e:
		default:
			// Channel full: drop oldest, then try once more.
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- e:
			default:
			}
		}
	}
}

func (b *MemBus) Subscribe(room RoomID) (<-chan Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		ch := make(chan Event)
		close(ch)
		return ch, func() {}
	}
	if b.subs[room] == nil {
		b.subs[room] = map[int64]chan Event{}
	}
	id := b.nextID
	b.nextID++
	ch := make(chan Event, defaultSubBuffer)
	b.subs[room][id] = ch
	cancel := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if set := b.subs[room]; set != nil {
			if c, ok := set[id]; ok {
				close(c)
				delete(set, id)
			}
		}
	}
	return ch, cancel
}

func (b *MemBus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for _, set := range b.subs {
		for _, ch := range set {
			close(ch)
		}
	}
	b.subs = nil
}
