// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package engage

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

type fakeNPCClient struct {
	mu      sync.Mutex
	calls   []string
	reply   string
	chatErr error
}

func (f *fakeNPCClient) Chat(_ context.Context, _ string, userMsg string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, userMsg)
	if f.chatErr != nil {
		return "", f.chatErr
	}
	return f.reply, nil
}

func TestNPCHandler_dispatchesAndRepliesToParticipant(t *testing.T) {
	client := &fakeNPCClient{reply: "Hello there."}
	host := &Host{ObjectID: 1, Kind: KindNPC}
	h := NewNPCHandler(host, &NPCBinding{
		Client:      client,
		DisplayName: "bartender",
		Persona:     "You are a gruff bartender.",
	}, nil)

	var out strings.Builder
	p := &Participant{
		SessionID: "s1", PlayerID: 7, DisplayName: "alice",
		Write: func(s string) error { out.WriteString(s); return nil },
	}
	h.Handle(p, "hi")

	if len(client.calls) != 1 || client.calls[0] != "hi" {
		t.Errorf("client.calls = %v", client.calls)
	}
	if !strings.Contains(out.String(), "Hello there.") {
		t.Errorf("participant output = %q; want NPC reply", out.String())
	}
	if !strings.Contains(out.String(), "bartender says") {
		t.Errorf("participant output = %q; want NPC name in reply", out.String())
	}
}

func TestNPCHandler_personaIncludesPrivateConversationNote(t *testing.T) {
	client := &fakeNPCClient{reply: "ok"}
	host := &Host{ObjectID: 1, Kind: KindNPC}
	h := NewNPCHandler(host, &NPCBinding{
		Client:      client,
		DisplayName: "bartender",
		Persona:     "Base persona.",
	}, nil)
	p := &Participant{
		SessionID: "s1", DisplayName: "alice",
		Write: func(string) error { return nil },
	}
	h.Handle(p, "hi")

	if !strings.Contains(h.systemPrompt(p), "Base persona.") {
		t.Errorf("system prompt missing base persona: %q", h.systemPrompt(p))
	}
	if !strings.Contains(h.systemPrompt(p), "private conversation with alice") {
		t.Errorf("system prompt missing privacy note: %q", h.systemPrompt(p))
	}
}

func TestNPCHandler_emptyLineIgnored(t *testing.T) {
	client := &fakeNPCClient{reply: "x"}
	h := NewNPCHandler(&Host{ObjectID: 1, Kind: KindNPC},
		&NPCBinding{Client: client, DisplayName: "x"}, nil)
	p := &Participant{Write: func(string) error { return nil }}
	h.Handle(p, "   ")
	if len(client.calls) != 0 {
		t.Errorf("client.calls = %v; want empty", client.calls)
	}
}

func TestNPCHandler_clientErrorBecomesFallback(t *testing.T) {
	client := &fakeNPCClient{chatErr: errors.New("boom")}
	h := NewNPCHandler(&Host{ObjectID: 1, Kind: KindNPC},
		&NPCBinding{Client: client, DisplayName: "bartender"}, nil)
	var out strings.Builder
	p := &Participant{
		Write: func(s string) error { out.WriteString(s); return nil },
	}
	h.Handle(p, "hi")
	if !strings.Contains(out.String(), "unable to reply") {
		t.Errorf("output = %q; want fallback message", out.String())
	}
}

func TestNPCHandler_nilClientBecomesFallback(t *testing.T) {
	h := NewNPCHandler(&Host{ObjectID: 1, Kind: KindNPC},
		&NPCBinding{DisplayName: "ghost"}, nil)
	var out strings.Builder
	p := &Participant{
		Write: func(s string) error { out.WriteString(s); return nil },
	}
	h.Handle(p, "hi")
	if !strings.Contains(out.String(), "stares blankly") {
		t.Errorf("output = %q; want blank-stare fallback", out.String())
	}
}

func TestNPCHandler_onCloseInvoked(t *testing.T) {
	called := false
	h := NewNPCHandler(&Host{ObjectID: 1, Kind: KindNPC},
		&NPCBinding{DisplayName: "x"},
		func() { called = true })
	p := &Participant{Write: func(string) error { return nil }}
	h.OnClose(p, CloseDisconnect)
	if !called {
		t.Error("onClose callback was not invoked")
	}
}
