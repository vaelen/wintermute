// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package store

import (
	"testing"
)

func TestPermissionsColumns(t *testing.T) {
	d := tempDB(t)

	for _, table := range []string{"rooms", "objects"} {
		var name string
		err := d.Read().QueryRow(
			`SELECT name FROM pragma_table_info(?) WHERE name='permissions'`,
			table,
		).Scan(&name)
		if err != nil {
			t.Errorf("%s.permissions column missing: %v", table, err)
			continue
		}
		if name != "permissions" {
			t.Errorf("%s.permissions: got column name %q", table, name)
		}
	}

	// Existing seed rows pick up the column with its default value.
	var perms int
	if err := d.Read().QueryRow(
		`SELECT permissions FROM rooms WHERE slug='lobby'`,
	).Scan(&perms); err != nil {
		t.Errorf("query lobby permissions: %v", err)
	}
	if perms != 0 {
		t.Errorf("lobby.permissions = %d, want 0 (default)", perms)
	}
}
