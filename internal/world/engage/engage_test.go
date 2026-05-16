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
