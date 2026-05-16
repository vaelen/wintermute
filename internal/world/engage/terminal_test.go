// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package engage

import (
	"strings"
	"testing"
)

func TestTerminalHandler_promptOnOpen(t *testing.T) {
	var out strings.Builder
	host := &Host{ObjectID: 1, Kind: KindTerminal, Prompt: "terminal> "}
	h := NewTerminalHandler(host)
	p := &Participant{
		SessionID: "s1", PlayerID: 7, DisplayName: "alice",
		Write: func(s string) error { out.WriteString(s); return nil },
	}
	h.OnOpen(p)
	if !strings.Contains(out.String(), "terminal>") {
		t.Errorf("OnOpen output %q does not contain prompt", out.String())
	}
}

func TestTerminalHandler_promptDefaults(t *testing.T) {
	var out strings.Builder
	// No prompt configured; default applies.
	host := &Host{ObjectID: 1, Kind: KindTerminal}
	h := NewTerminalHandler(host)
	p := &Participant{Write: func(s string) error { out.WriteString(s); return nil }}
	h.OnOpen(p)
	if !strings.Contains(out.String(), "terminal>") {
		t.Errorf("default prompt missing: %q", out.String())
	}
}

func TestTerminalHandler_unknownCommand(t *testing.T) {
	var out strings.Builder
	host := &Host{ObjectID: 1, Kind: KindTerminal, Prompt: "terminal> "}
	h := NewTerminalHandler(host)
	p := &Participant{
		SessionID: "s1",
		Write:     func(s string) error { out.WriteString(s); return nil },
	}
	h.Handle(p, "frobnicate")
	if !strings.Contains(out.String(), "command not recognized") {
		t.Errorf("output = %q; want 'command not recognized'", out.String())
	}
}

func TestTerminalHandler_stubCommands(t *testing.T) {
	var out strings.Builder
	host := &Host{ObjectID: 1, Kind: KindTerminal}
	h := NewTerminalHandler(host)
	p := &Participant{Write: func(s string) error { out.WriteString(s); return nil }}
	stubs := []string{"mail", "bb", "bbread", "bbpost", "bbcatchup", "upload", "download"}
	for _, cmd := range stubs {
		out.Reset()
		h.Handle(p, cmd)
		if !strings.Contains(out.String(), "not yet implemented (M6)") {
			t.Errorf("Handle(%q) output = %q; want stub line", cmd, out.String())
		}
	}
}

func TestTerminalHandler_helpListsCommands(t *testing.T) {
	var out strings.Builder
	host := &Host{ObjectID: 1, Kind: KindTerminal}
	h := NewTerminalHandler(host)
	p := &Participant{Write: func(s string) error { out.WriteString(s); return nil }}
	h.Handle(p, "help")
	got := out.String()
	for _, cmd := range []string{"mail", "bb", "bbread", "bbpost", "upload", "download"} {
		if !strings.Contains(got, cmd) {
			t.Errorf("help output missing %q: %q", cmd, got)
		}
	}
}

func TestTerminalHandler_emptyLineRedrawsPrompt(t *testing.T) {
	var out strings.Builder
	host := &Host{ObjectID: 1, Kind: KindTerminal}
	h := NewTerminalHandler(host)
	p := &Participant{Write: func(s string) error { out.WriteString(s); return nil }}
	h.Handle(p, "")
	if !strings.Contains(out.String(), "terminal>") {
		t.Errorf("empty-line prompt missing: %q", out.String())
	}
}
