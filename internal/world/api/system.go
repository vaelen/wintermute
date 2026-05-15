// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"database/sql"
)

const motdKey = "motd"

// Broadcast sends a message to every attached session. Empty messages
// are ignored.
func (a *API) Broadcast(msg string) {
	a.World.Broadcast(msg)
}

// SetMOTD persists a new message of the day; it will be shown to new
// sessions on login. The current implementation stores it in the
// schema_version-adjacent `kv` row under a "motd" key. If the kv table
// does not exist (it is created lazily here), it is added.
func (a *API) SetMOTD(ctx context.Context, msg string) error {
	if err := a.DB.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`CREATE TABLE IF NOT EXISTS kv (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO kv(key, value) VALUES (?, ?)
			   ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
			motdKey, msg,
		)
		return err
	}); err != nil {
		return errorf(CodeInternal, "set motd: %v", err)
	}
	return nil
}

// GetMOTD returns the current MOTD, or "" if none has been set.
func (a *API) GetMOTD() string {
	var v string
	err := a.DB.Read().QueryRow(`SELECT value FROM kv WHERE key = ?`, motdKey).Scan(&v)
	if err != nil {
		return ""
	}
	return v
}

// Log writes a message at INFO via the API's logger. Used by
// wintermute.system.log from Lua scripts.
func (a *API) Log(msg string, args ...any) {
	a.Logger.Info(msg, args...)
}
