// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

//go:build test

package npc

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/config"
	"github.com/vaelen/wintermute/internal/llm"
	_ "github.com/vaelen/wintermute/internal/llm/fake"
	"github.com/vaelen/wintermute/internal/store"
	"github.com/vaelen/wintermute/internal/world"
)

const bartenderResponse = "the bartender nods slowly."

// fakeDefaults pins every NPC at construction time to the test-only fake
// backend with a deterministic response keyed off "says". The substring
// "says" appears in HandleSay's user-message template ("<who> says: ...")
// so any incoming utterance routes to bartenderResponse.
func fakeDefaults() config.LLMBackend {
	return config.LLMBackend{
		Backend: "fake",
		Opts: map[string]any{
			"responses": map[string]any{
				"says": bartenderResponse,
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

type testEnv struct {
	db    *store.DB
	auth  *auth.Store
	world *world.World
	reg   *Registry
}

// newFakeBackendEnv builds a test environment whose seed NPC is forced to
// use the fake backend. Use this when the test wants HandleSay to drive a
// real Chat round-trip.
func newFakeBackendEnv(t *testing.T) *testEnv {
	t.Helper()
	return newEnv(t, true, fakeDefaults())
}

// newRawEnv builds an environment with the seed NPC's backend untouched —
// "ollama", which the test build does NOT register, so the bartender's
// `llm` ends up nil. Useful for testing the "no llm => no broadcast" path
// and for asserting that Load logs and continues on Open failures.
func newRawEnv(t *testing.T) *testEnv {
	t.Helper()
	return newEnv(t, false, fakeDefaults())
}

func newEnv(t *testing.T, switchBackendToFake bool, defaults config.LLMBackend) *testEnv {
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
	r, err := Load(context.Background(), db, w, defaults, logger)
	if err != nil {
		t.Fatalf("npc.Load: %v", err)
	}
	return &testEnv{db: db, auth: auth.NewStore(db), world: w, reg: r}
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
	// Load must succeed; the NPC must be present with llm == nil.
	e := newRawEnv(t)
	id := e.bartenderID(t)
	n := e.reg.Get(id)
	if n == nil {
		t.Fatalf("Get(%d) = nil", id)
	}
	if n.llm != nil {
		t.Errorf("expected llm == nil when backend %q is unregistered", n.Backend)
	}

	// HandleSay must not crash and must not broadcast anything.
	alice := e.attachPlayer(t, "alice")
	alice.drain()
	e.reg.HandleSay(lobbyID(t, e), alice.PlayerID, "alice", "hi, bartender")
	alice.assertSilent(t, 200*time.Millisecond)
}

func lobbyID(t *testing.T, e *testEnv) world.RoomID {
	t.Helper()
	id, err := e.world.LobbyID()
	if err != nil {
		t.Fatalf("LobbyID: %v", err)
	}
	return id
}

func TestHandleSayAddressedByName(t *testing.T) {
	e := newFakeBackendEnv(t)
	alice := e.attachPlayer(t, "alice")
	bob := e.attachPlayer(t, "bob") // a second player so rule (b) cannot fire.
	_ = bob
	alice.drain()
	bob.drain()

	e.reg.HandleSay(lobbyID(t, e), alice.PlayerID, "alice", "hi, bartender")
	alice.waitFor(t, bartenderResponse, 2*time.Second)
}

func TestHandleSayAddressedAsSoleEntity(t *testing.T) {
	e := newFakeBackendEnv(t)
	alice := e.attachPlayer(t, "alice")
	alice.drain()

	e.reg.HandleSay(lobbyID(t, e), alice.PlayerID, "alice", "hi")
	alice.waitFor(t, bartenderResponse, 2*time.Second)
}

func TestHandleSayNotAddressedWhenAnotherPlayerPresent(t *testing.T) {
	e := newFakeBackendEnv(t)
	alice := e.attachPlayer(t, "alice")
	bob := e.attachPlayer(t, "bob")
	alice.drain()
	bob.drain()

	e.reg.HandleSay(lobbyID(t, e), alice.PlayerID, "alice", "hi")
	alice.assertSilent(t, 200*time.Millisecond)
	bob.assertSilent(t, 1*time.Millisecond) // Drain again; shouldn't have changed.
}

// erroringLLM returns a Chat error every time. Used to cover the LLM-error
// path of HandleSay.
type erroringLLM struct{}

func (erroringLLM) Chat(context.Context, []llm.Message, []llm.ToolDef, llm.ChatOpts) (llm.Response, error) {
	return llm.Response{}, errors.New("simulated chat failure")
}

func (erroringLLM) Embed(context.Context, string) ([]float32, error) {
	return nil, errors.New("simulated embed failure")
}

func TestHandleSayLLMErrorDoesNotCrash(t *testing.T) {
	e := newFakeBackendEnv(t)
	id := e.bartenderID(t)
	n := e.reg.Get(id)
	if n == nil {
		t.Fatalf("Get(%d) = nil", id)
	}
	// Patch the llm under the per-NPC mutex so dispatch sees the new one.
	n.mu.Lock()
	n.llm = erroringLLM{}
	n.mu.Unlock()

	alice := e.attachPlayer(t, "alice")
	alice.drain()
	e.reg.HandleSay(lobbyID(t, e), alice.PlayerID, "alice", "hi, bartender")
	alice.assertSilent(t, 200*time.Millisecond)
}

func TestHandleSayHistoryAccumulatesAndTrims(t *testing.T) {
	e := newFakeBackendEnv(t)
	id := e.bartenderID(t)
	alice := e.attachPlayer(t, "alice")

	for i := 0; i < historyWindow+2; i++ {
		alice.drain()
		e.reg.HandleSay(lobbyID(t, e), alice.PlayerID, "alice", "hi, bartender")
		alice.waitFor(t, bartenderResponse, 2*time.Second)
	}

	n := e.reg.Get(id)
	n.mu.Lock()
	defer n.mu.Unlock()
	if got := len(n.history); got != 2*historyWindow {
		t.Errorf("history length = %d, want %d after over-filling", got, 2*historyWindow)
	}
	// Each window slot is a (user, assistant) pair; oldest pair must be
	// dropped as the window trims from the front.
	if n.history[0].Role != llm.RoleUser {
		t.Errorf("oldest entry role = %q, want %q", n.history[0].Role, llm.RoleUser)
	}
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

func TestNameMatchingWholeWord(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"hi, bartender", true},
		{"BARTENDER!", true},
		{"the BarTender, please", true},
		{"bartenderly speaking", false},
		{"foobartenderbar", false},
		{"i need a barten der", false},
		{"hi", false},
	}
	for _, tc := range cases {
		got := nameInText("the bartender", tc.text)
		if got != tc.want {
			t.Errorf("nameInText(\"the bartender\", %q) = %v, want %v",
				tc.text, got, tc.want)
		}
	}
}

func TestNameMatchingDoesNotFireOnStopword(t *testing.T) {
	// Even though "the" is in the NPC's name, a sentence containing only
	// "the" should not match — stopwords are stripped before comparison.
	if nameInText("the bartender", "I left the keycard at home") {
		t.Errorf("nameInText matched on the stopword \"the\"")
	}
}

// blockingLLM blocks Chat until the release channel is closed, then
// returns a fixed response. Used to assert Registry.Wait() actually
// blocks while a dispatch is in flight.
type blockingLLM struct {
	release  chan struct{}
	started  chan struct{}
	response string
}

func newBlockingLLM(response string) *blockingLLM {
	return &blockingLLM{
		release:  make(chan struct{}),
		started:  make(chan struct{}, 1),
		response: response,
	}
}

func (b *blockingLLM) Chat(ctx context.Context, _ []llm.Message, _ []llm.ToolDef, _ llm.ChatOpts) (llm.Response, error) {
	select {
	case b.started <- struct{}{}:
	default:
	}
	select {
	case <-b.release:
		return llm.Response{Content: b.response}, nil
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
}

func (b *blockingLLM) Embed(context.Context, string) ([]float32, error) {
	return nil, errors.New("embed not implemented")
}

func TestRegistryWaitBlocksUntilDispatchCompletes(t *testing.T) {
	e := newFakeBackendEnv(t)
	id := e.bartenderID(t)
	n := e.reg.Get(id)
	if n == nil {
		t.Fatalf("Get(%d) = nil", id)
	}

	blocker := newBlockingLLM("the bartender stirs.")
	n.mu.Lock()
	n.llm = blocker
	n.mu.Unlock()

	alice := e.attachPlayer(t, "alice")
	alice.drain()
	e.reg.HandleSay(lobbyID(t, e), alice.PlayerID, "alice", "hi, bartender")

	// Confirm the dispatch goroutine actually entered Chat.
	select {
	case <-blocker.started:
	case <-time.After(2 * time.Second):
		t.Fatalf("dispatch goroutine never reached Chat")
	}

	// Wait() must NOT return while Chat is blocked.
	waitDone := make(chan struct{})
	go func() {
		e.reg.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
		t.Fatalf("Registry.Wait() returned while a dispatch was in flight")
	case <-time.After(100 * time.Millisecond):
	}

	// Release the LLM; Wait() must return promptly.
	close(blocker.release)
	select {
	case <-waitDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("Registry.Wait() did not return after Chat completed")
	}
}

func TestHandleSayWholeWordViaLiveDispatch(t *testing.T) {
	e := newFakeBackendEnv(t)
	alice := e.attachPlayer(t, "alice")
	bob := e.attachPlayer(t, "bob") // disable rule (b) so only rule (a) can fire
	alice.drain()
	bob.drain()

	// "bartenderly" must NOT trigger.
	e.reg.HandleSay(lobbyID(t, e), alice.PlayerID, "alice",
		"speaking bartenderly today")
	alice.assertSilent(t, 200*time.Millisecond)

	// "BARTENDER!" must trigger.
	alice.drain()
	bob.drain()
	e.reg.HandleSay(lobbyID(t, e), alice.PlayerID, "alice",
		"BARTENDER!")
	alice.waitFor(t, bartenderResponse, 2*time.Second)
}
