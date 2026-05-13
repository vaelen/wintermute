// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

// Package llm defines the engine's pluggable LLM interface and the backend
// registry. Backends register themselves in init() (see internal/llm/ollama)
// and are selected per-NPC by name. See docs/milestones/03-reactive-npcs.md.
package llm
