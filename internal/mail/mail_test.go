// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package mail

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/vaelen/wintermute/internal/auth"
	ftnnetworks "github.com/vaelen/wintermute/internal/ftn/networks"
	"github.com/vaelen/wintermute/internal/ftn/msgid"
	"github.com/vaelen/wintermute/internal/store"
)

type testEnv struct {
	db       *store.DB
	auth     *auth.Store
	mail     *Service
	alice    *auth.Account
	bob      *auth.Account
}

func setup(t *testing.T) *testEnv {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := store.Open(context.Background(), path, logger)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	if err := ftnnetworks.Bootstrap(ctx, db, nil, logger); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	authStore := auth.NewStore(db)
	alice, err := authStore.Create(ctx, "alice", "alicepass", auth.AccessPlayer)
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := authStore.Create(ctx, "bob", "bobpass", auth.AccessPlayer)
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}

	issuer := msgid.NewIssuer(db)
	svc := NewService(db, issuer, "Wintermute/test", "")

	return &testEnv{db: db, auth: authStore, mail: svc, alice: alice, bob: bob}
}

func TestSend_LocalDelivery(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	id, err := e.mail.Send(ctx, e.alice, "bob", "Hello Bob", "Body text.", "")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if id == 0 {
		t.Errorf("Send returned id 0")
	}

	m, err := e.mail.Read(ctx, id, e.bob.ID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if m.FromName != "alice" || m.ToName != "bob" {
		t.Errorf("names: from=%q to=%q", m.FromName, m.ToName)
	}
	if m.FromAddr != "255:255/255.0@local" || m.ToAddr != "255:255/255.0@local" {
		t.Errorf("addrs: from=%q to=%q", m.FromAddr, m.ToAddr)
	}
	if m.Subject != "Hello Bob" || m.Body != "Body text." {
		t.Errorf("content: subj=%q body=%q", m.Subject, m.Body)
	}
	if m.MSGID == "" {
		t.Errorf("MSGID empty")
	}
	if m.OriginAddr != "255:255/255.0@local" {
		t.Errorf("OriginAddr = %q", m.OriginAddr)
	}
	if m.Charset != "UTF-8 4" {
		t.Errorf("Charset = %q", m.Charset)
	}
	if m.PID.String != "Wintermute/test" {
		t.Errorf("PID = %v", m.PID)
	}
}

func TestSend_UnknownRecipient(t *testing.T) {
	e := setup(t)
	_, err := e.mail.Send(context.Background(), e.alice, "nobody", "x", "y", "")
	if !errors.Is(err, ErrRecipientNotFound) {
		t.Errorf("err = %v, want ErrRecipientNotFound", err)
	}
}

func TestSend_RejectsEmptyBody(t *testing.T) {
	e := setup(t)
	_, err := e.mail.Send(context.Background(), e.alice, "bob", "subj", "", "")
	if !errors.Is(err, ErrEmptyBody) {
		t.Errorf("err = %v, want ErrEmptyBody", err)
	}
}

func TestSend_RejectsEmptySubject(t *testing.T) {
	e := setup(t)
	_, err := e.mail.Send(context.Background(), e.alice, "bob", "", "body", "")
	if !errors.Is(err, ErrEmptySubject) {
		t.Errorf("err = %v, want ErrEmptySubject", err)
	}
}

func TestSend_ReplyLinks(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	parentID, err := e.mail.Send(ctx, e.alice, "bob", "Parent", "p", "")
	if err != nil {
		t.Fatalf("Send parent: %v", err)
	}
	parent, err := e.mail.Read(ctx, parentID, e.bob.ID)
	if err != nil {
		t.Fatalf("Read parent: %v", err)
	}

	childID, err := e.mail.Send(ctx, e.bob, "alice", "Re: Parent", "ok", parent.MSGID)
	if err != nil {
		t.Fatalf("Send reply: %v", err)
	}
	child, err := e.mail.Read(ctx, childID, e.alice.ID)
	if err != nil {
		t.Fatalf("Read reply: %v", err)
	}
	if !child.ReplyToMSGID.Valid || child.ReplyToMSGID.String != parent.MSGID {
		t.Errorf("ReplyToMSGID = %v, want %q", child.ReplyToMSGID, parent.MSGID)
	}
}

func TestSend_ReplyRejectsUnknownParent(t *testing.T) {
	e := setup(t)
	_, err := e.mail.Send(context.Background(), e.alice, "bob", "subj", "body", "missing@local 00000099")
	if !errors.Is(err, ErrParentNotFound) {
		t.Errorf("err = %v, want ErrParentNotFound", err)
	}
}

func TestInbox_ReturnsNewestFirst(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	_, _ = e.mail.Send(ctx, e.alice, "bob", "First", "1", "")
	_, _ = e.mail.Send(ctx, e.alice, "bob", "Second", "2", "")
	_, _ = e.mail.Send(ctx, e.alice, "bob", "Third", "3", "")

	box, err := e.mail.Inbox(ctx, e.bob.ID)
	if err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	if len(box) != 3 {
		t.Fatalf("got %d, want 3", len(box))
	}
	if box[0].Subject != "Third" || box[2].Subject != "First" {
		t.Errorf("order wrong: %v", []string{box[0].Subject, box[1].Subject, box[2].Subject})
	}
}

func TestInbox_OnlyShowsMineNotOthers(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	_, _ = e.mail.Send(ctx, e.alice, "bob", "ForBob", "x", "")

	box, _ := e.mail.Inbox(ctx, e.alice.ID)
	if len(box) != 0 {
		t.Errorf("alice inbox should be empty, got %d", len(box))
	}
}

func TestRead_MarksRead(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	id, _ := e.mail.Send(ctx, e.alice, "bob", "subj", "body", "")

	n, _ := e.mail.UnreadCount(ctx, e.bob.ID)
	if n != 1 {
		t.Errorf("UnreadCount before = %d, want 1", n)
	}

	_, _ = e.mail.Read(ctx, id, e.bob.ID)

	n, _ = e.mail.UnreadCount(ctx, e.bob.ID)
	if n != 0 {
		t.Errorf("UnreadCount after = %d, want 0", n)
	}
}

func TestRead_RejectsNotMine(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	id, _ := e.mail.Send(ctx, e.alice, "bob", "subj", "body", "")
	if _, err := e.mail.Read(ctx, id, e.alice.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestDelete_RemovesFromInbox(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	id, _ := e.mail.Send(ctx, e.alice, "bob", "subj", "body", "")
	if err := e.mail.Delete(ctx, id, e.bob.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	box, _ := e.mail.Inbox(ctx, e.bob.ID)
	if len(box) != 0 {
		t.Errorf("after Delete, inbox len = %d", len(box))
	}
}

func TestDelete_RejectsNotMine(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	id, _ := e.mail.Send(ctx, e.alice, "bob", "subj", "body", "")
	if err := e.mail.Delete(ctx, id, e.alice.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestUnreadCount_Zero(t *testing.T) {
	e := setup(t)
	n, err := e.mail.UnreadCount(context.Background(), e.alice.ID)
	if err != nil {
		t.Fatalf("UnreadCount: %v", err)
	}
	if n != 0 {
		t.Errorf("got %d, want 0", n)
	}
}

func TestSendFromSystem_DefaultHandle(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	id, err := e.mail.SendFromSystem(ctx, "bob", "Welcome", "Body.")
	if err != nil {
		t.Fatalf("SendFromSystem: %v", err)
	}
	m, err := e.mail.Read(ctx, id, e.bob.ID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if m.FromID.Valid {
		t.Errorf("FromID = %v, want NULL", m.FromID)
	}
	if m.FromName != DefaultSystemName {
		t.Errorf("FromName = %q, want %q", m.FromName, DefaultSystemName)
	}
	if m.ToName != "bob" {
		t.Errorf("ToName = %q", m.ToName)
	}
	if m.OriginAddr != "255:255/255.0@local" {
		t.Errorf("OriginAddr = %q", m.OriginAddr)
	}
	if m.MSGID == "" {
		t.Errorf("MSGID empty")
	}
}

func TestSendFromSystem_CustomHandle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := store.Open(context.Background(), path, logger)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := ftnnetworks.Bootstrap(ctx, db, nil, logger); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	authStore := auth.NewStore(db)
	bob, err := authStore.Create(ctx, "bob", "bobpass", auth.AccessPlayer)
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	svc := NewService(db, msgid.NewIssuer(db), "Wintermute/test", "wintermute")

	id, err := svc.SendFromSystem(ctx, "bob", "Hello", "Body.")
	if err != nil {
		t.Fatalf("SendFromSystem: %v", err)
	}
	m, err := svc.Read(ctx, id, bob.ID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if m.FromName != "wintermute" {
		t.Errorf("FromName = %q, want %q", m.FromName, "wintermute")
	}
}

func TestSendFromSystem_UnknownRecipient(t *testing.T) {
	e := setup(t)
	_, err := e.mail.SendFromSystem(context.Background(), "nobody", "x", "y")
	if !errors.Is(err, ErrRecipientNotFound) {
		t.Errorf("err = %v, want ErrRecipientNotFound", err)
	}
}

func TestSendFromSystem_RejectsEmpty(t *testing.T) {
	e := setup(t)
	if _, err := e.mail.SendFromSystem(context.Background(), "bob", "", "body"); !errors.Is(err, ErrEmptySubject) {
		t.Errorf("empty subject err = %v, want ErrEmptySubject", err)
	}
	if _, err := e.mail.SendFromSystem(context.Background(), "bob", "subj", ""); !errors.Is(err, ErrEmptyBody) {
		t.Errorf("empty body err = %v, want ErrEmptyBody", err)
	}
}

func TestSendFromSystem_IssuesUniqueMSGIDs(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	id1, err := e.mail.SendFromSystem(ctx, "bob", "Welcome 1", "Body 1")
	if err != nil {
		t.Fatalf("SendFromSystem 1: %v", err)
	}
	id2, err := e.mail.SendFromSystem(ctx, "bob", "Welcome 2", "Body 2")
	if err != nil {
		t.Fatalf("SendFromSystem 2: %v", err)
	}
	m1, _ := e.mail.Read(ctx, id1, e.bob.ID)
	m2, _ := e.mail.Read(ctx, id2, e.bob.ID)
	if m1.MSGID == "" || m2.MSGID == "" || m1.MSGID == m2.MSGID {
		t.Errorf("MSGIDs not distinct: %q vs %q", m1.MSGID, m2.MSGID)
	}
}

func TestSystemName(t *testing.T) {
	e := setup(t)
	if got := e.mail.SystemName(); got != DefaultSystemName {
		t.Errorf("SystemName default = %q, want %q", got, DefaultSystemName)
	}
}
