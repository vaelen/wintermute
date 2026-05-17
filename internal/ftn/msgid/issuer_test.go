// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package msgid

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"

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

func TestIssue_FirstSerialIsOne(t *testing.T) {
	d := tempDB(t)
	issuer := NewIssuer(d)
	m, err := issuer.Issue(context.Background(), "255:255/255.0@local")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if m.Serial != 1 {
		t.Errorf("Serial = %d, want 1", m.Serial)
	}
	if m.OriginRaw != "255:255/255.0@local" {
		t.Errorf("OriginRaw = %q", m.OriginRaw)
	}
	if m.Origin.Domain != "local" {
		t.Errorf("Origin not parsed: %+v", m.Origin)
	}
}

func TestIssue_SecondSerialIsTwo(t *testing.T) {
	d := tempDB(t)
	issuer := NewIssuer(d)
	ctx := context.Background()
	_, _ = issuer.Issue(ctx, "1:1/1.0@x")
	m, err := issuer.Issue(ctx, "1:1/1.0@x")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if m.Serial != 2 {
		t.Errorf("Serial = %d, want 2", m.Serial)
	}
}

func TestIssue_IndependentCounters(t *testing.T) {
	d := tempDB(t)
	issuer := NewIssuer(d)
	ctx := context.Background()
	_, _ = issuer.Issue(ctx, "1:1/1.0@a")
	_, _ = issuer.Issue(ctx, "1:1/1.0@a")
	m, err := issuer.Issue(ctx, "2:2/2.0@b")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if m.Serial != 1 {
		t.Errorf("Serial for new origaddr = %d, want 1", m.Serial)
	}
}

func TestIssue_RendersToWellFormedMSGID(t *testing.T) {
	d := tempDB(t)
	issuer := NewIssuer(d)
	m, err := issuer.Issue(context.Background(), "1:234/5.6@fidonet")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if got, want := m.String(), "1:234/5.6@fidonet 00000001"; got != want {
		t.Errorf("MSGID.String() = %q, want %q", got, want)
	}
}

func TestIssue_ConcurrentNoGaps(t *testing.T) {
	d := tempDB(t)
	issuer := NewIssuer(d)
	ctx := context.Background()
	const N = 100
	results := make(chan uint32, N)
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m, err := issuer.Issue(ctx, "1:1/1.0@x")
			if err != nil {
				results <- 0
				return
			}
			results <- m.Serial
		}()
	}
	wg.Wait()
	close(results)
	seen := map[uint32]bool{}
	for s := range results {
		if s == 0 {
			t.Errorf("an Issue call failed")
			continue
		}
		if seen[s] {
			t.Errorf("duplicate serial %d", s)
		}
		seen[s] = true
	}
	for i := uint32(1); i <= N; i++ {
		if !seen[i] {
			t.Errorf("missing serial %d", i)
		}
	}
}
