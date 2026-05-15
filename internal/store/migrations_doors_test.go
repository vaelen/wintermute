// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"database/sql"
	"testing"
)

func TestDoorsSchema(t *testing.T) {
	d := tempDB(t)

	for _, table := range []string{"doors"} {
		var name string
		if err := d.Read().QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`,
			table,
		).Scan(&name); err != nil {
			t.Fatalf("%s table missing: %v", table, err)
		}
	}

	// exits table must be gone after 0009.
	var existsName string
	err := d.Read().QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='exits'`,
	).Scan(&existsName)
	if err == nil {
		t.Errorf("exits table should be dropped by migration 0009")
	}
}

func TestObjectsAcceptsDoorKind(t *testing.T) {
	d := tempDB(t)
	ctx := context.Background()

	// Inserting an object with kind='door' must succeed (CHECK includes it).
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO objects(slug, name, kind) VALUES ('door-test', 'a test door', 'door')`,
		)
		return err
	}); err != nil {
		t.Fatalf("insert door object: %v", err)
	}

	// Inserting an unknown kind must still fail.
	err := d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO objects(slug, name, kind) VALUES ('bad-kind', 'bad', 'nonsense')`,
		)
		return err
	})
	if err == nil {
		t.Errorf("unknown kind should be rejected by CHECK constraint")
	}
}

func TestExitsSeedMigratedToDoors(t *testing.T) {
	d := tempDB(t)

	// The M2 seed inserts five exits. After migration 0009 they should be
	// present as five door objects with matching doors rows.
	var doorObjs int
	if err := d.Read().QueryRow(
		`SELECT COUNT(*) FROM objects WHERE kind='door'`,
	).Scan(&doorObjs); err != nil {
		t.Fatalf("count door objects: %v", err)
	}
	if doorObjs != 5 {
		t.Errorf("door object count = %d, want 5 (seeded exits)", doorObjs)
	}

	var doorRows int
	if err := d.Read().QueryRow(`SELECT COUNT(*) FROM doors`).Scan(&doorRows); err != nil {
		t.Fatalf("count doors: %v", err)
	}
	if doorRows != 5 {
		t.Errorf("doors row count = %d, want 5", doorRows)
	}

	// Default templates are populated.
	var leave, arrive string
	if err := d.Read().QueryRow(
		`SELECT leave_msg, arrive_msg FROM doors LIMIT 1`,
	).Scan(&leave, &arrive); err != nil {
		t.Fatalf("read door templates: %v", err)
	}
	if leave != "{actor} leaves {direction}." {
		t.Errorf("default leave_msg = %q", leave)
	}
	if arrive != "{actor} arrives." {
		t.Errorf("default arrive_msg = %q", arrive)
	}

	// Each (from_room, direction) is unique.
	var dupes int
	if err := d.Read().QueryRow(
		`SELECT COUNT(*) FROM (
		    SELECT from_room, direction, COUNT(*) AS n
		      FROM doors
		     GROUP BY from_room, direction
		    HAVING n > 1
		 )`,
	).Scan(&dupes); err != nil {
		t.Fatalf("dupe check: %v", err)
	}
	if dupes != 0 {
		t.Errorf("doors has %d duplicate (from_room, direction) rows", dupes)
	}
}
