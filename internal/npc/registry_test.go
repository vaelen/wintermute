// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

//go:build test

package npc

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/config"
	_ "github.com/vaelen/wintermute/internal/llm/fake"
	"github.com/vaelen/wintermute/internal/store"
	"github.com/vaelen/wintermute/internal/world"
	"github.com/vaelen/wintermute/internal/world/events"
)

const bartenderResponse = "the bartender nods slowly."

// fakeDefaults pins every NPC at construction time to the test-only fake
// backend with a deterministic response keyed off "someone said". The
// loop renders observations as "- someone said: <text>" so any incoming
// utterance routes to bartenderResponse via the substring match.
func fakeDefaults() config.LLMBackend {
	return config.LLMBackend{
		Backend: "fake",
		Opts: map[string]any{
			"responses": map[string]any{
				"someone said": bartenderResponse,
			},
			"default": "(no idea)",
		},
	}
}

type recordingPresence struct {
	*world.Presence
	mu  sync.Mutex
	buf []string
}

func (r *recordingPresence) snapshot() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.buf, "")
}

func (r *recordingPresence) drain() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := strings.Join(r.buf, "")
	r.buf = nil
	return out
}

func (r *recordingPresence) waitFor(t *testing.T, needle string, budget time.Duration) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if strings.Contains(r.snapshot(), needle) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q in:\n%s", needle, r.snapshot())
}

func (r *recordingPresence) assertSilent(t *testing.T, budget time.Duration) {
	t.Helper()
	time.Sleep(budget)
	if got := r.drain(); got != "" {
		t.Fatalf("expected no output within %v, got:\n%s", budget, got)
	}
}

// loopBroadcaster adapts *world.World to loop.Broadcaster for tests.
type loopBroadcaster struct{ w *world.World }

func (b loopBroadcaster) NPCSay(npcID events.ObjectID, text string) error {
	return b.w.NPCSay(world.ObjectID(npcID), text)
}

type testEnv struct {
	db    *store.DB
	auth  *auth.Store
	world *world.World
	reg   *Registry
	bus   events.Bus
}

// newFakeBackendEnv builds a test environment whose seed NPC is forced to
// use the fake backend, with a real events.Bus wired so the per-NPC
// loop goroutine drives the say→reply round-trip.
func newFakeBackendEnv(t *testing.T) *testEnv {
	t.Helper()
	return newEnv(t, true, fakeDefaults(), true)
}

// newRawEnv builds an environment with the seed NPC's backend untouched —
// "ollama", which the test build does NOT register, so the bartender's
// `llm` ends up nil. Used for the "no llm => no loop started" path.
// Also wires a bus so the loop infrastructure is exercised.
func newRawEnv(t *testing.T) *testEnv {
	t.Helper()
	return newEnv(t, false, fakeDefaults(), true)
}

func newEnv(t *testing.T, switchBackendToFake bool, defaults config.LLMBackend, withBus bool) *testEnv {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	path := filepath.Join(t.TempDir(), "npc.db")
	db, err := store.Open(context.Background(), path, logger)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if switchBackendToFake {
		mustWrite(t, db, `UPDATE npc_config SET backend = 'fake', backend_opts = '{}'`)
	}
	w, err := world.Load(context.Background(), db, logger)
	if err != nil {
		t.Fatalf("world.Load: %v", err)
	}

	var bus events.Bus
	deps := LoopDeps{}
	if withBus {
		bus = events.NewMemBus()
		w.SetBus(bus)
		deps = LoopDeps{
			Bus:    bus,
			World:  loopBroadcaster{w: w},
			Config: LoopConfig{Debounce: 30 * time.Millisecond},
		}
	}

	r, err := Load(context.Background(), db, w, defaults, logger, deps)
	if err != nil {
		t.Fatalf("npc.Load: %v", err)
	}
	w.SetSayObserver(func(roomID world.RoomID, speakerID world.ObjectID, speakerName, text string) {
		r.HandleSay(roomID, speakerID, speakerName, text)
	})
	// Cleanup order matters: stop loops + drain memory first, then close
	// the bus. Otherwise a loop goroutine mid-NPCSay races bus.Close.
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = r.Shutdown(shutdownCtx)
		if bus != nil {
			bus.Close()
		}
	})
	return &testEnv{db: db, auth: auth.NewStore(db), world: w, reg: r, bus: bus}
}

func (e *testEnv) attachPlayer(t *testing.T, username string) *recordingPresence {
	t.Helper()
	acc, err := e.auth.Create(context.Background(), username, "hunter22", auth.AccessPlayer)
	if err != nil {
		t.Fatalf("auth.Create: %v", err)
	}
	id, err := e.world.CreatePlayer(context.Background(), acc)
	if err != nil {
		t.Fatalf("CreatePlayer: %v", err)
	}
	rp := &recordingPresence{}
	rp.Presence = &world.Presence{
		PlayerID: id,
		Account:  acc,
		Write: func(s string) error {
			rp.mu.Lock()
			rp.buf = append(rp.buf, s)
			rp.mu.Unlock()
			return nil
		},
	}
	if _, err := e.world.Attach(rp.Presence); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	return rp
}

func (e *testEnv) bartenderID(t *testing.T) world.ObjectID {
	t.Helper()
	lobby, err := e.world.LobbyID()
	if err != nil {
		t.Fatalf("LobbyID: %v", err)
	}
	for _, n := range e.world.NPCsInRoom(lobby) {
		if n.Slug == "npc/bartender" {
			return n.ID
		}
	}
	t.Fatalf("bartender NPC missing from lobby")
	return 0
}

func mustWrite(t *testing.T, db *store.DB, query string, args ...any) {
	t.Helper()
	if err := db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(query, args...)
		return err
	}); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func TestLoadRegistersBartender(t *testing.T) {
	e := newFakeBackendEnv(t)
	id := e.bartenderID(t)
	n := e.reg.Get(id)
	if n == nil {
		t.Fatalf("Get(%d) = nil", id)
	}
	if n.Name != "the bartender" {
		t.Errorf("Name = %q, want \"the bartender\"", n.Name)
	}
	if n.Backend != "fake" {
		t.Errorf("Backend = %q, want fake (after switch)", n.Backend)
	}
	if !strings.Contains(n.Persona, "bartender") {
		t.Errorf("persona missing expected text; got %q", n.Persona)
	}
	if n.MaxContext != 4096 {
		t.Errorf("MaxContext = %d, want 4096", n.MaxContext)
	}
	if n.llm == nil {
		t.Errorf("expected llm to be non-nil for fake-backed NPC")
	}
}

func TestLoadGracefulOnUnregisteredBackend(t *testing.T) {
	// Seed backend is "ollama" which isn't registered under build tag test.
	// Load must succeed; the NPC must be present with llm == nil. With a
	// nil llm the registry does not start a loop for this NPC.
	e := newRawEnv(t)
	id := e.bartenderID(t)
	n := e.reg.Get(id)
	if n == nil {
		t.Fatalf("Get(%d) = nil", id)
	}
	if n.llm != nil {
		t.Errorf("expected llm == nil when backend %q is unregistered", n.Backend)
	}

	// HandleSay must not crash. With no engagement and no loop, the
	// player sees only their own Say echo.
	alice := e.attachPlayer(t, "alice")
	alice.drain()
	e.reg.HandleSay(lobbyID(t, e), alice.PlayerID, "alice", "hi, bartender")
	// Brief grace then ensure no bartender reply appeared.
	time.Sleep(200 * time.Millisecond)
	if strings.Contains(alice.drain(), "bartender nods") {
		t.Errorf("expected no bartender reply when llm is nil")
	}
}

func lobbyID(t *testing.T, e *testEnv) world.RoomID {
	t.Helper()
	id, err := e.world.LobbyID()
	if err != nil {
		t.Fatalf("LobbyID: %v", err)
	}
	return id
}

// TestLoopRespondsViaBus is the bus-driven counterpart to M3's
// TestHandleSayAddressedByName: a player Says something, the world
// publishes a KindSay event, the bartender's loop observes it after
// the debounce, calls Chat, and broadcasts the reply via NPCSay.
func TestLoopRespondsViaBus(t *testing.T) {
	e := newFakeBackendEnv(t)
	alice := e.attachPlayer(t, "alice")
	alice.drain()

	if err := e.world.Say(alice.Presence, "hi bartender"); err != nil {
		t.Fatalf("Say: %v", err)
	}
	alice.waitFor(t, bartenderResponse, 3*time.Second)
}

func TestReloadRebuildsAfterPersonaUpdate(t *testing.T) {
	e := newFakeBackendEnv(t)
	id := e.bartenderID(t)
	before := e.reg.Get(id)
	if before == nil {
		t.Fatalf("Get pre-reload returned nil")
	}
	originalPersona := before.Persona

	const newPersona = "I AM RELOADED PERSONA, hear me roar."
	mustWrite(t, e.db, `UPDATE npc_config SET persona = ? WHERE object_id = ?`,
		newPersona, int64(id))

	if err := e.reg.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	after := e.reg.Get(id)
	if after == nil {
		t.Fatalf("Get post-reload returned nil")
	}
	if after.Persona != newPersona {
		t.Errorf("Persona after reload = %q, want %q", after.Persona, newPersona)
	}
	if after.Persona == originalPersona {
		t.Errorf("persona unchanged across reload")
	}
}
