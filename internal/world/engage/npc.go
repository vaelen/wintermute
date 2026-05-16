// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package engage

import (
	"context"
	"fmt"
	"strings"
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
}

// NPCHandler is the built-in handler for kind='npc' hosts. Every input
// line becomes one Chat call; the reply goes to the participant only.
// The per-NPC mutex (held by internal/npc) serialises concurrent calls;
// this handler does not impose additional locking.
type NPCHandler struct {
	host *Host
	npc  *NPCBinding
}

// NewNPCHandler returns a handler ready for the session loop.
func NewNPCHandler(host *Host, npc *NPCBinding) *NPCHandler {
	return &NPCHandler{host: host, npc: npc}
}

// OnOpen is a no-op. The room-level enter broadcast is the participant's
// confirmation; an extra prompt would just crowd the screen.
func (h *NPCHandler) OnOpen(_ *Participant) {}

// OnClose runs when the engagement ends. M4 summarisation will hook in
// here in a later milestone; M5.7 does nothing.
func (h *NPCHandler) OnClose(_ *Participant, _ CloseReason) {}

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
	reply, err := h.npc.Client.Chat(context.Background(), h.systemPrompt(p), line)
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
