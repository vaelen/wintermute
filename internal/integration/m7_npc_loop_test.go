// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

//go:build test

package integration

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/config"
	"github.com/vaelen/wintermute/internal/llm"
	"github.com/vaelen/wintermute/internal/llm/budget"
	"github.com/vaelen/wintermute/internal/llm/fake"
	"github.com/vaelen/wintermute/internal/npc"
	"github.com/vaelen/wintermute/internal/npc/loop"
	"github.com/vaelen/wintermute/internal/store"
	"github.com/vaelen/wintermute/internal/world"
	"github.com/vaelen/wintermute/internal/world/events"
)

// m7FakeRegistry stores *fake.Fake instances keyed by a per-test string.
// The "m7-fake" backend factory (registered once in init) returns the
// instance addressed by the "_m7_key" entry in its opts. This lets each
// test pre-construct a fake LLM, register it under a unique key, and be
// sure that npc.Load constructs the bartender's NPC against THAT
// instance — so the test can Queue responses on the same fake the loop
// holds, and read Calls back from it.
//
// Without this indirection, npc.Load calls llm.Open which constructs a
// brand-new *fake.Fake every time. There is no public API on
// npc.Registry to retrieve the constructed instance.
var (
	m7FakesMu sync.Mutex
	m7Fakes   = map[string]*fake.Fake{}
)

func init() {
	llm.Register("m7-fake", func(opts map[string]any) (llm.LLM, error) {
		key, _ := opts["_m7_key"].(string)
		if key == "" {
			return nil, fmt.Errorf("m7-fake: opts[\"_m7_key\"] missing or empty")
		}
		m7FakesMu.Lock()
		f, ok := m7Fakes[key]
		m7FakesMu.Unlock()
		if !ok {
			return nil, fmt.Errorf("m7-fake: no instance registered for key %q", key)
		}
		return f, nil
	})
}

// m7Setup bundles the live objects the M7 integration tests probe and
// assert against. It is produced by setupM7World; tests should treat the
// fields as read-only handles.
type m7Setup struct {
	World  *world.World
	Bus    *events.MemBus
	Budget *budget.Manager
	LLM    *fake.Fake
	Tools  *m7StubTools

	BartenderID   world.ObjectID
	AliceID       world.ObjectID
	BobID         world.ObjectID
	BeerID        world.ObjectID
	BarRoomID     world.RoomID
	KitchenRoomID world.RoomID

	AlicePresence *world.Presence
	BobPresence   *world.Presence
}

// m7StubTools is a loop.Tools implementation that:
//   - advertises serve_drink as a known tool, and
//   - on Invoke("serve_drink", ...) moves the configured beer item into
//     the configured player's inventory via world.Take.
//
// invoked records each tool name dispatched, in order, so the test can
// assert exactly one call. Reads are safe at end-of-test only.
type m7StubTools struct {
	world         *world.World
	alicePresence *world.Presence

	mu      sync.Mutex
	invoked []string
}

func (s *m7StubTools) Get(name string) loop.ToolMeta {
	if name == "serve_drink" {
		return loop.ToolMeta{
			Name:        "serve_drink",
			Description: "hand a drink from the bar stock to a patron",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"to": map[string]any{"type": "integer"},
				},
			},
		}
	}
	return loop.ToolMeta{}
}

func (s *m7StubTools) Invoke(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	s.mu.Lock()
	s.invoked = append(s.invoked, name)
	s.mu.Unlock()
	if name != "serve_drink" {
		return nil, fmt.Errorf("m7StubTools: unexpected tool %q", name)
	}
	obj, err := s.world.Take(ctx, s.alicePresence, "beer")
	if err != nil {
		return nil, fmt.Errorf("m7StubTools: take beer: %w", err)
	}
	return map[string]any{"ok": true, "object_id": int64(obj.ID), "name": obj.Name}, nil
}

func (s *m7StubTools) invokedSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.invoked))
	copy(out, s.invoked)
	return out
}

// m7Broadcaster adapts *world.World to loop.Broadcaster.
type m7Broadcaster struct{ w *world.World }

func (b m7Broadcaster) NPCSay(npcID events.ObjectID, text string) error {
	return b.w.NPCSay(world.ObjectID(npcID), text)
}

// setupM7World builds a world with rooms `bar` and `kitchen`, places a
// bartender NPC in `bar` (configured with the m7-fake backend so the
// test controls the LLM instance), seeds a `beer` item in the bar, and
// attaches two players: alice in the bar, bob in the kitchen. The fake
// backend is wired so the gate model returns YES only on say-driven
// observations (the default-NO falls through for derivative events like
// KindTake so the post-broadcast tick exits without a second response
// call). Two response-model entries are pre-queued: a ToolCall for
// serve_drink, then plain content "Here you go.".
//
// The returned m7Setup carries handles to every component the tests
// need, and t.Cleanup is registered for orderly teardown.
func setupM7World(t *testing.T) *m7Setup {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dbPath := filepath.Join(t.TempDir(), "m7.db")

	rootCtx, rootCancel := context.WithCancel(context.Background())

	db, err := store.Open(rootCtx, dbPath, logger)
	if err != nil {
		rootCancel()
		t.Fatalf("store.Open: %v", err)
	}

	// Construct the *fake.Fake the test will drive. The gate model's
	// default is "NO" — every gate call we want to allow is handled via
	// Queue (queued YES is popped on the first gate call; subsequent
	// gate calls fall through to default "NO" so any post-broadcast
	// tick driven by a derivative event like KindTake silently exits).
	// The response model has no substring rules and a default of "" —
	// every response-model call goes through Queue.
	//
	// Why not substring-match on observation text? The loop sends the
	// gate prompt as a single System message; *fake.Fake.Chat's
	// substring matcher looks at the last User message and falls
	// through to default when none is present, so substring routing
	// never matches against the gate prompt.
	fakeOpts := map[string]any{
		"models": map[string]any{
			"gate":     map[string]any{"default": "NO"},
			"response": map[string]any{"default": ""},
		},
	}
	fakeLLM, err := fake.New(fakeOpts)
	if err != nil {
		_ = db.Close()
		rootCancel()
		t.Fatalf("fake.New: %v", err)
	}
	f := fakeLLM.(*fake.Fake)

	// Register the instance under a unique key so npc.Load's call to
	// llm.Open("m7-fake", ...) resolves to it. t.Cleanup unregisters at
	// end-of-test so the global map does not leak.
	key := fmt.Sprintf("m7-%s", t.Name())
	m7FakesMu.Lock()
	m7Fakes[key] = f
	m7FakesMu.Unlock()

	// Seed bar and kitchen rooms, the beer item, and reconfigure the
	// already-seeded bartender NPC: move it to the bar, swap its
	// backend to m7-fake (keyed at our unique instance), set its
	// gate/chat models, and enable serve_drink as an allowed tool.
	if err := db.Write(rootCtx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(rootCtx,
			`INSERT INTO rooms(slug, name, description, created_at) VALUES
			    ('bar', 'The Bar', 'A neon-lit cyberpunk dive.', strftime('%s','now')),
			    ('kitchen', 'The Kitchen', 'A back-of-house prep station.', strftime('%s','now'))`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(rootCtx,
			`INSERT INTO objects(slug, name, short_desc, long_desc, kind)
			   VALUES ('bar/beer', 'beer', 'a frosted pint glass of beer', 'A frosted pint glass beaded with condensation.', 'item')`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(rootCtx,
			`INSERT INTO object_locations(object_id, room_id, holder_id)
			   SELECT (SELECT id FROM objects WHERE slug='bar/beer'),
			          (SELECT id FROM rooms   WHERE slug='bar'),
			          NULL`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(rootCtx,
			`UPDATE object_locations
			    SET room_id = (SELECT id FROM rooms WHERE slug='bar'), holder_id = NULL
			  WHERE object_id = (SELECT id FROM objects WHERE slug='npc/bartender')`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(rootCtx,
			`UPDATE npc_config
			    SET backend = 'm7-fake',
			        backend_opts = ?,
			        chat_model = 'response',
			        gate_model = 'gate',
			        tools = 'serve_drink'
			  WHERE object_id = (SELECT id FROM objects WHERE slug='npc/bartender')`,
			fmt.Sprintf(`{"_m7_key":%q}`, key)); err != nil {
			return err
		}
		return nil
	}); err != nil {
		_ = db.Close()
		rootCancel()
		t.Fatalf("seed bar world: %v", err)
	}

	w, err := world.Load(rootCtx, db, logger)
	if err != nil {
		_ = db.Close()
		rootCancel()
		t.Fatalf("world.Load: %v", err)
	}

	bar, err := w.RoomBySlug("bar")
	if err != nil {
		_ = db.Close()
		rootCancel()
		t.Fatalf("RoomBySlug(bar): %v", err)
	}
	kitchen, err := w.RoomBySlug("kitchen")
	if err != nil {
		_ = db.Close()
		rootCancel()
		t.Fatalf("RoomBySlug(kitchen): %v", err)
	}
	beerObjID := mustObjIDBySlug(t, w, "bar/beer")
	bartenderID := mustObjIDBySlug(t, w, "npc/bartender")

	bus := events.NewMemBus()
	w.SetBus(bus)

	a := auth.NewStore(db)
	acctAlice, alice := mustCreatePlayer(t, rootCtx, a, w, "alice")
	acctBob, bob := mustCreatePlayer(t, rootCtx, a, w, "bob")
	if err := w.MoveObject(rootCtx, alice, bar.ID); err != nil {
		_ = db.Close()
		rootCancel()
		t.Fatalf("MoveObject(alice -> bar): %v", err)
	}
	if err := w.MoveObject(rootCtx, bob, kitchen.ID); err != nil {
		_ = db.Close()
		rootCancel()
		t.Fatalf("MoveObject(bob -> kitchen): %v", err)
	}

	alicePresence := &world.Presence{
		PlayerID: alice,
		Account:  acctAlice,
		Write:    func(string) error { return nil },
		Log:      logger,
	}
	bobPresence := &world.Presence{
		PlayerID: bob,
		Account:  acctBob,
		Write:    func(string) error { return nil },
		Log:      logger,
	}
	if _, err := w.Attach(alicePresence); err != nil {
		_ = db.Close()
		rootCancel()
		t.Fatalf("Attach(alice): %v", err)
	}
	if _, err := w.Attach(bobPresence); err != nil {
		_ = db.Close()
		rootCancel()
		t.Fatalf("Attach(bob): %v", err)
	}

	mgr := budget.NewManager(db, budget.Defaults{Minute: 5000, Hour: 50000, Day: 500000})

	stub := &m7StubTools{
		world:         w,
		alicePresence: alicePresence,
	}

	// defaults intentionally use the m7-fake backend so the npc.Registry
	// resolves the bartender's per-NPC backend to our pre-registered
	// *fake.Fake instance. backend_opts on the npc_config row carries
	// the _m7_key; defaults.Opts could be empty, but we pass the same
	// key anyway in case mergeOpts changes shape.
	defaults := config.LLMBackend{
		Backend: "m7-fake",
		Opts:    map[string]any{"_m7_key": key},
	}

	reg, err := npc.Load(rootCtx, db, w, defaults, logger, npc.LoopDeps{
		Bus:    bus,
		Budget: mgr,
		World:  m7Broadcaster{w: w},
		Tools:  stub,
		Config: npc.LoopConfig{Debounce: 30 * time.Millisecond, MaxToolDepth: 3},
	})
	if err != nil {
		_ = db.Close()
		rootCancel()
		t.Fatalf("npc.Load: %v", err)
	}

	// Pre-queue one YES for the gate. After it pops on the first gate
	// call, subsequent gate calls (driven by derivative events like the
	// KindTake the tool publishes) fall through to "NO" so the loop
	// silently exits without a second response-model round.
	f.Queue("gate", llm.Response{Content: "YES"})

	// Pre-queue the response model's scripted turn pair.
	// Round 1: response model returns a ToolCall for serve_drink with
	// the alice player id as the destination.
	f.Queue("response", llm.Response{
		ToolCalls: []llm.ToolCall{{
			ID:        "call-1",
			Name:      "serve_drink",
			Arguments: map[string]any{"to": int64(alice)},
		}},
		UsageIn: 10, UsageOut: 5,
	})
	// Round 2: response model returns plain content (no more tool calls).
	f.Queue("response", llm.Response{
		Content: "Here you go.",
		UsageIn: 8, UsageOut: 4,
	})

	setup := &m7Setup{
		World:         w,
		Bus:           bus,
		Budget:        mgr,
		LLM:           f,
		Tools:         stub,
		BartenderID:   bartenderID,
		AliceID:       alice,
		BobID:         bob,
		BeerID:        beerObjID,
		BarRoomID:     bar.ID,
		KitchenRoomID: kitchen.ID,
		AlicePresence: alicePresence,
		BobPresence:   bobPresence,
	}

	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = reg.Shutdown(shutdownCtx)
		bus.Close()
		_ = db.Close()
		rootCancel()
		m7FakesMu.Lock()
		delete(m7Fakes, key)
		m7FakesMu.Unlock()
	})

	// npc.Load's rebuild spawned per-NPC Loop goroutines via
	// loop.Manager.Add. Each goroutine subscribes to its room's bus
	// asynchronously inside Run; until it has subscribed, any Publish
	// for that room misses it. Give the scheduler a moment to settle
	// the subscription before the test publishes. 100 ms is generous
	// — the goroutine typically subscribes in microseconds — but the
	// CI scheduler is occasionally noisy and an over-budget here costs
	// nothing while a too-short wait causes false-negative tests.
	time.Sleep(100 * time.Millisecond)
	return setup
}

// mustObjIDBySlug resolves an object id by slug or fails the test.
func mustObjIDBySlug(t *testing.T, w *world.World, slug string) world.ObjectID {
	t.Helper()
	for _, o := range w.ListObjects() {
		if o.Slug == slug {
			return o.ID
		}
	}
	t.Fatalf("object with slug %q not in world", slug)
	return 0
}

// mustCreatePlayer creates an account and its player body, returning both.
func mustCreatePlayer(t *testing.T, ctx context.Context, a *auth.Store, w *world.World, username string) (*auth.Account, world.ObjectID) {
	t.Helper()
	acc, err := a.Create(ctx, username, "hunter22", auth.AccessPlayer)
	if err != nil {
		t.Fatalf("auth.Create(%q): %v", username, err)
	}
	id, err := w.CreatePlayer(ctx, acc)
	if err != nil {
		t.Fatalf("CreatePlayer(%q): %v", username, err)
	}
	return acc, id
}

// drainBus drains every event currently buffered in ch without blocking.
func drainBus(ch <-chan events.Event) {
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		default:
			return
		}
	}
}

// waitForBartenderSay blocks until a KindSay event with Actor == npcID
// arrives on ch, or timeout expires. Returns the event and true on
// success.
func waitForBartenderSay(ch <-chan events.Event, npcID world.ObjectID, timeout time.Duration) (events.Event, bool) {
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return events.Event{}, false
		}
		select {
		case e, ok := <-ch:
			if !ok {
				return events.Event{}, false
			}
			if e.Kind == events.KindSay && e.Actor == events.ObjectID(npcID) {
				return e, true
			}
		case <-time.After(remaining):
			return events.Event{}, false
		}
	}
}

// TestM7_LoopRoundTripsToolAndBroadcasts exercises the full M7 path: a
// player says something in the bar, the bartender's loop ticks, the
// response model returns a ToolCall, the tool moves the beer into the
// player's inventory, the second response chat call returns plain
// content, and that content is broadcast to the bar room only. The
// kitchen room sees nothing from the bartender.
func TestM7_LoopRoundTripsToolAndBroadcasts(t *testing.T) {
	s := setupM7World(t)

	barCh, cancelBar := s.Bus.Subscribe(events.RoomID(s.BarRoomID))
	defer cancelBar()
	kitchenCh, cancelKitchen := s.Bus.Subscribe(events.RoomID(s.KitchenRoomID))
	defer cancelKitchen()

	// Drain any Attach/move noise that landed on either channel before
	// our subscriptions started.
	drainBus(barCh)
	drainBus(kitchenCh)

	if err := s.World.Say(s.AlicePresence, "I'll have a beer"); err != nil {
		t.Fatalf("world.Say: %v", err)
	}

	got, ok := waitForBartenderSay(barCh, s.BartenderID, 3*time.Second)
	if !ok {
		t.Fatalf("timed out waiting for bartender KindSay on bar bus (gate=%d response=%d tools=%v)",
			s.LLM.Calls("gate"), s.LLM.Calls("response"), s.Tools.invokedSnapshot())
	}
	if got.Text != "Here you go." {
		t.Errorf("bartender broadcast text = %q, want %q", got.Text, "Here you go.")
	}

	invoked := s.Tools.invokedSnapshot()
	if len(invoked) != 1 || invoked[0] != "serve_drink" {
		t.Errorf("Tools.invoked = %v, want [serve_drink]", invoked)
	}

	inv := s.World.Inventory(s.AliceID)
	foundBeer := false
	for _, o := range inv {
		if o.ID == s.BeerID {
			foundBeer = true
			break
		}
	}
	if !foundBeer {
		t.Errorf("alice inventory does not contain beer (id=%d); got %v", s.BeerID, inv)
	}

	// Kitchen must be silent w.r.t. bartender activity. Brief wait to
	// catch any cross-room leak that races past the publish boundary.
	deadline := time.After(150 * time.Millisecond)
DRAIN:
	for {
		select {
		case e, ok := <-kitchenCh:
			if !ok {
				break DRAIN
			}
			if e.Actor == events.ObjectID(s.BartenderID) {
				t.Errorf("kitchen bus saw bartender event: %+v", e)
			}
		case <-deadline:
			break DRAIN
		}
	}

	if calls := s.LLM.Calls("response"); calls != 2 {
		t.Errorf("response model calls = %d, want 2 (ToolCall round-trip)", calls)
	}
	if calls := s.LLM.Calls("gate"); calls < 1 {
		t.Errorf("gate model calls = %d, want >= 1", calls)
	}
}

// TestM7_BudgetExhaustionSilencesNPC pre-exhausts the bartender's minute
// budget and asserts the loop ticks but neither gate nor response calls
// land, and no NPCSay broadcast reaches the bar.
func TestM7_BudgetExhaustionSilencesNPC(t *testing.T) {
	s := setupM7World(t)

	// Burn the full minute window with a single Record so the next
	// Allow(npcID, gateBudgetEstimate) returns false. The defaultTick
	// short-circuits at the top of the function before the gate call.
	s.Budget.Record(s.BartenderID, 5000, 0)

	barCh, cancelBar := s.Bus.Subscribe(events.RoomID(s.BarRoomID))
	defer cancelBar()
	drainBus(barCh)

	if err := s.World.Say(s.AlicePresence, "another beer"); err != nil {
		t.Fatalf("world.Say: %v", err)
	}

	// Wait well past the debounce window — long enough for a tick to
	// have fired and broadcast a reply if the budget had permitted it.
	if _, sawIt := waitForBartenderSay(barCh, s.BartenderID, 300*time.Millisecond); sawIt {
		t.Fatalf("bartender broadcast a reply despite exhausted budget")
	}

	if calls := s.LLM.Calls("gate"); calls != 0 {
		t.Errorf("gate model calls = %d, want 0 (budget should short-circuit before gate)", calls)
	}
	if calls := s.LLM.Calls("response"); calls != 0 {
		t.Errorf("response model calls = %d, want 0", calls)
	}
}
