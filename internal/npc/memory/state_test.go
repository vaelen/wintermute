// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

//go:build test

package memory

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/vaelen/wintermute/internal/llm"
	_ "github.com/vaelen/wintermute/internal/llm/fake"
	"github.com/vaelen/wintermute/internal/store/vec"
	"github.com/vaelen/wintermute/internal/world"
)

const testNPCID world.ObjectID = 42

// stubStore is a minimal in-memory Store for State tests. It records
// every Insert and serves Search via vec.CosineSimilarity over the
// inserted/seeded memories, so retrieval tests don't need SQLite.
type stubStore struct {
	mu       sync.Mutex
	memories []Memory
	bumped   map[int64]int
}

func newStubStore() *stubStore { return &stubStore{bumped: map[int64]int{}} }

func (s *stubStore) Insert(_ context.Context, m Memory) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m.ID = int64(len(s.memories) + 1)
	s.memories = append(s.memories, m)
	return m.ID, nil
}

func (s *stubStore) Search(_ context.Context, npcID world.ObjectID, q []float32, k int, threshold float64) ([]Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	type scored struct {
		m   Memory
		sim float32
	}
	var hits []scored
	for _, m := range s.memories {
		if m.NPCID != npcID {
			continue
		}
		sim := vec.CosineSimilarity(q, m.Embedding)
		if float64(sim) < threshold {
			continue
		}
		hits = append(hits, scored{m, sim})
	}
	for i := 0; i < len(hits); i++ {
		for j := i + 1; j < len(hits); j++ {
			if hits[j].sim > hits[i].sim {
				hits[i], hits[j] = hits[j], hits[i]
			}
		}
	}
	if len(hits) > k {
		hits = hits[:k]
	}
	out := make([]Memory, len(hits))
	for i, h := range hits {
		out[i] = h.m
	}
	return out, nil
}

func (s *stubStore) BumpSalience(_ context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bumped[id]++
	return nil
}

func (s *stubStore) seed(items ...Memory) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, mem := range items {
		if mem.ID == 0 {
			mem.ID = int64(len(s.memories) + 1)
		}
		s.memories = append(s.memories, mem)
	}
}

func (s *stubStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.memories)
}

func (s *stubStore) bumpsFor(id int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bumped[id]
}

func startStateForTest(t *testing.T, st *stubStore, llmClient llm.LLM, idle time.Duration) *State {
	t.Helper()
	w := NewWorker(WorkerConfig{
		NPCID:     testNPCID,
		LLM:       llmClient,
		Store:     st,
		JobBuffer: 8,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.Run(ctx)
	return NewState(StateConfig{
		NPCID:        testNPCID,
		LLM:          llmClient,
		Store:        st,
		Worker:       w,
		ShortTermCap: 20,
		IdleTimeout:  idle,
	})
}

func TestStateAppendAndRecent(t *testing.T) {
	st := startStateForTest(t, newStubStore(), newFakeLLM(t), time.Hour)
	playerID := world.ObjectID(7)
	st.Append(playerID, Turn{Speaker: "alice", Text: "hi"})
	st.Append(playerID, Turn{Speaker: "<npc>", Text: "evening"})

	turns := st.Recent(playerID, 5)
	if len(turns) != 2 {
		t.Fatalf("Recent length = %d, want 2", len(turns))
	}
	if turns[0].Text != "hi" || turns[1].Text != "evening" {
		t.Errorf("Recent turns = %+v, want [hi, evening]", turns)
	}
}

func TestStateEndConversationDrainsToWorker(t *testing.T) {
	store := newStubStore()
	st := startStateForTest(t, store, newFakeLLM(t), time.Hour)
	playerID := world.ObjectID(7)

	st.Append(playerID, Turn{Speaker: "alice", Text: "transcript: looking for the gate"})
	st.Append(playerID, Turn{Speaker: "<npc>", Text: "the bartender nods"})

	st.EndConversation(context.Background(), playerID)
	waitFor(t, "memory recorded after EndConversation", 2*time.Second, func() bool {
		return store.count() == 1
	})

	if got := st.Recent(playerID, 5); len(got) != 0 {
		t.Errorf("after EndConversation, Recent length = %d, want 0", len(got))
	}
}

func TestStateIdleTimerFiresDrain(t *testing.T) {
	store := newStubStore()
	st := startStateForTest(t, store, newFakeLLM(t), 50*time.Millisecond)
	playerID := world.ObjectID(7)

	st.Append(playerID, Turn{Speaker: "alice", Text: "transcript: anything"})

	waitFor(t, "idle timer fires", 2*time.Second, func() bool {
		return store.count() == 1
	})
}

func TestStateAppendResetsIdleTimer(t *testing.T) {
	store := newStubStore()
	st := startStateForTest(t, store, newFakeLLM(t), 50*time.Millisecond)
	playerID := world.ObjectID(7)

	st.Append(playerID, Turn{Speaker: "alice", Text: "transcript: one"})
	for i := 0; i < 5; i++ {
		time.Sleep(20 * time.Millisecond)
		st.Append(playerID, Turn{Speaker: "alice", Text: "transcript: more"})
	}
	if got := store.count(); got != 0 {
		t.Errorf("idle timer fired despite continuous appends: %d memories stored", got)
	}
	waitFor(t, "idle timer fires after final append", 2*time.Second, func() bool {
		return store.count() == 1
	})
}

func TestStateRetrieveReturnsTopMemories(t *testing.T) {
	store := newStubStore()
	store.seed(
		Memory{NPCID: testNPCID, Summary: "old conversation about the gate", Embedding: []float32{1, 0}, Salience: 1},
		Memory{NPCID: testNPCID, Summary: "old conversation about coffee", Embedding: []float32{0, 1}, Salience: 1},
	)
	st := startStateForTest(t, store, newFakeLLM(t), time.Hour)
	playerID := world.ObjectID(7)

	st.Append(playerID, Turn{Speaker: "alice", Text: "the gate"})

	got, err := st.Retrieve(context.Background(), playerID, 5, -1.0)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(got) == 0 {
		t.Fatalf("Retrieve returned 0 memories; expected at least one")
	}
}

func TestStateBumpSalienceAfterRetrieval(t *testing.T) {
	store := newStubStore()
	store.seed(Memory{
		NPCID:     testNPCID,
		Summary:   "previous",
		Embedding: []float32{1, 0},
		Salience:  1,
	})
	st := startStateForTest(t, store, newFakeLLM(t), time.Hour)
	playerID := world.ObjectID(7)
	st.Append(playerID, Turn{Speaker: "alice", Text: "anything"})

	got, err := st.Retrieve(context.Background(), playerID, 5, -1.0)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	st.BumpSalience(context.Background(), got)

	if got := store.bumpsFor(1); got != 1 {
		t.Errorf("salience bumps for memory 1 = %d, want 1", got)
	}
}

func TestStateStopDrainsAllConversations(t *testing.T) {
	store := newStubStore()
	st := startStateForTest(t, store, newFakeLLM(t), time.Hour)

	st.Append(world.ObjectID(7), Turn{Speaker: "alice", Text: "transcript: one"})
	st.Append(world.ObjectID(8), Turn{Speaker: "bob", Text: "transcript: two"})

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := st.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if got := store.count(); got != 2 {
		t.Errorf("after Stop, memories = %d, want 2 (both conversations drained)", got)
	}
}
