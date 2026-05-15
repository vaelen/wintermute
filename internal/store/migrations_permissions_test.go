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

	// New rows default to 0.
	if _, err := d.Read().Exec(
		`SELECT permissions FROM rooms WHERE slug='lobby' AND permissions = 0`,
	); err != nil {
		t.Errorf("lobby.permissions default 0 lookup failed: %v", err)
	}
}
