// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package store

import (
	"database/sql"
	"testing"
)

func TestM6MigrationsApply(t *testing.T) {
	d := tempDB(t)
	for _, table := range []string{
		"ftn_networks", "mail", "boards", "board_posts", "board_reads",
		"files", "file_acls", "file_tokens",
	} {
		var name string
		err := d.Read().QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`,
			table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %s missing: %v", table, err)
		}
	}
}

func TestFTNNetworksDefaultUniqueIndex(t *testing.T) {
	d := tempDB(t)
	var idx string
	if err := d.Read().QueryRow(
		`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_ftn_networks_default'`,
	).Scan(&idx); err != nil {
		t.Fatalf("idx_ftn_networks_default missing: %v", err)
	}
}

func TestBoardsAreaTagUniquePerNetwork(t *testing.T) {
	d := tempDB(t)
	ctx := t.Context()

	var net1, net2 int64
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx,
			`INSERT INTO ftn_networks(slug, name, domain, our_addr, is_default)
			 VALUES ('local','Local','local','255:255/255.0',1)`)
		if err != nil {
			return err
		}
		if net1, err = r.LastInsertId(); err != nil {
			return err
		}
		r, err = tx.ExecContext(ctx,
			`INSERT INTO ftn_networks(slug, name, domain, our_addr, is_default)
			 VALUES ('fsxnet','fsxNet','fsxnet','21:1/100.0',0)`)
		if err != nil {
			return err
		}
		net2, err = r.LastInsertId()
		return err
	}); err != nil {
		t.Fatalf("seed networks: %v", err)
	}

	// Same area_tag on different networks must coexist.
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO boards(slug, name, network_id, area_tag) VALUES ('a','A',?,'GENERAL')`,
			net1,
		); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO boards(slug, name, network_id, area_tag) VALUES ('b','B',?,'GENERAL')`,
			net2,
		)
		return err
	}); err != nil {
		t.Fatalf("same area_tag on different networks should be allowed: %v", err)
	}

	// Same area_tag on the same network must collide.
	err := d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO boards(slug, name, network_id, area_tag) VALUES ('c','C',?,'GENERAL')`,
			net1,
		)
		return err
	})
	if err == nil {
		t.Errorf("duplicate area_tag on the same network should have failed")
	}
}

func TestFTNNetworksOnlyOneDefault(t *testing.T) {
	d := tempDB(t)
	ctx := t.Context()
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO ftn_networks(slug, name, domain, our_addr, is_default)
			 VALUES ('a','A','a','1:1/1.0',1)`)
		return err
	}); err != nil {
		t.Fatalf("first default insert: %v", err)
	}
	err := d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO ftn_networks(slug, name, domain, our_addr, is_default)
			 VALUES ('b','B','b','2:2/2.0',1)`)
		return err
	})
	if err == nil {
		t.Errorf("second default insert should have failed; partial unique index missing?")
	}
}

func TestBoardPostsFTNFidelityColumns(t *testing.T) {
	d := tempDB(t)
	cols := tableColumns(t, d, "board_posts")
	for _, want := range []string{
		"msgid", "reply_to_msgid", "thread_root_msgid", "origin_addr",
		"area_tag", "attributes", "charset", "pid", "tz_offset",
		"tearline", "origin_line", "seen_by", "path", "kludges",
		"network_id", "author_name",
	} {
		if _, ok := cols[want]; !ok {
			t.Errorf("board_posts missing column %q", want)
		}
	}
}

func TestMailFTNFidelityColumns(t *testing.T) {
	d := tempDB(t)
	cols := tableColumns(t, d, "mail")
	for _, want := range []string{
		"msgid", "reply_to_msgid", "origin_addr",
		"from_name", "to_name", "from_addr", "to_addr",
		"attributes", "charset", "pid", "tz_offset", "kludges",
		"network_id",
	} {
		if _, ok := cols[want]; !ok {
			t.Errorf("mail missing column %q", want)
		}
	}
}

func tableColumns(t *testing.T, d *DB, table string) map[string]string {
	t.Helper()
	rows, err := d.Read().Query(`SELECT name, type FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatalf("pragma_table_info %s: %v", table, err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var n, ty string
		if err := rows.Scan(&n, &ty); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[n] = ty
	}
	return out
}
