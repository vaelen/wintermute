// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package engage

import "github.com/vaelen/wintermute/internal/world"

// Host kinds. The string values are persisted in object_engage.kind.
const (
	KindTerminal = "terminal"
	KindNPC      = "npc"
	KindCustom   = "custom"
)

// CloseReason explains why an engagement ended. It is passed to
// Handler.OnClose so handlers can decide whether to persist or discard
// in-flight state (e.g. M4 NPC summarisation runs on CloseVoluntary or
// CloseMovement but not CloseDisconnect).
type CloseReason int

// CloseReason values.
const (
	CloseVoluntary CloseReason = iota
	CloseMovement
	CloseForced
	CloseDisconnect
)

// String returns the canonical lowercase name. Used by slog.
func (r CloseReason) String() string {
	switch r {
	case CloseVoluntary:
		return "voluntary"
	case CloseMovement:
		return "movement"
	case CloseForced:
		return "forced"
	case CloseDisconnect:
		return "disconnect"
	default:
		return "unknown"
	}
}

// Policy controls what non-participants perceive of an engagement.
// The zero value is fully restrictive (nothing visible, nothing audible,
// not joinable) — explicitly opt in to permissive bits.
type Policy struct {
	VisibleActivity bool // open/close lines are broadcast to the room
	AudibleContent  bool // free input also goes to the room as speech
	Joinable        bool // non-participants may join via `join`
}

// Host describes an engageable object: its kind, custom verbs/messages,
// prompt override, and policy. The resolved Host (after applying
// kind-defaults) is what handlers and the registry see.
type Host struct {
	ObjectID       world.ObjectID
	Kind           string // KindTerminal | KindNPC | KindCustom
	EngageVerbs    []string
	DisengageVerbs []string
	EnterMsg       string // templated with {{player}}, {{host}}
	PresentMsg     string // shown in `look` annotation, e.g. "at the terminal"
	ExitMsg        string
	Prompt         string // optional override for the in-engagement prompt
	Policy         Policy
}
