// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package engage

import (
	"testing"

	"github.com/vaelen/wintermute/internal/world"
)

func TestPolicyZeroValueIsAllRestrictive(t *testing.T) {
	var p Policy
	if p.VisibleActivity || p.AudibleContent || p.Joinable {
		t.Errorf("zero Policy should be fully restrictive, got %+v", p)
	}
}

func TestHostKindConstants(t *testing.T) {
	cases := []struct {
		kind string
		want string
	}{
		{KindTerminal, "terminal"},
		{KindNPC, "npc"},
		{KindCustom, "custom"},
	}
	for _, c := range cases {
		if c.kind != c.want {
			t.Errorf("kind constant %q != %q", c.kind, c.want)
		}
	}
}

func TestCloseReasonString(t *testing.T) {
	cases := []struct {
		r    CloseReason
		want string
	}{
		{CloseVoluntary, "voluntary"},
		{CloseMovement, "movement"},
		{CloseForced, "forced"},
		{CloseDisconnect, "disconnect"},
	}
	for _, c := range cases {
		if got := c.r.String(); got != c.want {
			t.Errorf("CloseReason(%d).String() = %q, want %q", c.r, got, c.want)
		}
	}
}

func TestHostHasObjectID(t *testing.T) {
	h := &Host{ObjectID: world.ObjectID(42), Kind: KindTerminal}
	if h.ObjectID != 42 {
		t.Errorf("Host.ObjectID = %d, want 42", h.ObjectID)
	}
}

type fakeHandler struct {
	opens      int
	closes     int
	lines      []string
	lastReason CloseReason
}

func (f *fakeHandler) OnOpen(_ *Participant)                 { f.opens++ }
func (f *fakeHandler) OnClose(_ *Participant, r CloseReason) { f.closes++; f.lastReason = r }
func (f *fakeHandler) Handle(_ *Participant, line string)    { f.lines = append(f.lines, line) }

func TestEngagementHasParticipant(t *testing.T) {
	p1 := &Participant{SessionID: "s1", DisplayName: "alice"}
	p2 := &Participant{SessionID: "s2", DisplayName: "bob"}
	eng := &Engagement{
		Host:         &Host{ObjectID: 1, Kind: KindTerminal},
		Handler:      &fakeHandler{},
		Participants: []*Participant{p1},
	}
	if !eng.HasParticipant("s1") {
		t.Error("expected s1 to be a participant")
	}
	if eng.HasParticipant("s2") {
		t.Errorf("did not expect s2 to be a participant; got %v", eng.Participants)
	}
	_ = p2
}
