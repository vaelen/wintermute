// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

//go:build test

package npc

import (
	"strings"
	"testing"
	"time"

	"github.com/vaelen/wintermute/internal/world"
	"github.com/vaelen/wintermute/internal/world/engage"
)

// dummyEngageHandler satisfies engage.Handler without doing anything.
type dummyEngageHandler struct{}

func (dummyEngageHandler) OnOpen(*engage.Participant)                      {}
func (dummyEngageHandler) OnClose(*engage.Participant, engage.CloseReason) {}
func (dummyEngageHandler) Handle(*engage.Participant, string)               {}

// TestHandleSay_brushOffWhenNPCEngagedWithSomeoneElse verifies that when an
// NPC is currently in a private engagement with player A, a say from player B
// targeting the NPC by name produces a brush-off broadcast instead of
// dispatching an LLM reply.
func TestHandleSay_brushOffWhenNPCEngagedWithSomeoneElse(t *testing.T) {
	e := newFakeBackendEnv(t)
	bartenderID := e.bartenderID(t)
	lobby := lobbyID(t, e)

	// Alice is engaged with the bartender.
	alice := e.attachPlayer(t, "alice")
	engageReg := engage.NewRegistry()
	_, err := engageReg.Open(
		&engage.Host{ObjectID: bartenderID, Kind: engage.KindNPC},
		dummyEngageHandler{},
		&engage.Participant{
			SessionID:   "alice-sess",
			PlayerID:    alice.PlayerID,
			DisplayName: "alice",
		},
	)
	if err != nil {
		t.Fatalf("engage.Open: %v", err)
	}
	e.reg.SetEngageLookup(engageReg)

	// Bob enters the room and says "hi bartender".
	bob := e.attachPlayer(t, "bob")
	alice.drain()
	bob.drain()

	e.reg.HandleSay(lobby, bob.PlayerID, "bob", "hi bartender")

	// Give BroadcastToRoom (synchronous) a moment to propagate.
	time.Sleep(50 * time.Millisecond)

	got := alice.drain() + bob.drain()
	if !strings.Contains(got, "raises a finger to bob") {
		t.Errorf("expected brush-off broadcast; got %q", got)
	}
}

// TestHandleSay_noInterruptWhenEngagedParticipantSpeaks verifies that the
// engaged participant (alice) talking to the bartender is NOT interrupted by
// the brush-off path — the engagement handler owns that conversation.
func TestHandleSay_noInterruptWhenEngagedParticipantSpeaks(t *testing.T) {
	e := newFakeBackendEnv(t)
	bartenderID := e.bartenderID(t)
	lobby := lobbyID(t, e)

	alice := e.attachPlayer(t, "alice")
	bob := e.attachPlayer(t, "bob") // presence ensures rule (b) can't fire

	engageReg := engage.NewRegistry()
	_, err := engageReg.Open(
		&engage.Host{ObjectID: bartenderID, Kind: engage.KindNPC},
		dummyEngageHandler{},
		&engage.Participant{
			SessionID:   "alice-sess",
			PlayerID:    alice.PlayerID,
			DisplayName: "alice",
		},
	)
	if err != nil {
		t.Fatalf("engage.Open: %v", err)
	}
	e.reg.SetEngageLookup(engageReg)

	alice.drain()
	bob.drain()

	// Alice (the engaged participant) speaks via the M3 say path. The
	// engagement-check sees her playerID in the participants list and does
	// NOT brush off. The NPC is engaged so no LLM dispatch either — both
	// alice and bob should see silence from HandleSay.
	e.reg.HandleSay(lobby, alice.PlayerID, "alice", "hi bartender")
	time.Sleep(50 * time.Millisecond)

	got := alice.drain() + bob.drain()
	// We specifically should NOT see a brush-off aimed at alice.
	if strings.Contains(got, "raises a finger to alice") {
		t.Errorf("brush-off should not fire for the engaged participant; got %q", got)
	}
}

// TestHandleSay_noEngageLookupNoChange verifies that when SetEngageLookup has
// NOT been called (engage == nil), HandleSay behaves exactly as before: it
// dispatches to the LLM when the NPC is addressed.
func TestHandleSay_noEngageLookupNoChange(t *testing.T) {
	e := newFakeBackendEnv(t)
	lobby := lobbyID(t, e)

	alice := e.attachPlayer(t, "alice")
	bob := e.attachPlayer(t, "bob") // disable rule (b)
	alice.drain()
	bob.drain()

	// No SetEngageLookup call — default nil engage field.
	e.reg.HandleSay(lobby, alice.PlayerID, "alice", "hi bartender")
	alice.waitFor(t, bartenderResponse, 2*time.Second)
}

// TestHandleSay_brushOffNotSentWhenNPCNotEngaged verifies that when the
// engage registry is wired but the NPC has no open engagement, HandleSay
// dispatches normally (no spurious brush-off).
func TestHandleSay_brushOffNotSentWhenNPCNotEngaged(t *testing.T) {
	e := newFakeBackendEnv(t)
	lobby := lobbyID(t, e)

	alice := e.attachPlayer(t, "alice")
	bob := e.attachPlayer(t, "bob") // disable rule (b)

	// Wire an empty engage registry — no engagements open.
	e.reg.SetEngageLookup(engage.NewRegistry())

	alice.drain()
	bob.drain()

	e.reg.HandleSay(lobby, alice.PlayerID, "alice", "hi bartender")
	alice.waitFor(t, bartenderResponse, 2*time.Second)

	// Verify no brush-off was broadcast.
	got := alice.drain() + bob.drain()
	if strings.Contains(got, "raises a finger") {
		t.Errorf("unexpected brush-off when NPC is not engaged; got %q", got)
	}
}

// TestEngagedWithSpeaker unit-tests the helper directly.
func TestEngagedWithSpeaker(t *testing.T) {
	aliceID := world.ObjectID(7)
	bobID := world.ObjectID(42)

	eng := &engage.Engagement{
		Host: &engage.Host{ObjectID: 1},
		Participants: []*engage.Participant{
			{SessionID: "alice-sess", PlayerID: aliceID, DisplayName: "alice"},
		},
	}

	if !engagedWithSpeaker(eng, aliceID) {
		t.Error("expected true for alice (the participant)")
	}
	if engagedWithSpeaker(eng, bobID) {
		t.Error("expected false for bob (not a participant)")
	}
}
