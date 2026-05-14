// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package cmd

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/store"
	"github.com/vaelen/wintermute/internal/world"
)

func newHandler(t *testing.T, username string) (*Handler, *recordingWriter) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "world.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := store.Open(context.Background(), path, logger)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	w, err := world.Load(context.Background(), db, logger)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	a := auth.NewStore(db)
	acc, err := a.Create(context.Background(), username, "hunter22", auth.AccessPlayer)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	playerID, err := w.CreatePlayer(context.Background(), acc)
	if err != nil {
		t.Fatalf("CreatePlayer: %v", err)
	}
	rw := &recordingWriter{}
	pres := &world.Presence{
		PlayerID: playerID,
		Account:  acc,
		Write:    rw.Write,
	}
	if _, err := w.Attach(pres); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	rw.Drain() // discard wake-up message
	return &Handler{World: w, Presence: pres}, rw
}

type recordingWriter struct {
	mu  sync.Mutex
	buf []string
}

func (r *recordingWriter) Write(s string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, s)
	return nil
}

func (r *recordingWriter) Drain() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := strings.Join(r.buf, "")
	r.buf = nil
	return out
}

func TestSplitCmd(t *testing.T) {
	cases := []struct {
		in, wantCmd, wantRest string
	}{
		{"look", "look", ""},
		{"look keycard", "look", "keycard"},
		{"  look  keycard  ", "look", "keycard"},
		{"say hello world", "say", "hello world"},
		{"'hello world", "'", "hello world"},
		{":waves", ":", "waves"},
	}
	for _, c := range cases {
		cmd, rest := splitCmd(strings.TrimSpace(c.in))
		if cmd != c.wantCmd || rest != c.wantRest {
			t.Errorf("splitCmd(%q) = (%q, %q), want (%q, %q)",
				c.in, cmd, rest, c.wantCmd, c.wantRest)
		}
	}
}

func TestCanonicalDirection(t *testing.T) {
	cases := map[string]string{
		"n": "n", "north": "n", "NORTH": "n",
		"s": "s", "south": "s",
		"e": "e", "east": "e",
		"w": "w", "west": "w",
		"u": "u", "up": "u",
		"d": "d", "down": "d",
		"in": "in", "out": "out",
	}
	for in, want := range cases {
		if got := canonicalDirection(in); got != want {
			t.Errorf("canonicalDirection(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDispatchUnknownReturnsUnknown(t *testing.T) {
	h, _ := newHandler(t, "alice")
	if got := h.Dispatch(context.Background(),"frobnicate"); got != OutcomeUnknown {
		t.Errorf("Dispatch(frobnicate) = %v, want OutcomeUnknown", got)
	}
}

func TestDispatchQuit(t *testing.T) {
	h, rw := newHandler(t, "alice")
	if got := h.Dispatch(context.Background(),"quit"); got != OutcomeQuit {
		t.Errorf("Dispatch(quit) = %v, want OutcomeQuit", got)
	}
	if !strings.Contains(rw.Drain(), "Goodbye") {
		t.Errorf("expected goodbye message")
	}
}

func TestDispatchLookShowsRoom(t *testing.T) {
	h, rw := newHandler(t, "alice")
	if got := h.Dispatch(context.Background(),"look"); got != OutcomeContinue {
		t.Errorf("Dispatch(look) = %v, want OutcomeContinue", got)
	}
	out := rw.Drain()
	if !strings.Contains(out, "The Lobby") {
		t.Errorf("look output missing room name:\n%s", out)
	}
}

func TestDispatchMoveAndNoExit(t *testing.T) {
	h, rw := newHandler(t, "alice")
	h.Dispatch(context.Background(),"e")
	out := rw.Drain()
	if !strings.Contains(out, "Corridor") {
		t.Errorf("after move east, expected corridor in output:\n%s", out)
	}
	h.Dispatch(context.Background(),"s") // no south exit from corridor
	if !strings.Contains(rw.Drain(), "can't go that way") {
		t.Errorf("expected 'can't go that way' for bad direction")
	}
}

func TestDispatchSayShowsSelfEcho(t *testing.T) {
	h, rw := newHandler(t, "alice")
	h.Dispatch(context.Background(),"say hello")
	out := rw.Drain()
	if !strings.Contains(out, `You say, "hello"`) {
		t.Errorf("expected self echo; got:\n%s", out)
	}
}

func TestDispatchSayApostropheAlias(t *testing.T) {
	h, rw := newHandler(t, "alice")
	h.Dispatch(context.Background(),"'hello")
	if !strings.Contains(rw.Drain(), `You say, "hello"`) {
		t.Errorf("apostrophe alias for say did not produce self echo")
	}
}

func TestDispatchEmoteColonAlias(t *testing.T) {
	h, rw := newHandler(t, "alice")
	h.Dispatch(context.Background(),":waves")
	out := rw.Drain()
	if !strings.Contains(out, "alice waves") {
		t.Errorf("colon alias for emote did not produce expected line:\n%s", out)
	}
}

func TestDispatchTakeDropInventory(t *testing.T) {
	h, rw := newHandler(t, "alice")
	h.Dispatch(context.Background(),"get keycard")
	out := rw.Drain()
	if !strings.Contains(out, "pick up keycard") {
		t.Errorf("take did not confirm:\n%s", out)
	}
	h.Dispatch(context.Background(),"inventory")
	out = rw.Drain()
	if !strings.Contains(out, "keycard") {
		t.Errorf("inventory missing keycard:\n%s", out)
	}
	h.Dispatch(context.Background(),"drop keycard")
	out = rw.Drain()
	if !strings.Contains(out, "drop keycard") {
		t.Errorf("drop did not confirm:\n%s", out)
	}
}

func TestDispatchHelpListsCommands(t *testing.T) {
	h, rw := newHandler(t, "alice")
	h.Dispatch(context.Background(),"help")
	out := rw.Drain()
	for _, want := range []string{"look", "say", "inventory", "quit"} {
		if !strings.Contains(out, want) {
			t.Errorf("help missing %q in:\n%s", want, out)
		}
	}
}

func TestDispatchWhoFiltersSelf(t *testing.T) {
	h, rw := newHandler(t, "alice")
	h.Dispatch(context.Background(),"who")
	out := rw.Drain()
	if !strings.Contains(out, "No one") {
		t.Errorf("expected 'No one' since only self is online; got:\n%s", out)
	}
}

func TestDispatchEmptyContinues(t *testing.T) {
	h, _ := newHandler(t, "alice")
	if got := h.Dispatch(context.Background(),""); got != OutcomeContinue {
		t.Errorf("Dispatch('') = %v, want OutcomeContinue", got)
	}
}
