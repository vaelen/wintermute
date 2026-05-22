// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package loop

import (
	"context"
	"fmt"
	"strings"

	"github.com/vaelen/wintermute/internal/llm"
	"github.com/vaelen/wintermute/internal/world/events"
)

// gatePrompt is the system message sent to the gate model. Kept short
// because the gate model is tiny (e.g. llama3.2:1b); we parse its
// reply as the first significant token, expecting "YES" or "NO".
const gatePrompt = `You are a relevance filter for the NPC %q.
Persona: %s
Recent events:
%s
Should %s respond or act now? Answer YES or NO. Answer with only one word.`

// defaultTick is the production-mode action: gate, then (if YES) call
// respond. The response-model + tool-call flow is filled in by later
// M7 tasks; for now defaultTick stops after the gate.
func (l *Loop) defaultTick(ctx context.Context, obs []events.Event) {
	if l.LLM == nil {
		return
	}
	if l.GateModel != "" && !l.gateAllows(ctx, obs) {
		l.recordObservationsToShortTerm(obs)
		return
	}
	l.respond(ctx, obs)
}

func (l *Loop) gateAllows(ctx context.Context, obs []events.Event) bool {
	prompt := fmt.Sprintf(gatePrompt,
		l.NPCName, l.Persona, renderObservations(obs), l.NPCName)
	resp, err := l.LLM.Chat(ctx, []llm.Message{
		{Role: llm.RoleSystem, Content: prompt},
	}, nil, llm.ChatOpts{Model: l.GateModel, Temperature: 0})
	if err != nil {
		l.Logger.Warn("npc loop: gate call failed, defaulting to YES",
			"npc", l.NPCName, "err", err)
		return true
	}
	return strings.EqualFold(firstToken(resp.Content), "YES")
}

// renderObservations formats the buffered events as a short bulleted
// list the gate prompt and (later) the response prompt can splice in.
func renderObservations(obs []events.Event) string {
	var b strings.Builder
	for _, e := range obs {
		switch e.Kind {
		case events.KindSay:
			fmt.Fprintf(&b, "- someone said: %s\n", e.Text)
		case events.KindEmote:
			fmt.Fprintf(&b, "- someone %s\n", e.Text)
		case events.KindArrive:
			b.WriteString("- someone arrived\n")
		case events.KindDepart:
			b.WriteString("- someone left\n")
		case events.KindSched:
			fmt.Fprintf(&b, "- scheduled goal: %s\n", e.Text)
		default:
			fmt.Fprintf(&b, "- %s\n", e.Kind)
		}
	}
	return b.String()
}

func firstToken(s string) string {
	s = strings.TrimSpace(s)
	for i, r := range s {
		if r == ' ' || r == '\n' || r == '\t' || r == '.' || r == ',' {
			return s[:i]
		}
	}
	return s
}

// recordObservationsToShortTerm is a stub at this stage; Task 7 wires
// it into npc/memory.State. Keeping it as a method now means the call
// site in defaultTick does not need to change when Task 7 lands.
func (l *Loop) recordObservationsToShortTerm(obs []events.Event) {
	_ = obs
}

// respond is filled in by Task 7. The placeholder ensures defaultTick
// compiles when GateModel="" (or gateAllows returns true).
func (l *Loop) respond(ctx context.Context, obs []events.Event) {
	_ = ctx
	_ = obs
}
