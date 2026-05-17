// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package npc

import (
	"context"
	"errors"
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

// ErrNoClient is returned by EngageChat when the NPC has no llm client
// configured (e.g. the backend Open call failed at registry load).
var ErrNoClient = errors.New("npc: no llm client")

// EngageChat is a single-shot chat call for the engagement primitive.
// The supplied system prompt overrides the NPC's normal persona (the
// engage layer composes a private-conversation augmented prompt itself).
// Holds the per-NPC mutex for the duration of the call so concurrent
// HandleSay and engagement chats serialise just like M3 expected.
//
// Returns the assistant text, or an error. Used by the engage NPC handler.
func (n *NPC) EngageChat(ctx context.Context, system, user string) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.llm == nil {
		return "", ErrNoClient
	}
	msgs := []llm.Message{
		{Role: llm.RoleSystem, Content: system},
		{Role: llm.RoleUser, Content: user},
	}
	resp, err := n.llm.Chat(ctx, msgs, nil, llm.ChatOpts{Model: n.Model})
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}
