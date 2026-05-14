// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package memory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/vaelen/wintermute/internal/llm"
	"github.com/vaelen/wintermute/internal/world"
)

const (
	defaultJobBuffer = 16
	summarizerSystem = "You are a summariser. Given a transcript of a " +
		"conversation between an NPC and a player, produce a single 2-3 " +
		"sentence summary in third person focused on facts and intents. " +
		"Do not address the reader. Do not mention being an AI. Do not " +
		"include direct quotes."
)

// SummaryJob is one drained conversation slice handed off for
// summarisation. PlayerID is the interlocutor whose turns these are; it
// is recorded for logging and salience metrics, not used by the LLM
// prompt itself.
type SummaryJob struct {
	PlayerID world.ObjectID
	Turns    []Turn
}

// WorkerConfig parameterises a Worker. Only Store and LLM are required;
// every other field has a sensible default.
//
// The embedding model is intentionally absent: llm.LLM.Embed has no
// model parameter, so the configured embed model is a property of the
// LLM client itself (set at construction via the backend's opts). If a
// later milestone wants per-NPC or per-job embed-model overrides, the
// place to add it is the llm.LLM interface, not here.
type WorkerConfig struct {
	NPCID           world.ObjectID
	LLM             llm.LLM
	Store           Store
	SummarizerModel string // model passed to Chat; empty defers to the backend's default
	JobBuffer       int    // pending job channel capacity; 0 → defaultJobBuffer
	Logger          *slog.Logger
}

// Worker summarises completed conversations for a single NPC and persists
// the result. One Worker per NPC keeps per-NPC ordering trivial: jobs are
// processed sequentially, so a long-running summarise won't be racing
// another one for the same row set.
type Worker struct {
	cfg    WorkerConfig
	jobs   chan SummaryJob
	stopMu sync.Mutex
	stopCh chan struct{} // closed by Stop to ask Run to drain and exit
	doneCh chan struct{} // closed when Run returns
}

// NewWorker prepares a Worker. Run must be invoked on a goroutine before
// any Submit. NewWorker does not start it automatically because the
// caller may want to coordinate worker startup with a sync.WaitGroup.
func NewWorker(cfg WorkerConfig) *Worker {
	if cfg.JobBuffer <= 0 {
		cfg.JobBuffer = defaultJobBuffer
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Worker{
		cfg:    cfg,
		jobs:   make(chan SummaryJob, cfg.JobBuffer),
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

// Submit enqueues a job. Discards jobs with no turns (nothing to
// summarise). If the queue is full the caller is bounded by ctx: a
// cancelled or expired context drops the job (with a warn log) rather
// than blocking. Callers in the shutdown path pass their shutdown
// context; non-shutdown callers should pass a context with a
// reasonable timeout so a stalled LLM cannot pin them indefinitely.
func (w *Worker) Submit(ctx context.Context, j SummaryJob) {
	if len(j.Turns) == 0 {
		return
	}
	select {
	case w.jobs <- j:
	case <-w.stopCh:
		w.cfg.Logger.Debug("memory worker: submit after stop, dropping job",
			"npc", w.cfg.NPCID, "player", j.PlayerID)
	case <-ctx.Done():
		w.cfg.Logger.Warn("memory worker: submit cancelled, dropping job",
			"npc", w.cfg.NPCID, "player", j.PlayerID, "err", ctx.Err())
	}
}

// Run processes jobs until the context is cancelled OR Stop is called.
// When Stop is called, Run drains every remaining queued job before
// returning, so callers can submit a final batch on shutdown.
func (w *Worker) Run(ctx context.Context) {
	defer close(w.doneCh)
	for {
		select {
		case j := <-w.jobs:
			w.process(ctx, j)
		case <-w.stopCh:
			// Drain anything still queued, with the parent context still
			// gating the LLM calls so a hung backend won't keep us
			// alive forever.
			for {
				select {
				case j := <-w.jobs:
					w.process(ctx, j)
				default:
					return
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

// Stop signals Run to drain queued jobs and exit. The provided context
// bounds how long Stop is willing to wait for Run to finish.
func (w *Worker) Stop(ctx context.Context) error {
	w.stopMu.Lock()
	select {
	case <-w.stopCh:
		// already stopped
	default:
		close(w.stopCh)
	}
	w.stopMu.Unlock()
	select {
	case <-w.doneCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *Worker) process(ctx context.Context, j SummaryJob) {
	if w.cfg.LLM == nil || w.cfg.Store == nil {
		return
	}
	summary, err := w.summarise(ctx, j.Turns)
	if err != nil {
		w.cfg.Logger.Warn("memory worker: summarise failed",
			"npc", w.cfg.NPCID, "player", j.PlayerID, "err", err)
		return
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return
	}
	emb, err := w.cfg.LLM.Embed(ctx, summary)
	if err != nil {
		w.cfg.Logger.Warn("memory worker: embed failed",
			"npc", w.cfg.NPCID, "player", j.PlayerID, "err", err)
		return
	}
	if _, err := w.cfg.Store.Insert(ctx, Memory{
		NPCID:     w.cfg.NPCID,
		Summary:   summary,
		Embedding: emb,
		CreatedAt: time.Now(),
		Salience:  1.0,
	}); err != nil {
		w.cfg.Logger.Warn("memory worker: insert failed",
			"npc", w.cfg.NPCID, "player", j.PlayerID, "err", err)
		return
	}
}

func (w *Worker) summarise(ctx context.Context, turns []Turn) (string, error) {
	if len(turns) == 0 {
		return "", errors.New("memory worker: empty transcript")
	}
	var b strings.Builder
	b.WriteString("Summarise the following transcript:\n\n")
	for _, t := range turns {
		fmt.Fprintf(&b, "%s: %s\n", t.Speaker, t.Text)
	}
	msgs := []llm.Message{
		{Role: llm.RoleSystem, Content: summarizerSystem},
		{Role: llm.RoleUser, Content: b.String()},
	}
	resp, err := w.cfg.LLM.Chat(ctx, msgs, nil, llm.ChatOpts{Model: w.cfg.SummarizerModel})
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}
