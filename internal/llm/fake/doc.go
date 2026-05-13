// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

// Package fake is a deterministic LLM backend used only by tests. The
// concrete registration is guarded by the "test" build tag so production
// builds do not include it. See docs/milestones/03-reactive-npcs.md.
package fake
