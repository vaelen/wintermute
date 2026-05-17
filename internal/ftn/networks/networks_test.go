// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package networks

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/vaelen/wintermute/internal/config"
	"github.com/vaelen/wintermute/internal/store"
)

func tempDB(t *testing.T) *store.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	d, err := store.Open(context.Background(), path, logger)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestBootstrap_AutoCreatesLocalWhenEmpty(t *testing.T) {
	d := tempDB(t)
	ctx := context.Background()
	if err := Bootstrap(ctx, d, nil, slog.Default()); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	all, err := List(ctx, d)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("want 1 network, got %d", len(all))
	}
	n := all[0]
	if n.Slug != "local" || n.Domain != "local" || n.OurAddr != "255:255/255.0" || !n.IsDefault {
		t.Errorf("auto-created local = %+v", n)
	}
}

func TestBootstrap_UpsertsDeclared(t *testing.T) {
	d := tempDB(t)
	ctx := context.Background()
	nets := []config.FTNNetwork{
		{Slug: "fidonet", Name: "FidoNet", Domain: "fidonet", Addr: "1:234/5.0"},
		{Slug: "fsxnet", Name: "fsxNet", Domain: "fsxnet", Addr: "21:1/100.0"},
	}
	if err := Bootstrap(ctx, d, nets, slog.Default()); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	all, err := List(ctx, d)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// 3 = 2 declared + auto-created local
	if len(all) != 3 {
		t.Fatalf("want 3 networks, got %d", len(all))
	}
	bySlug := map[string]Network{}
	for _, n := range all {
		bySlug[n.Slug] = n
	}
	if _, ok := bySlug["local"]; !ok {
		t.Errorf("auto-created local missing")
	}
	if !bySlug["local"].IsDefault {
		t.Errorf("local should be default when no other network is marked default")
	}
	if bySlug["fidonet"].OurAddr != "1:234/5.0" {
		t.Errorf("fidonet OurAddr = %q", bySlug["fidonet"].OurAddr)
	}
}

func TestBootstrap_HonorsExplicitDefault(t *testing.T) {
	d := tempDB(t)
	ctx := context.Background()
	nets := []config.FTNNetwork{
		{Slug: "fidonet", Name: "FidoNet", Domain: "fidonet", Addr: "1:234/5.0", Default: true},
	}
	if err := Bootstrap(ctx, d, nets, slog.Default()); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	all, err := List(ctx, d)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	bySlug := map[string]Network{}
	for _, n := range all {
		bySlug[n.Slug] = n
	}
	if !bySlug["fidonet"].IsDefault {
		t.Errorf("fidonet should be default")
	}
	if bySlug["local"].IsDefault {
		t.Errorf("local should not be default when fidonet is")
	}
}

func TestBootstrap_Idempotent(t *testing.T) {
	d := tempDB(t)
	ctx := context.Background()
	nets := []config.FTNNetwork{
		{Slug: "fidonet", Name: "FidoNet", Domain: "fidonet", Addr: "1:234/5.0"},
	}
	if err := Bootstrap(ctx, d, nets, slog.Default()); err != nil {
		t.Fatalf("first Bootstrap: %v", err)
	}
	if err := Bootstrap(ctx, d, nets, slog.Default()); err != nil {
		t.Fatalf("second Bootstrap: %v", err)
	}
	all, _ := List(ctx, d)
	if len(all) != 2 {
		t.Errorf("want 2 networks, got %d", len(all))
	}
}

func TestBootstrap_UpdatesExisting(t *testing.T) {
	d := tempDB(t)
	ctx := context.Background()
	first := []config.FTNNetwork{{Slug: "fidonet", Name: "FidoNet", Domain: "fidonet", Addr: "1:234/5.0"}}
	if err := Bootstrap(ctx, d, first, slog.Default()); err != nil {
		t.Fatalf("Bootstrap 1: %v", err)
	}
	updated := []config.FTNNetwork{{Slug: "fidonet", Name: "FidoNet (renamed)", Domain: "fidonet", Addr: "1:999/9.0"}}
	if err := Bootstrap(ctx, d, updated, slog.Default()); err != nil {
		t.Fatalf("Bootstrap 2: %v", err)
	}
	got, err := Get(ctx, d, "fidonet")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "FidoNet (renamed)" || got.OurAddr != "1:999/9.0" {
		t.Errorf("update didn't apply: %+v", got)
	}
}

func TestBootstrap_SwitchesDefault(t *testing.T) {
	d := tempDB(t)
	ctx := context.Background()
	// Initial: local is default (auto).
	if err := Bootstrap(ctx, d, nil, slog.Default()); err != nil {
		t.Fatalf("Bootstrap 1: %v", err)
	}
	// Now declare fidonet as default — local should lose its default.
	nets := []config.FTNNetwork{{Slug: "fidonet", Name: "FidoNet", Domain: "fidonet", Addr: "1:234/5.0", Default: true}}
	if err := Bootstrap(ctx, d, nets, slog.Default()); err != nil {
		t.Fatalf("Bootstrap 2: %v", err)
	}
	all, _ := List(ctx, d)
	bySlug := map[string]Network{}
	for _, n := range all {
		bySlug[n.Slug] = n
	}
	if !bySlug["fidonet"].IsDefault {
		t.Errorf("fidonet should now be default")
	}
	if bySlug["local"].IsDefault {
		t.Errorf("local should no longer be default")
	}
}

func TestGet_Missing(t *testing.T) {
	d := tempDB(t)
	if _, err := Get(context.Background(), d, "nosuch"); err == nil {
		t.Errorf("Get missing slug should error")
	}
}

func TestDefault_AfterBootstrap(t *testing.T) {
	d := tempDB(t)
	if err := Bootstrap(context.Background(), d, nil, slog.Default()); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	def, err := Default(context.Background(), d)
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	if def.Slug != "local" {
		t.Errorf("Default = %q, want local", def.Slug)
	}
}

func TestOrigAddr(t *testing.T) {
	n := Network{OurAddr: "1:234/5.0", Domain: "fidonet"}
	if got := n.OrigAddr(); got != "1:234/5.0@fidonet" {
		t.Errorf("OrigAddr = %q", got)
	}
}
