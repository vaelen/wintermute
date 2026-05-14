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
	"unicode"

	"github.com/vaelen/wintermute/internal/config"
	"github.com/vaelen/wintermute/internal/llm"
	"github.com/vaelen/wintermute/internal/npc/memory"
	"github.com/vaelen/wintermute/internal/store"
	"github.com/vaelen/wintermute/internal/world"
)

const (
	// retrievalK is the number of long-term memories fetched per turn.
	retrievalK = 5
	// retrievalThreshold is the minimum cosine similarity for inclusion.
	// Tuned against nomic-embed-text-class vectors; conservative enough
	// to keep unrelated topics from leaking into the system message.
	retrievalThreshold = 0.7
)

// dispatchTimeout bounds a single say→reply round-trip — embed for memory
// retrieval, plus the Chat itself. The dispatch context is derived from
// the registry's server-lifetime root context, not from any per-session
// context, so a speaker disconnecting mid-call does not cancel the in-
// flight reply. Sized generously enough to absorb a cold model load on
// the embed side followed by a cold model load on the chat side.
const dispatchTimeout = 120 * time.Second

// Registry holds every NPC in the world keyed by id. The world layer (or
// the session command loop) calls HandleSay after every `say`; the registry
// decides which NPCs should respond and dispatches the LLM round-trips on
// background goroutines.
//
// The registry stores a server-lifetime root context (set by Load and used
// as the parent of every dispatch's per-call timeout context). Callers must
// NOT pass a per-session context here — when a player disconnects, their
// session ctx cancels, and we don't want that to kill an NPC's in-flight
// reply on its way to other listeners in the room.
type Registry struct {
	world    *world.World
	db       *store.DB
	logger   *slog.Logger
	defaults config.LLMBackend
	rootCtx  context.Context

	mu   sync.RWMutex
	byID map[world.ObjectID]*NPC

	dispatchWG sync.WaitGroup

	// store is the shared long-term memory backend. Each NPC's State
	// references this same store; per-NPC scoping happens via NPCID
	// in queries.
	store memory.Store
	// memCancel cancels the per-NPC Worker goroutines started in
	// rebuild. Reset on every rebuild.
	memCancel context.CancelFunc
}

// Load builds a Registry by reading every npc_config row, opening the
// configured backend per NPC, and indexing the result. A missing object or
// failed Open is logged at WARN and the registry continues; the failed NPC
// is recorded with a nil llm so HandleSay silently skips it.
//
// serverCtx is retained as the parent of every dispatch's timeout context;
// it must be a server-lifetime context, not a per-session one. It is also
// used for the initial schema query.
func Load(serverCtx context.Context, db *store.DB, w *world.World, defaults config.LLMBackend, logger *slog.Logger) (*Registry, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if serverCtx == nil {
		serverCtx = context.Background()
	}
	r := &Registry{
		world:    w,
		db:       db,
		logger:   logger,
		defaults: defaults,
		rootCtx:  serverCtx,
		store:    memory.NewSQLStore(db),
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
		       c.max_context
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
		)
		if err := rows.Scan(&objID, &name, &persona, &backend, &optsRaw,
			&chatModel, &gateModel, &maxContext); err != nil {
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

		n := &NPC{
			ObjectID:    id,
			Name:        name,
			Persona:     persona,
			Backend:     backendName,
			BackendOpts: opts,
			Model:       chatModel,
			GateModel:   gateModel,
			MaxContext:  maxContext,
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

// HandleSay is the reactive entry point for a `say` event in roomID. It
// returns immediately; for each NPC matched by the addressing rules it
// dispatches an LLM round-trip on its own goroutine. The world is the only
// channel through which replies become visible — failed/empty replies do
// not broadcast anything.
//
// No ctx parameter on purpose: dispatch derives its own timeout context
// from the registry's server-lifetime root so a speaker disconnecting
// mid-call does not cancel the NPC's reply for everyone else in the room.
func (r *Registry) HandleSay(roomID world.RoomID, speakerID world.ObjectID, speakerName, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	npcs := r.NPCsInRoom(roomID)
	if len(npcs) == 0 {
		return
	}
	// Count entities via the unfiltered world view: an NPC present in the
	// room without an npc_config row still occupies the room as far as
	// "sole non-speaker entity" is concerned. Using the registry-filtered
	// list here would let rule (b) fire spuriously when an unregistered
	// NPC shares the room.
	players := r.world.PlayersInRoom(roomID)
	allNPCs := r.world.NPCsInRoom(roomID)
	otherEntities := otherEntityCount(speakerID, players, len(allNPCs))

	for _, n := range npcs {
		if !addressed(n, text, otherEntities) {
			continue
		}
		r.dispatchWG.Add(1)
		go func(n *NPC) {
			defer r.dispatchWG.Done()
			r.dispatch(n, speakerID, speakerName, text)
		}(n)
	}
}

// Wait blocks until every in-flight dispatch goroutine has finished.
// Callers MUST call this before closing the underlying DB or LLM
// resources at shutdown; otherwise an in-flight Chat that subsequently
// touches the world or DB could race with teardown.
func (r *Registry) Wait() {
	r.dispatchWG.Wait()
}

// Shutdown is the full teardown path: it waits for in-flight dispatch
// goroutines, then drains every per-NPC short-term buffer through the
// summarisation workers and waits for the workers to finish. ctx
// bounds how long Shutdown is willing to wait for the worker stop; a
// hung LLM backend cannot pin shutdown beyond that.
func (r *Registry) Shutdown(ctx context.Context) error {
	r.dispatchWG.Wait()

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
	// Cancel the worker context as a final safety net: by now every
	// per-NPC State.Stop has already asked its worker to drain and
	// exit, but a hung Embed call inside Run could still be holding on
	// to a goroutine. Cancelling the context unblocks the LLM call.
	if r.memCancel != nil {
		r.memCancel()
	}
	return firstErr
}

func (r *Registry) dispatch(n *NPC, speakerID world.ObjectID, speakerName, text string) {
	if n.llm == nil {
		// Load already logged a WARN when Open failed; re-logging per
		// say event would just be noise.
		r.logger.Debug("npc: no llm configured, ignoring say",
			"npc", n.Name, "backend", n.Backend)
		return
	}

	ctx, cancel := context.WithTimeout(r.rootCtx, dispatchTimeout)
	defer cancel()

	// Step 1: append the player's turn to the short-term buffer BEFORE
	// retrieval, so the embed query reflects "what was just said".
	if n.Memory != nil {
		n.Memory.Append(speakerID, memory.Turn{
			Speaker: speakerName,
			Text:    text,
			At:      time.Now(),
		})
	}

	// Step 2: pull relevant long-term memories. Failures here are non-
	// fatal: log and continue with no memory context.
	var retrieved []memory.Memory
	if n.Memory != nil {
		mems, err := n.Memory.Retrieve(ctx, speakerID, retrievalK, retrievalThreshold)
		if err != nil {
			r.logger.Warn("npc: memory retrieval failed",
				"npc", n.Name, "err", err)
		} else {
			retrieved = mems
		}
	}

	n.mu.Lock()
	userMsg := llm.Message{
		Role:    llm.RoleUser,
		Content: fmt.Sprintf("%s says: %s", speakerName, text),
	}
	// System: persona, then optional memory block, then history, then user.
	msgs := make([]llm.Message, 0, len(n.history)+3)
	msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Content: n.Persona})
	if len(retrieved) > 0 {
		msgs = append(msgs, llm.Message{
			Role:    llm.RoleSystem,
			Content: renderMemoryContext(retrieved),
		})
	}
	msgs = append(msgs, n.history...)
	msgs = append(msgs, userMsg)

	resp, err := n.llm.Chat(ctx, msgs, nil, llm.ChatOpts{Model: n.Model})
	if err != nil {
		n.mu.Unlock()
		r.logger.Warn("npc: llm chat failed",
			"npc", n.Name, "backend", n.Backend, "err", err)
		return
	}
	reply := strings.TrimSpace(resp.Content)
	// Empty replies are dropped silently: some backends (and personas)
	// produce blank output when an utterance was not actually meant for
	// the NPC; broadcasting nothing keeps the room quiet rather than
	// emitting a phantom "the bartender says: " line.
	if reply == "" {
		n.mu.Unlock()
		return
	}

	n.history = append(n.history, userMsg, llm.Message{
		Role:    llm.RoleAssistant,
		Content: reply,
	})
	if max := historyWindow * 2; len(n.history) > max {
		n.history = append([]llm.Message{}, n.history[len(n.history)-max:]...)
	}
	// Release n.mu before NPCSay: the broadcast flushes TCP writes to
	// every presence in the room, and a slow client must not stall the
	// next dispatch for this NPC. The mutex's only job is to serialize
	// Chat calls and protect history; both are done at this point.
	n.mu.Unlock()

	if err := r.world.NPCSay(n.ObjectID, reply); err != nil {
		r.logger.Warn("npc: broadcast failed",
			"npc", n.Name, "err", err)
	}

	// Step 4: record the NPC's response in short-term, and bump salience
	// on every memory we actually used. Both are best-effort: a failure
	// here should not affect future conversations beyond logging.
	if n.Memory != nil {
		n.Memory.Append(speakerID, memory.Turn{
			Speaker: "<npc>",
			Text:    reply,
			At:      time.Now(),
		})
		if len(retrieved) > 0 {
			n.Memory.BumpSalience(ctx, retrieved)
		}
	}
}

// onPlayerDetach ends the conversation for every NPC currently in the
// player's room. Called by the world layer after a Detach broadcast.
// Reason is currently informational; both quit and dropped trigger an
// identical conversation-end drain.
func (r *Registry) onPlayerDetach(playerID world.ObjectID, roomID world.RoomID, _ world.DisconnectReason) {
	for _, n := range r.NPCsInRoom(roomID) {
		if n.Memory == nil {
			continue
		}
		n.Memory.EndConversation(playerID)
	}
}

// onPlayerMove ends the conversation for every NPC the player left
// behind in the source room. The destination room's NPCs (if any) do
// not start a new conversation here — they wait for the player to
// actually address them via Say.
func (r *Registry) onPlayerMove(playerID world.ObjectID, from world.RoomID, _ world.RoomID, _ string) {
	for _, n := range r.NPCsInRoom(from) {
		if n.Memory == nil {
			continue
		}
		n.Memory.EndConversation(playerID)
	}
}

// renderMemoryContext formats retrieved memories as a system message that
// gets spliced between persona and history. Format kept simple and
// consistent so the LLM can scan it predictably.
func renderMemoryContext(mems []memory.Memory) string {
	var b strings.Builder
	b.WriteString("Relevant memories from past conversations:\n")
	for _, m := range mems {
		b.WriteString("- ")
		b.WriteString(m.Summary)
		b.WriteString("\n")
	}
	return b.String()
}

func addressed(n *NPC, text string, otherEntities int) bool {
	if nameInText(n.Name, text) {
		return true
	}
	return otherEntities == 1
}

// otherEntityCount counts all non-speaker conversational entities in the
// room — *awake* players plus NPCs. Rule (b) of the addressing rules
// fires only when this count is exactly 1, so the speaker is alone with
// one NPC. Sleeping bodies cannot participate in conversation and are
// deliberately excluded; otherwise a disconnected player leaving their
// body in the room would silently block every nearby NPC from replying
// to unaddressed `say`. npcCount must be the unfiltered world count of
// NPCs in the room, not the registry-filtered list.
func otherEntityCount(speakerID world.ObjectID, players []world.PresentPlayer, npcCount int) int {
	count := 0
	for _, p := range players {
		if p.ObjectID == speakerID {
			continue
		}
		if !p.Awake {
			continue
		}
		count++
	}
	count += npcCount
	return count
}

// nameInText reports whether any significant word of name (skipping common
// articles like "the"/"a"/"an") appears as a whole word in text, case-
// insensitively. "bartender" matches "the bartender"; "bartenderly" does
// not match "bartender"; punctuation around the word is ignored.
func nameInText(name, text string) bool {
	words := significantWords(name)
	if len(words) == 0 {
		return false
	}
	tokens := tokenize(text)
	if len(tokens) == 0 {
		return false
	}
	tokenSet := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		tokenSet[t] = struct{}{}
	}
	for _, w := range words {
		if _, ok := tokenSet[w]; ok {
			return true
		}
	}
	return false
}

var nameStopwords = map[string]struct{}{
	"the": {},
	"a":   {},
	"an":  {},
}

func significantWords(name string) []string {
	tokens := tokenize(name)
	var out []string
	for _, t := range tokens {
		if _, skip := nameStopwords[t]; skip {
			continue
		}
		out = append(out, t)
	}
	return out
}

func tokenize(s string) []string {
	var out []string
	var cur strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur.WriteRune(unicode.ToLower(r))
			continue
		}
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
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

