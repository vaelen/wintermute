// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

// Package msgid issues FTS-0009 MSGIDs for locally-originated content.
// Each full domain-form origaddr has its own monotonic 32-bit counter,
// persisted in ftn_msgid_counters and mutated only through the store's
// single-writer goroutine — so concurrent callers see strictly
// increasing serials with no duplicates and no gaps.
package msgid

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/vaelen/wintermute/internal/ftn/addr"
	"github.com/vaelen/wintermute/internal/store"
)

// Issuer hands out MSGIDs.
type Issuer struct {
	db *store.DB
}

// NewIssuer constructs an Issuer backed by the given store.
func NewIssuer(db *store.DB) *Issuer {
	return &Issuer{db: db}
}

// Issue returns the next MSGID for origAddr. origAddr should be the
// full domain-form address (e.g. "1:234/5.0@fidonet"). The returned
// MSGID has Serial set to the bumped counter, OriginRaw set to
// origAddr, and Origin populated if origAddr parses as an Addr.
func (i *Issuer) Issue(ctx context.Context, origAddr string) (addr.MSGID, error) {
	if origAddr == "" {
		return addr.MSGID{}, errors.New("msgid: empty origaddr")
	}
	var serial uint32
	err := i.db.Write(ctx, func(tx *sql.Tx) error {
		var cur uint32
		err := tx.QueryRowContext(ctx,
			`SELECT next_serial FROM ftn_msgid_counters WHERE origaddr = ?`,
			origAddr).Scan(&cur)
		if errors.Is(err, sql.ErrNoRows) {
			cur = 1
		} else if err != nil {
			return fmt.Errorf("read counter: %w", err)
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO ftn_msgid_counters(origaddr, next_serial) VALUES (?, ?)
			ON CONFLICT(origaddr) DO UPDATE SET next_serial = excluded.next_serial
		`, origAddr, cur+1)
		if err != nil {
			return fmt.Errorf("bump counter: %w", err)
		}
		serial = cur
		return nil
	})
	if err != nil {
		return addr.MSGID{}, err
	}
	m := addr.MSGID{OriginRaw: origAddr, Serial: serial}
	if a, perr := addr.Parse(origAddr); perr == nil {
		m.Origin = a
	}
	return m, nil
}
