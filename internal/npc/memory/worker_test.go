// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

//go:build test

package memory

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vaelen/wintermute/internal/llm"
	_ "github.com/vaelen/wintermute/internal/llm/fake"
	"github.com/vaelen/wintermute/internal/world"
)

func newFakeLLM(t *testing.T) llm.LLM {
	t.Helper()
	backend, err := llm.Open("fake", map[string]any{
		"responses": map[string]any{
			"transcript": "alice asked the bartender for the gate to the Sprawl.",
		},
		"default":       "(no summary)",
		"embedding_dim": 16,
	})
	if err != nil {
		t.Fatalf("Open fake: %v", err)
	}
	return backend
}

// recordingStore captures Insert calls. Used by worker tests to avoid the
// SQL round-trip when only the job → store hand-off matters.
type recordingStore struct {
	mu      sync.Mutex
	memories []Memory
}

func (r *recordingStore) Insert(_ context.Context, m Memory) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m.ID = int64(len(r.memories) + 1)
	r.memories = append(r.memories, m)
	return m.ID, nil
}

func (r *recordingStore) Search(context.Context, world.ObjectID, []float32, int, float64) ([]Memory, error) {
	return nil, nil
}

func (r *recordingStore) BumpSalience(context.Context, int64) error { return nil }

func (r *recordingStore) snapshot() []Memory {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Memory, len(r.memories))
	copy(out, r.memories)
	return out
}

func TestWorkerSummarizesAndStores(t *testing.T) {
	rec := &recordingStore{}
	w := NewWorker(WorkerConfig{
		NPCID:           world.ObjectID(42),
		LLM:             newFakeLLM(t),
		Store:           rec,
		SummarizerModel: "fake-summarizer",
		EmbeddingModel:  "fake-embed",
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()

	w.Submit(SummaryJob{
		PlayerID: world.ObjectID(7),
		Turns: []Turn{
			{Speaker: "alice", Text: "transcript: ask about the gate"},
			{Speaker: "<npc>", Text: "nods slowly"},
		},
	})

	waitFor(t, "summary recorded", 2*time.Second, func() bool {
		return len(rec.snapshot()) == 1
	})

	memories := rec.snapshot()
	if len(memories) != 1 {
		t.Fatalf("got %d memories, want 1", len(memories))
	}
	m := memories[0]
	if m.NPCID != world.ObjectID(42) {
		t.Errorf("memory NPCID = %d, want 42", m.NPCID)
	}
	if !strings.Contains(m.Summary, "Sprawl") {
		t.Errorf("memory summary = %q, want one that includes 'Sprawl'", m.Summary)
	}
	if len(m.Embedding) == 0 {
		t.Errorf("memory embedding is empty; expected the worker to embed the summary")
	}
}

func TestWorkerSkipsEmptyJobs(t *testing.T) {
	rec := &recordingStore{}
	w := NewWorker(WorkerConfig{
		NPCID: world.ObjectID(42),
		LLM:   newFakeLLM(t),
		Store: rec,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()

	w.Submit(SummaryJob{PlayerID: world.ObjectID(7)})
	time.Sleep(50 * time.Millisecond)

	if got := len(rec.snapshot()); got != 0 {
		t.Errorf("empty job produced %d memories, want 0", got)
	}
}

func TestWorkerStopDrainsPending(t *testing.T) {
	rec := &recordingStore{}
	w := NewWorker(WorkerConfig{
		NPCID: world.ObjectID(42),
		LLM:   newFakeLLM(t),
		Store: rec,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()

	for i := 0; i < 3; i++ {
		w.Submit(SummaryJob{
			PlayerID: world.ObjectID(7),
			Turns: []Turn{
				{Speaker: "alice", Text: "transcript: number " + string(rune('a'+i))},
			},
		})
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	if err := w.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("Run did not return after Stop")
	}

	if got := len(rec.snapshot()); got != 3 {
		t.Errorf("after Stop, memories = %d, want 3 (Stop must drain)", got)
	}
}

func waitFor(t *testing.T, what string, budget time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}
