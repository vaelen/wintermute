// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package boards

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/config"
	ftnnetworks "github.com/vaelen/wintermute/internal/ftn/networks"
	"github.com/vaelen/wintermute/internal/ftn/msgid"
	"github.com/vaelen/wintermute/internal/store"
)

type env struct {
	db    *store.DB
	auth  *auth.Store
	svc   *Service
	alice *auth.Account
	bob   *auth.Account
	admin *auth.Account
}

func setup(t *testing.T) *env {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := store.Open(context.Background(), path, logger)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	nets := []config.FTNNetwork{
		{Slug: "fsxnet", Name: "fsxNet", Domain: "fsxnet", Addr: "21:1/100.0"},
	}
	if err := ftnnetworks.Bootstrap(ctx, db, nets, logger); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	authStore := auth.NewStore(db)
	alice, _ := authStore.Create(ctx, "alice", "password", auth.AccessPlayer)
	bob, _ := authStore.Create(ctx, "bob", "password", auth.AccessPlayer)
	admin, _ := authStore.Create(ctx, "admin", "password", auth.AccessAdmin)

	issuer := msgid.NewIssuer(db)
	svc := NewService(db, issuer, ServiceOptions{
		PID:         "Wintermute/test",
		ServerName:  "Wintermute",
		Tearline:    "--- Wintermute/test",
	})
	return &env{db: db, auth: authStore, svc: svc, alice: alice, bob: bob, admin: admin}
}

func TestCreateBoard_Local(t *testing.T) {
	e := setup(t)
	id, err := e.svc.CreateBoard(context.Background(), CreateBoardSpec{
		Slug: "general", Name: "General Chat", NetworkSlug: "local",
	})
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	if id == 0 {
		t.Errorf("id 0")
	}
	b, err := e.svc.GetBoard(context.Background(), "general")
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	if b.Slug != "general" || b.Name != "General Chat" {
		t.Errorf("got %+v", b)
	}
	if b.AreaTag.Valid {
		t.Errorf("local board should have NULL area_tag")
	}
}

func TestCreateBoard_DuplicateSlugRejected(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	_, _ = e.svc.CreateBoard(ctx, CreateBoardSpec{Slug: "x", Name: "X", NetworkSlug: "local"})
	_, err := e.svc.CreateBoard(ctx, CreateBoardSpec{Slug: "x", Name: "X2", NetworkSlug: "local"})
	if err == nil {
		t.Errorf("expected error")
	}
}

func TestCreateBoard_UnknownNetworkRejected(t *testing.T) {
	e := setup(t)
	_, err := e.svc.CreateBoard(context.Background(), CreateBoardSpec{Slug: "y", Name: "Y", NetworkSlug: "nosuch"})
	if !errors.Is(err, ErrUnknownNetwork) {
		t.Errorf("err = %v, want ErrUnknownNetwork", err)
	}
}

func TestCreateBoard_SameAreaTagDifferentNetworks(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := e.svc.CreateBoard(ctx, CreateBoardSpec{
		Slug: "fido-general", Name: "G", NetworkSlug: "local", AreaTag: "GENERAL",
	}); err != nil {
		t.Fatalf("local GENERAL: %v", err)
	}
	if _, err := e.svc.CreateBoard(ctx, CreateBoardSpec{
		Slug: "fsx-general", Name: "G", NetworkSlug: "fsxnet", AreaTag: "GENERAL",
	}); err != nil {
		t.Fatalf("fsxnet GENERAL: %v", err)
	}
}

func TestListBoards_ACLFiltersByLevel(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	_, _ = e.svc.CreateBoard(ctx, CreateBoardSpec{
		Slug: "public", Name: "Public", NetworkSlug: "local",
		ReadMinLevel: LevelPlayer, PostMinLevel: LevelPlayer,
	})
	_, _ = e.svc.CreateBoard(ctx, CreateBoardSpec{
		Slug: "staff", Name: "Staff", NetworkSlug: "local",
		ReadMinLevel: LevelAdmin, PostMinLevel: LevelAdmin,
	})
	visible, err := e.svc.ListBoards(ctx, e.alice)
	if err != nil {
		t.Fatalf("ListBoards alice: %v", err)
	}
	slugs := map[string]bool{}
	for _, b := range visible {
		slugs[b.Slug] = true
	}
	if !slugs["public"] || slugs["staff"] {
		t.Errorf("alice should see public, not staff; got %v", slugs)
	}
	visible, _ = e.svc.ListBoards(ctx, e.admin)
	slugs = map[string]bool{}
	for _, b := range visible {
		slugs[b.Slug] = true
	}
	if !slugs["public"] || !slugs["staff"] {
		t.Errorf("admin should see both; got %v", slugs)
	}
}

func TestPost_CreatesRootWithThreadRoot(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	_, _ = e.svc.CreateBoard(ctx, CreateBoardSpec{Slug: "g", Name: "G", NetworkSlug: "local"})
	id, err := e.svc.Post(ctx, "g", e.alice, "Hello world", "Body text.")
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	p, err := e.svc.ReadPost(ctx, "g", id, e.alice)
	if err != nil {
		t.Fatalf("ReadPost: %v", err)
	}
	if p.AuthorName != "alice" || p.Subject != "Hello world" {
		t.Errorf("post = %+v", p)
	}
	if p.ThreadRootMSGID != p.MSGID {
		t.Errorf("root should have thread_root = own msgid; got %q vs %q", p.ThreadRootMSGID, p.MSGID)
	}
	if p.ReplyToMSGID.Valid {
		t.Errorf("root should have NULL reply_to_msgid")
	}
	if p.Tearline.String != "--- Wintermute/test" {
		t.Errorf("tearline = %q", p.Tearline.String)
	}
	if p.OriginLine.String == "" || !contains(p.OriginLine.String, "@local") && !contains(p.OriginLine.String, "255:255/255.0") {
		t.Errorf("origin_line = %q (expected to include network addr)", p.OriginLine.String)
	}
	if p.SeenBy.String == "" {
		t.Errorf("seen_by empty")
	}
	if p.Path.String == "" {
		t.Errorf("path empty")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestPost_RejectsInsufficientLevel(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	_, _ = e.svc.CreateBoard(ctx, CreateBoardSpec{
		Slug: "g", Name: "G", NetworkSlug: "local",
		ReadMinLevel: LevelPlayer, PostMinLevel: LevelAdmin,
	})
	_, err := e.svc.Post(ctx, "g", e.alice, "subj", "body")
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("err = %v, want ErrForbidden", err)
	}
}

func TestReply_LinksAndInheritsThreadRoot(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	_, _ = e.svc.CreateBoard(ctx, CreateBoardSpec{Slug: "g", Name: "G", NetworkSlug: "local"})
	rootID, _ := e.svc.Post(ctx, "g", e.alice, "Q", "?")
	root, _ := e.svc.ReadPost(ctx, "g", rootID, e.alice)

	replyID, err := e.svc.Reply(ctx, "g", e.bob, rootID, "Re: Q", "A")
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	reply, _ := e.svc.ReadPost(ctx, "g", replyID, e.bob)
	if reply.ReplyToMSGID.String != root.MSGID {
		t.Errorf("reply_to_msgid = %q, want %q", reply.ReplyToMSGID.String, root.MSGID)
	}
	if reply.ThreadRootMSGID != root.MSGID {
		t.Errorf("thread_root_msgid = %q, want %q", reply.ThreadRootMSGID, root.MSGID)
	}

	// Reply to the reply — should still root to the original.
	deepID, _ := e.svc.Reply(ctx, "g", e.alice, replyID, "Re: Re: Q", "more")
	deep, _ := e.svc.ReadPost(ctx, "g", deepID, e.alice)
	if deep.ThreadRootMSGID != root.MSGID {
		t.Errorf("deep thread_root_msgid = %q, want %q", deep.ThreadRootMSGID, root.MSGID)
	}
}

func TestReply_OrphanBecomesRoot(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	_, _ = e.svc.CreateBoard(ctx, CreateBoardSpec{Slug: "g", Name: "G", NetworkSlug: "local"})
	id, err := e.svc.ReplyToMSGID(ctx, "g", e.alice, "fake@local 99999999", "subj", "body")
	if err != nil {
		t.Fatalf("ReplyToMSGID: %v", err)
	}
	p, _ := e.svc.ReadPost(ctx, "g", id, e.alice)
	// Orphan: reply_to set, but thread_root_msgid = own msgid (treated as root).
	if !p.ReplyToMSGID.Valid || p.ReplyToMSGID.String != "fake@local 99999999" {
		t.Errorf("reply_to_msgid wrong: %v", p.ReplyToMSGID)
	}
	if p.ThreadRootMSGID != p.MSGID {
		t.Errorf("orphan should be its own thread root; got %q vs %q", p.ThreadRootMSGID, p.MSGID)
	}
}

func TestListThreads_NewestFirst(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	_, _ = e.svc.CreateBoard(ctx, CreateBoardSpec{Slug: "g", Name: "G", NetworkSlug: "local"})
	t1, _ := e.svc.Post(ctx, "g", e.alice, "First", "1")
	_, _ = e.svc.Post(ctx, "g", e.alice, "Second", "2")
	t3, _ := e.svc.Post(ctx, "g", e.alice, "Third", "3")
	_, _ = e.svc.Reply(ctx, "g", e.bob, t1, "Re: First", "reply") // bumps t1 activity

	threads, err := e.svc.ListThreads(ctx, "g", e.alice)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(threads) != 3 {
		t.Fatalf("got %d threads, want 3", len(threads))
	}
	// t1 had the latest reply so it should be first.
	if threads[0].RootPostID != t1 {
		t.Errorf("first thread root = %d, want %d (t1 with latest reply)", threads[0].RootPostID, t1)
	}
	// Third was last posted before reply; should be after t1.
	found := false
	for _, th := range threads {
		if th.RootPostID == t3 {
			found = true
		}
	}
	if !found {
		t.Errorf("t3 not in threads")
	}
	// t1's thread has 1 reply.
	for _, th := range threads {
		if th.RootPostID == t1 && th.ReplyCount != 1 {
			t.Errorf("t1 ReplyCount = %d, want 1", th.ReplyCount)
		}
	}
}

func TestListThread_OldestFirst(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	_, _ = e.svc.CreateBoard(ctx, CreateBoardSpec{Slug: "g", Name: "G", NetworkSlug: "local"})
	rootID, _ := e.svc.Post(ctx, "g", e.alice, "Q", "?")
	r1, _ := e.svc.Reply(ctx, "g", e.bob, rootID, "Re: Q", "A1")
	r2, _ := e.svc.Reply(ctx, "g", e.alice, r1, "Re: Q", "A2")

	posts, err := e.svc.ListThread(ctx, "g", rootID, e.alice)
	if err != nil {
		t.Fatalf("ListThread: %v", err)
	}
	if len(posts) != 3 {
		t.Fatalf("got %d, want 3", len(posts))
	}
	if posts[0].ID != rootID || posts[1].ID != r1 || posts[2].ID != r2 {
		t.Errorf("order wrong: %d %d %d", posts[0].ID, posts[1].ID, posts[2].ID)
	}
}

func TestReadPost_MarksRead(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	_, _ = e.svc.CreateBoard(ctx, CreateBoardSpec{Slug: "g", Name: "G", NetworkSlug: "local"})
	id, _ := e.svc.Post(ctx, "g", e.alice, "X", "y")
	threads, _ := e.svc.ListThreads(ctx, "g", e.bob)
	if !threads[0].Unread {
		t.Errorf("bob should see thread as unread")
	}
	_, _ = e.svc.ReadPost(ctx, "g", id, e.bob)
	threads, _ = e.svc.ListThreads(ctx, "g", e.bob)
	if threads[0].Unread {
		t.Errorf("after ReadPost, thread should be read")
	}
}

func TestCatchUp_MarksAllRead(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	_, _ = e.svc.CreateBoard(ctx, CreateBoardSpec{Slug: "g", Name: "G", NetworkSlug: "local"})
	_, _ = e.svc.Post(ctx, "g", e.alice, "X", "y")
	_, _ = e.svc.Post(ctx, "g", e.alice, "Y", "z")
	if err := e.svc.CatchUp(ctx, "g", e.bob); err != nil {
		t.Fatalf("CatchUp: %v", err)
	}
	threads, _ := e.svc.ListThreads(ctx, "g", e.bob)
	for _, th := range threads {
		if th.Unread {
			t.Errorf("thread %q still unread after CatchUp", th.Subject)
		}
	}
}

func TestPost_AreaTagDenormalized(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	_, _ = e.svc.CreateBoard(ctx, CreateBoardSpec{
		Slug: "g", Name: "G", NetworkSlug: "fsxnet", AreaTag: "FSX_GEN",
	})
	id, _ := e.svc.Post(ctx, "g", e.alice, "subj", "body")
	p, _ := e.svc.ReadPost(ctx, "g", id, e.alice)
	if p.AreaTag.String != "FSX_GEN" {
		t.Errorf("AreaTag = %q, want FSX_GEN", p.AreaTag.String)
	}
	// MSGID origaddr should be fsxnet's, not local's.
	if p.OriginAddr != "21:1/100.0@fsxnet" {
		t.Errorf("OriginAddr = %q, want 21:1/100.0@fsxnet", p.OriginAddr)
	}
}

func TestDeleteBoard_RejectsIfHasPosts(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	_, _ = e.svc.CreateBoard(ctx, CreateBoardSpec{Slug: "g", Name: "G", NetworkSlug: "local"})
	_, _ = e.svc.Post(ctx, "g", e.alice, "x", "y")
	if err := e.svc.DeleteBoard(ctx, "g"); err == nil {
		t.Errorf("DeleteBoard should refuse when posts exist")
	}
}

func TestDeleteBoard_OK(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	_, _ = e.svc.CreateBoard(ctx, CreateBoardSpec{Slug: "g", Name: "G", NetworkSlug: "local"})
	if err := e.svc.DeleteBoard(ctx, "g"); err != nil {
		t.Errorf("DeleteBoard: %v", err)
	}
}
