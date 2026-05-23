// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

//go:build test

package loop

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/vaelen/wintermute/internal/llm"
	"github.com/vaelen/wintermute/internal/llm/budget"
	"github.com/vaelen/wintermute/internal/llm/fake"
	"github.com/vaelen/wintermute/internal/world"
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

// stubTools is the minimal Tools implementation the loop's tool-call
// tests need: it records each invocation in order and serves
// pre-canned results.
type stubTools struct {
	metas   map[string]ToolMeta
	invoked []string
	results map[string]map[string]any
}

func (s *stubTools) Get(name string) ToolMeta { return s.metas[name] }

func (s *stubTools) Invoke(_ context.Context, name string, _ map[string]any) (map[string]any, error) {
	s.invoked = append(s.invoked, name)
	return s.results[name], nil
}

func TestLoop_ToolCallRoundTrip(t *testing.T) {
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

	// Round 1: response model returns a ToolCall.
	fb.Queue("response", llm.Response{
		ToolCalls: []llm.ToolCall{
			{ID: "call-1", Name: "serve_drink", Arguments: map[string]any{"drink": "beer"}},
		},
		UsageIn: 10, UsageOut: 5,
	})
	// Round 2: response model returns plain content (no more tool calls).
	fb.Queue("response", llm.Response{
		Content: "Here you go.",
		UsageIn: 8, UsageOut: 4,
	})

	tools := &stubTools{
		metas: map[string]ToolMeta{
			"serve_drink": {Name: "serve_drink", Description: "serve a drink"},
		},
		results: map[string]map[string]any{
			"serve_drink": {"ok": true, "drink": "beer"},
		},
	}
	bcast := newStubBroadcaster()

	done := make(chan struct{}, 1)
	l := &Loop{
		RoomID:       1,
		Bus:          bus,
		Debounce:     20 * time.Millisecond,
		NPCID:        100,
		NPCName:      "Bartender",
		Persona:      "tend the bar",
		LLM:          fakeLLM,
		ChatModel:    "response",
		GateModel:    "gate",
		World:        bcast,
		Tools:        tools,
		ToolNames:    []string{"serve_drink"},
		MaxToolDepth: 3,
	}
	l.Tick = func(ctx context.Context, obs []events.Event) {
		l.defaultTick(ctx, obs)
		done <- struct{}{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go l.Run(ctx)
	time.Sleep(10 * time.Millisecond)

	bus.Publish(events.Event{Kind: events.KindSay, RoomID: 1, Text: "I'll have a beer"})
	<-done

	if got := len(tools.invoked); got != 1 || tools.invoked[0] != "serve_drink" {
		t.Fatalf("tools.invoked=%v want [serve_drink]", tools.invoked)
	}
	select {
	case s := <-bcast.said:
		if s.text != "Here you go." {
			t.Fatalf("broadcast text=%q want %q", s.text, "Here you go.")
		}
	default:
		t.Fatal("no broadcast received")
	}
	if calls := fb.Calls("response"); calls != 2 {
		t.Fatalf("response model called %d times; want 2", calls)
	}
}

func TestLoop_ToolCallDepthBound(t *testing.T) {
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

	// Every response model call returns a ToolCall — infinite recursion
	// is only prevented by MaxToolDepth=2.
	for i := 0; i < 10; i++ {
		fb.Queue("response", llm.Response{
			ToolCalls: []llm.ToolCall{
				{ID: fmt.Sprintf("c%d", i), Name: "noop", Arguments: nil},
			},
		})
	}

	tools := &stubTools{
		metas:   map[string]ToolMeta{"noop": {Name: "noop"}},
		results: map[string]map[string]any{"noop": {"ok": true}},
	}
	bcast := newStubBroadcaster()
	done := make(chan struct{}, 1)

	l := &Loop{
		RoomID:       1,
		Bus:          bus,
		Debounce:     20 * time.Millisecond,
		NPCID:        100,
		NPCName:      "T",
		Persona:      "p",
		LLM:          fakeLLM,
		ChatModel:    "response",
		GateModel:    "gate",
		World:        bcast,
		Tools:        tools,
		ToolNames:    []string{"noop"},
		MaxToolDepth: 2,
	}
	l.Tick = func(ctx context.Context, obs []events.Event) {
		l.defaultTick(ctx, obs)
		done <- struct{}{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go l.Run(ctx)
	time.Sleep(10 * time.Millisecond)

	bus.Publish(events.Event{Kind: events.KindSay, RoomID: 1, Text: "go"})
	<-done

	// depth=0 -> Chat #1 returns ToolCall -> invoke -> depth=1
	// depth=1 -> Chat #2 returns ToolCall -> invoke -> depth=2
	// depth=2 -> Chat #3 returns ToolCall -> depth>=MaxToolDepth -> bail
	// Total Chat calls: 3; tool invocations: 2.
	if calls := fb.Calls("response"); calls != 3 {
		t.Fatalf("response calls=%d want 3", calls)
	}
	if got := len(tools.invoked); got != 2 {
		t.Fatalf("tool invocations=%d want 2", got)
	}
}

func TestLoop_BudgetExhausted_SkipsResponse(t *testing.T) {
	bus := events.NewMemBus()
	defer bus.Close()

	fakeLLM, err := fake.New(map[string]any{
		"models": map[string]any{
			"gate":     map[string]any{"default": "YES"},
			"response": map[string]any{"default": "Hello."},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	fb := fakeLLM.(*fake.Fake)

	// nil db is safe: Manager only dereferences db in Load/Flush, and
	// the loop never calls those — only Allow/Record.
	mgr := budget.NewManager(nil, budget.Defaults{Minute: 100, Hour: 1000, Day: 10000})
	const npcID world.ObjectID = 100
	// Burn the minute window so the next Allow(npcID, gateBudgetEstimate)
	// returns false.
	mgr.Record(npcID, 100, 0)

	bcast := newStubBroadcaster()
	done := make(chan struct{}, 1)

	l := &Loop{
		RoomID:    1,
		Bus:       bus,
		Debounce:  20 * time.Millisecond,
		NPCID:     events.ObjectID(npcID),
		NPCName:   "T",
		Persona:   "p",
		LLM:       fakeLLM,
		ChatModel: "response",
		GateModel: "gate",
		World:     bcast,
		Budget:    mgr,
	}
	l.Tick = func(ctx context.Context, obs []events.Event) {
		l.defaultTick(ctx, obs)
		done <- struct{}{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go l.Run(ctx)
	time.Sleep(10 * time.Millisecond)

	bus.Publish(events.Event{Kind: events.KindSay, RoomID: 1, Text: "hi"})
	<-done

	if calls := fb.Calls("gate"); calls != 0 {
		t.Fatalf("gate calls=%d want 0 (budget should have short-circuited before gate)", calls)
	}
	if calls := fb.Calls("response"); calls != 0 {
		t.Fatalf("response calls=%d want 0", calls)
	}
	select {
	case msg := <-bcast.said:
		t.Fatalf("unexpected broadcast: %+v", msg)
	default:
	}
}

func TestLoop_DropsOtherNPCSpeech(t *testing.T) {
	bus := events.NewMemBus()
	defer bus.Close()
	fakeLLM, err := fake.New(map[string]any{
		"models": map[string]any{
			"gate":     map[string]any{"default": "YES"},
			"response": map[string]any{"default": "should not fire"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	fb := fakeLLM.(*fake.Fake)

	done := make(chan struct{}, 1)
	l := &Loop{
		RoomID: 1, Bus: bus, Debounce: 30 * time.Millisecond,
		NPCID: 200, NPCName: "Listener", Persona: "p",
		LLM: fakeLLM, ChatModel: "response", GateModel: "gate",
	}
	l.Tick = func(ctx context.Context, obs []events.Event) {
		l.defaultTick(ctx, obs)
		done <- struct{}{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go l.Run(ctx)
	time.Sleep(10 * time.Millisecond)

	// Publish an NPC-originated say from a different NPC (Actor=100).
	bus.Publish(events.Event{
		Kind: events.KindSay, RoomID: 1, Actor: 100,
		Text: "I am another NPC", At: time.Now(),
		Extra: map[string]any{"actor_kind": "npc"},
	})

	// Also publish a player say to verify the tick still fires for player input.
	time.Sleep(60 * time.Millisecond) // let any NPC-event tick attempt fire
	bus.Publish(events.Event{
		Kind: events.KindSay, RoomID: 1, Actor: 50,
		Text: "I am a player", At: time.Now(),
	})

	<-done

	if calls := fb.Calls("gate"); calls != 1 {
		t.Fatalf("gate called %d times; want 1 (only the player say should reach the loop)", calls)
	}
}

// TestLoop_EmptyGateModel_StillRunsGate verifies that GateModel == ""
// no longer disables gating: the gate Chat call still fires (with an
// empty ChatOpts.Model, which the LLM backend resolves to its default
// model), and a YES decision allows the response path to broadcast.
//
// In production, this is what happens when no gate model is configured
// anywhere: per-NPC column empty, [llm.default.opts].gate_model empty.
// The Ollama backend falls back to its configured chat model; with the
// fake backend, an empty ChatOpts.Model routes to the top-level
// responses/defaultReply table.
func TestLoop_EmptyGateModel_StillRunsGate(t *testing.T) {
	bus := events.NewMemBus()
	defer bus.Close()

	fakeLLM, err := fake.New(map[string]any{
		// Top-level default is what the gate sees when ChatOpts.Model
		// is empty — i.e. the fake's analog of Ollama's chat-model
		// fallback.
		"default": "YES",
		"models": map[string]any{
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
		GateModel: "",
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

	if calls := fb.Calls(""); calls != 1 {
		t.Errorf("gate call with empty model = %d, want 1 (gate must fire even with empty GateModel)", calls)
	}
	if calls := fb.Calls("response"); calls != 1 {
		t.Errorf("response model called %d times; want 1 (gate said YES)", calls)
	}
	select {
	case s := <-bcast.said:
		if s.text != "Hello there." {
			t.Errorf("broadcast text = %q, want %q", s.text, "Hello there.")
		}
	case <-time.After(time.Second):
		t.Fatal("NPCSay was not called")
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
