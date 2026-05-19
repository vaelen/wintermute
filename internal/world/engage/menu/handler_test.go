// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package menu

import (
	"strings"
	"testing"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/world"
	"github.com/vaelen/wintermute/internal/world/engage"
)

// captureWriter is a Participant.Write that records every line for
// assertions.
func captureWriter(buf *strings.Builder) func(string) error {
	return func(s string) error { buf.WriteString(s); return nil }
}

func newMenuParticipant(buf *strings.Builder) *engage.Participant {
	return &engage.Participant{
		SessionID: "s1", PlayerID: 7, DisplayName: "alice",
		Write: captureWriter(buf),
	}
}

func newMenuHost(menu []engage.MenuEntry) *engage.Host {
	return &engage.Host{
		ObjectID: 100, Kind: engage.KindMenuTerminal,
		Menu: menu,
	}
}

func TestHandler_OnOpen_rendersMainMenu(t *testing.T) {
	var buf strings.Builder
	h := NewHandler(newMenuHost([]engage.MenuEntry{
		{Feature: engage.FeatureMail},
		{Feature: engage.FeatureBoards},
		{Feature: engage.FeatureFiles, Area: "dropbox"},
	}), nil)
	h.OnOpen(newMenuParticipant(&buf))
	out := buf.String()
	if !strings.Contains(out, "Mail") {
		t.Errorf("output missing Mail entry: %q", out)
	}
	if !strings.Contains(out, "Boards") {
		t.Errorf("output missing Boards entry: %q", out)
	}
	if !strings.Contains(out, "Files") {
		t.Errorf("output missing Files entry: %q", out)
	}
	if !strings.Contains(out, "Quit") {
		t.Errorf("output missing Quit entry: %q", out)
	}
	if !strings.Contains(out, "Select:") {
		t.Errorf("output missing 'Select:' prompt: %q", out)
	}
}

func TestHandler_OnOpen_singleFeatureKioskRendersOnlyThatFeature(t *testing.T) {
	var buf strings.Builder
	h := NewHandler(newMenuHost([]engage.MenuEntry{
		{Feature: engage.FeatureMail},
	}), nil)
	h.OnOpen(newMenuParticipant(&buf))
	out := buf.String()
	if !strings.Contains(out, "Mail") {
		t.Errorf("output missing Mail entry: %q", out)
	}
	if strings.Contains(out, "Boards") {
		t.Errorf("kiosk output unexpectedly contains Boards: %q", out)
	}
	if strings.Contains(out, "Files") {
		t.Errorf("kiosk output unexpectedly contains Files: %q", out)
	}
}

func TestHandler_filesEntryShowsAreaInLabel(t *testing.T) {
	var buf strings.Builder
	h := NewHandler(newMenuHost([]engage.MenuEntry{
		{Feature: engage.FeatureFiles, Area: "dropbox"},
	}), nil)
	h.OnOpen(newMenuParticipant(&buf))
	out := buf.String()
	if !strings.Contains(out, "dropbox") {
		t.Errorf("files label missing area name: %q", out)
	}
}

func TestHandler_Q_callsDisengage(t *testing.T) {
	var buf strings.Builder
	called := false
	h := NewHandler(newMenuHost([]engage.MenuEntry{
		{Feature: engage.FeatureMail},
	}), nil)
	h.SetDisengage(func() { called = true })
	p := newMenuParticipant(&buf)
	h.OnOpen(p)
	buf.Reset()
	h.Handle(p, "Q")
	if !called {
		t.Error("disengage callback not invoked on Q")
	}
}

func TestHandler_q_lowercase_alsoDisengages(t *testing.T) {
	called := false
	h := NewHandler(newMenuHost(nil), nil)
	h.SetDisengage(func() { called = true })
	var buf strings.Builder
	p := newMenuParticipant(&buf)
	h.OnOpen(p)
	h.Handle(p, "q")
	if !called {
		t.Error("disengage not invoked on lowercase q")
	}
}

func TestHandler_unknownInput_redrawsMainMenuWithHint(t *testing.T) {
	var buf strings.Builder
	h := NewHandler(newMenuHost([]engage.MenuEntry{
		{Feature: engage.FeatureMail},
	}), nil)
	p := newMenuParticipant(&buf)
	h.OnOpen(p)
	buf.Reset()
	h.Handle(p, "zzz")
	out := buf.String()
	if !strings.Contains(out, "Mail") {
		t.Errorf("after unknown input, main menu should re-render: %q", out)
	}
}

func TestHandler_OnClose_invokesCallback(t *testing.T) {
	called := false
	h := NewHandler(newMenuHost(nil), func() { called = true })
	h.OnClose(&engage.Participant{}, engage.CloseVoluntary)
	if !called {
		t.Error("OnClose callback not invoked")
	}
}

func TestHandler_OnOpen_emitsClearScreen(t *testing.T) {
	var buf strings.Builder
	h := NewHandler(newMenuHost(nil), nil)
	h.OnOpen(newMenuParticipant(&buf))
	if !strings.HasPrefix(buf.String(), ClearScreen) {
		t.Errorf("first frame did not begin with ClearScreen: %q", buf.String()[:20])
	}
}

func TestHandler_implementsEngageHandler(t *testing.T) {
	var _ engage.Handler = NewHandler(newMenuHost(nil), nil)
}

func TestHandler_adminEntry_hiddenFromNonAdmin(t *testing.T) {
	var buf strings.Builder
	h := NewHandler(newMenuHost([]engage.MenuEntry{
		{Feature: engage.FeatureMail},
		{Feature: engage.FeatureAdmin},
		{Feature: engage.FeatureBoards},
	}), nil)
	h.SetDeps(&engage.TerminalDeps{
		AccountFor: func(_ world.ObjectID) (*auth.Account, error) {
			return &auth.Account{ID: 1, Username: "alice", AccessLevel: auth.AccessPlayer}, nil
		},
	})
	h.OnOpen(newMenuParticipant(&buf))
	out := buf.String()
	if strings.Contains(out, "admin console") || strings.Contains(strings.ToLower(out), "admin") {
		t.Errorf("non-admin should not see Admin entry: %q", out)
	}
	if !strings.Contains(out, "1) Mail") && !strings.Contains(out, "1)  Mail") {
		t.Errorf("Mail should be numbered 1 for non-admin: %q", out)
	}
	if !strings.Contains(out, "2) Message Boards") && !strings.Contains(out, "2)  Message Boards") {
		t.Errorf("Boards should be numbered 2 (admin gap closed) for non-admin: %q", out)
	}
}

func TestHandler_adminEntry_visibleToAdmin(t *testing.T) {
	var buf strings.Builder
	h := NewHandler(newMenuHost([]engage.MenuEntry{
		{Feature: engage.FeatureMail},
		{Feature: engage.FeatureAdmin},
	}), nil)
	h.SetDeps(&engage.TerminalDeps{
		AccountFor: func(_ world.ObjectID) (*auth.Account, error) {
			return &auth.Account{ID: 1, Username: "root", AccessLevel: auth.AccessAdmin}, nil
		},
	})
	h.OnOpen(newMenuParticipant(&buf))
	out := buf.String()
	if !strings.Contains(out, "Admin") {
		t.Errorf("admin should see Admin entry: %q", out)
	}
}

func TestHandler_adminEntry_pressingNumberOnNonAdminGoesToBoardsNotAdmin(t *testing.T) {
	// Numbering after filtering: 1) Mail, 2) Boards. Pressing 2 must
	// open Boards (the entry that took the admin slot), not panic on
	// the filtered-out admin entry.
	var buf strings.Builder
	h := NewHandler(newMenuHost([]engage.MenuEntry{
		{Feature: engage.FeatureMail},
		{Feature: engage.FeatureAdmin},
		{Feature: engage.FeatureBoards},
	}), nil)
	h.SetDeps(&engage.TerminalDeps{
		AccountFor: func(_ world.ObjectID) (*auth.Account, error) {
			return &auth.Account{ID: 1, Username: "alice", AccessLevel: auth.AccessPlayer}, nil
		},
	})
	p := newMenuParticipant(&buf)
	h.OnOpen(p)
	buf.Reset()
	h.Handle(p, "2")
	out := buf.String()
	if !strings.Contains(out, "Boards") {
		t.Errorf("pressing 2 should open Boards submenu: %q", out)
	}
}
