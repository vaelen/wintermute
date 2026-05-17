// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package files

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/store"
)

type env struct {
	db    *store.DB
	svc   *Service
	alice *auth.Account
	bob   *auth.Account
	root  string
}

func setup(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := store.Open(context.Background(), path, logger)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	authStore := auth.NewStore(db)
	ctx := context.Background()
	alice, _ := authStore.Create(ctx, "alice", "password", auth.AccessPlayer)
	bob, _ := authStore.Create(ctx, "bob", "password", auth.AccessPlayer)

	root := filepath.Join(dir, "blobs")
	svc, err := NewService(db, root)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return &env{db: db, svc: svc, alice: alice, bob: bob, root: root}
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestPut_StoresBlobAndReturnsHash(t *testing.T) {
	e := setup(t)
	data := []byte("hello world")
	hash, size, mime, err := e.svc.PutBlob(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("PutBlob: %v", err)
	}
	if hash != sha256Hex(data) {
		t.Errorf("hash = %s, want %s", hash, sha256Hex(data))
	}
	if size != int64(len(data)) {
		t.Errorf("size = %d", size)
	}
	if !strings.HasPrefix(mime, "text/plain") {
		t.Errorf("mime = %q", mime)
	}
}

func TestPut_LayoutIsTwoLevels(t *testing.T) {
	e := setup(t)
	data := []byte("xyz")
	hash, _, _, _ := e.svc.PutBlob(context.Background(), bytes.NewReader(data))
	// Expected path: <root>/<ab>/<cd>/<full-hash>
	want := filepath.Join(e.root, hash[0:2], hash[2:4], hash)
	rc, err := e.svc.GetBlob(context.Background(), hash)
	if err != nil {
		t.Fatalf("GetBlob: %v", err)
	}
	rc.Close()
	if _, err := os.Stat(want); err != nil {
		t.Errorf("blob not at %s: %v", want, err)
	}
}

func TestPut_DedupSameContent(t *testing.T) {
	e := setup(t)
	data := []byte("dedupe me")
	h1, _, _, _ := e.svc.PutBlob(context.Background(), bytes.NewReader(data))
	h2, _, _, _ := e.svc.PutBlob(context.Background(), bytes.NewReader(data))
	if h1 != h2 {
		t.Errorf("hashes differ: %s vs %s", h1, h2)
	}
	// Only one blob on disk regardless.
}

func TestNewFile_RejectsDuplicateSlug(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	data := []byte("a")
	hash, size, mime, _ := e.svc.PutBlob(ctx, bytes.NewReader(data))
	if _, err := e.svc.NewFile(ctx, "notes", e.alice.ID, hash, size, mime, ""); err != nil {
		t.Fatalf("NewFile first: %v", err)
	}
	if _, err := e.svc.NewFile(ctx, "notes", e.alice.ID, hash, size, mime, ""); !errors.Is(err, ErrSlugTaken) {
		t.Errorf("err = %v, want ErrSlugTaken", err)
	}
}

func TestNewFile_TwoSlugsOneBlob(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	data := []byte("shared content")
	hash, size, mime, _ := e.svc.PutBlob(ctx, bytes.NewReader(data))
	_, _ = e.svc.NewFile(ctx, "first", e.alice.ID, hash, size, mime, "")
	_, _ = e.svc.NewFile(ctx, "second", e.alice.ID, hash, size, mime, "")

	files, err := e.svc.ListByOwner(ctx, e.alice.ID)
	if err != nil {
		t.Fatalf("ListByOwner: %v", err)
	}
	if len(files) != 2 {
		t.Errorf("expected 2 files, got %d", len(files))
	}
}

func TestGetBlob_NotFound(t *testing.T) {
	e := setup(t)
	if _, err := e.svc.GetBlob(context.Background(), strings.Repeat("0", 64)); err == nil {
		t.Errorf("expected error for unknown blob")
	}
}

func TestIssueUpload_TokenIsRandom32Hex(t *testing.T) {
	e := setup(t)
	tok, err := e.svc.IssueUpload(context.Background(), e.alice.ID, "notes", time.Minute)
	if err != nil {
		t.Fatalf("IssueUpload: %v", err)
	}
	if len(tok.Value) != 32 {
		t.Errorf("token len = %d", len(tok.Value))
	}
	for _, c := range tok.Value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			t.Errorf("token char %q not lowercase hex", c)
		}
	}
}

func TestIssueUpload_PersistsToken(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	tok, _ := e.svc.IssueUpload(ctx, e.alice.ID, "notes", time.Minute)
	got, err := e.svc.RedeemToken(ctx, tok.Value)
	if err != nil {
		t.Fatalf("RedeemToken: %v", err)
	}
	if got.Kind != "upload" || got.AccountID != e.alice.ID || got.Slug != "notes" {
		t.Errorf("got %+v", got)
	}
}

func TestRedeem_OneShot(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	tok, _ := e.svc.IssueUpload(ctx, e.alice.ID, "notes", time.Minute)
	if _, err := e.svc.RedeemToken(ctx, tok.Value); err != nil {
		t.Fatalf("first redeem: %v", err)
	}
	if _, err := e.svc.RedeemToken(ctx, tok.Value); !errors.Is(err, ErrTokenUsed) {
		t.Errorf("second redeem err = %v, want ErrTokenUsed", err)
	}
}

func TestRedeem_Expired(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	tok, _ := e.svc.IssueUpload(ctx, e.alice.ID, "notes", -time.Second)
	if _, err := e.svc.RedeemToken(ctx, tok.Value); !errors.Is(err, ErrTokenExpired) {
		t.Errorf("err = %v, want ErrTokenExpired", err)
	}
}

func TestRedeem_Missing(t *testing.T) {
	e := setup(t)
	if _, err := e.svc.RedeemToken(context.Background(), strings.Repeat("0", 32)); !errors.Is(err, ErrTokenNotFound) {
		t.Errorf("err = %v, want ErrTokenNotFound", err)
	}
}

func TestIssueDownload_RequiresFile(t *testing.T) {
	e := setup(t)
	_, err := e.svc.IssueDownload(context.Background(), e.alice.ID, 9999, time.Minute)
	if err == nil {
		t.Errorf("expected error for unknown file")
	}
}

func TestIssueDownload_OK(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	hash, size, mime, _ := e.svc.PutBlob(ctx, bytes.NewReader([]byte("x")))
	fileID, _ := e.svc.NewFile(ctx, "notes", e.alice.ID, hash, size, mime, "")
	tok, err := e.svc.IssueDownload(ctx, e.alice.ID, fileID, time.Minute)
	if err != nil {
		t.Fatalf("IssueDownload: %v", err)
	}
	if tok.Kind != "download" {
		t.Errorf("kind = %q", tok.Kind)
	}
	if tok.FileID == nil || *tok.FileID != fileID {
		t.Errorf("FileID = %v, want %d", tok.FileID, fileID)
	}
}

func TestGetFileBySlug(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	hash, size, mime, _ := e.svc.PutBlob(ctx, bytes.NewReader([]byte("x")))
	_, _ = e.svc.NewFile(ctx, "notes", e.alice.ID, hash, size, mime, "memo")
	f, err := e.svc.GetFile(ctx, "notes")
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if f.Slug != "notes" || f.OwnerID != e.alice.ID || f.Description != "memo" {
		t.Errorf("got %+v", f)
	}
}

func TestDeleteFile_RemovesRow(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	hash, size, mime, _ := e.svc.PutBlob(ctx, bytes.NewReader([]byte("x")))
	id, _ := e.svc.NewFile(ctx, "notes", e.alice.ID, hash, size, mime, "")
	if err := e.svc.DeleteFile(ctx, id); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	if _, err := e.svc.GetFile(ctx, "notes"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestJanitor_RemovesExpiredTokens(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	t1, _ := e.svc.IssueUpload(ctx, e.alice.ID, "a", -time.Second)
	t2, _ := e.svc.IssueUpload(ctx, e.alice.ID, "b", time.Minute)

	if err := e.svc.Janitor(ctx, time.Minute); err != nil {
		t.Fatalf("Janitor: %v", err)
	}
	if _, err := e.svc.RedeemToken(ctx, t1.Value); !errors.Is(err, ErrTokenNotFound) {
		t.Errorf("expired token still present: %v", err)
	}
	if _, err := e.svc.RedeemToken(ctx, t2.Value); err != nil {
		t.Errorf("live token should still redeem: %v", err)
	}
}
