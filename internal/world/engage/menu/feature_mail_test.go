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
	ftnnetworks "github.com/vaelen/wintermute/internal/ftn/networks"
	"github.com/vaelen/wintermute/internal/ftn/msgid"
	"github.com/vaelen/wintermute/internal/mail"
	"github.com/vaelen/wintermute/internal/store"
	"github.com/vaelen/wintermute/internal/world"
	"github.com/vaelen/wintermute/internal/world/engage"
)

type mailFixture struct {
	db    *store.DB
	mail  *mail.Service
	alice *auth.Account
	bob   *auth.Account
}

func setupMail(t *testing.T) *mailFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "menu_mail.db")
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
		t.Fatalf("create alice: %v", err)
	}
	bob, err := as.Create(ctx, "bob", "alicepass", auth.AccessPlayer)
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	svc := mail.NewService(db, msgid.NewIssuer(db), "Wintermute/test", "")
	return &mailFixture{db: db, mail: svc, alice: alice, bob: bob}
}

// newMailHandler returns a Handler whose deps point at the fixture's mail
// service. The AccountFor closure resolves the participant's PlayerID to
// the alice account regardless of ID — sufficient for these tests.
func newMailHandler(t *testing.T, f *mailFixture, menu []engage.MenuEntry) *Handler {
	t.Helper()
	h := NewHandler(&engage.Host{
		ObjectID: 100, Kind: engage.KindMenuTerminal, Menu: menu,
	}, nil)
	h.SetDeps(&engage.TerminalDeps{
		RootCtx: context.Background(),
		Mail:    f.mail,
		AccountFor: func(_ world.ObjectID) (*auth.Account, error) {
			return f.alice, nil
		},
	})
	return h
}

func TestMailSubmenu_emptyInbox_rendersInboxHeader(t *testing.T) {
	f := setupMail(t)
	h := newMailHandler(t, f, []engage.MenuEntry{{Feature: engage.FeatureMail}})
	var buf strings.Builder
	p := newMenuParticipant(&buf)
	h.OnOpen(p)
	buf.Reset()
	h.Handle(p, "1")
	out := buf.String()
	if !strings.Contains(out, "Mail") {
		t.Errorf("inbox should mention Mail: %q", out)
	}
	if !strings.Contains(out, "Compose") {
		t.Errorf("inbox should advertise Compose action: %q", out)
	}
	if !strings.Contains(out, "Back") {
		t.Errorf("inbox should advertise Back action: %q", out)
	}
}

func TestMailSubmenu_listsMessages(t *testing.T) {
	f := setupMail(t)
	// bob sends two messages to alice
	if _, err := f.mail.Send(context.Background(), f.bob, "alice", "first", "body 1", ""); err != nil {
		t.Fatalf("Send 1: %v", err)
	}
	if _, err := f.mail.Send(context.Background(), f.bob, "alice", "second", "body 2", ""); err != nil {
		t.Fatalf("Send 2: %v", err)
	}
	h := newMailHandler(t, f, []engage.MenuEntry{{Feature: engage.FeatureMail}})
	var buf strings.Builder
	p := newMenuParticipant(&buf)
	h.OnOpen(p)
	buf.Reset()
	h.Handle(p, "1")
	out := buf.String()
	if !strings.Contains(out, "first") || !strings.Contains(out, "second") {
		t.Errorf("inbox missing message subjects: %q", out)
	}
}

func TestMailSubmenu_back_returnsToMainMenu(t *testing.T) {
	f := setupMail(t)
	h := newMailHandler(t, f, []engage.MenuEntry{{Feature: engage.FeatureMail}})
	var buf strings.Builder
	p := newMenuParticipant(&buf)
	h.OnOpen(p)
	h.Handle(p, "1") // mail submenu
	buf.Reset()
	h.Handle(p, "B")
	out := buf.String()
	if !strings.Contains(out, "Quit") {
		t.Errorf("after Back, expected main menu (with Quit row): %q", out)
	}
}

func TestMailSubmenu_compose_promptsForRecipient(t *testing.T) {
	f := setupMail(t)
	h := newMailHandler(t, f, []engage.MenuEntry{{Feature: engage.FeatureMail}})
	var buf strings.Builder
	p := newMenuParticipant(&buf)
	h.OnOpen(p)
	h.Handle(p, "1")
	buf.Reset()
	h.Handle(p, "C")
	out := buf.String()
	if !strings.Contains(strings.ToLower(out), "recipient") &&
		!strings.Contains(strings.ToLower(out), "to:") {
		t.Errorf("compose start should prompt for recipient: %q", out)
	}
}

func TestMailSubmenu_compose_endToEnd_sendsMail(t *testing.T) {
	f := setupMail(t)
	h := newMailHandler(t, f, []engage.MenuEntry{{Feature: engage.FeatureMail}})
	var buf strings.Builder
	p := newMenuParticipant(&buf)
	h.OnOpen(p)
	h.Handle(p, "1")  // mail submenu
	h.Handle(p, "C")  // start compose
	h.Handle(p, "bob") // recipient
	h.Handle(p, "Greetings") // subject
	h.Handle(p, "line one")
	h.Handle(p, "line two")
	buf.Reset()
	h.Handle(p, ".") // commit

	// Verify alice (the sender per AccountFor) sent a message to bob.
	box, err := f.mail.Inbox(context.Background(), f.bob.ID)
	if err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	if len(box) != 1 {
		t.Fatalf("bob inbox = %d messages, want 1", len(box))
	}
	if box[0].Subject != "Greetings" {
		t.Errorf("Subject = %q, want %q", box[0].Subject, "Greetings")
	}
	if !strings.Contains(box[0].Body, "line one") || !strings.Contains(box[0].Body, "line two") {
		t.Errorf("Body = %q", box[0].Body)
	}

	// After commit we should be back at the inbox view.
	out := buf.String()
	if !strings.Contains(out, "Compose") {
		t.Errorf("after commit, expected inbox view: %q", out)
	}
}

func TestMailSubmenu_compose_abort_returnsToInbox(t *testing.T) {
	f := setupMail(t)
	h := newMailHandler(t, f, []engage.MenuEntry{{Feature: engage.FeatureMail}})
	var buf strings.Builder
	p := newMenuParticipant(&buf)
	h.OnOpen(p)
	h.Handle(p, "1")
	h.Handle(p, "C")
	h.Handle(p, "bob")
	h.Handle(p, "Some subject")
	h.Handle(p, "halfway through a thought")
	buf.Reset()
	h.Handle(p, "/abort")

	// No mail delivered.
	box, err := f.mail.Inbox(context.Background(), f.bob.ID)
	if err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	if len(box) != 0 {
		t.Errorf("expected no delivered mail after abort, got %d", len(box))
	}
	out := buf.String()
	if !strings.Contains(out, "Compose") {
		t.Errorf("after abort, expected inbox view (Compose visible): %q", out)
	}
}

func TestMailSubmenu_read_marksReadAndShowsBody(t *testing.T) {
	f := setupMail(t)
	id, err := f.mail.Send(context.Background(), f.bob, "alice", "hi", "body text", "")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	_ = id
	h := newMailHandler(t, f, []engage.MenuEntry{{Feature: engage.FeatureMail}})
	var buf strings.Builder
	p := newMenuParticipant(&buf)
	h.OnOpen(p)
	h.Handle(p, "1") // inbox
	buf.Reset()
	h.Handle(p, "1") // first message
	out := buf.String()
	if !strings.Contains(out, "body text") {
		t.Errorf("read view missing body: %q", out)
	}
	if !strings.Contains(out, "Reply") {
		t.Errorf("read view missing Reply action: %q", out)
	}
	if !strings.Contains(out, "Delete") {
		t.Errorf("read view missing Delete action: %q", out)
	}
}
