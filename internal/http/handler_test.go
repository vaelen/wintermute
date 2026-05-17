// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package http

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	netHTTP "net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/files"
	"github.com/vaelen/wintermute/internal/store"
)

type env struct {
	db    *store.DB
	files *files.Service
	srv   *httptest.Server
	alice *auth.Account
	got   []UploadEvent
	mu    sync.Mutex
}

func setup(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := store.Open(context.Background(), filepath.Join(dir, "test.db"), logger)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	authStore := auth.NewStore(db)
	alice, _ := authStore.Create(context.Background(), "alice", "password", auth.AccessPlayer)

	fs, err := files.NewService(db, filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatalf("files.NewService: %v", err)
	}

	e := &env{db: db, files: fs, alice: alice}
	h := NewHandler(fs, HandlerOptions{
		MaxUploadBytes: 1 << 20,
		OnUpload: func(ev UploadEvent) {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.got = append(e.got, ev)
		},
	})
	e.srv = httptest.NewServer(h)
	t.Cleanup(e.srv.Close)
	return e
}

func TestUpload_HappyPath(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	tok, _ := e.files.IssueUpload(ctx, e.alice.ID, "notes", time.Minute)

	body := []byte("file contents here")
	req, _ := netHTTP.NewRequest("POST", e.srv.URL+"/upload/"+tok.Value, bytes.NewReader(body))
	resp, err := netHTTP.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, b)
	}
	var got UploadResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Slug != "notes" {
		t.Errorf("slug = %q", got.Slug)
	}
	if got.Size != int64(len(body)) {
		t.Errorf("size = %d", got.Size)
	}
	if got.Hash == "" {
		t.Errorf("hash empty")
	}

	// File row exists.
	f, err := e.files.GetFile(ctx, "notes")
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if f.Hash != got.Hash || f.Size != got.Size {
		t.Errorf("row mismatch: %+v vs %+v", f, got)
	}

	// OnUpload was called.
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.got) != 1 {
		t.Fatalf("OnUpload calls = %d", len(e.got))
	}
	if e.got[0].AccountID != e.alice.ID || e.got[0].Slug != "notes" {
		t.Errorf("event = %+v", e.got[0])
	}
}

func TestUpload_RefusesSecondUse(t *testing.T) {
	e := setup(t)
	tok, _ := e.files.IssueUpload(context.Background(), e.alice.ID, "notes", time.Minute)
	url := e.srv.URL + "/upload/" + tok.Value
	for i, want := range []int{200, 410} {
		resp, err := netHTTP.Post(url, "application/octet-stream", strings.NewReader("x"))
		if err != nil {
			t.Fatalf("POST %d: %v", i, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("attempt %d: status = %d, want %d", i, resp.StatusCode, want)
		}
	}
}

func TestUpload_RefusesGet(t *testing.T) {
	e := setup(t)
	tok, _ := e.files.IssueUpload(context.Background(), e.alice.ID, "notes", time.Minute)
	resp, _ := netHTTP.Get(e.srv.URL + "/upload/" + tok.Value)
	defer resp.Body.Close()
	if resp.StatusCode != 405 {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

func TestUpload_TooLarge(t *testing.T) {
	e := setup(t)
	tok, _ := e.files.IssueUpload(context.Background(), e.alice.ID, "big", time.Minute)
	big := bytes.Repeat([]byte("X"), (1<<20)+1)
	req, _ := netHTTP.NewRequest("POST", e.srv.URL+"/upload/"+tok.Value, bytes.NewReader(big))
	resp, err := netHTTP.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 413 {
		t.Errorf("status = %d, want 413", resp.StatusCode)
	}
}

func TestUpload_UnknownToken(t *testing.T) {
	e := setup(t)
	resp, _ := netHTTP.Post(e.srv.URL+"/upload/"+strings.Repeat("0", 32),
		"application/octet-stream", strings.NewReader("x"))
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestDownload_HappyPath(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	hash, size, mime, _ := e.files.PutBlob(ctx, strings.NewReader("downloadable"))
	fid, _ := e.files.NewFile(ctx, "dl", e.alice.ID, hash, size, mime, "")
	tok, _ := e.files.IssueDownload(ctx, e.alice.ID, fid, time.Minute)

	resp, err := netHTTP.Get(e.srv.URL + "/download/" + tok.Value)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "downloadable" {
		t.Errorf("body = %q", body)
	}
	cd := resp.Header.Get("Content-Disposition")
	if !strings.Contains(cd, `filename="dl"`) {
		t.Errorf("Content-Disposition = %q", cd)
	}
}

func TestDownload_RefusesSecondUse(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	hash, size, mime, _ := e.files.PutBlob(ctx, strings.NewReader("x"))
	fid, _ := e.files.NewFile(ctx, "dl", e.alice.ID, hash, size, mime, "")
	tok, _ := e.files.IssueDownload(ctx, e.alice.ID, fid, time.Minute)

	r1, _ := netHTTP.Get(e.srv.URL + "/download/" + tok.Value)
	_ = r1.Body.Close()
	if r1.StatusCode != 200 {
		t.Fatalf("first GET: %d", r1.StatusCode)
	}
	r2, _ := netHTTP.Get(e.srv.URL + "/download/" + tok.Value)
	_ = r2.Body.Close()
	if r2.StatusCode != 410 {
		t.Errorf("second GET: %d, want 410", r2.StatusCode)
	}
}

func TestDownload_RefusesUploadToken(t *testing.T) {
	e := setup(t)
	tok, _ := e.files.IssueUpload(context.Background(), e.alice.ID, "x", time.Minute)
	r, _ := netHTTP.Get(e.srv.URL + "/download/" + tok.Value)
	defer r.Body.Close()
	if r.StatusCode != 400 {
		t.Errorf("status = %d, want 400", r.StatusCode)
	}
}

// TestDownload_WrongKindDoesNotBurnToken — PR review fix: presenting an
// upload token to /download/ must return 400 without marking the token
// used. The client should then be able to POST the same token to /upload/.
func TestDownload_WrongKindDoesNotBurnToken(t *testing.T) {
	e := setup(t)
	tok, _ := e.files.IssueUpload(context.Background(), e.alice.ID, "x", time.Minute)
	r, _ := netHTTP.Get(e.srv.URL + "/download/" + tok.Value)
	_ = r.Body.Close()
	if r.StatusCode != 400 {
		t.Fatalf("wrong-kind status = %d, want 400", r.StatusCode)
	}
	// Retry against the correct endpoint — must still succeed.
	resp, err := netHTTP.Post(e.srv.URL+"/upload/"+tok.Value,
		"application/octet-stream", strings.NewReader("body"))
	if err != nil {
		t.Fatalf("retry POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Errorf("retry status = %d, body = %s", resp.StatusCode, b)
	}
}
