// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package engage

import (
	"errors"
	"testing"

	"github.com/vaelen/wintermute/internal/world"
)

func TestRegistryOpenAndLookup(t *testing.T) {
	r := NewRegistry()
	host := &Host{ObjectID: 42, Kind: KindTerminal}
	h := &fakeHandler{}
	p := &Participant{SessionID: "s1", PlayerID: 7, DisplayName: "alice"}

	eng, err := r.Open(host, h, p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if eng == nil {
		t.Fatal("expected non-nil engagement")
	}
	if h.opens != 1 {
		t.Errorf("OnOpen called %d times, want 1", h.opens)
	}
	if got := r.HostEngagement(42); got != eng {
		t.Errorf("HostEngagement(42) = %v, want %v", got, eng)
	}
	if got := r.ParticipantEngagement("s1"); got != eng {
		t.Errorf("ParticipantEngagement(s1) = %v, want %v", got, eng)
	}
}

func TestRegistryOpenFailsWhenHostBusy(t *testing.T) {
	r := NewRegistry()
	host := &Host{ObjectID: 42, Kind: KindTerminal}
	_, err := r.Open(host, &fakeHandler{}, &Participant{SessionID: "s1", PlayerID: 7})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	_, err = r.Open(host, &fakeHandler{}, &Participant{SessionID: "s2", PlayerID: 8})
	if !errors.Is(err, ErrHostBusy) {
		t.Errorf("second Open: err = %v, want ErrHostBusy", err)
	}
}

func TestRegistryOpenFailsWhenParticipantAlreadyEngaged(t *testing.T) {
	r := NewRegistry()
	_, err := r.Open(&Host{ObjectID: 1, Kind: KindTerminal}, &fakeHandler{},
		&Participant{SessionID: "s1", PlayerID: 7})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	_, err = r.Open(&Host{ObjectID: 2, Kind: KindNPC}, &fakeHandler{},
		&Participant{SessionID: "s1", PlayerID: 7})
	if !errors.Is(err, ErrAlreadyEngaged) {
		t.Errorf("second Open: err = %v, want ErrAlreadyEngaged", err)
	}
}

func TestRegistryClose(t *testing.T) {
	r := NewRegistry()
	h := &fakeHandler{}
	eng, _ := r.Open(&Host{ObjectID: 1, Kind: KindTerminal}, h,
		&Participant{SessionID: "s1", PlayerID: 7})

	r.Close(eng, CloseVoluntary)
	if h.closes != 1 {
		t.Errorf("OnClose called %d times, want 1", h.closes)
	}
	if h.lastReason != CloseVoluntary {
		t.Errorf("close reason = %v, want voluntary", h.lastReason)
	}
	if r.HostEngagement(1) != nil {
		t.Error("HostEngagement should be nil after Close")
	}
	if r.ParticipantEngagement("s1") != nil {
		t.Error("ParticipantEngagement should be nil after Close")
	}
}

func TestRegistryCloseIsIdempotent(t *testing.T) {
	r := NewRegistry()
	h := &fakeHandler{}
	eng, _ := r.Open(&Host{ObjectID: 1, Kind: KindTerminal}, h,
		&Participant{SessionID: "s1", PlayerID: 7})

	r.Close(eng, CloseVoluntary)
	r.Close(eng, CloseVoluntary) // second call should be a no-op

	if h.closes != 1 {
		t.Errorf("OnClose called %d times after two Close calls, want 1", h.closes)
	}
}

var _ world.ObjectID = world.ObjectID(0) // keep the import "used"
