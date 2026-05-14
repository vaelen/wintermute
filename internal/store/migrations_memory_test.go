// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package store

import (
	"strings"
	"testing"
)

// TestNPCMemoriesSchema asserts that migration 0006 creates the
// npc_memories table with the columns and index the memory layer relies
// on. The test queries sqlite_master rather than introspecting via
// PRAGMA so the assertions stay readable at a glance.
func TestNPCMemoriesSchema(t *testing.T) {
	d := tempDB(t)

	var sqlText string
	if err := d.Read().QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='npc_memories'`,
	).Scan(&sqlText); err != nil {
		t.Fatalf("npc_memories table missing: %v", err)
	}

	for _, col := range []string{
		"npc_id", "summary", "embedding", "created_at", "salience",
	} {
		if !strings.Contains(sqlText, col) {
			t.Errorf("npc_memories schema missing column %q\nSQL: %s", col, sqlText)
		}
	}

	var indexName string
	if err := d.Read().QueryRow(
		`SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='npc_memories' AND name='idx_npc_memories_npc'`,
	).Scan(&indexName); err != nil {
		t.Errorf("idx_npc_memories_npc index missing: %v", err)
	}
}
