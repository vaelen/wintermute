// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	netHTTP "net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/config"
	"github.com/vaelen/wintermute/internal/files"
	ftnnetworks "github.com/vaelen/wintermute/internal/ftn/networks"
	"github.com/vaelen/wintermute/internal/ftn/msgid"
	wintermutehttp "github.com/vaelen/wintermute/internal/http"
	"github.com/vaelen/wintermute/internal/mail"
	"github.com/vaelen/wintermute/internal/store"
)

// TestUploadDeliversSystemMail covers acceptance criterion 2: an upload
// via the HTTPS endpoint produces a files row stamped with area =
// "dropbox" and a system mail to the owner titled "Upload complete:
// <slug>" containing the size and mime.
func TestUploadDeliversSystemMail(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	db, err := store.Open(ctx, filepath.Join(dir, "test.db"), logger)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := ftnnetworks.Bootstrap(ctx, db, []config.FTNNetwork{}, logger); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	authStore := auth.NewStore(db)
	alice, _ := authStore.Create(ctx, "alice", "alicepass", auth.AccessPlayer)

	issuer := msgid.NewIssuer(db)
	mailSvc := mail.NewService(db, issuer, "Wintermute/test", "")

	filesSvc, err := files.NewService(db, filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatalf("files.NewService: %v", err)
	}

	handler := wintermutehttp.NewHandler(filesSvc, wintermutehttp.HandlerOptions{
		MaxUploadBytes: 1 << 20,
		OnUpload:       uploadMailNotifier(ctx, logger, authStore, mailSvc),
		Logger:         logger,
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	tok, err := filesSvc.IssueUpload(ctx, alice.ID, "notes", time.Minute)
	if err != nil {
		t.Fatalf("IssueUpload: %v", err)
	}

	payload := []byte("hello mailbox")
	resp, err := netHTTP.Post(srv.URL+"/upload/"+tok.Value,
		"application/octet-stream", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	// File row stamped with default area.
	f, err := filesSvc.GetFile(ctx, "notes")
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if f.Area != "dropbox" {
		t.Errorf("Area = %q, want dropbox", f.Area)
	}

	// Owner gets a system mail with the expected subject + body fields.
	inbox, err := mailSvc.Inbox(ctx, alice.ID)
	if err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	if len(inbox) != 1 {
		t.Fatalf("len(inbox) = %d, want 1", len(inbox))
	}
	m := inbox[0]
	if m.FromID.Valid {
		t.Errorf("FromID = %v, want NULL (system mail)", m.FromID)
	}
	if m.FromName != mail.DefaultSystemName {
		t.Errorf("FromName = %q, want %q", m.FromName, mail.DefaultSystemName)
	}
	if m.Subject != "Upload complete: notes" {
		t.Errorf("Subject = %q", m.Subject)
	}
	if !strings.Contains(m.Body, "notes") {
		t.Errorf("body missing slug: %q", m.Body)
	}
	if !strings.Contains(m.Body, "text/plain") {
		t.Errorf("body missing mime: %q", m.Body)
	}
	if !strings.Contains(m.Body, "13") {
		t.Errorf("body missing byte size: %q", m.Body)
	}
	if !strings.Contains(m.Body, "download notes") {
		t.Errorf("body missing in-world download hint: %q", m.Body)
	}

	// Unread count reflects the queued mail — covers acceptance criterion
	// 3 (next-login visibility): a count > 0 is what the post-MOTD hook
	// prints for an account that was offline at the time of upload.
	n, err := mailSvc.UnreadCount(ctx, alice.ID)
	if err != nil {
		t.Fatalf("UnreadCount: %v", err)
	}
	if n != 1 {
		t.Errorf("UnreadCount = %d, want 1", n)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1024 * 1024, "1.0 MB"},
	}
	for _, c := range cases {
		got := humanBytes(c.in)
		if got != c.want {
			t.Errorf("humanBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}
