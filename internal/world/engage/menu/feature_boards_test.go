// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package menu

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/boards"
	ftnnetworks "github.com/vaelen/wintermute/internal/ftn/networks"
	"github.com/vaelen/wintermute/internal/ftn/msgid"
	"github.com/vaelen/wintermute/internal/store"
	"github.com/vaelen/wintermute/internal/world"
	"github.com/vaelen/wintermute/internal/world/engage"
)

type boardsFixture struct {
	db    *store.DB
	svc   *boards.Service
	alice *auth.Account
	bob   *auth.Account
}

func setupBoards(t *testing.T) *boardsFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "menu_boards.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := store.Open(context.Background(), path, logger)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := ftnnetworks.Bootstrap(ctx, db, nil, logger); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	as := auth.NewStore(db)
	alice, err := as.Create(ctx, "alice", "alicepass", auth.AccessPlayer)
	if err != nil {
		t.Fatalf("alice: %v", err)
	}
	bob, err := as.Create(ctx, "bob", "bobpass", auth.AccessPlayer)
	if err != nil {
		t.Fatalf("bob: %v", err)
	}
	svc := boards.NewService(db, msgid.NewIssuer(db), boards.ServiceOptions{
		PID: "Wintermute/test", ServerName: "Wintermute", Tearline: "--- Wintermute/test",
	})
	if _, err := svc.CreateBoard(ctx, boards.CreateBoardSpec{
		Slug: "general", Name: "General Chat", NetworkSlug: "local",
	}); err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	return &boardsFixture{db: db, svc: svc, alice: alice, bob: bob}
}

func newBoardsHandler(t *testing.T, f *boardsFixture) *Handler {
	t.Helper()
	h := NewHandler(&engage.Host{
		ObjectID: 100, Kind: engage.KindMenuTerminal,
		Menu: []engage.MenuEntry{{Feature: engage.FeatureBoards}},
	}, nil)
	h.SetDeps(&engage.TerminalDeps{
		RootCtx: context.Background(),
		Boards:  f.svc,
		AccountFor: func(_ world.ObjectID) (*auth.Account, error) {
			return f.alice, nil
		},
	})
	return h
}

func TestBoardsSubmenu_listsBoards(t *testing.T) {
	f := setupBoards(t)
	h := newBoardsHandler(t, f)
	var buf strings.Builder
	p := newMenuParticipant(&buf)
	h.OnOpen(p)
	buf.Reset()
	h.Handle(p, "1")
	out := buf.String()
	if !strings.Contains(out, "general") {
		t.Errorf("boards menu missing 'general': %q", out)
	}
	if !strings.Contains(out, "Back") {
		t.Errorf("boards menu missing Back: %q", out)
	}
}

func TestBoardsSubmenu_pickBoard_showsThreads(t *testing.T) {
	f := setupBoards(t)
	if _, err := f.svc.Post(context.Background(), "general", f.bob, "hello", "hi there"); err != nil {
		t.Fatalf("Post: %v", err)
	}
	h := newBoardsHandler(t, f)
	var buf strings.Builder
	p := newMenuParticipant(&buf)
	h.OnOpen(p)
	h.Handle(p, "1") // boards
	buf.Reset()
	h.Handle(p, "1") // first board
	out := buf.String()
	if !strings.Contains(out, "hello") {
		t.Errorf("threads view missing post subject: %q", out)
	}
	if !strings.Contains(out, "New") {
		t.Errorf("threads view missing 'New post' action: %q", out)
	}
	if !strings.Contains(out, "Catch") {
		t.Errorf("threads view missing 'Catch up' action: %q", out)
	}
}

func TestBoardsSubmenu_pickThread_showsThreadAndReply(t *testing.T) {
	f := setupBoards(t)
	if _, err := f.svc.Post(context.Background(), "general", f.bob, "hello", "hi there"); err != nil {
		t.Fatalf("Post: %v", err)
	}
	h := newBoardsHandler(t, f)
	var buf strings.Builder
	p := newMenuParticipant(&buf)
	h.OnOpen(p)
	h.Handle(p, "1") // boards
	h.Handle(p, "1") // first board
	buf.Reset()
	h.Handle(p, "1") // first thread
	out := buf.String()
	if !strings.Contains(out, "hi there") {
		t.Errorf("thread view missing body: %q", out)
	}
	if !strings.Contains(out, "Reply") {
		t.Errorf("thread view missing Reply action: %q", out)
	}
}

func TestBoardsSubmenu_newPost_endToEnd(t *testing.T) {
	f := setupBoards(t)
	h := newBoardsHandler(t, f)
	var buf strings.Builder
	p := newMenuParticipant(&buf)
	h.OnOpen(p)
	h.Handle(p, "1") // boards
	h.Handle(p, "1") // first board → threads
	h.Handle(p, "N") // new post
	h.Handle(p, "Howdy") // subject
	h.Handle(p, "first line")
	h.Handle(p, "second line")
	buf.Reset()
	h.Handle(p, ".") // commit

	threads, err := f.svc.ListThreads(context.Background(), "general", f.alice)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(threads) == 0 {
		t.Fatal("expected thread after post")
	}
	found := false
	for _, th := range threads {
		if th.Subject == "Howdy" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("did not find 'Howdy' post; got %+v", threads)
	}
}

func TestBoardsSubmenu_back_returnsToBoardsList(t *testing.T) {
	f := setupBoards(t)
	h := newBoardsHandler(t, f)
	var buf strings.Builder
	p := newMenuParticipant(&buf)
	h.OnOpen(p)
	h.Handle(p, "1") // boards (list of boards)
	h.Handle(p, "1") // first board → threads
	buf.Reset()
	h.Handle(p, "B") // back to boards
	out := buf.String()
	if !strings.Contains(out, "general") {
		t.Errorf("after Back, should show boards list: %q", out)
	}
}
