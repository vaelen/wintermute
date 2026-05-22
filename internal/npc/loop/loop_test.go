// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package loop

import (
	"context"
	"testing"
	"time"

	"github.com/vaelen/wintermute/internal/world/events"
)

func TestLoop_BuffersUntilDebounceExpires(t *testing.T) {
	bus := events.NewMemBus()
	defer bus.Close()

	got := make(chan []events.Event, 1)
	l := &Loop{
		RoomID:   1,
		Bus:      bus,
		Debounce: 50 * time.Millisecond,
		Tick: func(_ context.Context, obs []events.Event) {
			cp := make([]events.Event, len(obs))
			copy(cp, obs)
			got <- cp
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go l.Run(ctx)

	// Subscriptions are asynchronous; give the Run goroutine a moment
	// to call Subscribe before we Publish.
	time.Sleep(10 * time.Millisecond)
	for i := 0; i < 3; i++ {
		bus.Publish(events.Event{Kind: events.KindSay, RoomID: 1, Text: "x"})
	}

	select {
	case obs := <-got:
		if len(obs) != 3 {
			t.Fatalf("want 3 buffered observations, got %d", len(obs))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("tick never fired")
	}
}

func TestLoop_StopsOnCtxDone(t *testing.T) {
	bus := events.NewMemBus()
	defer bus.Close()
	l := &Loop{
		RoomID:   1,
		Bus:      bus,
		Debounce: 10 * time.Millisecond,
		Tick:     func(context.Context, []events.Event) {},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { l.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not exit on ctx cancel")
	}
}

func TestLoop_NoTickWithoutObservations(t *testing.T) {
	bus := events.NewMemBus()
	defer bus.Close()

	called := make(chan struct{}, 1)
	l := &Loop{
		RoomID:   1,
		Bus:      bus,
		Debounce: 20 * time.Millisecond,
		Tick: func(context.Context, []events.Event) {
			called <- struct{}{}
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go l.Run(ctx)

	// Wait well past the debounce duration without publishing anything.
	select {
	case <-called:
		t.Fatal("Tick fired despite no observations")
	case <-time.After(100 * time.Millisecond):
	}
}
