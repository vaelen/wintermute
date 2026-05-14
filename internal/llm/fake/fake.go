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

	"github.com/vaelen/wintermute/internal/llm"
)

const (
	defaultEmbeddingDim = 16
	defaultReply        = "…"
)

func init() { llm.Register("fake", New) }

// Fake is a deterministic LLM backend for tests. After construction it is
// read-only, which is sufficient for safe concurrent use.
type Fake struct {
	responses    map[string]string
	keys         []string // sorted, for stable substring scan
	defaultReply string
	embeddingDim int
}

// New builds a Fake from an opts map. Recognised keys:
//
//	responses     map[string]any  substring -> reply text
//	default       string          fallback reply when no substring matches
//	embedding_dim int             length of the synthetic embedding vector
func New(opts map[string]any) (llm.LLM, error) {
	f := &Fake{
		responses:    map[string]string{},
		defaultReply: defaultReply,
		embeddingDim: defaultEmbeddingDim,
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

	f.keys = make([]string, 0, len(f.responses))
	for k := range f.responses {
		f.keys = append(f.keys, k)
	}
	sort.Strings(f.keys)

	return f, nil
}

func (f *Fake) Chat(_ context.Context, msgs []llm.Message, _ []llm.ToolDef, _ llm.ChatOpts) (llm.Response, error) {
	last := lastUserContent(msgs)
	for _, k := range f.keys {
		if strings.Contains(last, k) {
			return llm.Response{Content: f.responses[k]}, nil
		}
	}
	return llm.Response{Content: f.defaultReply}, nil
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

func lastUserContent(msgs []llm.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleUser {
			return msgs[i].Content
		}
	}
	return ""
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
