// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package store

import (
	"testing"
)

func TestNPCConfigSchema(t *testing.T) {
	d := tempDB(t)

	var name string
	if err := d.Read().QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='npc_config'`,
	).Scan(&name); err != nil {
		t.Fatalf("npc_config table missing: %v", err)
	}
	if name != "npc_config" {
		t.Errorf("got table name %q, want npc_config", name)
	}
}

func TestBartenderSeed(t *testing.T) {
	d := tempDB(t)

	var count int
	if err := d.Read().QueryRow(`SELECT COUNT(*) FROM npc_config`).Scan(&count); err != nil {
		t.Fatalf("count npc_config: %v", err)
	}
	if count != 1 {
		t.Fatalf("npc_config row count = %d, want 1", count)
	}

	var (
		slug       string
		kind       string
		backend    string
		maxContext int
	)
	if err := d.Read().QueryRow(
		`SELECT o.slug, o.kind, c.backend, c.max_context
		   FROM npc_config c
		   JOIN objects o ON o.id = c.object_id`,
	).Scan(&slug, &kind, &backend, &maxContext); err != nil {
		t.Fatalf("query bartender: %v", err)
	}
	if slug != "npc/bartender" {
		t.Errorf("slug = %q, want npc/bartender", slug)
	}
	if kind != "npc" {
		t.Errorf("kind = %q, want npc", kind)
	}
	if backend != "ollama" {
		t.Errorf("backend = %q, want ollama", backend)
	}
	if maxContext != 4096 {
		t.Errorf("max_context = %d, want 4096", maxContext)
	}

	var lobbyID, bartenderRoomID int64
	if err := d.Read().QueryRow(
		`SELECT id FROM rooms WHERE slug='lobby'`,
	).Scan(&lobbyID); err != nil {
		t.Fatalf("query lobby id: %v", err)
	}
	if err := d.Read().QueryRow(
		`SELECT room_id FROM object_locations
		  WHERE object_id = (SELECT id FROM objects WHERE slug='npc/bartender')`,
	).Scan(&bartenderRoomID); err != nil {
		t.Fatalf("query bartender location: %v", err)
	}
	if bartenderRoomID != lobbyID {
		t.Errorf("bartender room_id = %d, want lobby id %d", bartenderRoomID, lobbyID)
	}
}
