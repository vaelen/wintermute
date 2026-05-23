// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package npc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/vaelen/wintermute/internal/config"
	"github.com/vaelen/wintermute/internal/llm"
	"github.com/vaelen/wintermute/internal/llm/budget"
	"github.com/vaelen/wintermute/internal/npc/loop"
	"github.com/vaelen/wintermute/internal/npc/memory"
	"github.com/vaelen/wintermute/internal/store"
	"github.com/vaelen/wintermute/internal/world"
	"github.com/vaelen/wintermute/internal/world/engage"
	"github.com/vaelen/wintermute/internal/world/events"
)

// EngageLookup is the minimal interface HandleSay needs to detect when
// an NPC is currently in a private engagement (and with whom). The
// engage package's *Registry satisfies it. Kept as an interface so npc
// doesn't depend on a concrete engage type beyond what it actually uses.
type EngageLookup interface {
	HostEngagement(id world.ObjectID) *engage.Engagement
}

// LoopConfig controls per-NPC loop construction. Zero values are
// replaced with sensible defaults in Load.
type LoopConfig struct {
	Debounce     time.Duration
	MaxToolDepth int
	// DefaultGateModel is the fallback gate model name when an NPC's
	// npc_config.gate_model column is empty. "" disables the fallback;
	// such NPCs run gate-less (every observation triggers the chat
	// model). Set via cfg.NPC.Loop.DefaultGateModel in main.go.
	DefaultGateModel string
}

// LoopDeps bundles the cross-cutting dependencies the per-NPC loops
// need. All fields are optional: when any of Bus, Budget, World, or
// Tools is nil, the corresponding feature degrades gracefully (no
// loops are started when Bus is nil; budget is unenforced; broadcast
// is silenced; tools are unavailable).
type LoopDeps struct {
	Bus    events.Bus
	Budget *budget.Manager
	World  loop.Broadcaster
	Tools  loop.Tools
	Config LoopConfig
}

// Registry holds every NPC in the world keyed by id. With M7, dispatch
// happens on per-NPC tick goroutines that subscribe to the world's
// events.Bus. HandleSay's residual job is the engagement-aware
// brush-off broadcast.
//
// The registry stores a server-lifetime root context (set by Load and
// used as the parent for every loop goroutine and observer-spawned
// drain). Callers must NOT pass a per-session context here.
type Registry struct {
	world    *world.World
	db       *store.DB
	logger   *slog.Logger
	defaults config.LLMBackend
	rootCtx  context.Context

	mu   sync.RWMutex
	byID map[world.ObjectID]*NPC

	inFlightWG sync.WaitGroup

	// store is the shared long-term memory backend. Each NPC's State
	// references this same store; per-NPC scoping happens via NPCID
	// in queries.
	store memory.Store
	// memCancel cancels the per-NPC Worker goroutines started in
	// rebuild. Reset on every rebuild.
	memCancel context.CancelFunc

	// engage is wired after Load via SetEngageLookup so HandleSay can
	// detect whether an NPC is currently in a private engagement. Nil
	// means no lookup is available (tests that don't need the feature).
	engage EngageLookup

	// loopDeps bundles the cross-cutting deps used to construct
	// per-NPC loops in rebuild. Zero-value LoopDeps disables loops.
	loopDeps LoopDeps
	// loopMgr owns the per-NPC loop goroutines started in rebuild.
	// Nil when loopDeps.Bus is nil.
	loopMgr *loop.Manager
}

// SetEngageLookup wires the engagement registry so HandleSay can detect
// when an NPC is currently engaged and brush off external addressing
// instead of dispatching an LLM reply. Pass nil to clear.
func (r *Registry) SetEngageLookup(e EngageLookup) {
	r.mu.Lock()
	r.engage = e
	r.mu.Unlock()
}

// Load builds a Registry by reading every npc_config row, opening the
// configured backend per NPC, and indexing the result. A missing object or
// failed Open is logged at WARN and the registry continues; the failed NPC
// is recorded with a nil llm so HandleSay silently skips it.
//
// serverCtx is retained as the parent of every per-NPC loop goroutine
// and observer-spawned drain. It must be a server-lifetime context, not
// a per-session one. It is also used for the initial schema query.
func Load(serverCtx context.Context, db *store.DB, w *world.World, defaults config.LLMBackend, logger *slog.Logger, deps LoopDeps) (*Registry, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if serverCtx == nil {
		serverCtx = context.Background()
	}
	if deps.Config.Debounce == 0 {
		deps.Config.Debounce = 800 * time.Millisecond
	}
	if deps.Config.MaxToolDepth == 0 {
		deps.Config.MaxToolDepth = 3
	}
	r := &Registry{
		world:    w,
		db:       db,
		logger:   logger,
		defaults: defaults,
		rootCtx:  serverCtx,
		store:    memory.NewSQLStore(db),
		loopDeps: deps,
	}
	if err := r.rebuild(serverCtx); err != nil {
		return nil, err
	}
	// Wire structured presence observers so the memory layer can detect
	// conversation-end without parsing room broadcast strings. The
	// callbacks are intentionally cheap (just dispatch to per-NPC State
	// methods, which queue work for the worker goroutine and return).
	w.SetDetachObserver(r.onPlayerDetach)
	w.SetMoveObserver(r.onPlayerMove)
	return r, nil
}

// Reload re-reads every npc_config row and replaces the in-memory maps
// atomically. Existing LLM clients are discarded; new ones are constructed.
// Used by the @npcreload admin command.
func (r *Registry) Reload(ctx context.Context) error {
	return r.rebuild(ctx)
}

func (r *Registry) rebuild(ctx context.Context) error {
	rows, err := r.db.Read().QueryContext(ctx, `
		SELECT c.object_id, o.name, c.persona, c.backend, c.backend_opts,
		       COALESCE(c.chat_model, ''), COALESCE(c.gate_model, ''),
		       c.max_context, COALESCE(c.tools, '')
		  FROM npc_config c
		  JOIN objects o ON o.id = c.object_id
		 WHERE o.kind = 'npc'`)
	if err != nil {
		return fmt.Errorf("npc: read npc_config: %w", err)
	}
	defer rows.Close()

	byID := map[world.ObjectID]*NPC{}

	for rows.Next() {
		var (
			objID      int64
			name       string
			persona    string
			backend    string
			optsRaw    string
			chatModel  string
			gateModel  string
			maxContext int
			toolsRaw   string
		)
		if err := rows.Scan(&objID, &name, &persona, &backend, &optsRaw,
			&chatModel, &gateModel, &maxContext, &toolsRaw); err != nil {
			return fmt.Errorf("npc: scan npc_config: %w", err)
		}

		id := world.ObjectID(objID)
		if _, err := r.world.Object(id); err != nil {
			r.logger.Warn("npc: object not in world cache, skipping",
				"object_id", id, "err", err)
			continue
		}

		opts, err := mergeOpts(r.defaults.Opts, optsRaw)
		if err != nil {
			r.logger.Warn("npc: bad backend_opts JSON, ignoring NPC overrides",
				"npc", name, "err", err)
			opts = cloneMap(r.defaults.Opts)
		}
		backendName := backend
		if backendName == "" {
			backendName = r.defaults.Backend
		}

		var toolNames []string
		for _, t := range strings.Split(toolsRaw, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				toolNames = append(toolNames, t)
			}
		}

		n := &NPC{
			ObjectID:    id,
			Name:        name,
			Persona:     persona,
			Backend:     backendName,
			BackendOpts: opts,
			Model:       chatModel,
			GateModel:   gateModel,
			MaxContext:  maxContext,
			ToolNames:   toolNames,
		}
		client, err := llm.Open(backendName, opts)
		if err != nil {
			r.logger.Warn("npc: failed to open llm backend, NPC will not respond",
				"npc", name, "backend", backendName, "err", err)
		} else {
			n.llm = client
		}

		byID[id] = n
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("npc: iterate npc_config: %w", err)
	}

	// Tear down any previously-running workers and replace them. Done
	// here (between scan and commit) so a partial failure above leaves
	// the old workers alive.
	//
	// Order matters: drain each old State first (which drains its
	// short-term buffer through the worker and waits for the worker to
	// exit), then cancel the old worker context as a safety net.
	// Reversing the order would race a still-armed idle timer or
	// observer goroutine into a dead channel, silently dropping work.
	r.mu.RLock()
	oldNPCs := make([]*NPC, 0, len(r.byID))
	for _, n := range r.byID {
		oldNPCs = append(oldNPCs, n)
	}
	r.mu.RUnlock()
	for _, n := range oldNPCs {
		if n.Memory == nil {
			continue
		}
		if err := n.Memory.Stop(ctx); err != nil {
			r.logger.Warn("npc: rebuild: stop previous memory state",
				"npc", n.Name, "err", err)
		}
	}
	if r.memCancel != nil {
		r.memCancel()
	}
	memCtx, memCancel := context.WithCancel(r.rootCtx)
	r.memCancel = memCancel

	summarizerModel := optString(r.defaults.Opts, "summarizer_model")
	for _, n := range byID {
		if n.llm == nil {
			continue
		}
		// Per-NPC overrides could be added later as new npc_config
		// columns; for now every NPC uses the default summariser model.
		w := memory.NewWorker(memory.WorkerConfig{
			NPCID:           n.ObjectID,
			LLM:             n.llm,
			Store:           r.store,
			SummarizerModel: summarizerModel,
			Logger:          r.logger,
		})
		go w.Run(memCtx)
		n.Memory = memory.NewState(memory.StateConfig{
			NPCID:  n.ObjectID,
			LLM:    n.llm,
			Store:  r.store,
			Worker: w,
			Logger: r.logger,
		})
	}

	// Tear down any prior loop manager and start a fresh one. Done
	// after the memory workers so a Stop on the loops cannot race with
	// a hot worker queue: the worker drain in the SAME rebuild already
	// handled that boundary.
	//
	// Swap the loopMgr field under r.mu so concurrent readers
	// (LoopSnapshot, Shutdown) cannot see a torn value. The Stop on the
	// old manager and the Add calls on the new one happen OUTSIDE the
	// lock — Stop can block on goroutine drain, and we never want to
	// hold r.mu across that.
	var newMgr *loop.Manager
	if r.loopDeps.Bus != nil {
		newMgr = loop.NewManager(r.rootCtx)
	}
	r.mu.Lock()
	oldMgr := r.loopMgr
	r.loopMgr = newMgr
	r.mu.Unlock()
	if oldMgr != nil {
		oldMgr.Stop()
	}
	if newMgr != nil {
		engAdapter := engagementAdapter{r: r}
		for id, n := range byID {
			if n.llm == nil {
				continue
			}
			loc, err := r.world.LocationOf(id)
			if err != nil || loc.RoomID == 0 {
				r.logger.Warn("npc loop: NPC has no room, skipping",
					"npc", n.Name, "err", err)
				continue
			}
			gateModel := n.GateModel
			if gateModel == "" {
				gateModel = r.loopDeps.Config.DefaultGateModel
			}
			l := &loop.Loop{
				RoomID:       events.RoomID(loc.RoomID),
				Bus:          r.loopDeps.Bus,
				Debounce:     r.loopDeps.Config.Debounce,
				NPCID:        events.ObjectID(n.ObjectID),
				NPCName:      n.Name,
				Persona:      n.Persona,
				LLM:          n.llm,
				ChatModel:    n.Model,
				GateModel:    gateModel,
				Memory:       n.Memory,
				Budget:       r.loopDeps.Budget,
				Tools:        r.loopDeps.Tools,
				ToolNames:    n.ToolNames,
				MaxToolDepth: r.loopDeps.Config.MaxToolDepth,
				World:        r.loopDeps.World,
				Engage:       engAdapter,
				Logger:       r.logger.With("npc", n.Name),
			}
			newMgr.Add(l)
		}
	}

	r.mu.Lock()
	r.byID = byID
	r.mu.Unlock()
	return nil
}

// Get returns the NPC with the given object id, or nil.
func (r *Registry) Get(id world.ObjectID) *NPC {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byID[id]
}

// LookupByName resolves an NPC by name (case-insensitive), returning
// (NPC, true) on the first match. Per-NPC names are not guaranteed
// unique in the schema; returning the first match is acceptable for
// a debug command.
func (r *Registry) LookupByName(name string) (*NPC, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, n := range r.byID {
		if strings.EqualFold(n.Name, name) {
			return n, true
		}
	}
	return nil, false
}

// LoopSnapshot returns the snapshot of the named NPC's loop (recent
// observations + last reply). Returns (Snapshot{}, false) if the
// loop is not running.
func (r *Registry) LoopSnapshot(npcID world.ObjectID) (loop.Snapshot, bool) {
	r.mu.RLock()
	mgr := r.loopMgr
	r.mu.RUnlock()
	if mgr == nil {
		return loop.Snapshot{}, false
	}
	l := mgr.Get(events.ObjectID(npcID))
	if l == nil {
		return loop.Snapshot{}, false
	}
	return l.Snapshot(), true
}

// BudgetFor returns a snapshot of the three budget windows for npcID.
// The boolean is false when no budget is wired or no record exists
// yet. Used by the @npc-debug admin command.
func (r *Registry) BudgetFor(npcID world.ObjectID) (budget.Window, budget.Window, budget.Window, bool) {
	r.mu.RLock()
	mgr := r.loopDeps.Budget
	r.mu.RUnlock()
	if mgr == nil {
		return budget.Window{}, budget.Window{}, budget.Window{}, false
	}
	return mgr.WindowsFor(npcID)
}

// DB exposes the underlying store handle so admin commands that need
// to run direct SQL (e.g. @gc-memories) can do so without dragging
// the store dependency through cmd.
func (r *Registry) DB() *store.DB { return r.db }

// NPCsInRoom returns the registered NPCs currently located in room. NPCs in
// the room without an npc_config entry are silently omitted.
func (r *Registry) NPCsInRoom(roomID world.RoomID) []*NPC {
	objs := r.world.NPCsInRoom(roomID)
	if len(objs) == 0 {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*NPC, 0, len(objs))
	for _, o := range objs {
		if n := r.byID[o.ID]; n != nil {
			out = append(out, n)
		}
	}
	return out
}

// HandleSay is the SayObserver wired by main.go. With the M7 loop,
// dispatch happens on the per-NPC tick goroutine, which subscribes to
// the world's events.Bus. The observer's residual responsibility is
// the engagement-aware brush-off line: when an NPC is currently
// engaged with someone other than the speaker, emit a one-line
// brush-off broadcast to the room so the speaker isn't left hanging.
// The loop's shouldDropEvent then filters this same event so the NPC
// does not also respond via the LLM.
//
// No ctx parameter on purpose: the broadcast is synchronous and the
// engagement lookup is in-memory.
func (r *Registry) HandleSay(roomID world.RoomID, speakerID world.ObjectID, speakerName, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	r.mu.RLock()
	eng := r.engage
	r.mu.RUnlock()
	if eng == nil {
		return
	}
	for _, n := range r.NPCsInRoom(roomID) {
		e := eng.HostEngagement(n.ObjectID)
		if e == nil {
			continue
		}
		if engagedWithSpeaker(e, speakerID) {
			continue
		}
		msg := fmt.Sprintf("%s raises a finger to %s — \"one moment.\"\r\n",
			n.Name, speakerName)
		r.world.BroadcastToRoom(roomID, 0, msg)
	}
}

// Wait blocks until every in-flight observer-spawned drain goroutine
// has finished. Callers MUST call this before closing the underlying
// DB or LLM resources at shutdown; otherwise an in-flight worker
// submission that subsequently touches the DB could race with teardown.
func (r *Registry) Wait() {
	r.inFlightWG.Wait()
}

// Shutdown is the full teardown path. It stops every per-NPC loop
// goroutine, waits for observer-spawned drain goroutines (bounded by
// ctx), and finally drains every per-NPC short-term buffer through the
// summarisation workers (also bounded by ctx). Returns ctx.Err() if
// the deadline expires before the work completes.
func (r *Registry) Shutdown(ctx context.Context) error {
	// Step 1: stop loops first so no new dispatches land on the memory
	// worker queue while it's being drained. Read the field under r.mu
	// so a concurrent rebuild (theoretical at shutdown but cheap to
	// guard against) cannot tear the pointer.
	r.mu.RLock()
	mgr := r.loopMgr
	r.mu.RUnlock()
	if mgr != nil {
		mgr.Stop()
	}

	// Step 2: bounded wait for observer-spawned drain goroutines. If
	// ctx expires first, log and stop — the goroutines will eventually
	// exit via their own observerSubmitTimeout and the worker-context
	// cancel below.
	waitDone := make(chan struct{})
	go func() { r.inFlightWG.Wait(); close(waitDone) }()
	select {
	case <-waitDone:
	case <-ctx.Done():
		r.logger.Warn("npc: shutdown: in-flight wait timed out, proceeding",
			"err", ctx.Err())
		if r.memCancel != nil {
			r.memCancel()
		}
		return ctx.Err()
	}

	// Step 3: drain each NPC's memory state. Each Stop call is itself
	// bounded by ctx, so this loop cannot exceed the remaining budget.
	r.mu.RLock()
	npcs := make([]*NPC, 0, len(r.byID))
	for _, n := range r.byID {
		npcs = append(npcs, n)
	}
	r.mu.RUnlock()

	var firstErr error
	for _, n := range npcs {
		if n.Memory == nil {
			continue
		}
		if err := n.Memory.Stop(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	// Cancel the worker context as a final safety net.
	if r.memCancel != nil {
		r.memCancel()
	}
	return firstErr
}

// observerSubmitTimeout bounds how long an observer-spawned drain is
// willing to wait if the worker queue is saturated. Generous because a
// stalled queue here means the engine is already in trouble; we'd
// rather log a drop than wedge the goroutine forever.
const observerSubmitTimeout = 30 * time.Second

// onPlayerDetach ends the conversation for every NPC currently in the
// player's room. Called by the world layer after a Detach broadcast.
func (r *Registry) onPlayerDetach(playerID world.ObjectID, roomID world.RoomID, _ world.DisconnectReason) {
	r.inFlightWG.Add(1)
	go func() {
		defer r.inFlightWG.Done()
		ctx, cancel := context.WithTimeout(r.rootCtx, observerSubmitTimeout)
		defer cancel()
		for _, n := range r.NPCsInRoom(roomID) {
			if n.Memory == nil {
				continue
			}
			n.Memory.EndConversation(ctx, playerID)
		}
	}()
}

// onPlayerMove ends the conversation for every NPC the player left
// behind in the source room.
func (r *Registry) onPlayerMove(playerID world.ObjectID, from world.RoomID, _ world.RoomID, _ string) {
	r.inFlightWG.Add(1)
	go func() {
		defer r.inFlightWG.Done()
		ctx, cancel := context.WithTimeout(r.rootCtx, observerSubmitTimeout)
		defer cancel()
		for _, n := range r.NPCsInRoom(from) {
			if n.Memory == nil {
				continue
			}
			n.Memory.EndConversation(ctx, playerID)
		}
	}()
}

func mergeOpts(defaults map[string]any, raw string) (map[string]any, error) {
	out := cloneMap(defaults)
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return out, nil
	}
	overrides := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &overrides); err != nil {
		return out, err
	}
	for k, v := range overrides {
		out[k] = v
	}
	return out, nil
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// optString reads a string value from a backend opts map. Missing keys
// and non-string values return "" rather than an error, because every
// caller's fallback is "use the backend's default".
func optString(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// engagedWithSpeaker reports whether the engagement's participants
// include the speaker. Used by HandleSay to suppress the brush-off when
// the engaged participant is the one talking.
func engagedWithSpeaker(eng *engage.Engagement, speakerID world.ObjectID) bool {
	for _, p := range eng.Participants {
		if p.PlayerID == speakerID {
			return true
		}
	}
	return false
}

// engagementAdapter bridges the npc.EngageLookup (set via
// SetEngageLookup) to the loop.EngageLookup interface, converting
// between the named world.ObjectID and the events.ObjectID alias.
type engagementAdapter struct{ r *Registry }

func (a engagementAdapter) EngagedWith(npcID events.ObjectID) ([]events.ObjectID, bool) {
	a.r.mu.RLock()
	eng := a.r.engage
	a.r.mu.RUnlock()
	if eng == nil {
		return nil, false
	}
	e := eng.HostEngagement(world.ObjectID(npcID))
	if e == nil {
		return nil, false
	}
	ids := make([]events.ObjectID, 0, len(e.Participants))
	for _, p := range e.Participants {
		ids = append(ids, events.ObjectID(p.PlayerID))
	}
	return ids, true
}
