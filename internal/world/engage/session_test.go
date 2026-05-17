// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package engage

import "testing"

func TestOpenForSessionAttachesAndDetaches(t *testing.T) {
	r := NewRegistry()
	ss := &fakeSession{id: "s1"}
	host := &Host{ObjectID: 1, Kind: KindTerminal}
	h := &fakeHandler{}

	eng, err := OpenForSession(r, ss, host, h, &Participant{
		SessionID: "s1", PlayerID: 7, DisplayName: "alice",
	})
	if err != nil {
		t.Fatalf("OpenForSession: %v", err)
	}
	if ss.engagement != eng {
		t.Error("session should be marked engaged")
	}

	CloseForSession(r, ss, CloseVoluntary)
	if ss.engagement != nil {
		t.Error("session engagement should be nil after Close")
	}
	if r.HostEngagement(1) != nil {
		t.Error("registry should no longer index the host")
	}
}

func TestCloseForSession_noopIfNotEngaged(t *testing.T) {
	r := NewRegistry()
	ss := &fakeSession{id: "s1"}
	// no engagement set; Close should be a silent no-op.
	CloseForSession(r, ss, CloseVoluntary)
	if ss.engagement != nil {
		t.Error("engagement should remain nil")
	}
}

type fakeSession struct {
	id         string
	engagement *Engagement
}

func (f *fakeSession) Engagement() *Engagement     { return f.engagement }
func (f *fakeSession) SetEngagement(e *Engagement) { f.engagement = e }
