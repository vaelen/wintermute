// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package menu

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/files"
	"github.com/vaelen/wintermute/internal/store"
	"github.com/vaelen/wintermute/internal/world"
	"github.com/vaelen/wintermute/internal/world/engage"
)

type filesFixture struct {
	db    *store.DB
	svc   *files.Service
	alice *auth.Account
}

func setupFiles(t *testing.T) *filesFixture {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "menu_files.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := store.Open(context.Background(), path, logger)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	as := auth.NewStore(db)
	alice, err := as.Create(context.Background(), "alice", "alicepass", auth.AccessPlayer)
	if err != nil {
		t.Fatalf("alice: %v", err)
	}
	svc, err := files.NewService(db, filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatalf("files.NewService: %v", err)
	}
	return &filesFixture{db: db, svc: svc, alice: alice}
}

func newFilesHandler(t *testing.T, f *filesFixture, area string) *Handler {
	t.Helper()
	h := NewHandler(&engage.Host{
		ObjectID: 100, Kind: engage.KindMenuTerminal,
		Menu: []engage.MenuEntry{{Feature: engage.FeatureFiles, Area: area}},
	}, nil)
	h.SetDeps(&engage.TerminalDeps{
		RootCtx:     context.Background(),
		Files:       f.svc,
		UploadURL:   func(tok string) string { return "https://example.test/upload/" + tok },
		DownloadURL: func(tok string) string { return "https://example.test/download/" + tok },
		AccountFor: func(_ world.ObjectID) (*auth.Account, error) {
			return f.alice, nil
		},
	})
	return h
}

func TestFilesSubmenu_emptyArea_showsListAndActions(t *testing.T) {
	f := setupFiles(t)
	h := newFilesHandler(t, f, "dropbox")
	var buf strings.Builder
	p := newMenuParticipant(&buf)
	h.OnOpen(p)
	buf.Reset()
	h.Handle(p, "1")
	out := buf.String()
	if !strings.Contains(out, "Upload") {
		t.Errorf("files view missing Upload action: %q", out)
	}
	if !strings.Contains(out, "Back") {
		t.Errorf("files view missing Back: %q", out)
	}
	if !strings.Contains(strings.ToLower(out), "dropbox") {
		t.Errorf("files view missing area name: %q", out)
	}
}

func TestFilesSubmenu_listsExistingFiles(t *testing.T) {
	f := setupFiles(t)
	ctx := context.Background()
	hash, size, mime, err := f.svc.PutBlob(ctx, bytes.NewReader([]byte("body")))
	if err != nil {
		t.Fatalf("PutBlob: %v", err)
	}
	if _, err := f.svc.NewFile(ctx, "report.txt", f.alice.ID, hash, size, mime, "weekly", "dropbox"); err != nil {
		t.Fatalf("NewFile: %v", err)
	}
	h := newFilesHandler(t, f, "dropbox")
	var buf strings.Builder
	p := newMenuParticipant(&buf)
	h.OnOpen(p)
	buf.Reset()
	h.Handle(p, "1")
	out := buf.String()
	if !strings.Contains(out, "report.txt") {
		t.Errorf("files view missing file slug: %q", out)
	}
}

func TestFilesSubmenu_upload_promptsForSlugAndIssuesURL(t *testing.T) {
	f := setupFiles(t)
	h := newFilesHandler(t, f, "dropbox")
	var buf strings.Builder
	p := newMenuParticipant(&buf)
	h.OnOpen(p)
	h.Handle(p, "1")
	buf.Reset()
	h.Handle(p, "U") // upload
	out := buf.String()
	if !strings.Contains(strings.ToLower(out), "slug") {
		t.Errorf("upload start should prompt for slug: %q", out)
	}
	buf.Reset()
	h.Handle(p, "new-thing")
	out = buf.String()
	if !strings.Contains(out, "https://example.test/upload/") {
		t.Errorf("upload finish should print URL: %q", out)
	}
	if !strings.Contains(out, "curl") {
		t.Errorf("upload should mention curl: %q", out)
	}
}

func TestFilesSubmenu_download_picksFile_issuesURL(t *testing.T) {
	f := setupFiles(t)
	ctx := context.Background()
	hash, size, mime, err := f.svc.PutBlob(ctx, bytes.NewReader([]byte("body")))
	if err != nil {
		t.Fatalf("PutBlob: %v", err)
	}
	if _, err := f.svc.NewFile(ctx, "report.txt", f.alice.ID, hash, size, mime, "weekly", "dropbox"); err != nil {
		t.Fatalf("NewFile: %v", err)
	}
	h := newFilesHandler(t, f, "dropbox")
	var buf strings.Builder
	p := newMenuParticipant(&buf)
	h.OnOpen(p)
	h.Handle(p, "1")
	buf.Reset()
	h.Handle(p, "1") // first file → download URL
	out := buf.String()
	if !strings.Contains(out, "https://example.test/download/") {
		t.Errorf("download URL missing: %q", out)
	}
}

func TestFilesSubmenu_back_returnsToMainMenu(t *testing.T) {
	f := setupFiles(t)
	h := newFilesHandler(t, f, "dropbox")
	var buf strings.Builder
	p := newMenuParticipant(&buf)
	h.OnOpen(p)
	h.Handle(p, "1")
	buf.Reset()
	h.Handle(p, "B")
	out := buf.String()
	if !strings.Contains(out, "Quit") {
		t.Errorf("after Back expected main menu (Quit row): %q", out)
	}
}
