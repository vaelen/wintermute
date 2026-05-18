// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package lua

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/config"
	"github.com/vaelen/wintermute/internal/files"
	ftnnetworks "github.com/vaelen/wintermute/internal/ftn/networks"
	"github.com/vaelen/wintermute/internal/store"
	"github.com/vaelen/wintermute/internal/world"
	worldapi "github.com/vaelen/wintermute/internal/world/api"
)

type filesPoolEnv struct {
	pool     *Pool
	api      *worldapi.API
	filesSvc *files.Service
	alice    *auth.Account
}

func newTestPoolWithFiles(t *testing.T) *filesPoolEnv {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "lua-files.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	db, err := store.Open(ctx, path, logger)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := ftnnetworks.Bootstrap(ctx, db, []config.FTNNetwork{}, logger); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	w, err := world.Load(ctx, db, logger)
	if err != nil {
		t.Fatalf("world.Load: %v", err)
	}

	authStore := auth.NewStore(db)
	alice, _ := authStore.Create(ctx, "alice", "alicepass", auth.AccessPlayer)

	filesSvc, err := files.NewService(db, filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatalf("files.NewService: %v", err)
	}

	api := worldapi.New(w, db, authStore, nil, logger)
	api.Files = filesSvc

	luaAPI := NewAPI(api, nil, ctx)
	pool := NewPool(PoolConfig{Size: 2, API: luaAPI})
	t.Cleanup(pool.Close)

	return &filesPoolEnv{pool: pool, api: api, filesSvc: filesSvc, alice: alice}
}

func TestLuaFileAreaListIncludesSeededDropbox(t *testing.T) {
	e := newTestPoolWithFiles(t)
	err := runScript(t, e.pool, `
		local list = wintermute.file.area.list()
		assert(#list == 1, "expected 1 area, got " .. tostring(#list))
		assert(list[1].slug == "dropbox", "slug = " .. tostring(list[1].slug))
		assert(list[1].name == "Dropbox", "name = " .. tostring(list[1].name))
	`)
	if err != nil {
		t.Fatalf("script: %v", err)
	}
}

func TestLuaFileAreaCreateAndDelete(t *testing.T) {
	e := newTestPoolWithFiles(t)
	err := runScript(t, e.pool, `
		wintermute.file.area.create({
			slug = "warez", name = "Warez",
			description = "Stuff",
			read_min_level = 2, write_min_level = 3, admin_min_level = 3,
		})
		local list = wintermute.file.area.list()
		assert(#list == 2, "expected 2 areas, got " .. tostring(#list))
		wintermute.file.area.delete("warez")
		list = wintermute.file.area.list()
		assert(#list == 1, "expected 1 area after delete, got " .. tostring(#list))
	`)
	if err != nil {
		t.Fatalf("script: %v", err)
	}
}

func TestLuaFileAreaDuplicateSlugCanonicalError(t *testing.T) {
	e := newTestPoolWithFiles(t)
	err := runScript(t, e.pool, `
		wintermute.file.area.create({slug = "dropbox", name = "Dup"})
	`)
	if err == nil {
		t.Fatal("expected duplicate_slug")
	}
	if !strings.Contains(err.Error(), "duplicate_slug: dropbox") {
		t.Errorf("err = %q, want duplicate_slug canonical envelope", err.Error())
	}
}

func TestLuaFileAreaDeleteRefusesWhenInUse(t *testing.T) {
	e := newTestPoolWithFiles(t)
	ctx := context.Background()
	// Land a file in dropbox via the service.
	hash, size, mime, _ := e.filesSvc.PutBlob(ctx, bytes.NewReader([]byte("hello")))
	if _, err := e.filesSvc.NewFile(ctx, "notes", e.alice.ID, hash, size, mime, "", ""); err != nil {
		t.Fatalf("NewFile: %v", err)
	}
	err := runScript(t, e.pool, `wintermute.file.area.delete("dropbox")`)
	if err == nil {
		t.Fatal("expected refusal")
	}
	if !strings.Contains(err.Error(), "invalid_argument") || !strings.Contains(err.Error(), "in use") {
		t.Errorf("err = %q, want invalid_argument 'in use'", err.Error())
	}
}

func TestLuaFileAreaDeleteUnknown(t *testing.T) {
	e := newTestPoolWithFiles(t)
	err := runScript(t, e.pool, `wintermute.file.area.delete("nope")`)
	if err == nil {
		t.Fatal("expected not_found")
	}
	if !strings.Contains(err.Error(), "not_found: area:nope") {
		t.Errorf("err = %q, want not_found canonical envelope", err.Error())
	}
}

func TestLuaFileListByOwnerAndArea(t *testing.T) {
	e := newTestPoolWithFiles(t)
	ctx := context.Background()
	// Seed two files: one in dropbox, one in a new area.
	if err := e.filesSvc.CreateArea(ctx, files.Area{Slug: "warez", Name: "Warez"}); err != nil {
		t.Fatalf("CreateArea: %v", err)
	}
	hash, size, mime, _ := e.filesSvc.PutBlob(ctx, bytes.NewReader([]byte("x")))
	if _, err := e.filesSvc.NewFile(ctx, "a", e.alice.ID, hash, size, mime, "", "dropbox"); err != nil {
		t.Fatalf("NewFile a: %v", err)
	}
	hash2, size2, mime2, _ := e.filesSvc.PutBlob(ctx, bytes.NewReader([]byte("y")))
	if _, err := e.filesSvc.NewFile(ctx, "b", e.alice.ID, hash2, size2, mime2, "", "warez"); err != nil {
		t.Fatalf("NewFile b: %v", err)
	}

	err := runScript(t, e.pool, `
		local all = wintermute.file.list({owner_username = "alice"})
		assert(#all == 2, "all = " .. tostring(#all))
		local box = wintermute.file.list({area = "dropbox"})
		assert(#box == 1, "dropbox = " .. tostring(#box))
		assert(box[1].slug == "a", "dropbox file slug = " .. tostring(box[1].slug))
		local warez = wintermute.file.list({owner_username = "alice", area = "warez"})
		assert(#warez == 1, "warez = " .. tostring(#warez))
		assert(warez[1].slug == "b", "warez file slug = " .. tostring(warez[1].slug))
	`)
	if err != nil {
		t.Fatalf("script: %v", err)
	}
}

func TestLuaFileDeleteRemovesRow(t *testing.T) {
	e := newTestPoolWithFiles(t)
	ctx := context.Background()
	hash, size, mime, _ := e.filesSvc.PutBlob(ctx, bytes.NewReader([]byte("z")))
	if _, err := e.filesSvc.NewFile(ctx, "notes", e.alice.ID, hash, size, mime, "", ""); err != nil {
		t.Fatalf("NewFile: %v", err)
	}
	err := runScript(t, e.pool, `wintermute.file.delete("notes")`)
	if err != nil {
		t.Fatalf("script: %v", err)
	}
	if _, err := e.filesSvc.GetFile(ctx, "notes"); err == nil {
		t.Errorf("file still exists after delete")
	}
}

func TestLuaFileDeleteUnknown(t *testing.T) {
	e := newTestPoolWithFiles(t)
	err := runScript(t, e.pool, `wintermute.file.delete("nope")`)
	if err == nil {
		t.Fatal("expected not_found")
	}
	if !strings.Contains(err.Error(), "not_found: file:nope") {
		t.Errorf("err = %q, want not_found canonical envelope", err.Error())
	}
}

func TestLuaFileSetACLs(t *testing.T) {
	e := newTestPoolWithFiles(t)
	ctx := context.Background()
	hash, size, mime, _ := e.filesSvc.PutBlob(ctx, bytes.NewReader([]byte("z")))
	if _, err := e.filesSvc.NewFile(ctx, "notes", e.alice.ID, hash, size, mime, "", ""); err != nil {
		t.Fatalf("NewFile: %v", err)
	}
	err := runScript(t, e.pool, `
		wintermute.file.set_acls("notes", {account_username = "alice", perms = 1})
	`)
	if err != nil {
		t.Fatalf("script: %v", err)
	}
}
