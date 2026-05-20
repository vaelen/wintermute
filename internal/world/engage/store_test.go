// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package engage_test

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/vaelen/wintermute/internal/store"
	"github.com/vaelen/wintermute/internal/world"
	"github.com/vaelen/wintermute/internal/world/engage"
)

func newTestDB(t *testing.T) *store.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "engage.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := store.Open(context.Background(), path, logger)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// Load the world so the seed migrations run and the world cache is
	// populated. We don't use the returned world directly.
	if _, err := world.Load(context.Background(), db, logger); err != nil {
		t.Fatalf("world.Load: %v", err)
	}
	return db
}

func TestLoadHosts_returnsBackfilledNPCsAndSeedTerminal(t *testing.T) {
	db := newTestDB(t)
	hosts, err := engage.LoadHosts(context.Background(), db)
	if err != nil {
		t.Fatalf("LoadHosts: %v", err)
	}
	foundNPC, foundTerminal := false, false
	for _, h := range hosts {
		switch h.Kind {
		case engage.KindNPC:
			foundNPC = true
		case engage.KindTerminal:
			foundTerminal = true
		}
	}
	if !foundNPC {
		t.Error("expected at least one NPC host from backfill")
	}
	if !foundTerminal {
		t.Error("expected the seed lobby-terminal host")
	}
}

func TestLoadHosts_resolvesKindDefaults(t *testing.T) {
	db := newTestDB(t)
	hosts, err := engage.LoadHosts(context.Background(), db)
	if err != nil {
		t.Fatalf("LoadHosts: %v", err)
	}
	var npc *engage.Host
	for _, h := range hosts {
		if h.Kind == engage.KindNPC {
			npc = h
			break
		}
	}
	if npc == nil {
		t.Fatal("no NPC host found")
	}
	wantEngage := []string{"talk to", "address"}
	if !stringSlicesEqual(npc.EngageVerbs, wantEngage) {
		t.Errorf("NPC EngageVerbs = %v, want %v", npc.EngageVerbs, wantEngage)
	}
	wantDisengage := []string{"leave", "goodbye"}
	if !stringSlicesEqual(npc.DisengageVerbs, wantDisengage) {
		t.Errorf("NPC DisengageVerbs = %v, want %v", npc.DisengageVerbs, wantDisengage)
	}
	if npc.PresentMsg == "" {
		t.Error("NPC PresentMsg should default to non-empty")
	}
}

func TestLoadHosts_resolvesTerminalKindDefaults(t *testing.T) {
	db := newTestDB(t)
	hosts, err := engage.LoadHosts(context.Background(), db)
	if err != nil {
		t.Fatalf("LoadHosts: %v", err)
	}
	var term *engage.Host
	for _, h := range hosts {
		if h.Kind == engage.KindTerminal {
			term = h
			break
		}
	}
	if term == nil {
		t.Fatal("no terminal host found")
	}
	wantEngage := []string{"use", "sit at"}
	if !stringSlicesEqual(term.EngageVerbs, wantEngage) {
		t.Errorf("terminal EngageVerbs = %v, want %v", term.EngageVerbs, wantEngage)
	}
	// Migration 0023 explicitly overrides the kind-default list to
	// include "leave" and "exit" as well — players were leaning on
	// those words and got confused when only the kind-defaults
	// "stand up" / "step away" matched.
	wantDisengage := []string{"stand up", "step away", "leave", "exit"}
	if !stringSlicesEqual(term.DisengageVerbs, wantDisengage) {
		t.Errorf("terminal DisengageVerbs = %v, want %v", term.DisengageVerbs, wantDisengage)
	}
	if term.Prompt != "terminal> " {
		t.Errorf("terminal Prompt = %q, want %q", term.Prompt, "terminal> ")
	}
}

func TestHostCache_loadGetPutDelete(t *testing.T) {
	db := newTestDB(t)
	c := engage.NewHostCache()
	if err := c.Load(context.Background(), db); err != nil {
		t.Fatalf("Load: %v", err)
	}
	// At least the seed terminal and any seed NPCs should appear.
	var anyID world.ObjectID
	for _, h := range engageHosts(t, db) {
		if h.Kind == engage.KindTerminal {
			anyID = h.ObjectID
			break
		}
	}
	if anyID == 0 {
		t.Fatal("no terminal in seed")
	}
	got := c.Get(anyID)
	if got == nil || got.Kind != engage.KindTerminal {
		t.Errorf("Get(%d) = %v, want terminal", anyID, got)
	}
	// Put: replace with a custom host
	c.Put(&engage.Host{ObjectID: anyID, Kind: engage.KindCustom, Prompt: "x"})
	if got := c.Get(anyID); got == nil || got.Kind != engage.KindCustom {
		t.Errorf("after Put: Get(%d) = %v, want custom", anyID, got)
	}
	c.Delete(anyID)
	if got := c.Get(anyID); got != nil {
		t.Errorf("after Delete: Get(%d) = %v, want nil", anyID, got)
	}
}

// engageHosts is a small shortcut that re-runs LoadHosts for test data.
func engageHosts(t *testing.T, db *store.DB) []*engage.Host {
	t.Helper()
	hosts, err := engage.LoadHosts(context.Background(), db)
	if err != nil {
		t.Fatalf("LoadHosts: %v", err)
	}
	return hosts
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
