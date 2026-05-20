// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"errors"
	"log/slog"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/boards"
	"github.com/vaelen/wintermute/internal/files"
	"github.com/vaelen/wintermute/internal/mail"
	"github.com/vaelen/wintermute/internal/security"
	"github.com/vaelen/wintermute/internal/store"
	"github.com/vaelen/wintermute/internal/world"
	"github.com/vaelen/wintermute/internal/world/engage"
)

// NPCReloader is the minimal interface this package needs from the NPC
// registry: re-read npc_config and rebuild the in-memory NPC map. The
// concrete *npc.Registry satisfies it. Holding the interface here keeps
// the api package free of the npc → llm → ollama dependency chain.
type NPCReloader interface {
	Reload(ctx context.Context) error
}

// API is the stable, intent-named surface used by admin Lua (M5) and
// later by the M8 sandbox. It wraps internal/world with named operations
// and stable error codes. Construct via New.
type API struct {
	World   *world.World
	DB      *store.DB
	Accts   *auth.Store
	NPCs    NPCReloader
	Logger  *slog.Logger
	// Engage is optional: nil disables set_engage/clear_engage.
	Engage *engage.HostCache
	// Mail is optional: nil disables wintermute.mail.* bindings.
	Mail *mail.Service
	// Boards is optional: nil disables wintermute.board.* bindings.
	Boards *boards.Service
	// Files is optional: nil disables wintermute.file.* bindings.
	Files *files.Service
	// Security is optional: nil disables wintermute.security.* and
	// admin rename/history operations. Wired by main from the M6.6
	// service.
	Security *security.Service
}

// New builds an API. Any of npcs, accts, or logger may be nil; the
// affected operations error with internal when invoked without the
// required dependency.
func New(w *world.World, db *store.DB, accts *auth.Store, npcs NPCReloader, logger *slog.Logger) *API {
	if logger == nil {
		logger = slog.Default()
	}
	return &API{
		World:  w,
		DB:     db,
		Accts:  accts,
		NPCs:   npcs,
		Logger: logger,
	}
}

// translateWorldErr maps a sentinel error from the world package into a
// stable api.Error. Returns nil if err is nil. Pass throughs unrecognised
// errors as internal so callers see a code instead of a raw message.
func translateWorldErr(kind, slug string, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, world.ErrSlugInUse):
		return duplicateSlug(slug)
	case errors.Is(err, world.ErrUnknownRoom):
		return notFound("room", slug)
	case errors.Is(err, world.ErrUnknownObject):
		return notFound(kind, slug)
	}
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return err
	}
	return errorf(CodeInternal, "%v", err)
}
