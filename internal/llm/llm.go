// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package llm

import "context"

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is a single chat turn. Tool-call fields are declared now but unused
// at runtime in M3; locking them in keeps the type stable across backends so
// later milestones can wire tool calling without breaking persisted shapes.
type Message struct {
	Role       Role
	Content    string
	Name       string
	ToolCalls  []ToolCall
	ToolCallID string
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments map[string]any
}

type ToolDef struct {
	Name        string
	Description string
	Schema      map[string]any
}

type ChatOpts struct {
	Model       string
	Temperature float32
	MaxTokens   int
}

type Response struct {
	Content   string
	ToolCalls []ToolCall
	UsageIn   int
	UsageOut  int
}

type LLM interface {
	Chat(ctx context.Context, msgs []Message, tools []ToolDef, opts ChatOpts) (Response, error)
	Embed(ctx context.Context, text string) ([]float32, error)
}
