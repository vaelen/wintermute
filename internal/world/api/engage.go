// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/vaelen/wintermute/internal/world/engage"
)

// SetEngageOpts mirrors the engage.Host configurable fields for use by
// SetEngage. Kind must be "terminal", "menu_terminal", or "npc"; "custom"
// is reserved. Menu is only meaningful when Kind == KindMenuTerminal and
// is validated against the closed feature set in engage.ValidateMenu.
type SetEngageOpts struct {
	Kind           string
	EngageVerbs    []string
	DisengageVerbs []string
	EnterMsg       string
	PresentMsg     string
	ExitMsg        string
	Prompt         string
	Policy         engage.Policy
	Menu           []engage.MenuEntry
}

// SetEngage marks an object as engageable. opts.Kind must be "terminal"
// or "npc" — "custom" is reserved for a later milestone. Empty verb
// slices fall back to the kind-defaults via engage.ApplyKindDefaults.
func (a *API) SetEngage(ctx context.Context, slug string, opts SetEngageOpts) error {
	if a.Engage == nil {
		return errorf(CodeInternal, "engage cache not configured")
	}
	if opts.Kind != engage.KindTerminal &&
		opts.Kind != engage.KindNPC &&
		opts.Kind != engage.KindMenuTerminal {
		return errorf(CodeInvalidArgument,
			"kind must be 'terminal', 'menu_terminal', or 'npc' (custom is not yet supported)")
	}
	if opts.Kind != engage.KindMenuTerminal && len(opts.Menu) > 0 {
		return errorf(CodeInvalidArgument,
			"menu is only valid when kind = 'menu_terminal'")
	}
	if err := engage.ValidateMenu(opts.Menu); err != nil {
		return errorf(CodeInvalidArgument, "%s", err.Error())
	}
	obj, err := a.World.ObjectBySlug(slug)
	if err != nil {
		return translateWorldErr("object", slug, err)
	}

	ev, _ := json.Marshal(opts.EngageVerbs)
	dv, _ := json.Marshal(opts.DisengageVerbs)
	policy, perr := engage.EncodePolicyJSON(opts.Policy, opts.Menu)
	if perr != nil {
		return fmt.Errorf("api: set_engage: encode policy: %w", perr)
	}

	if err := a.DB.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO object_engage (
				object_id, kind, engage_verbs, disengage_verbs,
				enter_msg, present_msg, exit_msg, prompt, policy)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(object_id) DO UPDATE SET
				kind=excluded.kind,
				engage_verbs=excluded.engage_verbs,
				disengage_verbs=excluded.disengage_verbs,
				enter_msg=excluded.enter_msg,
				present_msg=excluded.present_msg,
				exit_msg=excluded.exit_msg,
				prompt=excluded.prompt,
				policy=excluded.policy`,
			int64(obj.ID), opts.Kind, string(ev), string(dv),
			nullString(opts.EnterMsg), nullString(opts.PresentMsg),
			nullString(opts.ExitMsg), nullString(opts.Prompt),
			policy,
		)
		return err
	}); err != nil {
		return fmt.Errorf("api: set_engage: %w", err)
	}

	h := &engage.Host{
		ObjectID:       obj.ID,
		Kind:           opts.Kind,
		EngageVerbs:    append([]string(nil), opts.EngageVerbs...),
		DisengageVerbs: append([]string(nil), opts.DisengageVerbs...),
		EnterMsg:       opts.EnterMsg,
		PresentMsg:     opts.PresentMsg,
		ExitMsg:        opts.ExitMsg,
		Prompt:         opts.Prompt,
		Policy:         opts.Policy,
		Menu:           append([]engage.MenuEntry(nil), opts.Menu...),
	}
	engage.ApplyKindDefaults(h)
	a.Engage.Put(h)
	return nil
}

// ClearEngage removes an object's engageability from the DB and cache.
func (a *API) ClearEngage(ctx context.Context, slug string) error {
	if a.Engage == nil {
		return errorf(CodeInternal, "engage cache not configured")
	}
	obj, err := a.World.ObjectBySlug(slug)
	if err != nil {
		return translateWorldErr("object", slug, err)
	}
	if err := a.DB.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`DELETE FROM object_engage WHERE object_id = ?`, int64(obj.ID))
		return err
	}); err != nil {
		return fmt.Errorf("api: clear_engage: %w", err)
	}
	a.Engage.Delete(obj.ID)
	return nil
}

// nullString converts an empty string to nil so SQLite stores NULL.
// COALESCE in LoadHosts maps NULL back to the empty string.
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
