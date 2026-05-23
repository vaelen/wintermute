// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

//go:build test

package fake

import (
	"context"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/vaelen/wintermute/internal/llm"
)

const (
	defaultEmbeddingDim = 16
	defaultReply        = "…"
)

func init() { llm.Register("fake", New) }

// modelResponses mirrors the global responses/keys/defaultReply triple,
// scoped to one ChatOpts.Model name so tests can wire up distinct
// gate/response routing under a single Fake instance.
type modelResponses struct {
	responses    map[string]string
	keys         []string // sorted, for stable substring scan
	defaultReply string
}

// Fake is a deterministic LLM backend for tests. The matching tables are
// fixed at construction; the call counter is the only mutable state and
// is guarded by mu.
type Fake struct {
	responses    map[string]string
	keys         []string // sorted, for stable substring scan
	defaultReply string
	embeddingDim int

	models map[string]*modelResponses

	mu           sync.Mutex
	callsByModel map[string]int
	// queues holds per-model FIFOs of pre-scripted responses. When a
	// queue for the active ChatOpts.Model is non-empty, Chat returns the
	// next queued response verbatim (no substring matching, no Usage
	// auto-population) and pops it. After the queue drains, the
	// substring/default path resumes.
	queues map[string][]llm.Response
}

// New builds a Fake from an opts map. Recognised keys:
//
//	responses     map[string]any  substring -> reply text
//	default       string          fallback reply when no substring matches
//	embedding_dim int             length of the synthetic embedding vector
//	models        map[string]any  per-ChatOpts.Model routing; each value is
//	                              itself map[string]any with optional keys
//	                              "responses" (map[string]any) and "default"
//	                              (string). When ChatOpts.Model matches a
//	                              key here, the per-model table is used in
//	                              place of the top-level responses/default.
func New(opts map[string]any) (llm.LLM, error) {
	f := &Fake{
		responses:    map[string]string{},
		defaultReply: defaultReply,
		embeddingDim: defaultEmbeddingDim,
		models:       map[string]*modelResponses{},
		callsByModel: map[string]int{},
		queues:       map[string][]llm.Response{},
	}

	if raw, ok := opts["responses"]; ok {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("fake: opts[\"responses\"] must be map[string]any, got %T", raw)
		}
		for k, v := range m {
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("fake: responses[%q] must be string, got %T", k, v)
			}
			f.responses[k] = s
		}
	}

	if raw, ok := opts["default"]; ok {
		s, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("fake: opts[\"default\"] must be string, got %T", raw)
		}
		f.defaultReply = s
	}

	if raw, ok := opts["embedding_dim"]; ok {
		dim, err := toPositiveInt(raw)
		if err != nil {
			return nil, fmt.Errorf("fake: opts[\"embedding_dim\"]: %w", err)
		}
		f.embeddingDim = dim
	}

	if raw, ok := opts["models"]; ok {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("fake: opts[\"models\"] must be map[string]any, got %T", raw)
		}
		for name, v := range m {
			cfg, ok := v.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("fake: models[%q] must be map[string]any, got %T", name, v)
			}
			mr := &modelResponses{
				responses:    map[string]string{},
				defaultReply: defaultReply,
			}
			if rraw, ok := cfg["responses"]; ok {
				rm, ok := rraw.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("fake: models[%q].responses must be map[string]any, got %T", name, rraw)
				}
				for k, vv := range rm {
					s, ok := vv.(string)
					if !ok {
						return nil, fmt.Errorf("fake: models[%q].responses[%q] must be string, got %T", name, k, vv)
					}
					mr.responses[k] = s
				}
			}
			if draw, ok := cfg["default"]; ok {
				s, ok := draw.(string)
				if !ok {
					return nil, fmt.Errorf("fake: models[%q].default must be string, got %T", name, draw)
				}
				mr.defaultReply = s
			}
			mr.keys = make([]string, 0, len(mr.responses))
			for k := range mr.responses {
				mr.keys = append(mr.keys, k)
			}
			sort.Strings(mr.keys)
			f.models[name] = mr
		}
	}

	f.keys = make([]string, 0, len(f.responses))
	for k := range f.responses {
		f.keys = append(f.keys, k)
	}
	sort.Strings(f.keys)

	return f, nil
}

func (f *Fake) Chat(_ context.Context, msgs []llm.Message, _ []llm.ToolDef, opts llm.ChatOpts) (llm.Response, error) {
	f.mu.Lock()
	if q := f.queues[opts.Model]; len(q) > 0 {
		resp := q[0]
		f.queues[opts.Model] = q[1:]
		f.callsByModel[opts.Model]++
		f.mu.Unlock()
		return resp, nil
	}
	f.mu.Unlock()

	last := lastUserContent(msgs)

	keys := f.keys
	responses := f.responses
	def := f.defaultReply
	if opts.Model != "" {
		if mr, ok := f.models[opts.Model]; ok {
			keys = mr.keys
			responses = mr.responses
			def = mr.defaultReply
		}
	}

	reply := def
	for _, k := range keys {
		if strings.Contains(last, k) {
			reply = responses[k]
			break
		}
	}

	f.mu.Lock()
	f.callsByModel[opts.Model]++
	f.mu.Unlock()

	return llm.Response{
		Content:  reply,
		UsageIn:  roughTokenCount(msgs),
		UsageOut: roughWordCount(reply),
	}, nil
}

// Queue enqueues a pre-scripted response to be returned by the next
// Chat call for the given model. Multiple Queue calls form a FIFO; the
// fake's substring/default path resumes once the queue drains.
// Usage fields are returned verbatim — the caller is responsible for
// setting UsageIn/UsageOut if budget-realism matters in the test.
func (f *Fake) Queue(model string, resp llm.Response) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queues[model] = append(f.queues[model], resp)
}

func (f *Fake) Embed(_ context.Context, text string) ([]float32, error) {
	out := make([]float32, f.embeddingDim)
	// Seed each dimension by hashing the text together with the dimension
	// index; this gives deterministic, input-sensitive vectors without any
	// risk of two distinct inputs colliding to the same vector for any
	// reasonable text.
	for i := range out {
		h := fnv.New64a()
		var idx [8]byte
		binary.LittleEndian.PutUint64(idx[:], uint64(i))
		_, _ = h.Write(idx[:])
		_, _ = h.Write([]byte(text))
		sum := h.Sum64()
		// Map to [-1, 1) using the high bits as a fraction.
		out[i] = float32(int64(sum)) / float32(math.MaxInt64)
	}
	return out, nil
}

// Calls returns a snapshot of how many times Chat has been called with
// ChatOpts.Model == model. The empty string counts calls made without a
// model name.
func (f *Fake) Calls(model string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.callsByModel[model]
}

func lastUserContent(msgs []llm.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleUser {
			return msgs[i].Content
		}
	}
	return ""
}

// roughTokenCount produces a non-zero coarse estimate of prompt size by
// counting whitespace-separated tokens across every message body. The
// budget machinery in Task 7 only needs a positive integer to record.
func roughTokenCount(msgs []llm.Message) int {
	n := 0
	for _, m := range msgs {
		n += roughWordCount(m.Content)
	}
	if n == 0 {
		n = 1
	}
	return n
}

func roughWordCount(s string) int {
	n := len(strings.Fields(s))
	if n == 0 {
		return 1
	}
	return n
}

func toPositiveInt(v any) (int, error) {
	var n int
	switch x := v.(type) {
	case int:
		n = x
	case int32:
		n = int(x)
	case int64:
		n = int(x)
	case float32:
		n = int(x)
	case float64:
		n = int(x)
	default:
		return 0, fmt.Errorf("expected integer, got %T", v)
	}
	if n <= 0 {
		return 0, fmt.Errorf("expected positive integer, got %d", n)
	}
	return n, nil
}
