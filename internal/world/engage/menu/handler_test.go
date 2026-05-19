// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package menu

import (
	"strings"
	"testing"

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
