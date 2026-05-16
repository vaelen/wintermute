// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package engage

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/vaelen/wintermute/internal/store"
	"github.com/vaelen/wintermute/internal/world"
)

// LoadHosts reads every row of object_engage, resolves kind-defaults,
// and returns the resulting hosts. Order is unspecified.
func LoadHosts(ctx context.Context, db *store.DB) ([]*Host, error) {
	rows, err := db.Read().QueryContext(ctx, `
		SELECT object_id, kind, engage_verbs, disengage_verbs,
		       COALESCE(enter_msg, ''), COALESCE(present_msg, ''),
		       COALESCE(exit_msg, ''), COALESCE(prompt, ''),
		       policy
		  FROM object_engage`)
	if err != nil {
		return nil, fmt.Errorf("engage: load hosts: %w", err)
	}
	defer rows.Close()

	var out []*Host
	for rows.Next() {
		var (
			id     int64
			kind   string
			ev, dv string
			em, pm string
			xm, pr string
			policy string
		)
		if err := rows.Scan(&id, &kind, &ev, &dv, &em, &pm, &xm, &pr, &policy); err != nil {
			return nil, fmt.Errorf("engage: scan host: %w", err)
		}
		h := &Host{ObjectID: world.ObjectID(id), Kind: kind}
		if err := decodeStrings(ev, &h.EngageVerbs); err != nil {
			return nil, fmt.Errorf("engage: bad engage_verbs for %d: %w", id, err)
		}
		if err := decodeStrings(dv, &h.DisengageVerbs); err != nil {
			return nil, fmt.Errorf("engage: bad disengage_verbs for %d: %w", id, err)
		}
		h.EnterMsg = em
		h.PresentMsg = pm
		h.ExitMsg = xm
		h.Prompt = pr
		if policy != "" && policy != "{}" {
			if err := json.Unmarshal([]byte(policy), &h.Policy); err != nil {
				return nil, fmt.Errorf("engage: bad policy for %d: %w", id, err)
			}
		}
		ApplyKindDefaults(h)
		out = append(out, h)
	}
	return out, rows.Err()
}

func decodeStrings(s string, dst *[]string) error {
	if s == "" || s == "[]" {
		return nil
	}
	return json.Unmarshal([]byte(s), dst)
}

// kindDefaults is the table of per-kind fallbacks.
var kindDefaults = map[string]Host{
	KindNPC: {
		EngageVerbs:    []string{"talk to", "address"},
		DisengageVerbs: []string{"leave", "goodbye"},
		EnterMsg:       "{{player}} turns to {{host}}.",
		PresentMsg:     "talking with {{host}}",
		ExitMsg:        "{{player}} turns away from {{host}}.",
	},
	KindTerminal: {
		EngageVerbs:    []string{"use", "sit at"},
		DisengageVerbs: []string{"stand up", "step away"},
		EnterMsg:       "{{player}} sits down at {{host}}.",
		PresentMsg:     "at {{host}}",
		ExitMsg:        "{{player}} steps away from {{host}}.",
		Prompt:         "terminal> ",
	},
}

// ApplyKindDefaults fills h's empty fields from the kind-default table.
// Called by LoadHosts; exported so admin Lua's set_engage helper can
// apply defaults when callers pass nil for individual fields.
func ApplyKindDefaults(h *Host) {
	d, ok := kindDefaults[h.Kind]
	if !ok {
		return
	}
	if len(h.EngageVerbs) == 0 {
		h.EngageVerbs = append([]string(nil), d.EngageVerbs...)
	}
	if len(h.DisengageVerbs) == 0 {
		h.DisengageVerbs = append([]string(nil), d.DisengageVerbs...)
	}
	if h.EnterMsg == "" {
		h.EnterMsg = d.EnterMsg
	}
	if h.PresentMsg == "" {
		h.PresentMsg = d.PresentMsg
	}
	if h.ExitMsg == "" {
		h.ExitMsg = d.ExitMsg
	}
	if h.Prompt == "" {
		h.Prompt = d.Prompt
	}
}
