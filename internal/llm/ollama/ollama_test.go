// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package ollama_test

import (
	"context"
	"strings"
	"testing"

	"github.com/vaelen/wintermute/internal/llm"
	_ "github.com/vaelen/wintermute/internal/llm/ollama"
)

// isolate ensures OLLAMA_HOST cannot leak into tests that rely on default URL
// behaviour or on parsing failures triggered by deliberately bad opts["url"].
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("OLLAMA_HOST", "")
}

func TestBackendRegistered(t *testing.T) {
	var found bool
	for _, name := range llm.Backends() {
		if name == "ollama" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("ollama backend not registered; got backends %v", llm.Backends())
	}
}

func TestNewDefaults(t *testing.T) {
	isolate(t)
	got, err := llm.Open("ollama", nil)
	if err != nil {
		t.Fatalf("Open: unexpected error: %v", err)
	}
	if got == nil {
		t.Fatalf("Open returned nil LLM")
	}
}

func TestNewErrors(t *testing.T) {
	cases := []struct {
		name string
		opts map[string]any
		want string
	}{
		{
			name: "bad url",
			opts: map[string]any{"url": "not-a-url:::"},
			want: "url",
		},
		{
			name: "url wrong type",
			opts: map[string]any{"url": 42},
			want: "url",
		},
		{
			name: "model wrong type",
			opts: map[string]any{"model": 42},
			want: "model",
		},
		{
			name: "embedding_model wrong type",
			opts: map[string]any{"embedding_model": 42},
			want: "embedding_model",
		},
		{
			name: "keep_alive wrong type",
			opts: map[string]any{"keep_alive": 5},
			want: "keep_alive",
		},
		{
			name: "keep_alive invalid",
			opts: map[string]any{"keep_alive": "invalid"},
			want: "keep_alive",
		},
		{
			name: "temperature wrong type",
			opts: map[string]any{"temperature": "hot"},
			want: "temperature",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolate(t)
			_, err := llm.Open("ollama", tc.opts)
			if err == nil {
				t.Fatalf("Open(%v) returned nil error, want error mentioning %q", tc.opts, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Open(%v) error %q does not mention %q", tc.opts, err.Error(), tc.want)
			}
		})
	}
}

func TestChatNoModelConfigured(t *testing.T) {
	isolate(t)
	got, err := llm.Open("ollama", nil)
	if err != nil {
		t.Fatalf("Open: unexpected error: %v", err)
	}
	_, err = got.Chat(context.Background(), []llm.Message{{Role: llm.RoleUser, Content: "hi"}}, nil, llm.ChatOpts{})
	if err == nil {
		t.Fatalf("Chat returned nil error, want error about missing model")
	}
	if !strings.Contains(err.Error(), "model") {
		t.Fatalf("Chat error %q does not mention model", err.Error())
	}
}

func TestEmbedNoEmbeddingModelConfigured(t *testing.T) {
	isolate(t)
	got, err := llm.Open("ollama", nil)
	if err != nil {
		t.Fatalf("Open: unexpected error: %v", err)
	}
	_, err = got.Embed(context.Background(), "hello")
	if err == nil {
		t.Fatalf("Embed returned nil error, want error about missing embedding_model")
	}
	if !strings.Contains(err.Error(), "embedding_model") {
		t.Fatalf("Embed error %q does not mention embedding_model", err.Error())
	}
}

func TestTemperatureAcceptsFloat64(t *testing.T) {
	isolate(t)
	_, err := llm.Open("ollama", map[string]any{"temperature": 0.5})
	if err != nil {
		t.Fatalf("Open: unexpected error: %v", err)
	}
}

func TestTemperatureAcceptsFloat32(t *testing.T) {
	isolate(t)
	_, err := llm.Open("ollama", map[string]any{"temperature": float32(0.5)})
	if err != nil {
		t.Fatalf("Open: unexpected error: %v", err)
	}
}

func TestKeepAliveAcceptsValid(t *testing.T) {
	isolate(t)
	_, err := llm.Open("ollama", map[string]any{"keep_alive": "5m"})
	if err != nil {
		t.Fatalf("Open: unexpected error: %v", err)
	}
}
