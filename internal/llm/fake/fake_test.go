// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

//go:build test

package fake

import (
	"context"
	"testing"

	"github.com/vaelen/wintermute/internal/llm"
)

func TestFakeRegistered(t *testing.T) {
	got, err := llm.Open("fake", nil)
	if err != nil {
		t.Fatalf("Open(\"fake\"): unexpected error: %v", err)
	}
	if got == nil {
		t.Fatalf("Open(\"fake\") returned nil LLM")
	}
}

func TestFakeChatScriptedMatch(t *testing.T) {
	backend, err := llm.Open("fake", map[string]any{
		"responses": map[string]any{
			"hello":   "hi there",
			"goodbye": "see you",
		},
		"default": "no idea",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	resp, err := backend.Chat(context.Background(), []llm.Message{
		{Role: llm.RoleSystem, Content: "you are a test"},
		{Role: llm.RoleUser, Content: "well hello there"},
	}, nil, llm.ChatOpts{})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "hi there" {
		t.Fatalf("Chat content = %q, want %q", resp.Content, "hi there")
	}
}

func TestFakeChatFallbackToDefault(t *testing.T) {
	backend, err := llm.Open("fake", map[string]any{
		"responses": map[string]any{"hello": "hi"},
		"default":   "no match",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	resp, err := backend.Chat(context.Background(), []llm.Message{
		{Role: llm.RoleUser, Content: "completely unrelated"},
	}, nil, llm.ChatOpts{})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "no match" {
		t.Fatalf("Chat content = %q, want %q", resp.Content, "no match")
	}
}

func TestFakeChatBuiltinDefault(t *testing.T) {
	backend, err := llm.Open("fake", nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	resp, err := backend.Chat(context.Background(), []llm.Message{
		{Role: llm.RoleUser, Content: "anything"},
	}, nil, llm.ChatOpts{})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != defaultReply {
		t.Fatalf("Chat content = %q, want %q", resp.Content, defaultReply)
	}
}

func TestFakeEmbedDeterministic(t *testing.T) {
	backend, err := llm.Open("fake", nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	a, err := backend.Embed(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	b, err := backend.Embed(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(a) != len(b) {
		t.Fatalf("embed lengths differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("embed[%d] differs across calls: %v vs %v", i, a[i], b[i])
		}
	}
}

func TestFakeEmbedDifferentInputs(t *testing.T) {
	backend, err := llm.Open("fake", nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	a, err := backend.Embed(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	b, err := backend.Embed(context.Background(), "bravo")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	if len(a) != len(b) {
		t.Fatalf("embed lengths differ: %d vs %d", len(a), len(b))
	}
	allEqual := true
	for i := range a {
		if a[i] != b[i] {
			allEqual = false
			break
		}
	}
	if allEqual {
		t.Fatalf("embeddings for different inputs are identical: %v", a)
	}
}

func TestFakeEmbedDimension(t *testing.T) {
	for _, dim := range []int{1, 8, 32, 128} {
		backend, err := llm.Open("fake", map[string]any{"embedding_dim": dim})
		if err != nil {
			t.Fatalf("Open(dim=%d): %v", dim, err)
		}
		v, err := backend.Embed(context.Background(), "anything")
		if err != nil {
			t.Fatalf("Embed(dim=%d): %v", dim, err)
		}
		if len(v) != dim {
			t.Fatalf("Embed length = %d, want %d", len(v), dim)
		}
	}
}

func TestFakeEmbedDefaultDimension(t *testing.T) {
	backend, err := llm.Open("fake", nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	v, err := backend.Embed(context.Background(), "x")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(v) != defaultEmbeddingDim {
		t.Fatalf("default embed length = %d, want %d", len(v), defaultEmbeddingDim)
	}
}
