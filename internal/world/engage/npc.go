// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package engage

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// NPCClient is the minimal LLM interface this handler needs. The
// internal/npc package wires this to the real per-NPC client via a
// thin adapter (built in T12).
type NPCClient interface {
	Chat(ctx context.Context, system, user string) (string, error)
}

// NPCBinding ties an NPC host to its LLM client and identity bits used
// to compose the per-engagement system prompt.
type NPCBinding struct {
	Client      NPCClient
	DisplayName string
	Persona     string

	// RootCtx is the server-lifetime parent for per-call timeouts. If nil,
	// falls back to context.Background() — acceptable for unit tests but
	// production wiring (main.go) MUST set it.
	RootCtx context.Context

	// Timeout bounds each Chat call. Zero falls back to 120s, matching
	// internal/npc dispatchTimeout.
	Timeout time.Duration
}

// NPCHandler is the built-in handler for kind='npc' hosts. Every input
// line becomes one Chat call; the reply goes to the participant only.
// The per-NPC mutex (held by internal/npc) serialises concurrent calls;
// this handler does not impose additional locking.
type NPCHandler struct {
	host  *Host
	npc   *NPCBinding
	close func() // optional callback fired on OnClose
}

// NewNPCHandler returns a handler ready for the session loop. onClose, if
// non-nil, is invoked from OnClose for outside-view broadcasts (the
// engage caller in main.go provides it).
func NewNPCHandler(host *Host, npc *NPCBinding, onClose func()) *NPCHandler {
	return &NPCHandler{host: host, npc: npc, close: onClose}
}

func (h *NPCHandler) OnOpen(_ *Participant) {}

// OnClose fires the optional close callback. Without it the room sees no
// exit broadcast; the callback is provided by main.go via a captured closure.
func (h *NPCHandler) OnClose(_ *Participant, _ CloseReason) {
	if h.close != nil {
		h.close()
	}
}

// Handle sends line to the NPC's LLM. The dispatch is synchronous; the
// existing per-NPC mutex serialises concurrent NPC calls.
func (h *NPCHandler) Handle(p *Participant, line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	if h.npc == nil || h.npc.Client == nil {
		name := ""
		if h.npc != nil {
			name = h.npc.DisplayName
		}
		if name == "" {
			name = "they"
		}
		_ = p.Write(name + " stares blankly into space.\r\n")
		return
	}
	root := h.npc.RootCtx
	if root == nil {
		root = context.Background()
	}
	timeout := h.npc.Timeout
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(root, timeout)
	defer cancel()
	reply, err := h.npc.Client.Chat(ctx, h.systemPrompt(p), line)
	if err != nil {
		_ = p.Write(h.npc.DisplayName + " seems unable to reply.\r\n")
		return
	}
	_ = p.Write(formatNPCReply(h.npc.DisplayName, reply))
}

// systemPrompt builds the per-engagement system prompt: the NPC's base
// persona plus a private-conversation note naming the participant.
func (h *NPCHandler) systemPrompt(p *Participant) string {
	return fmt.Sprintf("%s\n\nYou are in a private conversation with %s. "+
		"They are speaking directly and only to you. Other people in the "+
		"room may be visible to you but cannot hear what you are saying.",
		h.npc.Persona, p.DisplayName)
}

// formatNPCReply renders the LLM's text as a `says, "..."` line.
func formatNPCReply(name, text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		text = "..."
	}
	return name + ` says, "` + text + `"` + "\r\n"
}
