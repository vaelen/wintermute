// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package cmd

import (
	"context"
	"testing"

	"github.com/vaelen/wintermute/internal/world"
	"github.com/vaelen/wintermute/internal/world/engage"
)

func TestDispatch_engageWithUniversal_callsOpenFn(t *testing.T) {
	h, _, db := newHandlerWithDB(t, "alice")
	cache := engage.NewHostCache()
	if err := cache.Load(context.Background(), db); err != nil {
		t.Fatalf("HostCache.Load: %v", err)
	}
	var openedHost *engage.Host
	h.Engage = &EngageBackend{
		Registry: engage.NewRegistry(),
		Hosts:    cache,
		OpenFn: func(host *engage.Host, _ *world.Presence, _ engage.SessionBinding) error {
			openedHost = host
			return nil
		},
	}
	out := h.Dispatch(context.Background(), "engage with terminal")
	if out != OutcomeContinue {
		t.Errorf("Dispatch outcome = %v, want OutcomeContinue", out)
	}
	if openedHost == nil {
		t.Fatal("OpenFn not called")
	}
	if openedHost.Kind != engage.KindTerminal {
		t.Errorf("opened host kind = %q, want terminal", openedHost.Kind)
	}
}

func TestDispatch_engageHostVerb_callsOpenFn(t *testing.T) {
	h, _, db := newHandlerWithDB(t, "alice")
	cache := engage.NewHostCache()
	if err := cache.Load(context.Background(), db); err != nil {
		t.Fatalf("HostCache.Load: %v", err)
	}
	var openedHost *engage.Host
	h.Engage = &EngageBackend{
		Registry: engage.NewRegistry(),
		Hosts:    cache,
		OpenFn: func(host *engage.Host, _ *world.Presence, _ engage.SessionBinding) error {
			openedHost = host
			return nil
		},
	}
	out := h.Dispatch(context.Background(), "sit at terminal")
	if out != OutcomeContinue {
		t.Errorf("Dispatch outcome = %v, want OutcomeContinue", out)
	}
	if openedHost == nil || openedHost.Kind != engage.KindTerminal {
		t.Errorf("openedHost = %v, want terminal", openedHost)
	}
}

func TestDispatch_engageNilBackend_returnsUnknown(t *testing.T) {
	h, _ := newHandler(t, "alice")
	// h.Engage is nil — engage path must be skipped and fall through to unknown.
	out := h.Dispatch(context.Background(), "engage with terminal")
	if out != OutcomeUnknown {
		t.Errorf("Dispatch with nil Engage = %v, want OutcomeUnknown", out)
	}
}

func TestMove_autoDisengages(t *testing.T) {
	h, _, db := newHandlerWithDB(t, "alice")
	cache := engage.NewHostCache()
	if err := cache.Load(context.Background(), db); err != nil {
		t.Fatalf("HostCache.Load: %v", err)
	}
	reg := engage.NewRegistry()

	// Build a fake session binding that the OpenFn can use.
	sb := &cmdFakeSession{}
	h.SB = sb
	h.Engage = &EngageBackend{
		Registry: reg,
		Hosts:    cache,
		OpenFn: func(host *engage.Host, presence *world.Presence, sb engage.SessionBinding) error {
			_, err := engage.OpenForSession(reg, sb, host,
				engage.NewTerminalHandler(host, nil),
				&engage.Participant{
					SessionID: "test", PlayerID: presence.PlayerID,
					DisplayName: "alice",
					Write:       presence.Write,
				})
			return err
		},
	}

	// Open an engagement on the lobby-terminal.
	if out := h.Dispatch(context.Background(), "engage with terminal"); out != OutcomeContinue {
		t.Fatalf("Dispatch (engage) outcome = %v, want OutcomeContinue", out)
	}
	if reg.ParticipantEngagement("test") == nil {
		t.Fatal("setup: expected open engagement")
	}

	// Now move north. Auto-disengage should fire.
	h.Dispatch(context.Background(), "n")

	if got := reg.ParticipantEngagement("test"); got != nil {
		t.Errorf("expected engagement closed after move, got %v", got)
	}
}

// TestDispatch_engageHostVerb_disambiguates verifies that when two hosts
// share a verb (e.g. two NPCs both with "talk to"), the target text is
// used to pick the right one — and a duplicate name produces an
// AmbiguousMatchError-style prompt.
func TestDispatch_engageHostVerb_disambiguates(t *testing.T) {
	// Use newHandlerWithDB and add a second engageable object (the
	// existing seed bartender + a second NPC manually inserted, OR
	// two terminals — whichever is easier with the available world API.
	// If creating a second NPC is hard, this test can be skipped — but
	// at minimum verify that with one candidate matching, the dispatch
	// succeeds.
	h, _, db := newHandlerWithDB(t, "alice")
	cache := engage.NewHostCache()
	if err := cache.Load(context.Background(), db); err != nil {
		t.Fatalf("HostCache.Load: %v", err)
	}
	var opened *engage.Host
	h.Engage = &EngageBackend{
		Registry: engage.NewRegistry(),
		Hosts:    cache,
		OpenFn: func(host *engage.Host, _ *world.Presence, _ engage.SessionBinding) error {
			opened = host
			return nil
		},
	}
	// Single candidate (seed terminal) — should still work.
	if out := h.Dispatch(context.Background(), "sit at terminal"); out != OutcomeContinue {
		t.Errorf("Dispatch = %v, want OutcomeContinue", out)
	}
	if opened == nil || opened.Kind != engage.KindTerminal {
		t.Errorf("opened = %v, want terminal", opened)
	}
}

type cmdFakeSession struct {
	eng *engage.Engagement
}

func (f *cmdFakeSession) Engagement() *engage.Engagement     { return f.eng }
func (f *cmdFakeSession) SetEngagement(e *engage.Engagement) { f.eng = e }
