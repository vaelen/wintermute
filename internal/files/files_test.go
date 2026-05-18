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
	if _, err := e.svc.NewFile(ctx, "notes", e.alice.ID, hash, size, mime, "", ""); err != nil {
		t.Fatalf("NewFile first: %v", err)
	}
	if _, err := e.svc.NewFile(ctx, "notes", e.alice.ID, hash, size, mime, "", ""); !errors.Is(err, ErrSlugTaken) {
		t.Errorf("err = %v, want ErrSlugTaken", err)
	}
}

func TestNewFile_TwoSlugsOneBlob(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	data := []byte("shared content")
	hash, size, mime, _ := e.svc.PutBlob(ctx, bytes.NewReader(data))
	_, _ = e.svc.NewFile(ctx, "first", e.alice.ID, hash, size, mime, "", "")
	_, _ = e.svc.NewFile(ctx, "second", e.alice.ID, hash, size, mime, "", "")

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
	got, err := e.svc.RedeemToken(ctx, tok.Value, KindUpload)
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
	if _, err := e.svc.RedeemToken(ctx, tok.Value, KindUpload); err != nil {
		t.Fatalf("first redeem: %v", err)
	}
	if _, err := e.svc.RedeemToken(ctx, tok.Value, KindUpload); !errors.Is(err, ErrTokenUsed) {
		t.Errorf("second redeem err = %v, want ErrTokenUsed", err)
	}
}

func TestRedeem_Expired(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	tok, _ := e.svc.IssueUpload(ctx, e.alice.ID, "notes", -time.Second)
	if _, err := e.svc.RedeemToken(ctx, tok.Value, KindUpload); !errors.Is(err, ErrTokenExpired) {
		t.Errorf("err = %v, want ErrTokenExpired", err)
	}
}

func TestRedeem_Missing(t *testing.T) {
	e := setup(t)
	if _, err := e.svc.RedeemToken(context.Background(), strings.Repeat("0", 32), KindUpload); !errors.Is(err, ErrTokenNotFound) {
		t.Errorf("err = %v, want ErrTokenNotFound", err)
	}
}

// TestRedeem_WrongKindDoesNotBurn covers the PR review fix: presenting
// an upload token to the download flow (or vice versa) must NOT mark
// the row used. The caller should be able to retry against the correct
// endpoint.
func TestRedeem_WrongKindDoesNotBurn(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	tok, _ := e.svc.IssueUpload(ctx, e.alice.ID, "notes", time.Minute)
	if _, err := e.svc.RedeemToken(ctx, tok.Value, KindDownload); !errors.Is(err, ErrTokenWrongKind) {
		t.Fatalf("wrong-kind redeem err = %v, want ErrTokenWrongKind", err)
	}
	// The token must still be redeemable with the correct kind.
	if _, err := e.svc.RedeemToken(ctx, tok.Value, KindUpload); err != nil {
		t.Errorf("retry with correct kind failed: %v", err)
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
	fileID, _ := e.svc.NewFile(ctx, "notes", e.alice.ID, hash, size, mime, "", "")
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
	_, _ = e.svc.NewFile(ctx, "notes", e.alice.ID, hash, size, mime, "memo", "")
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
	id, _ := e.svc.NewFile(ctx, "notes", e.alice.ID, hash, size, mime, "", "")
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

	stats, err := e.svc.Janitor(ctx, time.Minute)
	if err != nil {
		t.Fatalf("Janitor: %v", err)
	}
	if stats.TokensReaped != 1 {
		t.Errorf("TokensReaped = %d, want 1", stats.TokensReaped)
	}
	if _, err := e.svc.RedeemToken(ctx, t1.Value, KindUpload); !errors.Is(err, ErrTokenNotFound) {
		t.Errorf("expired token still present: %v", err)
	}
	if _, err := e.svc.RedeemToken(ctx, t2.Value, KindUpload); err != nil {
		t.Errorf("live token should still redeem: %v", err)
	}
}

func TestSeededDropboxArea(t *testing.T) {
	e := setup(t)
	areas, err := e.svc.ListAreas(context.Background())
	if err != nil {
		t.Fatalf("ListAreas: %v", err)
	}
	if len(areas) != 1 {
		t.Fatalf("len(areas) = %d, want 1 (just dropbox)", len(areas))
	}
	if areas[0].Slug != "dropbox" {
		t.Errorf("seed area slug = %q, want dropbox", areas[0].Slug)
	}
	if areas[0].Name != "Dropbox" {
		t.Errorf("seed area name = %q, want Dropbox", areas[0].Name)
	}
}

func TestNewFile_DefaultsToDropboxArea(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	hash, size, mime, _ := e.svc.PutBlob(ctx, bytes.NewReader([]byte("hi")))
	if _, err := e.svc.NewFile(ctx, "notes", e.alice.ID, hash, size, mime, "", ""); err != nil {
		t.Fatalf("NewFile: %v", err)
	}
	f, err := e.svc.GetFile(ctx, "notes")
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if f.Area != "dropbox" {
		t.Errorf("Area = %q, want dropbox", f.Area)
	}
}

func TestNewFile_UnknownAreaRejected(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	hash, size, mime, _ := e.svc.PutBlob(ctx, bytes.NewReader([]byte("hi")))
	if _, err := e.svc.NewFile(ctx, "notes", e.alice.ID, hash, size, mime, "", "ghost"); !errors.Is(err, ErrAreaNotFound) {
		t.Errorf("err = %v, want ErrAreaNotFound", err)
	}
}

func TestCreateArea_AndListFilesByArea(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if err := e.svc.CreateArea(ctx, Area{Slug: "warez", Name: "Warez"}); err != nil {
		t.Fatalf("CreateArea: %v", err)
	}
	hash, size, mime, _ := e.svc.PutBlob(ctx, bytes.NewReader([]byte("a")))
	if _, err := e.svc.NewFile(ctx, "a", e.alice.ID, hash, size, mime, "", "warez"); err != nil {
		t.Fatalf("NewFile a: %v", err)
	}
	h2, s2, m2, _ := e.svc.PutBlob(ctx, bytes.NewReader([]byte("b")))
	if _, err := e.svc.NewFile(ctx, "b", e.alice.ID, h2, s2, m2, "", ""); err != nil {
		t.Fatalf("NewFile b: %v", err)
	}

	warez, err := e.svc.ListFiles(ctx, ListFilter{Area: "warez"})
	if err != nil {
		t.Fatalf("ListFiles warez: %v", err)
	}
	if len(warez) != 1 || warez[0].Slug != "a" {
		t.Errorf("warez = %+v, want [a]", warez)
	}
	dbox, _ := e.svc.ListFiles(ctx, ListFilter{Area: "dropbox"})
	if len(dbox) != 1 || dbox[0].Slug != "b" {
		t.Errorf("dropbox = %+v, want [b]", dbox)
	}
}

func TestDeleteArea_RefusedWhenInUse(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	hash, size, mime, _ := e.svc.PutBlob(ctx, bytes.NewReader([]byte("a")))
	if _, err := e.svc.NewFile(ctx, "notes", e.alice.ID, hash, size, mime, "", ""); err != nil {
		t.Fatalf("NewFile: %v", err)
	}
	if err := e.svc.DeleteArea(ctx, "dropbox"); !errors.Is(err, ErrAreaInUse) {
		t.Errorf("DeleteArea: %v, want ErrAreaInUse", err)
	}
}

func TestDeleteArea_OKWhenEmpty(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if err := e.svc.CreateArea(ctx, Area{Slug: "ephemeral", Name: "Ephemeral"}); err != nil {
		t.Fatalf("CreateArea: %v", err)
	}
	if err := e.svc.DeleteArea(ctx, "ephemeral"); err != nil {
		t.Errorf("DeleteArea: %v", err)
	}
}

func TestDeleteArea_Unknown(t *testing.T) {
	e := setup(t)
	if err := e.svc.DeleteArea(context.Background(), "nope"); !errors.Is(err, ErrAreaNotFound) {
		t.Errorf("err = %v, want ErrAreaNotFound", err)
	}
}

func TestCreateArea_DuplicateSlug(t *testing.T) {
	e := setup(t)
	if err := e.svc.CreateArea(context.Background(), Area{Slug: "dropbox", Name: "Dup"}); !errors.Is(err, ErrAreaTaken) {
		t.Errorf("err = %v, want ErrAreaTaken", err)
	}
}
