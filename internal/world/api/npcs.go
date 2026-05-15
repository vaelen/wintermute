// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"database/sql"

	"github.com/vaelen/wintermute/internal/world"
)

// NPCSpec is the input for CreateNPC / EnsureNPC. It bundles the world-
// level object fields with the npc_config row that gives the NPC a
// persona and backend.
type NPCSpec struct {
	Slug         string
	Name         string
	ShortDesc    string
	LongDesc     string
	RoomSlug     string
	OwnerID      int64
	Persona      string
	Backend      string
	BackendOpts  string // JSON; defaults to "{}" when empty
	ChatModel    string
	GateModel    string
	MaxContext   int
}

const defaultMaxContext = 4096

// CreateNPC inserts a new NPC object, places it in the given room, and
// writes its npc_config row. The NPC registry (if wired) is reloaded so
// the new NPC begins responding to room `say` events immediately.
func (a *API) CreateNPC(ctx context.Context, spec NPCSpec) (world.ObjectID, error) {
	if spec.Slug == "" {
		return 0, errorf(CodeInvalidArgument, "npc slug required")
	}
	if spec.Persona == "" {
		return 0, errorf(CodeInvalidArgument, "npc persona required")
	}
	backend := spec.Backend
	if backend == "" {
		backend = "ollama"
	}
	opts := spec.BackendOpts
	if opts == "" {
		opts = "{}"
	}
	maxCtx := spec.MaxContext
	if maxCtx == 0 {
		maxCtx = defaultMaxContext
	}
	id, err := a.CreateObject(ctx, ObjectSpec{
		Slug:      spec.Slug,
		Name:      spec.Name,
		ShortDesc: spec.ShortDesc,
		LongDesc:  spec.LongDesc,
		Kind:      world.KindNPC,
		OwnerID:   spec.OwnerID,
		RoomSlug:  spec.RoomSlug,
	})
	if err != nil {
		return 0, err
	}
	if err := a.DB.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO npc_config(object_id, persona, backend, backend_opts,
			                        chat_model, gate_model, max_context)
			   VALUES (?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?)`,
			id, spec.Persona, backend, opts,
			spec.ChatModel, spec.GateModel, maxCtx,
		)
		return err
	}); err != nil {
		_ = a.World.DeleteObject(ctx, id)
		return 0, errorf(CodeInternal, "write npc_config: %v", err)
	}
	if a.NPCs != nil {
		if err := a.NPCs.Reload(ctx); err != nil {
			a.Logger.Warn("npc reload after CreateNPC", "slug", spec.Slug, "err", err)
		}
	}
	return id, nil
}

// EnsureNPC is the idempotent variant. First call inserts; subsequent
// calls update the descriptive object fields (Name, ShortDesc, LongDesc)
// and the npc_config persona/backend/model fields when non-empty. Does
// NOT move the NPC or re-place it in a different room.
func (a *API) EnsureNPC(ctx context.Context, spec NPCSpec) (world.ObjectID, bool, error) {
	if spec.Slug == "" {
		return 0, false, errorf(CodeInvalidArgument, "npc slug required")
	}
	if existing, err := a.World.ObjectBySlug(spec.Slug); err == nil {
		if existing.Kind != world.KindNPC {
			return 0, false, errorf(CodeInvalidArgument,
				"slug %q is not an NPC", spec.Slug)
		}
		id, _, err := a.World.UpsertObject(ctx, world.ObjectSpec{
			Slug:      spec.Slug,
			Name:      spec.Name,
			ShortDesc: spec.ShortDesc,
			LongDesc:  spec.LongDesc,
		})
		if err != nil {
			return 0, false, translateWorldErr("npc", spec.Slug, err)
		}
		if err := a.updateNPCConfig(ctx, id, spec); err != nil {
			return 0, false, err
		}
		if a.NPCs != nil {
			if err := a.NPCs.Reload(ctx); err != nil {
				a.Logger.Warn("npc reload after EnsureNPC", "slug", spec.Slug, "err", err)
			}
		}
		return id, false, nil
	}
	id, err := a.CreateNPC(ctx, spec)
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// SetNPCPersona updates the persona text of an existing NPC. The NPC
// registry is reloaded so the new persona is picked up by the next
// dispatch.
func (a *API) SetNPCPersona(ctx context.Context, slug, persona string) error {
	o, err := a.World.ObjectBySlug(slug)
	if err != nil {
		return translateWorldErr("npc", slug, err)
	}
	if o.Kind != world.KindNPC {
		return errorf(CodeInvalidArgument, "slug %q is not an NPC", slug)
	}
	if err := a.DB.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE npc_config SET persona = ? WHERE object_id = ?`,
			persona, o.ID,
		)
		return err
	}); err != nil {
		return errorf(CodeInternal, "update persona: %v", err)
	}
	if a.NPCs != nil {
		_ = a.NPCs.Reload(ctx)
	}
	return nil
}

// updateNPCConfig writes only the supplied (non-zero) fields back to
// npc_config. Used by EnsureNPC. No-op when every config field is empty.
func (a *API) updateNPCConfig(ctx context.Context, id world.ObjectID, spec NPCSpec) error {
	if spec.Persona == "" && spec.Backend == "" && spec.BackendOpts == "" &&
		spec.ChatModel == "" && spec.GateModel == "" && spec.MaxContext == 0 {
		return nil
	}
	// COALESCE lets a caller supply a subset of fields and leave the rest
	// of the row untouched. NULLIF('', x) maps the empty string to NULL
	// so COALESCE falls through to the existing column value.
	if err := a.DB.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE npc_config
			   SET persona      = COALESCE(NULLIF(?, ''), persona),
			       backend      = COALESCE(NULLIF(?, ''), backend),
			       backend_opts = COALESCE(NULLIF(?, ''), backend_opts),
			       chat_model   = COALESCE(NULLIF(?, ''), chat_model),
			       gate_model   = COALESCE(NULLIF(?, ''), gate_model),
			       max_context  = CASE WHEN ? > 0 THEN ? ELSE max_context END
			 WHERE object_id = ?`,
			spec.Persona, spec.Backend, spec.BackendOpts,
			spec.ChatModel, spec.GateModel,
			spec.MaxContext, spec.MaxContext,
			id,
		)
		return err
	}); err != nil {
		return errorf(CodeInternal, "update npc_config: %v", err)
	}
	return nil
}
