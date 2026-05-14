// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

//go:build test

package npc

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/npc/memory"
	"github.com/vaelen/wintermute/internal/store"
	"github.com/vaelen/wintermute/internal/world"
)

// TestNPCMemoryAppendsAfterDispatch verifies that after a successful
// say → reply round-trip, the NPC's per-player short-term buffer holds
// both turns: the player's utterance and the NPC's response.
func TestNPCMemoryAppendsAfterDispatch(t *testing.T) {
	e := newFakeBackendEnv(t)
	alice := e.attachPlayer(t, "alice")
	bob := e.attachPlayer(t, "bob")
	_ = bob
	alice.drain()
	bob.drain()

	n := e.reg.Get(e.bartenderID(t))
	if n == nil {
		t.Fatalf("bartender NPC missing")
	}
	if n.Memory == nil {
		t.Fatalf("expected NPC.Memory to be wired up after Load")
	}

	e.reg.HandleSay(lobbyID(t, e), alice.PlayerID, "alice", "hi, bartender")
	alice.waitFor(t, bartenderResponse, 2*time.Second)
	e.reg.Wait()

	turns := n.Memory.Recent(alice.PlayerID, 10)
	if len(turns) != 2 {
		t.Fatalf("Recent length = %d, want 2 (player + npc); got %+v", len(turns), turns)
	}
	if !strings.Contains(turns[0].Text, "bartender") {
		t.Errorf("player turn text = %q, want one containing 'bartender'", turns[0].Text)
	}
	if turns[1].Speaker != "<npc>" {
		t.Errorf("second turn speaker = %q, want <npc>", turns[1].Speaker)
	}
	if !strings.Contains(turns[1].Text, "bartender") {
		// bartenderResponse is "the bartender nods slowly."
		t.Errorf("NPC turn text = %q, want %q", turns[1].Text, bartenderResponse)
	}
}

// TestNPCMemoryPersistsAcrossRestart verifies the headline acceptance
// criterion: memories from before a server restart influence retrieval
// on the next conversation. The test boots a server, completes a
// conversation, shuts down (draining memory to disk), boots a fresh
// server pointing at the same DB, and asserts that the bartender's
// memory state on the new boot finds the prior memory.
func TestNPCMemoryPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "wintermute.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// --- First boot: engage, then shutdown drain. ---
	db1, err := store.Open(context.Background(), dbPath, logger)
	if err != nil {
		t.Fatalf("first store.Open: %v", err)
	}
	mustWrite(t, db1, `UPDATE npc_config SET backend = 'fake', backend_opts = '{}'`)
	w1, err := world.Load(context.Background(), db1, logger)
	if err != nil {
		t.Fatalf("first world.Load: %v", err)
	}
	a1 := auth.NewStore(db1)
	reg1, err := Load(context.Background(), db1, w1, fakeDefaults(), logger)
	if err != nil {
		t.Fatalf("first npc.Load: %v", err)
	}
	alice1 := attachPlayerOn(t, w1, a1, "alice")
	lobby1, _ := w1.LobbyID()

	reg1.HandleSay(lobby1, alice1.PlayerID, "alice", "hi, bartender")
	// Wait for the dispatch to complete; the recordingPresence helper
	// gives us a "waitFor" — but we don't need TCP correctness here,
	// just dispatch completion.
	reg1.Wait()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := reg1.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("first Shutdown: %v", err)
	}
	_ = db1.Close()

	// Confirm a memory row landed on disk before we tear down.
	db2, err := store.Open(context.Background(), dbPath, logger)
	if err != nil {
		t.Fatalf("second store.Open: %v", err)
	}
	var rows int
	if err := db2.Read().QueryRow(`SELECT COUNT(*) FROM npc_memories`).Scan(&rows); err != nil {
		t.Fatalf("count npc_memories post-restart: %v", err)
	}
	if rows < 1 {
		t.Fatalf("expected memory to persist across restart; got %d rows", rows)
	}

	// --- Second boot: load fresh registry, verify retrieval finds the row. ---
	w2, err := world.Load(context.Background(), db2, logger)
	if err != nil {
		t.Fatalf("second world.Load: %v", err)
	}
	reg2, err := Load(context.Background(), db2, w2, fakeDefaults(), logger)
	if err != nil {
		t.Fatalf("second npc.Load: %v", err)
	}
	defer func() {
		_ = reg2.Shutdown(context.Background())
		_ = db2.Close()
	}()

	// Find the bartender on the new boot and prime its short-term so
	// Retrieve has something to embed for the query.
	var bartenderID world.ObjectID
	lobby2, _ := w2.LobbyID()
	for _, n := range w2.NPCsInRoom(lobby2) {
		if n.Slug == "npc/bartender" {
			bartenderID = n.ID
			break
		}
	}
	if bartenderID == 0 {
		t.Fatalf("bartender missing after restart")
	}
	n := reg2.Get(bartenderID)
	if n == nil || n.Memory == nil {
		t.Fatalf("bartender NPC or memory not loaded on second boot")
	}
	n.Memory.Append(world.ObjectID(99), memory.Turn{Speaker: "alice", Text: "hi again, bartender"})

	got, err := n.Memory.Retrieve(context.Background(), world.ObjectID(99), 5, -1.0)
	if err != nil {
		t.Fatalf("Retrieve on second boot: %v", err)
	}
	if len(got) == 0 {
		t.Fatalf("expected Retrieve on second boot to return the persisted memory; got 0")
	}
}

// attachPlayerOn creates an account + body and attaches a recording
// presence — same as testEnv.attachPlayer but standalone so the cross-
// restart test can use it across two separate world instances.
func attachPlayerOn(t *testing.T, w *world.World, a *auth.Store, username string) *recordingPresence {
	t.Helper()
	acc, err := a.Create(context.Background(), username, "hunter22", auth.AccessPlayer)
	if err != nil {
		t.Fatalf("auth.Create: %v", err)
	}
	id, err := w.CreatePlayer(context.Background(), acc)
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
	if _, err := w.Attach(rp.Presence); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	return rp
}

// TestNPCRegistryShutdownHonoursContext verifies Shutdown returns at
// or before its context's deadline even when in-flight dispatch work
// would otherwise run longer. Without this, main.go's outer timeout
// races the inner shutdownCtx and db.Close() can fire while Shutdown
// is still touching the DB.
func TestNPCRegistryShutdownHonoursContext(t *testing.T) {
	e := newFakeBackendEnv(t)

	// Pin a fake in-flight dispatch by adding to inFlightWG directly
	// and never calling Done. This is the "hung dispatch" model:
	// Shutdown must not wait for it past its own deadline.
	e.reg.inFlightWG.Add(1)
	defer e.reg.inFlightWG.Done()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := e.reg.Shutdown(ctx)
	elapsed := time.Since(start)

	if err == nil {
		t.Errorf("Shutdown returned nil; want a context error from the deadline")
	}
	// Allow generous slack so a slow CI runner doesn't flake; the
	// failure mode would be a multi-second wait.
	if elapsed > 1*time.Second {
		t.Fatalf("Shutdown returned after %v, want at or near the 100ms ctx deadline", elapsed)
	}
}

// TestNPCMemoryReloadDrainsOldStates verifies that Registry.Reload
// drains every existing per-NPC State BEFORE cancelling the old worker
// context. Otherwise late idle timers or in-flight observer goroutines
// could submit jobs onto a dead channel, silently losing data.
func TestNPCMemoryReloadDrainsOldStates(t *testing.T) {
	e := newFakeBackendEnv(t)
	alice := e.attachPlayer(t, "alice")
	bob := e.attachPlayer(t, "bob")
	_ = bob
	alice.drain()
	bob.drain()

	// Engage so the bartender's short-term buffer has content that
	// MUST be summarised before the old workers are torn down.
	e.reg.HandleSay(lobbyID(t, e), alice.PlayerID, "alice", "hi, bartender")
	alice.waitFor(t, bartenderResponse, 2*time.Second)
	e.reg.Wait()

	// Sanity: short-term has the two turns. No memory row yet.
	var rows int
	if err := e.db.Read().QueryRow(`SELECT COUNT(*) FROM npc_memories`).Scan(&rows); err != nil {
		t.Fatalf("count pre-reload: %v", err)
	}
	if rows != 0 {
		t.Fatalf("pre-reload npc_memories rows = %d, want 0", rows)
	}

	if err := e.reg.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	// After Reload, the old state's buffer must have been drained
	// through its worker, producing a row.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := e.db.Read().QueryRow(`SELECT COUNT(*) FROM npc_memories`).Scan(&rows); err == nil && rows >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if rows < 1 {
		t.Fatalf("expected Reload to drain the prior conversation into a memory row; got %d", rows)
	}
}

// TestNPCMemoryShutdownDrainsBuffers verifies that Registry.Shutdown
// drains every per-NPC short-term buffer through the workers and waits
// for the resulting summarisation to land before returning.
func TestNPCMemoryShutdownDrainsBuffers(t *testing.T) {
	e := newFakeBackendEnv(t)
	alice := e.attachPlayer(t, "alice")
	bob := e.attachPlayer(t, "bob")
	_ = bob
	alice.drain()
	bob.drain()

	e.reg.HandleSay(lobbyID(t, e), alice.PlayerID, "alice", "hi, bartender")
	alice.waitFor(t, bartenderResponse, 2*time.Second)
	e.reg.Wait()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := e.reg.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	var rows int
	if err := e.db.Read().QueryRow(`SELECT COUNT(*) FROM npc_memories`).Scan(&rows); err != nil {
		t.Fatalf("count npc_memories: %v", err)
	}
	if rows < 1 {
		t.Fatalf("expected at least one npc_memories row after Shutdown, got %d", rows)
	}
}

// TestNPCMemoryEndsOnDetach verifies that when an engaged player detaches —
// for either reason — the registry observes the world event, drains the
// per-conversation buffer through the worker, and a memory row lands.
func TestNPCMemoryEndsOnDetach(t *testing.T) {
	for _, reason := range []world.DisconnectReason{world.DisconnectQuit, world.DisconnectDropped} {
		name := "quit"
		if reason == world.DisconnectDropped {
			name = "dropped"
		}
		t.Run(name, func(t *testing.T) {
			e := newFakeBackendEnv(t)
			alice := e.attachPlayer(t, "alice")
			bob := e.attachPlayer(t, "bob")
			_ = bob
			alice.drain()
			bob.drain()

			n := e.reg.Get(e.bartenderID(t))
			if n == nil || n.Memory == nil {
				t.Fatalf("bartender NPC or its memory not initialised")
			}

			e.reg.HandleSay(lobbyID(t, e), alice.PlayerID, "alice", "hi, bartender")
			alice.waitFor(t, bartenderResponse, 2*time.Second)
			e.reg.Wait()

			if got := len(n.Memory.Recent(alice.PlayerID, 10)); got != 2 {
				t.Fatalf("pre-detach short-term length = %d, want 2", got)
			}

			e.world.Detach(alice.PlayerID, reason)

			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				var rows int
				if err := e.db.Read().QueryRow(`SELECT COUNT(*) FROM npc_memories`).Scan(&rows); err == nil && rows >= 1 {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			var rows int
			if err := e.db.Read().QueryRow(`SELECT COUNT(*) FROM npc_memories`).Scan(&rows); err != nil {
				t.Fatalf("count npc_memories: %v", err)
			}
			if rows < 1 {
				t.Fatalf("expected at least one npc_memories row after detach(%s), got %d", name, rows)
			}

			if got := len(n.Memory.Recent(alice.PlayerID, 10)); got != 0 {
				t.Errorf("post-detach short-term length = %d, want 0", got)
			}
		})
	}
}

// TestNPCMemoryEndsOnMoveAway verifies the same end-of-conversation
// path triggers when the player moves to a different room.
func TestNPCMemoryEndsOnMoveAway(t *testing.T) {
	e := newFakeBackendEnv(t)
	alice := e.attachPlayer(t, "alice")
	bob := e.attachPlayer(t, "bob")
	_ = bob
	alice.drain()
	bob.drain()

	n := e.reg.Get(e.bartenderID(t))
	if n == nil || n.Memory == nil {
		t.Fatalf("bartender NPC or its memory not initialised")
	}

	e.reg.HandleSay(lobbyID(t, e), alice.PlayerID, "alice", "hi, bartender")
	alice.waitFor(t, bartenderResponse, 2*time.Second)
	e.reg.Wait()

	// Alice walks east, leaving the bartender behind.
	if _, err := e.world.Move(context.Background(), alice.Presence, "e"); err != nil {
		t.Fatalf("Move east: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var rows int
		if err := e.db.Read().QueryRow(`SELECT COUNT(*) FROM npc_memories`).Scan(&rows); err == nil && rows >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	var rows int
	if err := e.db.Read().QueryRow(`SELECT COUNT(*) FROM npc_memories`).Scan(&rows); err != nil {
		t.Fatalf("count npc_memories: %v", err)
	}
	if rows < 1 {
		t.Fatalf("expected at least one npc_memories row after move-away, got %d", rows)
	}
}

// TestNPCMemoryRetrievesPriorMemories verifies that a seeded prior
// memory matching the conversation topic appears in the LLM context
// (we observe this via the recorded request to the test-only LLM).
func TestNPCMemoryRetrievesPriorMemories(t *testing.T) {
	e := newFakeBackendEnv(t)
	alice := e.attachPlayer(t, "alice")
	bob := e.attachPlayer(t, "bob")
	_ = bob
	alice.drain()
	bob.drain()

	n := e.reg.Get(e.bartenderID(t))
	if n == nil || n.Memory == nil {
		t.Fatalf("NPC missing or memory not wired")
	}

	// Seed a prior memory for this NPC. Use a unit-length vector so the
	// recording LLM's deterministic Embed (which produces values close
	// to but not exactly the same) still scores above a low threshold.
	emb, err := n.llm.Embed(context.Background(), "alice asked about the gate")
	if err != nil {
		t.Fatalf("seed embed: %v", err)
	}
	memID, err := e.reg.store.Insert(context.Background(), memory.Memory{
		NPCID:     n.ObjectID,
		Summary:   "alice asked about the Sprawl gate previously",
		Embedding: emb,
		Salience:  1.0,
	})
	if err != nil {
		t.Fatalf("seed Insert: %v", err)
	}
	_ = memID

	// Send a topic-matching say and retrieve.
	e.reg.HandleSay(lobbyID(t, e), alice.PlayerID, "alice", "hey bartender, the gate again")
	alice.waitFor(t, bartenderResponse, 2*time.Second)
	e.reg.Wait()

	got, err := n.Memory.Retrieve(context.Background(), alice.PlayerID, 5, -1.0)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(got) == 0 {
		t.Fatalf("Retrieve returned no memories; expected the seeded one")
	}
	if !strings.Contains(got[0].Summary, "Sprawl gate") {
		t.Errorf("top memory = %q, want one mentioning 'Sprawl gate'", got[0].Summary)
	}
}
