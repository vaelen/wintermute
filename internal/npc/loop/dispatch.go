// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/vaelen/wintermute/internal/llm"
	"github.com/vaelen/wintermute/internal/npc/memory"
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

// respond drives the response-model path: retrieve relevant long-term
// memories, build a chat context (persona + memory context +
// observations), call the response model, broadcast the reply via
// World.NPCSay, and record both observations and the NPC's own reply
// into short-term memory.
//
// Memory ergonomics note: M3 used per-(npc, player) conversation
// buffers keyed by the speaker's ObjectID. The autonomous loop sees
// events from multiple speakers in a room, so it stores all
// observations under playerID=0 as a single "room observations"
// bucket. Retrieve also queries against bucket 0. This is a deliberate
// shoehorn — refining the per-conversation key for the autonomous
// path is out of scope for M7.
func (l *Loop) respond(ctx context.Context, obs []events.Event) {
	if l.LLM == nil {
		return
	}

	var memCtx string
	if l.Memory != nil {
		mems, err := l.Memory.Retrieve(ctx, 0, 5, 0.7)
		if err != nil {
			l.Logger.Warn("npc loop: memory retrieve failed",
				"npc", l.NPCName, "err", err)
		} else if len(mems) > 0 {
			memCtx = renderMemoryContext(mems)
		}
	}

	msgs := []llm.Message{
		{Role: llm.RoleSystem, Content: l.Persona},
	}
	if memCtx != "" {
		msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Content: memCtx})
	}
	msgs = append(msgs, llm.Message{
		Role:    llm.RoleUser,
		Content: renderObservations(obs),
	})

	tools := l.toolDefs()

	resp, err := l.callChatWithTools(ctx, msgs, tools, 0)
	if err != nil {
		l.Logger.Warn("npc loop: response model failed",
			"npc", l.NPCName, "err", err)
		return
	}
	reply := strings.TrimSpace(resp.Content)
	l.recordObservationsToShortTerm(obs)
	if reply == "" {
		return
	}

	if l.World != nil {
		if err := l.World.NPCSay(l.NPCID, reply); err != nil {
			l.Logger.Warn("npc loop: NPCSay failed",
				"npc", l.NPCName, "err", err)
		}
	}
	l.recordOwnReplyToShortTerm(reply)
}

// renderMemoryContext formats retrieved memories as a single system
// message slotted between persona and observations.
func renderMemoryContext(mems []memory.Memory) string {
	var b strings.Builder
	b.WriteString("Relevant memories from past conversations:\n")
	for _, m := range mems {
		b.WriteString("- ")
		b.WriteString(m.Summary)
		b.WriteString("\n")
	}
	return b.String()
}

// recordObservationsToShortTerm appends each say-kind observation to
// the room-observations bucket (playerID=0). Other event kinds are
// ignored at this layer — they're already represented in the chat
// context as a rendered line.
func (l *Loop) recordObservationsToShortTerm(obs []events.Event) {
	if l.Memory == nil {
		return
	}
	for _, e := range obs {
		if e.Kind != events.KindSay {
			continue
		}
		l.Memory.Append(0, memory.Turn{
			Speaker: "observed",
			Text:    e.Text,
			At:      e.At,
		})
	}
}

func (l *Loop) recordOwnReplyToShortTerm(reply string) {
	if l.Memory == nil {
		return
	}
	l.Memory.Append(0, memory.Turn{
		Speaker: "<npc>",
		Text:    reply,
		At:      time.Now(),
	})
}

// toolDefs returns the ToolDef list the response model receives for
// this NPC. Tools the NPC hasn't opted into (via ToolNames) are not
// exposed.
func (l *Loop) toolDefs() []llm.ToolDef {
	if l.Tools == nil || len(l.ToolNames) == 0 {
		return nil
	}
	out := make([]llm.ToolDef, 0, len(l.ToolNames))
	for _, name := range l.ToolNames {
		meta := l.Tools.Get(name)
		if meta.Name == "" {
			continue
		}
		out = append(out, llm.ToolDef{
			Name:        meta.Name,
			Description: meta.Description,
			Schema:      meta.Schema,
		})
	}
	return out
}

// callChatWithTools issues a Chat call and loops on ToolCalls until
// the model returns plain content (no ToolCalls) or MaxToolDepth is
// reached. Each tool call is dispatched via Tools.Invoke; the result
// is appended as a RoleTool message before the next Chat round.
func (l *Loop) callChatWithTools(ctx context.Context, msgs []llm.Message, tools []llm.ToolDef, depth int) (llm.Response, error) {
	for {
		resp, err := l.LLM.Chat(ctx, msgs, tools, llm.ChatOpts{Model: l.ChatModel})
		if err != nil {
			return resp, err
		}
		if len(resp.ToolCalls) == 0 {
			return resp, nil
		}
		if depth >= l.MaxToolDepth {
			l.Logger.Warn("npc loop: tool-call depth exceeded",
				"npc", l.NPCName, "depth", depth)
			return resp, nil
		}
		msgs = append(msgs, llm.Message{
			Role:      llm.RoleAssistant,
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})
		for _, tc := range resp.ToolCalls {
			result, invErr := l.invokeTool(ctx, tc)
			msgs = append(msgs, llm.Message{
				Role:       llm.RoleTool,
				Name:       tc.Name,
				ToolCallID: tc.ID,
				Content:    encodeToolResult(result, invErr),
			})
		}
		depth++
	}
}

// invokeTool dispatches a single ToolCall through the configured Tools
// registry after validating it against the NPC's allow-list. Returns a
// descriptive error if the tool isn't permitted or the registry is
// unconfigured — these are surfaced back to the response model via
// encodeToolResult so the model can adapt rather than receiving a
// silent failure.
func (l *Loop) invokeTool(ctx context.Context, tc llm.ToolCall) (map[string]any, error) {
	if l.Tools == nil {
		return nil, fmt.Errorf("npc loop: tool registry not configured")
	}
	allowed := false
	for _, name := range l.ToolNames {
		if name == tc.Name {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, fmt.Errorf("npc loop: tool %q not in NPC allow-list", tc.Name)
	}
	return l.Tools.Invoke(ctx, tc.Name, tc.Arguments)
}

// encodeToolResult formats either an invocation result or an error as
// the JSON string that goes back to the response model as a RoleTool
// message body. The model only ever sees a JSON object, so it can be
// parsed uniformly on the model side.
func encodeToolResult(result map[string]any, invErr error) string {
	if invErr != nil {
		return fmt.Sprintf(`{"ok":false,"error":%q}`, invErr.Error())
	}
	out, err := json.Marshal(result)
	if err != nil {
		return fmt.Sprintf(`{"ok":false,"error":"marshal: %s"}`, err.Error())
	}
	return string(out)
}
