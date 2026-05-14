// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

//go:build ollama

package ollama_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/vaelen/wintermute/internal/llm"
	_ "github.com/vaelen/wintermute/internal/llm/ollama"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func TestOllamaChat(t *testing.T) {
	url := os.Getenv("OLLAMA_URL")
	if url == "" {
		t.Skip("OLLAMA_URL not set; skipping live Ollama test")
	}
	model := envOr("OLLAMA_MODEL", "llama3.2:1b")

	got, err := llm.Open("ollama", map[string]any{
		"url":   url,
		"model": model,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, err := got.Chat(ctx, []llm.Message{
		{Role: llm.RoleSystem, Content: "Respond with a single short word."},
		{Role: llm.RoleUser, Content: "Say hi."},
	}, nil, llm.ChatOpts{})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content == "" {
		t.Fatalf("Chat returned empty content")
	}
}

func TestOllamaEmbed(t *testing.T) {
	url := os.Getenv("OLLAMA_URL")
	if url == "" {
		t.Skip("OLLAMA_URL not set; skipping live Ollama test")
	}
	embModel := os.Getenv("OLLAMA_EMBEDDING_MODEL")
	if embModel == "" {
		embModel = "nomic-embed-text"
	}

	got, err := llm.Open("ollama", map[string]any{
		"url":             url,
		"embedding_model": embModel,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	vec, err := got.Embed(ctx, "the quick brown fox")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) == 0 {
		t.Fatalf("Embed returned empty vector")
	}
}
