// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package cmd

import (
	"context"
	"errors"
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

// newHandlerWithLevel builds a Handler whose Presence has the requested
// access level. Used by the @npcreload admin-gate tests.
func newHandlerWithLevel(t *testing.T, username string, level auth.AccessLevel) (*Handler, *recordingWriter) {
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
	acc, err := a.Create(context.Background(), username, "hunter22", level)
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
	rw.Drain()
	return &Handler{World: w, Presence: pres}, rw
}

type stubReloader struct {
	mu      sync.Mutex
	calls   int
	failErr error
}

func (s *stubReloader) Reload(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.failErr
}

func (s *stubReloader) called() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func TestDispatchNPCReloadAdminSuccess(t *testing.T) {
	h, rw := newHandlerWithLevel(t, "admin", auth.AccessAdmin)
	stub := &stubReloader{}
	h.NPC = stub
	if got := h.Dispatch(context.Background(), "@npcreload"); got != OutcomeContinue {
		t.Errorf("Dispatch(@npcreload) = %v, want OutcomeContinue", got)
	}
	if stub.called() != 1 {
		t.Errorf("Reload called %d times, want 1", stub.called())
	}
	if !strings.Contains(rw.Drain(), "NPC registry reloaded.") {
		t.Errorf("expected success message")
	}
}

func TestDispatchNPCReloadAdminFailure(t *testing.T) {
	h, rw := newHandlerWithLevel(t, "admin", auth.AccessAdmin)
	stub := &stubReloader{failErr: errors.New("boom")}
	h.NPC = stub
	if got := h.Dispatch(context.Background(), "@npcreload"); got != OutcomeContinue {
		t.Errorf("Dispatch(@npcreload) = %v, want OutcomeContinue", got)
	}
	if stub.called() != 1 {
		t.Errorf("Reload called %d times, want 1", stub.called())
	}
	out := rw.Drain()
	if !strings.Contains(out, "NPC reload failed:") || !strings.Contains(out, "boom") {
		t.Errorf("expected failure message; got:\n%s", out)
	}
}

func TestDispatchNPCReloadNonAdminHiddenAsUnknown(t *testing.T) {
	h, _ := newHandlerWithLevel(t, "bob", auth.AccessPlayer)
	stub := &stubReloader{}
	h.NPC = stub
	if got := h.Dispatch(context.Background(), "@npcreload"); got != OutcomeUnknown {
		t.Errorf("Dispatch(@npcreload) for non-admin = %v, want OutcomeUnknown", got)
	}
	if stub.called() != 0 {
		t.Errorf("Reload called %d times for non-admin, want 0", stub.called())
	}
}

func TestDispatchNPCReloadNoRegistryHiddenAsUnknown(t *testing.T) {
	h, _ := newHandlerWithLevel(t, "admin", auth.AccessAdmin)
	// h.NPC stays nil.
	if got := h.Dispatch(context.Background(), "@npcreload"); got != OutcomeUnknown {
		t.Errorf("Dispatch(@npcreload) with nil NPC = %v, want OutcomeUnknown", got)
	}
}

func TestDidYouMean(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, "Which one?\r\n"},
		{[]string{"a coffee cup"}, "Did you mean a coffee cup?\r\n"},
		{[]string{"a coffee cup", "a teacup"}, "Did you mean a coffee cup or a teacup?\r\n"},
		{[]string{"a", "b", "c"}, "Did you mean a, b, or c?\r\n"},
		{[]string{"a", "b", "c", "d"}, "Did you mean a, b, c, or d?\r\n"},
	}
	for _, tc := range cases {
		if got := didYouMean(tc.in); got != tc.want {
			t.Errorf("didYouMean(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDispatchLookPartialMatchAndDisambiguation(t *testing.T) {
	h, rw := newHandler(t, "alice")
	// Lobby has a keycard; corridor has a coffee cup. Move east to be near it.
	if got := h.Dispatch(context.Background(), "e"); got != OutcomeContinue {
		t.Fatalf("move east outcome = %v", got)
	}
	rw.Drain()

	// Token match: 'cup' should resolve 'coffee cup' — render the long desc.
	h.Dispatch(context.Background(), "look cup")
	out := rw.Drain()
	if strings.Contains(out, "nothing like that here") {
		t.Errorf("look cup should match coffee cup; got refusal:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "lukewarm") {
		t.Errorf("look cup should render the coffee cup's long desc; got:\n%s", out)
	}

	// Token match: 'coffee' should also resolve.
	h.Dispatch(context.Background(), "look coffee")
	out = rw.Drain()
	if !strings.Contains(strings.ToLower(out), "lukewarm") {
		t.Errorf("look coffee should render the coffee cup; got:\n%s", out)
	}
}

