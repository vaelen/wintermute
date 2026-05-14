// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package llm

import (
	"context"
	"sort"
	"strings"
	"testing"
)

type stubLLM struct{ tag string }

func (s *stubLLM) Chat(context.Context, []Message, []ToolDef, ChatOpts) (Response, error) {
	return Response{Content: s.tag}, nil
}

func (s *stubLLM) Embed(context.Context, string) ([]float32, error) {
	return []float32{0}, nil
}

func stubFactory(tag string) Factory {
	return func(opts map[string]any) (LLM, error) {
		return &stubLLM{tag: tag}, nil
	}
}

func TestRegisterAndOpen(t *testing.T) {
	const name = "registry-test-known"
	Register(name, stubFactory("hello"))

	got, err := Open(name, nil)
	if err != nil {
		t.Fatalf("Open(%q): unexpected error: %v", name, err)
	}
	resp, err := got.Chat(context.Background(), nil, nil, ChatOpts{})
	if err != nil {
		t.Fatalf("Chat: unexpected error: %v", err)
	}
	if resp.Content != "hello" {
		t.Fatalf("Chat returned content %q, want %q", resp.Content, "hello")
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	const name = "registry-test-duplicate"
	Register(name, stubFactory("first"))

	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("Register did not panic on duplicate name")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panic value is %T, want string", r)
		}
		if !strings.Contains(msg, name) {
			t.Fatalf("panic message %q does not mention name %q", msg, name)
		}
	}()
	Register(name, stubFactory("second"))
}

func TestOpenUnknownBackend(t *testing.T) {
	const name = "registry-test-unknown"
	_, err := Open(name, nil)
	if err == nil {
		t.Fatalf("Open(%q) returned nil error, want error", name)
	}
	if !strings.Contains(err.Error(), name) {
		t.Fatalf("error %q does not mention backend name %q", err.Error(), name)
	}
}

func TestBackendsSorted(t *testing.T) {
	for _, name := range []string{
		"registry-test-sorted-charlie",
		"registry-test-sorted-alpha",
		"registry-test-sorted-bravo",
	} {
		Register(name, stubFactory(name))
	}

	got := Backends()
	if !sort.StringsAreSorted(got) {
		t.Fatalf("Backends() not sorted: %v", got)
	}

	wanted := map[string]bool{
		"registry-test-sorted-alpha":   false,
		"registry-test-sorted-bravo":   false,
		"registry-test-sorted-charlie": false,
	}
	for _, name := range got {
		if _, ok := wanted[name]; ok {
			wanted[name] = true
		}
	}
	for name, present := range wanted {
		if !present {
			t.Fatalf("Backends() missing %q: got %v", name, got)
		}
	}
}
