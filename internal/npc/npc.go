// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package npc

import (
	"sync"

	"github.com/vaelen/wintermute/internal/llm"
	"github.com/vaelen/wintermute/internal/npc/memory"
	"github.com/vaelen/wintermute/internal/world"
)

// historyWindow caps the rolling short-term context: up to this many
// (user, assistant) pairs — i.e. 2*historyWindow messages — fed back into
// the next chat call. Persistence and summarisation arrive in M4.
const historyWindow = 10

// NPC is a single non-player character: persona, backend selection, and the
// in-memory state needed to converse. The runtime fields are unexported and
// guarded by mu.
type NPC struct {
	ObjectID    world.ObjectID
	Name        string
	Persona     string
	Backend     string
	BackendOpts map[string]any
	Model       string
	GateModel   string
	MaxContext  int

	// Memory is the per-NPC short-term + long-term + worker bundle.
	// Wired up by Registry.Load; non-nil when llm is non-nil.
	Memory *memory.State

	llm     llm.LLM
	mu      sync.Mutex
	history []llm.Message
}
