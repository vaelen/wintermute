// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

// Package loop runs the per-NPC tick goroutine that subscribes to room
// events, debounces observations, gates with a small model, and dispatches
// the response model plus tool calls. See
// docs/milestones/07-autonomous-npc-loop.md.
package loop
