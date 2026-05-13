// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

// Package budget tracks per-NPC token consumption against minute/hour/day
// windows and exposes a non-blocking Allow / Record API. See
// docs/milestones/07-autonomous-npc-loop.md.
package budget
