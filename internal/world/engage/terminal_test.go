// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package engage

import (
	"strings"
	"sync"
	"testing"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/world"
)

func TestTerminalHandler_promptOnOpen(t *testing.T) {
	var out strings.Builder
	host := &Host{ObjectID: 1, Kind: KindTerminal, Prompt: "terminal> "}
	h := NewTerminalHandler(host, nil)
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
	h := NewTerminalHandler(host, nil)
	p := &Participant{Write: func(s string) error { out.WriteString(s); return nil }}
	h.OnOpen(p)
	if !strings.Contains(out.String(), "terminal>") {
		t.Errorf("default prompt missing: %q", out.String())
	}
}

func TestTerminalHandler_unknownCommand(t *testing.T) {
	var out strings.Builder
	host := &Host{ObjectID: 1, Kind: KindTerminal, Prompt: "terminal> "}
	h := NewTerminalHandler(host, nil)
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
	h := NewTerminalHandler(host, nil)
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
	h := NewTerminalHandler(host, nil)
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
	h := NewTerminalHandler(host, nil)
	p := &Participant{Write: func(s string) error { out.WriteString(s); return nil }}
	h.Handle(p, "")
	if !strings.Contains(out.String(), "terminal>") {
		t.Errorf("empty-line prompt missing: %q", out.String())
	}
}

func TestTerminalHandler_onCloseInvoked(t *testing.T) {
	called := false
	h := NewTerminalHandler(&Host{ObjectID: 1, Kind: KindTerminal},
		func() { called = true })
	p := &Participant{Write: func(string) error { return nil }}
	h.OnClose(p, CloseVoluntary)
	if !called {
		t.Error("onClose callback was not invoked")
	}
}

// TestTerminalHandler_composeStateRaceFree — PR review fix: the
// engage.Handler contract requires concurrent safety. Drive Handle from
// multiple goroutines to exercise the mutex around the compose state
// under `go test -race`.
func TestTerminalHandler_composeStateRaceFree(t *testing.T) {
	h := NewTerminalHandler(&Host{ObjectID: 1, Kind: KindTerminal}, nil)
	// No deps — Handle dispatches to the "not yet implemented" stubs and
	// returns; the compose field is only touched by Handle/writePrompt.
	p := &Participant{
		SessionID: "s1", PlayerID: 7,
		Write: func(string) error { return nil },
	}
	lines := []string{"mail", "bb", "help", "frobnicate", ""}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			h.Handle(p, lines[i%len(lines)])
		}(i)
	}
	wg.Wait()
}

// TestTerminalHandler_commitComposeNilAccountDoesNotPanic — PR review
// fix: commitCompose previously discarded AccountFor's error and passed
// nil into mail.Service.Send, which dereferences from.ID/from.Username
// and panics. The bail-out path should write a clear error instead.
func TestTerminalHandler_commitComposeNilAccountDoesNotPanic(t *testing.T) {
	var out strings.Builder
	h := NewTerminalHandler(&Host{ObjectID: 1, Kind: KindTerminal}, nil)
	h.SetDeps(&TerminalDeps{
		// Mail/Boards intentionally nil — commitCompose must bail out on
		// the AccountFor failure before touching them.
		AccountFor: func(_ world.ObjectID) (*auth.Account, error) {
			return nil, nil
		},
	})
	p := &Participant{
		SessionID: "s1", PlayerID: 7,
		Write: func(s string) error { out.WriteString(s); return nil },
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("commitCompose panicked: %v", r)
		}
	}()
	h.commitCompose(p, &composeState{
		kind: "mail-send", args: []string{"bob"}, subject: "subj", body: []string{"body"},
	})
	if !strings.Contains(out.String(), "cannot resolve account") {
		t.Errorf("expected resolve-account error in output, got %q", out.String())
	}
}
