// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

//go:build test

package loop

import (
	"context"
	"testing"
	"time"

	"github.com/vaelen/wintermute/internal/llm/fake"
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

func TestLoop_GateNo_DoesNotCallResponse(t *testing.T) {
	bus := events.NewMemBus()
	defer bus.Close()

	fakeLLM, err := fake.New(map[string]any{
		"models": map[string]any{
			"gate":     map[string]any{"default": "NO"},
			"response": map[string]any{"default": "Hello there."},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	fb := fakeLLM.(*fake.Fake)

	done := make(chan struct{}, 1)
	l := &Loop{
		RoomID:    1,
		Bus:       bus,
		Debounce:  20 * time.Millisecond,
		NPCID:     100,
		NPCName:   "Tester",
		Persona:   "tester",
		LLM:       fakeLLM,
		ChatModel: "response",
		GateModel: "gate",
	}
	// Wrap defaultTick to signal when it has run end-to-end.
	l.Tick = func(ctx context.Context, obs []events.Event) {
		l.defaultTick(ctx, obs)
		done <- struct{}{}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go l.Run(ctx)

	time.Sleep(10 * time.Millisecond)
	bus.Publish(events.Event{Kind: events.KindSay, RoomID: 1, Text: "ignore me"})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Tick never fired")
	}

	if calls := fb.Calls("response"); calls != 0 {
		t.Fatalf("response model called %d times on NO gate; want 0", calls)
	}
	if calls := fb.Calls("gate"); calls != 1 {
		t.Fatalf("gate model called %d times; want 1", calls)
	}
}

func TestLoop_GateYes_AllowsResponsePath(t *testing.T) {
	// With the response path being a stub in this task, this test only
	// confirms that gateAllows returns true and defaultTick proceeds
	// to (the stub) respond — i.e., it doesn't bail at the gate.
	bus := events.NewMemBus()
	defer bus.Close()

	fakeLLM, err := fake.New(map[string]any{
		"models": map[string]any{
			"gate": map[string]any{"default": "YES"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	fb := fakeLLM.(*fake.Fake)

	done := make(chan struct{}, 1)
	l := &Loop{
		RoomID:    1,
		Bus:       bus,
		Debounce:  20 * time.Millisecond,
		NPCID:     100,
		NPCName:   "Tester",
		Persona:   "tester",
		LLM:       fakeLLM,
		ChatModel: "response",
		GateModel: "gate",
	}
	l.Tick = func(ctx context.Context, obs []events.Event) {
		l.defaultTick(ctx, obs)
		done <- struct{}{}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go l.Run(ctx)

	time.Sleep(10 * time.Millisecond)
	bus.Publish(events.Event{Kind: events.KindSay, RoomID: 1, Text: "hello"})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Tick never fired")
	}

	if calls := fb.Calls("gate"); calls != 1 {
		t.Fatalf("gate model called %d times; want 1", calls)
	}
}

// stubBroadcaster captures NPCSay calls from the loop so the
// broadcast test can assert what the response path emitted without
// standing up a real *world.World.
type stubBroadcaster struct {
	said chan stubSay
}

type stubSay struct {
	npcID events.ObjectID
	text  string
}

func newStubBroadcaster() *stubBroadcaster {
	return &stubBroadcaster{said: make(chan stubSay, 4)}
}

func (s *stubBroadcaster) NPCSay(npcID events.ObjectID, text string) error {
	s.said <- stubSay{npcID: npcID, text: text}
	return nil
}

func TestLoop_GateYes_BroadcastsResponse(t *testing.T) {
	bus := events.NewMemBus()
	defer bus.Close()

	fakeLLM, err := fake.New(map[string]any{
		"models": map[string]any{
			"gate":     map[string]any{"default": "YES"},
			"response": map[string]any{"default": "Hello there."},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	fb := fakeLLM.(*fake.Fake)

	bcast := newStubBroadcaster()

	done := make(chan struct{}, 1)
	l := &Loop{
		RoomID:    1,
		Bus:       bus,
		Debounce:  20 * time.Millisecond,
		NPCID:     100,
		NPCName:   "Tester",
		Persona:   "tester",
		LLM:       fakeLLM,
		ChatModel: "response",
		GateModel: "gate",
		World:     bcast,
	}
	l.Tick = func(ctx context.Context, obs []events.Event) {
		l.defaultTick(ctx, obs)
		done <- struct{}{}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go l.Run(ctx)

	time.Sleep(10 * time.Millisecond)
	bus.Publish(events.Event{Kind: events.KindSay, RoomID: 1, Text: "hello"})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Tick never fired")
	}

	select {
	case s := <-bcast.said:
		if s.npcID != 100 {
			t.Errorf("NPCSay npcID = %d, want 100", s.npcID)
		}
		if s.text != "Hello there." {
			t.Errorf("NPCSay text = %q, want %q", s.text, "Hello there.")
		}
	case <-time.After(time.Second):
		t.Fatal("NPCSay was not called")
	}

	if calls := fb.Calls("response"); calls != 1 {
		t.Fatalf("response model called %d times; want 1", calls)
	}
	if calls := fb.Calls("gate"); calls != 1 {
		t.Fatalf("gate model called %d times; want 1", calls)
	}
}

func TestFirstToken(t *testing.T) {
	cases := map[string]string{
		"YES":          "YES",
		"  YES":        "YES",
		"YES.":         "YES",
		"YES, please.": "YES",
		"":             "",
	}
	for in, want := range cases {
		if got := firstToken(in); got != want {
			t.Errorf("firstToken(%q)=%q want %q", in, got, want)
		}
	}
}
